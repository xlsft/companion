// Package logging provides redaction for technical logs and diagnostics
// (FR-DATA-006): logs never carry screenshots, audio, prompt content,
// answers, keys or tokens.
package logging

import (
	"regexp"
	"strings"
	"unicode"
)

// Masked replaces anything removed by redaction. It must never match any of
// the secret patterns itself, so redaction stays idempotent.
const Masked = "[REDACTED]"

// sensitiveWords are matched against the lower-cased words of an attribute
// key (split on _ - . and camelCase), so "api_key" and "apiKey" match while
// "keyboard" and "tokens_used" do not.
var sensitiveWords = map[string]bool{
	"key": true, "apikey": true, "token": true, "authorization": true,
	"password": true, "passwd": true, "secret": true, "prompt": true,
	"transcript": true, "text": true, "content": true, "response": true,
	"image": true, "audio": true, "screenshot": true, "bearer": true,
}

// IsSensitiveKey reports whether values under the attribute key must be masked.
func IsSensitiveKey(key string) bool {
	for _, w := range splitKey(key) {
		if sensitiveWords[w] {
			return true
		}
	}
	return false
}

func splitKey(key string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(key)
	for i, r := range rs {
		switch {
		case r == '_' || r == '-' || r == '.' || r == ' ' || r == ':':
			flush()
		case unicode.IsUpper(r) && i > 0 && unicode.IsLower(rs[i-1]):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words
}

var (
	reBearer = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
	reSKAnt  = regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{8,}`)
	reSK     = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{10,}`)
	reXI     = regexp.MustCompile(`\bxi-[A-Za-z0-9_-]{10,}`)
	reJWT    = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`)
	// key=value / key: value pairs inside free text.
	reKV   = regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|token|secret|password|passwd|authorization)(["']?\s*[=:]\s*["']?)[^\s"',;]{8,}`)
	reBlob = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)
)

// blobIsSecret decides whether a long token looks like a key rather than a
// path or an identifier made of words.
func blobIsSecret(s string) bool {
	var letter, digit bool
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case unicode.IsLetter(r):
			letter = true
		}
	}
	if !letter || !digit {
		return false
	}
	return strings.Count(s, "/") < 3 // deeper slashes look like a file path
}

// RedactString masks secret-looking substrings in s.
func RedactString(s string) string {
	if s == "" {
		return s
	}
	s = reBearer.ReplaceAllString(s, "$1 "+Masked)
	s = reSKAnt.ReplaceAllString(s, Masked)
	s = reSK.ReplaceAllString(s, Masked)
	s = reXI.ReplaceAllString(s, Masked)
	s = reJWT.ReplaceAllString(s, Masked)
	s = reKV.ReplaceAllString(s, "${1}${2}"+Masked)
	s = reBlob.ReplaceAllStringFunc(s, func(m string) string {
		if blobIsSecret(m) {
			return Masked
		}
		return m
	})
	return s
}

// ContainsSecret reports whether RedactString would change s. Export uses it
// as a last safety net.
func ContainsSecret(s string) bool {
	return RedactString(s) != s
}
