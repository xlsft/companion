package vad

import "time"

// Reason says why hands-free capture should stop.
type Reason string

const (
	// ReasonSilence: speech was heard and then followed by EndSilence.
	ReasonSilence Reason = "silence"
	// ReasonNoSpeech: nothing but silence/noise for MaxLeadingSilence.
	ReasonNoSpeech Reason = "no_speech_timeout"
	// ReasonMaxDuration: the session reached MaxDuration.
	ReasonMaxDuration Reason = "max_duration"
)

// Endpoint is the single stop decision of a session.
type Endpoint struct {
	Reason Reason
	// At is the position in session audio at which the decision was made.
	At time.Duration
}

// EndpointConfig holds the per-session endpointing knobs. Zero fields take
// the defaults from DefaultEndpointConfig.
type EndpointConfig struct {
	// EndSilence is the silence after speech that ends the utterance. It is
	// the detector's hangover, so pauses shorter than it never split speech.
	EndSilence time.Duration
	// MaxLeadingSilence is how long to wait for speech to start.
	MaxLeadingSilence time.Duration
	// MinSpeech is the detector's attack: shorter bursts are ignored.
	MinSpeech time.Duration
	// MarginDB is the detector's margin over the noise floor.
	MarginDB float64
	// MaxDuration caps the session; 0 disables the check.
	MaxDuration time.Duration
}

func DefaultEndpointConfig() EndpointConfig {
	return EndpointConfig{
		EndSilence:        800 * time.Millisecond,
		MaxLeadingSilence: 5 * time.Second,
		MinSpeech:         120 * time.Millisecond,
		MarginDB:          12,
	}
}

func (c EndpointConfig) withDefaults() EndpointConfig {
	d := DefaultEndpointConfig()
	if c.EndSilence <= 0 {
		c.EndSilence = d.EndSilence
	}
	if c.MaxLeadingSilence <= 0 {
		c.MaxLeadingSilence = d.MaxLeadingSilence
	}
	if c.MinSpeech <= 0 {
		c.MinSpeech = d.MinSpeech
	}
	if c.MarginDB <= 0 {
		c.MarginDB = d.MarginDB
	}
	return c
}

// Endpointer wraps a VAD and decides when hands-free capture should stop.
// It reports at most one Endpoint; audio may keep flowing afterwards (the
// core stops it with session.finish).
type Endpointer struct {
	v         VAD
	cfg       EndpointConfig
	rate      int
	samples   int64
	sawSpeech bool
	done      bool
}

// NewEndpointer builds an energy-based endpointer. base supplies the
// absolute threshold, frame size and sample rate; cfg overrides attack,
// hangover and margin.
func NewEndpointer(base Config, cfg EndpointConfig) *Endpointer {
	cfg = cfg.withDefaults()
	base.Attack, base.Hangover, base.MarginDB = cfg.MinSpeech, cfg.EndSilence, cfg.MarginDB
	return NewEndpointerWith(NewEnergy(base), base.SampleRate, cfg)
}

// NewEndpointerWith wraps any VAD whose hangover already equals
// cfg.EndSilence (a speech to silence event is the silence endpoint). rate 0
// means 16 kHz.
func NewEndpointerWith(v VAD, rate int, cfg EndpointConfig) *Endpointer {
	if rate <= 0 {
		rate = DefaultConfig().SampleRate
	}
	return &Endpointer{v: v, cfg: cfg.withDefaults(), rate: rate}
}

// Process feeds audio and returns the VAD transitions plus the endpoint, if
// this chunk produced it. After the endpoint, later calls return nil for it.
func (p *Endpointer) Process(pcm []int16) ([]Event, *Endpoint) {
	events := p.v.Process(pcm)
	p.samples += int64(len(pcm))
	if p.done {
		return events, nil
	}
	now := time.Duration(p.samples) * time.Second / time.Duration(p.rate)
	for _, ev := range events {
		if ev.Speech {
			p.sawSpeech = true
		} else if p.sawSpeech {
			return events, p.fire(ReasonSilence, now)
		}
	}
	switch {
	case p.cfg.MaxDuration > 0 && now >= p.cfg.MaxDuration:
		return events, p.fire(ReasonMaxDuration, now)
	case !p.sawSpeech && !p.v.Speech() && now >= p.cfg.MaxLeadingSilence:
		return events, p.fire(ReasonNoSpeech, now)
	}
	return events, nil
}

func (p *Endpointer) fire(r Reason, at time.Duration) *Endpoint {
	p.done = true
	return &Endpoint{Reason: r, At: at}
}

func (p *Endpointer) Speech() bool { return p.v.Speech() }
