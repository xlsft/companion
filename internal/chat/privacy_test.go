package chat

import (
	"context"
	"image"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/history"
	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/internal/store"
	"github.com/xlsft/companion/pkg/contracts"
)

func newRepo(t *testing.T) (*history.Repo, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dir, "oc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	data := filepath.Join(dir, "data")
	return history.New(db, data), data
}

func shotOf(keep bool) contracts.ScreenAttachment {
	sh := contracts.ScreenAttachment{Meta: contracts.CaptureMetadata{
		ID: "cap1", DisplayID: "d1", Native: contracts.PixelSize{W: 800, H: 400}, Model: contracts.PixelSize{W: 800, H: 400},
		Bounds: contracts.RectXYWH(0, 0, 800, 400), Scale: 1, Timestamp: time.Unix(1700000000, 0),
	}}
	if keep {
		img := image.NewRGBA(image.Rect(0, 0, 800, 400))
		for i := range img.Pix {
			img.Pix[i] = 0xC0
		}
		sh.Image = img
	}
	return sh
}

// files lists every regular file under dir.
func files(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestScreenshotLifecycle(t *testing.T) {
	tests := []struct {
		name       string
		save       bool
		keep       bool
		early      bool // the sink hears about the screenshot before the user turn
		wantBlob   bool
		wantStored bool
	}{
		{name: "not kept: flag only, no pixels on disk", save: true, keep: false, wantStored: true},
		{name: "kept: blob stored", save: true, keep: true, wantBlob: true, wantStored: true},
		{name: "kept, reported early", save: true, keep: true, early: true, wantBlob: true, wantStored: true},
		{name: "transcripts off: nothing stored", save: false, keep: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, data := newRepo(t)
			s, ft, ch := start(t, Config{History: repo, Save: func() bool { return tt.save }})
			shot := []contracts.ScreenAttachment{shotOf(tt.keep)}
			if tt.early {
				s.ScreenAttached(g1, shot)
			}
			ft.snaps <- snap(g1, session.StateReasoning, session.ModeText, "", "what is this?")
			if !tt.early {
				waitFor(t, "user turn", func() bool {
					s.mu.Lock()
					defer s.mu.Unlock()
					return s.held >= 0
				})
				s.ScreenAttached(g1, shot)
			}
			ft.snaps <- snap(g1, session.StateResponding, session.ModeText, "a window", "")
			ft.snaps <- snap(g1, session.StateIdle, session.ModeText, "", "")
			v := settle(t, ch, func(v View) bool { return v.Phase == PhaseIdle && len(v.Messages) == 2 })

			user := v.Messages[0]
			if len(user.Screens) != 1 || user.Screens[0].Meta.ID != "cap1" {
				t.Fatalf("chat view lost the attachment flag: %+v", user.Screens)
			}
			if user.Screens[0].Kept() != tt.keep {
				t.Errorf("thumbnail present = %v, want %v", user.Screens[0].Kept(), tt.keep)
			}
			if len(v.Messages[1].Screens) != 0 {
				t.Error("assistant turn carries a screenshot")
			}

			if !tt.wantStored {
				time.Sleep(20 * time.Millisecond)
				if got := files(data); len(got) != 0 {
					t.Fatalf("files written with saving off: %v", got)
				}
				if l, _ := repo.List(context.Background(), history.Page{Limit: 5}); len(l) != 0 {
					t.Fatalf("conversation stored with saving off")
				}
				return
			}
			var conv *history.Conversation
			waitFor(t, "stored turns", func() bool {
				l, _ := repo.List(context.Background(), history.Page{Limit: 1})
				if len(l) == 0 {
					return false
				}
				c, err := repo.Get(context.Background(), l[0].ID)
				conv = c
				return err == nil && len(c.Messages) == 2
			})
			caps := conv.Messages[0].Captures
			if len(caps) != 1 {
				t.Fatalf("stored captures %+v", caps)
			}
			blobs := files(data)
			if tt.wantBlob != (len(blobs) == 1) || tt.wantBlob != (caps[0].BlobPath != "") {
				t.Fatalf("blobs %v, capture path %q, want blob = %v", blobs, caps[0].BlobPath, tt.wantBlob)
			}

			// Reopening shows the flag, and the picture only if it was kept.
			_, _, ch2 := start(t, Config{History: repo})
			v2 := settle(t, ch2, func(v View) bool { return len(v.Messages) == 2 })
			got := v2.Messages[0].Screens
			if len(got) != 1 || got[0].Kept() != tt.wantBlob {
				t.Fatalf("reloaded screens %+v", got)
			}
			if tt.wantBlob {
				if b := got[0].Thumb.Bounds(); b.Dx() != thumbSide || b.Dy() != thumbSide/2 {
					t.Errorf("thumbnail %v", b)
				}
			}

			// The blob goes with the conversation.
			if err := repo.Delete(context.Background(), conv.ID); err != nil {
				t.Fatal(err)
			}
			if left := files(data); len(left) != 0 {
				t.Errorf("blobs left after deleting the conversation: %v", left)
			}
		})
	}
}

// A screenshot reported for a conversation the user already left must not
// leak into the new chat.
func TestScreenshotAfterNewChatIgnored(t *testing.T) {
	repo, data := newRepo(t)
	s, ft, ch := start(t, Config{History: repo, Save: func() bool { return true }})
	ft.snaps <- snap(g1, session.StateReasoning, session.ModeText, "", "q")
	settle(t, ch, func(v View) bool { return len(v.Messages) == 1 })
	if err := s.NewChat(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.ScreenAttached(g1, []contracts.ScreenAttachment{shotOf(true)})
	v := settle(t, ch, func(v View) bool { return len(v.Messages) == 0 })
	if len(v.Messages) != 0 {
		t.Fatalf("new chat shows %q", describe(v.Messages))
	}
	time.Sleep(20 * time.Millisecond)
	for _, f := range files(data) {
		t.Errorf("late screenshot was stored: %s", f)
	}
}
