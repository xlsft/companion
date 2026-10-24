package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xlsft/openclick/internal/stt/engine/fake"
	"github.com/xlsft/openclick/internal/stt/protocol"
)

type harness struct {
	t      *testing.T
	enc    *protocol.Encoder
	in     *io.PipeWriter
	msgs   chan *protocol.Message
	done   chan error
	nextID uint64
}

func newHarness(t *testing.T, opts fake.Options) *harness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := New(Config{
		EngineName:      "fake",
		Factory:         fake.Factory(opts),
		PartialInterval: 300 * time.Millisecond,
	})
	h := &harness{t: t, enc: protocol.NewEncoder(inW), in: inW, msgs: make(chan *protocol.Message, 1024), done: make(chan error, 1)}
	go func() {
		h.done <- srv.Serve(context.Background(), inR, outW)
		outW.Close()
	}()
	go func() {
		dec := protocol.NewDecoder(outR)
		for {
			m, err := dec.Decode()
			if err != nil {
				close(h.msgs)
				return
			}
			h.msgs <- m
		}
	}()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	return h
}

func (h *harness) send(m *protocol.Message) {
	h.t.Helper()
	if err := h.enc.Encode(m); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) request(method string, params any) json.RawMessage {
	h.t.Helper()
	h.nextID++
	id := protocol.IntID(h.nextID)
	m, err := protocol.NewRequest(id, method, params)
	if err != nil {
		h.t.Fatal(err)
	}
	h.send(m)
	return id
}

func (h *harness) notify(method string, params any) {
	h.t.Helper()
	m, err := protocol.NewNotification(method, params)
	if err != nil {
		h.t.Fatal(err)
	}
	h.send(m)
}

// await returns the response for id, collecting notifications seen before it.
func (h *harness) await(id json.RawMessage) (*protocol.Message, []*protocol.Message) {
	h.t.Helper()
	var notes []*protocol.Message
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-h.msgs:
			if !ok {
				h.t.Fatal("stream closed")
			}
			if m.IsNotification() {
				notes = append(notes, m)
				continue
			}
			if string(m.ID) == string(id) {
				return m, notes
			}
			h.t.Fatalf("unexpected response %s", m.ID)
		case <-timeout:
			h.t.Fatalf("no response for %s", id)
		}
	}
}

func (h *harness) call(method string, params, result any) error {
	h.t.Helper()
	resp, _ := h.await(h.request(method, params))
	if resp.Error != nil {
		return resp.Error
	}
	if err := resp.DecodeResult(result); err != nil {
		h.t.Fatal(err)
	}
	return nil
}

func (h *harness) init() {
	h.t.Helper()
	if err := h.call(protocol.MethodInitialize, protocol.InitializeParams{Language: "auto", Backend: "cpu"}, nil); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) startSession(id string, partials bool) {
	h.t.Helper()
	if err := h.call(protocol.MethodSessionStart, protocol.SessionStartParams{
		SessionID: id, SampleRate: 16000, Format: protocol.FormatPCMS16LE, Partials: partials,
	}, nil); err != nil {
		h.t.Fatal(err)
	}
}

// stream sends audio in 100 ms chunks.
func (h *harness) stream(id string, pcm []int16) { h.streamPaced(id, pcm, 0) }

// streamPaced sleeps between chunks so rolling partials get scheduled
// deterministically instead of all audio landing during one decode.
func (h *harness) streamPaced(id string, pcm []int16, pace time.Duration) {
	h.t.Helper()
	for seq, i := uint64(0), 0; i < len(pcm); seq, i = seq+1, i+1600 {
		time.Sleep(pace)
		h.notify(protocol.MethodSessionAudio, protocol.SessionAudioParams{
			SessionID: id, Seq: seq, Data: protocol.EncodePCM16(pcm[i:min(i+1600, len(pcm))]),
		})
	}
}

func tone(d time.Duration) []int16 {
	out := make([]int16, int(d.Seconds()*16000))
	for i := range out {
		out[i] = int16(8000 * math.Sin(2*math.Pi*220*float64(i)/16000))
	}
	return out
}

func byMethod(notes []*protocol.Message, method string) []*protocol.Message {
	var out []*protocol.Message
	for _, n := range notes {
		if n.Method == method {
			out = append(out, n)
		}
	}
	return out
}

func TestInitialize(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "model.bin")
	if err := os.WriteFile(model, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	const abcSHA = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	tests := []struct {
		name   string
		params protocol.InitializeParams
		want   error
	}{
		{"missing model", protocol.InitializeParams{ModelPath: filepath.Join(dir, "nope")}, protocol.ErrModelNotFound},
		{"checksum mismatch", protocol.InitializeParams{ModelPath: model, ModelSHA256: "00"}, protocol.ErrModelChecksum},
		{"bad backend", protocol.InitializeParams{Backend: "tpu"}, protocol.ErrInvalidParams},
		{"ok with checksum", protocol.InitializeParams{ModelPath: model, ModelSHA256: abcSHA, Backend: "auto"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, fake.Options{})
			var res protocol.InitializeResult
			err := h.call(protocol.MethodInitialize, tt.params, &res)
			if !errors.Is(err, tt.want) && !(err == nil && tt.want == nil) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			if tt.want != nil {
				return
			}
			if res.ProtocolVersion != protocol.Version || res.Engine != "fake" || res.BackendInUse != "cpu" || len(res.Languages) == 0 {
				t.Fatalf("result %+v", res)
			}
			if err := h.call(protocol.MethodInitialize, tt.params, nil); !errors.Is(err, protocol.ErrAlreadyInitialized) {
				t.Fatalf("second initialize: %v", err)
			}
		})
	}
}

func TestSessionStartValidation(t *testing.T) {
	h := newHarness(t, fake.Options{})
	ok := protocol.SessionStartParams{SessionID: "s", SampleRate: 16000, Format: protocol.FormatPCMS16LE}
	if err := h.call(protocol.MethodSessionStart, ok, nil); !errors.Is(err, protocol.ErrNotInitialized) {
		t.Fatalf("before init: %v", err)
	}
	h.init()
	bad := ok
	bad.SampleRate = 44100
	tests := []struct {
		name   string
		params protocol.SessionStartParams
		want   error
	}{
		{"wrong rate", bad, protocol.ErrAudioFormat},
		{"missing id", protocol.SessionStartParams{SampleRate: 16000, Format: protocol.FormatPCMS16LE}, protocol.ErrInvalidParams},
		{"ok", ok, nil},
		{"duplicate", ok, protocol.ErrSessionExists},
	}
	for _, tt := range tests {
		if err := h.call(protocol.MethodSessionStart, tt.params, nil); !errors.Is(err, tt.want) && !(err == nil && tt.want == nil) {
			t.Errorf("%s: got %v, want %v", tt.name, err, tt.want)
		}
	}
}

func TestSessionFlowWithPartials(t *testing.T) {
	h := newHarness(t, fake.Options{})
	h.init()
	h.startSession("s1", true)
	pcm := append(make([]int16, 8000), tone(3*time.Second)...)
	h.streamPaced("s1", pcm, 5*time.Millisecond)
	resp, notes := h.await(h.request(protocol.MethodSessionFinish, protocol.SessionRef{SessionID: "s1"}))
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	var final protocol.FinalResult
	if err := resp.DecodeResult(&final); err != nil {
		t.Fatal(err)
	}
	if want := fake.Text(false, len(pcm)); final.Text != want || !final.Speech || final.DurationMS != 3500 || final.Language != "en" || len(final.Segments) != 1 {
		t.Fatalf("final %+v, want text %q", final, want)
	}

	vads := byMethod(notes, protocol.MethodSessionVAD)
	if len(vads) == 0 {
		t.Fatal("no vad events")
	}
	var v protocol.VADParams
	_ = vads[0].DecodeParams(&v)
	if !v.Speech || v.AtMS != 500 {
		t.Fatalf("vad %+v", v)
	}
	partials := byMethod(notes, protocol.MethodSessionPartial)
	if len(partials) < 2 {
		t.Fatalf("want rolling partials, got %d", len(partials))
	}
	var last uint64
	for _, n := range partials {
		var p protocol.PartialParams
		_ = n.DecodeParams(&p)
		if p.SessionID != "s1" || p.Stable || p.Seq <= last {
			t.Fatalf("partial %+v", p)
		}
		last = p.Seq
	}
	if err := h.call(protocol.MethodSessionFinish, protocol.SessionRef{SessionID: "s1"}, nil); !errors.Is(err, protocol.ErrSessionUnknown) {
		t.Fatalf("finished session must be forgotten: %v", err)
	}
}

func TestSilenceSkipsDecode(t *testing.T) {
	h := newHarness(t, fake.Options{})
	h.init()
	h.startSession("s", true)
	h.stream("s", make([]int16, 32000))
	resp, notes := h.await(h.request(protocol.MethodSessionFinish, protocol.SessionRef{SessionID: "s"}))
	var final protocol.FinalResult
	if err := resp.DecodeResult(&final); err != nil || resp.Error != nil {
		t.Fatal(err, resp.Error)
	}
	if final.Speech || final.Text != "" || final.DurationMS != 2000 {
		t.Fatalf("final %+v", final)
	}
	if n := len(byMethod(notes, protocol.MethodSessionPartial)); n != 0 {
		t.Fatalf("partials on silence: %d", n)
	}
}

func TestCancelDuringFinish(t *testing.T) {
	h := newHarness(t, fake.Options{Delay: 2 * time.Second})
	h.init()
	h.startSession("s", false)
	h.stream("s", tone(time.Second))
	finishID := h.request(protocol.MethodSessionFinish, protocol.SessionRef{SessionID: "s"})

	var health protocol.HealthResult
	if err := h.call(protocol.MethodHealth, nil, &health); err != nil || !health.OK || health.Sessions != 1 {
		t.Fatalf("health during decode: %+v %v", health, err)
	}
	t0 := time.Now()
	if err := h.call(protocol.MethodSessionCancel, protocol.SessionRef{SessionID: "s"}, nil); err != nil {
		t.Fatal(err)
	}
	resp, _ := h.await(finishID)
	if !errors.Is(resp.Error, protocol.ErrSessionCancelled) {
		t.Fatalf("finish after cancel: %+v", resp)
	}
	if time.Since(t0) > time.Second {
		t.Fatal("cancel did not abort the decode")
	}
	if err := h.call(protocol.MethodSessionCancel, protocol.SessionRef{SessionID: "s"}, nil); !errors.Is(err, protocol.ErrSessionUnknown) {
		t.Fatalf("second cancel: %v", err)
	}
	// Audio after cancel is dropped, not an error.
	h.stream("s", tone(100*time.Millisecond))
	_, notes := h.await(h.request(protocol.MethodHealth, nil))
	for _, n := range notes {
		if n.Method != protocol.MethodLog {
			t.Fatalf("unexpected %s after cancel", n.Method)
		}
	}
}

func TestAudioSequence(t *testing.T) {
	h := newHarness(t, fake.Options{})
	h.init()
	h.startSession("s", false)
	chunk := protocol.EncodePCM16(tone(100 * time.Millisecond))
	for _, seq := range []uint64{0, 1, 1, 3} {
		h.notify(protocol.MethodSessionAudio, protocol.SessionAudioParams{SessionID: "s", Seq: seq, Data: chunk})
	}
	resp, notes := h.await(h.request(protocol.MethodSessionFinish, protocol.SessionRef{SessionID: "s"}))
	var final protocol.FinalResult
	_ = resp.DecodeResult(&final)
	if final.DurationMS != 300 {
		t.Fatalf("duplicate chunk must be dropped, duration %d", final.DurationMS)
	}
	var codes []string
	for _, n := range byMethod(notes, protocol.MethodLog) {
		var l protocol.LogParams
		_ = n.DecodeParams(&l)
		codes = append(codes, l.Code)
	}
	if len(codes) != 2 || codes[0] != "audio_seq_out_of_order" || codes[1] != "audio_seq_gap" {
		t.Fatalf("log codes %v", codes)
	}
}

func TestProtocolErrorsAndShutdown(t *testing.T) {
	h := newHarness(t, fake.Options{})
	if _, err := h.in.Write([]byte("garbage\n")); err != nil {
		t.Fatal(err)
	}
	m := <-h.msgs
	if !errors.Is(m.Error, protocol.ErrParse) || string(m.ID) != "null" {
		t.Fatalf("parse error response %+v", m)
	}
	if err := h.call("nope", nil, nil); !errors.Is(err, protocol.ErrMethodNotFound) {
		t.Fatalf("unknown method: %v", err)
	}
	h.init()
	h.startSession("s", false)
	if err := h.call(protocol.MethodShutdown, nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
		h.done <- nil // for cleanup
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after shutdown")
	}
}
