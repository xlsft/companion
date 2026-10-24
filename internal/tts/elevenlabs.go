package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const (
	elevenDefaultBaseURL = "https://api.elevenlabs.io"
	elevenDefaultModel   = "eleven_multilingual_v2"
	// ElevenLabs accepts voice_settings.speed only in this range, narrower
	// than the provider-neutral [MinSpeed, MaxSpeed]; we clamp rather than
	// have the API reject a request the caller validly made.
	elevenMinSpeed = 0.7
	elevenMaxSpeed = 1.2
	maxVoicesBody  = 8 << 20
)

// ElevenLabsConfig configures the ElevenLabs provider.
type ElevenLabsConfig struct {
	Config            // BaseURL defaults to https://api.elevenlabs.io
	ModelID    string // default eleven_multilingual_v2
	HTTPClient *http.Client
}

// ElevenLabs implements Provider for /v1/text-to-speech/{voice}/stream.
type ElevenLabs struct {
	base
	model string
}

func NewElevenLabs(cfg ElevenLabsConfig) *ElevenLabs {
	if cfg.BaseURL == "" {
		cfg.BaseURL = elevenDefaultBaseURL
	}
	if cfg.ModelID == "" {
		cfg.ModelID = elevenDefaultModel
	}
	return &ElevenLabs{base: newBase("elevenlabs", cfg.Config, cfg.HTTPClient), model: cfg.ModelID}
}

func (e *ElevenLabs) newRequest(method, path string, body io.Reader) (*http.Request, error) {
	r, err := http.NewRequest(method, e.cfg.BaseURL+path, body)
	if err != nil {
		return nil, &Error{Kind: ErrBadRequest, Provider: e.name, Cause: stripURL(err)}
	}
	if e.cfg.APIKey != "" {
		r.Header.Set("xi-api-key", e.cfg.APIKey)
	}
	return r, nil
}

func (e *ElevenLabs) ListVoices(ctx context.Context) ([]Voice, error) {
	ctx, cancel := context.WithTimeout(ctx, e.cfg.timeout())
	defer cancel()
	r, err := e.newRequest(http.MethodGet, "/v1/voices", nil)
	if err != nil {
		return nil, err
	}
	resp, err := e.do(ctx, cancel, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var payload struct {
		Voices []struct {
			VoiceID  string `json:"voice_id"`
			Name     string `json:"name"`
			Verified []struct {
				Language string `json:"language"`
			} `json:"verified_languages"`
		} `json:"voices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxVoicesBody)).Decode(&payload); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Kind: ErrUnavailable, Provider: e.name, Cause: fmt.Errorf("decode voices: %w", stripURL(err))}
	}
	voices := make([]Voice, 0, len(payload.Voices))
	for _, v := range payload.Voices {
		out := Voice{ID: v.VoiceID, Name: v.Name}
		for _, l := range v.Verified {
			if l.Language != "" {
				out.Languages = append(out.Languages, l.Language)
			}
		}
		voices = append(voices, out)
	}
	return voices, nil
}

type elevenBody struct {
	Text          string `json:"text"`
	ModelID       string `json:"model_id"`
	VoiceSettings struct {
		Speed float64 `json:"speed"`
	} `json:"voice_settings"`
}

func elevenOutputFormat(f Format) (string, error) {
	switch f.Container {
	case ContainerPCM16:
		switch f.SampleRate {
		case 8000, 16000, 22050, 24000, 44100:
			if f.Channels == 1 {
				return fmt.Sprintf("pcm_%d", f.SampleRate), nil
			}
		}
		return "", badRequest("elevenlabs pcm supports mono at 8000/16000/22050/24000/44100 Hz")
	case ContainerMP3:
		return "mp3_44100_128", nil
	}
	return "", badRequest("elevenlabs streaming supports pcm16 and mp3 only")
}

func (e *ElevenLabs) SynthesizeStream(ctx context.Context, req SynthesisRequest) (Stream, error) {
	req, err := req.normalize()
	if err != nil {
		return nil, err
	}
	of, err := elevenOutputFormat(req.Format)
	if err != nil {
		return nil, err
	}
	var b elevenBody
	b.Text, b.ModelID = req.Text, e.model
	b.VoiceSettings.Speed = min(max(req.Speed, elevenMinSpeed), elevenMaxSpeed)
	body, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}

	sctx, cancel := context.WithCancel(ctx)
	path := "/v1/text-to-speech/" + url.PathEscape(req.VoiceID) + "/stream?output_format=" + of
	hreq, err := e.newRequest(http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	untrack := e.track(cancel) // before do, so Cancel also aborts a pending connect
	resp, err := e.do(sctx, cancel, hreq)
	if err != nil {
		untrack()
		cancel()
		return nil, err
	}
	return newReaderStream(sctx, cancel, ctx, resp.Body, req.Format, e.name, untrack), nil
}
