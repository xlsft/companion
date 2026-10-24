package memory

import (
	"regexp"
	"strings"
)

// Reason codes for a rejected fact. They are technical codes, safe to log.
const (
	ReasonEmpty   = "empty"
	ReasonLong    = "long"
	ReasonSecret  = "secret"
	ReasonPayment = "payment"
	ReasonHealth  = "health"
	ReasonID      = "identity"
	ReasonNumber  = "number"
)

const maxFactRunes = 200

type rule struct {
	reason string
	re     *regexp.Regexp
	subs   []string // lowercase substrings, for scripts where \b is useless
}

// Go's \b is ASCII-only, so Cyrillic and CJK words are matched as
// substrings.
var rules = []rule{
	{reason: ReasonSecret,
		re:   regexp.MustCompile(`(?i)\b(passwords?|passwd|passphrase|api[ _-]?keys?|secret|tokens?|private key|credentials?|pin code|otp|2fa|recovery code)\b|\bsk-[A-Za-z0-9_-]{12,}|\bAKIA[0-9A-Z]{12,}|\bgh[pousr]_[A-Za-z0-9]{16,}|\bxox[abprs]-[A-Za-z0-9-]{8,}|\beyJ[A-Za-z0-9_-]{10,}\.|-----BEGIN|[A-Za-z0-9+/_=-]{32,}`),
		subs: []string{"парол", "токен", "секрет", "api-ключ", "ключ api", "пин-код", "учётные данные", "учетные данные", "密码", "密碼", "パスワード", "비밀번호"}},
	{reason: ReasonPayment,
		re:   regexp.MustCompile(`(?i)\b(credit card|debit card|card number|cvv|cvc|iban|bank account|routing number|swift)\b|\b[A-Z]{2}\d{2}[A-Z0-9]{10,30}\b`),
		subs: []string{"номер карты", "банковск", "кредитн", "дебетов", "расчетный счет", "расчётный счёт", "银行卡", "信用卡", "クレジットカード", "신용카드"}},
	{reason: ReasonHealth,
		re:   regexp.MustCompile(`(?i)(diagnos|medic|prescription|disease|illness|cancer|diabet|depress|anxiety|therap|pregnan|\bhiv\b|surgery|allerg|disorder|symptom|mental health|\badhd\b|autis|chronic|disabilit|\bsick\b)`),
		subs: []string{"диагноз", "болезн", "лекарств", "заболеван", "беремен", "депресс", "терапевт", "аллерг", "врач", "таблетк", "онколог", "диабет", "психи", "здоровь", "病", "药", "医", "診断", "病気", "薬", "질병", "진단", "우울"}},
	{reason: ReasonID,
		re:   regexp.MustCompile(`(?i)\b(ssn|social security|passport|driver'?s licen[cs]e|national id|tax id)\b|\b\d{3}-\d{2}-\d{4}\b`),
		subs: []string{"паспорт", "снилс", "护照", "身份证", "パスポート", "여권"}},
}

var digitRun = regexp.MustCompile(`\d[\d\s-]{11,}\d`)

// Allowed reports whether text may be remembered, with the reason code
// when it may not. It is the last line of defence: it runs on whatever the
// model proposed, so a prompt that is ignored or injected still cannot put
// secrets into the profile.
func Allowed(text string) (bool, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return false, ReasonEmpty
	}
	if len([]rune(text)) > maxFactRunes {
		return false, ReasonLong
	}
	if r := sensitive(text, nil); r != "" {
		return false, r
	}
	return true, ""
}

// sensitive returns the reason of the first matching rule among only (all
// rules when only is nil), or "".
func sensitive(text string, only map[string]bool) string {
	low := strings.ToLower(text)
	for _, r := range rules {
		if only != nil && !only[r.reason] {
			continue
		}
		if r.re.MatchString(text) {
			return r.reason
		}
		for _, s := range r.subs {
			if strings.Contains(low, s) {
				return r.reason
			}
		}
	}
	if only == nil || only[ReasonPayment] {
		for _, m := range digitRun.FindAllString(text, -1) {
			if luhn(m) {
				return ReasonPayment
			}
		}
	}
	// Phone numbers, account numbers and the like: nothing a profile needs.
	if only == nil && longestDigitRun(text) >= 9 {
		return ReasonNumber
	}
	return ""
}

// secretShapes are what makes a whole user message unfit to send to the
// extraction call: it looks like it carries a credential or a card.
var secretShapes = map[string]bool{ReasonSecret: true, ReasonPayment: true, ReasonID: true}

func longestDigitRun(s string) int {
	best, cur := 0, 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			cur++
			best = max(best, cur)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '+':
		default:
			cur = 0
		}
	}
	return best
}

func luhn(s string) bool {
	var ds []int
	for _, r := range s {
		if r >= '0' && r <= '9' {
			ds = append(ds, int(r-'0'))
		}
	}
	if len(ds) < 13 || len(ds) > 19 {
		return false
	}
	sum := 0
	for i, alt := len(ds)-1, false; i >= 0; i-- {
		d := ds[i]
		if alt {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}

var privateMarkers = []string{
	"don't remember", "do not remember", "dont remember", "don't save this", "do not save this",
	"off the record", "keep this private", "this is private", "private:", "between us", "don't store",
	"не запоминай", "не запомин", "не сохраняй", "это личное", "это приватно", "между нами",
	"不要记住", "别记", "覚えないで", "기억하지 마",
}

// IsPrivate reports whether the user asked for this message not to be
// remembered.
func IsPrivate(text string) bool {
	low := strings.ToLower(text)
	for _, m := range privateMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// Forget is a spoken or typed request to delete facts.
type Forget struct {
	// Last: "forget that", the facts learned from the previous turn.
	Last bool
	// Topic names what to forget; empty when Last.
	Topic string
}

var forgetRe = regexp.MustCompile(`(?i)^\s*(?:please |пожалуйста,? )?(?:forget|delete from memory|remove from memory|забудь|удали из памяти)(?:\s|[,:.!-]|$)\s*(.*?)[\s.!]*$`)

var topicFiller = []string{"what i said about ", "what i told you about ", "about ", "that i ", "что я ", "про ", "о ", "об "}

// ParseForget recognises the forget intent. "forget everything" is not an
// intent: wiping the whole profile needs the confirmation in Settings, and
// a mis-heard sentence must not do it.
func ParseForget(text string) (Forget, bool) {
	m := forgetRe.FindStringSubmatch(text)
	if m == nil {
		return Forget{}, false
	}
	rest := strings.ToLower(strings.TrimSpace(m[1]))
	switch rest {
	case "", "that", "this", "it", "that please", "что", "это", "его", "то", "это пожалуйста":
		return Forget{Last: true}, true
	case "everything", "all", "all of it", "всё", "все", "всё это", "все это":
		return Forget{}, false
	}
	for _, f := range topicFiller {
		rest = strings.TrimPrefix(rest, f)
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return Forget{Last: true}, true
	}
	return Forget{Topic: rest}, true
}

var wordSplit = regexp.MustCompile(`[\p{L}\p{N}]+`)

func words(s string) []string { return wordSplit.FindAllString(strings.ToLower(s), -1) }

var stopWords = map[string]bool{"the": true, "my": true, "our": true, "his": true, "her": true, "its": true, "and": true,
	"мой": true, "моя": true, "мою": true, "мои": true, "моё": true, "мое": true}

// matchesTopic reports whether the fact covers every word of topic that is
// long enough to mean something. A shared stem is enough for inflected
// words ("собаку" finds "собака").
func matchesTopic(fact, topic string) bool {
	fw := words(fact)
	significant := 0
	for _, tw := range words(topic) {
		if len([]rune(tw)) < 3 {
			continue
		}
		if stopWords[tw] {
			continue
		}
		significant++
		found := false
		for _, w := range fw {
			if w == tw || (len([]rune(tw)) >= 4 && len([]rune(w)) >= 4 && (strings.HasPrefix(w, stem(tw)) || strings.HasPrefix(tw, stem(w)))) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return significant > 0
}

// stem drops the last rune or two of a long word, a crude stem that lets
// case endings match.
func stem(w string) string {
	r := []rune(w)
	switch {
	case len(r) >= 6:
		return string(r[:len(r)-2])
	case len(r) >= 4:
		return string(r[:len(r)-1])
	}
	return w
}
