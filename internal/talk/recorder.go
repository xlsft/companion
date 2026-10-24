package talk

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/xlsft/openclick/internal/audio"
	"github.com/xlsft/openclick/internal/session"
	"github.com/xlsft/openclick/internal/stt"
	"github.com/xlsft/openclick/internal/stt/supervisor"
	"github.com/xlsft/openclick/pkg/contracts"
)

// STTSession is one recognition session; *stt.Session and the
// supervisor's session satisfy it.
type STTSession interface {
	Write(pcm []int16) error
	Finish(ctx context.Context) (stt.Final, error)
	Cancel()
	Partials() <-chan stt.Partial
	Endpoint() <-chan stt.EndpointEvent
}

// STT starts recognition sessions.
type STT func(ctx context.Context, opts ...stt.SessionOption) (STTSession, error)

// ClientSTT adapts a directly owned sidecar client.
func ClientSTT(c *stt.Client) STT {
	return func(ctx context.Context, opts ...stt.SessionOption) (STTSession, error) {
		return c.StartSession(ctx, opts...)
	}
}

// SupervisedSTT adapts the STT supervisor, the normal wiring in the app.
func SupervisedSTT(s *supervisor.Supervisor) STT {
	return func(ctx context.Context, opts ...stt.SessionOption) (STTSession, error) {
		return s.StartSession(ctx, opts...)
	}
}

// MicRecorder streams the microphone into the local STT sidecar (OC-58).
// Audio only ever lives in memory between the device and the sidecar
// (FR-VOICE-012).
type MicRecorder struct {
	Mic audio.Capturer
	STT STT
	// Device returns the selected microphone; read per session so a change
	// applies to the next request (FR-STT-009). Nil means system default.
	Device   func() string
	Language string

	mu  sync.Mutex
	cur *recording
}

type recording struct {
	ref    contracts.SessionRef
	stream audio.Stream
	sess   STTSession
	pumped chan error // result of the mic → STT pump
	cancel context.CancelFunc
}

var _ Recorder = (*MicRecorder)(nil)

var ErrNotRecording = errors.New("talk: no recording for this session")

func (r *MicRecorder) Start(ctx context.Context, ref contracts.SessionRef, emit session.Emitter) error {
	device := ""
	if r.Device != nil {
		device = r.Device()
	}
	// The sidecar's endpointer decides when hands-free speech has ended;
	// the session manager ignores UtteranceEnd in other modes.
	opts := []stt.SessionOption{stt.WithPartials(true), stt.WithVAD(stt.VADOptions{})}
	if r.Language != "" {
		opts = append(opts, stt.WithLanguage(r.Language))
	}
	sess, err := r.STT(ctx, opts...)
	if err != nil {
		return fmt.Errorf("start stt session: %w", err)
	}
	// The mic must keep running past Start's return, until Finish or the
	// generation ends; ctx is the generation context, so derive from it.
	micCtx, cancel := context.WithCancel(ctx)
	stream, err := r.Mic.Open(micCtx, device)
	if err != nil {
		cancel()
		sess.Cancel()
		return fmt.Errorf("open microphone: %w", err)
	}
	rec := &recording{ref: ref, stream: stream, sess: sess, pumped: make(chan error, 1), cancel: cancel}

	r.mu.Lock()
	if old := r.cur; old != nil {
		old.abort()
	}
	r.cur = rec
	r.mu.Unlock()

	go func() {
		var err error
		for f := range stream.Frames() {
			if werr := sess.Write(f.Samples); werr != nil {
				err = werr
				break
			}
		}
		if err == nil {
			err = stream.Err()
		}
		rec.pumped <- err
	}()
	go relay(micCtx, sess, emit)
	// A generation cancelled before Finish must not leave the sidecar
	// session open.
	go func() {
		<-micCtx.Done()
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.cur == rec {
			r.cur = nil
			rec.abort()
		}
	}()
	return nil
}

// relay forwards partial transcripts and the end of speech for hands-free
// mode. Partials are display-only and never trigger actions (FR-STT-008).
func relay(ctx context.Context, sess STTSession, emit session.Emitter) {
	partials, endpoint := sess.Partials(), sess.Endpoint()
	for partials != nil || endpoint != nil {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-partials:
			if !ok {
				partials = nil
				continue
			}
			emit(session.TranscriptPartial{Text: p.Text})
		case _, ok := <-endpoint:
			if !ok {
				endpoint = nil
				continue
			}
			emit(session.UtteranceEnd{})
		}
	}
}

func (r *MicRecorder) Finish(ctx context.Context, ref contracts.SessionRef) (string, error) {
	r.mu.Lock()
	rec := r.cur
	if rec == nil || rec.ref != ref {
		r.mu.Unlock()
		return "", ErrNotRecording
	}
	r.cur = nil
	r.mu.Unlock()

	// Stop the device first so the sidecar receives every captured frame
	// before finish.
	_ = rec.stream.Close()
	pumpErr := <-rec.pumped
	defer rec.cancel()
	if pumpErr != nil && !errors.Is(pumpErr, context.Canceled) {
		rec.sess.Cancel()
		return "", fmt.Errorf("microphone: %w", pumpErr)
	}
	final, err := rec.sess.Finish(ctx)
	if err != nil {
		return "", err
	}
	return final.Text, nil
}

func (rec *recording) abort() {
	rec.cancel()
	_ = rec.stream.Close()
	rec.sess.Cancel()
}
