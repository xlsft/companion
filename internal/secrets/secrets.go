// Package secrets implements contracts.SecretStore over the OS keyring
// (Keychain, Credential Manager, Secret Service).
package secrets

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/xlsft/companion/pkg/contracts"
	"github.com/zalando/go-keyring"
)

// Service is the keyring service name all refs live under.
const Service = "companion"

// legacyService is where versions before 0.1.0 kept the refs, under the
// project's old name; Get moves what it finds there to Service.
const legacyService = "openclick"

// NewRef joins parts into a SecretRef, e.g. NewRef("provider", id, "api_key").
func NewRef(parts ...string) contracts.SecretRef {
	return contracts.SecretRef(strings.Join(parts, "/"))
}

// Keyring is the OS-backed store.
type Keyring struct{}

// NewKeyring returns the OS keyring store.
func NewKeyring() *Keyring { return &Keyring{} }

var _ contracts.SecretStore = (*Keyring)(nil)

// wrap emits a fixed message instead of the backend error text: backends may
// echo request data, and a secret value must never reach an error string.
func wrap(op string, ref contracts.SecretRef, err error) error {
	if errors.Is(err, keyring.ErrNotFound) {
		return contracts.ErrSecretNotFound
	}
	reason := "keyring backend error"
	if errors.Is(err, keyring.ErrSetDataTooBig) {
		reason = "value too big"
	}
	return errors.New("secrets: " + op + " " + string(ref) + " failed: " + reason)
}

func (Keyring) Put(ctx context.Context, ref contracts.SecretRef, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := keyring.Set(Service, string(ref), string(value)); err != nil {
		return wrap("put", ref, err)
	}
	return nil
}

func (Keyring) Get(ctx context.Context, ref contracts.SecretRef) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v, err := keyring.Get(Service, string(ref))
	if errors.Is(err, keyring.ErrNotFound) {
		v, err = adopt(ref)
	}
	if err != nil {
		return nil, wrap("get", ref, err)
	}
	return []byte(v), nil
}

// adopt moves ref from legacyService to Service. A failed move still
// returns the value: the key works now and moves on the next read.
func adopt(ref contracts.SecretRef) (string, error) {
	v, err := keyring.Get(legacyService, string(ref))
	if err != nil {
		return "", err
	}
	if keyring.Set(Service, string(ref), v) == nil {
		_ = keyring.Delete(legacyService, string(ref))
	}
	return v, nil
}

func (Keyring) Delete(ctx context.Context, ref contracts.SecretRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := keyring.Delete(Service, string(ref))
	// A ref not read since the rename still sits under the old name.
	legacy := keyring.Delete(legacyService, string(ref))
	if errors.Is(err, keyring.ErrNotFound) && legacy == nil {
		err = nil
	}
	if err != nil {
		return wrap("delete", ref, err)
	}
	return nil
}

// Memory is an in-process store for tests.
type Memory struct {
	mu sync.Mutex
	m  map[contracts.SecretRef][]byte
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{m: map[contracts.SecretRef][]byte{}} }

var _ contracts.SecretStore = (*Memory)(nil)

func (s *Memory) Put(ctx context.Context, ref contracts.SecretRef, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[ref] = append([]byte(nil), value...)
	return nil
}

func (s *Memory) Get(ctx context.Context, ref contracts.SecretRef) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[ref]
	if !ok {
		return nil, contracts.ErrSecretNotFound
	}
	return append([]byte(nil), v...), nil
}

func (s *Memory) Delete(ctx context.Context, ref contracts.SecretRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[ref]; !ok {
		return contracts.ErrSecretNotFound
	}
	delete(s.m, ref)
	return nil
}
