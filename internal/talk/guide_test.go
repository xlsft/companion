package talk

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/overlay"
	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/internal/walkthrough"
	"github.com/xlsft/companion/pkg/contracts"
)

type fakeGuide struct {
	mu      sync.Mutex
	view    walkthrough.View
	started []walkthrough.Plan
	capture contracts.CaptureID
	cmds    []walkthrough.Command
	// controls are the model's decisions and at the steps they were made on.
	controls []walkthrough.Control
	at       []int
	revised  []walkthrough.Revision
	hushes   int
	stops    int
	// order records Start relative to speech.
	order *[]string
}

func (g *fakeGuide) View() walkthrough.View { g.mu.Lock(); defer g.mu.Unlock(); return g.view }
func (g *fakeGuide) Start(_ context.Context, p walkthrough.Plan, c contracts.CaptureID) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.started, g.capture = append(g.started, p), c
	if g.order != nil {
		*g.order = append(*g.order, "start")
	}
	return nil
}
func (g *fakeGuide) Do(_ context.Context, c walkthrough.Command) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cmds = append(g.cmds, c)
	return nil
}
func (g *fakeGuide) Control(_ context.Context, c walkthrough.Control, at int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.controls, g.at = append(g.controls, c), append(g.at, at)
	if g.order != nil {
		*g.order = append(*g.order, "control:"+string(c.Action))
	}
	return nil
}
func (g *fakeGuide) Revise(_ context.Context, r walkthrough.Revision) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.revised = append(g.revised, r)
	return nil
}
func (g *fakeGuide) Hush()                { g.mu.Lock(); g.hushes++; g.mu.Unlock() }
func (g *fakeGuide) Stop(context.Context) { g.mu.Lock(); g.stops++; g.mu.Unlock() }

// orderSpeaker records speech into a shared order.
type orderSpeaker struct{ order *[]string }

func (s orderSpeaker) Say(_ context.Context, text string, started func()) error {
	started()
	*s.order = append(*s.order, "say:"+text)
	return nil
}

const threeSteps = `{"goal":"Export as PNG","steps":[
 {"title":"Select the frame","say":"Click the frame.","shapes":[{"kind":"ring","points":[[0.3,0.4]]}]},
 {"title":"Open Export","say":"Open the Export panel."},
 {"title":"Export","say":"Press Export."}]}`

func TestReasonStartsWalkthrough(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		started bool
	}{
		{"valid plan", threeSteps, true},
		{"invalid plan goes back to the model", `{"goal":"x","steps":[]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &scriptedProvider{rounds: [][]llm.Event{
				{llm.TextDelta{Text: "Let's do it step by step."}, llm.ToolCallCompleted{ID: "w1", Name: walkthrough.ToolStart, Arguments: json.RawMessage(tt.args)}, llm.Completed{}},
				{llm.TextDelta{Text: "Sorry."}, llm.Completed{}},
			}}
			var order []string
			g := &fakeGuide{order: &order}
			o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil },
				Overlay: &fakeOverlay{}, Guide: g, Speaker: orderSpeaker{&order}}
			_, err := o.Reason(context.Background(), ref, session.Request{Text: "how do I export?", Captures: []*contracts.Capture{screenCapture()}}, (&events{}).emit)
			if err != nil {
				t.Fatal(err)
			}
			var offered bool
			for _, tl := range p.reqs[0].Tools {
				offered = offered || tl.Name == walkthrough.ToolStart
			}
			if !offered {
				t.Fatal("start_walkthrough not offered")
			}
			if got := len(g.started) == 1; got != tt.started {
				t.Fatalf("started %v, want %v", g.started, tt.started)
			}
			if !tt.started {
				if len(p.reqs) != 2 {
					t.Fatalf("model rounds %d, want the error round too", len(p.reqs))
				}
				return
			}
			if len(p.reqs) != 1 {
				t.Errorf("model asked %d times; the steps speak for themselves after the plan", len(p.reqs))
			}
			if g.capture != "cap1" || len(g.started[0].Steps) != 3 {
				t.Errorf("started %+v on %q", g.started[0], g.capture)
			}
			if strings.Join(order, ",") != "say:Let's do it step by step.,start" {
				t.Errorf("order %v: the walkthrough must start after the answer was spoken", order)
			}
		})
	}
}

func TestReasonDuringWalkthrough(t *testing.T) {
	call := func(name, args string) llm.Event {
		return llm.ToolCallCompleted{ID: "c-" + name, Name: name, Arguments: json.RawMessage(args)}
	}
	tests := []struct {
		name    string
		text    string
		active  bool
		rounds  [][]llm.Event
		control *walkthrough.Control
		revised int
		// asked is the number of model rounds; order the speech and the
		// walkthrough move.
		asked int
		order string
	}{
		{"what's next moves on after the answer", "готово, что дальше?", true, [][]llm.Event{
			{llm.TextDelta{Text: "Отлично."}, call(walkthrough.ToolControl, `{"action":"next"}`), llm.Completed{}},
		}, &walkthrough.Control{Action: walkthrough.CmdNext}, 0, 1, "say:Отлично.,control:next"},
		{"already done jumps", "я это уже сделал", true, [][]llm.Event{
			{call(walkthrough.ToolControl, `{"action":"goto","step":2,"reason":"exported"}`), llm.Completed{}},
		}, &walkthrough.Control{Action: walkthrough.CmdGoto, Step: 2, Reason: "exported"}, 0, 1, "control:goto"},
		{"go back", "вернись к прошлому", true, [][]llm.Event{
			{call(walkthrough.ToolControl, `{"action":"previous"}`), llm.Completed{}},
		}, &walkthrough.Control{Action: walkthrough.CmdPrev}, 0, 1, "control:previous"},
		{"a question is just answered", "а где кнопка экспорта?", true, [][]llm.Event{
			{llm.TextDelta{Text: "Справа вверху."}, llm.Completed{}},
		}, nil, 0, 1, "say:Справа вверху."},
		{"an invalid move goes back to the model", "перейди к девятому", true, [][]llm.Event{
			{call(walkthrough.ToolControl, `{"action":"goto","step":9}`), llm.Completed{}},
			{llm.TextDelta{Text: "Там всего два шага."}, llm.Completed{}},
		}, nil, 0, 2, "say:Там всего два шага."},
		{"revise and keep answering", "тут нет такой кнопки", true, [][]llm.Event{
			{call(walkthrough.ToolRevise, `{"title":"Open the menu","say":"Open the menu first."}`), llm.Completed{}},
			{llm.TextDelta{Text: "Сначала меню."}, llm.Completed{}},
		}, nil, 1, 2, "say:Сначала меню."},
		{"no walkthrough, no control", "дальше", false, [][]llm.Event{
			{call(walkthrough.ToolControl, `{"action":"next"}`), llm.Completed{}},
			{llm.TextDelta{Text: "Что дальше?"}, llm.Completed{}},
		}, nil, 0, 2, "say:Что дальше?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &scriptedProvider{rounds: tt.rounds}
			var order []string
			g := &fakeGuide{order: &order}
			if tt.active {
				g.view = walkthrough.View{Status: walkthrough.StatusRunning, Goal: "Export", Index: 1, Total: 2,
					Title: "Open Export", Text: "Open it.", Steps: []string{"Select", "Open Export"}, Done: []bool{true, false}}
			}
			o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil }, Guide: g, Speaker: orderSpeaker{&order}}
			shot := screenCapture()
			if _, err := o.Reason(context.Background(), ref, session.Request{Text: tt.text, Captures: []*contracts.Capture{shot}}, (&events{}).emit); err != nil {
				t.Fatal(err)
			}
			if shot.Image != nil {
				t.Error("capture not released")
			}
			if len(p.reqs) != tt.asked || g.hushes != 1 {
				t.Fatalf("model rounds %d, want %d; hushes %d", len(p.reqs), tt.asked, g.hushes)
			}
			if len(g.cmds) != 0 {
				t.Errorf("voice drove the walkthrough without the model: %v", g.cmds)
			}
			switch {
			case tt.control == nil && len(g.controls) != 0:
				t.Errorf("moved %+v", g.controls)
			case tt.control != nil && (len(g.controls) != 1 || g.controls[0] != *tt.control || g.at[0] != 1):
				t.Errorf("controls %+v at %v, want %+v at step 1", g.controls, g.at, *tt.control)
			}
			if len(g.revised) != tt.revised {
				t.Errorf("revisions %+v, want %d", g.revised, tt.revised)
			}
			if got := strings.Join(order, ","); got != tt.order {
				t.Errorf("order %q, want %q", got, tt.order)
			}
			offered := map[string]bool{}
			for _, tl := range p.reqs[0].Tools {
				offered[tl.Name] = true
			}
			if offered[walkthrough.ToolControl] != tt.active || offered[walkthrough.ToolRevise] != tt.active || !offered[walkthrough.ToolStart] {
				t.Errorf("tools offered %v, walkthrough active %v", offered, tt.active)
			}
			sys := p.reqs[0].Messages[0].Content[0].Text
			if tt.active != strings.Contains(sys, `Walkthrough in progress (running). Goal: "Export". Steps: 1. "Select" [done]; 2. "Open Export" [current];`) {
				t.Errorf("system prompt %q; walkthrough active %v", sys, tt.active)
			}
		})
	}
}

type fakeCapturer struct{ n int }

func (f *fakeCapturer) Capture(context.Context, contracts.CaptureRequest) ([]*contracts.Capture, error) {
	f.n++
	c := screenCapture()
	c.Meta.ID = contracts.CaptureID("fresh")
	return []*contracts.Capture{c}, nil
}

func TestPresenterShow(t *testing.T) {
	stepRef := contracts.SessionRef{ID: "walkthrough-w", Generation: 2}
	step := walkthrough.Step{Title: "Open Export", Say: "Open the Export panel.", Shapes: json.RawMessage(`[{"kind":"ring","points":[[0.3,0.4]]}]`)}
	tests := []struct {
		name     string
		capture  contracts.CaptureID
		rounds   [][]llm.Event
		captures int
		drawnOn  contracts.CaptureID
		draws    int
		title    string
	}{
		{"plan screenshot draws the planned shapes", "plan", nil, 0, "plan", 1, "Open Export"},
		{"later showing re-marks on a new screenshot", "", [][]llm.Event{
			{llm.ToolCallCompleted{ID: "p", Name: overlay.ToolPointAt, Arguments: json.RawMessage(`{"x":0.6,"y":0.2}`)}, llm.Completed{}},
		}, 1, "fresh", 2, "Open Export"},
		{"model revises the step", "", [][]llm.Event{
			{llm.ToolCallCompleted{ID: "r", Name: walkthrough.ToolRevise, Arguments: json.RawMessage(`{"title":"Open the menu","say":"Open the menu first."}`)}, llm.Completed{}},
		}, 1, "", 0, "Open the menu"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &scriptedProvider{rounds: tt.rounds}
			ov, capt := &fakeOverlay{}, &fakeCapturer{}
			att := &recordingSink{}
			o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil }, Overlay: ov, Capturer: capt, Attachments: att}
			pr := &Presenter{Orch: o}
			if tt.capture != "" {
				// The plan's screenshot, as Reason prepared it.
				o.rememberMeta(contracts.CaptureMetadata{ID: "plan", Native: contracts.PixelSize{W: 1600, H: 1000},
					Bounds: contracts.RectXYWH(0, 0, 1600, 1000), Scale: 1})
			}
			shown, err := pr.Show(context.Background(), walkthrough.Presentation{Ref: stepRef, Goal: "Export", Index: 1, Total: 3, Step: step, Capture: tt.capture})
			if err != nil {
				t.Fatal(err)
			}
			got := shown.Step
			if (shown.Area != contracts.Rect{}) != (tt.draws > 0) {
				t.Errorf("area %+v with %d hints drawn", shown.Area, tt.draws)
			}
			if got.Title != tt.title {
				t.Errorf("step title %q, want %q", got.Title, tt.title)
			}
			if capt.n != tt.captures {
				t.Errorf("captures %d, want %d", capt.n, tt.captures)
			}
			if len(ov.cmds) != tt.draws {
				t.Fatalf("drew %d, want %d: %+v", len(ov.cmds), tt.draws, ov.cmds)
			}
			for _, c := range ov.cmds {
				if c.Session != stepRef || c.Capture != tt.drawnOn {
					t.Errorf("drew %+v, want session %v on %q", c, stepRef, tt.drawnOn)
				}
			}
			if att.n != 0 {
				t.Error("a step screenshot was attached to the chat")
			}
			if tt.capture == "" {
				msgs := p.reqs[0].Messages
				if user := msgs[len(msgs)-1]; !strings.Contains(user.Content[0].Text, "step 2 of 3") || user.Content[1].Kind != llm.PartImage {
					t.Errorf("step request %+v", user.Content)
				}
			}
		})
	}
}

type recordingSink struct{ n int }

func (r *recordingSink) ScreenAttached(contracts.SessionRef, []contracts.ScreenAttachment) { r.n++ }

func TestPresenterCheck(t *testing.T) {
	stepRef := contracts.SessionRef{ID: "walkthrough-w", Generation: 5}
	pr0 := walkthrough.Presentation{Ref: stepRef, Goal: "Export", Index: 0, Total: 3,
		Step: walkthrough.Step{Title: "Select", Say: "Click the frame."}, Brief: "Walkthrough in progress (running). Current step 1 of 3"}
	call := func(name, args string) llm.Event {
		return llm.ToolCallCompleted{ID: "c-" + name, Name: name, Arguments: json.RawMessage(args)}
	}
	tests := []struct {
		name    string
		rounds  [][]llm.Event
		control *walkthrough.Control
		title   string
		area    contracts.Rect
	}{
		{"done", [][]llm.Event{{call(walkthrough.ToolControl, `{"action":"next","reason":"selected"}`), llm.Completed{}}},
			&walkthrough.Control{Action: walkthrough.CmdNext, Reason: "selected"}, "", contracts.Rect{}},
		{"not yet", [][]llm.Event{{llm.Completed{}}}, nil, "", contracts.Rect{}},
		{"elsewhere: marked anew", [][]llm.Event{
			{call(overlay.ToolPointAt, `{"x":0.5,"y":0.5,"radius":0.05}`), llm.Completed{}},
			{llm.Completed{}},
		}, nil, "", contracts.RectXYWH(960-96, 540-96, 192, 192)},
		{"revised", [][]llm.Event{
			{call(walkthrough.ToolRevise, `{"title":"Select the layer","say":"Click the layer."}`), llm.Completed{}},
			{llm.Completed{}},
		}, nil, "Select the layer", contracts.Rect{}},
		{"bad control goes back", [][]llm.Event{
			{call(walkthrough.ToolControl, `{"action":"goto"}`), llm.Completed{}},
			{llm.Completed{}},
		}, nil, "", contracts.Rect{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &scriptedProvider{rounds: tt.rounds}
			ov, capt := &fakeOverlay{}, &fakeCapturer{}
			o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil }, Overlay: ov, Capturer: capt}
			v, err := (&Presenter{Orch: o}).Check(context.Background(), pr0)
			if err != nil {
				t.Fatal(err)
			}
			if capt.n != 1 {
				t.Errorf("captures %d, want a fresh screenshot", capt.n)
			}
			if (v.Control == nil) != (tt.control == nil) || (v.Control != nil && *v.Control != *tt.control) {
				t.Errorf("control %+v, want %+v", v.Control, tt.control)
			}
			if v.Step.Title != tt.title {
				t.Errorf("revised %+v, want %q", v.Step, tt.title)
			}
			if !near(v.Area, tt.area) {
				t.Errorf("area %+v, want %+v", v.Area, tt.area)
			}
			req := p.reqs[0]
			if sys := req.Messages[0].Content[0].Text; !strings.Contains(sys, "has just changed") {
				t.Errorf("system prompt %q", sys)
			}
			if user := req.Messages[1]; user.Content[0].Text != pr0.Brief || user.Content[1].Kind != llm.PartImage {
				t.Errorf("check request %+v", user.Content)
			}
			names := map[string]bool{}
			for _, tl := range req.Tools {
				names[tl.Name] = true
			}
			if !names[walkthrough.ToolControl] || !names[walkthrough.ToolRevise] || !names[overlay.ToolPointAt] || names[walkthrough.ToolStart] {
				t.Errorf("tools %v", names)
			}
			for _, c := range ov.cmds {
				if c.Session != stepRef {
					t.Errorf("drew for %v", c.Session)
				}
			}
		})
	}
}

func near(a, b contracts.Rect) bool {
	d := func(x, y float64) bool { return x-y < 1e-6 && y-x < 1e-6 }
	return d(a.Min.X, b.Min.X) && d(a.Min.Y, b.Min.Y) && d(a.Max.X, b.Max.X) && d(a.Max.Y, b.Max.Y)
}

func TestHintArea(t *testing.T) {
	meta := contracts.CaptureMetadata{ID: "c", Native: contracts.PixelSize{W: 2000, H: 1000},
		Bounds: contracts.RectXYWH(100, 0, 1000, 500), Scale: 2}
	show := func(k overlay.Kind, r float64, pts ...contracts.Point) overlay.Command {
		return overlay.Command{Op: overlay.OpShow, Kind: k, Radius: r, Points: pts}
	}
	tests := []struct {
		name string
		meta contracts.CaptureMetadata
		cmds []overlay.Command
		want contracts.Rect
	}{
		{"nothing", meta, nil, contracts.Rect{}},
		{"point", meta, []overlay.Command{show(overlay.KindPoint, 0, contracts.Point{X: 0.5, Y: 0.5})}, contracts.RectXYWH(600, 250, 0, 0)},
		{"ring is round on a wide screen", meta, []overlay.Command{show(overlay.KindRing, 0.1, contracts.Point{X: 0.5, Y: 0.5})},
			contracts.RectXYWH(500, 150, 200, 200)},
		{"brackets, label aside", meta, []overlay.Command{
			show(overlay.KindBrackets, 0, contracts.Point{X: 0.1, Y: 0.2}, contracts.Point{X: 0.3, Y: 0.4}),
			show(overlay.KindLabel, 0, contracts.Point{X: 0.9, Y: 0.9}),
		}, contracts.RectXYWH(200, 100, 200, 100)},
		{"clamped to the capture", meta, []overlay.Command{show(overlay.KindRing, 0.2, contracts.Point{X: 0, Y: 0})}, contracts.RectXYWH(100, 0, 200, 200)},
		{"unknown capture", contracts.CaptureMetadata{}, []overlay.Command{show(overlay.KindPoint, 0, contracts.Point{X: 0.5, Y: 0.5})}, contracts.Rect{}},
	}
	for _, tt := range tests {
		if got := hintArea(tt.meta, tt.cmds); !near(got, tt.want) {
			t.Errorf("%s: %+v, want %+v", tt.name, got, tt.want)
		}
	}
}
