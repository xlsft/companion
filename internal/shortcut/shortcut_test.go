package shortcut

import (
	"slices"
	"testing"

	"github.com/xlsft/companion/pkg/contracts"
)

func TestParseFormat(t *testing.T) {
	tests := []struct {
		in, out string
		code    string
	}{
		{"super+space", "super+space", ""},
		{"Shift+Super+Space", "shift+super+space", ""},
		{"ctrl+alt+Esc", "ctrl+alt+escape", ""},
		{"meta+f13", "super+f13", ""},
		{"  alt+a ", "alt+a", ""},
		{"", "", CodeEmpty},
		{"hyper+a", "", CodeUnknownKey},
		{"super+", "", CodeUnknownKey},
		{"super+foo", "", CodeUnknownKey},
	}
	for _, tt := range tests {
		h, err := Parse(tt.in)
		if Code(err) != tt.code {
			t.Errorf("Parse(%q) err = %v, want code %q", tt.in, err, tt.code)
			continue
		}
		if err == nil && Format(h) != tt.out {
			t.Errorf("Format(Parse(%q)) = %q, want %q", tt.in, Format(h), tt.out)
		}
	}
}

func TestValidate(t *testing.T) {
	talk := contracts.Hotkey{Mods: contracts.ModSuper, Key: "space"}
	tests := []struct {
		name string
		role Role
		h    string
		code string
	}{
		{"default talk", Talk, DefaultTalk, ""},
		{"default cancel", Cancel, DefaultCancel, ""},
		{"talk needs modifier", Talk, "space", CodeNoModifier},
		{"talk f-key needs modifier", Talk, "f9", CodeNoModifier},
		{"cancel bare f-key", Cancel, "f9", ""},
		{"cancel bare escape", Cancel, "escape", CodeNoModifier},
		{"shift only types", Talk, "shift+a", CodeShiftOnly},
		{"copy is reserved", Talk, "ctrl+c", CodeReserved},
		{"alt tab reserved", Cancel, "alt+tab", CodeReserved},
		{"vt switch reserved", Talk, "ctrl+alt+f2", CodeReserved},
		{"same as talk", Cancel, "super+space", CodeSame},
		{"ctrl alt letter", Talk, "ctrl+alt+k", ""},
	}
	for _, tt := range tests {
		h, err := Parse(tt.h)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if got := Code(Validate(tt.role, h, talk)); got != tt.code {
			t.Errorf("%s: code %q, want %q", tt.name, got, tt.code)
		}
	}
}

func TestValidatePair(t *testing.T) {
	if _, _, err := ValidatePair(DefaultTalk, DefaultCancel); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ValidatePair("super+space", "super+space"); Code(err) != CodeSame {
		t.Errorf("same pair: %v", err)
	}
	if _, _, err := ValidatePair("", DefaultCancel); Code(err) != CodeEmpty {
		t.Errorf("empty talk: %v", err)
	}
}

func TestCaps(t *testing.T) {
	h := contracts.Hotkey{Mods: contracts.ModSuper | contracts.ModShift, Key: "backspace"}
	tests := []struct {
		goos string
		want []string
	}{
		{"linux", []string{"Shift", "Super", "Backspace"}},
		{"windows", []string{"Shift", "Win", "Backspace"}},
		{"darwin", []string{"⇧", "⌘", "Backspace"}},
	}
	for _, tt := range tests {
		if got := Caps(h, tt.goos); !slices.Equal(got, tt.want) {
			t.Errorf("%s: %v, want %v", tt.goos, got, tt.want)
		}
	}
	if got := Label(contracts.Hotkey{Mods: contracts.ModCtrl, Key: "k"}, "linux"); got != "Ctrl+K" {
		t.Errorf("Label = %q", got)
	}
}
