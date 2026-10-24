// Package vad detects speech in 16 kHz mono PCM streams.
//
// The energy detector here is deliberately cheap; VAD is the seam where a
// model-based detector (whisper.cpp's Silero VAD) can be plugged in later
// without touching the sidecar session logic. Endpointer (endpoint.go)
// turns VAD transitions into hands-free stop decisions.
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
	// ThresholdDB is the absolute minimum threshold, in dBFS frame RMS: a
	// frame is never voiced below it, so digital silence or a very quiet
	// room cannot count as speech however low the tracked floor falls.
	ThresholdDB float64
	// MarginDB is how far above the tracked noise floor a frame must be to
	// count as voiced. The effective threshold is
	// max(ThresholdDB, floor+MarginDB).
	MarginDB float64
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
		MarginDB:    12,
		Attack:      120 * time.Millisecond,
		Hangover:    800 * time.Millisecond,
	}
}

// Noise rejection, in order of cost:
//
//  1. Adaptive floor. The floor is the minimum frame level over a sliding
//     window (minimum statistics), smoothed. Speech has pauses and
//     syllable dips, so the window minimum is background noise; a steady
//     fan hum has no dips, so it becomes the floor itself and falls under
//     floor+margin. It follows noise both up (slowly, so speech onsets are
//     not absorbed) and down (fast), and keeps tracking during speech so a
//     fan that starts mid-utterance cannot hold the endpoint open forever.
//  2. Calibration. The first calibFrames frames decide the initial floor
//     (their minimum, clamped to maxFloorDB) and are then replayed, so
//     hum already present at start never produces a false onset.
//  3. Zero-crossing rate. Hiss and broadband white noise cross zero about
//     every other sample; voiced speech and fricatives stay well below.
//     Frames above zcrMax are never voiced regardless of level. This is
//     the cheap stand-in for a spectral flatness test.
//
// Known limit: noise louder than maxFloorDB+margin that is present from the
// very first frame and is not hiss-like can be mistaken for speech until
// the floor adapts (about 3 s).
const (
	calibFrames = 10
	blockFrames = 5  // frames per minimum-tracking block
	blocks      = 20 // window = blocks*blockFrames frames (2 s at 20 ms)
	maxFloorDB  = -35.0
	zcrMax      = 0.42
	floorDown   = 0.2  // per-frame smoothing when the target is lower
	floorUp     = 0.01 // per-frame smoothing when the target is higher (~2 s)
	dbEpsilon   = 1e-12
)

type frameStat struct {
	db  float64
	zcr float64
}

// Energy is a frame-RMS detector with adaptive floor, attack and hangover.
type Energy struct {
	frameLen     int
	minDB        float64
	marginDB     float64
	attackFrames int
	hangFrames   int
	frameDur     time.Duration

	pending []int16
	frames  int64 // complete frames processed
	speech  bool
	voiced  int // consecutive voiced frames
	silent  int // consecutive unvoiced frames

	calib      []frameStat // frames held back until the floor is known
	calibrated bool
	floor      float64
	ring       [blocks]float64 // per-block minimum level
	ringPos    int
	blockN     int
	blockMin   float64
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
	if cfg.MarginDB <= 0 {
		cfg.MarginDB = d.MarginDB
	}
	if cfg.Attack <= 0 {
		cfg.Attack = d.Attack
	}
	if cfg.Hangover <= 0 {
		cfg.Hangover = d.Hangover
	}
	frameLen := max(1, int(int64(cfg.SampleRate)*int64(cfg.Frame)/int64(time.Second)))
	frames := func(t time.Duration) int { return max(1, int((t+cfg.Frame-1)/cfg.Frame)) }
	e := &Energy{
		frameLen:     frameLen,
		minDB:        cfg.ThresholdDB,
		marginDB:     cfg.MarginDB,
		attackFrames: frames(cfg.Attack),
		hangFrames:   frames(cfg.Hangover),
		frameDur:     time.Duration(frameLen) * time.Second / time.Duration(cfg.SampleRate),
	}
	e.Reset()
	return e
}

func (e *Energy) Speech() bool { return e.speech }

// Floor returns the tracked noise floor in dBFS, or false before
// calibration finished.
func (e *Energy) Floor() (float64, bool) { return e.floor, e.calibrated }

func (e *Energy) Reset() {
	e.pending = e.pending[:0]
	e.frames, e.voiced, e.silent, e.speech = 0, 0, 0, false
	e.calib = e.calib[:0]
	e.calibrated = false
	e.floor, e.ringPos, e.blockN = 0, 0, 0
	e.blockMin = math.Inf(1)
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
	var crossings int
	for i, s := range f {
		v := float64(s) / 32768
		sum += v * v
		if i > 0 && (s >= 0) != (f[i-1] >= 0) {
			crossings++
		}
	}
	st := frameStat{
		db:  10 * math.Log10(sum/float64(len(f))+dbEpsilon),
		zcr: float64(crossings) / float64(len(f)),
	}
	if !e.calibrated {
		e.calib = append(e.calib, st)
		if len(e.calib) < calibFrames {
			return events
		}
		lo := math.Inf(1)
		for _, c := range e.calib {
			lo = math.Min(lo, c.db)
		}
		e.floor = math.Min(lo, maxFloorDB)
		e.calibrated = true
		for i := range e.ring {
			e.ring[i] = e.floor
		}
		held := e.calib
		e.calib = e.calib[:0]
		for _, c := range held {
			events = e.classify(c, events)
		}
		return events
	}
	return e.classify(st, events)
}

func (e *Energy) classify(st frameStat, events []Event) []Event {
	thr := math.Max(e.minDB, e.floor+e.marginDB)
	voiced := st.db >= thr && st.zcr <= zcrMax
	idx := e.frames
	e.frames++
	e.trackFloor(st.db)
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

// trackFloor moves the floor toward the window minimum.
func (e *Energy) trackFloor(db float64) {
	e.blockMin = math.Min(e.blockMin, db)
	e.blockN++
	if e.blockN == blockFrames {
		e.ring[e.ringPos] = e.blockMin
		e.ringPos = (e.ringPos + 1) % blocks
		e.blockN, e.blockMin = 0, math.Inf(1)
	}
	target := math.Min(e.blockMin, e.ring[0])
	for _, m := range e.ring[1:] {
		target = math.Min(target, m)
	}
	if target < e.floor {
		e.floor += (target - e.floor) * floorDown
	} else {
		e.floor += (target - e.floor) * floorUp
	}
	e.floor = math.Min(e.floor, maxFloorDB)
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
