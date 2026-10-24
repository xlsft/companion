//go:build linux && cgo

// Command mainui opens the main window with a scripted chat so the Home
// page can be looked at and screenshotted without a configured assistant
// (OC-71). It never talks to a model: the conversation is canned, Send only
// appends the typed turn, and Stop ends the canned reply.
//
//	mainui -theme dark -lang ru -state chat   # chat | empty | offline
//	mainui -route settings -section voice
//	                                     # route: home | settings
//	                                     # section: general | voice | screen | privacy | diagnostics
//
// Diagnostics (Settings › Diagnostics) shows the real capability report of
// this machine.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	gioapp "gioui.org/app"

	"github.com/xlsft/companion/internal/app"
	"github.com/xlsft/companion/internal/chat"
	"github.com/xlsft/companion/internal/platform/appearance"
	"github.com/xlsft/companion/internal/platform/detect"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/store"
	"github.com/xlsft/companion/internal/ui"
	"github.com/xlsft/companion/pkg/contracts"
)

func main() {
	themeMode := flag.String("theme", "auto", "auto (follow the system), dark or light")
	lang := flag.String("lang", "ru", "interface language (en, ru, de, …)")
	state := flag.String("state", "chat", "chat, empty or offline")
	route := flag.String("route", "home", "page to open: home or settings")
	section := flag.String("section", "general", "settings section: general, voice, screen, privacy, diagnostics")
	flag.Parse()
	routes := map[string]ui.Route{"home": ui.RouteHome, "settings": ui.RouteSettings}
	start, ok := routes[*route]
	if !ok {
		fmt.Fprintln(os.Stderr, "mainui: unknown route", *route)
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	scheme, err := appearance.Choose(*themeMode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mainui:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	_ = scheme.Start(ctx) // Unsupported keeps the light theme

	dir, err := os.MkdirTemp("", "mainui-spike")
	if err != nil {
		log.Error("temp dir", "err", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	db, err := store.Open(ctx, filepath.Join(dir, "spike.db"))
	if err != nil {
		log.Error("open store", "err", err)
		os.Exit(1)
	}
	svc, err := settings.NewService(ctx, store.NewKV(db))
	if err != nil {
		log.Error("settings", "err", err)
		os.Exit(1)
	}
	if err := svc.Update(ctx, func(s *settings.Settings) { s.UI.Language = *lang }); err != nil {
		log.Error("settings", "err", err)
		os.Exit(1)
	}

	var be ui.ChatBackend
	switch *state {
	case "chat":
		be = newDemo(demoView())
	case "empty":
		be = newDemo(chat.View{})
	}
	diagnose := func(ctx context.Context) (contracts.CapabilityReport, error) { return detect.New().Detect(ctx) }
	shell := ui.New(ui.Options{Mode: ui.ModeMain, Settings: svc, Chat: be, Diagnose: diagnose, Quit: stop, Log: log, Appearance: scheme, Version: "spike"})
	go func() {
		// The main window exists once the app has started the shell.
		time.Sleep(300 * time.Millisecond)
		shell.Navigate(start)
		if start == ui.RouteSettings && !shell.ShowSettings(*section) {
			fmt.Fprintln(os.Stderr, "mainui: unknown section", *section)
		}
	}()
	go func() {
		if err := app.New(log, shell).Run(ctx); err != nil {
			log.Error("spike exited with error", "err", err)
		}
		stop()
		db.Close()
		os.RemoveAll(dir)
		os.Exit(0)
	}()
	gioapp.Main()
}

// demo is a ChatBackend over a fixed conversation.
type demo struct {
	mu   sync.Mutex
	view chat.View
	subs []chan chat.View
}

func newDemo(v chat.View) *demo { return &demo{view: v} }

func (d *demo) Capabilities() chat.Capabilities {
	return chat.Capabilities{Text: true, Voice: true, Hotkey: "Mod+Space", Model: "claude-sonnet-5-5", Speech: "ElevenLabs · Алиса"}
}

func (d *demo) Subscribe() (<-chan chat.View, func()) {
	ch := make(chan chat.View, 1)
	d.mu.Lock()
	ch <- d.view
	d.subs = append(d.subs, ch)
	d.mu.Unlock()
	return ch, func() {}
}

func (d *demo) update(fn func(*chat.View)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := d.view
	v.Messages = append([]chat.Message(nil), v.Messages...)
	fn(&v)
	d.view = v
	for _, ch := range d.subs {
		select {
		case <-ch:
		default:
		}
		ch <- v
	}
	return nil
}

func (d *demo) Send(_ context.Context, text string) error {
	return d.update(func(v *chat.View) {
		v.Messages = append(v.Messages, chat.Message{Role: chat.RoleUser, Text: text, Via: chat.ViaText, At: time.Now()})
	})
}

func (d *demo) ToggleVoice(context.Context) error {
	return d.update(func(v *chat.View) {
		if v.Phase == chat.PhaseListening {
			v.Phase, v.Partial = chat.PhaseIdle, ""
			return
		}
		v.Phase, v.Partial = chat.PhaseListening, "Как сделать"
	})
}

func (d *demo) Stop(context.Context) error {
	return d.update(func(v *chat.View) {
		for i := range v.Messages {
			if v.Messages[i].Streaming {
				v.Messages[i].Streaming, v.Messages[i].Interrupted = false, true
			}
		}
		v.Phase, v.Partial = chat.PhaseIdle, ""
	})
}

func (d *demo) NewChat(context.Context) error {
	return d.update(func(v *chat.View) { *v = chat.View{} })
}

func demoView() chat.View {
	at := time.Now().Add(-2 * time.Minute)
	return chat.View{
		Title: "Прозрачный фон в Figma",
		Phase: chat.PhaseSpeaking,
		Messages: []chat.Message{
			{Role: chat.RoleUser, Text: "Как сделать фон у этого фрейма прозрачным?", Via: chat.ViaVoice, STTModel: "whisper medium", At: at},
			{Role: chat.RoleAssistant, Text: "Выделите фрейм и в правой панели в блоке Fill уберите заливку кнопкой «−». Я обвёл этот блок на экране."},
			{Role: chat.RoleUser, Text: "А чтобы при экспорте в PNG тоже было прозрачно?", At: at.Add(time.Minute)},
			{Role: chat.RoleAssistant, Text: "При экспорте оставьте формат PNG — пустая заливка сохранится как прозрачность. Внизу правой панели в блоке Export", Streaming: true},
		},
	}
}
