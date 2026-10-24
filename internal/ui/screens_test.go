package ui

import (
	"image"
	"testing"

	"gioui.org/io/input"

	"github.com/xlsft/companion/internal/chat"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/theme"
)

// The chat tells that a screenshot was attached; a picture is drawn only
// for a kept one (OC-42).
func TestHomeScreenshotAttachment(t *testing.T) {
	kept := image.NewRGBA(image.Rect(0, 0, 160, 80))
	tests := []struct {
		name       string
		shot       chat.Screenshot
		wantThumbs int
	}{
		{"attached, not kept", chat.Screenshot{}, 0},
		{"kept", chat.Screenshot{Thumb: kept}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			be := newFakeChat(chat.Capabilities{Text: true})
			h, m, p := homeWith(t, be, nil)
			th := theme.New(true, 1)
			r := new(input.Router)
			user := chat.Message{Role: chat.RoleUser, Text: "what is this?", At: at, Screens: []chat.Screenshot{tt.shot}}
			be.views <- chat.View{Messages: []chat.Message{user, {Role: chat.RoleAssistant, Text: "A window."}}}
			waitFor(t, r, h, th, m, "message", func() bool { return len(p.sels) == 2 })
			if len(p.thumbs) != tt.wantThumbs {
				t.Errorf("textures cached = %d, want %d", len(p.thumbs), tt.wantThumbs)
			}
		})
	}
}

func TestScreenshotStringsDistinct(t *testing.T) {
	for _, code := range []i18n.Lang{i18n.EN, i18n.RU} {
		if i18n.T(code, "chat.shot.attached") == i18n.T(code, "chat.shot.kept") {
			t.Errorf("%s: attached and kept look the same", code)
		}
	}
}
