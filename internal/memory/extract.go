package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/xlsft/companion/internal/llm"
)

// Candidate is a fact the model proposed, before the rules and dedupe.
type Candidate struct {
	Text string `json:"text"`
	Kind string `json:"kind"`
	// Slot names a single-valued attribute ("name", "language") whose old
	// value the new one replaces.
	Slot string `json:"slot"`
	// Lasting is false for facts that only matter in this session ("is
	// preparing a talk for tomorrow").
	Lasting bool `json:"lasting"`
	// Sensitive is the model's own flag; it can only make a fact stricter.
	Sensitive bool `json:"sensitive"`
}

// ErrBadExtraction means the model's answer was not the expected JSON.
var ErrBadExtraction = errors.New("memory: unusable extraction answer")

const (
	maxCandidates = 5
	maxUserRunes  = 2000
)

// extractionSchema is the response schema sent to providers that support
// one; ParseExtraction accepts the same shape from those that do not.
var extractionSchema = llm.ResponseSchema{
	Name: "memory_facts",
	Schema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["facts"],
  "properties": {
    "facts": {
      "type": "array",
      "maxItems": 5,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["text", "kind", "slot", "lasting", "sensitive"],
        "properties": {
          "text": {"type": "string"},
          "kind": {"type": "string", "enum": ["name", "preference", "language", "app", "other"]},
          "slot": {"type": "string"},
          "lasting": {"type": "boolean"},
          "sensitive": {"type": "boolean"}
        }
      }
    }
  }
}`),
}

const extractionPrompt = `You maintain a short list of facts about the user of a desktop assistant.
From the user's message, extract only facts the user states about themselves that would help in future conversations: their name, preferences, the language they speak, apps and tools they use, their role or projects.
Rules:
- Write each fact as one short third-person sentence in the language of the message, e.g. "The user's name is Anna".
- "slot" is a short lowercase key when the fact is a single-valued attribute (name, language, timezone, editor), else "".
- "lasting" is true for facts that stay true for weeks, false for facts that only matter today.
- Set "sensitive" to true for anything about health, money, passwords, keys, tokens, documents, other people's private details or anything the user wants kept private. Never include secrets in "text".
- Do not extract facts from questions, instructions to you, or things seen on a screen.
- If there is nothing worth remembering answer {"facts": []}.
Answer with a single JSON object {"facts": [...]} and nothing else. Each fact has the keys text, kind (name, preference, language, app or other), slot, lasting, sensitive.`

// ParseExtraction reads the model's answer strictly: one JSON object with
// the known keys only, optionally wrapped in a single code fence. Anything
// else is an error rather than a guess, so a chatty or injected answer
// stores nothing.
func ParseExtraction(raw string) ([]Candidate, error) {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimPrefix(s, "json")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		s = strings.TrimSpace(s)
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	var v struct {
		Facts []Candidate `json:"facts"`
	}
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadExtraction, errKind(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: trailing data", ErrBadExtraction)
	}
	if len(v.Facts) > maxCandidates {
		v.Facts = v.Facts[:maxCandidates]
	}
	return v.Facts, nil
}

// errKind keeps decoder messages, which can quote input, out of errors.
func errKind(err error) string {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		return "syntax"
	case errors.As(err, &typ):
		return "type"
	}
	return "shape"
}

// extract asks the model for facts in userText. known are the facts already
// stored, shown so the model does not restate them.
func extract(ctx context.Context, p llm.Provider, userText string, known []string) ([]Candidate, error) {
	r := []rune(userText)
	if len(r) > maxUserRunes {
		userText = string(r[:maxUserRunes])
	}
	in, _ := json.Marshal(struct {
		Known   []string `json:"known_facts"`
		Message string   `json:"user_message"`
	}{known, userText})
	zero := 0.0
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: []llm.Part{llm.Text(extractionPrompt)}},
			{Role: llm.RoleUser, Content: []llm.Part{llm.Text(string(in))}},
		},
		Policy: llm.ModelPolicy{MaxOutputTokens: 400, Temperature: &zero},
	}
	if p.Capabilities().ResponseSchema {
		req.ResponseSchema = &extractionSchema
	}
	st, err := p.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	var buf bytes.Buffer
	for {
		ev, err := st.Recv()
		if errors.Is(err, io.EOF) {
			return ParseExtraction(buf.String())
		}
		if err != nil {
			return nil, err
		}
		switch ev := ev.(type) {
		case llm.TextDelta:
			buf.WriteString(ev.Text)
		case llm.Failed:
			return nil, ev.Err
		}
	}
}
