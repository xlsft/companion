//go:build whisper && cgo

package whisper

// Untested: mirrors whisper.cpp's default macOS build (Metal + Accelerate,
// OpenMP off). Validate on the macOS spike before relying on it.

// #cgo LDFLAGS: -lwhisper -lggml -lggml-base -lggml-cpu -lggml-metal -lggml-blas -lc++ -framework Accelerate -framework Metal -framework MetalKit -framework Foundation
import "C"
