package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DescriptorFile is written by the model installer into a multi-file model
// directory. It tells the engine which file plays which role, so the
// sidecar never guesses from file names for an installed model.
const DescriptorFile = "model.json"

// Model kinds.
const (
	KindOffline   = "offline"
	KindStreaming = "streaming"
)

// File roles inside a model directory.
const (
	RoleEncoder = "encoder"
	RoleDecoder = "decoder"
	RoleJoiner  = "joiner"
	RoleModel   = "model"
	RoleTokens  = "tokens"
)

// Descriptor describes a model directory.
type Descriptor struct {
	// Engine is the runtime that loads it, e.g. "sherpa".
	Engine string `json:"engine"`
	// Type is the engine's model architecture, e.g. "nemo_transducer",
	// "sense_voice", "zipformer_transducer".
	Type string `json:"type"`
	// Kind is KindOffline or KindStreaming.
	Kind string `json:"kind"`
	// Files maps a role to a bare file name in the directory.
	Files map[string]string `json:"files"`
	// Languages are ISO 639-1 codes the model recognises.
	Languages []string `json:"languages,omitempty"`
}

// Validate checks that every file is a bare name; Path relies on it.
func (d Descriptor) Validate() error {
	if d.Engine == "" || d.Type == "" {
		return errors.New("descriptor: engine and type required")
	}
	if d.Kind != KindOffline && d.Kind != KindStreaming {
		return fmt.Errorf("descriptor: kind %q", d.Kind)
	}
	if d.Files[RoleTokens] == "" {
		return errors.New("descriptor: tokens file required")
	}
	for role, name := range d.Files {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			return fmt.Errorf("descriptor: %s must be a bare file name", role)
		}
	}
	return nil
}

// Path is the absolute path of a role's file in dir, or "" if absent.
func (d Descriptor) Path(dir, role string) string {
	if n := d.Files[role]; n != "" {
		return filepath.Join(dir, n)
	}
	return ""
}

// ReadDescriptor loads and validates dir/model.json.
func ReadDescriptor(dir string) (Descriptor, error) {
	var d Descriptor
	data, err := os.ReadFile(filepath.Join(dir, DescriptorFile))
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return d, fmt.Errorf("descriptor: %w", err)
	}
	return d, d.Validate()
}

// Auto returns a factory that loads a model directory with the dirEngine
// and a single file with fileEngine, so one sidecar build serves both
// sherpa-onnx and whisper.cpp models without the core naming the engine.
func Auto(dirEngine, fileEngine string) Factory {
	return func(cfg Config) (Engine, Info, error) {
		name := fileEngine
		st, err := os.Stat(cfg.ModelPath)
		if err != nil {
			return nil, Info{}, fmt.Errorf("%w: %v", ErrModelNotFound, err)
		}
		if st.IsDir() {
			name = dirEngine
		}
		f, err := Lookup(name)
		if err != nil {
			return nil, Info{}, fmt.Errorf("%w: %v", ErrBackendInit, err)
		}
		eng, info, err := f(cfg)
		info.Engine = name
		return eng, info, err
	}
}

// WriteDescriptor writes dir/model.json.
func WriteDescriptor(dir string, d Descriptor) error {
	if err := d.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, DescriptorFile), data, 0o644)
}
