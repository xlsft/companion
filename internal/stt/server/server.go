// Package server is the sidecar side of the STT protocol: it owns sessions,
// runs VAD and schedules rolling partial and final decodes on an Engine.
// cmd/stt-sidecar only wires flags and stdio into it.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/openclick/internal/stt/engine"
	"github.com/xlsft/openclick/internal/stt/protocol"
	"github.com/xlsft/openclick/internal/stt/vad"
)

type Config struct {
	// EngineName is reported in initialize.
	EngineName string
	Factory    engine.Factory
	// PartialInterval is how much new audio triggers a partial decode.
	PartialInterval time.Duration
	// PartialWindow is how much trailing audio a partial decode sees.
	PartialWindow time.Duration
	// MaxUtterance caps buffered audio per session.
	MaxUtterance time.Duration
	// VAD configures the per-session detector; zero fields take defaults.
	VAD vad.Config
	// VADDisabled treats all audio as speech and never endpoints on silence.
	VADDisabled bool
	// Log receives technical codes only.
	Log *slog.Logger
}

func (c *Config) defaults() {
	if c.PartialInterval <= 0 {
		c.PartialInterval = 700 * time.Millisecond
	}
	if c.PartialWindow <= 0 {
		c.PartialWindow = 10 * time.Second
	}
	if c.MaxUtterance <= 0 {
		c.MaxUtterance = 5 * time.Minute
	}
	if c.Log == nil {
		c.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
}

func samples(d time.Duration) int { return int(d * protocol.SampleRate / time.Second) }

type Server struct {
	cfg     Config
	started time.Time
	enc     *protocol.Encoder
	cancel  context.CancelFunc
	wg      sync.WaitGroup // initialize and decode goroutines

	// mu guards everything below. Notifications are written while holding
	// it so that a partial can never be sent after session.finish or
	// session.cancel was processed.
	mu           sync.Mutex
	ctx          context.Context
	eng          engine.Engine
	info         engine.Info
	initializing bool
	initialized  bool
	language     string
	sessions     map[string]*session
	shuttingDown bool
}

type session struct {
	id       string
	language string
	partials bool
	vad      vad.VAD
	ep       *vad.Endpointer // nil unless the session asked for endpointing
	ctx      context.Context
	cancel   context.CancelFunc

	pcm         []int16
	gotAudio    bool
	lastSeq     uint64
	overflowed  bool
	sawSpeech   bool
	finishing   bool
	sinceDecode int

	partialCancel context.CancelFunc
	partialDone   chan struct{}
	partialSeq    uint64
	lastPartial   string
}

func New(cfg Config) *Server {
	cfg.defaults()
	return &Server{cfg: cfg, sessions: map[string]*session{}}
}

type decoded struct {
	m   *protocol.Message
	err error
}

// Serve runs the protocol loop until shutdown, EOF on r, a write failure on
// w or ctx cancellation. It releases the engine before returning.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.started = time.Now()
	s.enc = protocol.NewEncoder(w)
	s.cancel = cancel
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()

	msgs := make(chan decoded)
	go func() {
		dec := protocol.NewDecoder(r)
		for {
			m, err := dec.Decode()
			select {
			case msgs <- decoded{m, err}:
			case <-ctx.Done():
				return
			}
			if protocol.IsTerminal(err) {
				return
			}
		}
	}()

	var shutdownID json.RawMessage
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case d := <-msgs:
			if d.err != nil {
				if protocol.IsTerminal(d.err) {
					if errors.Is(d.err, protocol.ErrParse) {
						s.respondErr(nil, d.err.(*protocol.Error))
					}
					break loop
				}
				var id json.RawMessage
				if d.m != nil {
					id = d.m.ID
				}
				s.respondErr(id, d.err.(*protocol.Error))
				continue
			}
			if d.m.IsRequest() && d.m.Method == protocol.MethodShutdown {
				shutdownID = d.m.ID
				break loop
			}
			s.dispatch(d.m)
		}
	}
	s.close()
	if shutdownID != nil {
		s.respond(shutdownID, struct{}{})
	}
	return nil
}

func (s *Server) dispatch(m *protocol.Message) {
	if m.IsNotification() {
		if m.Method == protocol.MethodSessionAudio {
			s.audio(m)
		}
		return
	}
	if !m.IsRequest() {
		return // we never send requests, so responses are stray
	}
	switch m.Method {
	case protocol.MethodInitialize:
		s.initialize(m)
	case protocol.MethodHealth:
		s.health(m)
	case protocol.MethodSessionStart:
		s.start(m)
	case protocol.MethodSessionFinish:
		s.finish(m)
	case protocol.MethodSessionCancel:
		s.cancelSession(m)
	default:
		s.respondErr(m.ID, protocol.ErrMethodNotFound)
	}
}

func (s *Server) initialize(m *protocol.Message) {
	var p protocol.InitializeParams
	if err := m.DecodeParams(&p); err != nil {
		s.respondErr(m.ID, err.(*protocol.Error))
		return
	}
	if p.Language == "" {
		p.Language = protocol.LanguageAuto
	}
	if p.Backend == "" {
		p.Backend = protocol.BackendCPU
	}
	if p.Backend != protocol.BackendCPU && p.Backend != protocol.BackendAuto {
		s.respondErr(m.ID, protocol.ErrInvalidParams.WithDetail("backend"))
		return
	}
	s.mu.Lock()
	if s.initialized || s.initializing {
		s.mu.Unlock()
		s.respondErr(m.ID, protocol.ErrAlreadyInitialized)
		return
	}
	s.initializing = true
	s.mu.Unlock()

	// Model loading takes seconds; keep answering health meanwhile.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		eng, info, perr := s.load(p)
		s.mu.Lock()
		s.initializing = false
		if perr == nil && s.shuttingDown {
			_ = eng.Close()
			perr = protocol.ErrShuttingDown
		}
		if perr != nil {
			s.mu.Unlock()
			s.cfg.Log.Warn("initialize failed", "code", perr.Code)
			s.respondErr(m.ID, perr)
			return
		}
		s.eng, s.info, s.initialized, s.language = eng, info, true, p.Language
		if info.FallbackReason != "" {
			s.logCode("warn", "backend_fallback_cpu", "")
		}
		s.mu.Unlock()
		s.respond(m.ID, protocol.InitializeResult{
			ProtocolVersion: protocol.Version,
			Engine:          s.cfg.EngineName,
			RuntimeVersion:  info.RuntimeVersion,
			BackendInUse:    info.BackendInUse,
			FallbackReason:  info.FallbackReason,
			Languages:       info.Languages,
		})
	}()
}

func (s *Server) load(p protocol.InitializeParams) (engine.Engine, engine.Info, *protocol.Error) {
	if p.ModelPath != "" {
		if _, err := os.Stat(p.ModelPath); err != nil {
			return nil, engine.Info{}, protocol.ErrModelNotFound.WithDetail("stat")
		}
		if p.ModelSHA256 != "" {
			sum, err := fileSHA256(p.ModelPath)
			if err != nil {
				return nil, engine.Info{}, protocol.ErrModelNotFound.WithDetail("read")
			}
			if !strings.EqualFold(sum, p.ModelSHA256) {
				return nil, engine.Info{}, protocol.ErrModelChecksum
			}
		}
	}
	eng, info, err := s.cfg.Factory(engine.Config{
		ModelPath: p.ModelPath,
		Language:  p.Language,
		Backend:   p.Backend,
		Threads:   p.Threads,
	})
	switch {
	case err == nil:
		return eng, info, nil
	case errors.Is(err, engine.ErrModelNotFound):
		return nil, info, protocol.ErrModelNotFound
	default:
		return nil, info, protocol.ErrBackendInitFailed
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Server) health(m *protocol.Message) {
	s.mu.Lock()
	res := protocol.HealthResult{
		OK:          true,
		Initialized: s.initialized,
		UptimeMS:    time.Since(s.started).Milliseconds(),
		Sessions:    len(s.sessions),
	}
	s.mu.Unlock()
	s.respond(m.ID, res)
}

func (s *Server) start(m *protocol.Message) {
	var p protocol.SessionStartParams
	if err := m.DecodeParams(&p); err != nil {
		s.respondErr(m.ID, err.(*protocol.Error))
		return
	}
	if p.SessionID == "" {
		s.respondErr(m.ID, protocol.ErrInvalidParams.WithDetail("session_id"))
		return
	}
	if p.SampleRate != protocol.SampleRate || p.Format != protocol.FormatPCMS16LE {
		s.respondErr(m.ID, protocol.ErrAudioFormat)
		return
	}
	if o := p.VAD; o != nil && (o.EndSilenceMS < 0 || o.EndSilenceMS > 60000 ||
		o.MaxLeadingSilenceMS < 0 || o.MaxLeadingSilenceMS > 600000 ||
		o.MinSpeechMS < 0 || o.MinSpeechMS > 5000 ||
		o.MarginDB < 0 || o.MarginDB > 60 || o.MaxDurationMS < 0) {
		s.respondErr(m.ID, protocol.ErrInvalidParams.WithDetail("vad"))
		return
	}
	s.mu.Lock()
	if !s.initialized {
		s.mu.Unlock()
		s.respondErr(m.ID, protocol.ErrNotInitialized)
		return
	}
	if _, ok := s.sessions[p.SessionID]; ok {
		s.mu.Unlock()
		s.respondErr(m.ID, protocol.ErrSessionExists)
		return
	}
	lang := p.Language
	if lang == "" {
		lang = s.language
	}
	ctx, cancel := context.WithCancel(s.ctx)
	sess := &session{id: p.SessionID, language: lang, partials: p.Partials, ctx: ctx, cancel: cancel}
	switch {
	case p.VAD != nil:
		sess.ep = s.newEndpointer(*p.VAD)
	case s.cfg.VADDisabled:
		sess.vad = &vad.Always{}
	default:
		sess.vad = vad.NewEnergy(s.cfg.VAD)
	}
	s.sessions[p.SessionID] = sess
	s.mu.Unlock()
	s.respond(m.ID, struct{}{})
}

func (s *Server) audio(m *protocol.Message) {
	var p protocol.SessionAudioParams
	if err := m.DecodeParams(&p); err != nil {
		s.mu.Lock()
		s.logCode("warn", "audio_invalid", "")
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[p.SessionID]
	if sess == nil || sess.finishing {
		s.logCode("debug", "audio_dropped", p.SessionID)
		return
	}
	switch {
	case sess.gotAudio && p.Seq <= sess.lastSeq:
		s.logCode("warn", "audio_seq_out_of_order", sess.id)
		return
	case (sess.gotAudio && p.Seq != sess.lastSeq+1) || (!sess.gotAudio && p.Seq != 0):
		s.logCode("warn", "audio_seq_gap", sess.id)
	}
	sess.gotAudio, sess.lastSeq = true, p.Seq

	pcm := protocol.DecodePCM16(p.Data)
	if room := samples(s.cfg.MaxUtterance) - len(sess.pcm); len(pcm) > room {
		pcm = pcm[:max(room, 0)]
		if !sess.overflowed {
			sess.overflowed = true
			s.logCode("warn", "audio_overflow", sess.id)
		}
	}
	if len(pcm) == 0 {
		return
	}
	sess.pcm = append(sess.pcm, pcm...)
	sess.sinceDecode += len(pcm)

	events, ep := sess.process(pcm)
	for _, ev := range events {
		if ev.Speech {
			sess.sawSpeech = true
		}
		s.notify(protocol.MethodSessionVAD, protocol.VADParams{
			SessionID: sess.id, Speech: ev.Speech, AtMS: ev.At.Milliseconds(),
		})
	}
	if ep != nil {
		s.notify(protocol.MethodSessionEndpoint, protocol.EndpointParams{
			SessionID: sess.id, Reason: string(ep.Reason), AtMS: ep.At.Milliseconds(),
		})
	}

	// While a partial decode runs, sinceDecode keeps growing, so the next
	// one starts with the first chunk after it finishes.
	if sess.partials && sess.sawSpeech && sess.partialDone == nil && sess.sinceDecode >= samples(s.cfg.PartialInterval) {
		s.startPartial(sess)
	}
}

// process feeds the detector; only endpointing sessions can produce an
// endpoint.
func (sess *session) process(pcm []int16) ([]vad.Event, *vad.Endpoint) {
	if sess.ep != nil {
		return sess.ep.Process(pcm)
	}
	return sess.vad.Process(pcm), nil
}

// newEndpointer builds the session endpointer from client options on top of
// the sidecar's VAD configuration. With VAD disabled only the max-duration
// check can fire, since "speech" starts with the first sample.
func (s *Server) newEndpointer(o protocol.VADOptions) *vad.Endpointer {
	ec := vad.EndpointConfig{
		EndSilence:        time.Duration(o.EndSilenceMS) * time.Millisecond,
		MaxLeadingSilence: time.Duration(o.MaxLeadingSilenceMS) * time.Millisecond,
		MinSpeech:         time.Duration(o.MinSpeechMS) * time.Millisecond,
		MarginDB:          o.MarginDB,
		MaxDuration:       time.Duration(o.MaxDurationMS) * time.Millisecond,
	}
	// Audio past MaxUtterance is dropped, so the limit is also the latest
	// possible max_duration endpoint.
	if ec.MaxDuration <= 0 || ec.MaxDuration > s.cfg.MaxUtterance {
		ec.MaxDuration = s.cfg.MaxUtterance
	}
	if s.cfg.VADDisabled {
		return vad.NewEndpointerWith(&vad.Always{}, protocol.SampleRate, ec)
	}
	return vad.NewEndpointer(s.cfg.VAD, ec)
}

// startPartial must be called with s.mu held.
func (s *Server) startPartial(sess *session) {
	window := samples(s.cfg.PartialWindow)
	src := sess.pcm[max(0, len(sess.pcm)-window):]
	pcm := protocol.Float32(src)
	sess.sinceDecode = 0
	ctx, cancel := context.WithCancel(sess.ctx)
	done := make(chan struct{})
	sess.partialCancel, sess.partialDone = cancel, done
	eng, lang := s.eng, sess.language

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		res, err := eng.Transcribe(ctx, pcm, engine.Options{Language: lang, Partial: true})
		s.mu.Lock()
		defer s.mu.Unlock()
		close(done)
		sess.partialCancel, sess.partialDone = nil, nil
		if ctx.Err() != nil || sess.finishing || s.sessions[sess.id] != sess {
			return
		}
		if err != nil {
			s.logCode("warn", "partial_failed", sess.id)
			return
		}
		text := strings.TrimSpace(res.Text)
		if text == "" || text == sess.lastPartial {
			return
		}
		sess.lastPartial = text
		sess.partialSeq++
		s.notify(protocol.MethodSessionPartial, protocol.PartialParams{
			SessionID: sess.id, Seq: sess.partialSeq, Text: text,
		})
	}()
}

func (s *Server) finish(m *protocol.Message) {
	var p protocol.SessionRef
	if err := m.DecodeParams(&p); err != nil {
		s.respondErr(m.ID, err.(*protocol.Error))
		return
	}
	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	if sess == nil {
		s.mu.Unlock()
		s.respondErr(m.ID, protocol.ErrSessionUnknown)
		return
	}
	if sess.finishing {
		s.mu.Unlock()
		s.respondErr(m.ID, protocol.ErrInvalidRequest.WithDetail("finish pending"))
		return
	}
	sess.finishing = true
	if sess.partialCancel != nil {
		sess.partialCancel()
	}
	partialDone, eng := sess.partialDone, s.eng
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if partialDone != nil {
			<-partialDone // engines may serialise decodes; don't queue behind it
		}
		res := protocol.FinalResult{
			SessionID:  sess.id,
			Language:   sess.language,
			DurationMS: int64(len(sess.pcm)) * 1000 / protocol.SampleRate,
			Segments:   []protocol.Segment{},
			Speech:     sess.sawSpeech,
		}
		var err error
		if sess.sawSpeech {
			t0 := time.Now()
			var out engine.Result
			out, err = eng.Transcribe(sess.ctx, protocol.Float32(sess.pcm), engine.Options{Language: sess.language})
			res.DecodeMS = time.Since(t0).Milliseconds()
			res.Text = strings.TrimSpace(out.Text)
			if out.Language != "" {
				res.Language = out.Language
			}
			for _, seg := range out.Segments {
				res.Segments = append(res.Segments, protocol.Segment{
					StartMS: seg.Start.Milliseconds(), EndMS: seg.End.Milliseconds(), Text: strings.TrimSpace(seg.Text),
				})
			}
		}

		s.mu.Lock()
		cancelled := sess.ctx.Err() != nil
		shutting := s.shuttingDown
		if s.sessions[sess.id] == sess {
			delete(s.sessions, sess.id)
		}
		s.mu.Unlock()
		sess.cancel()

		switch {
		case cancelled && shutting:
			s.respondErr(m.ID, protocol.ErrShuttingDown)
		case cancelled:
			s.respondErr(m.ID, protocol.ErrSessionCancelled)
		case err != nil:
			s.cfg.Log.Warn("final decode failed", "code", "transcribe_failed")
			s.respondErr(m.ID, protocol.ErrTranscribeFailed)
		default:
			s.respond(m.ID, res)
		}
	}()
}

func (s *Server) cancelSession(m *protocol.Message) {
	var p protocol.SessionRef
	if err := m.DecodeParams(&p); err != nil {
		s.respondErr(m.ID, err.(*protocol.Error))
		return
	}
	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	if sess != nil {
		delete(s.sessions, p.SessionID)
		sess.cancel()
	}
	s.mu.Unlock()
	if sess == nil {
		s.respondErr(m.ID, protocol.ErrSessionUnknown)
		return
	}
	s.respond(m.ID, struct{}{})
}

func (s *Server) close() {
	s.mu.Lock()
	s.shuttingDown = true
	for id, sess := range s.sessions {
		sess.cancel()
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eng != nil {
		_ = s.eng.Close()
		s.eng = nil
	}
}

// logCode emits a technical event to stderr and to the core. Callers hold
// s.mu, which only orders it relative to other notifications.
func (s *Server) logCode(level, code, sessionID string) {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	s.cfg.Log.Log(context.Background(), l, "event", "code", code)
	s.notify(protocol.MethodLog, protocol.LogParams{Level: level, Code: code, SessionID: sessionID})
}

func (s *Server) notify(method string, params any) {
	m, err := protocol.NewNotification(method, params)
	if err == nil {
		s.write(m)
	}
}

func (s *Server) respond(id json.RawMessage, result any) {
	m, err := protocol.NewResult(id, result)
	if err != nil {
		s.respondErr(id, protocol.ErrInternal)
		return
	}
	s.write(m)
}

func (s *Server) respondErr(id json.RawMessage, e *protocol.Error) {
	s.write(protocol.NewErrorResponse(id, e))
}

// write stops the loop when the core's end of stdout is gone; there is
// nobody left to serve.
func (s *Server) write(m *protocol.Message) {
	if err := s.enc.Encode(m); err != nil {
		s.cfg.Log.Error("write failed", "code", "stdout_closed")
		s.cancel()
	}
}
