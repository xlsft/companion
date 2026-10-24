package ui

import "testing"

func TestNavigator(t *testing.T) {
	var n Navigator
	if n.Current() != RouteHome {
		t.Fatalf("initial route = %v", n.Current())
	}
	steps := []struct {
		name    string
		do      func() bool
		changed bool
		want    Route
	}{
		{"same route", func() bool { return n.Go(RouteHome) }, false, RouteHome},
		{"invalid", func() bool { return n.Go(Route(99)) }, false, RouteHome},
		{"to settings", func() bool { return n.Go(RouteSettings) }, true, RouteSettings},
		{"to diagnostics", func() bool { return n.Go(RouteDiagnostics) }, true, RouteDiagnostics},
		{"back", n.Back, true, RouteSettings},
		{"back again", n.Back, true, RouteHome},
		{"back empty", n.Back, false, RouteHome},
	}
	for _, s := range steps {
		if got := s.do(); got != s.changed || n.Current() != s.want {
			t.Errorf("%s: changed=%v route=%v, want %v %v", s.name, got, n.Current(), s.changed, s.want)
		}
	}
}

func TestNavigatorHistoryBounded(t *testing.T) {
	var n Navigator
	for i := 0; i < 3*maxHistory; i++ {
		n.Go(Routes[i%len(Routes)])
	}
	if len(n.back) != maxHistory {
		t.Errorf("history len = %d", len(n.back))
	}
}

func TestRouteForShortcut(t *testing.T) {
	tests := []struct {
		digit int
		want  Route
		ok    bool
	}{
		{0, 0, false}, {1, RouteHome, true}, {5, RouteSettings, true}, {6, RouteDiagnostics, true}, {7, 0, false},
	}
	for _, tt := range tests {
		r, ok := RouteForShortcut(tt.digit)
		if ok != tt.ok || (ok && r != tt.want) {
			t.Errorf("RouteForShortcut(%d) = %v %v", tt.digit, r, ok)
		}
	}
}

func TestRoutesHaveLabels(t *testing.T) {
	for _, r := range Routes {
		for _, l := range []Lang{LangEN, LangRU} {
			if k := r.labelKey(); T(l, k) == k {
				t.Errorf("route %d has no %s label", r, l)
			}
		}
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"setup": ModeSetup, "main": ModeMain} {
		if m, err := ParseMode(in); err != nil || m != want || m.String() != in {
			t.Errorf("ParseMode(%q) = %v %v", in, m, err)
		}
	}
	if _, err := ParseMode("ready"); err == nil {
		t.Error("expected error")
	}
}
