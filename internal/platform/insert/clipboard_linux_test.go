//go:build linux

package insert

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestPickTypes(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"image/png\ntext/plain;charset=utf-8\n", []string{"text/plain;charset=utf-8", "image/png"}},
		{"TARGETS\nTIMESTAMP\nUTF8_STRING\nSTRING\n", []string{"UTF8_STRING", "STRING"}},
		{"image/png\n", []string{"image/png"}},
		{"", nil},
	}
	for _, tt := range tests {
		if got := pickTypes(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("pickTypes(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// fakeTools answers wl-paste/wl-copy invocations from a fake clipboard.
type fakeTools struct {
	types string // "" = nothing owns the clipboard
	data  []byte
	calls []string
	stdin [][]byte
}

func (f *fakeTools) run(_ context.Context, stdin []byte, _ bool, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	f.stdin = append(f.stdin, stdin)
	switch {
	case name == "wl-paste" && f.types == "":
		return nil, errors.New("exit status 1")
	case name == "wl-paste" && args[0] == "--list-types":
		return []byte(f.types), nil
	case name == "wl-paste":
		return f.data, nil
	}
	return nil, nil
}

func TestCmdClipboard(t *testing.T) {
	tests := []struct {
		name    string
		tools   fakeTools
		missing bool
		saved   Clip
		restore string // last call
		saveErr bool
	}{
		{name: "text", tools: fakeTools{types: "text/plain;charset=utf-8\nimage/png\n", data: []byte("old")},
			saved: Clip{Mime: "text/plain;charset=utf-8", Data: []byte("old")}, restore: "wl-copy --type text/plain;charset=utf-8"},
		{name: "empty", tools: fakeTools{}, saved: Clip{Empty: true}, restore: "wl-copy --clear"},
		{name: "too big", tools: fakeTools{types: "image/png", data: bytes.Repeat([]byte{1}, maxClip+1)}, saveErr: true},
		{name: "no tools", missing: true, saveErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &cmdClipboard{wayland: true, run: tt.tools.run, look: func(n string) (string, error) {
				if tt.missing {
					return "", errors.New("not found")
				}
				return "/usr/bin/" + n, nil
			}}
			ctx := context.Background()
			saved, err := c.Save(ctx)
			if tt.saveErr {
				if err == nil {
					t.Fatal("expected Save to refuse")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if saved.Mime != tt.saved.Mime || !bytes.Equal(saved.Data, tt.saved.Data) || saved.Empty != tt.saved.Empty {
				t.Errorf("saved %+v, want %+v", saved, tt.saved)
			}
			if err := c.SetText(ctx, "secret words"); err != nil {
				t.Fatal(err)
			}
			if err := c.Restore(ctx, saved); err != nil {
				t.Fatal(err)
			}
			if last := tt.tools.calls[len(tt.tools.calls)-1]; last != tt.restore {
				t.Errorf("restore ran %q, want %q", last, tt.restore)
			}
			// The dictated text goes through stdin only.
			for i, call := range tt.tools.calls {
				if strings.Contains(call, "secret") {
					t.Errorf("text in arguments: %q", call)
				}
				if strings.HasPrefix(call, "wl-copy --type text/plain") && i < len(tt.tools.calls)-1 && string(tt.tools.stdin[i]) != "secret words" {
					t.Errorf("SetText stdin = %q", tt.tools.stdin[i])
				}
			}
		})
	}
}
