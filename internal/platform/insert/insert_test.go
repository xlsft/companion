package insert

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// fakeTyper records what it was asked to do.
type fakeTyper struct {
	name     string
	ready    error
	typeErr  error
	faithful bool
	log      *[]string
}

func (f *fakeTyper) Name() string { return f.name }
func (f *fakeTyper) Ready(context.Context) error {
	*f.log = append(*f.log, f.name+".ready")
	return f.ready
}
func (f *fakeTyper) Type(_ context.Context, text string) error {
	*f.log = append(*f.log, f.name+".type:"+text)
	return f.typeErr
}
func (f *fakeTyper) Chord(_ context.Context, h contracts.Hotkey) error {
	*f.log = append(*f.log, f.name+".chord:"+h.Key)
	return nil
}
func (f *fakeTyper) Faithful(string) bool { return f.faithful }
func (f *fakeTyper) Close() error         { return nil }

type fakeClip struct {
	saveErr error
	cur     Clip
	log     *[]string
}

func (c *fakeClip) Save(context.Context) (Clip, error) {
	*c.log = append(*c.log, "clip.save")
	return c.cur, c.saveErr
}
func (c *fakeClip) SetText(_ context.Context, text string) error {
	*c.log = append(*c.log, "clip.set:"+text)
	return nil
}
func (c *fakeClip) Restore(ctx context.Context, cl Clip) error {
	*c.log = append(*c.log, "clip.restore:"+cl.Mime)
	return ctx.Err()
}

func TestInserterChain(t *testing.T) {
	unsupported := contracts.Unsupported(contracts.CapTextInsertion)
	denied := contracts.PermissionDenied(contracts.CapTextInsertion)
	tests := []struct {
		name    string
		a, b    fakeTyper
		clip    *fakeClip
		useClip bool
		want    []string
		err     error // matched with errors.Is
		code    string
	}{
		{name: "first ready types",
			a: fakeTyper{faithful: true}, b: fakeTyper{faithful: true},
			want: []string{"a.ready", "a.type:hi"}},
		{name: "unsupported falls through",
			a: fakeTyper{ready: unsupported}, b: fakeTyper{faithful: true},
			want: []string{"a.ready", "b.ready", "b.type:hi"}},
		{name: "declined consent falls through",
			a: fakeTyper{ready: denied}, b: fakeTyper{faithful: true},
			want: []string{"a.ready", "b.ready", "b.type:hi"}},
		{name: "nothing works",
			a: fakeTyper{ready: unsupported}, b: fakeTyper{ready: unsupported},
			want: []string{"a.ready", "b.ready"}, err: contracts.ErrUnsupported},
		{name: "declined beats unsupported",
			a: fakeTyper{ready: denied}, b: fakeTyper{ready: unsupported},
			want: []string{"a.ready", "b.ready"}, err: contracts.ErrPermissionDenied},
		{name: "a typing error is not retried elsewhere",
			a: fakeTyper{typeErr: errors.New("broken pipe")}, b: fakeTyper{},
			want: []string{"a.ready", "a.type:hi"}, code: "broken pipe"},
		{name: "unfaithful without opt-in types anyway",
			a: fakeTyper{}, clip: &fakeClip{}, useClip: false,
			want: []string{"a.ready", "a.type:hi"}},
		{name: "unfaithful with opt-in pastes and restores",
			a: fakeTyper{}, clip: &fakeClip{cur: Clip{Mime: "image/png"}}, useClip: true,
			want: []string{"a.ready", "clip.save", "clip.set:hi", "a.chord:v", "clip.restore:image/png"}},
		{name: "faithful with opt-in still types",
			a: fakeTyper{faithful: true}, clip: &fakeClip{}, useClip: true,
			want: []string{"a.ready", "a.type:hi"}},
		{name: "no clipboard tool: typed instead",
			a: fakeTyper{}, clip: &fakeClip{saveErr: unsupported}, useClip: true,
			want: []string{"a.ready", "clip.save", "a.type:hi"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var log []string
			tt.a.name, tt.a.log = "a", &log
			tt.b.name, tt.b.log = "b", &log
			in := &Inserter{Typers: []Typer{&tt.a, &tt.b}, Settle: time.Millisecond, UseClipboard: func() bool { return tt.useClip }}
			if tt.clip != nil {
				tt.clip.log = &log
				in.Clipboard = tt.clip
			}
			err := in.InsertText(context.Background(), "hi")
			switch {
			case tt.err != nil && !errors.Is(err, tt.err):
				t.Errorf("err = %v, want %v", err, tt.err)
			case tt.code != "" && (err == nil || !strings.Contains(err.Error(), tt.code)):
				t.Errorf("err = %v, want %q", err, tt.code)
			case tt.err == nil && tt.code == "" && err != nil:
				t.Errorf("err = %v", err)
			}
			if !slices.Equal(log, tt.want) {
				t.Errorf("calls %v, want %v", log, tt.want)
			}
		})
	}
}

// The clipboard is put back even when the insertion is cancelled after
// the paste chord.
func TestPasteRestoresOnCancel(t *testing.T) {
	var log []string
	ctx, cancel := context.WithCancel(context.Background())
	a := &fakeTyper{name: "a", log: &log}
	in := &Inserter{Typers: []Typer{a}, Clipboard: &fakeClip{cur: Clip{Mime: "text/plain"}, log: &log},
		UseClipboard: func() bool { return true }, Settle: time.Hour}
	time.AfterFunc(20*time.Millisecond, cancel)
	_ = in.InsertText(ctx, "x")
	if !slices.Contains(log, "clip.restore:text/plain") {
		t.Errorf("not restored: %v", log)
	}
}

func TestEmptyTextTouchesNothing(t *testing.T) {
	var log []string
	in := &Inserter{Typers: []Typer{&fakeTyper{name: "a", log: &log}}}
	if err := in.InsertText(context.Background(), ""); err != nil || len(log) != 0 {
		t.Errorf("err %v, calls %v", err, log)
	}
	if err := (&Inserter{}).InsertText(context.Background(), "x"); !errors.Is(err, contracts.ErrUnsupported) {
		t.Errorf("no backends: %v", err)
	}
}

func TestKeysym(t *testing.T) {
	tests := []struct {
		r    rune
		want uint32
	}{
		{'a', 0x61}, {' ', 0x20}, {'~', 0x7e}, {'ü', 0xfc}, {'\n', 0xff0d}, {'\t', 0xff09},
		{'П', 0x0100041f}, {'你', 0x01004f60}, {'😀', 0x0101f600},
		{'\r', 0}, {0x7f, 0}, {0x85, 0},
	}
	for _, tt := range tests {
		if got := Keysym(tt.r); got != tt.want {
			t.Errorf("Keysym(%q) = %#x, want %#x", tt.r, got, tt.want)
		}
	}
	if got := Keysyms("a\rb"); !slices.Equal(got, []uint32{'a', 'b'}) {
		t.Errorf("Keysyms skips control characters: %v", got)
	}
}

func TestKeymap(t *testing.T) {
	km := keymap([]uint32{'a', 0x0100041f, symReturn, symControlL})
	for _, want := range []string{
		"maximum = 13;", "<K1> = 9;", "<K4> = 12;",
		"key <K1> {[ U0061 ]};", "key <K2> {[ U041F ]};", "key <K3> {[ Return ]};",
		"key <K4> {[ Control_L ]};", "modifier_map Control { <K4> };",
		`xkb_types "companion" { include "complete" };`,
	} {
		if !strings.Contains(km, want) {
			t.Errorf("keymap lacks %q:\n%s", want, km)
		}
	}
	if strings.Count(km, "{")-strings.Count(km, "}") != 0 {
		t.Errorf("unbalanced braces:\n%s", km)
	}
}

func TestChunks(t *testing.T) {
	syms := Keysyms("abcabd")
	got := chunks(syms, 3)
	if len(got) != 2 || string(runes(got[0])) != "abcab" || string(runes(got[1])) != "d" {
		t.Errorf("chunks = %v", got)
	}
	if got := chunks(syms, 10); len(got) != 1 {
		t.Errorf("one chunk expected: %v", got)
	}
	if got := distinct(syms); string(runes(got)) != "abcd" {
		t.Errorf("distinct = %v", got)
	}
}

func runes(s []uint32) []rune {
	out := make([]rune, len(s))
	for i, v := range s {
		out[i] = rune(v)
	}
	return out
}

func TestASCIIOnly(t *testing.T) {
	for in, want := range map[string]bool{"Hello, world!\n\t": true, "привет": false, "café": false, "": true} {
		if got := asciiOnly(in); got != want {
			t.Errorf("asciiOnly(%q) = %v", in, got)
		}
	}
}
