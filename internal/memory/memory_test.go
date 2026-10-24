package memory

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/store"
	"github.com/xlsft/companion/pkg/contracts"
)

// fakeProvider answers every Stream with the next scripted text and keeps
// the requests.
type fakeProvider struct {
	mu      sync.Mutex
	answers []string
	reqs    []llm.ChatRequest
	schema  bool
	err     error
}

func (p *fakeProvider) Capabilities() llm.Capabilities {
	return llm.Capabilities{ResponseSchema: p.schema}
}

func (p *fakeProvider) Stream(_ context.Context, req llm.ChatRequest) (llm.EventStream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, req)
	if p.err != nil {
		return nil, p.err
	}
	text := `{"facts": []}`
	if len(p.answers) > 0 {
		text, p.answers = p.answers[0], p.answers[1:]
	}
	return &textStream{events: []llm.Event{llm.TextDelta{Text: text}, llm.Completed{}}}, nil
}

func (p *fakeProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

type textStream struct{ events []llm.Event }

func (s *textStream) Recv() (llm.Event, error) {
	if len(s.events) == 0 {
		return nil, io.EOF
	}
	ev := s.events[0]
	s.events = s.events[1:]
	return ev, nil
}
func (s *textStream) Close() error { return nil }

func openRepo(t *testing.T, path string) (*Repo, *store.DB) {
	t.Helper()
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewRepo(db), db
}

type harness struct {
	svc      *Service
	repo     *Repo
	prov     *fakeProvider
	enabled  bool
	saveText bool
}

func newHarness(t *testing.T, answers ...string) *harness {
	t.Helper()
	h := &harness{prov: &fakeProvider{answers: answers}, enabled: true}
	h.repo, _ = openRepo(t, filepath.Join(t.TempDir(), "m.db"))
	h.svc = New(Config{
		Repo:            h.repo,
		Provider:        func(context.Context) (llm.Provider, error) { return h.prov, nil },
		Enabled:         func() bool { return h.enabled },
		SaveTranscripts: func() bool { return h.saveText },
	})
	t.Cleanup(h.svc.Close)
	return h
}

var ref = contracts.SessionRef{ID: "s1", Generation: 3}

func (h *harness) say(t *testing.T, user string) Prepared {
	t.Helper()
	ctx := context.Background()
	p := h.svc.Prepare(ctx, user)
	h.svc.Record(ctx, ref, user, "ok")
	h.svc.Wait(ctx)
	return p
}

func texts(fs []Fact) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Text)
	}
	return out
}

func TestMessagesBudget(t *testing.T) {
	long := strings.Repeat("x", 4000) // about 1000 tokens
	tests := []struct {
		name   string
		turns  []Turn
		budget Budget
		want   []string // user texts replayed, oldest first
	}{
		{"all fit", []Turn{{"a", "A"}, {"b", "B"}}, Budget{Turns: 8, Tokens: 1000}, []string{"a", "b"}},
		{"turn limit keeps newest", []Turn{{"a", "A"}, {"b", "B"}, {"c", "C"}}, Budget{Turns: 2, Tokens: 1000}, []string{"b", "c"}},
		{"token limit drops oldest", []Turn{{long, "A"}, {"b", "B"}, {"c", "C"}}, Budget{Turns: 8, Tokens: 100}, []string{"b", "c"}},
		{"oversized newest leaves nothing", []Turn{{"a", "A"}, {long, "B"}}, Budget{Turns: 8, Tokens: 100}, nil},
		{"no gap past an oversized turn", []Turn{{"a", "A"}, {long, "B"}, {"c", "C"}}, Budget{Turns: 8, Tokens: 100}, []string{"c"}},
		{"defaults", []Turn{{"a", "A"}}, Budget{}, []string{"a"}},
		{"unanswered turn", []Turn{{"a", ""}, {"b", "B"}}, Budget{}, []string{"a", "b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var c Conversation
			c.Seed(tc.turns)
			var got []string
			for _, m := range c.Messages(tc.budget) {
				if m.Role == llm.RoleUser {
					got = append(got, m.Content[0].Text)
				}
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("replayed %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMessagesAlternateRoles(t *testing.T) {
	var c Conversation
	c.Seed([]Turn{{"q1", "a1"}, {"q2", "a2"}})
	msgs := c.Messages(Budget{})
	want := []llm.Role{llm.RoleUser, llm.RoleAssistant, llm.RoleUser, llm.RoleAssistant}
	if len(msgs) != len(want) {
		t.Fatalf("%d messages", len(msgs))
	}
	for i, m := range msgs {
		if m.Role != want[i] {
			t.Errorf("message %d role %s", i, m.Role)
		}
	}
}

func TestReopenedConversationReachesPrepare(t *testing.T) {
	h := newHarness(t)
	h.svc.Seed([]Turn{{"what is Go", "A language."}, {"who made it", "Google."}})
	p := h.svc.Prepare(context.Background(), "and when")
	if len(p.History) != 4 {
		t.Fatalf("history = %d messages, want 4", len(p.History))
	}
	h.svc.Reset()
	if p := h.svc.Prepare(context.Background(), "x"); len(p.History) != 0 {
		t.Fatalf("history after reset: %d", len(p.History))
	}
}

func TestParseExtraction(t *testing.T) {
	good := `{"facts":[{"text":"The user's name is Anna","kind":"name","slot":"name","lasting":true,"sensitive":false}]}`
	tests := []struct {
		name    string
		raw     string
		wantN   int
		wantErr bool
	}{
		{"plain", good, 1, false},
		{"fenced", "```json\n" + good + "\n```", 1, false},
		{"empty list", `{"facts": []}`, 0, false},
		{"prose around", "Sure! " + good, 0, true},
		{"trailing data", good + `{"x":1}`, 0, true},
		{"unknown field", `{"facts":[],"extra":1}`, 0, true},
		{"unknown fact field", `{"facts":[{"text":"a","evil":1}]}`, 0, true},
		{"wrong type", `{"facts":"none"}`, 0, true},
		{"not json", `no facts`, 0, true},
		{"empty", ``, 0, true},
		{"capped", `{"facts":[{"text":"1"},{"text":"2"},{"text":"3"},{"text":"4"},{"text":"5"},{"text":"6"},{"text":"7"}]}`, 5, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseExtraction(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tc.wantErr && !errors.Is(err, ErrBadExtraction) {
				t.Errorf("err %v is not ErrBadExtraction", err)
			}
			if len(got) != tc.wantN {
				t.Errorf("got %d facts, want %d", len(got), tc.wantN)
			}
		})
	}
}

func TestParseErrorsDoNotQuoteInput(t *testing.T) {
	_, err := ParseExtraction(`{"facts": SECRET-CANARY}`)
	if err == nil || strings.Contains(err.Error(), "CANARY") {
		t.Fatalf("error leaks input: %v", err)
	}
}

func TestAllowed(t *testing.T) {
	tests := []struct {
		text   string
		reason string // "" means allowed
	}{
		{"The user's name is Anna", ""},
		{"Prefers short answers", ""},
		{"Uses VS Code and Firefox", ""},
		{"Пользователя зовут Анна", ""},
		{"Speaks Russian", ""},
		{"", ReasonEmpty},
		{strings.Repeat("a b ", 80), ReasonLong},
		{"The user's password is hunter2", ReasonSecret},
		{"API key is sk-abcdefghijklmnop1234", ReasonSecret},
		{"Token ghp_abcdefghijklmnopqrstuv", ReasonSecret},
		{"Пароль от почты 12345", ReasonSecret},
		{"Credit card 4111 1111 1111 1111", ReasonPayment},
		{"Card 4111-1111-1111-1111", ReasonPayment},
		{"IBAN DE89370400440532013000", ReasonPayment},
		{"Номер карты 4111 1111 1111 1111", ReasonPayment},
		{"Has type 2 diabetes", ReasonHealth},
		{"Takes medication daily", ReasonHealth},
		{"У пользователя диагноз", ReasonHealth},
		{"Passport number is AB1234567", ReasonID},
		{"SSN 123-45-6789", ReasonID},
		{"Phone is +1 (555) 123 4567 89", ReasonNumber},
		{"Account 123456789", ReasonNumber},
		{"Born in 1990 and lives in Berlin", ""},
	}
	for _, tc := range tests {
		t.Run(tc.text, func(t *testing.T) {
			ok, reason := Allowed(tc.text)
			if ok != (tc.reason == "") || reason != tc.reason {
				t.Fatalf("Allowed = %v, %q; want reason %q", ok, reason, tc.reason)
			}
		})
	}
}

func TestParseForget(t *testing.T) {
	tests := []struct {
		in   string
		want Forget
		ok   bool
	}{
		{"forget that", Forget{Last: true}, true},
		{"Forget it.", Forget{Last: true}, true},
		{"please forget this!", Forget{Last: true}, true},
		{"забудь", Forget{Last: true}, true},
		{"Забудь это", Forget{Last: true}, true},
		{"забудь, пожалуйста", Forget{Topic: "пожалуйста"}, true},
		{"forget about my dog", Forget{Topic: "my dog"}, true},
		{"забудь про собаку", Forget{Topic: "собаку"}, true},
		{"forget everything", Forget{}, false},
		{"забудь всё", Forget{}, false},
		{"don't forget to buy milk", Forget{}, false},
		{"I forget my keys", Forget{}, false},
		{"забудьте", Forget{}, false},
		{"what is the capital", Forget{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := ParseForget(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("ParseForget = %+v, %v; want %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestIsPrivate(t *testing.T) {
	for in, want := range map[string]bool{
		"don't remember this: my boss is annoying": true,
		"Это личное, не запоминай":                 true,
		"my name is Anna": false,
	} {
		if got := IsPrivate(in); got != want {
			t.Errorf("IsPrivate(%q) = %v", in, got)
		}
	}
}

func TestLearnsAndFiltersFacts(t *testing.T) {
	h := newHarness(t, `{"facts":[
		{"text":"The user's name is Anna","kind":"name","slot":"name","lasting":true,"sensitive":false},
		{"text":"The user's password is hunter2","kind":"other","slot":"","lasting":true,"sensitive":false},
		{"text":"Has diabetes","kind":"other","slot":"","lasting":true,"sensitive":false},
		{"text":"Uses Firefox","kind":"app","slot":"bad slot!","lasting":true,"sensitive":false},
		{"text":"Is preparing a talk for tomorrow","kind":"other","slot":"","lasting":false,"sensitive":false}
	]}`)
	h.say(t, "My name is Anna and I use Firefox")
	stored, _ := h.repo.List(context.Background())
	got := strings.Join(texts(stored), "|")
	if !strings.Contains(got, "Anna") || !strings.Contains(got, "Firefox") || len(stored) != 2 {
		t.Fatalf("stored = %q", got)
	}
	for _, f := range stored {
		if f.Text == "Uses Firefox" && f.Slot != "" {
			t.Errorf("bad slot kept: %q", f.Slot)
		}
		if f.SourceTurn != "" {
			t.Errorf("source turn %q stored although transcripts are not saved", f.SourceTurn)
		}
	}
	all, _ := h.svc.Facts(context.Background())
	var vol []Fact
	for _, f := range all {
		if f.Volatile {
			vol = append(vol, f)
		}
	}
	if len(vol) != 1 || vol[0].Text != "Is preparing a talk for tomorrow" {
		t.Fatalf("volatile = %v", texts(vol))
	}
}

func TestModelFlaggedSensitiveIsDropped(t *testing.T) {
	h := newHarness(t, `{"facts":[{"text":"Likes jazz","kind":"preference","slot":"","lasting":true,"sensitive":true}]}`)
	h.say(t, "I like jazz a lot")
	if all, _ := h.svc.Facts(context.Background()); len(all) != 0 {
		t.Fatalf("stored %v", texts(all))
	}
}

func TestSourceTurnFollowsSaveTranscripts(t *testing.T) {
	h := newHarness(t, `{"facts":[{"text":"Likes tea","kind":"preference","slot":"","lasting":true,"sensitive":false}]}`)
	h.saveText = true
	h.say(t, "I like tea a lot")
	fs, _ := h.repo.List(context.Background())
	if len(fs) != 1 || fs[0].SourceTurn != "s1:3" {
		t.Fatalf("facts = %+v", fs)
	}
}

func TestDedupeAndSlotMerge(t *testing.T) {
	h := newHarness(t,
		`{"facts":[{"text":"The user's name is Anna","kind":"name","slot":"name","lasting":true,"sensitive":false},{"text":"the user's  name is ANNA","kind":"name","slot":"","lasting":true,"sensitive":false}]}`,
		`{"facts":[{"text":"The user's name is Anna","kind":"name","slot":"name","lasting":true,"sensitive":false}]}`,
		`{"facts":[{"text":"The user's name is Anya","kind":"name","slot":"name","lasting":true,"sensitive":false}]}`,
	)
	ctx := context.Background()
	h.say(t, "I'm Anna")
	if fs, _ := h.repo.List(ctx); len(fs) != 1 {
		t.Fatalf("in-batch duplicate stored: %v", texts(fs))
	}
	first, _ := h.repo.List(ctx)
	h.say(t, "Again, I'm Anna")
	fs, _ := h.repo.List(ctx)
	if len(fs) != 1 || fs[0].ID != first[0].ID || !fs[0].UpdatedAt.After(first[0].UpdatedAt) && !fs[0].UpdatedAt.Equal(first[0].UpdatedAt) {
		t.Fatalf("repeat not merged: %+v", fs)
	}
	h.say(t, "Actually call me Anya")
	fs, _ = h.repo.List(ctx)
	if len(fs) != 1 || fs[0].Text != "The user's name is Anya" || fs[0].ID != first[0].ID {
		t.Fatalf("slot not replaced: %+v", fs)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	ctx := context.Background()
	repo, db := openRepo(t, path)
	if _, _, err := repo.Upsert(ctx, Fact{Kind: KindLanguage, Slot: "language", Text: "Speaks Russian", SourceTurn: "s:1"}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	repo2, _ := openRepo(t, path)
	fs, err := repo2.List(ctx, ScopeGlobal)
	if err != nil || len(fs) != 1 || fs[0].Text != "Speaks Russian" || fs[0].Scope != ScopeGlobal || fs[0].Kind != KindLanguage || fs[0].SourceTurn != "s:1" || fs[0].CreatedAt.IsZero() {
		t.Fatalf("after reopen: %+v, %v", fs, err)
	}
}

func TestScopesAreSeparate(t *testing.T) {
	repo, _ := openRepo(t, filepath.Join(t.TempDir(), "m.db"))
	ctx := context.Background()
	for _, f := range []Fact{{Text: "Likes tea"}, {Scope: AgentScope("a1"), Text: "Likes tea"}, {Scope: AgentScope("a1"), Text: "Reviews code"}} {
		if _, _, err := repo.Upsert(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	if g, _ := repo.List(ctx, ScopeGlobal); len(g) != 1 {
		t.Errorf("global = %d", len(g))
	}
	if a, _ := repo.List(ctx, AgentScope("a1")); len(a) != 2 {
		t.Errorf("agent = %d", len(a))
	}
	if all, _ := repo.List(ctx); len(all) != 3 {
		t.Errorf("all = %d", len(all))
	}
}

func TestDisabledStoresNothing(t *testing.T) {
	h := newHarness(t, `{"facts":[{"text":"Likes tea","kind":"preference","slot":"","lasting":true,"sensitive":false}]}`)
	h.enabled = false
	p := h.say(t, "I like tea a lot")
	if h.prov.calls() != 0 {
		t.Error("extraction ran although memory is off")
	}
	all, _ := h.svc.Facts(context.Background())
	if len(all) != 0 {
		t.Errorf("facts stored while off: %v", texts(all))
	}
	if p.System != "" {
		t.Errorf("system block while off: %q", p.System)
	}
	// Existing facts are kept but not used while off.
	if _, _, err := h.repo.Upsert(context.Background(), Fact{Text: "Likes jazz"}); err != nil {
		t.Fatal(err)
	}
	if p := h.svc.Prepare(context.Background(), "hi"); p.System != "" {
		t.Errorf("stored facts used while off: %q", p.System)
	}
	h.enabled = true
	if p := h.svc.Prepare(context.Background(), "hi"); !strings.Contains(p.System, "Likes jazz") {
		t.Errorf("facts missing when on: %q", p.System)
	}
}

func TestNoExtractionForSkippedTurns(t *testing.T) {
	tests := []struct{ name, user string }{
		{"private", "don't remember this, my name is Anna"},
		{"russian private", "Не запоминай: меня зовут Анна"},
		{"forget", "forget that"},
		{"card", "my card is 4111 1111 1111 1111 call me Anna"},
		{"password", "my password is hunter2 and I am Anna"},
		{"too short", "hi"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, `{"facts":[{"text":"Name is Anna","kind":"name","slot":"name","lasting":true,"sensitive":false}]}`)
			h.say(t, tc.user)
			if h.prov.calls() != 0 {
				t.Fatal("turn was sent to extraction")
			}
			if fs, _ := h.repo.List(context.Background()); len(fs) != 0 {
				t.Fatalf("stored %v", texts(fs))
			}
		})
	}
}

func TestBadAnswerStoresNothing(t *testing.T) {
	for _, ans := range []string{"I would remember Anna", `{"facts": [{"text": }]}`, ""} {
		h := newHarness(t, ans)
		h.say(t, "My name is Anna")
		if all, _ := h.svc.Facts(context.Background()); len(all) != 0 {
			t.Errorf("answer %q stored %v", ans, texts(all))
		}
	}
}

func TestProviderFailureIsHarmless(t *testing.T) {
	h := newHarness(t)
	h.prov.err = errors.New("boom")
	h.say(t, "My name is Anna")
	h.svc.cfg.Provider = func(context.Context) (llm.Provider, error) { return nil, errors.New("no provider") }
	h.say(t, "My name is Anna")
	if all, _ := h.svc.Facts(context.Background()); len(all) != 0 {
		t.Errorf("stored %v", texts(all))
	}
}

func TestExtractionRequestShape(t *testing.T) {
	for _, schema := range []bool{false, true} {
		h := newHarness(t)
		h.prov.schema = schema
		h.say(t, "My name is Anna")
		if h.prov.calls() != 1 {
			t.Fatalf("calls = %d", h.prov.calls())
		}
		req := h.prov.reqs[0]
		if (req.ResponseSchema != nil) != schema {
			t.Errorf("schema=%v but request has one: %v", schema, req.ResponseSchema != nil)
		}
		if len(req.Tools) != 0 || len(req.Attachments) != 0 {
			t.Error("extraction must not carry tools or pictures")
		}
		if strings.Contains(req.Messages[1].Content[0].Text, "assistant") && strings.Contains(req.Messages[1].Content[0].Text, "ok") {
			// Only the user's words are sent, never the model's answer.
			t.Error("answer text sent to extraction")
		}
	}
}

func TestForgetThat(t *testing.T) {
	h := newHarness(t, `{"facts":[{"text":"Has a dog called Rex","kind":"other","slot":"","lasting":true,"sensitive":false}]}`)
	ctx := context.Background()
	h.say(t, "I have a dog called Rex")
	if fs, _ := h.repo.List(ctx); len(fs) != 1 {
		t.Fatalf("setup: %v", texts(fs))
	}
	p := h.svc.Prepare(ctx, "forget that")
	if !p.Forget || p.Forgot != 1 || !strings.Contains(p.System, "deleted") {
		t.Fatalf("prepared = %+v", p)
	}
	if fs, _ := h.repo.List(ctx); len(fs) != 0 {
		t.Fatalf("not deleted: %v", texts(fs))
	}
	for _, m := range p.History {
		if strings.Contains(m.Content[0].Text, "Rex") {
			t.Error("forgotten turn is still in the context")
		}
	}
	if strings.Contains(p.System, "Rex") {
		t.Error("forgotten fact in the system prompt")
	}
}

func TestForgetTopic(t *testing.T) {
	h := newHarness(t,
		`{"facts":[{"text":"Has a dog called Rex","kind":"other","slot":"","lasting":true,"sensitive":false},{"text":"Likes jazz","kind":"preference","slot":"","lasting":true,"sensitive":false}]}`)
	ctx := context.Background()
	h.say(t, "I have a dog called Rex and like jazz")
	p := h.svc.Prepare(ctx, "forget about my dog")
	if p.Forgot != 1 {
		t.Fatalf("forgot %d", p.Forgot)
	}
	fs, _ := h.repo.List(ctx)
	if len(fs) != 1 || fs[0].Text != "Likes jazz" {
		t.Fatalf("remaining %v", texts(fs))
	}
	if p := h.svc.Prepare(ctx, "forget about spaceships"); p.Forgot != 0 || !strings.Contains(p.System, "nothing matching") {
		t.Fatalf("no-match prepared = %+v", p)
	}
}

func TestForgetVolatile(t *testing.T) {
	h := newHarness(t, `{"facts":[{"text":"Is writing a report today","kind":"other","slot":"","lasting":false,"sensitive":false}]}`)
	h.say(t, "I am writing a report today")
	if p := h.svc.Prepare(context.Background(), "забудь это"); p.Forgot != 1 {
		t.Fatalf("forgot %d", p.Forgot)
	}
	if all, _ := h.svc.Facts(context.Background()); len(all) != 0 {
		t.Fatalf("left %v", texts(all))
	}
}

func TestForgetWorksWhileDisabled(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	stored, _, _ := h.repo.Upsert(ctx, Fact{Text: "Has a dog called Rex"})
	h.enabled = false
	if p := h.svc.Prepare(ctx, "forget about the dog"); p.Forgot != 1 {
		t.Fatalf("forgot %d", p.Forgot)
	}
	if fs, _ := h.repo.List(ctx); len(fs) != 0 {
		t.Fatalf("left %v (%s)", texts(fs), stored.ID)
	}
}

func TestVolatileDroppedAtSessionEnd(t *testing.T) {
	h := newHarness(t, `{"facts":[{"text":"Is writing a report today","kind":"other","slot":"","lasting":false,"sensitive":false}]}`)
	ctx := context.Background()
	h.say(t, "I am writing a report today")
	if p := h.svc.Prepare(ctx, "next"); !strings.Contains(p.System, "report") {
		t.Fatalf("volatile fact not offered: %q", p.System)
	}
	h.svc.Reset()
	if p := h.svc.Prepare(ctx, "next"); strings.Contains(p.System, "report") {
		t.Fatal("volatile fact survived the session")
	}
}

func TestVolatileOnlyWithoutRepo(t *testing.T) {
	prov := &fakeProvider{answers: []string{`{"facts":[{"text":"Likes tea","kind":"preference","slot":"","lasting":true,"sensitive":false}]}`}}
	svc := New(Config{Provider: func(context.Context) (llm.Provider, error) { return prov, nil }, Enabled: func() bool { return true }})
	defer svc.Close()
	svc.Record(context.Background(), ref, "I like tea", "ok")
	svc.Wait(context.Background())
	fs, _ := svc.Facts(context.Background())
	if len(fs) != 1 || !fs[0].Volatile {
		t.Fatalf("facts = %+v", fs)
	}
}

func TestDeleteAndClear(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a, _, _ := h.repo.Upsert(ctx, Fact{Text: "Likes tea"})
	h.repo.Upsert(ctx, Fact{Text: "Likes jazz"})
	h.svc.addVolatile(Fact{Text: "Is busy"})
	if err := h.svc.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := h.svc.Facts(ctx); len(all) != 2 {
		t.Fatalf("after delete: %v", texts(all))
	}
	if err := h.svc.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if all, _ := h.svc.Facts(ctx); len(all) != 0 {
		t.Fatalf("after clear: %v", texts(all))
	}
}

func TestProfileIsBounded(t *testing.T) {
	repo, _ := openRepo(t, filepath.Join(t.TempDir(), "m.db"))
	ctx := context.Background()
	for i := range maxFactsPerScope + 10 {
		if _, _, err := repo.Upsert(ctx, Fact{Text: "fact number " + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26))}); err != nil {
			t.Fatal(err)
		}
	}
	if fs, _ := repo.List(ctx); len(fs) > maxFactsPerScope {
		t.Fatalf("%d facts", len(fs))
	}
}
