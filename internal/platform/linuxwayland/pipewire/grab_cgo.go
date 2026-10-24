//go:build linux && cgo

package pipewire

/*
// pkg-config would be nicer, but libpipewire-0.3.pc carries
// -fno-strict-overflow, which cmd/go rejects without CGO_CFLAGS_ALLOW.
#cgo CFLAGS: -I/usr/include/pipewire-0.3 -I/usr/include/spa-0.2 -D_REENTRANT -fno-strict-aliasing
#cgo LDFLAGS: -lpipewire-0.3
#include <pipewire/pipewire.h>
#include <spa/param/video/format-utils.h>
#include <spa/param/buffers.h>
#include <spa/pod/builder.h>
#include <spa/buffer/buffer.h>
#include <spa/utils/result.h>
#include <linux/dma-buf.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

typedef struct {
	struct pw_thread_loop *loop;
	struct pw_context *context;
	struct pw_core *core;
	struct pw_stream *stream;
	struct spa_hook stream_listener;
	struct spa_hook core_listener;
	struct spa_video_info_raw info;
	int state; // 0 running, 1 frame captured, -1 error
	char err[256];
	uint8_t *pixels;
	int width, height, stride;
	uint32_t format;
	uint32_t data_type;
	uint64_t modifier;
	int has_modifier;
	// Diagnostics reported on timeout.
	int n_format, n_process, n_empty, n_corrupted;
	const char *stream_state;
	uint32_t last_type;
} oc_grab;

static void oc_set_err(oc_grab *g, const char *msg) {
	if (g->state == 0) {
		snprintf(g->err, sizeof g->err, "%s", msg ? msg : "error");
		g->state = -1;
	}
}

static void oc_on_core_error(void *data, uint32_t id, int seq, int res, const char *message) {
	oc_grab *g = data;
	if (id == PW_ID_CORE) oc_set_err(g, message);
}

static const struct pw_core_events oc_core_events = {
	PW_VERSION_CORE_EVENTS,
	.error = oc_on_core_error,
};

static void oc_on_state_changed(void *data, enum pw_stream_state old, enum pw_stream_state state, const char *error) {
	oc_grab *g = data;
	g->stream_state = pw_stream_state_as_string(state);
	if (state == PW_STREAM_STATE_ERROR) oc_set_err(g, error ? error : "stream error");
}

static void oc_on_param_changed(void *data, uint32_t id, const struct spa_pod *param) {
	oc_grab *g = data;
	uint32_t media_type, media_subtype;
	if (param == NULL || id != SPA_PARAM_Format) return;
	if (spa_format_parse(param, &media_type, &media_subtype) < 0) return;
	if (media_type != SPA_MEDIA_TYPE_video || media_subtype != SPA_MEDIA_SUBTYPE_raw) return;
	spa_zero(g->info);
	if (spa_format_video_raw_parse(param, &g->info) < 0) {
		oc_set_err(g, "cannot parse video format");
		return;
	}
	g->n_format++;
	g->format = g->info.format;
	g->has_modifier = spa_pod_find_prop(param, NULL, SPA_FORMAT_VIDEO_modifier) != NULL;
	g->modifier = g->info.modifier;

	// A negotiated modifier means the producer allocates DMA-BUFs;
	// otherwise ask for CPU-mappable memory.
	int types = g->has_modifier ? (1 << SPA_DATA_DmaBuf)
	                            : ((1 << SPA_DATA_MemPtr) | (1 << SPA_DATA_MemFd));
	uint8_t buf[1024];
	struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buf, sizeof buf);
	const struct spa_pod *params[1];
	params[0] = spa_pod_builder_add_object(&b,
		SPA_TYPE_OBJECT_ParamBuffers, SPA_PARAM_Buffers,
		SPA_PARAM_BUFFERS_buffers, SPA_POD_CHOICE_RANGE_Int(4, 1, 32),
		SPA_PARAM_BUFFERS_dataType, SPA_POD_CHOICE_FLAGS_Int(types));
	pw_stream_update_params(g->stream, params, 1);
}

static void oc_on_process(void *data) {
	oc_grab *g = data;
	struct pw_buffer *b = pw_stream_dequeue_buffer(g->stream);
	if (b == NULL) return;
	struct spa_data *d = &b->buffer->datas[0];
	g->n_process++;
	g->last_type = d->type;
	// DMA-BUF producers (niri) leave chunk->size at 0; only MemPtr/MemFd use it.
	int empty = d->chunk == NULL || (d->type != SPA_DATA_DmaBuf && d->chunk->size == 0);
	if (empty) g->n_empty++;
	else if (d->chunk->flags & SPA_CHUNK_FLAG_CORRUPTED) g->n_corrupted++;
	if (g->state != 0 || empty ||
	    (d->chunk->flags & SPA_CHUNK_FLAG_CORRUPTED)) {
		// Producers send empty chunks for cursor-only or damage-free updates.
		pw_stream_queue_buffer(g->stream, b);
		return;
	}
	int w = g->info.size.width, h = g->info.size.height;
	int stride = d->chunk->stride > 0 ? d->chunk->stride : w * 4;
	uint8_t *src = NULL;
	void *map = NULL;
	size_t maplen = 0;
	if (d->data != NULL) {
		src = (uint8_t *)d->data + d->chunk->offset;
	} else if (d->type == SPA_DATA_MemFd || d->type == SPA_DATA_DmaBuf) {
		maplen = d->maxsize + d->mapoffset;
		map = mmap(NULL, maplen, PROT_READ, MAP_SHARED, d->fd, 0);
		if (map == MAP_FAILED) {
			map = NULL;
			oc_set_err(g, d->type == SPA_DATA_DmaBuf ? "mmap of dma-buf failed" : "mmap failed");
		} else {
			src = (uint8_t *)map + d->mapoffset + d->chunk->offset;
		}
	} else {
		oc_set_err(g, "unsupported buffer data type");
	}
	if (src != NULL) {
		if (d->type == SPA_DATA_DmaBuf) {
			struct dma_buf_sync s = { DMA_BUF_SYNC_START | DMA_BUF_SYNC_READ };
			ioctl(d->fd, DMA_BUF_IOCTL_SYNC, &s);
		}
		size_t n = (size_t)stride * h;
		size_t avail = d->maxsize > d->chunk->offset ? d->maxsize - d->chunk->offset : 0;
		if (n > avail) n = avail;
		g->pixels = malloc((size_t)stride * h);
		memset(g->pixels, 0, (size_t)stride * h);
		memcpy(g->pixels, src, n);
		if (d->type == SPA_DATA_DmaBuf) {
			struct dma_buf_sync s = { DMA_BUF_SYNC_END | DMA_BUF_SYNC_READ };
			ioctl(d->fd, DMA_BUF_IOCTL_SYNC, &s);
		}
		g->width = w;
		g->height = h;
		g->stride = stride;
		g->data_type = d->type;
		g->state = 1;
	}
	if (map != NULL) munmap(map, maplen);
	pw_stream_queue_buffer(g->stream, b);
}

static const struct pw_stream_events oc_stream_events = {
	PW_VERSION_STREAM_EVENTS,
	.state_changed = oc_on_state_changed,
	.param_changed = oc_on_param_changed,
	.process = oc_on_process,
};

#define OC_MOD_LINEAR   1
#define OC_MOD_IMPLICIT 2
#define OC_DRM_FORMAT_MOD_INVALID 0x00ffffffffffffffULL

static const struct spa_pod *oc_build_format(struct spa_pod_builder *b, int mods) {
	struct spa_pod_frame f[2];
	spa_pod_builder_push_object(b, &f[0], SPA_TYPE_OBJECT_Format, SPA_PARAM_EnumFormat);
	spa_pod_builder_add(b, SPA_FORMAT_mediaType, SPA_POD_Id(SPA_MEDIA_TYPE_video), 0);
	spa_pod_builder_add(b, SPA_FORMAT_mediaSubtype, SPA_POD_Id(SPA_MEDIA_SUBTYPE_raw), 0);
	spa_pod_builder_add(b, SPA_FORMAT_VIDEO_format, SPA_POD_CHOICE_ENUM_Id(5,
		SPA_VIDEO_FORMAT_BGRx, SPA_VIDEO_FORMAT_BGRx, SPA_VIDEO_FORMAT_BGRA,
		SPA_VIDEO_FORMAT_RGBx, SPA_VIDEO_FORMAT_RGBA), 0);
	if (mods) {
		// LINEAR is the one layout the CPU can read after mmap. IMPLICIT
		// (DRM_FORMAT_MOD_INVALID) lets the driver pick; it is only
		// readable when the driver happens to choose a linear layout. No
		// DONT_FIXATE: we list only values we can consume, and with it niri
		// left the stream paused.
		uint64_t first = (mods & OC_MOD_LINEAR) ? 0 : OC_DRM_FORMAT_MOD_INVALID;
		spa_pod_builder_prop(b, SPA_FORMAT_VIDEO_modifier,
			SPA_POD_PROP_FLAG_MANDATORY);
		spa_pod_builder_push_choice(b, &f[1], SPA_CHOICE_Enum, 0);
		spa_pod_builder_long(b, first);
		if (mods & OC_MOD_LINEAR) spa_pod_builder_long(b, 0);
		if (mods & OC_MOD_IMPLICIT) spa_pod_builder_long(b, OC_DRM_FORMAT_MOD_INVALID);
		spa_pod_builder_pop(b, &f[1]);
	}
	spa_pod_builder_add(b, SPA_FORMAT_VIDEO_size, SPA_POD_CHOICE_RANGE_Rectangle(
		&SPA_RECTANGLE(1920, 1080), &SPA_RECTANGLE(1, 1), &SPA_RECTANGLE(16384, 16384)), 0);
	spa_pod_builder_add(b, SPA_FORMAT_VIDEO_framerate, SPA_POD_CHOICE_RANGE_Fraction(
		&SPA_FRACTION(30, 1), &SPA_FRACTION(0, 1), &SPA_FRACTION(1000, 1)), 0);
	return spa_pod_builder_pop(b, &f[0]);
}

static oc_grab *oc_grab_start(int fd, uint32_t node, int mods, char *err, size_t errlen) {
	pw_init(NULL, NULL);
	oc_grab *g = calloc(1, sizeof *g);
	g->loop = pw_thread_loop_new("openclick-grab", NULL);
	if (g->loop == NULL) { snprintf(err, errlen, "pw_thread_loop_new failed"); free(g); return NULL; }
	g->context = pw_context_new(pw_thread_loop_get_loop(g->loop), NULL, 0);
	if (g->context == NULL || pw_thread_loop_start(g->loop) < 0) {
		snprintf(err, errlen, "pw_context_new failed");
		return g;
	}
	pw_thread_loop_lock(g->loop);
	if (fd >= 0) {
		g->core = pw_context_connect_fd(g->context, fcntl(fd, F_DUPFD_CLOEXEC, 3), NULL, 0);
	} else {
		g->core = pw_context_connect(g->context, NULL, 0);
	}
	if (g->core == NULL) {
		snprintf(err, errlen, "cannot connect to PipeWire");
		pw_thread_loop_unlock(g->loop);
		return g;
	}
	pw_core_add_listener(g->core, &g->core_listener, &oc_core_events, g);
	g->stream = pw_stream_new(g->core, "openclick-grab", pw_properties_new(
		PW_KEY_MEDIA_TYPE, "Video",
		PW_KEY_MEDIA_CATEGORY, "Capture",
		PW_KEY_MEDIA_ROLE, "Screen", NULL));
	pw_stream_add_listener(g->stream, &g->stream_listener, &oc_stream_events, g);
	uint8_t buf[2048];
	struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buf, sizeof buf);
	const struct spa_pod *params[2];
	int n = 0;
	if (mods) params[n++] = oc_build_format(&b, mods);
	params[n++] = oc_build_format(&b, 0);
	int res = pw_stream_connect(g->stream, PW_DIRECTION_INPUT, node,
		PW_STREAM_FLAG_AUTOCONNECT | PW_STREAM_FLAG_MAP_BUFFERS, params, n);
	if (res < 0) snprintf(err, errlen, "pw_stream_connect: %s", spa_strerror(res));
	pw_thread_loop_unlock(g->loop);
	return g;
}

static int oc_grab_state(oc_grab *g, char *err, size_t errlen) {
	pw_thread_loop_lock(g->loop);
	int s = g->state;
	if (s < 0) snprintf(err, errlen, "%s", g->err);
	pw_thread_loop_unlock(g->loop);
	return s;
}

static void oc_grab_diag(oc_grab *g, char *out, size_t n) {
	pw_thread_loop_lock(g->loop);
	snprintf(out, n, "stream=%s formats=%d buffers=%d empty=%d corrupted=%d type=%u format=%u modifier=%#llx",
		g->stream_state ? g->stream_state : "?", g->n_format, g->n_process, g->n_empty, g->n_corrupted, g->last_type,
		g->format, (unsigned long long)g->modifier);
	pw_thread_loop_unlock(g->loop);
}

static void oc_grab_free(oc_grab *g) {
	if (g == NULL) return;
	if (g->loop) {
		pw_thread_loop_lock(g->loop);
		if (g->stream) pw_stream_destroy(g->stream);
		if (g->core) pw_core_disconnect(g->core);
		pw_thread_loop_unlock(g->loop);
		pw_thread_loop_stop(g->loop);
	}
	if (g->context) pw_context_destroy(g->context);
	if (g->loop) pw_thread_loop_destroy(g->loop);
	free(g->pixels);
	free(g);
}

static int oc_is_bgr(uint32_t f) { return f == SPA_VIDEO_FORMAT_BGRx || f == SPA_VIDEO_FORMAT_BGRA; }
static int oc_is_rgb(uint32_t f) { return f == SPA_VIDEO_FORMAT_RGBx || f == SPA_VIDEO_FORMAT_RGBA; }
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unsafe"

	"github.com/xlsft/openclick/internal/platform/pixconv"
)

const Available = true

// GrabFrame connects to PipeWire through fd (from ScreenCast
// OpenPipeWireRemote; -1 means the default daemon), consumes node and
// returns the first non-empty frame. fd stays owned by the caller.
func GrabFrame(ctx context.Context, fd int, node uint32, opts Options) (*Frame, error) {
	var errbuf [256]C.char
	mods := C.int(0)
	if opts.AllowDMABuf {
		mods |= C.OC_MOD_LINEAR
	}
	if opts.ImplicitModifier {
		mods |= C.OC_MOD_IMPLICIT
	}
	g := C.oc_grab_start(C.int(fd), C.uint32_t(node), mods, &errbuf[0], C.size_t(len(errbuf)))
	defer C.oc_grab_free(g)
	if errbuf[0] != 0 {
		return nil, fmt.Errorf("pipewire: %s", C.GoString(&errbuf[0]))
	}
	if g == nil {
		return nil, errors.New("pipewire: init failed")
	}
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		switch C.oc_grab_state(g, &errbuf[0], C.size_t(len(errbuf))) {
		case 1:
			return frameFrom(g)
		case -1:
			return nil, fmt.Errorf("pipewire: %s", C.GoString(&errbuf[0]))
		}
		select {
		case <-ctx.Done():
			C.oc_grab_diag(g, &errbuf[0], C.size_t(len(errbuf)))
			return nil, fmt.Errorf("pipewire: no frame (%s): %w", C.GoString(&errbuf[0]), ctx.Err())
		case <-tick.C:
		}
	}
}

func frameFrom(g *C.oc_grab) (*Frame, error) {
	w, h, stride := int(g.width), int(g.height), int(g.stride)
	if w <= 0 || h <= 0 || stride < w*4 || g.pixels == nil {
		return nil, errors.New("pipewire: bad frame geometry")
	}
	var swap bool
	switch {
	case C.oc_is_bgr(g.format) != 0:
		swap = true
	case C.oc_is_rgb(g.format) != 0:
	default:
		return nil, fmt.Errorf("pipewire: unexpected format %d", int(g.format))
	}
	src := unsafe.Slice((*byte)(unsafe.Pointer(g.pixels)), stride*h)
	f := &Frame{
		Image:    pixconv.BGRxToRGBA(src, stride, w, h, swap),
		Format:   formatName(uint32(g.format)),
		DataType: dataTypeName(uint32(g.data_type)),
	}
	if g.has_modifier != 0 {
		f.Modifier = uint64(g.modifier)
		f.HasModifier = true
	}
	return f, nil
}

func formatName(f uint32) string {
	switch f {
	case C.SPA_VIDEO_FORMAT_BGRx:
		return "BGRx"
	case C.SPA_VIDEO_FORMAT_BGRA:
		return "BGRA"
	case C.SPA_VIDEO_FORMAT_RGBx:
		return "RGBx"
	case C.SPA_VIDEO_FORMAT_RGBA:
		return "RGBA"
	}
	return fmt.Sprintf("spa-format-%d", f)
}

func dataTypeName(t uint32) string {
	switch t {
	case C.SPA_DATA_MemPtr:
		return "MemPtr"
	case C.SPA_DATA_MemFd:
		return "MemFd"
	case C.SPA_DATA_DmaBuf:
		return "DmaBuf"
	}
	return fmt.Sprintf("spa-data-%d", t)
}
