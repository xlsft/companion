package wizard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/models/hw"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

const meterRefresh = 50 * time.Millisecond

// sttState is the model and microphone step. Dropdowns are UI-goroutine
// only; everything else is guarded by Wizard.mu.
type sttState struct {
	modelDD, deviceDD dropdown

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
		var whisper models.Catalog
		for _, m := range cat {
			if m.Family == "whisper" {
				whisper = append(whisper, m)
			}
		}
		rec, _ := hw.Recommend(prof, cat, lang)
		w.mu.Lock()
		defer w.mu.Unlock()
		t.loaded = true
		t.profile, t.catalog, t.devices, t.recommended = prof, whisper, devs, rec
		t.installed = map[string]bool{}
		for _, m := range inst {
			t.installed[m.ID] = true
		}
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
	if i := t.modelDD.update(gtx, len(cat)); i >= 0 {
		w.mu.Lock()
		t.selected, t.installErr = cat[i].ID, ""
		w.mu.Unlock()
	}
	if i := t.deviceDD.update(gtx, len(devs)+1); i >= 0 {
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
		t.installed[m.ID] = true
	})
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

func mb(n int64) string { return fmt.Sprintf("%.0f MB", float64(n)/(1<<20)) }

func langsOf(l i18n.Lang, m models.Manifest) string {
	if m.Multilingual() {
		return i18n.T(l, "wiz.stt.multilingual")
	}
	return strings.Join(m.Languages, ", ")
}

func hardwareSummary(l i18n.Lang, p hw.Profile) string {
	cpu := kit.OrDash(p.CPU.Model)
	gpu := i18n.T(l, "wiz.stt.gpu.none")
	switch {
	case p.GPU.Usable():
		gpu = strings.Join(p.GPU.Backends, "+")
	case !p.GPU.Known:
		gpu = i18n.T(l, "wiz.stt.gpu.unknown")
	}
	return i18n.Tf(l, "wiz.stt.hardware", cpu, p.CPU.Cores, float64(p.Memory.TotalBytes)/(1<<30), gpu)
}

// meterFraction maps RMS dBFS onto [0, 1] over a 60 dB range.
func meterFraction(dbfs float64) float32 {
	return float32(max(0, min(1, (dbfs+60)/60)))
}

func fmtETA(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func (t *sttState) rows(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) []layout.Widget {
	b := w.cfg.Backends.STT
	if b == nil {
		return []layout.Widget{w.text(th.StatusLabel(theme.StatusError, i18n.T(l, "wiz.notWired"))), gap(12),
			w.checkButton(th, l, s, readiness.StepSTT, true)}
	}
	w.mu.Lock()
	loaded, loadErr, prof, cat, inst := t.loaded, t.loadErr, t.profile, t.catalog, t.installed
	rec, selected, devs, device := t.recommended, t.selected, t.devices, t.device
	installing, prog, installErr := t.installing, t.progress, t.installErr
	meter, meterErr, transcript := t.meter, t.meterErr, t.transcript
	w.mu.Unlock()
	if !loaded {
		return []layout.Widget{w.text(kit.Muted(th, i18n.T(l, "wiz.loading")))}
	}
	rows := []layout.Widget{
		w.text(kit.Subheading(th, i18n.T(l, "wiz.stt.model"))),
		w.text(kit.Body(th, hardwareSummary(l, prof))),
	}
	if rec.ID != "" {
		rows = append(rows, w.text(kit.Body(th, i18n.Tf(l, "wiz.stt.recommended", rec.Name, mb(rec.Size), langsOf(l, rec)))))
	}
	if loadErr != "" {
		rows = append(rows, w.text(th.StatusLabel(theme.StatusError, loadErr)), w.text(kit.Muted(th, fixText(l, loadErr))))
	}
	labels := make([]string, len(cat))
	sel := -1
	for i, m := range cat {
		labels[i] = fmt.Sprintf("%s · %s · %s", m.Name, mb(m.Size), langsOf(l, m))
		if m.ID == rec.ID {
			labels[i] += " · " + i18n.T(l, "wiz.stt.rec")
		}
		if inst[m.ID] {
			labels[i] += " · " + i18n.T(l, "wiz.stt.installed")
		}
		if m.ID == selected {
			sel = i
		}
	}
	rows = append(rows, gap(6), func(gtx layout.Context) layout.Dimensions {
		return t.modelDD.layout(w, gtx, th, labels, sel, i18n.T(l, "wiz.stt.pickModel"), !installing && !s.running)
	}, gap(8))

	dlLabel := "wiz.stt.download"
	if inst[selected] {
		dlLabel = "wiz.stt.use"
	}
	if installing {
		frac := float32(0)
		if prog.Total > 0 {
			frac = float32(prog.Done) / float32(prog.Total)
		}
		line := i18n.Tf(l, "wiz.stt.progress", mb(prog.Done), mb(prog.Total), prog.Rate/(1<<20), fmtETA(prog.ETA))
		rows = append(rows, bar(th, frac), gap(4), w.text(kit.Muted(th, line)), gap(4),
			func(gtx layout.Context) layout.Dimensions {
				return w.button(gtx, th, ActCancel, i18n.T(l, "wiz.cancel"), true)
			})
	} else {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return w.button(gtx, th, ActDownload, i18n.T(l, dlLabel), sel >= 0 && !s.running)
		})
	}
	if installErr != "" {
		rows = append(rows, w.text(th.StatusLabel(theme.StatusError, i18n.Tf(l, "wiz.stt.installFailed", installErr))),
			w.text(kit.Muted(th, fixText(l, installErr))))
	}

	// Microphone test.
	anyInstalled := len(inst) > 0
	devLabels := []string{i18n.T(l, "wiz.stt.defaultDevice")}
	devSel := 0
	for i, d := range devs {
		devLabels = append(devLabels, d.Name)
		if d.ID == device && device != "" {
			devSel = i + 1
		}
	}
	rows = append(rows, gap(16),
		w.text(kit.Subheading(th, i18n.T(l, "wiz.stt.mic"))),
		func(gtx layout.Context) layout.Dimensions {
			return t.deviceDD.layout(w, gtx, th, devLabels, devSel, devLabels[0], anyInstalled && !s.running)
		}, gap(8),
		func(gtx layout.Context) layout.Dimensions {
			return w.button(gtx, th, ActMicTest, i18n.T(l, "wiz.stt.micTest"), anyInstalled && meter == nil && !s.running)
		},
	)
	if meter != nil {
		lvl := meter.Level()
		rows = append(rows, gap(6), func(gtx layout.Context) layout.Dimensions {
			gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(meterRefresh)})
			return bar(th, meterFraction(lvl.RMS))(gtx)
		}, w.text(kit.Muted(th, i18n.Tf(l, "wiz.stt.level", lvl.RMS))))
	}
	if meterErr != "" {
		rows = append(rows, w.text(th.StatusLabel(theme.StatusError, meterErr)), w.text(kit.Muted(th, fixText(l, meterErr))))
	}
	phrase := w.text(kit.Subheading(th, "«"+readiness.MicPhrase(string(l))+"»"))
	rows = append(rows, gap(8), w.text(kit.Body(th, i18n.T(l, "wiz.stt.readAloud"))),
		func(gtx layout.Context) layout.Dimensions { return kit.Card(gtx, th, phrase) },
		gap(8), w.checkButton(th, l, s, readiness.StepSTT, anyInstalled))
	if transcript != "" {
		rows = append(rows, gap(6), w.text(kit.Body(th, i18n.Tf(l, "wiz.stt.heard", transcript))))
	}
	return rows
}
