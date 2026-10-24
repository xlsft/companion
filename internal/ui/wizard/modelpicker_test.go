package wizard

import (
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/theme"
)

func TestModelOptions(t *testing.T) {
	cat, err := models.BuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		id        string
		rec, inst bool
		tags      []string
		meta      string
	}{
		{"parakeet-tdt-0.6b-v3-int8", true, false, []string{"Offline", "Recommended"}, "465 MB"},
		{"zipformer-streaming-en-int8", false, true, []string{"Streaming", "Downloaded"}, "296 MB"},
		{"whisper-small-q5_1", false, false, []string{"Fallback"}, "181 MB"},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			m, ok := cat.Find(tt.id)
			if !ok {
				t.Fatal("not in catalogue")
			}
			o := ModelOption(i18n.EN, m, tt.rec, tt.inst)
			var tags []string
			for _, tg := range o.Tags {
				tags = append(tags, tg.Text)
			}
			if o.Title != m.Name || strings.Join(tags, ",") != strings.Join(tt.tags, ",") || o.Meta != tt.meta || o.Desc == "" {
				t.Fatalf("option %+v", o)
			}
		})
	}
	// Hidden whisper models stay out unless installed or kept.
	picked := PickerModels(cat, map[string]bool{"whisper-tiny": true}, "whisper-medium-q5_0")
	ids := map[string]bool{}
	for _, m := range picked {
		ids[m.ID] = true
	}
	if !ids["parakeet-tdt-0.6b-v3-int8"] || !ids["whisper-tiny"] || !ids["whisper-medium-q5_0"] || ids["whisper-medium"] {
		t.Fatalf("picker models %v", ids)
	}
	if picked[0].Family != models.FamilySherpa {
		t.Fatalf("primary models must come first, got %s", picked[0].ID)
	}
}

// The Settings picker downloads the chosen model and then marks it in use.
func TestModelPicker(t *testing.T) {
	stt := newFakeSTT()
	p := &ModelPicker{Backend: stt, Language: func() string { return "ja" }}
	th := theme.New(false, 1)
	r := new(input.Router)
	draw := func() []string {
		var texts []string
		ops := new(op.Ops)
		gtx := layout.Context{Ops: ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Now: time.Now(), Source: r.Source(),
			Constraints: layout.Exact(image.Pt(700, 600))}
		p.Layout(th, i18n.EN, &texts)(gtx)
		r.Frame(ops)
		return texts
	}
	eventually := func(what string, cond func([]string) bool) []string {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			texts := draw()
			if cond(texts) {
				return texts
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %q", what, texts)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	has := func(sub string) func([]string) bool {
		return func(texts []string) bool { return strings.Contains(strings.Join(texts, "|"), sub) }
	}
	eventually("recommendation for Japanese", has("SenseVoice Small"))
	texts := draw()
	if !has("Recommended")(texts) || !has("Download · ")(texts) {
		t.Fatalf("texts %q", texts)
	}
	p.action.Click()
	eventually("in use", has("In use"))
	if inst, _ := stt.Installed(); len(inst) != 1 || inst[0].ID != "sense-voice-small-int8" {
		t.Fatalf("installed %+v", inst)
	}
	// Picking another model offers it for download again.
	p.mu.Lock()
	cat := p.cat
	p.mu.Unlock()
	for i, m := range cat {
		if m.ID == "parakeet-tdt-0.6b-v3-int8" {
			p.dd.Pick(i)
		}
	}
	eventually("other model", func(texts []string) bool { return has("Parakeet TDT v3")(texts) && has("Download · ")(texts) })
}
