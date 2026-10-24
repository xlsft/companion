package wizard

import (
	"context"
	"errors"
	"slices"
	"time"

	"gioui.org/layout"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/models/hw"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/kit"
)

const meterRefresh = 50 * time.Millisecond

// sttState is the model and microphone step. Dropdowns are UI-goroutine
// only; everything else is guarded by Wizard.mu.
type sttState struct {
	modelDD, deviceDD kit.Dropdown

	loaded      bool
	loadErr     string
	profile     hw.Profile
	catalog     models.Catalog // whisper models only
	installed   map[string]bool
	recommended models.Manifest
	selected    string // model id
	devices     []audio.Device
	device      string // "" is the system default

	installing  bool
	progress    models.Progress
	installErr  string
	stopInstall context.CancelFunc

	meter      audio.Stream
	meterErr   string
	transcript string
}

func (t *sttState) load(w *Wizard, _ context.Context) {
	b := w.cfg.Backends.STT
	if b == nil {
		return
	}
	lang := w.cfg.SpeechLanguage()
	w.goStep(func(ctx context.Context) {
		prof := b.Hardware(ctx)
		cat, cerr := b.Catalog()
		inst, ierr := b.Installed()
		devs, derr := b.Devices(ctx)
		rec, _ := hw.Recommend(prof, cat, lang)
		installed := map[string]bool{}
		for _, m := range inst {
			installed[m.ID] = true
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		t.loaded = true
		t.profile, t.devices, t.recommended = prof, devs, rec
		t.installed = installed
		t.catalog = PickerModels(cat, installed, rec.ID, t.selected)
		if t.selected == "" {
			t.selected = rec.ID
		}
		switch {
		case cerr != nil:
			t.loadErr = "catalog_unavailable"
		case ierr != nil:
			t.loadErr = "models_dir_unreadable"
		case derr != nil:
			t.loadErr = "no_audio_devices"
		}
	})
}

func (t *sttState) update(w *Wizard, gtx layout.Context, s snapshot, l i18n.Lang) {
	w.mu.Lock()
	cat, devs := t.catalog, t.devices
	w.mu.Unlock()
	if i := t.modelDD.Update(gtx, len(cat)); i >= 0 {
		w.mu.Lock()
		t.selected, t.installErr = cat[i].ID, ""
		w.mu.Unlock()
	}
	if i := t.deviceDD.Update(gtx, len(devs)+1); i >= 0 {
		id := ""
		if i > 0 {
			id = devs[i-1].ID
		}
		w.mu.Lock()
		changedDev := t.device != id
		t.device = id
		w.mu.Unlock()
		if changedDev {
			t.stopMeter(w)
		}
	}
	if w.clicked(gtx, ActDownload) {
		t.install(w)
	}
	if w.clicked(gtx, ActCancel) {
		w.mu.Lock()
		if t.stopInstall != nil {
			t.stopInstall()
		}
		w.mu.Unlock()
	}
	if w.clicked(gtx, ActMicTest) {
		t.startMeter(w)
	}
	if w.checkClicked(gtx) {
		t.check(w)
	}
}

func (t *sttState) install(w *Wizard) {
	b := w.cfg.Backends.STT
	w.mu.Lock()
	m, ok := t.catalog.Find(t.selected)
	if b == nil || !ok || t.installing {
		w.mu.Unlock()
		return
	}
	t.installing, t.installErr, t.progress = true, "", models.Progress{Total: m.Size}
	w.mu.Unlock()
	w.goStep(func(stepCtx context.Context) {
		ctx, cancel := context.WithCancel(stepCtx)
		defer cancel()
		w.mu.Lock()
		t.stopInstall = cancel
		w.mu.Unlock()
		err := b.Install(ctx, m, func(p models.Progress) {
			w.mu.Lock()
			t.progress = p
			w.mu.Unlock()
			w.cfg.Invalidate()
		})
		w.mu.Lock()
		defer w.mu.Unlock()
		t.installing, t.stopInstall = false, nil
		if err != nil {
			t.installErr = installErrCode(err)
			w.cfg.Log.Warn("setup model install failed", "model", m.ID, "code", t.installErr)
			return
		}
		// Copy on write: views take the map under the lock and read it
		// after releasing it.
		next := make(map[string]bool, len(t.installed)+1)
		for id := range t.installed {
			next[id] = true
		}
		next[m.ID] = true
		t.installed = next
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

// startMeter opens the microphone for the level meter only; frames are
// dropped at once and never stored (FR-VOICE-012).
func (t *sttState) startMeter(w *Wizard) {
	b := w.cfg.Backends.STT
	w.mu.Lock()
	if b == nil || t.meter != nil {
		w.mu.Unlock()
		return
	}
	dev := t.device
	t.meterErr = ""
	w.mu.Unlock()
	w.goStep(func(ctx context.Context) {
		st, err := b.Open(ctx, dev)
		if err != nil {
			w.mu.Lock()
			t.meterErr = "mic_open_failed"
			if errors.Is(err, audio.ErrDeviceNotFound) {
				t.meterErr = "mic_not_found"
			}
			w.mu.Unlock()
			return
		}
		w.mu.Lock()
		t.meter = st
		w.mu.Unlock()
		go func() {
			for range st.Frames() {
			}
			w.mu.Lock()
			if t.meter == st {
				t.meter = nil
				if errors.Is(st.Err(), audio.ErrDeviceLost) {
					t.meterErr = "mic_lost"
				}
			}
			w.mu.Unlock()
			_ = st.Close()
			w.cfg.Invalidate()
		}()
	})
}

func (t *sttState) stopMeter(w *Wizard) {
	w.mu.Lock()
	st := t.meter
	t.meter = nil
	w.mu.Unlock()
	if st != nil {
		go st.Close()
	}
}

// check releases the meter so the check can record, stores the chosen
// device, runs the Gate check and fetches the transcript for display.
func (t *sttState) check(w *Wizard) {
	b := w.cfg.Backends.STT
	w.mu.Lock()
	st, dev := t.meter, t.device
	t.meter, t.transcript = nil, ""
	w.mu.Unlock()
	w.runCheck(readiness.StepSTT, func(ctx context.Context) error {
		if st != nil {
			_ = st.Close()
		}
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
