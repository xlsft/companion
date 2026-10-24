package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDescriptorRoundTrip(t *testing.T) {
	ok := Descriptor{Engine: "sherpa", Type: "sense_voice", Kind: KindOffline,
		Files: map[string]string{RoleModel: "model.int8.onnx", RoleTokens: "tokens.txt"}}
	tests := []struct {
		name    string
		d       Descriptor
		wantErr bool
	}{
		{"ok", ok, false},
		{"no tokens", Descriptor{Engine: "sherpa", Type: "x", Kind: KindOffline, Files: map[string]string{RoleModel: "m.onnx"}}, true},
		{"bad kind", Descriptor{Engine: "sherpa", Type: "x", Kind: "batch", Files: ok.Files}, true},
		{"path escape", Descriptor{Engine: "sherpa", Type: "x", Kind: KindOffline, Files: map[string]string{RoleTokens: "../tokens.txt"}}, true},
		{"no type", Descriptor{Engine: "sherpa", Kind: KindOffline, Files: ok.Files}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			err := WriteDescriptor(dir, tt.d)
			if (err != nil) != tt.wantErr {
				t.Fatalf("write: %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			got, err := ReadDescriptor(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got.Path(dir, RoleTokens) != filepath.Join(dir, "tokens.txt") || got.Path(dir, RoleJoiner) != "" {
				t.Fatalf("paths of %+v", got)
			}
		})
	}
}

func TestAuto(t *testing.T) {
	suffix := fmt.Sprint(time.Now().UnixNano())
	dirEng, fileEng := "test-dir-"+suffix, "test-file-"+suffix
	Register(dirEng, func(Config) (Engine, Info, error) { return nil, Info{RuntimeVersion: "d"}, nil })
	Register(fileEng, func(Config) (Engine, Info, error) { return nil, Info{RuntimeVersion: "f"}, nil })
	tmp := t.TempDir()
	file := filepath.Join(tmp, "ggml.bin")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path, want string
		err        error
	}{
		{tmp, dirEng, nil},
		{file, fileEng, nil},
		{filepath.Join(tmp, "missing"), "", ErrModelNotFound},
	}
	for _, tt := range tests {
		_, info, err := Auto(dirEng, fileEng)(Config{ModelPath: tt.path})
		if !errors.Is(err, tt.err) || info.Engine != tt.want {
			t.Errorf("%s: engine %q err %v, want %q %v", tt.path, info.Engine, err, tt.want, tt.err)
		}
	}
	if _, _, err := Auto("not-compiled-"+suffix, fileEng)(Config{ModelPath: tmp}); !errors.Is(err, ErrBackendInit) {
		t.Errorf("missing engine: %v", err)
	}
}
