package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/pkg/contracts"
)

// ProviderFunc returns the model used for extraction. The core passes the
// Talk provider: the same key, no second account to set up.
type ProviderFunc func(ctx context.Context) (llm.Provider, error)

// Config wires a Service. The zero value of every field is usable: no
// repo keeps facts volatile, no provider disables learning, nil switches
// read as off.
type Config struct {
	// Repo is the persistent profile; nil keeps every fact volatile.
	Repo *Repo
	// Provider performs extraction; nil learns nothing.
	Provider ProviderFunc
	// Enabled reports settings.Memory.Enabled right now. nil: off.
	Enabled func() bool
	// SaveTranscripts reports settings.Privacy.SaveTranscripts. It decides
	// whether a stored fact may point at the turn it came from.
	SaveTranscripts func() bool
	Budget          Budget
	Log             *slog.Logger
}

const (
	extractTimeout = 30 * time.Second
	// maxBlockFacts bounds the "what I know" block in the system prompt.
	maxBlockFacts = 30
)

var slotRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Service is the memory behind the Talk pipeline and the Settings page.
type Service struct {
	cfg  Config
	conv Conversation

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	busy   chan struct{} // one extraction at a time

	mu       sync.Mutex
	volatile []Fact
	volSeq   int
	lastIDs  []string // facts the previous turn touched, for "forget that"
}

// New returns a Service. Close ends any extraction still running.
func New(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Budget == (Budget{}) {
		cfg.Budget = DefaultBudget
	}
	s := &Service{cfg: cfg, busy: make(chan struct{}, 1)}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s
}

// Close cancels running extractions and waits for them.
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}

func (s *Service) enabled() bool { return s.cfg.Enabled != nil && s.cfg.Enabled() }

// Seed loads the turns of a reopened conversation as context.
func (s *Service) Seed(turns []Turn) { s.conv.Seed(turns) }

// Reset starts a new session: the context and the volatile facts go.
func (s *Service) Reset() {
	s.conv.Reset()
	s.mu.Lock()
	s.volatile, s.lastIDs = nil, nil
	s.mu.Unlock()
}

// Prepared is what the model request gets from memory.
type Prepared struct {
	// System is appended to the system prompt: the facts block and, after a
	// forget request, a note that it was carried out. May be empty.
	System string
	// History are the earlier turns that fit the budget, oldest first.
	History []llm.Message
	// Forgot is the number of facts a forget request removed.
	Forgot int
	// Forget is set when the message was a forget request.
	Forget bool
}

// Prepare runs before the model call for text. It carries out a forget
// request first, so the answer is built without the forgotten facts.
func (s *Service) Prepare(ctx context.Context, text string) Prepared {
	var p Prepared
	if f, ok := ParseForget(text); ok {
		p.Forget = true
		p.Forgot = s.forget(ctx, f)
	}
	p.History = s.conv.Messages(s.cfg.Budget)
	var sys []string
	if s.enabled() {
		if b := s.block(ctx); b != "" {
			sys = append(sys, b)
		}
	}
	if p.Forget {
		if p.Forgot > 0 {
			sys = append(sys, "The user asked you to forget something and it has been deleted from memory. Confirm in a few words.")
		} else {
			sys = append(sys, "The user asked you to forget something, but nothing matching was stored. Say so in a few words.")
		}
	}
	p.System = strings.Join(sys, "\n\n")
	return p
}

// forget deletes the facts a forget request names, from the profile and
// from this session, and returns how many went. The earlier turns that
// carried them are dropped from the context too, or the model would just
// read them again.
func (s *Service) forget(ctx context.Context, f Forget) int {
	if f.Last {
		// The turn before may still be learning.
		s.waitIdle(ctx)
	}
	all, err := s.entries(ctx)
	if err != nil {
		s.cfg.Log.Warn("memory: forget list failed", "err", err)
	}
	s.mu.Lock()
	last := slices.Clone(s.lastIDs)
	if f.Last {
		s.lastIDs = nil
	}
	s.mu.Unlock()
	n := 0
	for _, fact := range all {
		hit := false
		if f.Last {
			hit = slices.Contains(last, fact.ID)
		} else {
			hit = matchesTopic(fact.Text, f.Topic)
		}
		if hit && s.Delete(ctx, fact.ID) == nil {
			n++
		}
	}
	if f.Last {
		s.conv.DropLast()
	} else {
		s.conv.DropMatching(f.Topic)
	}
	return n
}

// block is the "what I know about you" text for the system prompt.
func (s *Service) block(ctx context.Context) string {
	facts, err := s.entries(ctx)
	if err != nil {
		s.cfg.Log.Warn("memory: read profile failed", "err", err)
	}
	if len(facts) == 0 {
		return ""
	}
	if len(facts) > maxBlockFacts {
		facts = facts[:maxBlockFacts]
	}
	var b strings.Builder
	b.WriteString("What you know about the user, from earlier. Use it only when it helps, and don't recite it unprompted:")
	for _, f := range facts {
		b.WriteString("\n- ")
		b.WriteString(f.Text)
	}
	return b.String()
}

// Record is called after a turn was answered. It adds the exchange to the
// context and, when memory is on, starts learning from it in the
// background.
func (s *Service) Record(ctx context.Context, ref contracts.SessionRef, user, answer string) {
	s.conv.Append(Turn{User: user, Assistant: answer})
	if s.ctx.Err() != nil || !s.shouldLearn(user) {
		return
	}
	source := ""
	if s.cfg.SaveTranscripts != nil && s.cfg.SaveTranscripts() {
		source = fmt.Sprintf("%s:%d", ref.ID, ref.Generation)
	}
	select {
	case s.busy <- struct{}{}:
	default:
		s.cfg.Log.Debug("memory: extraction busy, turn skipped")
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.busy }()
		s.learn(user, source)
	}()
}

func (s *Service) shouldLearn(user string) bool {
	if !s.enabled() || s.cfg.Provider == nil {
		return false
	}
	user = strings.TrimSpace(user)
	if len([]rune(user)) < 4 || IsPrivate(user) {
		return false
	}
	if _, forget := ParseForget(user); forget {
		return false
	}
	// A message that carries a credential or a card is not shown to the
	// extraction call at all.
	return sensitive(user, secretShapes) == ""
}

func (s *Service) learn(user, source string) {
	ctx, cancel := context.WithTimeout(s.ctx, extractTimeout)
	defer cancel()
	p, err := s.cfg.Provider(ctx)
	if err != nil {
		s.cfg.Log.Warn("memory: extraction provider unavailable")
		return
	}
	var known []string
	if facts, err := s.entries(ctx); err == nil {
		for _, f := range facts {
			known = append(known, f.Text)
		}
	}
	cands, err := extract(ctx, p, user, known)
	if err != nil {
		if ctx.Err() == nil {
			s.cfg.Log.Warn("memory: extraction failed", "bad_answer", errors.Is(err, ErrBadExtraction))
		}
		return
	}
	var touched []string
	seen := map[string]bool{}
	for _, c := range cands {
		f, ok := s.accept(c, seen)
		if !ok {
			continue
		}
		f.SourceTurn = source
		if !c.Lasting || s.cfg.Repo == nil {
			touched = append(touched, s.addVolatile(f))
			continue
		}
		stored, _, err := s.cfg.Repo.Upsert(ctx, f)
		if err != nil {
			s.cfg.Log.Warn("memory: store fact failed", "err", err)
			continue
		}
		touched = append(touched, stored.ID)
	}
	s.mu.Lock()
	s.lastIDs = touched
	s.mu.Unlock()
}

// accept applies the rules to one proposal.
func (s *Service) accept(c Candidate, seen map[string]bool) (Fact, bool) {
	text := strings.Join(strings.Fields(c.Text), " ")
	if c.Sensitive {
		s.cfg.Log.Debug("memory: fact dropped", "reason", "flagged")
		return Fact{}, false
	}
	if ok, reason := Allowed(text); !ok {
		s.cfg.Log.Debug("memory: fact dropped", "reason", reason)
		return Fact{}, false
	}
	// The slot and kind are model text too.
	slot := strings.ToLower(strings.TrimSpace(c.Slot))
	if !slotRe.MatchString(slot) {
		slot = ""
	}
	norm := normalize(text)
	if seen[norm] {
		return Fact{}, false
	}
	seen[norm] = true
	return Fact{Scope: ScopeGlobal, Kind: kindOf(c.Kind), Slot: slot, Text: text}, true
}

// addVolatile keeps a session-only fact, applying the same dedupe and slot
// replacement as the profile.
func (s *Service) addVolatile(f Fact) string {
	now := time.Now()
	norm := normalize(f.Text)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.volatile {
		v := &s.volatile[i]
		if normalize(v.Text) == norm || (f.Slot != "" && v.Slot == f.Slot) {
			v.Text, v.Kind, v.Slot, v.SourceTurn, v.UpdatedAt = f.Text, f.Kind, f.Slot, f.SourceTurn, now
			return v.ID
		}
	}
	s.volSeq++
	f.ID, f.Volatile, f.CreatedAt, f.UpdatedAt = fmt.Sprintf("volatile-%d", s.volSeq), true, now, now
	s.volatile = append(s.volatile, f)
	return f.ID
}

// Wait blocks until background learning is done or ctx ends.
func (s *Service) Wait(ctx context.Context) { s.waitIdle(ctx) }

// waitIdle takes the extraction slot, which is only free between runs.
func (s *Service) waitIdle(ctx context.Context) {
	select {
	case s.busy <- struct{}{}:
		<-s.busy
	case <-ctx.Done():
	}
}

func (s *Service) entries(ctx context.Context) ([]Fact, error) {
	var out []Fact
	var err error
	if s.cfg.Repo != nil {
		out, err = s.cfg.Repo.List(ctx, ScopeGlobal)
	}
	s.mu.Lock()
	vol := slices.Clone(s.volatile)
	s.mu.Unlock()
	slices.Reverse(vol)
	return append(out, vol...), err
}

// Facts lists everything remembered: the profile, then this session's
// volatile facts.
func (s *Service) Facts(ctx context.Context) ([]Fact, error) { return s.entries(ctx) }

// Delete removes one fact, volatile or stored.
func (s *Service) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	for i, f := range s.volatile {
		if f.ID == id {
			s.volatile = slices.Delete(s.volatile, i, i+1)
			s.mu.Unlock()
			return nil
		}
	}
	s.mu.Unlock()
	if s.cfg.Repo == nil {
		return nil
	}
	return s.cfg.Repo.Delete(ctx, id)
}

// Clear forgets everything: the profile and this session's facts.
func (s *Service) Clear(ctx context.Context) error {
	s.mu.Lock()
	s.volatile, s.lastIDs = nil, nil
	s.mu.Unlock()
	if s.cfg.Repo == nil {
		return nil
	}
	return s.cfg.Repo.DeleteAll(ctx)
}
