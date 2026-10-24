//go:build whisper && cgo

package whisper

// ggml's static archives reference each other in both directions, hence the
// group. GPU/BLAS backends detected by `make whisper` are added through
// CGO_LDFLAGS by the Makefile.

// #cgo LDFLAGS: -Wl,--start-group -lwhisper -lggml -lggml-base -lggml-cpu -Wl,--end-group -lgomp -lstdc++ -lm -lpthread
import "C"
