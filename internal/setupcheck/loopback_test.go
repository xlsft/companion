package setupcheck

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/stt"
	"github.com/xlsft/companion/internal/tts"
)

// loopSpeaker checks the microphone records before the phrase plays.
type loopSpeaker struct {
	fakeSpeaker
	mic         *fakeMic
	micOpenSeen bool
	cancel      context.CancelFunc // set: the user closes the step mid-phrase
}

func (s *loopSpeaker) Say(ctx context.Context, text string, started func()) error {
	s.micOpenSeen = s.mic.openCount() > 0
	if s.cancel != nil {
		s.cancel()
		return ctx.Err()
	}
	return s.fakeSpeaker.Say(ctx, text, started)
}

func TestLoopback(t *testing.T) {
	ru := readiness.MicPhrase("ru")
	ok, skip := readiness.DetailOK, readiness.DetailSkipped
	tests := []struct {
		name   string
		mic    *fakeMic
		stt    *fakeSTT
		sp     fakeSpeaker
		conf   func() bool
		cancel bool
		status readiness.CheckStatus
		code   string
		spoke  string
		heard  string
		text   string // LastTranscript afterwards
		reused bool   // StepSTT passes without recording
	}{
		{name: "heard and matched", stt: &fakeSTT{final: speech(ru + ".")},
			status: readiness.Passed, spoke: ok, heard: ok, text: ru + ".", reused: true},
		{name: "headphones: nothing heard", stt: &fakeSTT{final: stt.Final{}},
			status: readiness.Passed, spoke: ok, heard: readiness.HeardNoSpeech},
		{name: "mismatch", stt: &fakeSTT{final: speech("продолжение следует")},
			status: readiness.Passed, spoke: ok, heard: readiness.HeardMismatch, text: "продолжение следует"},
		{name: "no endpoint: playback end plus tail stops it", stt: &fakeSTT{final: speech(ru), noEndpoint: true},
			status: readiness.Passed, spoke: ok, heard: ok, text: ru, reused: true},
		{name: "no microphone, voice still checked", mic: &fakeMic{openErr: audio.ErrDeviceNotFound}, stt: &fakeSTT{},
			status: readiness.Passed, spoke: ok, heard: CodeMicNotFound},
		{name: "playback fails", stt: &fakeSTT{final: speech(ru)}, sp: fakeSpeaker{err: &tts.Error{Kind: tts.ErrAuth}},
			status: readiness.Failed, code: "auth", spoke: "auth", heard: skip},
		{name: "nothing audible", stt: &fakeSTT{final: speech(ru)}, sp: fakeSpeaker{silent: true},
			status: readiness.Failed, code: CodePlayback, spoke: CodePlayback, heard: skip},
		{name: "not configured", stt: &fakeSTT{final: speech(ru)}, conf: func() bool { return false },
			status: readiness.Failed, code: CodeNotConfigured, spoke: CodeNotConfigured, heard: skip},
		{name: "canceled", stt: &fakeSTT{final: speech(ru), noEndpoint: true}, cancel: true,
			status: readiness.Failed, code: CodeCanceled, spoke: CodeCanceled, heard: skip},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mic := tt.mic
			if mic == nil {
				mic = &fakeMic{frames: 20, hold: true}
			}
			sp := &loopSpeaker{fakeSpeaker: tt.sp, mic: mic}
			if tt.cancel {
				sp.cancel = cancel
			}
			conf := tt.conf
			if conf == nil {
				conf = yes
			}
			// StepSTT's own recording would fail: a pass proves the reuse.
			sttCheck := &STT{
				Listener: listener(&fakeMic{openErr: audio.ErrDeviceNotFound}, &fakeSTT{}),
				Lang:     func() string { return "ru" }, Key: func() string { return "k" },
			}
			lb := Loopback{Listener: listener(mic, tt.stt), Speaker: sp, Configured: conf,
				Lang: func() string { return "ru" }, STT: sttCheck, Tail: time.Millisecond}

			got := lb.Run(ctx)
			if got.Status != tt.status || got.Code != tt.code {
				t.Fatalf("got %s/%q, want %s/%q", got.Status, got.Code, tt.status, tt.code)
			}
			if !tt.cancel {
				want := map[string]string{readiness.DetailSpoke: tt.spoke, readiness.DetailHeard: tt.heard}
				if !maps.Equal(got.Detail, want) {
					t.Errorf("detail %v, want %v", got.Detail, want)
				}
			}
			if tt.spoke == ok {
				if len(sp.said) != 1 || sp.said[0] != ru {
					t.Errorf("said %q, want the mic phrase", sp.said)
				}
				if tt.mic == nil && !sp.micOpenSeen {
					t.Error("the phrase played before the microphone recorded")
				}
			}
			if s := sttCheck.LastTranscript(); s != tt.text {
				t.Errorf("transcript %q, want %q", s, tt.text)
			}
			res := sttCheck.Run(context.Background())
			if reused := res.Status == readiness.Passed; reused != tt.reused {
				t.Fatalf("StepSTT reused=%v (%+v)", reused, res)
			}
			if tt.reused && res.Detail[readiness.DetailSource] != readiness.SourceLoopback {
				t.Errorf("STT detail %v", res.Detail)
			}
		})
	}
}

func TestSTTLoopbackReuse(t *testing.T) {
	ru := readiness.MicPhrase("ru")
	t0 := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		after  time.Duration
		keyNow string
		noKey  bool
		source string // "" : StepSTT records and fails (mic missing)
	}{
		{name: "fresh", after: time.Minute, keyNow: "k", source: readiness.SourceLoopback},
		{name: "at the edge", after: DefaultLoopbackReuse, keyNow: "k", source: readiness.SourceLoopback},
		{name: "too old", after: DefaultLoopbackReuse + time.Second, keyNow: "k"},
		{name: "settings changed", after: time.Second, keyNow: "other mic"},
		{name: "no key: never reused", after: time.Second, noKey: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now, key := t0, "k"
			s := &STT{
				Listener: listener(&fakeMic{openErr: audio.ErrDeviceNotFound}, &fakeSTT{}),
				Lang:     func() string { return "ru" },
				Key:      func() string { return key },
				Now:      func() time.Time { return now },
			}
			if tt.noKey {
				s.Key = nil
			}
			lb := Loopback{Listener: listener(&fakeMic{frames: 20, hold: true}, &fakeSTT{final: speech(ru)}),
				Speaker: &fakeSpeaker{}, Configured: yes, Lang: func() string { return "ru" }, STT: s}
			if r := lb.Run(context.Background()); r.Detail[readiness.DetailHeard] != readiness.DetailOK {
				t.Fatalf("loopback %+v", r)
			}
			now, key = t0.Add(tt.after), tt.keyNow
			got := s.Run(context.Background())
			if tt.source == "" {
				if got.Status != readiness.Failed || got.Code != CodeMicNotFound {
					t.Fatalf("fallback got %s/%q, want the user's own recording", got.Status, got.Code)
				}
				return
			}
			if got.Status != readiness.Passed || got.Detail[readiness.DetailSource] != tt.source {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestLoopbackCancelWhileListening(t *testing.T) {
	// The phrase played, the recording never ends on its own and the user
	// leaves before the tail is over.
	ctx, cancel := context.WithCancel(context.Background())
	sp := &fakeSpeaker{}
	lb := Loopback{Listener: listener(&fakeMic{hold: true}, &fakeSTT{noEndpoint: true}),
		Speaker: sp, Configured: yes, Lang: func() string { return "en" }, Tail: time.Minute}
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	got := lb.Run(ctx)
	if got.Status != readiness.Failed || got.Code != CodeCanceled {
		t.Fatalf("got %s/%q, want canceled", got.Status, got.Code)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
}
