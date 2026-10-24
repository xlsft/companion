//go:build whisper

package main

// Registers the whisper.cpp engine; it becomes the default -engine.
import _ "github.com/xlsft/openclick/internal/stt/engine/whisper"
