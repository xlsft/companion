package protocol

// Method names. Requests expect a response; notifications (N) do not.
const (
	MethodInitialize    = "initialize"
	MethodHealth        = "health"
	MethodShutdown      = "shutdown"
	MethodSessionStart  = "session.start"
	MethodSessionAudio  = "session.audio" // N, core → sidecar
	MethodSessionFinish = "session.finish"
	MethodSessionCancel = "session.cancel"

	MethodSessionPartial  = "session.partial"  // N, sidecar → core
	MethodSessionVAD      = "session.vad"      // N, sidecar → core
	MethodSessionEndpoint = "session.endpoint" // N, sidecar → core
	MethodLog             = "log"              // N, sidecar → core
)

// Audio format accepted by session.start. Only one format exists so the
// sidecar never resamples.
const (
	SampleRate     = 16000
	FormatPCMS16LE = "pcm_s16le"
)

const (
	LanguageAuto = "auto"
	BackendCPU   = "cpu"
	BackendAuto  = "auto"
)

type InitializeParams struct {
	ModelPath string `json:"model_path"`
	// ModelSHA256 is optional; when set the sidecar verifies the file before
	// loading it (hex, lowercase).
	ModelSHA256 string `json:"model_sha256,omitempty"`
	// Language is "auto" or an ISO 639-1 code; it is the default for every
	// session.
	Language string `json:"language"`
	Backend  string `json:"backend"`
	// Threads is the decode thread count; 0 lets the engine choose.
	Threads int `json:"threads,omitempty"`
}

type InitializeResult struct {
	ProtocolVersion string `json:"protocol_version"`
	Engine          string `json:"engine"`
	RuntimeVersion  string `json:"runtime_version"`
	BackendInUse    string `json:"backend_in_use"`
	// FallbackReason is set when the requested backend could not start and
	// the engine fell back to CPU (an error code such as
	// "backend_init_failed"). Initialization still succeeds.
	FallbackReason string   `json:"fallback_reason,omitempty"`
	Languages      []string `json:"languages"`
}

type SessionStartParams struct {
	SessionID  string `json:"session_id"`
	SampleRate int    `json:"sample_rate"`
	Format     string `json:"format"`
	Partials   bool   `json:"partials"`
	// Language overrides the initialize default for this session.
	Language string `json:"language,omitempty"`
	// VAD enables endpointing for the session (hands-free). Absent means
	// the sidecar never emits session.endpoint for it.
	VAD *VADOptions `json:"vad,omitempty"`
}

// VADOptions are the per-session endpointing parameters; zero or absent
// fields take the sidecar defaults.
type VADOptions struct {
	// EndSilenceMS is the silence after speech that ends the utterance (800).
	EndSilenceMS int `json:"end_silence_ms,omitempty"`
	// MaxLeadingSilenceMS is how long to wait for speech to start (5000).
	MaxLeadingSilenceMS int `json:"max_leading_silence_ms,omitempty"`
	// MinSpeechMS is the shortest burst that counts as speech (120).
	MinSpeechMS int `json:"min_speech_ms,omitempty"`
	// MarginDB is the level above the noise floor that counts as speech (12).
	MarginDB float64 `json:"margin_db,omitempty"`
	// MaxDurationMS ends the session at this audio length; 0 uses the
	// sidecar's per-session audio limit.
	MaxDurationMS int `json:"max_duration_ms,omitempty"`
}

// Endpoint reasons.
const (
	EndpointSilence     = "silence"
	EndpointNoSpeech    = "no_speech_timeout"
	EndpointMaxDuration = "max_duration"
)

type SessionAudioParams struct {
	SessionID string `json:"session_id"`
	// Seq starts at 0 and increases by one per chunk; chunks with an older
	// seq than already seen are dropped.
	Seq uint64 `json:"seq"`
	// Data is PCM in the session format; encoding/json base64-encodes it.
	Data []byte `json:"data"`
}

type SessionRef struct {
	SessionID string `json:"session_id"`
}

type Segment struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

type FinalResult struct {
	SessionID  string    `json:"session_id"`
	Text       string    `json:"text"`
	Language   string    `json:"language"`
	Segments   []Segment `json:"segments"`
	DurationMS int64     `json:"duration_ms"`
	// DecodeMS is the wall time of the final decode, for diagnostics.
	DecodeMS int64 `json:"decode_ms"`
	// Speech is false when VAD heard no speech; the sidecar then skips the
	// decode and returns empty text instead of a hallucination.
	Speech bool `json:"speech"`
}

type PartialParams struct {
	SessionID string `json:"session_id"`
	// Seq increases per partial within a session.
	Seq  uint64 `json:"seq"`
	Text string `json:"text"`
	// Stable marks text that will not change in later partials. The rolling
	// window decoder currently never sets it.
	Stable bool `json:"stable"`
}

type VADParams struct {
	SessionID string `json:"session_id"`
	Speech    bool   `json:"speech"`
	// AtMS is the transition position in session audio.
	AtMS int64 `json:"at_ms"`
}

// EndpointParams is sent at most once per session, only when session.start
// carried vad options. The core decides what to do (usually session.finish);
// audio and finish stay valid afterwards.
type EndpointParams struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
	// AtMS is the session audio position of the decision.
	AtMS int64 `json:"at_ms"`
}

// LogParams carries technical events only: Code is a stable snake_case
// identifier, never free text.
type LogParams struct {
	Level     string `json:"level"`
	Code      string `json:"code"`
	SessionID string `json:"session_id,omitempty"`
}

type HealthResult struct {
	OK          bool  `json:"ok"`
	Initialized bool  `json:"initialized"`
	UptimeMS    int64 `json:"uptime_ms"`
	Sessions    int   `json:"sessions"`
}
