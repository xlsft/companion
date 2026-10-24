package tts

import (
	"context"
	"errors"
	"io"
	"sync"
)

const readBufSize = 8192

// readerStream pumps an io.ReadCloser in a goroutine so Next can honour its
// own ctx even though a body Read cannot be interrupted directly; cancelling
// the stream ctx is what unblocks the read (it aborts the HTTP request).
type readerStream struct {
	ch     chan result
	cancel context.CancelFunc
	done   chan struct{}
	format Format

	mu  sync.Mutex
	err error // sticky terminal error
}

type result struct {
	data []byte
	err  error
}

// newReaderStream takes ownership of body. streamCtx must be the context the
// request was made with and cancel its cancel func. parent is the caller's
// ctx, used only to classify a cancellation. onDone runs once after the pump
// exits (provider registry cleanup).
func newReaderStream(streamCtx context.Context, cancel context.CancelFunc, parent context.Context,
	body io.ReadCloser, format Format, provider string, onDone func()) *readerStream {
	s := &readerStream{
		ch:     make(chan result, 2),
		cancel: cancel,
		done:   make(chan struct{}),
		format: format,
	}
	frame := 1
	if format.Container == ContainerPCM16 {
		frame = 2 * format.Channels // never split a sample across chunks
	}
	go func() {
		defer close(s.done)
		defer onDone()
		defer body.Close()
		defer cancel()
		send := func(r result) bool {
			select {
			case s.ch <- r:
				return true
			case <-streamCtx.Done():
				return false
			}
		}
		fail := func(err error) {
			switch {
			case parent.Err() != nil:
				err = parent.Err()
			case streamCtx.Err() != nil:
				err = context.Canceled
			default:
				err = &Error{Kind: ErrUnavailable, Provider: provider, Cause: stripURL(err)}
			}
			send(result{err: err})
		}
		buf := make([]byte, readBufSize)
		var carry []byte
		for {
			n, err := body.Read(buf)
			if n > 0 {
				data := append(carry, buf[:n]...)
				carry = nil
				if rem := len(data) % frame; rem != 0 {
					carry = append([]byte(nil), data[len(data)-rem:]...)
					data = data[:len(data)-rem]
				}
				if len(data) > 0 {
					out := append([]byte(nil), data...)
					if !send(result{data: out}) {
						return
					}
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					if len(carry) > 0 {
						fail(io.ErrUnexpectedEOF) // truncated sample means truncated body
						return
					}
					send(result{err: io.EOF})
					return
				}
				fail(err)
				return
			}
		}
	}()
	return s
}

func (s *readerStream) deliver(r result) (Chunk, error) {
	if r.err != nil {
		s.mu.Lock()
		s.err = r.err
		s.mu.Unlock()
		return Chunk{}, r.err
	}
	return Chunk{Data: r.data, Format: s.format}, nil
}

func (s *readerStream) Next(ctx context.Context) (Chunk, error) {
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return Chunk{}, err
	}
	select {
	case r := <-s.ch:
		return s.deliver(r)
	case <-ctx.Done():
		return Chunk{}, ctx.Err()
	case <-s.done:
		// Pump ended; drain whatever it managed to queue before giving up.
		select {
		case r := <-s.ch:
			return s.deliver(r)
		default:
		}
		return s.deliver(result{err: context.Canceled})
	}
}

func (s *readerStream) Close() error {
	s.cancel()
	<-s.done
	return nil
}
