package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCodecRoundTrip(t *testing.T) {
	audio := SessionAudioParams{SessionID: "s1", Seq: 3, Data: EncodePCM16([]int16{0, 1, -1, 32767, -32768})}
	req, err := NewRequest(IntID(7), MethodSessionFinish, SessionRef{SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	note, err := NewNotification(MethodSessionAudio, audio)
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewResult(IntID(7), FinalResult{SessionID: "s1", Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	errResp := NewErrorResponse(nil, ErrParse)

	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	for _, m := range []*Message{req, note, res, errResp} {
		if err := enc.Encode(m); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(buf.String(), "\n"); n != 4 {
		t.Fatalf("want 4 lines, got %d", n)
	}

	dec := NewDecoder(&buf)
	tests := []struct {
		name               string
		req, notif, respon bool
		method             string
	}{
		{"request", true, false, false, MethodSessionFinish},
		{"notification", false, true, false, MethodSessionAudio},
		{"result", false, false, true, ""},
		{"error", false, false, true, ""},
	}
	var got []*Message
	for _, tt := range tests {
		m, err := dec.Decode()
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if m.IsRequest() != tt.req || m.IsNotification() != tt.notif || m.IsResponse() != tt.respon || m.Method != tt.method {
			t.Errorf("%s: wrong kind %+v", tt.name, m)
		}
		got = append(got, m)
	}
	if _, err := dec.Decode(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}

	if id, ok := ParseIntID(got[0].ID); !ok || id != 7 {
		t.Errorf("id = %s", got[0].ID)
	}
	var a SessionAudioParams
	if err := got[1].DecodeParams(&a); err != nil {
		t.Fatal(err)
	}
	if want := []int16{0, 1, -1, 32767, -32768}; !equalPCM(DecodePCM16(a.Data), want) || a.Seq != 3 {
		t.Errorf("audio = %+v", a)
	}
	if !errors.Is(got[3].Error, ErrParse) || string(got[3].ID) != "null" {
		t.Errorf("error response = %+v", got[3])
	}
}

func TestDecoderSurvivesMalformedLines(t *testing.T) {
	in := strings.Join([]string{
		`not json`,
		``,
		`{"jsonrpc":"1.0","id":1,"method":"health"}`,
		`{"jsonrpc":"2.0"}`,
		`{"jsonrpc":"2.0","id":"abc","method":"health"}`,
	}, "\n")
	dec := NewDecoder(strings.NewReader(in))
	want := []error{ErrParse, ErrInvalidRequest, ErrInvalidRequest, nil}
	for i, w := range want {
		m, err := dec.Decode()
		if w == nil {
			if err != nil || m.Method != MethodHealth || string(m.ID) != `"abc"` {
				t.Fatalf("%d: %v %+v", i, err, m)
			}
			continue
		}
		if !errors.Is(err, w) || IsTerminal(err) {
			t.Fatalf("%d: want %v, got %v", i, w, err)
		}
	}
	if _, err := dec.Decode(); !IsTerminal(err) {
		t.Fatalf("want terminal EOF, got %v", err)
	}
}

func TestDecoderLineTooLongIsTerminal(t *testing.T) {
	dec := NewDecoder(strings.NewReader(strings.Repeat("a", MaxLineBytes+1) + "\n"))
	if _, err := dec.Decode(); !errors.Is(err, ErrParse) || !IsTerminal(err) {
		t.Fatalf("got %v", err)
	}
}

func TestErrorIsMatchesByCode(t *testing.T) {
	var wire Error
	if err := json.Unmarshal([]byte(`{"code":-32004,"message":"whatever"}`), &wire); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(&wire, ErrSessionUnknown) || errors.Is(&wire, ErrSessionCancelled) {
		t.Fatal("code matching broken")
	}
	if d := ErrModelNotFound.WithDetail("stat"); d.Data != "stat" || ErrModelNotFound.Data != "" {
		t.Fatal("WithDetail must copy")
	}
}

func TestFloat32(t *testing.T) {
	f := Float32([]int16{0, -32768, 16384})
	if f[0] != 0 || f[1] != -1 || f[2] != 0.5 {
		t.Fatalf("got %v", f)
	}
}

func equalPCM(a, b []int16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
