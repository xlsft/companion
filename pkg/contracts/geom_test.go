package contracts

import "testing"

func TestRectIntersect(t *testing.T) {
	tests := []struct {
		name string
		a, b Rect
		want Rect
	}{
		{"overlap", RectXYWH(0, 0, 10, 10), RectXYWH(5, 5, 10, 10), RectXYWH(5, 5, 5, 5)},
		{"negative origin", RectXYWH(-1920, 0, 1920, 1080), RectXYWH(-100, 100, 200, 50), RectXYWH(-100, 100, 100, 50)},
		{"disjoint", RectXYWH(0, 0, 10, 10), RectXYWH(10, 0, 10, 10), Rect{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Intersect(tt.b); got != tt.want {
				t.Fatalf("Intersect = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRectContainsIsHalfOpen(t *testing.T) {
	r := RectXYWH(0, 0, 10, 10)
	if !r.Contains(Point{0, 0}) || r.Contains(Point{10, 5}) || r.Contains(Point{5, 10}) {
		t.Fatal("Contains must include Min and exclude Max")
	}
}
