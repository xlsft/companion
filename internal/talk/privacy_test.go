package talk

import (
	"context"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/pkg/contracts"
)

type sinkCall struct {
	ref   contracts.SessionRef
	shots []contracts.ScreenAttachment
}

type fakeSink struct{ calls []sinkCall }

func (f *fakeSink) ScreenAttached(ref contracts.SessionRef, shots []contracts.ScreenAttachment) {
	f.calls = append(f.calls, sinkCall{ref, shots})
}

func paintedCapture() *contracts.Capture {
	c := screenCapture()
	for i := range c.Image.Pix {
		c.Image.Pix[i] = 0xAB
	}
	return c
}

// TestCaptureLifecycle covers FR-SCREEN-006: the pixels live only for the
// request, and reach the sink only when the user opted in.
func TestCaptureLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		keep      func() bool
		provErr   error
		wantImage bool
		wantCalls int
	}{
		{name: "default drops pixels", keep: nil, wantCalls: 1},
		{name: "setting off", keep: func() bool { return false }, wantCalls: 1},
		{name: "setting on hands a model-sized copy", keep: func() bool { return true }, wantImage: true, wantCalls: 1},
		{name: "provider error still releases", keep: func() bool { return true }, provErr: errors.New("no key"), wantCalls: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &scriptedProvider{}
			sink := &fakeSink{}
			o := &Orchestrator{
				Provider:    func(context.Context) (llm.Provider, error) { return p, tc.provErr },
				Attachments: sink, KeepScreenshots: tc.keep,
			}
			shot := paintedCapture()
			pix := shot.Image.Pix
			_, err := o.Reason(context.Background(), ref, session.Request{Text: "q", Captures: []*contracts.Capture{shot}}, (&events{}).emit)
			if (err != nil) != (tc.provErr != nil) {
				t.Fatalf("err %v", err)
			}
			if shot.Image != nil {
				t.Error("capture not released")
			}
			for _, b := range pix {
				if b != 0 {
					t.Fatal("pixel buffer not zeroed")
				}
			}
			if len(sink.calls) != tc.wantCalls {
				t.Fatalf("sink calls %d, want %d", len(sink.calls), tc.wantCalls)
			}
			if tc.wantCalls == 0 {
				return
			}
			got := sink.calls[0]
			if got.ref != ref || len(got.shots) != 1 || got.shots[0].Meta.ID != "cap1" || got.shots[0].Meta.Model.W == 0 {
				t.Fatalf("sink %+v", got)
			}
			img := got.shots[0].Image
			if (img != nil) != tc.wantImage {
				t.Fatalf("image kept = %v, want %v", img != nil, tc.wantImage)
			}
			if img != nil {
				if img.Bounds().Dx() != got.shots[0].Meta.Model.W || img.Bounds().Dx() > defaultMaxModelSide {
					t.Errorf("kept image %v is not model-sized %+v", img.Bounds(), got.shots[0].Meta.Model)
				}
				if img.RGBAAt(1, 1) != (color.RGBA{0xAB, 0xAB, 0xAB, 0xAB}) {
					t.Error("kept copy was zeroed with the capture")
				}
			}
		})
	}
}

// A capture small enough to be sent unscaled aliases the sent image; the
// kept copy must still survive the release.
func TestSmallCaptureKeptCopyIndependent(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 50))
	for i := range img.Pix {
		img.Pix[i] = 0x7F
	}
	shot := &contracts.Capture{Meta: contracts.CaptureMetadata{ID: "c"}, Image: img}
	sink := &fakeSink{}
	o := &Orchestrator{
		Provider:    func(context.Context) (llm.Provider, error) { return &scriptedProvider{}, nil },
		Attachments: sink, KeepScreenshots: func() bool { return true },
	}
	if _, err := o.Reason(context.Background(), ref, session.Request{Text: "q", Captures: []*contracts.Capture{shot}}, (&events{}).emit); err != nil {
		t.Fatal(err)
	}
	if got := sink.calls[0].shots[0].Image; got == nil || got.Pix[0] != 0x7F {
		t.Fatal("kept copy aliased the released buffer")
	}
}
