// Package sherpa is the sherpa-onnx engine (k2-fsa/sherpa-onnx-go), compiled
// with the `sherpa` build tag and cgo; importing it registers the engine as
// "sherpa". It loads a model directory described by engine.Descriptor:
// offline NeMo transducers (Parakeet TDT), offline SenseVoice and streaming
// zipformer transducers. Only cmd/stt-sidecar imports it (ADR-0001
// amendment 2). The helpers in this file set build without the tag so
// they are unit-tested by `make test`.
package sherpa
