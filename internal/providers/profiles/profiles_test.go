package profiles

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/xlsft/openclick/internal/llm"
	"github.com/xlsft/openclick/internal/llm/probe"
	"github.com/xlsft/openclick/internal/providers"
	"github.com/xlsft/openclick/internal/secrets"
	"github.com/xlsft/openclick/internal/store"
)

type env struct {
	m   *Manager
	svc *providers.Service
	db  *store.DB
}

func setup(t *testing.T) env {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "oc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc := providers.New(store.NewProviderRepo(db), secrets.NewMemory(), nil)
	return env{New(db, svc), svc, db}
}

func addProvider(t *testing.T, e env, name, model string) string {
	t.Helper()
	info, err := e.svc.Create(context.Background(), providers.Input{Name: name, Kind: llm.KindOpenAI,
		BaseURL: "https://api.example.com/v1", Model: model, APIKey: "k", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

func detect(t *testing.T, e env, id string, vision bool) {
	t.Helper()
	rep := probe.Report{At: time.Now(), Results: []probe.Result{{Capability: llm.CapVision, OK: vision}}}
	if err := probe.Save(context.Background(), store.NewProviderRepo(e.db), id, rep); err != nil {
		t.Fatal(err)
	}
}

func TestAssignAndResolve(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	a := addProvider(t, e, "a", "model-a")
	b := addProvider(t, e, "b", "model-b")

	if _, _, err := e.m.Resolve(ctx, RoleTalk); !errors.Is(err, ErrNotAssigned) {
		t.Fatalf("unassigned err = %v", err)
	}
	if err := e.m.Save(ctx, Profile{ID: ProfileFast, Name: "Fast", ProviderID: a, ModelOverride: "mini",
		Policy: llm.ModelPolicy{MaxOutputTokens: 100}}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Save(ctx, Profile{ID: ProfileDeep, Name: "Deep", ProviderID: b}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Assign(ctx, RoleTalk, ProfileFast); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Assign(ctx, RoleAgent, ProfileDeep); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Assign(ctx, "nope", ProfileDeep); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad role err = %v", err)
	}
	if err := e.m.Assign(ctx, RoleVision, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing profile err = %v", err)
	}

	_, prof, err := e.m.Resolve(ctx, RoleTalk)
	if err != nil || prof.ProviderID != a || prof.ModelOverride != "mini" || prof.Policy.MaxOutputTokens != 100 {
		t.Fatalf("talk = %+v, %v", prof, err)
	}
	if _, prof, err = e.m.Resolve(ctx, RoleAgent); err != nil || prof.ProviderID != b {
		t.Fatalf("agent = %+v, %v", prof, err)
	}
	as, _ := e.m.Assignments(ctx)
	if len(as) != 2 || as[RoleTalk] != ProfileFast {
		t.Fatalf("assignments = %v", as)
	}
}

func TestMissingCapabilityNoFallback(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	noVision := addProvider(t, e, "novision", "m1")
	withVision := addProvider(t, e, "vision", "m2")
	detect(t, e, noVision, false)
	detect(t, e, withVision, true)
	e.m.Save(ctx, Profile{ID: "p1", Name: "P1", ProviderID: noVision})
	e.m.Save(ctx, Profile{ID: "p2", Name: "P2", ProviderID: withVision})
	e.m.Assign(ctx, RoleVision, "p1")

	p, prof, err := e.m.Resolve(ctx, RoleVision)
	var uc *llm.ErrUnsupportedCapability
	if !errors.As(err, &uc) || uc.Cap != llm.CapVision || p != nil {
		t.Fatalf("err = %v, provider %v", err, p)
	}
	if prof.ProviderID != noVision {
		t.Errorf("profile = %+v", prof)
	}
	// A capable provider exists, yet it must not be used.
	if !errors.Is(err, &llm.ErrUnsupportedCapability{}) {
		t.Error("errors.Is must match")
	}

	// Manual override wins over detection.
	info, _ := e.svc.Get(ctx, noVision)
	in := info.Input
	in.Overrides = map[llm.Capability]bool{llm.CapVision: true}
	if _, err := e.svc.Update(ctx, noVision, in); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.m.Resolve(ctx, RoleVision); err != nil {
		t.Fatalf("override ignored: %v", err)
	}
}

func TestProfileChangeAffectsOnlyNewResolves(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	a := addProvider(t, e, "a", "model-a")
	e.m.Save(ctx, Profile{ID: "p", Name: "P", ProviderID: a, ModelOverride: "one"})
	e.m.Assign(ctx, RoleTalk, "p")
	_, before, _ := e.m.Resolve(ctx, RoleTalk)

	e.m.Save(ctx, Profile{ID: "p", Name: "P", ProviderID: a, ModelOverride: "two"})
	_, after, _ := e.m.Resolve(ctx, RoleTalk)
	if before.ModelOverride != "one" || after.ModelOverride != "two" {
		t.Fatalf("before %q after %q", before.ModelOverride, after.ModelOverride)
	}
}

func TestDeleteProviderUnassignsRole(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	a := addProvider(t, e, "a", "m")
	e.m.Save(ctx, Profile{ID: "p", Name: "P", ProviderID: a})
	e.m.Assign(ctx, RoleTalk, "p")
	if err := e.svc.Delete(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.m.Resolve(ctx, RoleTalk); !errors.Is(err, ErrNotAssigned) {
		t.Fatalf("err = %v", err)
	}
}
