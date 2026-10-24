package ui

import "github.com/xlsft/companion/internal/ui/icon"

// Route is a section of the main window.
type Route int

const (
	RouteHome Route = iota
	RouteHistory
	RouteAgents
	RouteSkills
	RouteSettings
	RouteDiagnostics
)

// Routes lists the sidebar in display (and Tab / Ctrl+N) order.
var Routes = []Route{RouteHome, RouteHistory, RouteAgents, RouteSkills, RouteSettings, RouteDiagnostics}

func (r Route) labelKey() string {
	switch r {
	case RouteHome:
		return "nav.home"
	case RouteHistory:
		return "nav.history"
	case RouteAgents:
		return "nav.agents"
	case RouteSkills:
		return "nav.skills"
	case RouteSettings:
		return "nav.settings"
	case RouteDiagnostics:
		return "nav.diagnostics"
	}
	return "nav.home"
}

func (r Route) icon() icon.Name {
	switch r {
	case RouteHistory:
		return icon.History
	case RouteAgents:
		return icon.Robot
	case RouteSkills:
		return icon.Sparkles
	case RouteSettings:
		return icon.Settings3
	case RouteDiagnostics:
		return icon.Heartbeat
	}
	return icon.Chat1
}

func (r Route) valid() bool { return r >= RouteHome && r <= RouteDiagnostics }

// Implemented reports whether the route has real content yet; the others
// render a placeholder.
func (r Route) Implemented() bool {
	return r == RouteHome || r == RouteSettings || r == RouteDiagnostics
}

const maxHistory = 32

// Navigator is the main window's route state with a bounded back stack.
// Not safe for concurrent use; mainUI guards it.
type Navigator struct {
	cur  Route
	back []Route
}

func (n *Navigator) Current() Route { return n.cur }

// Go switches to r and reports whether the route changed.
func (n *Navigator) Go(r Route) bool {
	if !r.valid() || r == n.cur {
		return false
	}
	n.back = append(n.back, n.cur)
	if len(n.back) > maxHistory {
		n.back = n.back[len(n.back)-maxHistory:]
	}
	n.cur = r
	return true
}

// Back returns to the previous route, if any.
func (n *Navigator) Back() bool {
	if len(n.back) == 0 {
		return false
	}
	n.cur = n.back[len(n.back)-1]
	n.back = n.back[:len(n.back)-1]
	return true
}

// RouteForShortcut maps Ctrl+1…6 to a route.
func RouteForShortcut(digit int) (Route, bool) {
	if digit < 1 || digit > len(Routes) {
		return 0, false
	}
	return Routes[digit-1], true
}
