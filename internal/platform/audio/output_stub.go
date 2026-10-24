//go:build !cgo

package audio

import (
	"context"

	"github.com/xlsft/companion/internal/audio/playback"
	"github.com/xlsft/companion/pkg/contracts"
)

// Without cgo there is no miniaudio; report playback as missing rather than
// pretending to speak.
type sink struct{}

func NewSink() playback.Sink { return sink{} }

func (sink) OpenOutput(context.Context, string) (playback.Output, error) {
	return nil, contracts.Unsupported(contracts.CapAudioOutput)
}
