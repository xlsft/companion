//go:build cgo

package audio

import (
	"context"
	"os"
	"testing"
	"time"

	core "github.com/xlsft/openclick/internal/audio"
)

// Touches real hardware, so it only runs when asked for explicitly.
func TestMalgoDefaultDevice(t *testing.T) {
	if os.Getenv("OPENCLICK_MIC_TEST") == "" {
		t.Skip("set OPENCLICK_MIC_TEST=1 to capture from the real default microphone")
	}
	ctx := context.Background()
	c := core.NewCapturer(New())
	devs, err := c.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d capture devices", len(devs))
	s, err := c.Open(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	timeout := time.After(2 * time.Second)
loop:
	for n < 25 {
		select {
		case _, ok := <-s.Frames():
			if !ok {
				break loop
			}
			n++
		case <-timeout:
			break loop
		}
	}
	start := time.Now()
	s.Close()
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("Close took %v", d)
	}
	if n < 25 {
		t.Fatalf("got %d frames in 2 s, err %v", n, s.Err())
	}
}
