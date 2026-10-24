// Package stt is the core-side client of the STT sidecar
// (docs/stt-protocol.md). It spawns the sidecar, speaks the protocol and
// exposes streaming sessions. Restart policy lives in the supervisor
// (OC-36); this package only reports that the sidecar died.
package stt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xlsft/openclick/internal/stt/protocol"
)

var (
	// ErrSidecarExited is returned by every pending and later call once the
	// sidecar process or its stdout is gone.
	ErrSidecarExited = errors.New("stt: sidecar exited")
	// ErrSessionClosed is returned when using a finished or cancelled session.
	ErrSessionClosed = errors.New("stt: session closed")
	// ErrCancelled is returned by Finish when the session was cancelled; it
	// matches the sidecar's session-cancelled error code too.
	ErrCancelled = protocol.ErrSessionCancelled
)

type Config struct {
	// Path is the sidecar binary; Args are passed through (e.g. -engine).
	Path string
	Args []string
	// Stderr receives the sidecar's technical log; nil discards it.
	Stderr io.Writer
	Init   protocol.InitializeParams
	// Logger receives sidecar log notifications (codes only); nil discards.
	Logger *slog.Logger
}

type Client struct {
	enc    *protocol.Encoder
	stdin  io.Closer
	cmd    *exec.Cmd
	log    *slog.Logger
	prefix string
	nextID atomic.Uint64
	nextSN atomic.Uint64
	info   protocol.InitializeResult

	done chan struct{}
	err  error // set before done is closed

	mu       sync.Mutex
	pending  map[uint64]chan *protocol.Message
	sessions map[string]*Session
}

// Start spawns the sidecar and initializes it. ctx bounds the process
// lifetime (exec.CommandContext kills it when ctx ends), not just startup.
func Start(ctx context.Context, cfg Config) (*Client, error) {
	cmd := exec.CommandContext(ctx, cfg.Path, cfg.Args...)
	cmd.Stderr = cfg.Stderr
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("stt: start sidecar: %w", err)
	}
	c := newClient(stdout, stdin, cmd, cfg.Logger)
	if _, err := c.Initialize(ctx, cfg.Init); err != nil {
		_ = cmd.Process.Kill()
		<-c.done
		return nil, err
	}
	return c, nil
}

// NewClient speaks the protocol over an existing transport, e.g. an
// in-process server. Closing w must make the peer stop.
func NewClient(r io.Reader, w io.WriteCloser, log *slog.Logger) *Client {
	return newClient(r, w, nil, log)
}

func newClient(r io.Reader, w io.WriteCloser, cmd *exec.Cmd, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	c := &Client{
		enc:      protocol.NewEncoder(w),
		stdin:    w,
		cmd:      cmd,
		log:      log,
		prefix:   hex.EncodeToString(b[:]),
		done:     make(chan struct{}),
		pending:  map[uint64]chan *protocol.Message{},
		sessions: map[string]*Session{},
	}
	go c.read(r)
	return c
}

// Info is the initialize result.
func (c *Client) Info() protocol.InitializeResult { return c.info }

// Done is closed when the sidecar connection ends; Err then says why.
func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

func (c *Client) read(r io.Reader) {
	dec := protocol.NewDecoder(r)
	var readErr error
	for {
		m, err := dec.Decode()
		if err != nil {
			if protocol.IsTerminal(err) {
				readErr = err
				break
			}
			c.log.Warn("stt sidecar sent malformed message", "code", "client_parse_error")
			continue
		}
		switch {
		case m.IsResponse():
			id, ok := protocol.ParseIntID(m.ID)
			c.mu.Lock()
			ch := c.pending[id]
			c.mu.Unlock()
			if ok && ch != nil {
				ch <- m // buffered; each id gets one response
			}
		case m.IsNotification():
			c.route(m)
		}
	}

	exit := ErrSidecarExited
	if c.cmd != nil {
		if werr := c.cmd.Wait(); werr != nil {
			exit = fmt.Errorf("%w: %w", ErrSidecarExited, werr)
		}
	} else if readErr != nil && readErr != io.EOF {
		exit = fmt.Errorf("%w: %w", ErrSidecarExited, readErr)
	}
	c.mu.Lock()
	c.err = exit
	sessions := c.sessions
	c.sessions = map[string]*Session{}
	c.mu.Unlock()
	close(c.done)
	for _, s := range sessions {
		s.end()
	}
}

func (c *Client) route(m *protocol.Message) {
	switch m.Method {
	case protocol.MethodLog:
		var p protocol.LogParams
		if m.DecodeParams(&p) == nil {
			var l slog.Level
			_ = l.UnmarshalText([]byte(p.Level))
			c.log.Log(context.Background(), l, "stt sidecar event", "code", p.Code, "session", p.SessionID)
		}
	case protocol.MethodSessionPartial:
		var p protocol.PartialParams
		if m.DecodeParams(&p) == nil {
			if s := c.session(p.SessionID); s != nil {
				s.deliverPartial(Partial{Seq: p.Seq, Text: p.Text, Stable: p.Stable})
			}
		}
	case protocol.MethodSessionEndpoint:
		var p protocol.EndpointParams
		if m.DecodeParams(&p) == nil {
			if s := c.session(p.SessionID); s != nil {
				s.deliverEndpoint(EndpointEvent{Reason: p.Reason, At: time.Duration(p.AtMS) * time.Millisecond})
			}
		}
	case protocol.MethodSessionVAD:
		var p protocol.VADParams
		if m.DecodeParams(&p) == nil {
			if s := c.session(p.SessionID); s != nil {
				s.deliverSpeech(p.Speech)
			}
		}
	}
}

func (c *Client) session(id string) *Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[id]
}

func (c *Client) call(ctx context.Context, method string, params, result any, abort <-chan struct{}) error {
	id := c.nextID.Add(1)
	ch := make(chan *protocol.Message, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return c.err
	default:
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	m, err := protocol.NewRequest(protocol.IntID(id), method, params)
	if err != nil {
		return err
	}
	if err := c.send(m); err != nil {
		return err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		return resp.DecodeResult(result)
	case <-c.done:
		return c.err
	case <-ctx.Done():
		return ctx.Err()
	case <-abort:
		return ErrCancelled
	}
}

// notifyRequest sends a request whose response nobody waits for; the reader
// drops it because no pending entry exists.
func (c *Client) notifyRequest(method string, params any) {
	m, err := protocol.NewRequest(protocol.IntID(c.nextID.Add(1)), method, params)
	if err == nil {
		_ = c.send(m)
	}
}

func (c *Client) send(m *protocol.Message) error {
	if err := c.enc.Encode(m); err != nil {
		select {
		case <-c.done:
			return c.err
		default:
			return fmt.Errorf("%w: write: %v", ErrSidecarExited, err)
		}
	}
	return nil
}

func (c *Client) Initialize(ctx context.Context, p protocol.InitializeParams) (protocol.InitializeResult, error) {
	var res protocol.InitializeResult
	if err := c.call(ctx, protocol.MethodInitialize, p, &res, nil); err != nil {
		return res, err
	}
	c.info = res
	return res, nil
}

func (c *Client) Health(ctx context.Context) (protocol.HealthResult, error) {
	var res protocol.HealthResult
	err := c.call(ctx, protocol.MethodHealth, nil, &res, nil)
	return res, err
}

// Close asks the sidecar to shut down and waits for it to exit; when ctx
// expires first the process is killed.
func (c *Client) Close(ctx context.Context) error {
	select {
	case <-c.done:
		return nil
	default:
	}
	_ = c.call(ctx, protocol.MethodShutdown, nil, nil, nil)
	_ = c.stdin.Close()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-c.done
		return ctx.Err()
	}
}

type SessionOption func(*protocol.SessionStartParams)

// WithPartials toggles rolling partials (default on).
func WithPartials(on bool) SessionOption {
	return func(p *protocol.SessionStartParams) { p.Partials = on }
}

// WithLanguage overrides the initialize language for one session.
func WithLanguage(code string) SessionOption {
	return func(p *protocol.SessionStartParams) { p.Language = code }
}

// VADOptions are the endpointing parameters of WithVAD; zero fields use the
// sidecar defaults (end silence 800 ms, leading silence 5 s, min speech
// 120 ms, margin 12 dB, max duration = the sidecar's utterance limit).
type VADOptions struct {
	EndSilence        time.Duration
	MaxLeadingSilence time.Duration
	MinSpeech         time.Duration
	MarginDB          float64
	MaxDuration       time.Duration
}

// WithVAD turns on endpointing (hands-free): the sidecar then sends one
// event on Session.Endpoint() when capture should stop.
func WithVAD(o VADOptions) SessionOption {
	return func(p *protocol.SessionStartParams) {
		p.VAD = &protocol.VADOptions{
			EndSilenceMS:        int(o.EndSilence / time.Millisecond),
			MaxLeadingSilenceMS: int(o.MaxLeadingSilence / time.Millisecond),
			MinSpeechMS:         int(o.MinSpeech / time.Millisecond),
			MarginDB:            o.MarginDB,
			MaxDurationMS:       int(o.MaxDuration / time.Millisecond),
		}
	}
}

// StartSession opens a streaming session. Each call uses a fresh id so
// events of an earlier, cancelled session can never leak into it.
func (c *Client) StartSession(ctx context.Context, opts ...SessionOption) (*Session, error) {
	p := protocol.SessionStartParams{
		SessionID:  c.prefix + "-" + strconv.FormatUint(c.nextSN.Add(1), 10),
		SampleRate: protocol.SampleRate,
		Format:     protocol.FormatPCMS16LE,
		Partials:   true,
	}
	for _, o := range opts {
		o(&p)
	}
	s := &Session{
		c:         c,
		id:        p.SessionID,
		partials:  make(chan Partial, 8),
		speech:    make(chan bool, 16),
		endpoint:  make(chan EndpointEvent, 1),
		cancelled: make(chan struct{}),
	}
	// Register before the request so a VAD event racing the response is
	// not lost.
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return nil, c.err
	}
	c.sessions[s.id] = s
	c.mu.Unlock()
	if err := c.call(ctx, protocol.MethodSessionStart, p, nil, nil); err != nil {
		s.end()
		return nil, err
	}
	return s, nil
}
