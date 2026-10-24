package chat

import (
	"context"
	"image"

	"github.com/xlsft/companion/internal/history"
	"github.com/xlsft/companion/internal/memory"
	"github.com/xlsft/companion/pkg/contracts"
)

// job runs on the persist goroutine, which owns the stored conversation id
// (empty: the next turn creates a conversation).
type job func(ctx context.Context, conv *string)

// load shows the most recent stored conversation, so reopening the app
// continues where the user left off, and returns its id.
func (s *Service) load(ctx context.Context) string {
	if s.cfg.History == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	list, err := s.cfg.History.List(ctx, history.Page{Limit: 1})
	if err != nil {
		s.cfg.Log.Warn("chat: list history failed", "err", err)
		return ""
	}
	if len(list) == 0 {
		return ""
	}
	conv, err := s.cfg.History.Get(ctx, list[0].ID)
	if err != nil {
		s.cfg.Log.Warn("chat: load conversation failed", "err", err)
		return ""
	}
	var msgs []Message
	for _, hm := range conv.Messages {
		m := Message{Text: hm.Text, At: hm.CreatedAt}
		for _, c := range hm.Captures {
			m.Screens = append(m.Screens, Screenshot{Meta: metaOf(c), Thumb: thumbFromFile(c.BlobPath)})
		}
		switch hm.Role {
		case history.RoleUser:
			m.Role = RoleUser
			if hm.Mode == history.ModeTalk {
				m.Via = ViaVoice
				if hm.ProviderID == providerSTT {
					m.STTModel = hm.Model
				}
			}
		case history.RoleAssistant:
			m.Role = RoleAssistant
		default:
			continue // tool and system turns are not part of the chat
		}
		msgs = append(msgs, m)
	}
	if s.cfg.Context != nil {
		s.cfg.Context.Seed(turnsOf(msgs))
	}
	s.mu.Lock()
	s.title, s.msgs = conv.Title, msgs
	s.publishLocked()
	s.mu.Unlock()
	return conv.ID
}

// turnsOf pairs each user message with the assistant reply after it. A
// reply that failed or was cut off is left out: the model should not be
// shown half an answer as its own.
func turnsOf(msgs []Message) []memory.Turn {
	var out []memory.Turn
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			out = append(out, memory.Turn{User: m.Text})
		case RoleAssistant:
			if n := len(out); n > 0 && out[n-1].Assistant == "" && !m.Failed && !m.Interrupted {
				out[n-1].Assistant = m.Text
			}
		}
	}
	return out
}

// persistLocked queues m for the history store when saving is allowed.
// The write happens off the session loop so a slow disk never delays the
// transcript.
//
// shots are the screenshots of a user turn: their metadata is stored with
// the message, and pixels only where the capture carries an image, which the
// pipeline hands over solely when the user opted in to keeping screenshots.
func (s *Service) persistLocked(m Message, shots []contracts.ScreenAttachment) {
	if s.cfg.History == nil || s.cfg.Save == nil || !s.cfg.Save() {
		return
	}
	title := s.title
	hm := history.Message{SessionID: s.ref.ID, Generation: s.ref.Generation, Text: m.Text, CreatedAt: m.At, Mode: history.ModeText}
	switch m.Role {
	case RoleUser:
		hm.Role = history.RoleUser
		if m.Via == ViaVoice {
			hm.Mode = history.ModeTalk
			if m.STTModel != "" {
				hm.ProviderID, hm.Model = providerSTT, m.STTModel
			}
		}
	case RoleAssistant:
		hm.Role = history.RoleAssistant
	}
	var metas []contracts.CaptureMetadata
	imgs := map[contracts.CaptureID]*image.RGBA{}
	for _, sh := range shots {
		metas = append(metas, sh.Meta)
		if sh.Image != nil {
			imgs[sh.Meta.ID] = sh.Image
		}
	}
	s.enqueueLocked(func(ctx context.Context, conv *string) {
		ctx, cancel := context.WithTimeout(ctx, opTimeout)
		defer cancel()
		if *conv == "" {
			c, err := s.cfg.History.CreateConversation(ctx, title, "")
			if err != nil {
				s.cfg.Log.Warn("chat: create conversation failed", "err", err)
				return
			}
			*conv = c.ID
		}
		if len(imgs) > 0 {
			if err := s.cfg.History.SetKeepScreenshots(ctx, *conv, true); err != nil {
				s.cfg.Log.Warn("chat: enable screenshots failed", "err", err)
				imgs = nil // no opt-in on the conversation: store no pixels
			}
		}
		if _, err := s.cfg.History.AppendMessage(ctx, *conv, hm, metas, imgs); err != nil {
			s.cfg.Log.Warn("chat: store message failed", "err", err)
		}
	})
}

// enqueueLocked never blocks the caller: a full queue drops the job, the
// on-screen chat stays intact.
func (s *Service) enqueueLocked(j job) {
	if !s.started || s.closed {
		return
	}
	select {
	case s.jobs <- j:
	default:
		s.cfg.Log.Warn("chat: history queue full, turn not stored")
	}
}

func (s *Service) persistLoop(ctx context.Context, conv string) {
	for {
		select {
		case j := <-s.jobs:
			j(ctx, &conv)
		case <-ctx.Done():
			return
		}
	}
}
