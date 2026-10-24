//go:build sherpa

package main

// Registers the sherpa-onnx engine (model directories: Parakeet, SenseVoice,
// streaming zipformer). With whisper also compiled in, the default -engine
// is "auto", which picks by the model path.
import _ "github.com/xlsft/companion/internal/stt/engine/sherpa"
