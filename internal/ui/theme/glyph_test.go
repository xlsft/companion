package theme

import (
	"testing"

	"gioui.org/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"

	"github.com/xlsft/companion/internal/assets/fonts"
)

// Status symbols are the non-colour cue, so a missing glyph (tofu) would
// break NFR-005. The Google Sans subset lacks some of them; the Go fonts
// in the collection are the fallback and must have them.
func TestSymbolsHaveGlyphs(t *testing.T) {
	syms := SelectedMarker
	for _, s := range []Status{StatusUnknown, StatusOK, StatusWarn, StatusError} {
		syms += s.Symbol()
	}
	missing(t, "goregular", goregular.TTF, syms+"…±·Ёё—")
}

// The UI text is Russian and English; the subset must keep both scripts
// and the logo arrow in its own face, or text would fall back mid-word.
func TestBundledFacesCoverUIText(t *testing.T) {
	const text = "Распознавание речи Ёё — «Далее» … Speech recognition 0123 №"
	tests := []struct {
		file, chars string
	}{
		{fonts.SansRegular, text},
		{fonts.SansMedium, text},
		{fonts.SansSemiBold, text},
		{fonts.Display, text + "↖"},
		{fonts.MonoRegular, `binds { spawn "companion" } 0O1lI`},
	}
	for _, tt := range tests {
		missing(t, tt.file, fonts.Must(tt.file), tt.chars)
	}
}

func TestCollectionHasTypefaces(t *testing.T) {
	want := map[font.Typeface][]font.Weight{
		Sans:    {font.Normal, font.Medium, font.SemiBold},
		Mono:    {font.Normal, font.Medium},
		Display: {font.SemiBold},
		cjkSC:   {font.Normal, font.Bold},
		cjkJP:   {font.Normal, font.Bold},
		cjkKR:   {font.Normal, font.Bold},
	}
	have := map[font.Typeface]map[font.Weight]bool{}
	for _, f := range Collection() {
		if have[f.Font.Typeface] == nil {
			have[f.Font.Typeface] = map[font.Weight]bool{}
		}
		have[f.Font.Typeface][f.Font.Weight] = true
	}
	for tf, ws := range want {
		for _, w := range ws {
			if !have[tf][w] {
				t.Errorf("no %s %v face", tf, w)
			}
		}
	}
}

func missing(t *testing.T, name string, ttf []byte, chars string) {
	t.Helper()
	f, err := sfnt.Parse(ttf)
	if err != nil {
		t.Fatal(err)
	}
	var buf sfnt.Buffer
	for _, r := range chars {
		if r == ' ' {
			continue
		}
		if i, err := f.GlyphIndex(&buf, r); err != nil || i == 0 {
			t.Errorf("%s: no glyph for %q", name, r)
		}
	}
}
