package overlay

import (
	"image"
	"testing"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// One animated frame of a busy scene on a 2560×1080 display; the frame
// loop runs every frameEvery, so this must stay well below it.
func BenchmarkAnimatedFrame(b *testing.B) {
	born := time.Unix(100, 0)
	shapes := []shape{
		{kind: KindSpotlight, pts: []contracts.Point{pt(800, 400)}, radius: 150, scale: 1, born: born},
		{kind: KindRing, pts: []contracts.Point{pt(800, 400)}, radius: 51, scale: 1, born: born, style: Style{Emphasis: true}},
		{kind: KindPoint, pts: []contracts.Point{pt(800, 400)}, scale: 1, born: born},
		{kind: KindLabel, pts: []contracts.Point{pt(870, 400)}, text: "Метроном", scale: 1, born: born},
		{kind: KindBrackets, pts: []contracts.Point{pt(400, 300), pt(1200, 520)}, scale: 1, born: born},
		{kind: KindHighlight, pts: []contracts.Point{pt(450, 380), pt(1100, 420)}, scale: 1, born: born},
		{kind: KindArrow, pts: []contracts.Point{pt(1600, 700), pt(1200, 520)}, scale: 1, born: born},
		{kind: KindBadge, pts: []contracts.Point{pt(400, 300)}, text: "1", scale: 1, born: born},
	}
	f := image.NewRGBA(image.Rect(0, 0, 2560, 1080))
	now := born.Add(2 * time.Second)
	for i := range shapes {
		shapes[i].cache = &shapeCache{}
	}
	var drawn image.Rectangle
	for b.Loop() {
		drawn, _ = renderInto(f, shapes, now, true, drawn)
		now = now.Add(frameEvery)
	}
}
