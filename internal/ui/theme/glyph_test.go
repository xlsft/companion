package theme

import (
	"testing"

	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
)

// Status symbols are the non-colour cue, so a missing glyph (tofu) would
// break NFR-005; the bundled Go fonts only cover WGL4.
func TestSymbolsHaveGlyphs(t *testing.T) {
	f, err := sfnt.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	var buf sfnt.Buffer
	syms := SelectedMarker
	for _, s := range []Status{StatusUnknown, StatusOK, StatusWarn, StatusError} {
		syms += s.Symbol()
	}
	for _, r := range syms + "…±·Ёё—" {
		if i, err := f.GlyphIndex(&buf, r); err != nil || i == 0 {
			t.Errorf("no glyph for %q", r)
		}
	}
}
