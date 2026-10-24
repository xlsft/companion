package dictation

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/stt"
)

// STTSession is the part of a recognition session dictation uses;
// *stt.Session and the supervisor's session satisfy it, whatever engine
// the sidecar runs.
type STTSession interface {
	Write(pcm []int16) error
	Finish(ctx context.Context) (stt.Final, error)
	Cancel()
}

// STT starts a recognition session.
type STT func(ctx context.Context, opts ...stt.SessionOption) (STTSession, error)

// MicRecorder streams the microphone into the local recogniser. Audio
// stays in memory between the device and the sidecar.
type MicRecorder struct {
	Mic audio.Capturer
	STT STT
	// Device and Language are read per dictation, so a change in Settings
	// applies to the next one; nil or "" mean the defaults.
	Device   func() string
	Language func() string

	mu  sync.Mutex
	cur *recording
}

type recording struct {
	stream audio.Stream
	sess   STTSession
	pumped chan error
	cancel context.CancelFunc
}

var _ Recorder = (*MicRecorder)(nil)

var errNotRecording = errors.New("dictation: not recording")

func (r *MicRecorder) Start(ctx context.Context) error {
	// Verbatim: no partials to show, no endpointing; the user stops it.
	opts := []stt.SessionOption{stt.WithPartials(false)}
	if r.Language != nil {
		if l := r.Language(); l != "" {
			opts = append(opts, stt.WithLanguage(l))
		}
	}
	sess, err := r.STT(ctx, opts...)
	if err != nil {
		return fmt.Errorf("dictation: start recognition: %w", err)
	}
	device := ""
	if r.Device != nil {
		device = r.Device()
	}
	micCtx, cancel := context.WithCancel(ctx)
	stream, err := r.Mic.Open(micCtx, device)
	if err != nil {
		cancel()
		sess.Cancel()
		return fmt.Errorf("%w: %w", ErrMicrophone, err)
	}
	rec := &recording{stream: stream, sess: sess, pumped: make(chan error, 1), cancel: cancel}
	r.mu.Lock()
	if old := r.cur; old != nil {
		old.abort()
	}
	r.cur = rec
	r.mu.Unlock()
	go func() {
		var err error
		for f := range stream.Frames() {
			if err = sess.Write(f.Samples); err != nil {
				break
			}
		}
		if err == nil {
			err = stream.Err()
		}
		rec.pumped <- err
	}()
	return nil
}

func (r *MicRecorder) take() *recording {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.cur
	r.cur = nil
	return rec
}

func (r *MicRecorder) Finish(ctx context.Context) (string, error) {
	rec := r.take()
	if rec == nil {
		return "", errNotRecording
	}
	defer rec.cancel()
	// Close the device first so every captured frame reaches the
	// recogniser before it decodes.
	_ = rec.stream.Close()
	if err := <-rec.pumped; err != nil && !errors.Is(err, context.Canceled) {
		rec.sess.Cancel()
		return "", fmt.Errorf("%w: %w", ErrMicrophone, err)
	}
	final, err := rec.sess.Finish(ctx)
	if err != nil {
		return "", err
	}
	return final.Text, nil
}

func (r *MicRecorder) Cancel() {
	if rec := r.take(); rec != nil {
		rec.abort()
	}
}

func (rec *recording) abort() {
	rec.cancel()
	_ = rec.stream.Close()
	rec.sess.Cancel()
}
