package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

const (
	openAIDefaultBaseURL = "https://api.openai.com/v1"
	openAIDefaultModel   = "gpt-4o-mini-tts"
)

// OpenAIConfig configures an OpenAI-compatible provider. Config.BaseURL
// defaults to the OpenAI API and may point at any compatible local server.
type OpenAIConfig struct {
	Config
	Model string // default gpt-4o-mini-tts
	// Voices overrides the static voice list; compatible servers have their own.
	Voices []Voice
	// HTTPClient is optional (tests, proxies). Do not set Client.Timeout: it
	// would cut long audio streams; use Config.Timeout instead.
	HTTPClient *http.Client
}

// OpenAI implements Provider for POST {base}/audio/speech.
type OpenAI struct {
	base
	model  string
	voices []Voice
}

var openAIVoices = []string{"alloy", "ash", "ballad", "coral", "echo", "fable", "nova", "onyx", "sage", "shimmer", "verse"}

func NewOpenAI(cfg OpenAIConfig) *OpenAI {
	if cfg.BaseURL == "" {
		cfg.BaseURL = openAIDefaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = openAIDefaultModel
	}
	voices := cfg.Voices
	if voices == nil {
		for _, v := range openAIVoices {
			voices = append(voices, Voice{ID: v, Name: v})
		}
	}
	return &OpenAI{base: newBase("openai", cfg.Config, cfg.HTTPClient), model: cfg.Model, voices: voices}
}

func (o *OpenAI) ListVoices(context.Context) ([]Voice, error) {
	return append([]Voice(nil), o.voices...), nil
}

type openAISpeechBody struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	Speed          float64 `json:"speed"`
	ResponseFormat string  `json:"response_format"`
}

func (o *OpenAI) SynthesizeStream(ctx context.Context, req SynthesisRequest) (Stream, error) {
	req, err := req.normalize()
	if err != nil {
		return nil, err
	}
	var rf string
	switch req.Format.Container {
	case ContainerPCM16:
		// The API's "pcm" is fixed at 24 kHz mono; anything else would play wrong.
		if req.Format != PCM24kMono {
			return nil, badRequest("openai pcm is 24000 Hz mono only")
		}
		rf = "pcm"
	case ContainerWAV:
		rf = "wav"
	case ContainerMP3:
		rf = "mp3"
	}
	body, err := json.Marshal(openAISpeechBody{
		Model: o.model, Input: req.Text, Voice: req.VoiceID, Speed: req.Speed, ResponseFormat: rf,
	})
	if err != nil {
		return nil, err
	}

	sctx, cancel := context.WithCancel(ctx)
	hreq, err := http.NewRequest(http.MethodPost, o.cfg.BaseURL+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, &Error{Kind: ErrBadRequest, Provider: o.name, Cause: stripURL(err)}
	}
	hreq.Header.Set("Content-Type", "application/json")
	if o.cfg.APIKey != "" { // local servers often need none
		hreq.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	}
	untrack := o.track(cancel) // before do, so Cancel also aborts a pending connect
	resp, err := o.do(sctx, cancel, hreq)
	if err != nil {
		untrack()
		cancel()
		return nil, err
	}
	return newReaderStream(sctx, cancel, ctx, resp.Body, req.Format, o.name, untrack), nil
}
