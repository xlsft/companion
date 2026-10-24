// Command mic is the OC-34 spike: it lists capture devices, records from one
// and prints level statistics. Audio stays in memory and is discarded unless
// -out is given explicitly (FR-VOICE-012).
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"time"

	"github.com/xlsft/openclick/internal/audio"
	paudio "github.com/xlsft/openclick/internal/platform/audio"
)

func main() {
	device := flag.String("device", "", "device ID to record from (default: system default)")
	dur := flag.Duration("dur", 3*time.Second, "recording length")
	out := flag.String("out", "", "write the recording to this WAV file (nothing is saved otherwise)")
	flag.Parse()
	if err := run(*device, *dur, *out); err != nil {
		fmt.Fprintln(os.Stderr, "mic:", err)
		os.Exit(1)
	}
}

func run(device string, dur time.Duration, out string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	c := audio.NewCapturer(paudio.New())

	t0 := time.Now()
	devs, err := c.Devices(ctx)
	if err != nil {
		return fmt.Errorf("devices: %w", err)
	}
	fmt.Printf("capture devices (%d, enumerated in %v):\n", len(devs), time.Since(t0).Round(time.Millisecond))
	for _, d := range devs {
		mark := " "
		if d.Default {
			mark = "*"
		}
		fmt.Printf(" %s %s\n     id=%s\n", mark, d.Name, d.ID)
	}

	recCtx, cancel := context.WithTimeout(ctx, dur+2*time.Second)
	defer cancel()
	t0 = time.Now()
	s, err := c.Open(recCtx, device)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	openTime := time.Since(t0)

	var (
		samples              []int16 // kept only for -out
		frames, clipped      int
		powSum               float64
		minRMS, maxRMS, peak = 0.0, audio.MinDBFS, audio.MinDBFS
		firstFrame           time.Duration
		want                 = int(dur / audio.FrameDuration)
	)
	for frames < want {
		f, ok := <-s.Frames()
		if !ok {
			break
		}
		if frames == 0 {
			firstFrame = time.Since(t0)
		}
		frames++
		lvl := audio.MeasureLevel(f.Samples)
		minRMS, maxRMS, peak = min(minRMS, lvl.RMS), max(maxRMS, lvl.RMS), max(peak, lvl.Peak)
		powSum += math.Pow(10, lvl.RMS/10)
		for _, v := range f.Samples {
			if v == math.MaxInt16 || v == math.MinInt16 {
				clipped++
			}
		}
		if out != "" {
			samples = append(samples, f.Samples...)
		}
	}
	wall := time.Since(t0)
	t1 := time.Now()
	s.Close()
	closeTime := time.Since(t1)
	if err := s.Err(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Printf("stream ended early: %v\n", err)
	}

	fmt.Printf("\nrecording (%s):\n", deviceLabel(device))
	fmt.Printf("  open           %v\n", openTime.Round(time.Millisecond))
	fmt.Printf("  first frame    %v after open call\n", firstFrame.Round(time.Millisecond))
	fmt.Printf("  frames         %d x %v = %v audio, %v wall\n", frames, audio.FrameDuration,
		time.Duration(frames)*audio.FrameDuration, wall.Round(time.Millisecond))
	fmt.Printf("  format         PCM16 mono %d Hz\n", audio.SampleRate)
	if frames > 0 {
		fmt.Printf("  rms dBFS       min %.1f / mean %.1f / max %.1f\n", minRMS, 10*math.Log10(powSum/float64(frames)), maxRMS)
		fmt.Printf("  peak dBFS      %.1f\n", peak)
		fmt.Printf("  clipped        %d samples\n", clipped)
	}
	fmt.Printf("  close          %v\n", closeTime.Round(time.Millisecond))

	if out != "" {
		if err := writeWAV(out, samples); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}
		fmt.Printf("  saved          %s (%d samples)\n", out, len(samples))
	}
	return nil
}

func deviceLabel(id string) string {
	if id == "" {
		return "system default"
	}
	return id
}

func writeWAV(path string, samples []int16) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	data := uint32(len(samples) * 2)
	le := binary.LittleEndian
	hdr := []any{
		[4]byte{'R', 'I', 'F', 'F'}, 36 + data, [4]byte{'W', 'A', 'V', 'E'},
		[4]byte{'f', 'm', 't', ' '}, uint32(16), uint16(1), uint16(1),
		uint32(audio.SampleRate), uint32(audio.SampleRate * 2), uint16(2), uint16(16),
		[4]byte{'d', 'a', 't', 'a'}, data,
	}
	for _, v := range hdr {
		if err := binary.Write(w, le, v); err != nil {
			f.Close()
			return err
		}
	}
	if err := binary.Write(w, le, samples); err != nil {
		f.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
