//go:build whisper

package main

// Registers the whisper.cpp engine; it becomes the default -engine.
import _ "github.com/xlsft/companion/internal/stt/engine/whisper"
