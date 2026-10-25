package tts

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hajimehoshi/go-mp3"
)

// edge_tone.mp3: half a second of 440 Hz in Edge's output format, made with
// ffmpeg -f lavfi -i sine=frequency=440:sample_rate=24000:duration=0.5
// -ac 1 -b:a 48k -c:a libmp3lame -write_xing 0 -id3v2_version 0.
//
//go:embed testdata/edge_tone.mp3
var edgeTone []byte

func TestEdgeGEC(t *testing.T) {
	// Expected values were produced by edge-tts 7.3.1 itself (the PyPI
	// wheel's edge_tts/drm.py, generate_sec_ms_gec_v2(unix_timestamp)),
	// run with the aiohttp import stubbed out.
	tests := []struct {
		unix float64
		want string
	}{
		{1700000000, "2CD23A2CA0B2595A-F3C846C370E05F9CC83ACC5F995C5F5D87A4D0F14CE8936A0F902E64099E38A2"},
		{1700000299.9, "0919602B5750900A-EC9DD394F23CB5635CF62A0E8E24136DCD32B26D10C543B10082E482D7516397"},
		{1767225600, "723A6EBC411B23BB-214991EA6B1E401F3D777A699DDDEE75E29740EA7A5DCD1007366FA1090DD267"},
		{0, "52BC7A17B50DFEC9-28C4394775C8751CD2F6859C030C06DB1489ACAB3B3A7651831FC26895177ED6"},
	}
	for _, tt := range tests {
		ts := time.Unix(0, int64(tt.unix*1e9))
		if got := edgeGEC(ts); got != tt.want {
			t.Errorf("edgeGEC(%v) = %s, want %s", tt.unix, got, tt.want)
		}
	}
	// Same five-minute window, same token.
	if edgeGEC(time.Unix(1700000000, 0)) != edgeGEC(time.Unix(1700000099, 0)) {
		t.Error("token changed inside its window")
	}
}

func TestEdgeRate(t *testing.T) {
	tests := []struct {
		speed float64
		want  string
	}{
		{MinSpeed, "-50%"}, {0.75, "-25%"}, {DefaultSpeed, "+0%"}, {1.25, "+25%"}, {MaxSpeed, "+50%"}, {1.104, "+10%"},
	}
	for _, tt := range tests {
		if got := edgeRate(tt.speed); got != tt.want {
			t.Errorf("edgeRate(%v) = %q, want %q", tt.speed, got, tt.want)
		}
	}
}

func TestEdgeCleanText(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"a < b & c > d", "a &lt; b &amp; c &gt; d"},
		{`say "hi" it's`, `say "hi" it's`}, // quotes are legal in element text
		{"&amp;", "&amp;amp;"},
		{"tab\there\vvt\x00nul\nnl", "tab\there vt nul\nnl"},
		{"</prosody><evil/>", "&lt;/prosody&gt;&lt;evil/&gt;"},
	}
	for _, tt := range tests {
		if got := edgeCleanText(tt.in); got != tt.want {
			t.Errorf("edgeCleanText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEdgeSSML(t *testing.T) {
	got := edgeSSML("en-US-AriaNeural", "+10%", edgeCleanText(`1 < 2 & "x"`))
	want := "<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='en-US'>" +
		"<voice name='Microsoft Server Speech Text to Speech Voice (en-US, AriaNeural)'>" +
		"<prosody pitch='+0Hz' rate='+10%' volume='+0%'>1 &lt; 2 &amp; \"x\"</prosody></voice></speak>"
	if got != want {
		t.Errorf("ssml =\n%s\nwant\n%s", got, want)
	}
	if got := edgeSSML("a'b", "+0%", "x"); !strings.Contains(got, "name='a&apos;b'") {
		t.Errorf("voice attribute not escaped: %s", got)
	}
}

func TestEdgeSSMLVoice(t *testing.T) {
	tests := []struct{ in, want string }{
		{"en-US-AriaNeural", "Microsoft Server Speech Text to Speech Voice (en-US, AriaNeural)"},
		{"fil-PH-AngeloNeural", "Microsoft Server Speech Text to Speech Voice (fil-PH, AngeloNeural)"},
		{"zh-CN-liaoning-XiaobeiNeural", "Microsoft Server Speech Text to Speech Voice (zh-CN-liaoning, XiaobeiNeural)"},
		{"Microsoft Server Speech Text to Speech Voice (cy-GB, NiaNeural)", "Microsoft Server Speech Text to Speech Voice (cy-GB, NiaNeural)"},
	}
	for _, tt := range tests {
		if got := edgeSSMLVoice(tt.in); got != tt.want {
			t.Errorf("edgeSSMLVoice(%q) = %q", tt.in, got)
		}
	}
}

func TestEdgeSplit(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		limit int
		want  []string
	}{
		{"short", "hello world", 20, []string{"hello world"}},
		{"space", "aaa bbb ccc", 8, []string{"aaa bbb", "ccc"}},
		{"newline first", "aa bb\ncc dd", 9, []string{"aa bb", "cc dd"}},
		{"no space", "abcdefgh", 3, []string{"abc", "def", "gh"}},
		{"utf8", "жжжж", 3, []string{"ж", "ж", "ж", "ж"}},
		{"entity", "ab&amp;cd", 5, []string{"ab", "&amp;", "cd"}},
		{"blank", "   ", 10, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := edgeSplit(tt.in, tt.limit)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") || len(got) != len(tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			for _, p := range got {
				if len(p) > tt.limit {
					t.Errorf("piece %q over limit", p)
				}
			}
		})
	}
}

func edgeBinary(headers string, data []byte) []byte {
	b := binary.BigEndian.AppendUint16(nil, uint16(len(headers)))
	return append(append(b, headers...), data...)
}

const edgeAudioHeaders = "X-RequestId:abc\r\nContent-Type:audio/mpeg\r\nX-StreamId:1\r\nPath:audio\r\n"

func TestEdgeAudioFrame(t *testing.T) {
	tests := []struct {
		name    string
		msg     []byte
		want    string
		wantErr bool
	}{
		{"audio", edgeBinary(edgeAudioHeaders, []byte("MP3")), "MP3", false},
		{"empty keepalive", edgeBinary("X-RequestId:abc\r\nPath:audio\r\n", nil), "", false},
		{"data without type", edgeBinary("Path:audio\r\n", []byte("x")), "", true},
		{"wrong type", edgeBinary("Content-Type:audio/wav\r\nPath:audio\r\n", []byte("x")), "", true},
		{"wrong path", edgeBinary("Content-Type:audio/mpeg\r\nPath:video\r\n", []byte("x")), "", true},
		{"short", []byte{0}, "", true},
		{"length beyond", []byte{0x10, 0, 'a'}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := edgeAudioFrame(tt.msg)
			if (err != nil) != tt.wantErr || string(got) != tt.want {
				t.Errorf("got %q, %v", got, err)
			}
		})
	}
}

func TestParseEdgeVoices(t *testing.T) {
	body := `[
	 {"Name":"Microsoft Server Speech Text to Speech Voice (en-US, AriaNeural)","ShortName":"en-US-AriaNeural","Gender":"Female","Locale":"en-US","FriendlyName":"Microsoft Aria Online (Natural) - English (United States)","Status":"GA"},
	 {"ShortName":"de-DE-OldNeural","Locale":"de-DE","FriendlyName":"Microsoft Old Online (Natural) - German (Germany)","Status":"Deprecated"},
	 {"ShortName":"ru-RU-SvetlanaNeural","Locale":"ru-RU","Status":"GA"}
	]`
	got, err := parseEdgeVoices(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []Voice{
		{ID: "en-US-AriaNeural", Name: "Aria - English (United States)", Languages: []string{"en-US"}},
		{ID: "ru-RU-SvetlanaNeural", Name: "ru-RU-SvetlanaNeural", Languages: []string{"ru-RU"}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Name != want[i].Name || strings.Join(got[i].Languages, ",") != strings.Join(want[i].Languages, ",") {
			t.Errorf("voice %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if _, err := parseEdgeVoices(strings.NewReader("<html>")); err == nil {
		t.Error("garbage parsed")
	}
}

func TestEdgeDefaultVoice(t *testing.T) {
	for _, lang := range []string{"en", "ru", "es", "pt", "fr", "de", "it", "pl", "tr", "uk", "zh", "ja", "ko"} {
		v := EdgeDefaultVoice(lang)
		if !strings.HasPrefix(v, lang+"-") || !strings.HasSuffix(v, "Neural") {
			t.Errorf("%s: %q", lang, v)
		}
	}
	tests := []struct{ in, want string }{
		{"pt-BR", "pt-BR-FranciscaNeural"}, {"DE", "de-DE-KatjaNeural"}, {"auto", edgeFallbackVoice}, {"", edgeFallbackVoice}, {"nl", edgeFallbackVoice},
	}
	for _, tt := range tests {
		if got := EdgeDefaultVoice(tt.in); got != tt.want {
			t.Errorf("EdgeDefaultVoice(%q) = %q", tt.in, got)
		}
	}
}

// edgeFake speaks the Read aloud framing. serve runs per connection after
// the client's two text messages arrived; reject may refuse the handshake.
type edgeFake struct {
	srv    *httptest.Server
	serve  func(ctx context.Context, c *websocket.Conn, n int)
	reject func(w http.ResponseWriter, n int) bool
	voices func(w http.ResponseWriter, r *http.Request, n int)

	mu    sync.Mutex
	conns int
	lists int
	urls  []*url.URL
	hdrs  []http.Header
	msgs  [][]string
}

var edgeNow = time.Unix(1700000000, 0)

func newEdgeFake(t *testing.T, f *edgeFake) *Edge {
	t.Helper()
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.urls = append(f.urls, r.URL)
		f.hdrs = append(f.hdrs, r.Header.Clone())
		f.mu.Unlock()
		switch r.URL.Path {
		case "/voices/list":
			f.mu.Lock()
			n := f.lists
			f.lists++
			f.mu.Unlock()
			f.voices(w, r, n)
			return
		case "/edge/v1":
		default:
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		n := f.conns
		f.conns++
		f.mu.Unlock()
		if f.reject != nil && f.reject(w, n) {
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) // Origin is the extension
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := context.Background()
		var got []string
		for i := 0; i < 2; i++ {
			_, m, err := c.Read(ctx)
			if err != nil {
				return
			}
			got = append(got, string(m))
		}
		f.mu.Lock()
		f.msgs = append(f.msgs, got)
		f.mu.Unlock()
		f.serve(ctx, c, n)
	}))
	t.Cleanup(f.srv.Close)
	return NewEdge(EdgeConfig{Config: Config{BaseURL: f.srv.URL, Timeout: 2 * time.Second}, Now: func() time.Time { return edgeNow }})
}

func edgeText(path string) []byte {
	return []byte("X-RequestId:abc\r\nContent-Type:application/json; charset=utf-8\r\nPath:" + path + "\r\n\r\n{}")
}

// sendTone sends the tone in pieces that ignore MP3 frame boundaries.
func sendTone(ctx context.Context, c *websocket.Conn, data []byte, end bool) error {
	c.Write(ctx, websocket.MessageText, edgeText("turn.start"))
	for len(data) > 0 {
		n := min(len(data), 700)
		if err := c.Write(ctx, websocket.MessageBinary, edgeBinary(edgeAudioHeaders, data[:n])); err != nil {
			return err
		}
		data = data[n:]
	}
	if end {
		return c.Write(ctx, websocket.MessageText, edgeText("turn.end"))
	}
	return nil
}

func decodedToneMono(t *testing.T) int {
	d, err := mp3.NewDecoder(bytes.NewReader(edgeTone))
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, d)
	if err != nil {
		t.Fatal(err)
	}
	return int(n) / 2
}

func drainEdge(t *testing.T, s Stream) (int, error) {
	t.Helper()
	total := 0
	for {
		ch, err := s.Next(context.Background())
		if err != nil {
			if err == io.EOF {
				return total, nil
			}
			return total, err
		}
		if ch.Format != PCM24kMono && ch.Format != MP3 {
			t.Fatalf("format %v", ch.Format)
		}
		total += len(ch.Data)
	}
}

func edgeReq(text string) SynthesisRequest {
	return SynthesisRequest{Text: text, VoiceID: "en-US-AriaNeural", Speed: 1.2}
}

func TestEdgeSynthesize(t *testing.T) {
	f := &edgeFake{serve: func(ctx context.Context, c *websocket.Conn, n int) {
		c.Write(ctx, websocket.MessageText, edgeText("response"))
		c.Write(ctx, websocket.MessageText, edgeText("audio.metadata"))
		c.Write(ctx, websocket.MessageBinary, edgeBinary("X-RequestId:abc\r\nPath:audio\r\n", nil))
		sendTone(ctx, c, edgeTone, true)
	}}
	p := newEdgeFake(t, f)
	s, err := p.SynthesizeStream(context.Background(), edgeReq(`TOPSECRET <b> & "q"`))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	total, err := drainEdge(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if want := decodedToneMono(t); total != want || total == 0 {
		t.Errorf("pcm bytes = %d, want %d", total, want)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.urls[0].Query()
	if q.Get("TrustedClientToken") != edgeTrustedToken || q.Get("Sec-MS-GEC") != edgeGEC(edgeNow) ||
		q.Get("Sec-MS-GEC-Version") != edgeGECVersion || len(q.Get("ConnectionId")) != 32 {
		t.Errorf("query = %v", q)
	}
	h := f.hdrs[0]
	if h.Get("Origin") != "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold" || !strings.Contains(h.Get("User-Agent"), "Edg/143") ||
		!strings.HasPrefix(h.Get("Cookie"), "muid=") {
		t.Errorf("headers = %v", h)
	}
	cfg, ssml := f.msgs[0][0], f.msgs[0][1]
	if !strings.Contains(cfg, "Path:speech.config\r\n\r\n") || !strings.Contains(cfg, `"outputFormat":"audio-24khz-48kbitrate-mono-mp3"`) {
		t.Errorf("config = %q", cfg)
	}
	for _, want := range []string{"Path:ssml\r\n\r\n<speak", "rate='+20%'", "(en-US, AriaNeural)", `TOPSECRET &lt;b&gt; &amp; "q"`, "Content-Type:application/ssml+xml"} {
		if !strings.Contains(ssml, want) {
			t.Errorf("ssml lacks %q: %q", want, ssml)
		}
	}
}

func TestEdgeMP3Passthrough(t *testing.T) {
	p := newEdgeFake(t, &edgeFake{serve: func(ctx context.Context, c *websocket.Conn, n int) { sendTone(ctx, c, edgeTone, true) }})
	r := edgeReq("hi")
	r.Format = MP3
	s, err := p.SynthesizeStream(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if total, err := drainEdge(t, s); err != nil || total != len(edgeTone) {
		t.Errorf("mp3 bytes = %d (%v), want %d", total, err, len(edgeTone))
	}
	if _, err := p.SynthesizeStream(context.Background(), SynthesisRequest{Text: "x", VoiceID: "v", Format: PCM16(16000, 1)}); !errors.Is(err, ErrBadRequest) {
		t.Errorf("16 kHz: %v", err)
	}
}

func TestEdgeLongTextUsesOneConnectionPerPiece(t *testing.T) {
	f := &edgeFake{serve: func(ctx context.Context, c *websocket.Conn, n int) { sendTone(ctx, c, edgeTone, true) }}
	p := newEdgeFake(t, f)
	long := strings.Repeat("word ", edgeMaxTextBytes/5+10)
	s, err := p.SynthesizeStream(context.Background(), edgeReq(long))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	total, err := drainEdge(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if want := 2 * decodedToneMono(t); total < want-4608 || total > want { // ±1 frame at the seam
		t.Errorf("pcm = %d, want about %d", total, want)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conns != 2 {
		t.Errorf("connections = %d", f.conns)
	}
}

func TestEdgeIncremental(t *testing.T) {
	gate := make(chan struct{})
	p := newEdgeFake(t, &edgeFake{serve: func(ctx context.Context, c *websocket.Conn, n int) {
		half := len(edgeTone) / 2
		sendTone(ctx, c, edgeTone[:half], false)
		<-gate
		sendTone(ctx, c, edgeTone[half:], true)
	}})
	s, err := p.SynthesizeStream(context.Background(), edgeReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first, err := s.Next(ctx)
	close(gate)
	if err != nil || len(first.Data) == 0 {
		t.Fatalf("first chunk before the rest was sent: %v", err)
	}
	if _, err := drainEdge(t, s); err != nil {
		t.Fatal(err)
	}
}

func TestEdgeCancelMidStream(t *testing.T) {
	for _, mode := range []string{"ctx", "close", "provider"} {
		t.Run(mode, func(t *testing.T) {
			gone := make(chan struct{})
			p := newEdgeFake(t, &edgeFake{serve: func(ctx context.Context, c *websocket.Conn, n int) {
				sendTone(ctx, c, edgeTone[:len(edgeTone)/2], false)
				c.Read(ctx) // returns once the client drops the socket
				close(gone)
			}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, err := p.SynthesizeStream(ctx, edgeReq("hi"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Next(context.Background()); err != nil {
				t.Fatal(err)
			}
			errc := make(chan error, 1)
			go func() {
				for {
					if _, err := s.Next(context.Background()); err != nil {
						errc <- err
						return
					}
				}
			}()
			time.Sleep(20 * time.Millisecond)
			switch mode {
			case "ctx":
				cancel()
			case "close":
				s.Close()
			case "provider":
				p.Cancel(context.Background())
			}
			select {
			case err := <-errc:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("err = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Next did not return after cancel")
			}
			select {
			case <-gone:
			case <-time.After(2 * time.Second):
				t.Error("server never saw the socket close")
			}
			s.Close()
		})
	}
}

func TestEdgeErrors(t *testing.T) {
	status := func(code int, retry string) func(http.ResponseWriter, int) bool {
		return func(w http.ResponseWriter, n int) bool {
			if retry != "" {
				w.Header().Set("Retry-After", retry)
			}
			http.Error(w, "nope "+testText, code)
			return true
		}
	}
	tests := []struct {
		name   string
		reject func(http.ResponseWriter, int) bool
		serve  func(ctx context.Context, c *websocket.Conn, n int)
		atOpen bool // the error comes from SynthesizeStream, not Next
		kind   error
	}{
		{"handshake 503", status(503, ""), nil, true, ErrUnavailable},
		{"handshake 429", status(429, "2"), nil, true, ErrRateLimited},
		{"handshake 400", status(400, ""), nil, true, ErrBadRequest},
		{"early close", nil, func(ctx context.Context, c *websocket.Conn, n int) {
			sendTone(ctx, c, edgeTone[:len(edgeTone)/2], false)
			c.Close(websocket.StatusInternalError, "")
		}, false, ErrUnavailable},
		{"close before audio", nil, func(ctx context.Context, c *websocket.Conn, n int) {
			c.CloseNow()
		}, false, ErrUnavailable},
		{"no audio", nil, func(ctx context.Context, c *websocket.Conn, n int) {
			c.Write(ctx, websocket.MessageText, edgeText("turn.start"))
			c.Write(ctx, websocket.MessageText, edgeText("turn.end"))
		}, false, ErrBadRequest},
		{"unknown path", nil, func(ctx context.Context, c *websocket.Conn, n int) {
			c.Write(ctx, websocket.MessageText, edgeText("weird"))
		}, false, ErrUnavailable},
		{"bad binary", nil, func(ctx context.Context, c *websocket.Conn, n int) {
			c.Write(ctx, websocket.MessageBinary, []byte{0})
		}, false, ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newEdgeFake(t, &edgeFake{reject: tt.reject, serve: tt.serve})
			s, err := p.SynthesizeStream(context.Background(), edgeReq(testText))
			if !tt.atOpen {
				if err != nil {
					t.Fatalf("open: %v", err)
				}
				defer s.Close()
				_, err = drainEdge(t, s)
				if _, again := s.Next(context.Background()); again != err {
					t.Errorf("error not sticky: %v vs %v", again, err)
				}
			}
			if !errors.Is(err, tt.kind) {
				t.Fatalf("err = %v, want %v", err, tt.kind)
			}
			noSecrets(t, err)
		})
	}
}

func TestEdgeSkewRetry(t *testing.T) {
	server := edgeNow.Add(17 * time.Minute)
	reject := func(w http.ResponseWriter, n int) bool {
		if n > 0 {
			return false
		}
		w.Header().Set("Date", server.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	f := &edgeFake{reject: reject, serve: func(ctx context.Context, c *websocket.Conn, n int) { sendTone(ctx, c, edgeTone, true) }}
	p := newEdgeFake(t, f)
	s, err := p.SynthesizeStream(context.Background(), edgeReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := drainEdge(t, s); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	gec := f.urls[1].Query().Get("Sec-MS-GEC")
	f.mu.Unlock()
	if gec != edgeGEC(server) || gec == edgeGEC(edgeNow) {
		t.Error("retry did not use the server clock")
	}

	// A second 403 is final.
	always := func(w http.ResponseWriter, n int) bool {
		w.Header().Set("Date", server.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	f2 := &edgeFake{reject: always}
	p2 := newEdgeFake(t, f2)
	if _, err := p2.SynthesizeStream(context.Background(), edgeReq("hi")); !errors.Is(err, ErrAuth) {
		t.Errorf("err = %v", err)
	}
	if f2.conns != 2 {
		t.Errorf("attempts = %d, want 2", f2.conns)
	}
}

func TestEdgeServerDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	p := NewEdge(EdgeConfig{Config: Config{BaseURL: srv.URL}})
	srv.Close()
	if _, err := p.SynthesizeStream(context.Background(), edgeReq("hi")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("synth: %v", err)
	}
	if _, err := p.ListVoices(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("voices: %v", err)
	}
}

func TestEdgeListVoices(t *testing.T) {
	f := &edgeFake{voices: func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 0 {
			w.Header().Set("Date", edgeNow.Add(time.Hour).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte(`[{"ShortName":"en-US-AriaNeural","Locale":"en-US","FriendlyName":"Microsoft Aria Online (Natural) - English (United States)","Status":"GA"}]`))
	}}
	p := newEdgeFake(t, f)
	voices, err := p.ListVoices(context.Background())
	if err != nil || len(voices) != 1 || voices[0].ID != "en-US-AriaNeural" {
		t.Fatalf("voices = %+v, %v", voices, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.urls[1].Query()
	if q.Get("trustedclienttoken") != edgeTrustedToken || q.Get("Sec-MS-GEC") != edgeGEC(edgeNow.Add(time.Hour)) {
		t.Errorf("query = %v", q)
	}
	if f.hdrs[1].Get("Accept-Encoding") != "" && f.hdrs[1].Get("Accept-Encoding") != "gzip" {
		t.Errorf("Accept-Encoding = %q: net/http would not decode it", f.hdrs[1].Get("Accept-Encoding"))
	}
}
