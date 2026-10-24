package wizard

import (
	"context"
	"sync"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/models/hw"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// ActiveModeler is implemented by an STT backend that can tell which
// model recognition runs on; the picker marks it "In use".
type ActiveModeler interface {
	ActiveModel() string
}

// ModelPicker is the speech-model choice of Settings → Voice: the same rich
// select as the setup step, one action (download, use, or "in use") and
// the download progress. Layout runs on the UI goroutine; loading and
// installing run on their own.
type ModelPicker struct {
	Backend STT
	// Language is the speech language the recommendation is for.
	Language   func() string
	Invalidate func()

	dd             kit.Dropdown
	action, cancel widget.Clickable

	mu        sync.Mutex
	loading   bool
	loaded    bool
	cat       models.Catalog
	rec       string
	installed map[string]bool
	active    string
	selected  string
	busy      bool
	progress  models.Progress
	errCode   string
	stop      context.CancelFunc
}

// Close cancels a download in flight.
func (p *ModelPicker) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stop != nil {
		p.stop()
	}
}

func (p *ModelPicker) invalidate() {
	if p.Invalidate != nil {
		p.Invalidate()
	}
}

// load reads the catalogue, installed models and hardware once (and again
// after an install, to pick up the new state).
func (p *ModelPicker) load() {
	p.mu.Lock()
	if p.loading || p.Backend == nil {
		p.mu.Unlock()
		return
	}
	p.loading = true
	p.mu.Unlock()
	go func() {
		ctx := context.Background()
		cat, err := p.Backend.Catalog()
		inst, ierr := p.Backend.Installed()
		lang := "auto"
		if p.Language != nil {
			lang = p.Language()
		}
		rec, _ := hw.Recommend(p.Backend.Hardware(ctx), cat, lang)
		installed := map[string]bool{}
		for _, m := range inst {
			installed[m.ID] = true
		}
		active := ""
		if a, ok := p.Backend.(ActiveModeler); ok {
			active = a.ActiveModel()
		}
		p.mu.Lock()
		p.loading, p.loaded = false, true
		p.rec, p.installed, p.active = rec.ID, installed, active
		if p.selected == "" {
			p.selected = active
		}
		if p.selected == "" {
			p.selected = rec.ID
		}
		p.cat = PickerModels(cat, installed, rec.ID, p.selected, active)
		switch {
		case err != nil:
			p.errCode = "catalog_unavailable"
		case ierr != nil:
			p.errCode = "models_dir_unreadable"
		}
		p.mu.Unlock()
		p.invalidate()
	}()
}

func (p *ModelPicker) install(m models.Manifest) {
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	if p.busy {
		p.mu.Unlock()
		cancel()
		return
	}
	p.busy, p.errCode, p.stop, p.progress = true, "", cancel, models.Progress{Total: m.Size}
	p.mu.Unlock()
	go func() {
		defer cancel()
		err := p.Backend.Install(ctx, m, func(pr models.Progress) {
			p.mu.Lock()
			p.progress = pr
			p.mu.Unlock()
			p.invalidate()
		})
		p.mu.Lock()
		p.busy, p.stop = false, nil
		if err != nil {
			p.errCode = installErrCode(err)
		} else {
			p.installed[m.ID], p.active = true, m.ID
		}
		p.mu.Unlock()
		p.invalidate()
	}()
}

// Layout draws the picker; texts receives every string it shows.
func (p *ModelPicker) Layout(th *theme.Theme, l i18n.Lang, texts *[]string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		p.mu.Lock()
		loaded := p.loaded
		cat, rec, installed, active, selected := p.cat, p.rec, p.installed, p.active, p.selected
		busy, prog, errCode := p.busy, p.progress, p.errCode
		p.mu.Unlock()
		if !loaded {
			p.load()
		}
		add := func(s string) string {
			if texts != nil && s != "" {
				*texts = append(*texts, s)
			}
			return s
		}

		if i := p.dd.Update(gtx, len(cat)); i >= 0 {
			p.mu.Lock()
			p.selected, p.errCode = cat[i].ID, ""
			p.mu.Unlock()
			selected = cat[i].ID
		}
		m, ok := cat.Find(selected)
		if p.action.Clicked(gtx) && ok && !busy && selected != active {
			p.install(m)
		}
		if p.cancel.Clicked(gtx) {
			p.Close()
		}

		opts, sel := ModelOptions(l, cat, rec, selected, installed)
		if sel >= 0 {
			for _, s := range opts[sel].Texts() {
				add(s)
			}
		}
		pick := func(gtx layout.Context) layout.Dimensions {
			if busy || !loaded {
				gtx = gtx.Disabled()
			}
			return p.dd.LayoutRich(gtx, th, opts, sel, add(i18n.T(l, "wiz.stt.pickModel")))
		}

		var row layout.Widget
		switch {
		case !ok:
		case busy:
			frac := float32(0)
			if prog.Total > 0 {
				frac = float32(prog.Done) / float32(prog.Total)
			}
			key := "wiz.stt.progress"
			if prog.Unpacking {
				key = "stt.unpacking"
			}
			status := add(i18n.Tf(l, key, size(l, prog.Done), size(l, prog.Total)))
			cancelLabel := add(i18n.T(l, "wiz.stt.cancel"))
			row = kit.VStack(th, 6,
				func(gtx layout.Context) layout.Dimensions { return kit.ProgressBar(gtx, th, 6, frac) },
				kit.Spread(kit.Muted(th, status).Layout, func(gtx layout.Context) layout.Dimensions {
					return kit.ButtonStyle{Kind: kit.Secondary, Small: true, Label: cancelLabel}.Layout(gtx, th, &p.cancel)
				}))
		case selected == active:
			inUse := add(i18n.T(l, "stt.inUse"))
			row = func(gtx layout.Context) layout.Dimensions { return kit.Badge(gtx, th, theme.StatusOK, inUse) }
		case installed[selected]:
			use := add(i18n.T(l, "wiz.stt.use"))
			row = func(gtx layout.Context) layout.Dimensions {
				return kit.ButtonStyle{Kind: kit.Secondary, Small: true, Label: use}.Layout(gtx, th, &p.action)
			}
		default:
			dl := add(i18n.T(l, "wiz.stt.download") + " · " + size(l, m.Size))
			row = func(gtx layout.Context) layout.Dimensions {
				return kit.ButtonStyle{Kind: kit.Primary, Small: true, Icon: icon.Download2, Label: dl}.Layout(gtx, th, &p.action)
			}
		}
		var alert layout.Widget
		if errCode != "" {
			title, fix := add(i18n.Tf(l, "wiz.stt.installFailed", errCode)), add(fixText(l, errCode))
			alert = func(gtx layout.Context) layout.Dimensions { return kit.Alert(gtx, th, theme.StatusError, title, fix) }
		}
		return kit.VStack(th, 12, pick, row, alert)(gtx)
	}
}
