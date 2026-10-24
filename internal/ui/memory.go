package ui

import (
	"context"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/memory"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// MemoryBackend is the part of *memory.Service the Memory settings use.
type MemoryBackend interface {
	Facts(ctx context.Context) ([]memory.Fact, error)
	Delete(ctx context.Context, id string) error
	Clear(ctx context.Context) error
}

const (
	memTimeout = 3 * time.Second
	// memRefresh is how stale the list may get while the section is open:
	// facts are learned in the background after each answer.
	memRefresh = 3 * time.Second
)

// memView is the Memory section's list of remembered facts. Reads and
// deletes run off the UI goroutine.
type memView struct {
	be         MemoryBackend
	invalidate func()

	clear, yes, cancel widget.Clickable
	confirming         bool
	dels               map[string]*widget.Clickable

	mu      sync.Mutex
	facts   []memory.Fact
	failed  bool
	loaded  bool
	loading bool
	at      time.Time
}

func newMemView(be MemoryBackend, invalidate func()) *memView {
	return &memView{be: be, invalidate: invalidate, dels: map[string]*widget.Clickable{}}
}

// refresh reloads the list unless a load is running.
func (v *memView) refresh() {
	v.mu.Lock()
	if v.loading {
		v.mu.Unlock()
		return
	}
	v.loading = true
	v.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), memTimeout)
		defer cancel()
		facts, err := v.be.Facts(ctx)
		v.mu.Lock()
		v.facts, v.failed, v.loaded, v.loading, v.at = facts, err != nil, true, false, time.Now()
		v.mu.Unlock()
		v.invalidate()
	}()
}

// do runs a change off the UI goroutine, then reloads.
func (v *memView) do(fn func(context.Context) error) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), memTimeout)
		defer cancel()
		err := fn(ctx)
		v.mu.Lock()
		v.failed = err != nil
		v.mu.Unlock()
		v.refresh()
	}()
}

// update turns clicks into changes and keeps the list fresh.
func (v *memView) update(gtx layout.Context) (facts []memory.Fact, failed bool) {
	v.mu.Lock()
	stale := !v.loaded || time.Since(v.at) > memRefresh
	facts, failed = v.facts, v.failed
	v.mu.Unlock()
	if stale {
		v.refresh()
	}
	for _, f := range facts {
		if b, ok := v.dels[f.ID]; ok && b.Clicked(gtx) {
			id := f.ID
			v.do(func(ctx context.Context) error { return v.be.Delete(ctx, id) })
		}
	}
	if v.clear.Clicked(gtx) {
		v.confirming = true
	}
	if v.cancel.Clicked(gtx) {
		v.confirming = false
	}
	if v.yes.Clicked(gtx) {
		v.confirming = false
		v.do(v.be.Clear)
	}
	return facts, failed
}

func kindKey(k memory.Kind) string {
	switch k {
	case memory.KindName:
		return "memory.kind.name"
	case memory.KindPreference:
		return "memory.kind.preference"
	case memory.KindLanguage:
		return "memory.kind.language"
	case memory.KindApp:
		return "memory.kind.app"
	}
	return "memory.kind.other"
}

// rows are the section's panels below the on/off switch.
func (v *memView) rows(gtx layout.Context, th *theme.Theme, l Lang) []layout.Widget {
	facts, failed := v.update(gtx)
	var out []layout.Widget
	if failed {
		out = append(out, func(gtx layout.Context) layout.Dimensions {
			return kit.Alert(gtx, th, theme.StatusError, T(l, "memory.loadError"), T(l, "memory.loadError.d"))
		})
	}
	var list []layout.Widget
	for _, f := range facts {
		b, ok := v.dels[f.ID]
		if !ok {
			b = new(widget.Clickable)
			v.dels[f.ID] = b
		}
		desc := T(l, kindKey(f.Kind))
		if f.Volatile {
			desc += " · " + T(l, "memory.session")
		}
		r := kit.SettingRow{Title: f.Text, Desc: desc, Control: func(gtx layout.Context) layout.Dimensions {
			return kit.ButtonStyle{Kind: kit.Secondary, Small: true, Label: T(l, "memory.delete")}.Layout(gtx, th, b)
		}}
		list = append(list, func(gtx layout.Context) layout.Dimensions { return r.Layout(gtx, th) })
	}
	if len(list) == 0 {
		list = append(list, func(gtx layout.Context) layout.Dimensions {
			return kit.Pad(th, 16, 20, kit.Muted(th, T(l, "memory.empty")).Layout)(gtx)
		})
	}
	out = append(out, panelOf(th, append([]layout.Widget{
		setting(th, l, "memory.facts", "memory.facts.d", true, nil),
	}, list...)...))

	clear := kit.SettingRow{Title: T(l, "memory.clear"), Desc: T(l, "memory.clear.d")}
	clear.Control = func(gtx layout.Context) layout.Dimensions {
		if len(facts) == 0 {
			gtx = gtx.Disabled()
		}
		return kit.ButtonStyle{Kind: kit.Secondary, Small: true, Label: T(l, "memory.clear.btn")}.Layout(gtx, th, &v.clear)
	}
	if v.confirming {
		clear.Desc = T(l, "memory.clear.confirm")
		clear.Control = kit.HStack(th, 8,
			func(gtx layout.Context) layout.Dimensions {
				return kit.ButtonStyle{Kind: kit.Primary, Small: true, Label: T(l, "memory.clear.yes")}.Layout(gtx, th, &v.yes)
			},
			func(gtx layout.Context) layout.Dimensions {
				return kit.ButtonStyle{Kind: kit.Secondary, Small: true, Label: T(l, "memory.cancel")}.Layout(gtx, th, &v.cancel)
			})
	}
	out = append(out, panelOf(th, func(gtx layout.Context) layout.Dimensions { return clear.Layout(gtx, th) }),
		kit.Small(th, T(l, "memory.hint")).Layout)
	return out
}
