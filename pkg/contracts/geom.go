package contracts

// Point is a position in the coordinate space stated by its owner.
type Point struct {
	X, Y float64
}

// Size is a width/height pair in the coordinate space stated by its owner.
type Size struct {
	W, H float64
}

// Rect is an axis-aligned rectangle; Min is inclusive, Max is exclusive.
type Rect struct {
	Min, Max Point
}

func RectXYWH(x, y, w, h float64) Rect {
	return Rect{Min: Point{x, y}, Max: Point{x + w, y + h}}
}

func (r Rect) Width() float64  { return r.Max.X - r.Min.X }
func (r Rect) Height() float64 { return r.Max.Y - r.Min.Y }
func (r Rect) Empty() bool     { return r.Min.X >= r.Max.X || r.Min.Y >= r.Max.Y }

func (r Rect) Contains(p Point) bool {
	return p.X >= r.Min.X && p.X < r.Max.X && p.Y >= r.Min.Y && p.Y < r.Max.Y
}

// Intersect returns the overlap of r and s, or an empty Rect.
func (r Rect) Intersect(s Rect) Rect {
	out := Rect{
		Min: Point{max(r.Min.X, s.Min.X), max(r.Min.Y, s.Min.Y)},
		Max: Point{min(r.Max.X, s.Max.X), min(r.Max.Y, s.Max.Y)},
	}
	if out.Empty() {
		return Rect{}
	}
	return out
}

// PixelSize is an integer size of a raster in native pixels.
type PixelSize struct {
	W, H int
}
