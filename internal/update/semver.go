package update

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a release version, MAJOR.MINOR.PATCH with an optional
// pre-release suffix. Build metadata (+...) is accepted and ignored, as
// semver says it does not affect precedence.
type Version struct {
	Major, Minor, Patch int
	Pre                 string // "" for a release
}

// ParseVersion reads "v1.2.3", "1.2.3" or "1.2.3-rc.1".
func ParseVersion(s string) (Version, error) {
	in := s
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v Version
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.Pre = s[i+1:]
		s = s[:i]
		if v.Pre == "" {
			return Version{}, fmt.Errorf("update: version %q: empty pre-release", in)
		}
		for _, id := range strings.Split(v.Pre, ".") {
			if id == "" {
				return Version{}, fmt.Errorf("update: version %q: empty pre-release identifier", in)
			}
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("update: version %q is not MAJOR.MINOR.PATCH", in)
	}
	nums := [3]*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return Version{}, fmt.Errorf("update: version %q: bad number %q", in, p)
		}
		*nums[i] = n
	}
	return v, nil
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Prerelease reports whether v has a pre-release suffix.
func (v Version) Prerelease() bool { return v.Pre != "" }

// Compare returns -1, 0 or 1 by semver precedence.
func Compare(a, b Version) int {
	for _, d := range [3][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			return cmpInt(d[0], d[1])
		}
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "":
		return 1 // a release outranks its pre-releases
	case b.Pre == "":
		return -1
	}
	as, bs := strings.Split(a.Pre, "."), strings.Split(b.Pre, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := cmpIdent(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(as), len(bs))
}

// cmpIdent orders pre-release identifiers: numeric ones numerically and
// below alphanumeric ones, which compare as ASCII.
func cmpIdent(a, b string) int {
	an, aerr := strconv.Atoi(a)
	bn, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil:
		return cmpInt(an, bn)
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
