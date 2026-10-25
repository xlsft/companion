package wizard

import (
	"testing"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/ui/i18n"
)

func byokHarness(t *testing.T, info *providers.Info, models []string, listErr error) *harness {
	t.Helper()
	h := newHarness(t)
	h.provider.info, h.provider.models, h.provider.listErr = info, models, listErr
	h.eventually("agent", func() bool { return h.w.scr == screenAgent && h.w.byok.loaded })
	return h
}

func (h *harness) tr(key string) string { return i18n.T(h.lang, key) }

// listed reports whether m holds a fetched list.
func (h *harness) listed(m *modelList) bool {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	return len(m.models) > 0
}

// pickModel chooses row i of a step's model dropdown, as a click would.
func (h *harness) pickModel(m *modelList, i int) {
	m.dd.SetOpen(true)
	h.frame()
	m.dd.Pick(i)
	h.frame()
	h.frame()
}

// A server of the user's own is asked for its models once the address
// rests; the list replaces the field, a pick fills it, and "Other model…"
// goes back to typing.
func TestModelListOffersFetchedModels(t *testing.T) {
	h := byokHarness(t, nil, []string{"llama3", "qwen3"}, nil)
	b := &h.w.byok
	h.pickAgent(customTile)
	b.baseURL.SetText("http://localhost:11434/v1")
	h.frame()
	if n := len(h.provider.listQueries()); n != 0 {
		t.Fatalf("fetched while typing: %d queries", n)
	}
	h.eventually("model list", func() bool { return h.shows(h.tr("wiz.byok.model.pick")) })
	q := h.provider.listQueries()
	if len(q) != 1 || q[0].Kind != llm.KindOpenAI || q[0].BaseURL != "http://localhost:11434/v1" || q[0].APIKey != "" {
		t.Fatalf("queries = %+v", q)
	}

	b.dirty = false
	h.pickModel(&b.models, 1)
	if b.model.Text() != "qwen3" || !b.dirty || !h.shows("qwen3") || h.shows(h.tr("wiz.byok.model.pick")) {
		t.Errorf("after pick: model %q dirty %v", b.model.Text(), b.dirty)
	}

	h.pickModel(&b.models, 2) // Other model…
	if !b.models.typing || b.model.Text() != "qwen3" || !h.has(ActModelList) {
		t.Fatalf("Other model: typing %v model %q", b.models.typing, b.model.Text())
	}
	h.click(ActModelList)
	h.frame()
	if b.models.typing || !h.shows(h.tr("wiz.byok.model.other")) {
		t.Errorf("back to the list: typing %v", b.models.typing)
	}

	// The form saves itself, and that asks again: the endpoint may answer
	// differently now.
	b.models.dd.SetOpen(false)
	h.eventually("listed after save", func() bool { return len(h.provider.listQueries()) == 2 })
}

// Without a list the field stays as it was; a failure says so quietly.
func TestModelListFallsBackToTyping(t *testing.T) {
	tests := []struct {
		name     string
		models   []string
		err      error
		wantHint bool
	}{
		{"failure", nil, llm.ErrAuth, true},
		{"no listing", nil, &llm.ErrProvider{Status: 404}, true},
		{"empty list", []string{}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := byokHarness(t, nil, tt.models, tt.err)
			b := &h.w.byok
			h.frame()
			h.pickAgent("ollama")
			h.eventually("fetched", func() bool {
				h.w.mu.Lock()
				defer h.w.mu.Unlock()
				return len(h.provider.listQueries()) > 0 && !b.models.loading
			})
			if h.shows(h.tr("wiz.byok.model.pick")) || h.listed(&b.models) {
				t.Error("dropdown without a list")
			}
			if h.shows(h.tr("wiz.byok.model.failed")) != tt.wantHint {
				t.Errorf("hint shown = %v, want %v", !tt.wantHint, tt.wantHint)
			}
			b.model.SetText("typed")
			h.frame()
			if b.model.Text() != "typed" || !h.shows(h.tr("wiz.byok.model")) {
				t.Error("model field not usable")
			}
		})
	}
}

// A saved provider lists with its stored key, which the UI never holds:
// the query names the provider instead. A service the key was not saved
// for waits for its own key.
func TestModelListUsesStoredKey(t *testing.T) {
	info := &providers.Info{ID: "prov_1", HasKey: true,
		Input: providers.Input{Kind: llm.KindOpenAI, BaseURL: "https://api.openai.com/v1", Model: "gpt-x"}}
	h := byokHarness(t, info, []string{"gpt-a", "gpt-x"}, nil)
	b := &h.w.byok
	h.eventually("model list", func() bool { return h.listed(&b.models) })
	h.frame()
	q := h.provider.listQueries()
	if len(q) != 1 || q[0].ProviderID != "prov_1" || q[0].APIKey != "" {
		t.Fatalf("queries = %+v", q)
	}
	if b.models.typing || !h.shows("gpt-x") || h.shows(h.tr("wiz.byok.model.pick")) {
		t.Errorf("saved model not selected in the list")
	}

	h.frame()
	h.pickAgent("openrouter")
	h.frame()
	h.frame()
	if h.listed(&b.models) || len(h.provider.listQueries()) != 1 {
		t.Fatalf("listed another service without its key: %+v", h.provider.listQueries())
	}
	b.replacing = true
	b.key.SetText("sk-or-typed")
	h.eventually("listed with the typed key", func() bool { return len(h.provider.listQueries()) == 2 })
	if q := h.provider.listQueries()[1]; q.APIKey != "sk-or-typed" || q.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("query = %+v", q)
	}
}

// A saved model the server does not list stays as typed.
func TestModelListKeepsUnlistedModel(t *testing.T) {
	info := &providers.Info{ID: "prov_1", Input: providers.Input{Kind: llm.KindOpenAI, BaseURL: "http://127.0.0.1:1234/v1", Model: "custom-1"}}
	h := byokHarness(t, info, []string{"other"}, nil)
	b := &h.w.byok
	h.eventually("model list", func() bool { return h.listed(&b.models) })
	h.frame()
	if !b.models.typing || b.model.Text() != "custom-1" || !h.has(ActModelList) {
		t.Errorf("typing %v model %q", b.models.typing, b.model.Text())
	}
}

// The voice step lists the models of a compatible speech server.
func TestTTSModelList(t *testing.T) {
	h := newHarness(t)
	h.tts.models = []string{"kokoro", "tts-1"}
	h.goScreen(screenVoice)
	ts := &h.w.tts
	if h.listed(&ts.models) {
		t.Fatal("listed before a server was chosen")
	}
	h.frame()
	h.pickVoice("kokoro")
	h.eventually("model list", func() bool { return h.shows(h.tr("wiz.byok.model.pick")) })
	ts.dirty = false
	h.pickModel(&ts.models, 0)
	if ts.model.Text() != "kokoro" || !ts.dirty {
		t.Errorf("model %q dirty %v", ts.model.Text(), ts.dirty)
	}
	h.tts.mu.Lock()
	q := h.tts.queries
	h.tts.mu.Unlock()
	if len(q) != 1 || q[0].BaseURL != "http://localhost:8880/v1" || q[0].APIKey != "" {
		t.Errorf("queries = %+v", q)
	}
}
