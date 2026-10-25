package setupcheck

import (
	"context"
	"time"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/talk"
)

// Loopback is the voice check for StepTTS (OC-133): Companion speaks
// readiness.MicPhrase(lang) through the configured voice while the
// microphone records, so one check covers voice, microphone and
// recognition. The step passes once the phrase played; what the
// microphone made of it goes to Detail[readiness.DetailHeard] only, since
// headphones keep a working setup from hearing itself. A match is handed
// to STT, which then passes without a second recording.
//
// Capture applies no echo cancellation (internal/platform/audio opens the
// device with miniaudio's defaults: no voice-processing unit, no
// communications stream), so the speakers reach the microphone. An echo
// canceller the user enabled in the OS may still remove the phrase; the
// check then reports no_speech and the user reads the phrase instead.
type Loopback struct {
	Listener   *Listener
	Speaker    talk.Speaker
	Configured func() bool // speech output is set up
	Lang       func() string
	// STT is the StepSTT check that reuses a match and shows what was
	// heard; nil keeps the result to this check.
	STT *STT
	// Window bounds the recording from the moment the microphone opens
	// (default 30 s): synthesis, playback and the phrase's end fit in it.
	Window time.Duration
	// Tail is how long the recording goes on after playback ended
	// without an end of utterance (default 2 s).
	Tail       time.Duration
	TTSTimeout time.Duration
}

func (l Loopback) Run(ctx context.Context) readiness.Result {
	lang := l.Lang()
	phrase := readiness.MicPhrase(lang)
	if l.STT != nil {
		l.STT.forgetLoopback()
	}
	window := l.Window
	if window <= 0 {
		window = 30 * time.Second
	}
	tail := l.Tail
	if tail <= 0 {
		tail = 2 * time.Second
	}

	// Recording starts first, so the phrase's first word is not lost to
	// the microphone's start-up.
	lctx, stopListen := context.WithCancel(ctx)
	defer stopListen()
	opened := make(chan struct{})
	stop := make(chan struct{})
	type outcome struct {
		heard Heard
		fail  *Failure
	}
	done := make(chan outcome, 1)
	if l.Listener == nil {
		done <- outcome{fail: &Failure{StageSTT, CodeSTTUnavailable}}
	} else {
		go func() {
			h, f := l.Listener.listen(lctx, readiness.PhraseLanguage(lang), listenHooks{
				opened: func() { close(opened) }, stop: stop, window: window,
			})
			done <- outcome{h, f}
		}()
	}
	var heard *outcome
	select {
	case <-opened:
	case o := <-done:
		// No microphone or recogniser: the voice is still worth testing.
		heard = &o
	}

	if code := say(ctx, l.Speaker, l.Configured, phrase, l.TTSTimeout); code != "" {
		stopListen()
		if heard == nil {
			<-done
		}
		if ctx.Err() != nil {
			code = CodeCanceled
		}
		return readiness.Result{Status: readiness.Failed, Code: code, Detail: map[string]string{
			readiness.DetailSpoke: code, readiness.DetailHeard: readiness.DetailSkipped,
		}}
	}
	if heard == nil {
		t := time.NewTimer(tail)
		select {
		case o := <-done:
			heard = &o
		case <-t.C:
			close(stop)
			o := <-done
			heard = &o
		}
		t.Stop()
	}
	if ctx.Err() != nil {
		return failed(CodeCanceled)
	}

	code := readiness.DetailOK
	switch {
	case heard.fail != nil:
		code = heard.fail.Code
	case !readiness.PhraseMatches(phrase, heard.heard.Text):
		code = CodeMismatch
	}
	if l.STT != nil {
		l.STT.loopbackHeard(heard.heard.Text, code == readiness.DetailOK)
	}
	return readiness.Result{Status: readiness.Passed, Detail: map[string]string{
		readiness.DetailSpoke: readiness.DetailOK, readiness.DetailHeard: code,
	}}
}
