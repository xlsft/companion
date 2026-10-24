package theme

import (
	"sync"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/font/opentype"

	"github.com/xlsft/companion/internal/assets/fonts"
)

var (
	collOnce sync.Once
	coll     []font.FontFace
)

// Collection is the bundled faces: Google Sans for text, Google Sans Code
// for code and Unbounded for headings and the logo, then the Go fonts as
// a fallback for glyphs the subsets lack (status symbols, rare scripts).
// Parsed once; faces are read-only and safe to share between shapers.
func Collection() []font.FontFace {
	collOnce.Do(func() {
		entries, err := fonts.FS.ReadDir(".")
		if err != nil {
			panic(err)
		}
		for _, e := range entries {
			faces, err := opentype.ParseCollection(fonts.Must(e.Name()))
			if err != nil {
				panic("theme: bad embedded font " + e.Name() + ": " + err.Error())
			}
			coll = append(coll, faces...)
		}
		coll = append(coll, gofont.Collection()...)
	})
	return coll
}
