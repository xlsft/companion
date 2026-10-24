// Package providers manages user-configured LLM endpoints (OC-24): CRUD
// over store.ProviderRepo with API keys in the secret store, and a
// step-by-step connection test for the settings UI and setup wizard.
package providers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/xlsft/openclick/internal/llm"
	"github.com/xlsft/openclick/internal/llm/probe"
	"github.com/xlsft/openclick/internal/secrets"
	"github.com/xlsft/openclick/internal/store"
	"github.com/xlsft/openclick/pkg/contracts"
)

// ErrNotFound is returned for unknown provider ids.
var ErrNotFound = store.ErrNotFound

// Input is what the UI submits to create or edit a provider.
type Input struct {
	Name    string
	Kind    llm.Kind
	BaseURL string
	Model   string
	Headers map[string]string
	Timeout time.Duration
	Retry   llm.RetryPolicy
	// Overrides are manual capability settings for non-standard endpoints.
	Overrides map[llm.Capability]bool
	// APIKey is write-only. On Update an empty value keeps the stored key.
	APIKey string
	// ClearKey removes the stored key on Update (local servers without key).
	ClearKey bool
}

// Info is a provider as shown to the UI. It never carries the key.
type Info struct {
	ID        string
	Input     Input // APIKey and ClearKey are always empty
	HasKey    bool
	Stored    probe.Stored
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Service is the provider backend. Safe for concurrent use.
type Service struct {
	repo    *store.ProviderRepo
	secrets contracts.SecretStore
	client  *http.Client // nil: default client with system roots
}

// New builds the service. client is used for every request, including the
// connection check; nil means a default client.
func New(repo *store.ProviderRepo, sec contracts.SecretStore, client *http.Client) *Service {
	return &Service{repo: repo, secrets: sec, client: client}
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the system RNG failing is unrecoverable
	}
	return "prov_" + hex.EncodeToString(b[:])
}

func (in Input) config(id string, ref contracts.SecretRef) llm.ProviderConfig {
	return llm.ProviderConfig{ID: id, Name: in.Name, Kind: in.Kind, BaseURL: in.BaseURL, Model: in.Model,
		Headers: in.Headers, Timeout: in.Timeout, Retry: in.Retry, Capabilities: in.Overrides, SecretRef: ref}
}

// Create validates in, stores the key and the record and returns the new
// provider.
func (s *Service) Create(ctx context.Context, in Input) (*Info, error) {
	id := newID()
	ref := contracts.SecretRef("")
	if in.APIKey != "" {
		ref = secrets.NewRef("provider", id, "api_key")
	}
	if err := in.config(id, ref).Validate(); err != nil {
		return nil, err
	}
	rec := &store.Provider{ID: id, Name: in.Name, Kind: string(in.Kind), BaseURL: in.BaseURL, Model: in.Model,
		Headers: in.Headers, TimeoutMS: int(in.Timeout / time.Millisecond), SecretRef: ref}
	var err error
	if rec.Retry, err = json.Marshal(in.Retry); err != nil {
		return nil, err
	}
	if rec.Capabilities, err = (probe.Stored{Overrides: in.Overrides}).Encode(); err != nil {
		return nil, err
	}
	if ref != "" {
		if err := s.secrets.Put(ctx, ref, []byte(in.APIKey)); err != nil {
			return nil, err
		}
	}
	if err := s.repo.Create(ctx, rec); err != nil {
		if ref != "" {
			_ = s.secrets.Delete(ctx, ref)
		}
		return nil, err
	}
	return s.info(rec), nil
}

// Update replaces the editable fields of provider id. The existing key is
// kept unless in.APIKey is set or in.ClearKey is true. Detection results
// are dropped when the endpoint or model changes, since they described the
// old one.
func (s *Service) Update(ctx context.Context, id string, in Input) (*Info, error) {
	rec, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	ref := rec.SecretRef
	if in.APIKey != "" && ref == "" {
		ref = secrets.NewRef("provider", id, "api_key")
	}
	if in.ClearKey && in.APIKey == "" {
		ref = ""
	}
	if err := in.config(id, ref).Validate(); err != nil {
		return nil, err
	}
	st, _ := probe.DecodeStored(rec.Capabilities)
	if rec.BaseURL != in.BaseURL || rec.Model != in.Model || string(rec.Kind) != string(in.Kind) {
		st.Detected = nil
	}
	st.Overrides = in.Overrides
	old := rec.SecretRef
	rec.Name, rec.Kind, rec.BaseURL, rec.Model = in.Name, string(in.Kind), in.BaseURL, in.Model
	rec.Headers, rec.TimeoutMS, rec.SecretRef = in.Headers, int(in.Timeout/time.Millisecond), ref
	if rec.Retry, err = json.Marshal(in.Retry); err != nil {
		return nil, err
	}
	if rec.Capabilities, err = st.Encode(); err != nil {
		return nil, err
	}
	if in.APIKey != "" {
		if err := s.secrets.Put(ctx, ref, []byte(in.APIKey)); err != nil {
			return nil, err
		}
	}
	if err := s.repo.Update(ctx, rec); err != nil {
		return nil, err
	}
	if old != "" && ref == "" {
		if err := s.secrets.Delete(ctx, old); err != nil && !errors.Is(err, contracts.ErrSecretNotFound) {
			return nil, err
		}
	}
	return s.info(rec), nil
}

// Delete removes the provider and its key.
func (s *Service) Delete(ctx context.Context, id string) error {
	rec, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	if rec.SecretRef != "" {
		if err := s.secrets.Delete(ctx, rec.SecretRef); err != nil && !errors.Is(err, contracts.ErrSecretNotFound) {
			return fmt.Errorf("providers: provider deleted but key removal failed: %w", err)
		}
	}
	return nil
}

func (s *Service) Get(ctx context.Context, id string) (*Info, error) {
	rec, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.info(rec), nil
}

func (s *Service) List(ctx context.Context) ([]*Info, error) {
	recs, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Info, len(recs))
	for i, r := range recs {
		out[i] = s.info(r)
	}
	return out, nil
}

func (s *Service) info(rec *store.Provider) *Info {
	in := Input{Name: rec.Name, Kind: llm.Kind(rec.Kind), BaseURL: rec.BaseURL, Model: rec.Model,
		Headers: rec.Headers, Timeout: time.Duration(rec.TimeoutMS) * time.Millisecond}
	_ = json.Unmarshal(rec.Retry, &in.Retry)
	st, _ := probe.DecodeStored(rec.Capabilities)
	in.Overrides = st.Overrides
	return &Info{ID: rec.ID, Input: in, HasKey: rec.SecretRef != "", Stored: st,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt}
}

// Config returns the llm config of provider id, including overrides.
func (s *Service) Config(ctx context.Context, id string) (llm.ProviderConfig, error) {
	rec, err := s.repo.Get(ctx, id)
	if err != nil {
		return llm.ProviderConfig{}, err
	}
	return s.config(rec), nil
}

func (s *Service) config(rec *store.Provider) llm.ProviderConfig {
	i := s.info(rec)
	return i.Input.config(rec.ID, rec.SecretRef)
}

// Capabilities returns what the provider can do: adapter defaults, then
// detection results, then manual overrides.
func (s *Service) Capabilities(ctx context.Context, id string, base llm.Capabilities) (llm.Capabilities, error) {
	rec, err := s.repo.Get(ctx, id)
	if err != nil {
		return base, err
	}
	st, err := probe.DecodeStored(rec.Capabilities)
	if err != nil {
		return base, err
	}
	return st.Apply(base), nil
}

func (s *Service) key(ctx context.Context, ref contracts.SecretRef) (string, error) {
	if ref == "" {
		return "", nil
	}
	b, err := s.secrets.Get(ctx, ref)
	return string(b), err
}

// Build returns a ready provider (with the configured retry policy) for
// id, for the profile resolver. The stored detection is not applied to the
// adapter's own capability set; callers check Capabilities themselves.
func (s *Service) Build(ctx context.Context, id string) (llm.Provider, llm.ProviderConfig, error) {
	rec, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, llm.ProviderConfig{}, err
	}
	cfg := s.config(rec)
	key, err := s.key(ctx, rec.SecretRef)
	if err != nil {
		return nil, cfg, err
	}
	p, err := llm.New(cfg, key, s.client)
	return p, cfg, err
}
