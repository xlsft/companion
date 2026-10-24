package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xlsft/openclick/internal/llm"
	"github.com/xlsft/openclick/internal/store"
)

// Stored is the JSON kept in providers.capabilities: the user's manual
// overrides next to the last detection run.
type Stored struct {
	Overrides map[llm.Capability]bool `json:"overrides,omitempty"`
	Detected  *Detected               `json:"detected,omitempty"`
}

// Detected is a persisted probe run.
type Detected struct {
	At time.Time `json:"at"`
	// Caps holds only conclusive answers for llm capabilities; a check that
	// failed for a transient reason (auth, network, timeout, rate limit) or
	// was skipped says nothing about the model and is left out.
	Caps    map[llm.Capability]bool `json:"caps"`
	Results []Result                `json:"results"`
}

// DecodeStored parses the column; empty and "{}" give the zero value.
func DecodeStored(raw json.RawMessage) (Stored, error) {
	var s Stored
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return Stored{}, fmt.Errorf("probe: decode stored capabilities: %w", err)
	}
	return s, nil
}

// Encode renders the column value.
func (s Stored) Encode() (json.RawMessage, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("probe: encode stored capabilities: %w", err)
	}
	return b, nil
}

// Detect condenses a report into its persistable form.
func (r Report) Detect() Detected {
	d := Detected{At: r.At, Caps: map[llm.Capability]bool{}, Results: r.Results}
	for _, res := range r.Results {
		if res.Capability != llm.CapVision && res.Capability != llm.CapTools {
			continue
		}
		if res.Skipped || (!res.OK && transient(res.ErrCode)) {
			continue
		}
		d.Caps[res.Capability] = res.OK
	}
	return d
}

func transient(code string) bool {
	switch code {
	case "auth", "network", "timeout", "rate_limit", "canceled":
		return true
	}
	return false
}

// Apply returns base with the detection and then the manual overrides
// layered on top: overrides always win.
func (s Stored) Apply(base llm.Capabilities) llm.Capabilities {
	if s.Detected != nil {
		base = base.With(s.Detected.Caps)
	}
	return base.With(s.Overrides)
}

// Save merges rep into the stored capabilities of provider id, keeping the
// manual overrides, and marks the report's results with them.
func Save(ctx context.Context, repo *store.ProviderRepo, id string, rep Report) error {
	p, err := repo.Get(ctx, id)
	if err != nil {
		return err
	}
	s, err := DecodeStored(p.Capabilities)
	if err != nil {
		// A corrupt column must not block re-detection; start over.
		s = Stored{}
	}
	d := rep.Detect()
	s.Detected = &d
	if p.Capabilities, err = s.Encode(); err != nil {
		return err
	}
	return repo.Update(ctx, p)
}

// Load returns the stored capabilities of provider id.
func Load(ctx context.Context, repo *store.ProviderRepo, id string) (Stored, error) {
	p, err := repo.Get(ctx, id)
	if err != nil {
		return Stored{}, err
	}
	return DecodeStored(p.Capabilities)
}
