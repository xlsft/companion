package ui

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/xlsft/companion/internal/chat"
)

// ChatBackend is what the Home chat needs; *chat.Service implements it
// over the Talk session and the history. Without one (Options.Chat nil)
// Home shows that the chat is not connected and sends nothing.
type ChatBackend interface {
	// Capabilities says which controls work now; the others are disabled.
	Capabilities() chat.Capabilities
	// Subscribe delivers the current conversation and every change to it;
	// the returned function ends the subscription.
	Subscribe() (<-chan chat.View, func())
	Send(ctx context.Context, text string) error
	// ToggleVoice starts recording, or stops it and sends the request.
	ToggleVoice(ctx context.Context) error
	// Stop cancels the active request, its reply and its speech.
	Stop(ctx context.Context) error
	// NewChat stops any request and starts an empty conversation.
	NewChat(ctx context.Context) error
}

var _ ChatBackend = (*chat.Service)(nil)

const chatOpTimeout = 5 * time.Second

// chatModel follows the backend off the UI goroutine. It is shared by the
// Home page and the sidebar status, and lives as long as the main UI.
type chatModel struct {
	be         ChatBackend
	log        *slog.Logger
	invalidate func()

	mu      sync.Mutex
	started bool
	closed  bool
	unsub   func()
	view    chat.View
	// failKey is the catalogue key of the last failed action, "" if none.
	failKey string
}

func newChatModel(be ChatBackend, log *slog.Logger, invalidate func()) *chatModel {
	return &chatModel{be: be, log: log, invalidate: invalidate}
}

// ensure subscribes on first use, so a main UI that is never shown costs
// nothing.
func (c *chatModel) ensure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.be == nil || c.started || c.closed {
		return
	}
	c.started = true
	ch, unsub := c.be.Subscribe()
	c.unsub = unsub
	go func() {
		for v := range ch {
			c.mu.Lock()
			c.view = v
			c.mu.Unlock()
			c.invalidate()
		}
	}()
}

func (c *chatModel) snapshot() (chat.View, chat.Capabilities, string) {
	var caps chat.Capabilities
	if c.be != nil {
		caps = c.be.Capabilities()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.view, caps, c.failKey
}

// run performs a backend action off the UI goroutine. Errors are logged by
// code only and shown as failKey; the text the user typed is never logged.
func (c *chatModel) run(op, failKey string, fn func(context.Context, ChatBackend) error) {
	if c.be == nil {
		return
	}
	c.mu.Lock()
	c.failKey = ""
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), chatOpTimeout)
		defer cancel()
		if err := fn(ctx, c.be); err != nil {
			c.log.Warn("chat action failed", "op", op, "err", err)
			c.mu.Lock()
			c.failKey = failKey
			c.mu.Unlock()
			c.invalidate()
		}
	}()
}

func (c *chatModel) send(text string) {
	c.run("send", "chat.error.send", func(ctx context.Context, b ChatBackend) error { return b.Send(ctx, text) })
}

func (c *chatModel) toggleVoice() {
	c.run("voice", "chat.error.voice", func(ctx context.Context, b ChatBackend) error { return b.ToggleVoice(ctx) })
}

func (c *chatModel) stop() {
	c.run("stop", "chat.error.stop", func(ctx context.Context, b ChatBackend) error { return b.Stop(ctx) })
}

func (c *chatModel) newChat() {
	c.run("new_chat", "chat.error.newChat", func(ctx context.Context, b ChatBackend) error { return b.NewChat(ctx) })
}

func (c *chatModel) close() {
	c.mu.Lock()
	c.closed = true
	unsub := c.unsub
	c.unsub = nil
	c.mu.Unlock()
	if unsub != nil {
		unsub()
	}
}
