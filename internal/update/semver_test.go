package update

import "testing"

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.0.1", "0.0.1", 0},
		{"v0.0.1", "0.0.1", 0},
		{"0.0.2", "0.0.1", 1},
		{"0.1.0", "0.0.9", 1},
		{"1.0.0", "0.99.99", 1},
		{"0.10.0", "0.9.0", 1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-alpha.beta", "1.0.0-beta", -1},
		{"1.0.0-beta.2", "1.0.0-beta.11", -1},
		{"1.0.0-beta.11", "1.0.0-rc.1", -1},
		{"1.0.0+build.5", "1.0.0", 0},
		{"1.0.1-rc.1", "1.0.0", 1},
	}
	for _, tt := range tests {
		a, err := ParseVersion(tt.a)
		if err != nil {
			t.Fatalf("%s: %v", tt.a, err)
		}
		b, err := ParseVersion(tt.b)
		if err != nil {
			t.Fatalf("%s: %v", tt.b, err)
		}
		if got := Compare(a, b); got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := Compare(b, a); got != -tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want string // "" when invalid
	}{
		{"v1.2.3", "1.2.3"},
		{"1.2.3-rc.1", "1.2.3-rc.1"},
		{"1.2.3+meta", "1.2.3"},
		{"1.2", ""},
		{"1.2.3.4", ""},
		{"v1.02.3", ""},
		{"1.2.x", ""},
		{"1.2.3-", ""},
		{"1.2.3-a..b", ""},
		{"-1.2.3", ""},
		{"dev", ""},
		{"", ""},
	}
	for _, tt := range tests {
		v, err := ParseVersion(tt.in)
		switch {
		case tt.want == "" && err == nil:
			t.Errorf("ParseVersion(%q) = %v, want error", tt.in, v)
		case tt.want != "" && err != nil:
			t.Errorf("ParseVersion(%q): %v", tt.in, err)
		case tt.want != "" && v.String() != tt.want:
			t.Errorf("ParseVersion(%q) = %s, want %s", tt.in, v, tt.want)
		}
	}
}
