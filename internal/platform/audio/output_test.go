//go:build cgo

package audio

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/audio/playback"
)

// silence is an endless source of digital silence, so the hardware test is
// inaudible whatever the system volume.
type silence struct{}

func (silence) Next(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return make([]byte, 4800), nil // 100 ms at 24 kHz mono
}

func (silence) Close() error { return nil }

// Touches real hardware, so it only runs when asked for explicitly.
func TestMalgoDefaultOutput(t *testing.T) {
	if os.Getenv("OPENCLICK_SPEAKER_TEST") == "" {
		t.Skip("set OPENCLICK_SPEAKER_TEST=1 to play silence on the real default output")
	}
	ctx := context.Background()
	format := playback.Format{SampleRate: 24000, Channels: 1}
	t0 := time.Now()
	pb, err := playback.New(NewSink()).Play(ctx, silence{}, format)
	if err != nil {
		t.Fatal(err)
	}
	open := time.Since(t0)
	select {
	case <-pb.Started():
	case <-time.After(2 * time.Second):
		t.Fatal("never started")
	}
	first := time.Since(t0)
	time.Sleep(300 * time.Millisecond)
	t1 := time.Now()
	pb.Stop()
	stop := time.Since(t1)
	t.Logf("open %v, first audio %v, stop %v, stats %+v", open, first, stop, pb.Stats())
	if stop > 150*time.Millisecond {
		t.Errorf("Stop took %v, budget 150 ms", stop)
	}
	if err := pb.Wait(); err != playback.ErrStopped {
		t.Errorf("Wait = %v", err)
	}

	// A short clip ends on its own, shortly after its audio.
	t0 = time.Now()
	pb, err = playback.New(NewSink()).Play(ctx, &playback.SliceSource{Chunks: [][]byte{make([]byte, 4800)}}, format)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-pb.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("100 ms clip did not end")
	}
	t.Logf("100 ms clip took %v", time.Since(t0))
	if err := pb.Wait(); err != nil {
		t.Errorf("Wait = %v", err)
	}
}
