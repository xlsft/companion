// Package fonts embeds the bundled typefaces (SIL OFL 1.1, see OFL.txt):
// Google Sans for text, Google Sans Code for code and Unbounded for
// headings and the logo, plus Noto Sans CJK subsets for the Chinese,
// Japanese and Korean UI text. It has no dependencies so both the Gio UI and the
// pure-Go overlay renderer can use the same faces.
//
// The files are static Latin+Cyrillic cuts of the upstream variable fonts
// and CJK subsets limited to the characters of the zh/ja/ko catalogues,
// made by build.py.
package fonts

import "embed"

//go:embed *.ttf *.otf
var FS embed.FS

// Files in FS, by role.
const (
	SansRegular  = "GoogleSans-Regular.ttf"
	SansMedium   = "GoogleSans-Medium.ttf"
	SansSemiBold = "GoogleSans-SemiBold.ttf"
	MonoRegular  = "GoogleSansCode-Regular.ttf"
	MonoMedium   = "GoogleSansCode-Medium.ttf"
	Display      = "Unbounded-SemiBold.ttf"
)

// CJK are the Noto Sans CJK subsets by UI language code: regular (serves
// 400/500) and bold (serves 600/700).
var CJK = map[string][2]string{
	"zh": {"NotoSansCJKsc-Regular.otf", "NotoSansCJKsc-Bold.otf"},
	"ja": {"NotoSansCJKjp-Regular.otf", "NotoSansCJKjp-Bold.otf"},
	"ko": {"NotoSansCJKkr-Regular.otf", "NotoSansCJKkr-Bold.otf"},
}

// Must returns the bytes of an embedded face.
func Must(name string) []byte {
	b, err := FS.ReadFile(name)
	if err != nil {
		panic("fonts: " + err.Error())
	}
	return b
}
