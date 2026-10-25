package tts

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/hajimehoshi/go-mp3"
)

// EdgeProviderID is the settings id of Microsoft Edge's Read aloud voices.
// They need no API key.
const EdgeProviderID = "edge"

// The service is the unofficial endpoint behind Edge's Read aloud; these
// values mimic the browser the way edge-tts does (src/edge_tts/constants.py)
// and must follow it when Microsoft changes them.
const (
	edgeDefaultBaseURL = "https://speech.platform.bing.com/consumer/speech/synthesize/readaloud"
	edgeTrustedToken   = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"
	edgeChromium       = "143.0.3650.75"
	edgeChromiumMajor  = "143"
	edgeGECVersion     = "2-" + edgeChromium
	edgeOutputFormat   = "audio-24khz-48kbitrate-mono-mp3"
	edgeSampleRate     = 24000
	// edgeMaxTextBytes is the escaped text one request may carry.
	edgeMaxTextBytes = 4096
	edgeMaxMessage   = 1 << 20
)

var edgeUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" +
	edgeChromiumMajor + ".0.0.0 Safari/537.36 Edg/" + edgeChromiumMajor + ".0.0.0"

// EdgeConfig configures the Edge Read aloud provider. Config.APIKey is
// unused; Config.Timeout bounds connecting and each wait for the next
// message, not the whole utterance.
type EdgeConfig struct {
	Config
	HTTPClient *http.Client
	// Now is the clock the token is derived from; nil means time.Now.
	Now func() time.Time
}

// Edge implements Provider over the Read aloud WebSocket. The service
// returns MP3, which is decoded here so callers get PCM16 like from every
// other provider.
type Edge struct {
	base
	now  func() time.Time
	skew atomic.Int64 // server minus local clock, ns; learnt from a 403
}

func NewEdge(cfg EdgeConfig) *Edge {
	if cfg.BaseURL == "" {
		cfg.BaseURL = edgeDefaultBaseURL
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Edge{base: newBase(EdgeProviderID, cfg.Config, cfg.HTTPClient), now: now}
}

// edgeDefaultVoices are per language; the multilingual voice covers others.
var edgeDefaultVoices = map[string]string{
	"en": "en-US-AriaNeural",
	"ru": "ru-RU-SvetlanaNeural",
	"es": "es-ES-ElviraNeural",
	"pt": "pt-BR-FranciscaNeural",
	"fr": "fr-FR-DeniseNeural",
	"de": "de-DE-KatjaNeural",
	"it": "it-IT-ElsaNeural",
	"pl": "pl-PL-ZofiaNeural",
	"tr": "tr-TR-EmelNeural",
	"uk": "uk-UA-PolinaNeural",
	"zh": "zh-CN-XiaoxiaoNeural",
	"ja": "ja-JP-NanamiNeural",
	"ko": "ko-KR-SunHiNeural",
}

const edgeFallbackVoice = "en-US-EmmaMultilingualNeural"

// EdgeDefaultVoice is the voice for a language code ("de", "pt-BR").
func EdgeDefaultVoice(lang string) string {
	lang = strings.ToLower(lang)
	if i := strings.IndexAny(lang, "-_"); i >= 0 {
		lang = lang[:i]
	}
	if v, ok := edgeDefaultVoices[lang]; ok {
		return v
	}
	return edgeFallbackVoice
}

func (e *Edge) token() string {
	return edgeGEC(e.now().Add(time.Duration(e.skew.Load())))
}

// learnSkew adopts the server clock from a rejected request's Date header,
// as edge-tts does: the token is time based, so a wrong local clock gets a
// 403. It reports whether a retry is worth it.
func (e *Edge) learnSkew(resp *http.Response) bool {
	t, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		return false
	}
	e.skew.Store(int64(t.Sub(e.now())))
	return true
}

func (e *Edge) headers(extra map[string]string) http.Header {
	h := http.Header{}
	// No Accept-Encoding: net/http only decodes bodies it asked to compress.
	h.Set("User-Agent", edgeUserAgent)
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("Cookie", "muid="+strings.ToUpper(randomHex())+";")
	for k, v := range extra {
		h.Set(k, v)
	}
	for k, v := range e.cfg.Headers {
		h.Set(k, v)
	}
	return h
}

func randomHex() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type edgeVoice struct {
	ShortName    string `json:"ShortName"`
	FriendlyName string `json:"FriendlyName"`
	Locale       string `json:"Locale"`
	Status       string `json:"Status"`
}

// edgeVoiceName turns "Microsoft Aria Online (Natural) - English (United
// States)" into "Aria - English (United States)".
func edgeVoiceName(v edgeVoice) string {
	n := strings.TrimPrefix(v.FriendlyName, "Microsoft ")
	n = strings.Replace(n, " Online (Natural)", "", 1)
	if n == "" {
		return v.ShortName
	}
	return n
}

func parseEdgeVoices(r io.Reader) ([]Voice, error) {
	var list []edgeVoice
	if err := json.NewDecoder(io.LimitReader(r, maxVoicesBody)).Decode(&list); err != nil {
		return nil, err
	}
	voices := make([]Voice, 0, len(list))
	for _, v := range list {
		if v.ShortName == "" || v.Status == "Deprecated" {
			continue
		}
		out := Voice{ID: v.ShortName, Name: edgeVoiceName(v)}
		if v.Locale != "" {
			out.Languages = []string{v.Locale}
		}
		voices = append(voices, out)
	}
	return voices, nil
}

func (e *Edge) ListVoices(ctx context.Context) ([]Voice, error) {
	for attempt := 0; ; attempt++ {
		voices, retry, err := e.listVoices(ctx, attempt == 0)
		if !retry {
			return voices, err
		}
	}
}

func (e *Edge) listVoices(parent context.Context, mayRetry bool) ([]Voice, bool, error) {
	ctx, cancel := context.WithTimeout(parent, e.cfg.timeout())
	defer cancel()
	u := e.cfg.BaseURL + "/voices/list?trustedclienttoken=" + edgeTrustedToken +
		"&Sec-MS-GEC=" + e.token() + "&Sec-MS-GEC-Version=" + edgeGECVersion
	r, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, false, &Error{Kind: ErrBadRequest, Provider: e.name, Cause: stripURL(err)}
	}
	r.Header = e.headers(map[string]string{
		"Authority":        "speech.platform.bing.com",
		"Sec-CH-UA":        `" Not;A Brand";v="99", "Microsoft Edge";v="` + edgeChromiumMajor + `", "Chromium";v="` + edgeChromiumMajor + `"`,
		"Sec-CH-UA-Mobile": "?0",
		"Accept":           "*/*",
		"Sec-Fetch-Site":   "none",
		"Sec-Fetch-Mode":   "cors",
		"Sec-Fetch-Dest":   "empty",
	})
	// Not base.do: a 403 carries the server clock this needs.
	resp, err := e.client.Do(r.WithContext(ctx))
	if err != nil {
		if parent.Err() != nil {
			return nil, false, parent.Err()
		}
		return nil, false, &Error{Kind: ErrUnavailable, Provider: e.name, Cause: stripURL(err)}
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		if mayRetry && resp.StatusCode == http.StatusForbidden && e.learnSkew(resp) {
			return nil, true, nil
		}
		return nil, false, statusError(e.name, resp)
	}
	defer resp.Body.Close()
	voices, err := parseEdgeVoices(resp.Body)
	if err != nil {
		if parent.Err() != nil {
			return nil, false, parent.Err()
		}
		return nil, false, &Error{Kind: ErrUnavailable, Provider: e.name, Cause: fmt.Errorf("decode voices: %w", stripURL(err))}
	}
	return voices, false, nil
}

// edgeRate maps a speed multiplier to SSML prosody rate.
func edgeRate(speed float64) string {
	return fmt.Sprintf("%+d%%", int(math.Round((speed-1)*100)))
}

// edgeSSMLVoice expands "en-US-AriaNeural" to the long form Edge sends;
// other spellings pass through.
func edgeSSMLVoice(id string) string {
	parts := strings.SplitN(id, "-", 3)
	if len(parts) != 3 || !strings.HasSuffix(parts[2], "Neural") {
		return id
	}
	region, name := parts[1], parts[2]
	if i := strings.IndexByte(name, '-'); i >= 0 { // e.g. zh-CN-liaoning-XiaobeiNeural
		region, name = region+"-"+name[:i], name[i+1:]
	}
	return "Microsoft Server Speech Text to Speech Voice (" + parts[0] + "-" + region + ", " + name + ")"
}

var (
	edgeTextEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	edgeAttrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "'", "&apos;", `"`, "&quot;")
)

// edgeCleanText replaces the control characters the service rejects
// (vertical tab and friends) with spaces, then escapes for SSML.
func edgeCleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r <= 8 || r == 11 || r == 12 || (r >= 14 && r <= 31) {
			return ' '
		}
		return r
	}, s)
	return edgeTextEscaper.Replace(s)
}

// edgeSplit cuts escaped text into pieces of at most limit bytes, preferring
// newlines then spaces, never inside a UTF-8 sequence or an XML entity.
func edgeSplit(text string, limit int) []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	for len(text) > limit {
		at := strings.LastIndexByte(text[:limit], '\n')
		if at < 0 {
			at = strings.LastIndexByte(text[:limit], ' ')
		}
		if at < 0 {
			at = limit
			for at > 0 && !utf8.RuneStart(text[at]) {
				at--
			}
		}
		for at > 0 {
			amp := strings.LastIndexByte(text[:at], '&')
			if amp < 0 || strings.IndexByte(text[amp:at], ';') >= 0 {
				break
			}
			at = amp
		}
		if at <= 0 {
			at = limit // degenerate input; a split entity beats looping forever
		}
		add(text[:at])
		text = text[at:]
	}
	add(text)
	return out
}

func edgeSSML(voice, rate, escaped string) string {
	return "<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='en-US'>" +
		"<voice name='" + edgeAttrEscaper.Replace(edgeSSMLVoice(voice)) + "'>" +
		"<prosody pitch='+0Hz' rate='" + rate + "' volume='+0%'>" + escaped +
		"</prosody></voice></speak>"
}

// edgeTimestamp is the JavaScript Date string Edge puts in X-Timestamp.
func edgeTimestamp(t time.Time) string {
	return t.UTC().Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)")
}

func (e *Edge) configMessage() string {
	return "X-Timestamp:" + edgeTimestamp(e.now()) + "\r\n" +
		"Content-Type:application/json; charset=utf-8\r\n" +
		"Path:speech.config\r\n\r\n" +
		`{"context":{"synthesis":{"audio":{"metadataoptions":{` +
		`"sentenceBoundaryEnabled":"true","wordBoundaryEnabled":"false"},` +
		`"outputFormat":"` + edgeOutputFormat + `"}}}}` + "\r\n"
}

func (e *Edge) ssmlMessage(ssml string) string {
	// The trailing Z after a local-style date is what Edge itself sends.
	return "X-RequestId:" + randomHex() + "\r\n" +
		"Content-Type:application/ssml+xml\r\n" +
		"X-Timestamp:" + edgeTimestamp(e.now()) + "Z\r\n" +
		"Path:ssml\r\n\r\n" + ssml
}

// dial opens the socket; one 403 is retried after adopting the server clock.
func (e *Edge) dial(ctx context.Context) (*websocket.Conn, error) {
	for attempt := 0; ; attempt++ {
		u := e.cfg.BaseURL + "/edge/v1?TrustedClientToken=" + edgeTrustedToken +
			"&ConnectionId=" + randomHex() + "&Sec-MS-GEC=" + e.token() + "&Sec-MS-GEC-Version=" + edgeGECVersion
		dctx, cancel := context.WithTimeout(ctx, e.cfg.timeout())
		conn, resp, err := websocket.Dial(dctx, u, &websocket.DialOptions{
			HTTPClient: e.client,
			HTTPHeader: e.headers(map[string]string{
				"Pragma":        "no-cache",
				"Cache-Control": "no-cache",
				"Origin":        "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold",
			}),
		})
		timedOut := dctx.Err() != nil
		cancel()
		if err == nil {
			conn.SetReadLimit(edgeMaxMessage)
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			if resp.StatusCode == http.StatusForbidden && attempt == 0 && e.learnSkew(resp) {
				continue
			}
			return nil, statusError(e.name, resp)
		}
		if timedOut {
			return nil, &Error{Kind: ErrUnavailable, Provider: e.name, Cause: errors.New("timeout connecting")}
		}
		return nil, &Error{Kind: ErrUnavailable, Provider: e.name, Cause: stripURL(err)}
	}
}

// request sends the config and one SSML document on conn.
func (e *Edge) request(ctx context.Context, conn *websocket.Conn, ssml string) error {
	for _, m := range []string{e.configMessage(), e.ssmlMessage(ssml)} {
		if err := conn.Write(ctx, websocket.MessageText, []byte(m)); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &Error{Kind: ErrUnavailable, Provider: e.name, Cause: errors.New("send request")}
		}
	}
	return nil
}

func (e *Edge) SynthesizeStream(ctx context.Context, req SynthesisRequest) (Stream, error) {
	req, err := req.normalize()
	if err != nil {
		return nil, err
	}
	switch {
	case req.Format == PCM16(edgeSampleRate, 1), req.Format.Container == ContainerMP3:
	default:
		return nil, badRequest("edge supports pcm16 24000 Hz mono and mp3")
	}
	pieces := edgeSplit(edgeCleanText(req.Text), edgeMaxTextBytes)
	if len(pieces) == 0 {
		return nil, badRequest("text has nothing to say")
	}
	rate := edgeRate(req.Speed)

	sctx, cancel := context.WithCancel(ctx)
	untrack := e.track(cancel) // before dialing, so Cancel also aborts a pending connect
	conn, err := e.dial(sctx)
	if err == nil {
		if err = e.request(sctx, conn, edgeSSML(req.VoiceID, rate, pieces[0])); err != nil {
			// The request already failed; closing an aborted socket can
			// only fail the same way.
			_ = conn.CloseNow()
		}
	}
	if err != nil {
		untrack()
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}

	pr, pw := io.Pipe()
	body := &edgeBody{pr: pr, done: make(chan struct{}), mp3: req.Format.Container == ContainerMP3}
	go func() {
		defer close(body.done)
		err := e.pump(sctx, conn, pw, req.VoiceID, rate, pieces)
		body.setErr(err)
		pw.CloseWithError(err) // nil means io.EOF
	}()
	return newReaderStream(sctx, cancel, ctx, body, req.Format, e.name, untrack), nil
}

var errEdgeNoAudio = errors.New("no audio received")

// pump copies the MP3 of every piece into w, one connection per piece as
// the service ends a connection's usefulness at turn.end.
func (e *Edge) pump(ctx context.Context, conn *websocket.Conn, w io.Writer, voice, rate string, pieces []string) error {
	for i, p := range pieces {
		if i > 0 {
			var err error
			if conn, err = e.dial(ctx); err != nil {
				return err
			}
			if err := e.request(ctx, conn, edgeSSML(voice, rate, p)); err != nil {
				_ = conn.CloseNow()
				return err
			}
		}
		err := e.readTurn(ctx, conn, w)
		_ = conn.CloseNow() // the turn is over either way
		if err != nil {
			return err
		}
	}
	return nil
}

// readTurn reads one turn's audio until Path:turn.end.
func (e *Edge) readTurn(ctx context.Context, conn *websocket.Conn, w io.Writer) error {
	audio := false
	for {
		rctx, cancel := context.WithTimeout(ctx, e.cfg.timeout())
		typ, msg, err := conn.Read(rctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &Error{Kind: ErrUnavailable, Provider: e.name, Cause: errors.New("connection closed before turn end")}
		}
		switch typ {
		case websocket.MessageText:
			path, _ := edgeTextPath(msg)
			switch path {
			case "turn.end":
				if !audio {
					// Usually an unknown voice: the service says nothing about why.
					return &Error{Kind: ErrBadRequest, Provider: e.name, Cause: errEdgeNoAudio}
				}
				return nil
			case "turn.start", "response", "audio.metadata":
			default:
				return &Error{Kind: ErrUnavailable, Provider: e.name, Cause: fmt.Errorf("unexpected path %q", path)}
			}
		case websocket.MessageBinary:
			data, err := edgeAudioFrame(msg)
			if err != nil {
				return &Error{Kind: ErrUnavailable, Provider: e.name, Cause: err}
			}
			if len(data) == 0 {
				continue
			}
			audio = true
			if _, err := w.Write(data); err != nil {
				return context.Canceled // reader closed: the stream was closed
			}
		}
	}
}

// edgeHeaders parses "Key:Value\r\n" lines.
func edgeHeaders(b []byte) map[string]string {
	h := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(b), "\r\n"), "\r\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			h[k] = v
		}
	}
	return h
}

func edgeTextPath(msg []byte) (string, []byte) {
	head, body, _ := bytes.Cut(msg, []byte("\r\n\r\n"))
	return edgeHeaders(head)["Path"], body
}

// edgeAudioFrame returns the MP3 bytes of a binary message: a big-endian
// uint16 header length, the headers, then the audio.
func edgeAudioFrame(msg []byte) ([]byte, error) {
	if len(msg) < 2 {
		return nil, errors.New("binary message without header length")
	}
	n := int(binary.BigEndian.Uint16(msg))
	if 2+n > len(msg) {
		return nil, errors.New("binary header length beyond message")
	}
	h := edgeHeaders(msg[2 : 2+n])
	if h["Path"] != "audio" {
		return nil, errors.New("binary message is not audio")
	}
	data := msg[2+n:]
	switch ct, ok := h["Content-Type"]; {
	case !ok:
		if len(data) != 0 {
			return nil, errors.New("audio without content type")
		}
		return nil, nil
	case ct != "audio/mpeg":
		return nil, errors.New("unexpected audio content type")
	}
	return data, nil
}

// edgeBody is what readerStream reads: PCM16 mono decoded from the MP3 the
// pump writes into the pipe, or the MP3 itself when that was requested.
type edgeBody struct {
	pr   *io.PipeReader
	done chan struct{}
	mp3  bool

	dec     *mp3.Decoder
	stereo  []byte
	pending []byte // leftover partial stereo frame

	mu  sync.Mutex
	err error // pump's terminal error
}

func (b *edgeBody) setErr(err error) {
	b.mu.Lock()
	b.err = err
	b.mu.Unlock()
}

// fail prefers the pump's error: the decoder only sees a truncated stream.
func (b *edgeBody) fail(err error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	return err
}

func (b *edgeBody) Read(p []byte) (int, error) {
	if b.mp3 {
		n, err := b.pr.Read(p)
		if err != nil && n == 0 {
			return 0, b.fail(err)
		}
		return n, nil
	}
	if b.dec == nil {
		d, err := mp3.NewDecoder(b.pr)
		if err != nil {
			return 0, b.fail(err)
		}
		if d.SampleRate() != edgeSampleRate {
			return 0, &Error{Kind: ErrUnavailable, Provider: EdgeProviderID, Cause: errors.New("unexpected sample rate")}
		}
		b.dec = d
	}
	// go-mp3 always yields 16-bit stereo; Edge's mono comes out duplicated.
	want := len(p) / 2 * 4
	if want == 0 {
		return 0, nil
	}
	if cap(b.stereo) < want {
		b.stereo = make([]byte, want)
	}
	buf := append(b.stereo[:0], b.pending...)
	n, err := b.dec.Read(b.stereo[len(buf):want])
	buf = b.stereo[:len(buf)+n]
	frames := len(buf) / 4
	for i := 0; i < frames; i++ {
		l := int16(binary.LittleEndian.Uint16(buf[4*i:]))
		r := int16(binary.LittleEndian.Uint16(buf[4*i+2:]))
		binary.LittleEndian.PutUint16(p[2*i:], uint16((int32(l)+int32(r))/2))
	}
	b.pending = append(b.pending[:0], buf[4*frames:]...)
	if err != nil {
		if frames > 0 {
			return 2 * frames, nil // report the error on the next call
		}
		return 0, b.fail(err)
	}
	return 2 * frames, nil
}

func (b *edgeBody) Close() error {
	b.pr.Close()
	<-b.done
	return nil
}
