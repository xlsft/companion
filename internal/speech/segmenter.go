// Package speech turns a streaming LLM answer into speakable segments so
// TTS can start before the answer is complete (FR-VOICE-004).
package speech

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Segmenter accumulates text deltas and emits complete sentences. The first
// segment is allowed to be shorter (cut at a clause boundary) to reduce
// time to first audio; later segments prefer whole sentences.
type Segmenter struct {
	// MinFirst is the minimum rune count before the first segment may be
	// cut at a comma or similar clause boundary.
	MinFirst int
	// Max forces a cut at the last space once a segment grows this long,
	// so a run-on sentence doesn't stall speech.
	Max int

	buf     strings.Builder
	emitted int
}

func NewSegmenter() *Segmenter {
	return &Segmenter{MinFirst: 40, Max: 300}
}

// Push adds a text delta and returns zero or more complete segments.
func (s *Segmenter) Push(delta string) []string {
	s.buf.WriteString(delta)
	var out []string
	for {
		seg, ok := s.next(false)
		if !ok {
			return out
		}
		out = append(out, seg)
	}
}

// Flush returns whatever is left at the end of the answer.
func (s *Segmenter) Flush() []string {
	var out []string
	for {
		seg, ok := s.next(true)
		if !ok {
			return out
		}
		out = append(out, seg)
	}
}

func (s *Segmenter) next(final bool) (string, bool) {
	text := s.buf.String()
	cut := s.findCut(text, final)
	if cut <= 0 {
		return "", false
	}
	seg := cleanForSpeech(text[:cut])
	rest := strings.TrimLeftFunc(text[cut:], unicode.IsSpace)
	s.buf.Reset()
	s.buf.WriteString(rest)
	if seg == "" {
		// Pure markup or whitespace: consume it and continue.
		return s.next(final)
	}
	s.emitted++
	return seg, true
}

// findCut returns the byte offset after which the buffer can be spoken, or
// 0 if more text is needed.
func (s *Segmenter) findCut(text string, final bool) int {
	if strings.TrimSpace(text) == "" {
		if final && text != "" {
			return len(text)
		}
		return 0
	}
	runes := 0
	lastSpace, lastClause := -1, -1
	inCode := false
	for i, r := range text {
		runes++
		if r == '`' {
			inCode = !inCode
		}
		next, _ := utf8.DecodeRuneInString(text[i+utf8.RuneLen(r):])
		atEnd := i+utf8.RuneLen(r) == len(text)
		boundary := atEnd || unicode.IsSpace(next)
		switch {
		case r == '\n' && i > 0 && text[i-1] == '\n':
			// Paragraph break.
			return i + 1
		case !inCode && boundary && isSentenceEnd(r) && !atEnd:
			if !abbreviationBefore(text[:i+1]) {
				return i + utf8.RuneLen(r)
			}
		case !inCode && boundary && isClauseEnd(r) && !atEnd:
			lastClause = i + utf8.RuneLen(r)
		case unicode.IsSpace(r):
			lastSpace = i
		}
		if s.emitted == 0 && runes >= s.MinFirst && lastClause > 0 {
			return lastClause
		}
		if runes >= s.Max && lastSpace > 0 {
			return lastSpace
		}
	}
	if final {
		return len(text)
	}
	return 0
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '.', '!', '?', '…', '。', '！', '？':
		return true
	}
	return false
}

func isClauseEnd(r rune) bool {
	switch r {
	case ',', ';', ':', '—', '–':
		return true
	}
	return false
}

// abbreviationBefore reports whether the period ending text belongs to an
// abbreviation or a number like "3.5", "т.е." or "e.g." rather than a
// sentence end.
func abbreviationBefore(text string) bool {
	if !strings.HasSuffix(text, ".") {
		return false
	}
	word := text[:len(text)-1]
	if i := strings.LastIndexFunc(word, unicode.IsSpace); i >= 0 {
		word = word[i+1:]
	}
	word = strings.ToLower(strings.TrimLeft(word, "(\"«"))
	switch word {
	case "e.g", "i.e", "etc", "vs", "mr", "mrs", "dr", "т.е", "т.д", "т.п", "т.к", "см", "напр", "г", "гг", "рис", "стр":
		return true
	}
	// Single letters ("A. Smith", "т. е.") are initials or abbreviations.
	return utf8.RuneCountInString(word) == 1
}

// cleanForSpeech strips Markdown that sounds bad when read aloud while
// keeping the words; the visible answer keeps the original text.
func cleanForSpeech(s string) string {
	var b strings.Builder
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		t := strings.TrimSpace(line)
		t = strings.TrimLeft(t, "#>")
		t = strings.TrimSpace(t)
		if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") {
			t = t[2:]
		}
		if t == "" || strings.HasPrefix(t, "```") {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(t)
	}
	out := strings.NewReplacer("**", "", "__", "", "`", "").Replace(b.String())
	return strings.TrimSpace(out)
}
