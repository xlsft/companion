package logging

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ErrorEvent is one technical event kept for the diagnostics screen.
type ErrorEvent struct {
	Time      time.Time `json:"time"`
	Level     string    `json:"level"`
	Component string    `json:"component,omitempty"`
	Code      string    `json:"code,omitempty"`
	Message   string    `json:"message"`
}

// DefaultErrorLogSize is the number of events the diagnostics screen shows.
const DefaultErrorLogSize = 200

// ErrorLog is a fixed-size in-memory ring buffer of recent technical events.
type ErrorLog struct {
	mu   sync.Mutex
	buf  []ErrorEvent
	next int
	full bool
}

// NewErrorLog returns a ring buffer holding the last n events (n<=0 means
// DefaultErrorLogSize).
func NewErrorLog(n int) *ErrorLog {
	if n <= 0 {
		n = DefaultErrorLogSize
	}
	return &ErrorLog{buf: make([]ErrorEvent, n)}
}

// Add stores e, overwriting the oldest event when full. Strings are redacted
// again here so direct callers cannot leak.
func (l *ErrorLog) Add(e ErrorEvent) {
	e.Message = RedactString(e.Message)
	e.Component = RedactString(e.Component)
	e.Code = RedactString(e.Code)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf[l.next] = e
	l.next = (l.next + 1) % len(l.buf)
	if l.next == 0 {
		l.full = true
	}
}

// Snapshot returns the stored events, oldest first.
func (l *ErrorLog) Snapshot() []ErrorEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.full {
		return append([]ErrorEvent(nil), l.buf[:l.next]...)
	}
	out := make([]ErrorEvent, 0, len(l.buf))
	out = append(out, l.buf[l.next:]...)
	return append(out, l.buf[:l.next]...)
}

// Option configures a RedactingHandler.
type Option func(*RedactingHandler)

// WithErrorLog feeds events of level >= Warn into log.
func WithErrorLog(log *ErrorLog) Option {
	return func(h *RedactingHandler) { h.errs = log }
}

// RedactingHandler wraps a slog.Handler, masking sensitive attributes and
// secret-looking strings before they reach it.
type RedactingHandler struct {
	inner  slog.Handler // may be nil: only feed the error log
	errs   *ErrorLog
	preset []slog.Attr // WithAttrs attributes, kept for component/code lookup
}

// NewRedactingHandler wraps inner. inner may be nil when only the error log
// is wanted.
func NewRedactingHandler(inner slog.Handler, opts ...Option) *RedactingHandler {
	h := &RedactingHandler{inner: inner}
	for _, o := range opts {
		o(h)
	}
	return h
}

func (h *RedactingHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	if h.errs != nil && lvl >= slog.LevelWarn {
		return true
	}
	return h.inner != nil && h.inner.Enabled(ctx, lvl)
}

func (h *RedactingHandler) Handle(ctx context.Context, r slog.Record) error {
	msg := RedactString(r.Message)
	var attrs []slog.Attr
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, redactAttr(a))
		return true
	})

	if h.errs != nil && r.Level >= slog.LevelWarn {
		ev := ErrorEvent{Time: r.Time, Level: r.Level.String(), Message: msg}
		if ev.Time.IsZero() {
			ev.Time = time.Now()
		}
		for _, a := range append(append([]slog.Attr(nil), h.preset...), attrs...) {
			switch a.Key {
			case "component":
				ev.Component = a.Value.String()
			case "code":
				ev.Code = a.Value.String()
			}
		}
		h.errs.Add(ev)
	}

	if h.inner == nil || !h.inner.Enabled(ctx, r.Level) {
		return nil
	}
	nr := slog.NewRecord(r.Time, r.Level, msg, r.PC)
	nr.AddAttrs(attrs...)
	return h.inner.Handle(ctx, nr)
}

func (h *RedactingHandler) WithAttrs(as []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(as))
	for i, a := range as {
		red[i] = redactAttr(a)
	}
	nh := *h
	nh.preset = append(append([]slog.Attr(nil), h.preset...), red...)
	if h.inner != nil {
		nh.inner = h.inner.WithAttrs(red)
	}
	return &nh
}

func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	nh := *h
	if h.inner != nil {
		nh.inner = h.inner.WithGroup(name)
	}
	return &nh
}

func redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	if IsSensitiveKey(a.Key) {
		return slog.String(a.Key, Masked)
	}
	switch v.Kind() {
	case slog.KindGroup:
		g := v.Group()
		out := make([]slog.Attr, len(g))
		for i, ga := range g {
			out[i] = redactAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	case slog.KindString:
		return slog.String(a.Key, RedactString(v.String()))
	case slog.KindAny:
		// Arbitrary values may embed anything; stringify and scrub.
		return slog.String(a.Key, RedactString(fmt.Sprintf("%+v", v.Any())))
	default:
		return slog.Attr{Key: a.Key, Value: v}
	}
}
