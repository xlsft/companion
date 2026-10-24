package ui

import "github.com/xlsft/companion/internal/ui/icon"

// Route is a section of the main window.
type Route int

const (
	RouteHome Route = iota
	RouteHistory
	RouteMemory
	RouteAgents
	RouteSkills
	RouteSettings
)

// Routes lists the sidebar in display order. Diagnostics is a section of
// Settings, not a route.
var Routes = []Route{RouteHome, RouteHistory, RouteMemory, RouteAgents, RouteSkills, RouteSettings}

func (r Route) labelKey() string {
	switch r {
	case RouteHome:
		return "nav.home"
	case RouteHistory:
		return "nav.history"
	case RouteMemory:
		return "nav.memory"
	case RouteAgents:
		return "nav.agents"
	case RouteSkills:
		return "nav.skills"
	case RouteSettings:
		return "nav.settings"
	}
	return "nav.home"
}

func (r Route) icon() icon.Name {
	switch r {
	case RouteHistory:
		return icon.History
	case RouteMemory:
		return icon.Brain
	case RouteAgents:
		return icon.Robot
	case RouteSkills:
		return icon.Sparkles
	case RouteSettings:
		return icon.Settings3
	}
	return icon.Chat1
}

// Implemented reports whether the route has content yet. The others are
// shown in the sidebar as "Soon" and cannot be reached at all: not by
// click, Tab, shortcut or Navigate.
func (r Route) Implemented() bool { return r == RouteHome || r == RouteSettings }

// NavRoutes are the reachable routes in display order; Tab and Ctrl+1…
// walk them, so their shortcuts have no gaps.
func NavRoutes() []Route {
	var out []Route
	for _, r := range Routes {
		if r.Implemented() {
			out = append(out, r)
		}
	}
	return out
}

func (r Route) valid() bool { return r >= RouteHome && r <= RouteSettings && r.Implemented() }

const maxHistory = 32

// Navigator is the main window's route state with a bounded back stack.
// Not safe for concurrent use; mainUI guards it.
type Navigator struct {
	cur  Route
	back []Route
}

func (n *Navigator) Current() Route { return n.cur }

// Go switches to r and reports whether the route changed; routes that are
// not implemented are refused.
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

// RouteForShortcut maps Ctrl+1… to the reachable routes in order.
func RouteForShortcut(digit int) (Route, bool) {
	nav := NavRoutes()
	if digit < 1 || digit > len(nav) {
		return 0, false
	}
	return nav[digit-1], true
}
