package main

import (
	"context"
	"errors"
	"log/slog"
	"strconv"

	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/tts"
)

const ttsEdge = tts.EdgeProviderID

// edgeLabel names Edge in the header; the service name is not translated.
const edgeLabel = "Microsoft Edge"

// speechProvider is what the speaker speaks with: Edge when it is the
// choice or nothing usable is configured, otherwise the configured
// provider with Edge as its fallback.
func (c *core) speechProvider(ctx context.Context, s settings.Settings) (tts.Provider, string) {
	edge := tts.NewEdge(tts.EdgeConfig{HTTPClient: c.http})
	voice := edgeVoice(s)
	if s.TTS.ProviderID == ttsEdge {
		if s.TTS.Voice != "" {
			voice = s.TTS.Voice
		}
		return &fixedVoice{Provider: edge, voice: voice}, joinLabel(edgeLabel, s.TTS.Voice)
	}
	prov, name := c.ttsProvider(ctx, s)
	if prov == nil {
		return &fixedVoice{Provider: edge, voice: voice}, edgeLabel
	}
	return &edgeFallback{Provider: prov, id: s.TTS.ProviderID, edge: edge, voice: voice, log: c.log}, name
}

// voicesProvider lists the saved provider's voices; Edge needs no key.
func (c *core) voicesProvider(ctx context.Context, s settings.Settings) tts.Provider {
	if s.TTS.ProviderID == ttsEdge {
		return tts.NewEdge(tts.EdgeConfig{HTTPClient: c.http})
	}
	prov, _ := c.ttsProvider(ctx, s)
	return prov
}

// edgeVoice is the default Edge voice for the speech language, or the
// interface language when speech is set to detect.
func edgeVoice(s settings.Settings) string {
	lang := s.Voice.Language
	if lang == "" || lang == "auto" {
		lang = s.UI.Language
	}
	return tts.EdgeDefaultVoice(lang)
}

// fixedVoice speaks every request with one voice: the speaker's own voice
// setting belongs to another provider when Edge stands in for it.
type fixedVoice struct {
	tts.Provider
	voice string
}

func (f *fixedVoice) SynthesizeStream(ctx context.Context, req tts.SynthesisRequest) (tts.Stream, error) {
	req.VoiceID = f.voice
	return f.Provider.SynthesizeStream(ctx, req)
}

// edgeFallback retries an utterance once with Edge when the configured
// provider cannot start it for a passing reason (network, 5xx, rate
// limit). Every utterance tries the configured provider first, and every
// fallback is logged, so a broken setup does not go unnoticed.
type edgeFallback struct {
	tts.Provider
	id    string
	edge  tts.Provider
	voice string
	log   *slog.Logger
}

func (f *edgeFallback) SynthesizeStream(ctx context.Context, req tts.SynthesisRequest) (tts.Stream, error) {
	st, err := f.Provider.SynthesizeStream(ctx, req)
	if err == nil || !fallsBack(ctx, err) {
		return st, err
	}
	f.log.Warn("speech provider failed, speaking with edge", "provider", f.id, "code", speechErrCode(err))
	req.VoiceID = f.voice
	st, eerr := f.edge.SynthesizeStream(ctx, req)
	if eerr != nil {
		if ctx.Err() == nil {
			f.log.Warn("edge fallback failed", "code", speechErrCode(eerr))
		}
		return nil, err // the configured provider's failure is the one to show
	}
	return st, nil
}

func (f *edgeFallback) Cancel(ctx context.Context) error {
	return errors.Join(f.Provider.Cancel(ctx), f.edge.Cancel(ctx))
}

// fallsBack: only failures Edge can fix. A barge-in is not one, and a bad
// key or request must surface rather than be papered over.
func fallsBack(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return errors.Is(err, tts.ErrUnavailable) || errors.Is(err, tts.ErrRateLimited)
}

// speechErrCode is a log-safe code for a speech error.
func speechErrCode(err error) string {
	var code string
	switch {
	case errors.Is(err, tts.ErrUnavailable):
		code = "unavailable"
	case errors.Is(err, tts.ErrRateLimited):
		code = "rate_limited"
	case errors.Is(err, tts.ErrAuth):
		code = "auth"
	case errors.Is(err, tts.ErrBadRequest):
		code = "bad_request"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = "canceled"
	default:
		return "other"
	}
	var te *tts.Error
	if errors.As(err, &te) && te.Status != 0 {
		code += "_" + strconv.Itoa(te.Status)
	}
	return code
}
