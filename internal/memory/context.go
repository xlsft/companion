package memory

import (
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/xlsft/companion/internal/llm"
)

// Turn is one exchange of the conversation, text only: pixels of past
// screenshots are never resent.
type Turn struct {
	User      string
	Assistant string // empty for a turn that got no answer
}

// Budget bounds the context sent with a request.
type Budget struct {
	// Turns is the most exchanges to replay.
	Turns int
	// Tokens is the estimated size the replayed text may take.
	Tokens int
}

// DefaultBudget keeps follow-ups cheap: the last eight exchanges, capped
// at roughly 3000 tokens so a long reopened conversation cannot crowd out
// the screenshot and the answer.
var DefaultBudget = Budget{Turns: 8, Tokens: 3000}

const maxKeptTurns = 64

// EstimateTokens is a deliberately rough, provider-independent size: about
// one token per four ASCII bytes, one per other character (CJK and Cyrillic
// tokenise much worse than English).
func EstimateTokens(s string) int {
	ascii, other := 0, 0
	for _, r := range s {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+3)/4 + other + 4
}

// Conversation is the in-memory context of the open conversation.
type Conversation struct {
	mu    sync.Mutex
	turns []Turn
}

// Seed replaces the context, e.g. with the turns of a reopened stored
// conversation.
func (c *Conversation) Seed(turns []Turn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turns = nil
	for _, t := range turns {
		c.appendLocked(t)
	}
}

// Reset empties the context (a new chat).
func (c *Conversation) Reset() {
	c.mu.Lock()
	c.turns = nil
	c.mu.Unlock()
}

// Append adds a finished exchange.
func (c *Conversation) Append(t Turn) {
	c.mu.Lock()
	c.appendLocked(t)
	c.mu.Unlock()
}

func (c *Conversation) appendLocked(t Turn) {
	if t.User == "" {
		return
	}
	c.turns = append(c.turns, t)
	if n := len(c.turns); n > maxKeptTurns {
		c.turns = append([]Turn(nil), c.turns[n-maxKeptTurns:]...)
	}
}

// Len is the number of exchanges kept.
func (c *Conversation) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.turns)
}

// Messages returns the newest exchanges that fit b, oldest first. An
// exchange is taken whole or not at all, and older ones are never taken
// past one that does not fit, so the replay is always a contiguous tail.
func (c *Conversation) Messages(b Budget) []llm.Message {
	if b.Turns <= 0 {
		b.Turns = DefaultBudget.Turns
	}
	if b.Tokens <= 0 {
		b.Tokens = DefaultBudget.Tokens
	}
	c.mu.Lock()
	turns := append([]Turn(nil), c.turns...)
	c.mu.Unlock()

	start, used := len(turns), 0
	for i := len(turns) - 1; i >= 0 && len(turns)-i <= b.Turns; i-- {
		cost := EstimateTokens(turns[i].User)
		if turns[i].Assistant != "" {
			cost += EstimateTokens(turns[i].Assistant)
		}
		if used+cost > b.Tokens {
			break
		}
		used += cost
		start = i
	}
	var out []llm.Message
	for _, t := range turns[start:] {
		out = append(out, llm.Message{Role: llm.RoleUser, Content: []llm.Part{llm.Text(t.User)}})
		if t.Assistant != "" {
			out = append(out, llm.Message{Role: llm.RoleAssistant, Content: []llm.Part{llm.Text(t.Assistant)}})
		}
	}
	return out
}

// DropLast removes the newest exchange, for "forget that".
func (c *Conversation) DropLast() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := len(c.turns); n > 0 {
		c.turns = c.turns[:n-1]
	}
}

// DropMatching removes the exchanges whose user text mentions topic.
func (c *Conversation) DropMatching(topic string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turns = slices.DeleteFunc(c.turns, func(t Turn) bool { return matchesTopic(t.User, topic) })
}
