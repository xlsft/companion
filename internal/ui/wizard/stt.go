package wizard

import (
	"context"
	"errors"
	"slices"

	"gioui.org/layout"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/kit"
)

// sttState is the microphone of the voice screen; the speech model itself
// downloads in the background (speechModel). The dropdown is UI-goroutine
// only; everything else is guarded by Wizard.mu.
type sttState struct {
	deviceDD kit.Dropdown
	reading  bool // UI goroutine: the user reads the phrase (no loopback)

	loaded     bool
	devices    []audio.Device
	device     string // "" is the system default
	transcript string
}

func (t *sttState) load(w *Wizard, _ context.Context) {
	b := w.cfg.Backends.STT
	if b == nil {
		return
	}
	w.goStep(func(ctx context.Context) {
		devs, _ := b.Devices(ctx)
		w.mu.Lock()
		defer w.mu.Unlock()
		t.loaded, t.devices = true, devs
	})
}

func (t *sttState) update(w *Wizard, gtx layout.Context, s snapshot, l i18n.Lang) {
	w.mu.Lock()
	devs := t.devices
	w.mu.Unlock()
	if i := t.deviceDD.Update(gtx, len(devs)+1); i >= 0 {
		id := ""
		if i > 0 {
			id = devs[i-1].ID
		}
		w.mu.Lock()
		t.device = id
		w.mu.Unlock()
		if b := w.cfg.Backends.STT; b != nil {
			w.goStep(func(ctx context.Context) { _ = b.SelectDevice(ctx, id) })
		}
	}
}

// check is the fallback when the microphone did not hear the voice (e.g.
// headphones): the user reads the phrase, and the transcript is fetched
// for display.
func (t *sttState) check(w *Wizard) {
	b := w.cfg.Backends.STT
	w.mu.Lock()
	dev := t.device
	t.transcript = ""
	w.mu.Unlock()
	w.runCheck(readiness.StepSTT, func(ctx context.Context) error {
		if b == nil {
			return nil
		}
		return b.SelectDevice(ctx, dev)
	}, func(context.Context) {
		if b == nil {
			return
		}
		tr := b.LastTranscript()
		w.mu.Lock()
		t.transcript = tr
		w.mu.Unlock()
	})
}

// PickerModels are the catalogue entries a model picker offers, in
// catalogue order (primary models first): every listed model, plus hidden
// ones that are recommended, selected or installed, so whatever the user
// has or is told to get is always visible.
func PickerModels(cat models.Catalog, installed map[string]bool, keep ...string) models.Catalog {
	var out models.Catalog
	for _, m := range cat {
		if !m.Hidden || installed[m.ID] || slices.Contains(keep, m.ID) {
			out = append(out, m)
		}
	}
	return out
}

func installErrCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, models.ErrChecksum), errors.Is(err, models.ErrChecksumMismatch):
		return "checksum_mismatch"
	case errors.Is(err, models.ErrInsufficientSpace):
		return "insufficient_space"
	case errors.Is(err, models.ErrSmokeFailed):
		return "smoke_failed"
	}
	return "download_failed"
}

// size formats a byte count the way the prototypes do: GB with two
// decimals from 1 GB, whole MB below.
func size(l i18n.Lang, n int64) string {
	if n >= 1<<30 {
		return i18n.Tf(l, "wiz.stt.gb", float64(n)/(1<<30))
	}
	return i18n.Tf(l, "wiz.stt.mb", float64(n)/(1<<20))
}
