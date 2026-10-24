package i18n

import "testing"

func TestT(t *testing.T) {
	Register(map[string][2]string{
		"test.both":   {"Hello", "Привет"},
		"test.enOnly": {"Only", ""},
	})
	tests := []struct {
		lang Lang
		key  string
		want string
	}{
		{EN, "test.both", "Hello"},
		{RU, "test.both", "Привет"},
		{RU, "test.enOnly", "Only"},
		{RU, "test.missing", "test.missing"},
	}
	for _, tt := range tests {
		if got := T(tt.lang, tt.key); got != tt.want {
			t.Errorf("T(%s, %s) = %q, want %q", tt.lang, tt.key, got, tt.want)
		}
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	Register(map[string][2]string{"test.dup": {"a", "б"}})
	defer func() {
		if recover() == nil {
			t.Error("duplicate key did not panic")
		}
	}()
	Register(map[string][2]string{"test.dup": {"a", "б"}})
}

func TestOf(t *testing.T) {
	for code, want := range map[string]Lang{"en": EN, "ru": RU, "": RU, "de": RU} {
		if got := Of(code); got != want {
			t.Errorf("Of(%q) = %s", code, got)
		}
	}
}
