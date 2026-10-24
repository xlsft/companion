package icon

import (
	"fmt"
	"math"
	"strconv"

	"gioui.org/f32"
)

// seg is one cubic Bézier in the 24×24 icon grid; lines are stored as
// cubics with control points on the line so contours reverse uniformly.
type seg struct{ p0, c1, c2, p1 f32.Point }

type contour []seg

func line(a, b f32.Point) seg {
	return seg{a, a.Add(b.Sub(a).Mul(1.0 / 3)), a.Add(b.Sub(a).Mul(2.0 / 3)), b}
}

// parsePath reads SVG path data (all commands, absolute and relative) into
// closed contours. Open subpaths are closed, as filling implies.
func parsePath(d string) ([]contour, error) {
	p := &scanner{s: d}
	var (
		out        []contour
		cur        contour
		pos, start f32.Point
		lastCtl    f32.Point // reflection source for S/T
		lastCmd    byte
		cmd        byte
	)
	closeCur := func() {
		if len(cur) == 0 {
			return
		}
		if pos != start {
			cur = append(cur, line(pos, start))
		}
		out = append(out, cur)
		cur = nil
		pos = start
	}
	for {
		p.skipSep()
		if p.done() {
			break
		}
		if c := p.s[p.i]; isCmd(c) {
			cmd = c
			p.i++
		} else if cmd == 0 {
			return nil, fmt.Errorf("path data must start with a command at %d", p.i)
		}
		rel := cmd >= 'a'
		abs := func(x, y float32) f32.Point {
			if rel {
				return f32.Pt(pos.X+x, pos.Y+y)
			}
			return f32.Pt(x, y)
		}
		switch cmd | 0x20 {
		case 'z':
			closeCur()
			lastCmd = 'z'
			continue
		case 'm':
			x, y, err := p.pair()
			if err != nil {
				return nil, err
			}
			closeCur()
			pos = abs(x, y)
			start = pos
			// Further pairs after a moveto are implicit linetos.
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'l':
			x, y, err := p.pair()
			if err != nil {
				return nil, err
			}
			to := abs(x, y)
			cur = append(cur, line(pos, to))
			pos = to
		case 'h':
			x, err := p.num()
			if err != nil {
				return nil, err
			}
			to := f32.Pt(x, pos.Y)
			if rel {
				to.X += pos.X
			}
			cur = append(cur, line(pos, to))
			pos = to
		case 'v':
			y, err := p.num()
			if err != nil {
				return nil, err
			}
			to := f32.Pt(pos.X, y)
			if rel {
				to.Y += pos.Y
			}
			cur = append(cur, line(pos, to))
			pos = to
		case 'c':
			v, err := p.nums(6)
			if err != nil {
				return nil, err
			}
			c1, c2, to := abs(v[0], v[1]), abs(v[2], v[3]), abs(v[4], v[5])
			cur = append(cur, seg{pos, c1, c2, to})
			pos, lastCtl = to, c2
		case 's':
			v, err := p.nums(4)
			if err != nil {
				return nil, err
			}
			c1 := pos
			if l := lastCmd | 0x20; l == 'c' || l == 's' {
				c1 = pos.Mul(2).Sub(lastCtl)
			}
			c2, to := abs(v[0], v[1]), abs(v[2], v[3])
			cur = append(cur, seg{pos, c1, c2, to})
			pos, lastCtl = to, c2
		case 'q':
			v, err := p.nums(4)
			if err != nil {
				return nil, err
			}
			q, to := abs(v[0], v[1]), abs(v[2], v[3])
			cur = append(cur, quad(pos, q, to))
			pos, lastCtl = to, q
		case 't':
			v, err := p.nums(2)
			if err != nil {
				return nil, err
			}
			q := pos
			if l := lastCmd | 0x20; l == 'q' || l == 't' {
				q = pos.Mul(2).Sub(lastCtl)
			}
			to := abs(v[0], v[1])
			cur = append(cur, quad(pos, q, to))
			pos, lastCtl = to, q
		case 'a':
			v, err := p.nums(3)
			if err != nil {
				return nil, err
			}
			large, err := p.flag()
			if err != nil {
				return nil, err
			}
			sweep, err := p.flag()
			if err != nil {
				return nil, err
			}
			x, y, err := p.pair()
			if err != nil {
				return nil, err
			}
			to := abs(x, y)
			cur = append(cur, arc(pos, to, v[0], v[1], v[2], large, sweep)...)
			pos = to
		default:
			return nil, fmt.Errorf("unsupported path command %q", cmd)
		}
		lastCmd = cmd
	}
	closeCur()
	return out, nil
}

func quad(p0, q, p1 f32.Point) seg {
	return seg{p0, p0.Add(q.Sub(p0).Mul(2.0 / 3)), p1.Add(q.Sub(p1).Mul(2.0 / 3)), p1}
}

// arc converts an SVG elliptical arc to cubics (SVG 1.1 appendix F.6).
func arc(from, to f32.Point, rx, ry, phiDeg float32, large, sweep bool) []seg {
	if from == to {
		return nil
	}
	if rx == 0 || ry == 0 {
		return []seg{line(from, to)}
	}
	x1, y1, x2, y2 := float64(from.X), float64(from.Y), float64(to.X), float64(to.Y)
	rX, rY := math.Abs(float64(rx)), math.Abs(float64(ry))
	phi := float64(phiDeg) * math.Pi / 180
	sin, cos := math.Sincos(phi)
	dx, dy := (x1-x2)/2, (y1-y2)/2
	x1p, y1p := cos*dx+sin*dy, -sin*dx+cos*dy
	if l := x1p*x1p/(rX*rX) + y1p*y1p/(rY*rY); l > 1 {
		s := math.Sqrt(l)
		rX, rY = rX*s, rY*s
	}
	num := rX*rX*rY*rY - rX*rX*y1p*y1p - rY*rY*x1p*x1p
	den := rX*rX*y1p*y1p + rY*rY*x1p*x1p
	co := math.Sqrt(math.Max(0, num/den))
	if large == sweep {
		co = -co
	}
	cxp, cyp := co*rX*y1p/rY, -co*rY*x1p/rX
	cx, cy := cos*cxp-sin*cyp+(x1+x2)/2, sin*cxp+cos*cyp+(y1+y2)/2
	ang := func(ux, uy, vx, vy float64) float64 {
		return math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy)
	}
	t1 := ang(1, 0, (x1p-cxp)/rX, (y1p-cyp)/rY)
	dt := ang((x1p-cxp)/rX, (y1p-cyp)/rY, (-x1p-cxp)/rX, (-y1p-cyp)/rY)
	if !sweep && dt > 0 {
		dt -= 2 * math.Pi
	} else if sweep && dt < 0 {
		dt += 2 * math.Pi
	}
	n := int(math.Ceil(math.Abs(dt) / (math.Pi / 2)))
	step := dt / float64(n)
	k := 4.0 / 3 * math.Tan(step/4)
	pt := func(t float64) (f32.Point, f32.Point) {
		st, ct := math.Sincos(t)
		ex, ey := rX*ct, rY*st
		tx, ty := -rX*st, rY*ct // derivative
		p := f32.Pt(float32(cos*ex-sin*ey+cx), float32(sin*ex+cos*ey+cy))
		d := f32.Pt(float32(cos*tx-sin*ty), float32(sin*tx+cos*ty))
		return p, d
	}
	segs := make([]seg, 0, n)
	a := from
	_, da := pt(t1)
	for i := 1; i <= n; i++ {
		b, db := pt(t1 + step*float64(i))
		if i == n {
			b = to
		}
		segs = append(segs, seg{a, a.Add(da.Mul(float32(k))), b.Sub(db.Mul(float32(k))), b})
		a, da = b, db
	}
	return segs
}

func isCmd(c byte) bool {
	switch c | 0x20 {
	case 'm', 'z', 'l', 'h', 'v', 'c', 's', 'q', 't', 'a':
		return true
	}
	return false
}

type scanner struct {
	s string
	i int
}

func (p *scanner) done() bool { return p.i >= len(p.s) }

func (p *scanner) skipSep() {
	for !p.done() {
		switch p.s[p.i] {
		case ' ', ',', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

// num reads one number; SVG lets "1.5.5" mean 1.5 and .5, and "1-2" mean 1
// and -2, which mingcute's minified data relies on.
func (p *scanner) num() (float32, error) {
	p.skipSep()
	start := p.i
	if !p.done() && (p.s[p.i] == '-' || p.s[p.i] == '+') {
		p.i++
	}
	dot, exp := false, false
	for !p.done() {
		c := p.s[p.i]
		switch {
		case c >= '0' && c <= '9':
		case c == '.' && !dot && !exp:
			dot = true
		case (c == 'e' || c == 'E') && !exp:
			exp = true
			if p.i+1 < len(p.s) && (p.s[p.i+1] == '-' || p.s[p.i+1] == '+') {
				p.i++
			}
		default:
			goto end
		}
		p.i++
	}
end:
	v, err := strconv.ParseFloat(p.s[start:p.i], 32)
	if err != nil {
		return 0, fmt.Errorf("bad number at %d: %w", start, err)
	}
	return float32(v), nil
}

func (p *scanner) pair() (float32, float32, error) {
	x, err := p.num()
	if err != nil {
		return 0, 0, err
	}
	y, err := p.num()
	return x, y, err
}

func (p *scanner) nums(n int) ([]float32, error) {
	v := make([]float32, n)
	for i := range v {
		var err error
		if v[i], err = p.num(); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// flag reads an arc flag, which may be packed without separators ("011").
func (p *scanner) flag() (bool, error) {
	p.skipSep()
	if p.done() || (p.s[p.i] != '0' && p.s[p.i] != '1') {
		return false, fmt.Errorf("bad arc flag at %d", p.i)
	}
	p.i++
	return p.s[p.i-1] == '1', nil
}
