// Package bench measures the STT sidecar's latency and accuracy over the
// real protocol (OC-37). It is pure Go; the sidecar is a separate process.
package bench

import (
	"sort"
	"strings"
	"unicode"
)

// Normalize prepares a transcript for scoring: case-folded, punctuation
// removed, ё folded into е (Russian writers use them interchangeably),
// whitespace collapsed. Digits stay: the references are written the way
// the model is expected to write them.
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r == 'ё':
			b.WriteRune('е')
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r):
			b.WriteRune(r)
		case r == '\'' || r == '’':
			// Dropped, so "don't" and "dont" are one word: whisper and the
			// references disagree on apostrophes.
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Edits is the Levenshtein distance between two token sequences.
func Edits[T comparable](ref, hyp []T) int {
	prev := make([]int, len(hyp)+1)
	cur := make([]int, len(hyp)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ref); i++ {
		cur[0] = i
		for j := 1; j <= len(hyp); j++ {
			cost := 1
			if ref[i-1] == hyp[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(hyp)]
}

// ErrorCounts are the numerator and denominator of an error rate, kept
// separate so corpus rates weigh long utterances by their length.
type ErrorCounts struct {
	WordEdits int `json:"word_edits"`
	WordRef   int `json:"word_ref"`
	CharEdits int `json:"char_edits"`
	CharRef   int `json:"char_ref"`
}

// Score compares one hypothesis with its reference after normalisation.
func Score(ref, hyp string) ErrorCounts {
	r, h := Normalize(ref), Normalize(hyp)
	rw, hw := strings.Fields(r), strings.Fields(h)
	rc, hc := []rune(r), []rune(h)
	return ErrorCounts{
		WordEdits: Edits(rw, hw), WordRef: len(rw),
		CharEdits: Edits(rc, hc), CharRef: len(rc),
	}
}

func (e *ErrorCounts) Add(o ErrorCounts) {
	e.WordEdits += o.WordEdits
	e.WordRef += o.WordRef
	e.CharEdits += o.CharEdits
	e.CharRef += o.CharRef
}

func (e ErrorCounts) WER() float64 { return ratio(e.WordEdits, e.WordRef) }
func (e ErrorCounts) CER() float64 { return ratio(e.CharEdits, e.CharRef) }

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// Percentile returns the p-th percentile (0..100) by linear interpolation;
// 0 for an empty sample.
func Percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	pos := p / 100 * float64(len(s)-1)
	lo := int(pos)
	if lo >= len(s)-1 {
		return s[len(s)-1]
	}
	return s[lo] + (s[lo+1]-s[lo])*(pos-float64(lo))
}
