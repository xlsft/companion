package setupcheck

import (
	"context"
	"errors"
	"image"
	"io"
	"sync"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/overlay"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/stt"
	"github.com/xlsft/companion/internal/talk"
	"github.com/xlsft/companion/pkg/contracts"
)

var errBoom = errors.New("boom")

// fakeMic delivers frames frames of silence, then ends with end.
type fakeMic struct {
	openErr error
	frames  int
	end     error
	hold    bool // keep the stream open after the frames until Close

	mu     sync.Mutex
	device string
}

func (m *fakeMic) Devices(context.Context) ([]audio.Device, error) { return nil, nil }

func (m *fakeMic) Open(ctx context.Context, id string) (audio.Stream, error) {
	m.mu.Lock()
	m.device = id
	m.mu.Unlock()
	if m.openErr != nil {
		return nil, m.openErr
	}
	s := &fakeStream{ch: make(chan audio.Frame), stop: make(chan struct{})}
	go func() {
		defer close(s.ch)
		for i := 0; i < m.frames; i++ {
			select {
			case s.ch <- audio.Frame{Samples: make([]int16, audio.FrameSamples)}:
			case <-s.stop:
				return
			case <-ctx.Done():
				return
			}
		}
		if m.hold {
			select {
			case <-s.stop:
			case <-ctx.Done():
			}
			return
		}
		s.mu.Lock()
		s.err = m.end
		s.mu.Unlock()
	}()
	return s, nil
}

type fakeStream struct {
	ch   chan audio.Frame
	stop chan struct{}
	once sync.Once
	mu   sync.Mutex
	err  error
}

func (s *fakeStream) Frames() <-chan audio.Frame { return s.ch }
func (s *fakeStream) Level() audio.Level         { return audio.Level{} }
func (s *fakeStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
func (s *fakeStream) Close() error {
	s.once.Do(func() { close(s.stop) })
	return nil
}

// fakeSTT hands out sessions that end the utterance after the first write
// and finish with final.
type fakeSTT struct {
	startErr  error
	final     stt.Final
	finishErr error
	// noEndpoint: the utterance never ends on its own, so the check waits
	// for the mic stream; otherwise an early endpoint races a mic error.
	noEndpoint bool
}

func (f *fakeSTT) start(ctx context.Context, opts ...stt.SessionOption) (talk.STTSession, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &fakeSession{f: f, endpoint: make(chan stt.EndpointEvent, 1)}, nil
}

type fakeSession struct {
	f        *fakeSTT
	endpoint chan stt.EndpointEvent
	once     sync.Once
}

func (s *fakeSession) Write([]int16) error {
	if !s.f.noEndpoint {
		s.once.Do(func() { s.endpoint <- stt.EndpointEvent{Reason: stt.EndpointSilence} })
	}
	return nil
}
func (s *fakeSession) Finish(context.Context) (stt.Final, error) { return s.f.final, s.f.finishErr }
func (s *fakeSession) Cancel()                                   {}
func (s *fakeSession) Partials() <-chan stt.Partial              { return nil }
func (s *fakeSession) Endpoint() <-chan stt.EndpointEvent        { return s.endpoint }

func listener(mic *fakeMic, f *fakeSTT) *Listener {
	return &Listener{Mic: mic, STT: f.start}
}

type fakeCapturer struct {
	err   error
	empty bool
}

func (c *fakeCapturer) Capture(context.Context, contracts.CaptureRequest) ([]*contracts.Capture, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.empty {
		return nil, nil
	}
	shot := &contracts.Capture{
		Meta:  contracts.CaptureMetadata{ID: "cap1", Native: contracts.PixelSize{W: 8, H: 6}},
		Image: image.NewRGBA(image.Rect(0, 0, 8, 6)),
	}
	return []*contracts.Capture{shot}, nil
}

type fakeHost struct {
	openErr, presentErr error
	presented           int
	closed              int
}

func (h *fakeHost) Open(context.Context, contracts.DisplayID) (contracts.OverlaySurface, error) {
	if h.openErr != nil {
		return nil, h.openErr
	}
	return &fakeSurface{h: h}, nil
}

type fakeSurface struct{ h *fakeHost }

func (s *fakeSurface) Display() contracts.DisplayID { return "d1" }
func (s *fakeSurface) Present(_ context.Context, f *image.RGBA) error {
	if s.h.presentErr != nil {
		return s.h.presentErr
	}
	for i := 3; i < len(f.Pix); i += 4 {
		if f.Pix[i] != 0 {
			return errors.New("frame not transparent")
		}
	}
	s.h.presented++
	return nil
}
func (s *fakeSurface) SetInteractive(contracts.Rect) error { return nil }
func (s *fakeSurface) Close() error                        { s.h.closed++; return nil }

func oneDisplay(context.Context) (contracts.Topology, error) {
	return contracts.Topology{Version: 1, Displays: []contracts.Display{
		{ID: "d1", Native: contracts.PixelSize{W: 4, H: 3}, Scale: 1, Primary: true},
	}}, nil
}

// fakeSpeaker records what it said; silent: Say succeeds without audio.
type fakeSpeaker struct {
	err    error
	silent bool
	said   []string
}

func (s *fakeSpeaker) Say(_ context.Context, text string, started func()) error {
	s.said = append(s.said, text)
	if s.err != nil {
		return s.err
	}
	if !s.silent && started != nil {
		started()
	}
	return nil
}

func yes() bool { return true }

// fakeLLM streams reply, or fails with err.
type fakeLLM struct {
	err, failEv error
	reply       string
	req         llm.ChatRequest
}

func (p *fakeLLM) Capabilities() llm.Capabilities { return llm.Capabilities{} }
func (p *fakeLLM) Stream(_ context.Context, req llm.ChatRequest) (llm.EventStream, error) {
	p.req = req
	if p.err != nil {
		return nil, p.err
	}
	var evs []llm.Event
	if p.reply != "" {
		evs = append(evs, llm.TextDelta{Text: p.reply})
	}
	if p.failEv != nil {
		evs = append(evs, llm.Failed{Err: p.failEv})
	} else {
		evs = append(evs, llm.Completed{})
	}
	return &fakeEvents{evs: evs}, nil
}

type fakeEvents struct{ evs []llm.Event }

func (s *fakeEvents) Recv() (llm.Event, error) {
	if len(s.evs) == 0 {
		return nil, io.EOF
	}
	ev := s.evs[0]
	s.evs = s.evs[1:]
	return ev, nil
}
func (s *fakeEvents) Close() error { return nil }

type fakeHints struct {
	err  error
	cmds []overlay.Command
	reg  []contracts.CaptureMetadata
}

func (h *fakeHints) RegisterCapture(m contracts.CaptureMetadata) { h.reg = append(h.reg, m) }
func (h *fakeHints) Apply(_ context.Context, c overlay.Command) error {
	h.cmds = append(h.cmds, c)
	if c.Op == overlay.OpShow {
		return h.err
	}
	return nil
}

type fakeProviders struct {
	list    []*providers.Info
	listErr error
	res     providers.TestResult
	testErr error
	tested  string
}

func (f *fakeProviders) List(context.Context) ([]*providers.Info, error) { return f.list, f.listErr }
func (f *fakeProviders) Test(_ context.Context, id string) (providers.TestResult, error) {
	f.tested = id
	return f.res, f.testErr
}
