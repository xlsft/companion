package chat

import (
	"context"
	"errors"
	"image"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/companion/internal/history"
	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/pkg/contracts"
)

// Talk is the part of *session.Manager the chat drives.
type Talk interface {
	Post(session.Event) error
	Subscribe() (<-chan session.Snapshot, func())
}

// Store is the part of *history.Repo the chat reads and writes.
type Store interface {
	List(ctx context.Context, p history.Page) ([]history.Conversation, error)
	Get(ctx context.Context, id string) (*history.Conversation, error)
	CreateConversation(ctx context.Context, title, agentID string) (*history.Conversation, error)
	AppendMessage(ctx context.Context, convID string, m history.Message, captures []contracts.CaptureMetadata, imgs map[contracts.CaptureID]*image.RGBA) (string, error)
	// SetKeepScreenshots is the per-conversation opt-in that lets
	// AppendMessage write images.
	SetKeepScreenshots(ctx context.Context, id string, keep bool) error
}

// Config wires a Service. Only Talk is required.
type Config struct {
	Talk Talk
	// History keeps turns between runs; nil keeps the chat in memory.
	History Store
	// Save reports whether turns may be written to History right now
	// (settings.Privacy.SaveTranscripts); nil means never.
	Save func() bool
	// Caps reports voice availability and the header labels; Text is
	// derived from Talk. nil means text only, without labels.
	Caps func() Capabilities
	// STTModel names the recogniser for voice turns, e.g. "whisper medium".
	STTModel func() string
	Now      func() time.Time
	Log      *slog.Logger
}

const (
	titleRunes = 48
	queueLen   = 64
	opTimeout  = 5 * time.Second
	// providerSTT marks the recogniser in a stored voice turn's ProviderID,
	// so the STT model label survives a restart.
	providerSTT = "stt"
)

// ErrClosed is returned after Close.
var ErrClosed = errors.New("chat: closed")

// Service is the Home window's chat backend over the Talk session and the
// local history.
type Service struct {
	cfg  Config
	jobs chan job

	mu      sync.Mutex
	started bool
	closed  bool
	cancel  context.CancelFunc
	done    sync.WaitGroup
	subs    map[chan View]struct{}

	// Transcript state, guarded by mu.
	title    string
	msgs     []Message
	phase    Phase
	partial  string
	since    time.Time
	ref      contracts.SessionRef
	reply    int  // index of the reply of ref in msgs, -1 if none yet
	pendingQ bool // the user turn of ref is waiting for its reply
	// ignore is the generation a new chat left behind: its late snapshots
	// still move the phase but add nothing to the new conversation.
	ignore contracts.SessionRef
	// held is the user turn of heldRef that is not stored yet, because its
	// screenshots are only known once the request reaches the model; -1 if
	// none. heldShots are the metadata and, only when kept, the pictures.
	held      int
	heldRef   contracts.SessionRef
	heldShots []contracts.ScreenAttachment
	// early keeps screenshots reported before the user turn was seen.
	early *early
}

type early struct {
	ref   contracts.SessionRef
	shots []contracts.ScreenAttachment
}

var _ contracts.AttachmentSink = (*Service)(nil)

// New returns a Service; Start begins following the session.
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{cfg: cfg, jobs: make(chan job, queueLen), subs: map[chan View]struct{}{}, reply: -1, held: -1}
}

// Start loads the most recent stored conversation and follows the session
// until ctx ends or Close is called.
func (s *Service) Start(ctx context.Context) error {
	if s.cfg.Talk == nil {
		return errors.New("chat: Config.Talk is required")
	}
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return nil
	}
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	convID := s.load(ctx)
	snaps, unsub := s.cfg.Talk.Subscribe()
	s.done.Add(2)
	go func() {
		defer s.done.Done()
		s.persistLoop(ctx, convID)
	}()
	go func() {
		defer s.done.Done()
		defer unsub()
		for {
			select {
			case snap, ok := <-snaps:
				if !ok {
					return
				}
				s.apply(snap)
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}

// Close stops following the session and closes every subscription.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	cancel := s.cancel
	for ch := range s.subs {
		close(ch)
		delete(s.subs, ch)
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.done.Wait()
}

func (s *Service) Capabilities() Capabilities {
	var c Capabilities
	if s.cfg.Caps != nil {
		c = s.cfg.Caps()
	}
	c.Text = s.cfg.Talk != nil
	return c
}

// Subscribe returns the current view followed by every change. A slow
// reader skips intermediate views: each view is the whole chat, so only
// the latest matters.
func (s *Service) Subscribe() (<-chan View, func()) {
	ch := make(chan View, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		close(ch)
		return ch, func() {}
	}
	s.subs[ch] = struct{}{}
	ch <- s.viewLocked()
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
	}
}

// Send submits a typed request.
func (s *Service) Send(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return s.post(ctx, session.SubmitText{Text: text})
}

// ToggleVoice starts recording, or stops it and sends the request.
func (s *Service) ToggleVoice(ctx context.Context) error {
	if !s.Capabilities().Voice {
		return contracts.Unsupported(contracts.CapMicrophone)
	}
	return s.post(ctx, session.ToggleTalk{})
}

// Stop cancels the active request, its reply and its speech.
func (s *Service) Stop(ctx context.Context) error { return s.post(ctx, session.Cancel{}) }

// NewChat cancels any active request and starts an empty conversation.
func (s *Service) NewChat(ctx context.Context) error {
	if err := s.post(ctx, session.Cancel{}); err != nil {
		return err
	}
	s.mu.Lock()
	s.flushHeldLocked()
	s.early = nil
	s.ignore = s.ref
	s.title, s.msgs, s.reply, s.pendingQ, s.partial = "", nil, -1, false, ""
	s.publishLocked()
	s.enqueueLocked(func(_ context.Context, conv *string) { *conv = "" })
	s.mu.Unlock()
	return nil
}

// post hands ev to the session; Manager.Post blocks only while its inbox
// is full, which ctx bounds.
func (s *Service) post(ctx context.Context, ev session.Event) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() { errc <- s.cfg.Talk.Post(ev) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func phaseOf(st session.State) Phase {
	switch st {
	case session.StateListening:
		return PhaseListening
	case session.StateTranscribing:
		return PhaseTranscribing
	case session.StateCapturing, session.StateReasoning:
		return PhaseThinking
	case session.StateResponding:
		return PhaseResponding
	case session.StateSpeaking:
		return PhaseSpeaking
	case session.StateAwaitingInput:
		return PhaseAwaiting
	}
	return PhaseIdle
}

// apply folds one session snapshot into the transcript.
func (s *Service) apply(snap session.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap.Ref != s.ref {
		// Terminal snapshots normally close a reply; this covers a
		// generation that vanished without one.
		s.endReplyLocked(func(m *Message) { m.Interrupted = true })
		s.flushHeldLocked()
		s.ref, s.reply, s.pendingQ = snap.Ref, -1, false
	}
	s.phase, s.since = phaseOf(snap.State), snap.Since
	if snap.Ref == s.ignore && s.ignore != (contracts.SessionRef{}) {
		s.partial = ""
		s.publishLocked()
		return
	}
	switch snap.State {
	case session.StateListening, session.StateTranscribing:
		if snap.Text != "" {
			s.partial = snap.Text
		}
	default:
		s.partial = ""
	}
	now := s.cfg.Now()
	if snap.Request != "" {
		m := Message{Role: RoleUser, Text: snap.Request, Via: ViaText, At: now}
		if snap.Mode != session.ModeText {
			m.Via = ViaVoice
			if s.cfg.STTModel != nil {
				m.STTModel = s.cfg.STTModel()
			}
		}
		if s.title == "" {
			s.title = titleOf(m.Text)
		}
		s.flushHeldLocked()
		s.msgs = append(s.msgs, m)
		s.pendingQ = true
		s.held, s.heldRef, s.heldShots = len(s.msgs)-1, snap.Ref, nil
		if e := s.early; e != nil && e.ref == snap.Ref {
			s.attachHeldLocked(e.shots)
			s.flushHeldLocked()
		}
		s.early = nil
	}
	switch snap.State {
	case session.StateResponding, session.StateSpeaking, session.StateCancelled, session.StateInterrupted,
		session.StateFailed, session.StateIdle, session.StateAwaitingInput:
		// The request has passed the model call, so a screenshot would
		// have been reported by now.
		s.flushHeldLocked()
	}
	switch snap.State {
	case session.StateResponding, session.StateSpeaking:
		if snap.Text == "" {
			break
		}
		if s.reply < 0 {
			s.msgs = append(s.msgs, Message{Role: RoleAssistant, At: now, Streaming: true})
			s.reply = len(s.msgs) - 1
		}
		s.msgs[s.reply].Text += snap.Text
	case session.StateCancelled, session.StateInterrupted:
		s.endReplyLocked(func(m *Message) { m.Interrupted = true })
	case session.StateFailed:
		if s.reply < 0 && s.pendingQ {
			// The failure is the answer to the user's turn: show it.
			s.msgs = append(s.msgs, Message{Role: RoleAssistant, At: now, Streaming: true})
			s.reply = len(s.msgs) - 1
		}
		s.endReplyLocked(func(m *Message) { m.Failed, m.Code = true, snap.Reason })
	case session.StateIdle, session.StateAwaitingInput:
		s.endReplyLocked(nil)
	}
	s.publishLocked()
}

// ScreenAttached records which screenshots the request of ref carried. The
// user turn is stored once they are known, so the stored message and its
// image blob are written together.
func (s *Service) ScreenAttached(ref contracts.SessionRef, shots []contracts.ScreenAttachment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || (ref == s.ignore && ref != (contracts.SessionRef{})) {
		return
	}
	if s.held >= 0 && s.heldRef == ref {
		s.attachHeldLocked(shots)
		s.flushHeldLocked()
		s.publishLocked()
		return
	}
	s.early = &early{ref: ref, shots: shots}
}

// attachHeldLocked shows shots on the held user turn. Only the small
// thumbnail stays in the chat; the full picture goes to the store job.
func (s *Service) attachHeldLocked(shots []contracts.ScreenAttachment) {
	screens := make([]Screenshot, 0, len(shots))
	for _, sh := range shots {
		screens = append(screens, Screenshot{Meta: sh.Meta, Thumb: thumbOf(sh.Image)})
	}
	s.msgs[s.held].Screens = screens
	s.heldShots = shots
}

// flushHeldLocked stores the held user turn.
func (s *Service) flushHeldLocked() {
	if s.held < 0 {
		return
	}
	m, shots := s.msgs[s.held], s.heldShots
	s.held, s.heldShots = -1, nil
	s.persistLocked(m, shots)
}

// endReplyLocked closes the reply of the current generation if it is still
// open, and stores it. mark records how it ended.
func (s *Service) endReplyLocked(mark func(*Message)) {
	if s.reply >= 0 && s.msgs[s.reply].Streaming {
		m := &s.msgs[s.reply]
		if mark != nil {
			mark(m)
		}
		m.Streaming = false
		if m.Text != "" {
			s.persistLocked(*m, nil)
		}
	}
	s.reply, s.pendingQ = -1, false
}

func (s *Service) viewLocked() View {
	return View{Title: s.title, Messages: slices.Clone(s.msgs), Phase: s.phase, Partial: s.partial, Since: s.since}
}

func (s *Service) publishLocked() {
	v := s.viewLocked()
	for ch := range s.subs {
		select {
		case <-ch: // drop the unread older view
		default:
		}
		ch <- v
	}
}

func titleOf(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	r := []rune(text)
	if len(r) <= titleRunes {
		return text
	}
	return strings.TrimSpace(string(r[:titleRunes-1])) + "…"
}
