// Package setupcheck implements the setup wizard's step checks (OC-70,
// FR-ONB-005…009) over narrow interfaces, so the decisions are testable
// without devices and the app only wires the real services in. Every check
// makes real calls with fixed inputs (phrases, sentences, a fixed model
// request), never free user input, and honours cancellation.
//
// Results carry technical codes only. Transcripts, screenshots, audio and
// model replies stay in memory for the duration of the check and are never
// logged or put into a Result.
package setupcheck

import (
	"context"
	"errors"
	"time"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/pkg/contracts"
)

// Failure codes shared by several checks; the wizard maps each to its fix
// text (wiz.fix.<code>).
const (
	CodeCanceled       = "canceled"
	CodeNoModel        = "no_model"
	CodeSTTUnavailable = "stt_unavailable"
	CodeSTTFailed      = "stt_failed"
	CodeNoSpeech       = readiness.HeardNoSpeech
	CodeMismatch       = readiness.HeardMismatch
	CodeMicNotFound    = "mic_not_found"
	CodeMicOpenFailed  = "mic_open_failed"
	CodeMicLost        = "mic_lost"
	CodeNotConfigured  = "not_configured"
	CodePlayback       = "playback_failed"
	CodeCaptureFailed  = "capture_failed"
	CodeNoProvider     = "no_provider"
	CodeEmptyReply     = "empty_reply"
	CodeOverlayFailed  = "overlay_failed"
	CodeDetectFailed   = "detect_failed"
)

func failed(code string) readiness.Result {
	return readiness.Result{Status: readiness.Failed, Code: code}
}

// permCode maps a platform error to a permission detail value.
func permCode(err error, other string) string {
	switch {
	case errors.Is(err, contracts.ErrUnsupported):
		return readiness.PermUnavailable
	case errors.Is(err, contracts.ErrPermissionDenied):
		return readiness.PermDenied
	case errors.Is(err, context.Canceled):
		return CodeCanceled
	}
	return other
}

// micCode maps a microphone open or stream error.
func micCode(err error) string {
	switch {
	case errors.Is(err, audio.ErrDeviceNotFound):
		return CodeMicNotFound
	case errors.Is(err, audio.ErrDeviceLost):
		return CodeMicLost
	}
	return permCode(err, CodeMicOpenFailed)
}

// ttsCode maps a speech output error.
func ttsCode(err error) string {
	switch {
	case errors.Is(err, tts.ErrAuth):
		return "auth"
	case errors.Is(err, tts.ErrRateLimited):
		return "rate_limit"
	case errors.Is(err, tts.ErrUnavailable):
		return "network"
	case errors.Is(err, tts.ErrBadRequest):
		return "bad_request"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return CodeCanceled
	}
	return CodePlayback
}

// llmCode maps a model request error to llm's technical kinds.
func llmCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return llm.ErrorKind(err)
}

// within runs fn under a timeout derived from ctx; d <= 0 uses def.
func within(ctx context.Context, d, def time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		d = def
	}
	return context.WithTimeout(ctx, d)
}
