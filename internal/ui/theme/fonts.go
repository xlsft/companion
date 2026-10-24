package theme

import (
	"embed"
	"sync"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/font/opentype"
)

//go:embed fonts/*.ttf
var fontFS embed.FS

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
		entries, err := fontFS.ReadDir("fonts")
		if err != nil {
			panic(err)
		}
		for _, e := range entries {
			b, err := fontFS.ReadFile("fonts/" + e.Name())
			if err != nil {
				panic(err)
			}
			faces, err := opentype.ParseCollection(b)
			if err != nil {
				panic("theme: bad embedded font " + e.Name() + ": " + err.Error())
			}
			coll = append(coll, faces...)
		}
		coll = append(coll, gofont.Collection()...)
	})
	return coll
}
