package settings

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/store"
)

func newSvc(t *testing.T) (*Service, *store.KV) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "oc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	kv := store.NewKV(db)
	s, err := NewService(ctx, kv)
	if err != nil {
		t.Fatal(err)
	}
	return s, kv
}

func TestDefaults(t *testing.T) {
	d := Defaults()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if d.Version != CurrentVersion || d.Screen.KeepScreenshots || d.Screen.Target != ScreenActive ||
		d.Voice.Language != "auto" || d.Voice.TTSSpeed != 1.0 {
		t.Errorf("defaults: %+v", d)
	}
	s, _ := newSvc(t)
	if s.Get() != d {
		t.Errorf("fresh service = %+v", s.Get())
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Settings)
	}{
		{"hotkey mode", func(s *Settings) { s.Voice.HotkeyMode = "tap" }},
		{"capture moment", func(s *Settings) { s.Voice.CaptureMoment = "mid" }},
		{"speed low", func(s *Settings) { s.Voice.TTSSpeed = 0.4 }},
		{"speed high", func(s *Settings) { s.Voice.TTSSpeed = 1.6 }},
		{"language", func(s *Settings) { s.Voice.Language = "English!" }},
		{"screen target", func(s *Settings) { s.Screen.Target = "some" }},
		{"ui language", func(s *Settings) { s.UI.Language = "de" }},
	}
	s, _ := newSvc(t)
	before := s.Get()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.Update(context.Background(), tt.mut); err == nil {
				t.Error("expected error")
			}
			if s.Get() != before {
				t.Error("state changed after rejected update")
			}
		})
	}
	for _, ok := range []string{"auto", "ru", "en-US", "fil"} {
		d := Defaults()
		d.Voice.Language = ok
		if err := d.Validate(); err != nil {
			t.Errorf("language %q: %v", ok, err)
		}
	}
}

func TestUpdatePersistsAndNotifies(t *testing.T) {
	ctx := context.Background()
	s, kv := newSvc(t)
	ch, cancel := s.Subscribe()
	if err := s.Update(ctx, func(x *Settings) { x.Voice.HotkeyMode = HotkeyToggle; x.UI.Language = "en" }); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ch:
		if got.Voice.HotkeyMode != HotkeyToggle || got.UI.Language != "en" {
			t.Errorf("notified %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no notification")
	}
	// Reload from storage sees the change.
	s2, err := NewService(ctx, kv)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Get().Voice.HotkeyMode != HotkeyToggle {
		t.Errorf("not persisted: %+v", s2.Get())
	}
	// Slow reader: latest wins, writer never blocks.
	for _, sp := range []float64{0.6, 0.7, 0.8} {
		sp := sp
		if err := s.Update(ctx, func(x *Settings) { x.Voice.TTSSpeed = sp }); err != nil {
			t.Fatal(err)
		}
	}
	if got := <-ch; got.Voice.TTSSpeed != 0.8 {
		t.Errorf("latest = %v", got.Voice.TTSSpeed)
	}
	cancel()
	cancel() // idempotent
	if _, open := <-ch; open {
		t.Error("channel not closed")
	}
	if err := s.Update(ctx, func(x *Settings) { x.Memory.Enabled = false }); err != nil {
		t.Fatal(err)
	}
}

func TestUpgrade(t *testing.T) {
	ctx := context.Background()
	_, kv := newSvc(t)
	if err := kv.Set(ctx, kvKey, []byte(`{"version":1,"voice":{"tts_speed":1.2}}`)); err != nil {
		t.Fatal(err)
	}
	ups := map[int]func(*Settings){
		1: func(s *Settings) { s.UI.Language = "en" },
	}
	s, err := newService(ctx, kv, 2, ups)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Get()
	if got.Version != 2 || got.UI.Language != "en" || got.Voice.TTSSpeed != 1.2 || got.Voice.HotkeyMode != HotkeyHold {
		t.Errorf("upgraded %+v", got)
	}
	stored, _ := store.GetJSON[Settings](ctx, kv, kvKey)
	if stored.Version != 2 {
		t.Errorf("upgrade not persisted: %d", stored.Version)
	}
	if _, err := newService(ctx, kv, 1, nil); err == nil {
		t.Error("newer stored version should be rejected")
	}
}

func TestExportHasNoSecrets(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	if err := s.Update(ctx, func(x *Settings) {
		x.TTS.ProviderID = "tts1"
		x.TTS.APIKeyRef = "provider/tts1/api_key"
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := s.Export(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "provider/tts1/api_key") || strings.Contains(out, "api_key_ref") {
		t.Errorf("secret leaked: %s", out)
	}
	if !strings.Contains(out, "tts1") {
		t.Errorf("non-secret missing: %s", out)
	}
	if s.Get().TTS.APIKeyRef == "" {
		t.Error("export mutated live settings")
	}
}

func TestImportRoundTripIgnoresSecrets(t *testing.T) {
	ctx := context.Background()
	src, _ := newSvc(t)
	if err := src.Update(ctx, func(x *Settings) {
		x.Voice.HotkeyMode = HotkeyHandsfree
		x.Voice.TTSSpeed = 1.3
		x.Screen.KeepScreenshots = true
		x.TTS.Voice = "alloy"
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := src.Export(&buf); err != nil {
		t.Fatal(err)
	}

	dst, _ := newSvc(t)
	if err := dst.Update(ctx, func(x *Settings) { x.TTS.APIKeyRef = "local/ref" }); err != nil {
		t.Fatal(err)
	}
	// A hostile/hand-edited file tries to set a secret ref.
	doc := strings.Replace(buf.String(), `"voice": "alloy"`, `"voice": "alloy", "api_key_ref": "evil/ref"`, 1)
	if !strings.Contains(doc, "evil/ref") {
		t.Fatal("test setup")
	}
	if err := dst.Import(ctx, strings.NewReader(doc)); err != nil {
		t.Fatal(err)
	}
	got := dst.Get()
	if got.Voice.HotkeyMode != HotkeyHandsfree || got.Voice.TTSSpeed != 1.3 || !got.Screen.KeepScreenshots || got.TTS.Voice != "alloy" {
		t.Errorf("import lost data: %+v", got)
	}
	if got.TTS.APIKeyRef != "local/ref" {
		t.Errorf("secret ref = %q", got.TTS.APIKeyRef)
	}

	if err := dst.Import(ctx, strings.NewReader(`{"voice":{"tts_speed":9}}`)); err == nil {
		t.Error("invalid import accepted")
	}
	if err := dst.Import(ctx, strings.NewReader(`not json`)); err == nil {
		t.Error("garbage import accepted")
	}
	if dst.Get() != got {
		t.Error("failed import changed state")
	}
}

func TestSecretTagStripping(t *testing.T) {
	type inner struct {
		Token string `json:"token,omitempty" secret:"true"`
		Keep  string `json:"keep"`
	}
	type outer struct {
		In inner `json:"in"`
	}
	v := outer{In: inner{Token: "t", Keep: "k"}}
	stripSecrets(&v)
	if v.In.Token != "" || v.In.Keep != "k" {
		t.Errorf("%+v", v)
	}
	restoreSecrets(&v, &outer{In: inner{Token: "old", Keep: "x"}})
	if v.In.Token != "old" || v.In.Keep != "k" {
		t.Errorf("%+v", v)
	}
}
