package wizard

import (
	"fmt"
	"image"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/models/hw"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// size formats a byte count the way the prototypes do: GB with two
// decimals from 1 GB, whole MB below.
func size(l i18n.Lang, n int64) string {
	if n >= 1<<30 {
		return i18n.Tf(l, "wiz.stt.gb", float64(n)/(1<<30))
	}
	return i18n.Tf(l, "wiz.stt.mb", float64(n)/(1<<20))
}

func langsOf(l i18n.Lang, m models.Manifest) string {
	if m.Multilingual() {
		return i18n.T(l, "wiz.stt.multilingual")
	}
	return strings.ToUpper(strings.Join(m.Languages, ", "))
}

// hardwareChip is the accelerator and memory the recommendation is for.
func hardwareChip(l i18n.Lang, p hw.Profile) string {
	acc := i18n.Tf(l, "wiz.stt.cpu", p.CPU.Cores)
	if p.GPU.Usable() {
		acc = strings.ToUpper(strings.Join(p.GPU.Backends, "+"))
	}
	return i18n.Tf(l, "wiz.stt.ram", acc, float64(p.Memory.TotalBytes)/(1<<30))
}

// meterFraction maps RMS dBFS onto [0, 1] over a 60 dB range.
func meterFraction(dbfs float64) float32 {
	return float32(max(0, min(1, (dbfs+60)/60)))
}

func fmtETA(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%d s", int(d.Seconds()))
	}
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func (t *sttState) body(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) stepBody {
	b := w.cfg.Backends.STT
	if b == nil {
		return stepBody{rows: []layout.Widget{alert(w, th, theme.StatusError, i18n.T(l, "wiz.notWired"), "")}}
	}
	w.mu.Lock()
	loaded := t.loaded
	w.mu.Unlock()
	if !loaded {
		return stepBody{rows: []layout.Widget{w.text(kit.Muted(th, i18n.T(l, "wiz.loading")))}}
	}
	// The two cards are stacked at full width: side by side the short
	// model card would be stretched to the microphone card's height.
	return stepBody{rows: []layout.Widget{
		kit.VStack(th, 16, t.modelCard(w, th, l, s), t.micCard(w, th, l, s)),
	}}
}

func (t *sttState) modelCard(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) layout.Widget {
	w.mu.Lock()
	loadErr, prof, cat, inst := t.loadErr, t.profile, t.catalog, t.installed
	rec, selected := t.recommended, t.selected
	installing, prog, installErr := t.installing, t.progress, t.installErr
	w.mu.Unlock()

	shown, ok := cat.Find(selected)
	if !ok {
		shown = rec
	}
	hwTxt := hardwareChip(l, prof)
	w.frame.texts = append(w.frame.texts, hwTxt)
	head := kit.Spread(w.text(kit.Caption(th, i18n.T(l, "wiz.stt.recFor"))), func(gtx layout.Context) layout.Dimensions {
		return kit.Tag(gtx, th, hwTxt, kit.TagAccent)
	})
	// The name and its size and languages are two lines, so neither ever
	// wraps beside the other.
	var title layout.Widget
	if shown.ID != "" {
		name := kit.Text(th, 20, shown.Name, th.Pal.Fg)
		name.Font = font.Font{Typeface: theme.Display, Weight: font.SemiBold}
		title = kit.VStack(th, 4, w.text(name), w.text(kit.Text(th, 13, size(l, shown.Size)+" · "+langsOf(l, shown), th.Pal.Muted)))
	}

	labels := make([]string, len(cat))
	sel := -1
	for i, m := range cat {
		labels[i] = m.Name + " — " + size(l, m.Size)
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
	w.frame.texts = append(w.frame.texts, i18n.T(l, "wiz.stt.other"))
	pick := kit.Labeled(th, i18n.T(l, "wiz.stt.other"), func(gtx layout.Context) layout.Dimensions {
		return w.dropdown(gtx, th, &t.modelDD, labels, sel, i18n.T(l, "wiz.stt.pickModel"), !installing && !s.running)
	})

	var action, progress layout.Widget
	switch {
	case installing:
		frac := float32(0)
		if prog.Total > 0 {
			frac = float32(prog.Done) / float32(prog.Total)
		}
		left := w.text(kit.Muted(th, i18n.Tf(l, "wiz.stt.progress", size(l, prog.Done), size(l, prog.Total))))
		right := w.text(kit.Muted(th, i18n.Tf(l, "wiz.stt.rate", prog.Rate/(1<<20), fmtETA(prog.ETA))))
		progress = kit.VStack(th, 6, kit.Spread(left, right),
			func(gtx layout.Context) layout.Dimensions { return kit.ProgressBar(gtx, th, 6, frac) },
			kit.Spread(w.text(kit.Muted(th, i18n.T(l, "wiz.stt.checksum"))), func(gtx layout.Context) layout.Dimensions {
				return w.btn(gtx, th, ActCancel, kit.ButtonStyle{Kind: kit.Secondary, Small: true, Label: i18n.T(l, "wiz.stt.cancel")}, true)
			}))
	case inst[selected]:
		ready := i18n.T(l, "wiz.stt.ready")
		w.frame.texts = append(w.frame.texts, ready)
		action = kit.HStack(th, 8, func(gtx layout.Context) layout.Dimensions {
			return kit.Badge(gtx, th, theme.StatusOK, ready)
		}, func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActDownload, kit.ButtonStyle{Kind: kit.Secondary, Small: true, Label: i18n.T(l, "wiz.stt.use")}, !s.running)
		})
	default:
		action = func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActDownload, kit.ButtonStyle{Kind: kit.Primary, Icon: icon.Download2,
				Label: i18n.T(l, "wiz.stt.download") + " · " + size(l, shown.Size)}, sel >= 0 && !s.running)
		}
	}
	model := func(gtx layout.Context) layout.Dimensions {
		if action == nil || title == nil || gtx.Constraints.Max.X < gtx.Dp(th.Dp(520)) {
			return kit.VStack(th, 12, title, action)(gtx)
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, title),
			kit.HSpace(th.Dp(16)),
			layout.Rigid(action),
		)
	}
	var errs []layout.Widget
	if installErr != "" {
		errs = append(errs, alert(w, th, theme.StatusError, i18n.Tf(l, "wiz.stt.installFailed", installErr), fixText(l, installErr)))
	}
	if loadErr != "" {
		errs = append(errs, alert(w, th, theme.StatusError, loadErr, fixText(l, loadErr)))
	}
	return card(th, kit.VStack(th, 16, append([]layout.Widget{head, model, progress, pick}, errs...)...))
}

// meterBars are the level meter's bar heights (fractions of its height),
// the shape of a spoken syllable as in the prototype.
var meterBars = []float32{.20, .34, .55, .80, .62, .90, .70, .44, .58, .30, .22, .14, .10, .10, .10, .10}

// meter lights the bars up to the level; unlit bars stay visible so the
// meter reads as one even when silent.
func meter(th *theme.Theme, level float32, on bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(th.Dp(36))
		g := gtx.Dp(th.Dp(3))
		n := len(meterBars)
		bw := (gtx.Constraints.Max.X - g*(n-1)) / n
		lit := 0
		if on {
			lit = int(level*float32(n) + 0.5)
		}
		for i, f := range meterBars {
			col := th.Pal.Border
			if i < lit {
				col = th.Pal.AccentLine
			}
			bh := max(2, int(f*float32(h)))
			r := image.Rect(i*(bw+g), h-bh, i*(bw+g)+bw, h)
			paint.FillShape(gtx.Ops, col, clip.UniformRRect(r, gtx.Dp(1)).Op(gtx.Ops))
		}
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, h)}
	}
}

func (t *sttState) micCard(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) layout.Widget {
	w.mu.Lock()
	inst, devs, device := t.installed, t.devices, t.device
	m, meterErr, transcript := t.meter, t.meterErr, t.transcript
	w.mu.Unlock()
	anyInstalled := len(inst) > 0

	var hint layout.Widget
	if !anyInstalled {
		hint = w.text(kit.Muted(th, i18n.T(l, "wiz.stt.micAfter")))
	}
	head := kit.Spread(w.text(kit.Subheading(th, i18n.T(l, "wiz.stt.mic"))), hint)

	devLabels := []string{i18n.T(l, "wiz.stt.defaultDevice")}
	devSel := 0
	for i, d := range devs {
		devLabels = append(devLabels, d.Name)
		if d.ID == device && device != "" {
			devSel = i + 1
		}
	}
	w.frame.texts = append(w.frame.texts, i18n.T(l, "wiz.stt.device"))
	pick := kit.Labeled(th, i18n.T(l, "wiz.stt.device"), func(gtx layout.Context) layout.Dimensions {
		return w.dropdown(gtx, th, &t.deviceDD, devLabels, devSel, devLabels[0], anyInstalled && !s.running)
	})

	var level float32
	var levelTxt layout.Widget
	if m != nil {
		lvl := m.Level()
		level = meterFraction(lvl.RMS)
		levelTxt = w.text(kit.Muted(th, i18n.Tf(l, "wiz.stt.level", lvl.RMS)))
	} else {
		levelTxt = func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActMicTest, kit.ButtonStyle{Kind: kit.Secondary, Small: true, Icon: icon.Mic, Label: i18n.T(l, "wiz.stt.micTest")},
				anyInstalled && !s.running)
		}
	}
	meterRow := func(gtx layout.Context) layout.Dimensions {
		if m != nil {
			gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(meterRefresh)})
		}
		return layout.Flex{Alignment: layout.End}.Layout(gtx,
			layout.Flexed(1, meter(th, level, m != nil)),
			kit.HSpace(th.Dp(12)),
			layout.Rigid(levelTxt),
		)
	}
	var meterErrs layout.Widget
	if meterErr != "" {
		meterErrs = alert(w, th, theme.StatusError, meterErr, fixText(l, meterErr))
	}

	readCap := w.text(kit.Caption(th, i18n.T(l, "wiz.stt.readAloud")))
	phrase := w.text(kit.Text(th, 16, "«"+readiness.MicPhrase(string(l))+"»", th.Pal.Fg))
	phraseBox := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return kit.Box(gtx, th, th.Pal.Bg, th.Pal.Border, 4, kit.Pad(th, 12, 14, kit.VStack(th, 4, readCap, phrase)))
	}

	act, key := ActCheck, "wiz.stt.record"
	if _, ok := s.result(readiness.StepSTT); ok {
		act, key = ActRecheck, "wiz.stt.recordAgain"
	}
	record := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = 0
		return w.btn(gtx, th, act, kit.ButtonStyle{Kind: kit.Primary, Icon: icon.Mic, Label: i18n.T(l, key)}, anyInstalled && !s.running)
	}
	var heard layout.Widget
	if transcript != "" {
		heard = w.text(kit.Text(th, 13, i18n.Tf(l, "wiz.stt.heard", transcript), th.Pal.Fg2))
	}
	left := kit.VStack(th, 14, pick, meterRow, meterErrs)
	right := kit.VStack(th, 14, phraseBox, kit.Centered(record), heard)
	return card(th, kit.VStack(th, 16, head, func(gtx layout.Context) layout.Dimensions {
		if gtx.Constraints.Max.X < gtx.Dp(th.Dp(600)) {
			return kit.VStack(th, 14, left, right)(gtx)
		}
		return kit.Grid(th, 2, 24, left, right)(gtx)
	}))
}

func (t *sttState) footer(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) footerSpec {
	return footerSpec{primary: w.next(th, l, s)}
}
