package doubletap

import (
	"testing"
	"time"
)

// step is one key event at ms milliseconds; up=false is a press.
type step struct {
	key string
	up  bool
	ms  int
}

func dn(k string, ms int) step { return step{k, false, ms} }
func up(k string, ms int) step { return step{k, true, ms} }

func TestDetectorDownUp(t *testing.T) {
	tests := []struct {
		name  string
		steps []step
		fires []int // indices of steps that report a double tap
	}{
		{"double tap", []step{dn("ctrl", 0), up("ctrl", 80), dn("ctrl", 200), up("ctrl", 280)}, []int{3}},
		{"single tap", []step{dn("ctrl", 0), up("ctrl", 80)}, nil},
		{"too slow", []step{dn("ctrl", 0), up("ctrl", 80), dn("ctrl", 81+int(Window/time.Millisecond)), up("ctrl", 600)}, nil},
		{"slow then quick counts the later pair", []step{
			dn("ctrl", 0), up("ctrl", 80), dn("ctrl", 900), up("ctrl", 960), dn("ctrl", 1100), up("ctrl", 1150),
		}, []int{5}},
		{"edge of window", []step{dn("ctrl", 0), up("ctrl", 80), dn("ctrl", 80+int(Window/time.Millisecond)), up("ctrl", 560)}, []int{3}},
		{"second press held", []step{dn("ctrl", 0), up("ctrl", 80), dn("ctrl", 200), up("ctrl", 201+int(MaxHold/time.Millisecond))}, nil},
		{"first press held", []step{dn("ctrl", 0), up("ctrl", 1+int(MaxHold/time.Millisecond)), dn("ctrl", 400), up("ctrl", 450)}, nil},
		{"chord on second press", []step{dn("ctrl", 0), up("ctrl", 80), dn("ctrl", 200), dn("c", 220), up("c", 250), up("ctrl", 270)}, nil},
		{"chord on first press", []step{dn("ctrl", 0), dn("c", 20), up("c", 40), up("ctrl", 60), dn("ctrl", 200), up("ctrl", 260)}, nil},
		{"key typed between taps", []step{dn("ctrl", 0), up("ctrl", 80), dn("a", 120), up("a", 150), dn("ctrl", 200), up("ctrl", 260)}, nil},
		{"other modifier held throughout", []step{dn("shift", 0), dn("ctrl", 10), up("ctrl", 80), dn("ctrl", 200), up("ctrl", 260), up("shift", 300)}, nil},
		{"other key released between is harmless", []step{dn("a", 0), up("a", 5), dn("ctrl", 10), up("ctrl", 80), dn("ctrl", 200), up("ctrl", 260)}, []int{5}},
		{"autorepeat press ignored", []step{dn("ctrl", 0), dn("ctrl", 30), up("ctrl", 80), dn("ctrl", 200), dn("ctrl", 230), up("ctrl", 260)}, []int{5}},
		{"bounce is one tap", []step{dn("ctrl", 0), up("ctrl", 60), dn("ctrl", 70), up("ctrl", 80)}, nil},
		{"bounce then real second tap", []step{dn("ctrl", 0), up("ctrl", 60), dn("ctrl", 70), up("ctrl", 80), dn("ctrl", 250), up("ctrl", 300)}, []int{5}},
		{"triple tap fires once", []step{
			dn("ctrl", 0), up("ctrl", 60), dn("ctrl", 150), up("ctrl", 200), dn("ctrl", 300), up("ctrl", 350),
		}, []int{3}},
		{"quadruple tap fires twice", []step{
			dn("ctrl", 0), up("ctrl", 60), dn("ctrl", 150), up("ctrl", 200), dn("ctrl", 300), up("ctrl", 350), dn("ctrl", 450), up("ctrl", 500),
		}, []int{3, 7}},
		{"other key double tap", []step{dn("shift", 0), up("shift", 60), dn("shift", 150), up("shift", 200)}, nil},
		{"release without press", []step{up("ctrl", 0), dn("ctrl", 100), up("ctrl", 150)}, nil},
	}
	t0 := time.Unix(1000, 0)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := New("ctrl")
			var got []int
			for i, s := range tt.steps {
				at := t0.Add(time.Duration(s.ms) * time.Millisecond)
				if s.up {
					if d.Up(s.key, at) {
						got = append(got, i)
					}
				} else {
					d.Down(s.key, at)
				}
			}
			if len(got) != len(tt.fires) {
				t.Fatalf("fired at %v, want %v", got, tt.fires)
			}
			for i := range got {
				if got[i] != tt.fires[i] {
					t.Fatalf("fired at %v, want %v", got, tt.fires)
				}
			}
		})
	}
}

func TestDetectorTap(t *testing.T) {
	tests := []struct {
		name  string
		taps  []int
		fires []int
	}{
		{"double", []int{0, 250}, []int{1}},
		{"single", []int{0}, nil},
		{"too slow", []int{0, 1 + int(Window/time.Millisecond)}, nil},
		{"slow then quick", []int{0, 900, 1200}, []int{2}},
		{"duplicate report", []int{0, 5}, nil},
		{"duplicate then second", []int{0, 5, 250}, []int{2}},
		{"triple fires once", []int{0, 200, 400}, []int{1}},
		{"quadruple fires twice", []int{0, 200, 400, 600}, []int{1, 3}},
	}
	t0 := time.Unix(1000, 0)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := New("super")
			var got []int
			for i, ms := range tt.taps {
				if d.Tap(t0.Add(time.Duration(ms) * time.Millisecond)) {
					got = append(got, i)
				}
			}
			if len(got) != len(tt.fires) {
				t.Fatalf("fired at %v, want %v", got, tt.fires)
			}
			for i := range got {
				if got[i] != tt.fires[i] {
					t.Fatalf("fired at %v, want %v", got, tt.fires)
				}
			}
		})
	}
}

func TestReset(t *testing.T) {
	t0 := time.Unix(1000, 0)
	d := New("alt")
	d.Down("alt", t0)
	d.Up("alt", t0.Add(50*time.Millisecond))
	d.Down("x", t0.Add(60*time.Millisecond)) // stuck: its release got lost
	d.Reset()
	d.Down("alt", t0.Add(100*time.Millisecond))
	if d.Up("alt", t0.Add(150*time.Millisecond)) {
		t.Fatal("a tap before Reset still counted")
	}
	d.Down("alt", t0.Add(250*time.Millisecond))
	if !d.Up("alt", t0.Add(300*time.Millisecond)) {
		t.Fatal("stuck key survived Reset")
	}
}

func TestValid(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{{"", true}, {"ctrl", true}, {"shift", true}, {"alt", true}, {"super", true}, {"Ctrl", false}, {"a", false}, {"space", false}}
	for _, tt := range tests {
		if got := Valid(tt.key); got != tt.want {
			t.Errorf("Valid(%q) = %v", tt.key, got)
		}
		if (Check(tt.key) == nil) != tt.want {
			t.Errorf("Check(%q) disagrees with Valid", tt.key)
		}
	}
}
