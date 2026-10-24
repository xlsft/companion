package sherpa

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xlsft/companion/internal/stt/engine"
)

func TestNormalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"HELLO WORLD", "Hello world"},
		{"  AND SO MY FELLOW AMERICANS  ", "And so my fellow americans"},
		{"I THINK I'M HERE", "I think I'm here"},
		{"Hello, world.", "Hello, world."},
		{"你好世界", "你好世界"},
		{"ПРИВЕТ МИР", "Привет мир"},
	}
	for _, tt := range tests {
		if got := normalize(tt.in); got != tt.want {
			t.Errorf("normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSenseVoiceLang(t *testing.T) {
	tests := []struct{ tag, want, hint string }{
		{"<|en|>", "en", "auto"},
		{"<|zh|>", "zh", "auto"},
		{"<|nospeech|>", "", "auto"},
		{"", "", "auto"},
	}
	for _, tt := range tests {
		if got := senseVoiceLang(tt.tag); got != tt.want {
			t.Errorf("senseVoiceLang(%q) = %q, want %q", tt.tag, got, tt.want)
		}
	}
	for lang, want := range map[string]string{"ja": "ja", "yue": "yue", "ru": "auto", "auto": "auto", "": "auto"} {
		if got := senseVoiceHint(lang); got != want {
			t.Errorf("senseVoiceHint(%q) = %q, want %q", lang, got, want)
		}
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		dir   string
		files []string
		ok    bool
		typ   string
		kind  string
		enc   string
	}{
		{"sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8", []string{"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"}, true, TypeNemoTransducer, engine.KindOffline, "encoder.int8.onnx"},
		{"sherpa-onnx-streaming-zipformer-en-2023-06-26", []string{"encoder-epoch-99-avg-1-chunk-16-left-128.onnx", "encoder-epoch-99-avg-1-chunk-16-left-128.int8.onnx", "decoder-epoch-99-avg-1-chunk-16-left-128.onnx", "joiner-epoch-99-avg-1-chunk-16-left-128.int8.onnx", "tokens.txt"}, true, TypeZipformerTransducer, engine.KindStreaming, "encoder-epoch-99-avg-1-chunk-16-left-128.int8.onnx"},
		{"sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2025-09-09", []string{"model.int8.onnx", "tokens.txt"}, true, TypeSenseVoice, engine.KindOffline, ""},
		{"no-tokens", []string{"encoder.onnx", "decoder.onnx", "joiner.onnx"}, false, "", "", ""},
		{"unknown", []string{"model.onnx", "tokens.txt"}, false, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), tt.dir)
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, f := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			d, ok := Detect(dir)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if d.Type != tt.typ || d.Kind != tt.kind || d.Files[engine.RoleEncoder] != tt.enc {
				t.Fatalf("got %+v", d)
			}
		})
	}
}
