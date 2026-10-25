package wizard

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

const (
	// modelDebounce is how long the form must rest before its models are
	// fetched, so typing an address does not fire a request per key.
	modelDebounce = 600 * time.Millisecond
	modelTimeout  = 15 * time.Second
)

// modelQuery is what a form asks for its model list. An empty sig means
// the form does not say enough yet (no address, or a service's key is
// missing); fetch runs off the UI goroutine.
type modelQuery struct {
	sig   string
	fetch func(ctx context.Context) ([]string, error)
}

// modelList puts the endpoint's models in place of a step's Model field:
// it fetches them by itself once the form says enough, shows them in a
// dropdown with "Other model…" last, and falls back to the text field
// whenever there is no list. The editor stays the value either way.
// Widgets and the fields above "async" are UI-goroutine only.
type modelList struct {
	dd     kit.Dropdown
	typing bool // the text field although a list is there
	// soon fetches this frame's query at once instead of after the
	// debounce: set when a preset is picked or the saved form arrives.
	soon bool

	want    string    // sig of the query the form describes
	started string    // sig of the last fetch started
	due     time.Time // when want is fetched
	seen    int       // gen whose list typing was decided for

	// async, guarded by Wizard.mu
	gen     int
	cancel  context.CancelFunc
	loading bool
	failed  bool
	models  []string
	again   bool // fetch the current query again (after a Save)
}

// reset forgets the last query, so the step fetches again on arrival:
// leaving the step cancelled whatever was in flight.
func (m *modelList) reset(w *Wizard) {
	m.want, m.started = "", ""
	w.mu.Lock()
	m.stop()
	w.mu.Unlock()
}

// stop cancels the fetch in flight and drops the list. Wizard.mu held.
func (m *modelList) stop() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.gen++
	m.loading, m.failed, m.models = false, false, nil
}

// refresh fetches the current query again; any goroutine, Wizard.mu held.
func (m *modelList) refresh() { m.again = true }

// pick applies a choice from the dropdown to ed and reports whether the
// model changed. Call it before the form drains its editors, so the edit
// marks the form as changed like typing would.
func (m *modelList) pick(w *Wizard, gtx layout.Context, ed *widget.Editor) bool {
	w.mu.Lock()
	models, gen := m.models, m.gen
	w.mu.Unlock()
	if models != nil && m.seen != gen {
		// A fresh list shows the value only if it is one of its models;
		// anything else stays as typed.
		m.seen = gen
		v := strings.TrimSpace(ed.Text())
		m.typing = v != "" && !slices.Contains(models, v)
	}
	if w.clicked(gtx, ActModelList) {
		m.typing = false
		m.dd.SetOpen(true)
	}
	i := m.dd.Update(gtx, len(models)+1)
	switch {
	case i < 0:
		return false
	case i >= len(models):
		m.typing = true
		gtx.Execute(key.FocusCmd{Tag: ed})
		return false
	}
	ed.SetText(models[i])
	return true
}

// follow tracks the form's query: a changed one is fetched after the
// debounce (at once when soon), cancelling the previous fetch.
func (m *modelList) follow(w *Wizard, gtx layout.Context, q modelQuery) {
	w.mu.Lock()
	again := m.again
	m.again = false
	if q.sig != m.want {
		m.stop()
	}
	w.mu.Unlock()
	if q.sig != m.want {
		m.want, m.started = q.sig, ""
		m.due = gtx.Now.Add(modelDebounce)
		if m.soon {
			m.due = gtx.Now
		}
	}
	// soon covers the frame of the pick only: a key typed next is debounced.
	m.soon = false
	if again && m.want != "" {
		m.started, m.due = "", gtx.Now
	}
	if m.want == "" || m.started == m.want {
		return
	}
	if gtx.Now.Before(m.due) {
		gtx.Execute(op.InvalidateCmd{At: m.due})
		return
	}
	m.started = m.want
	m.start(w, q.fetch)
}

func (m *modelList) start(w *Wizard, fetch func(ctx context.Context) ([]string, error)) {
	w.mu.Lock()
	m.stop()
	g := m.gen
	ctx, cancel := context.WithTimeout(w.stepCtx, modelTimeout)
	m.cancel, m.loading = cancel, true
	w.mu.Unlock()
	go func() {
		defer cancel()
		ids, err := fetch(ctx)
		w.mu.Lock()
		cur := g == m.gen
		if cur {
			m.loading, m.cancel = false, nil
			switch {
			case err == nil:
				m.models = ids
			case !errors.Is(err, context.Canceled):
				// No list is no error: the model is typed as before, so
				// the hint stays quiet and the log technical.
				m.failed = true
			}
		}
		w.mu.Unlock()
		if cur && err != nil && !errors.Is(err, context.Canceled) {
			code := llm.ErrorKind(err)
			if code == "unknown" {
				code = "list_failed"
			}
			w.cfg.Log.Info("setup model list unavailable", "code", code)
		}
		w.cfg.Invalidate()
	}()
}

// field is the Model control: the dropdown of the fetched models, or the
// text field with a quiet line under it while loading, after a failure, or
// to go back to the list.
func (m *modelList) field(w *Wizard, th *theme.Theme, l i18n.Lang, hint string, ed *widget.Editor, enabled bool) layout.Widget {
	w.mu.Lock()
	models, loading, failed := m.models, m.loading, m.failed
	w.mu.Unlock()
	caption := i18n.T(l, "wiz.byok.model")
	if len(models) > 0 && !m.typing {
		labels := append(slices.Clip(models), i18n.T(l, "wiz.byok.model.other"))
		sel := slices.Index(models, strings.TrimSpace(ed.Text()))
		w.frame.texts = append(w.frame.texts, caption)
		return kit.Labeled(th, caption, func(gtx layout.Context) layout.Dimensions {
			return w.dropdown(gtx, th, &m.dd, labels, sel, i18n.T(l, "wiz.byok.model.pick"), enabled)
		})
	}
	f := field(w, th, caption, hint, ed, enabled)
	var note layout.Widget
	switch {
	case loading:
		note = w.text(kit.Muted(th, i18n.T(l, "wiz.byok.model.loading")))
	case failed:
		note = w.text(kit.Muted(th, i18n.T(l, "wiz.byok.model.failed")))
	case len(models) > 0:
		note = func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActModelList, kit.ButtonStyle{Kind: kit.Ghost, Small: true, Label: i18n.T(l, "wiz.byok.model.fromList")}, enabled)
		}
	default:
		return f
	}
	return kit.VStack(th, 6, f, note)
}

// hasHost reports an absolute http(s) address, so a half-typed one is not
// asked for models.
func hasHost(base string) bool {
	u, err := url.Parse(base)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// localAddr reports an address on this computer or the local network,
// where servers usually take no key.
func localAddr(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if strings.EqualFold(h, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && (a.Unmap().IsLoopback() || a.Unmap().IsPrivate())
}

// sameHost reports whether two addresses share scheme and host: the
// backend uses a stored key only then.
func sameHost(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil || ua.Host == "" {
		return false
	}
	ub, err := url.Parse(b)
	return err == nil && strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}
