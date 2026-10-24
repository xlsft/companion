//go:build whisper && cgo

package whisper

/*
#cgo CFLAGS: -I${SRCDIR}/../../../../third_party/whisper-install/include
#cgo LDFLAGS: -L${SRCDIR}/../../../../third_party/whisper-install/lib
#include <stdlib.h>
#include <string.h>
#include <stdbool.h>
#include "whisper.h"
#include "ggml-backend.h"

// whisper.cpp logs model and decode diagnostics to stderr; the sidecar's
// stderr must stay a terse technical log, so drop them.
static void oc_log_discard(enum ggml_log_level level, const char *text, void *user) {
	(void)level; (void)text; (void)user;
}
static void oc_quiet(void) { whisper_log_set(oc_log_discard, NULL); }

static bool oc_abort(void *data) { return __atomic_load_n((int *)data, __ATOMIC_RELAXED) != 0; }
static void oc_store(int *flag, int v) { __atomic_store_n(flag, v, __ATOMIC_RELAXED); }
static void oc_set_abort(struct whisper_full_params *p, int *flag) {
	p->abort_callback = oc_abort;
	p->abort_callback_user_data = flag;
}

// oc_gpu_device copies the name of the first GPU device compiled into ggml.
static int oc_gpu_device(char *name, size_t n) {
	for (size_t i = 0; i < ggml_backend_dev_count(); i++) {
		ggml_backend_dev_t dev = ggml_backend_dev_get(i);
		enum ggml_backend_dev_type t = ggml_backend_dev_type(dev);
		if (t == GGML_BACKEND_DEVICE_TYPE_GPU || t == GGML_BACKEND_DEVICE_TYPE_IGPU) {
			strncpy(name, ggml_backend_dev_name(dev), n - 1);
			name[n - 1] = 0;
			return 1;
		}
	}
	return 0;
}
*/
import "C"

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/xlsft/companion/internal/stt/engine"
	"github.com/xlsft/companion/internal/stt/protocol"
)

func init() { engine.Register("whisper", New) }

// minSamples pads short utterances: whisper is trained on ≥1 s inputs and
// degrades (or returns nothing) below that.
const minSamples = protocol.SampleRate

type Engine struct {
	// mu serialises decodes: one whisper_context holds one decoder state.
	mu           sync.Mutex
	ctx          *C.struct_whisper_context
	abort        *C.int
	threads      int
	multilingual bool
}

func New(cfg engine.Config) (engine.Engine, engine.Info, error) {
	info := engine.Info{RuntimeVersion: "whisper.cpp " + C.GoString(C.whisper_version()), BackendInUse: protocol.BackendCPU}
	if cfg.ModelPath == "" {
		return nil, info, engine.ErrModelNotFound
	}
	if _, err := os.Stat(cfg.ModelPath); err != nil {
		return nil, info, fmt.Errorf("%w: %v", engine.ErrModelNotFound, err)
	}
	C.oc_quiet()

	var gpu string
	if cfg.Backend == protocol.BackendAuto {
		buf := make([]byte, 128)
		if C.oc_gpu_device((*C.char)(unsafe.Pointer(&buf[0])), C.size_t(len(buf))) == 1 {
			gpu = C.GoString((*C.char)(unsafe.Pointer(&buf[0])))
		}
	}
	path := C.CString(cfg.ModelPath)
	defer C.free(unsafe.Pointer(path))
	cparams := C.whisper_context_default_params()
	cparams.use_gpu = C.bool(gpu != "")
	wctx := C.whisper_init_from_file_with_params(path, cparams)
	if wctx == nil && gpu != "" {
		// A broken GPU driver must not cost the user STT (FR-STT-010).
		cparams.use_gpu = false
		wctx = C.whisper_init_from_file_with_params(path, cparams)
		info.FallbackReason, gpu = "backend_init_failed", ""
	}
	if wctx == nil {
		return nil, info, fmt.Errorf("%w: whisper_init_from_file", engine.ErrBackendInit)
	}
	if gpu != "" {
		info.BackendInUse = gpu
	}

	e := &Engine{
		ctx:          wctx,
		abort:        (*C.int)(C.calloc(1, C.size_t(unsafe.Sizeof(C.int(0))))),
		threads:      cfg.Threads,
		multilingual: C.whisper_is_multilingual(wctx) != 0,
	}
	if e.threads <= 0 {
		e.threads = min(runtime.NumCPU(), 8)
	}
	if e.multilingual {
		for i := 0; i <= int(C.whisper_lang_max_id()); i++ {
			info.Languages = append(info.Languages, C.GoString(C.whisper_lang_str(C.int(i))))
		}
	} else {
		info.Languages = []string{"en"}
	}
	return e, info, nil
}

func (e *Engine) Transcribe(ctx context.Context, pcm []float32, opts engine.Options) (engine.Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return engine.Result{}, err
	}
	if e.ctx == nil {
		return engine.Result{}, fmt.Errorf("whisper: engine closed")
	}
	if len(pcm) < minSamples {
		padded := make([]float32, minSamples)
		copy(padded, pcm)
		pcm = padded
	}

	lang := opts.Language
	if lang == "" {
		lang = protocol.LanguageAuto
	}
	if !e.multilingual {
		lang = "en"
	}
	clang := C.CString(lang)
	defer C.free(unsafe.Pointer(clang))

	p := C.whisper_full_default_params(C.WHISPER_SAMPLING_GREEDY)
	p.n_threads = C.int(e.threads)
	p.language = clang
	p.no_context = true // every call is a fresh decode of the given audio
	p.print_progress = false
	p.print_realtime = false
	p.print_timestamps = false
	p.print_special = false
	p.suppress_nst = true
	if opts.Partial {
		p.single_segment = true
		p.no_timestamps = true
	}
	C.oc_store(e.abort, 0)
	C.oc_set_abort(&p, e.abort)

	// The abort callback is polled between ggml graph computations, so a
	// cancelled decode stops within one encoder/decoder step.
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		C.oc_store(e.abort, 1)
		close(stopped)
	})
	rc := C.whisper_full(e.ctx, p, (*C.float)(unsafe.Pointer(&pcm[0])), C.int(len(pcm)))
	if !stop() {
		<-stopped // don't let a late store abort the next decode
	}
	if err := ctx.Err(); err != nil {
		return engine.Result{}, err
	}
	if rc != 0 {
		return engine.Result{}, fmt.Errorf("whisper_full: rc=%d", int(rc))
	}

	res := engine.Result{Language: lang}
	if lang == protocol.LanguageAuto {
		res.Language = C.GoString(C.whisper_lang_str(C.whisper_full_lang_id(e.ctx)))
	}
	n := int(C.whisper_full_n_segments(e.ctx))
	for i := 0; i < n; i++ {
		text := C.GoString(C.whisper_full_get_segment_text(e.ctx, C.int(i)))
		// Timestamps are in 10 ms units.
		res.Segments = append(res.Segments, engine.Segment{
			Start: time.Duration(C.whisper_full_get_segment_t0(e.ctx, C.int(i))) * 10 * time.Millisecond,
			End:   time.Duration(C.whisper_full_get_segment_t1(e.ctx, C.int(i))) * 10 * time.Millisecond,
			Text:  text,
		})
		res.Text += text
	}
	return res, nil
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx != nil {
		C.whisper_free(e.ctx)
		e.ctx = nil
		C.free(unsafe.Pointer(e.abort))
		e.abort = nil
	}
	return nil
}
