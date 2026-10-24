// Package vad detects speech in 16 kHz mono PCM streams.
//
// The energy detector here is deliberately simple; VAD is the seam where a
// model-based detector (whisper.cpp's Silero VAD) can be plugged in later
// without touching the sidecar session logic.
package vad

import (
	"math"
	"time"
)

// VAD consumes audio incrementally and reports speech/silence transitions.
type VAD interface {
	// Process consumes the next samples and returns transitions found in
	// them, in order. Positions are relative to the first sample ever
	// processed since the last Reset.
	Process(pcm []int16) []Event
	Speech() bool
	Reset()
}

type Event struct {
	Speech bool
	At     time.Duration
}

type Config struct {
	SampleRate int
	// Frame is the analysis window.
	Frame time.Duration
	// ThresholdDB is the frame RMS, in dBFS, at or above which a frame counts
	// as voiced.
	ThresholdDB float64
	// Attack is how much consecutive voiced audio confirms speech onset;
	// it rejects clicks and keyboard noise.
	Attack time.Duration
	// Hangover is how much consecutive unvoiced audio ends speech; it keeps
	// short pauses between words inside one speech run.
	Hangover time.Duration
}

func DefaultConfig() Config {
	return Config{
		SampleRate:  16000,
		Frame:       20 * time.Millisecond,
		ThresholdDB: -45,
		Attack:      60 * time.Millisecond,
		Hangover:    400 * time.Millisecond,
	}
}

// Energy is a frame-RMS detector with attack and hangover.
type Energy struct {
	frameLen     int
	threshold    float64 // mean square, linear
	attackFrames int
	hangFrames   int
	frameDur     time.Duration

	pending []int16
	frames  int64 // complete frames processed
	speech  bool
	voiced  int // consecutive voiced frames
	silent  int // consecutive unvoiced frames
}

func NewEnergy(cfg Config) *Energy {
	d := DefaultConfig()
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = d.SampleRate
	}
	if cfg.Frame <= 0 {
		cfg.Frame = d.Frame
	}
	if cfg.ThresholdDB == 0 {
		cfg.ThresholdDB = d.ThresholdDB
	}
	frameLen := max(1, int(int64(cfg.SampleRate)*int64(cfg.Frame)/int64(time.Second)))
	frames := func(t time.Duration) int { return max(1, int((t+cfg.Frame-1)/cfg.Frame)) }
	amp := math.Pow(10, cfg.ThresholdDB/20)
	return &Energy{
		frameLen:     frameLen,
		threshold:    amp * amp,
		attackFrames: frames(cfg.Attack),
		hangFrames:   frames(cfg.Hangover),
		frameDur:     time.Duration(frameLen) * time.Second / time.Duration(cfg.SampleRate),
	}
}

func (e *Energy) Speech() bool { return e.speech }

func (e *Energy) Reset() {
	e.pending = e.pending[:0]
	e.frames, e.voiced, e.silent, e.speech = 0, 0, 0, false
}

func (e *Energy) Process(pcm []int16) []Event {
	var events []Event
	if len(e.pending) > 0 {
		need := e.frameLen - len(e.pending)
		if len(pcm) < need {
			e.pending = append(e.pending, pcm...)
			return nil
		}
		e.pending = append(e.pending, pcm[:need]...)
		pcm = pcm[need:]
		events = e.frame(e.pending, events)
		e.pending = e.pending[:0]
	}
	for len(pcm) >= e.frameLen {
		events = e.frame(pcm[:e.frameLen], events)
		pcm = pcm[e.frameLen:]
	}
	e.pending = append(e.pending, pcm...)
	return events
}

func (e *Energy) frame(f []int16, events []Event) []Event {
	var sum float64
	for _, s := range f {
		v := float64(s) / 32768
		sum += v * v
	}
	voiced := sum/float64(len(f)) >= e.threshold
	idx := e.frames
	e.frames++
	if voiced {
		e.voiced++
		e.silent = 0
	} else {
		e.silent++
		e.voiced = 0
	}
	switch {
	case !e.speech && e.voiced >= e.attackFrames:
		e.speech = true
		// Report the onset where the voiced run began, not where it was
		// confirmed, so partial windows include the first syllable.
		events = append(events, Event{Speech: true, At: e.at(idx - int64(e.voiced) + 1)})
	case e.speech && e.silent >= e.hangFrames:
		e.speech = false
		events = append(events, Event{Speech: false, At: e.at(idx - int64(e.silent) + 1)})
	}
	return events
}

func (e *Energy) at(frame int64) time.Duration { return time.Duration(frame) * e.frameDur }

// Always treats all audio as speech; used when VAD is disabled.
type Always struct{ started bool }

func (a *Always) Process(pcm []int16) []Event {
	if a.started || len(pcm) == 0 {
		return nil
	}
	a.started = true
	return []Event{{Speech: true}}
}

func (a *Always) Speech() bool { return a.started }
func (a *Always) Reset()       { a.started = false }
