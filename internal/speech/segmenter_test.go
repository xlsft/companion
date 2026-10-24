package speech

import (
	"reflect"
	"strings"
	"testing"
)

// feed pushes text in small chunks, like a token stream, then flushes.
func feed(s *Segmenter, text string, chunk int) []string {
	var out []string
	for len(text) > 0 {
		n := min(chunk, len(text))
		// Don't split inside a UTF-8 sequence.
		for n < len(text) && text[n]&0xC0 == 0x80 {
			n++
		}
		out = append(out, s.Push(text[:n])...)
		text = text[n:]
	}
	return append(out, s.Flush()...)
}

func TestSegments(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"sentences", "Open the File menu. Then click Export! Done?",
			[]string{"Open the File menu.", "Then click Export!", "Done?"}},
		{"decimal and abbreviation", "Set it to 3.5 px, e.g. in the panel. Next step.",
			[]string{"Set it to 3.5 px, e.g. in the panel.", "Next step."}},
		{"russian", "Нажмите «Файл», затем «Экспорт», т.е. второй пункт. Готово.",
			[]string{"Нажмите «Файл», затем «Экспорт», т.е. второй пункт.", "Готово."}},
		{"markdown stripped", "## Steps\n\n- Click **Save**.\n- Press `Ctrl+S`.",
			[]string{"Steps", "Click Save.", "Press Ctrl+S."}},
		{"no terminal punctuation", "just a fragment", []string{"just a fragment"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSegmenter()
			s.MinFirst = 1000 // disable early clause cut for exact sentence tests
			for _, chunk := range []int{1, 3, 1000} {
				s := *s
				s.buf.Reset()
				got := feed(&s, tt.in, chunk)
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("chunk %d: got %q, want %q", chunk, got, tt.want)
				}
			}
		})
	}
}

func TestFirstSegmentCutsEarlyAtClause(t *testing.T) {
	s := NewSegmenter()
	text := "To export the project as a video file, open the File menu, choose Export and pick MP4. Then wait."
	got := feed(s, text, 4)
	if len(got) < 2 || !strings.HasSuffix(got[0], ",") {
		t.Fatalf("first segment should end at a clause: %q", got)
	}
	if strings.Join(got, " ") != text {
		t.Fatalf("segments lost text: %q", got)
	}
}

func TestLongRunOnIsSplit(t *testing.T) {
	s := NewSegmenter()
	s.Max = 50
	got := feed(s, strings.Repeat("word ", 40), 7)
	for _, seg := range got {
		if len([]rune(seg)) > 50 {
			t.Fatalf("segment too long: %d runes", len([]rune(seg)))
		}
	}
	if len(got) < 3 {
		t.Fatalf("expected several segments, got %d", len(got))
	}
}
