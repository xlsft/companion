//go:build linux

// Package screencopy captures Wayland outputs with zwlr_screencopy_manager_v1
// (niri, Sway, Hyprland and other wlroots-style compositors) into wl_shm
// buffers. Unlike the ScreenCast portal it needs no consent dialog and no
// GPU readback, which makes it the first choice where it exists (OC-40).
package screencopy

import (
	"context"
	"errors"
	"fmt"
	"image"
	"strconv"
	"sync"
	"time"

	"github.com/xlsft/openclick/internal/platform/linuxwayland/wl"
	"github.com/xlsft/openclick/pkg/contracts"
)

// Name is the capability backend label.
const Name = "wlr-screencopy"

const (
	ifaceManager = "zwlr_screencopy_manager_v1"
	maxMgrVer    = 3
	maxOutputVer = 4

	opRegistryBind = 0
	opRegGlobal    = 0
	opRegRemove    = 1

	opShmCreatePool    = 0
	opPoolCreateBuffer = 0
	opPoolDestroy      = 1
	opBufferDestroy    = 0
	opShmFormat        = 0 // event

	opOutputRelease = 0 // request, v3
	opOutputName    = 4 // event, v4

	opMgrCaptureOutput       = 0
	opMgrCaptureOutputRegion = 1
	opMgrDestroy             = 2

	opFrameCopy    = 0
	opFrameDestroy = 1

	evFrameBuffer      = 0
	evFrameFlags       = 1
	evFrameReady       = 2
	evFrameFailed      = 3
	evFrameDamage      = 4
	evFrameLinuxDmabuf = 5
	evFrameBufferDone  = 6

	flagYInvert = 1
)

// ErrFailed is the compositor's zwlr_screencopy_frame_v1.failed.
var ErrFailed = errors.New("screencopy: compositor failed the capture")

// Backend owns one Wayland connection used for all captures.
type Backend struct {
	c        *wl.Conn
	registry uint32
	shm      uint32
	mgr      uint32
	mgrVer   uint32

	mu       sync.Mutex
	initDone bool               // registry globals after this are hotplugs
	outputs  map[uint32]*output // by registry name
	formats  map[uint32]bool    // wl_shm formats the compositor accepts
}

type output struct {
	global, id uint32
	version    uint32
	name       string
}

// id matches the DisplayID the Wayland displays backend reports, which
// falls back to the registry name when wl_output has no name (v < 4).
func (o *output) displayID() string {
	if o.name != "" {
		return o.name
	}
	return "output-" + strconv.FormatUint(uint64(o.global), 10)
}

// Open connects to $WAYLAND_DISPLAY. It returns Unsupported(CapScreenCapture)
// when the compositor has no screencopy manager or no wl_shm.
func Open(ctx context.Context) (*Backend, error) {
	c, err := wl.Connect()
	if err != nil {
		return nil, &contracts.CapabilityError{Capability: contracts.CapScreenCapture, Err: err}
	}
	b := &Backend{c: c, outputs: map[uint32]*output{}, formats: map[uint32]bool{}}
	if err := b.init(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return b, nil
}

type global struct {
	name    uint32
	iface   string
	version uint32
}

func (b *Backend) init(ctx context.Context) error {
	var globals []global
	b.registry = b.c.NewObject(func(op uint16, ev *wl.Event) {
		switch op {
		case opRegGlobal:
			g := global{ev.Uint(), ev.String(), ev.Uint()}
			b.mu.Lock()
			hotplug := b.initDone
			if !hotplug {
				globals = append(globals, g)
			}
			b.mu.Unlock()
			if hotplug && g.iface == "wl_output" {
				b.bindOutput(g)
			}
		case opRegRemove:
			b.removeOutput(ev.Uint())
		}
	})
	if err := b.c.Send(wl.DisplayID, 1, wl.NewMsg().NewID(b.registry)); err != nil {
		return err
	}
	if err := b.c.Roundtrip(ctx); err != nil {
		return err
	}
	// Binding the initial globals here rather than in the handler lets us
	// pick one wl_shm and one manager before anything is sent.
	b.mu.Lock()
	b.initDone = true
	initial := globals
	b.mu.Unlock()
	for _, g := range initial {
		switch g.iface {
		case "wl_shm":
			if b.shm == 0 {
				b.shm = b.bind(g, 1, b.handleShm)
			}
		case ifaceManager:
			if b.mgr == 0 {
				b.mgrVer = min(g.version, maxMgrVer)
				b.mgr = b.bind(g, b.mgrVer, nil)
			}
		case "wl_output":
			b.bindOutput(g)
		}
	}
	if b.mgr == 0 || b.shm == 0 {
		return &contracts.CapabilityError{
			Capability: contracts.CapScreenCapture,
			Err:        fmt.Errorf("%w: compositor has no %s", contracts.ErrUnsupported, ifaceManager),
		}
	}
	// Second roundtrip delivers wl_shm formats and output names.
	return b.c.Roundtrip(ctx)
}

func (b *Backend) bind(g global, version uint32, h wl.Handler) uint32 {
	id := b.c.NewObject(h)
	_ = b.c.Send(b.registry, opRegistryBind, wl.NewMsg().Uint(g.name).String(g.iface).Uint(version).NewID(id))
	return id
}

func (b *Backend) bindOutput(g global) {
	o := &output{global: g.name, version: min(g.version, maxOutputVer)}
	b.mu.Lock()
	b.outputs[g.name] = o
	b.mu.Unlock()
	o.id = b.bind(g, o.version, func(op uint16, ev *wl.Event) {
		if op == opOutputName {
			name := ev.String()
			b.mu.Lock()
			o.name = name
			b.mu.Unlock()
		}
	})
}

func (b *Backend) removeOutput(gname uint32) {
	b.mu.Lock()
	o, ok := b.outputs[gname]
	delete(b.outputs, gname)
	b.mu.Unlock()
	if !ok {
		return
	}
	if o.version >= 3 {
		_ = b.c.Send(o.id, opOutputRelease, nil)
	}
	b.c.Forget(o.id)
}

func (b *Backend) handleShm(op uint16, ev *wl.Event) {
	if op == opShmFormat {
		f := ev.Uint()
		b.mu.Lock()
		b.formats[f] = true
		b.mu.Unlock()
	}
}

// Close releases the connection. Captures in flight fail.
func (b *Backend) Close() error {
	if b.mgr != 0 {
		_ = b.c.Send(b.mgr, opMgrDestroy, nil)
	}
	return b.c.Close()
}

// Outputs lists the display IDs this connection can capture.
func (b *Backend) Outputs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.outputs))
	for _, o := range b.outputs {
		out = append(out, o.displayID())
	}
	return out
}

func (b *Backend) outputFor(display string) (*output, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, o := range b.outputs {
		if o.displayID() == display {
			return o, true
		}
	}
	return nil, false
}

// Frame is one captured buffer, converted to RGBA in buffer orientation
// (the output's transform is not undone).
type Frame struct {
	Image   *image.RGBA
	Format  uint32
	YInvert bool
	// Ready is when the compositor reported the copy complete.
	Ready time.Time
}

// frameState collects the events of one zwlr_screencopy_frame_v1. It is
// written on the read goroutine and read after a channel handoff.
type frameState struct {
	mu             sync.Mutex
	format         uint32
	w, h, stride   int
	haveShm        bool
	flags          uint32
	bufferInfo     chan struct{} // closed on buffer (v1-2) or buffer_done (v3)
	done           chan struct{} // closed on ready or failed
	failed         bool
	infoOnce       sync.Once
	doneOnce       sync.Once
	readyAt        time.Time
	waitBufferDone bool
}

func (f *frameState) handle(op uint16, ev *wl.Event) {
	switch op {
	case evFrameBuffer:
		format, w, h, stride := ev.Uint(), ev.Uint(), ev.Uint(), ev.Uint()
		f.mu.Lock()
		// Prefer a format we convert; a later wl_shm offer never replaces
		// an earlier usable one.
		if !f.haveShm || (!Supported(f.format) && Supported(format)) {
			f.format, f.w, f.h, f.stride = format, int(w), int(h), int(stride)
			f.haveShm = true
		}
		f.mu.Unlock()
		if !f.waitBufferDone {
			f.infoOnce.Do(func() { close(f.bufferInfo) })
		}
	case evFrameBufferDone:
		f.infoOnce.Do(func() { close(f.bufferInfo) })
	case evFrameFlags:
		fl := ev.Uint()
		f.mu.Lock()
		f.flags = fl
		f.mu.Unlock()
	case evFrameReady:
		f.mu.Lock()
		f.readyAt = time.Now()
		f.mu.Unlock()
		f.doneOnce.Do(func() { close(f.done) })
	case evFrameFailed:
		f.mu.Lock()
		f.failed = true
		f.mu.Unlock()
		f.infoOnce.Do(func() { close(f.bufferInfo) })
		f.doneOnce.Do(func() { close(f.done) })
	case evFrameDamage, evFrameLinuxDmabuf:
		// SHM only: DMA-BUF would need a GPU import (see the OC-6 spike).
	}
}

// Capture copies one frame of the output with the given display ID. A
// non-nil region is in output-local logical coordinates and selects
// capture_output_region; the compositor clips it to the output.
func (b *Backend) Capture(ctx context.Context, display string, region *image.Rectangle) (*Frame, error) {
	o, ok := b.outputFor(display)
	if !ok {
		return nil, fmt.Errorf("screencopy: unknown output %q", display)
	}
	if err := b.c.Err(); err != nil {
		return nil, err
	}
	fs := &frameState{
		bufferInfo:     make(chan struct{}),
		done:           make(chan struct{}),
		waitBufferDone: b.mgrVer >= 3,
	}
	frame := b.c.NewObject(fs.handle)
	defer func() {
		_ = b.c.Send(frame, opFrameDestroy, nil)
		b.c.Forget(frame)
	}()
	// overlay_cursor=0: the pointer is not part of what the user asked to
	// show, and it would hide what is under it.
	var err error
	if region == nil {
		err = b.c.Send(b.mgr, opMgrCaptureOutput, wl.NewMsg().NewID(frame).Int(0).Object(o.id))
	} else {
		r := *region
		err = b.c.Send(b.mgr, opMgrCaptureOutputRegion, wl.NewMsg().NewID(frame).Int(0).Object(o.id).
			Int(int32(r.Min.X)).Int(int32(r.Min.Y)).Int(int32(r.Dx())).Int(int32(r.Dy())))
	}
	if err != nil {
		return nil, err
	}
	if err := b.wait(ctx, fs.bufferInfo); err != nil {
		return nil, err
	}
	fs.mu.Lock()
	failed, haveShm := fs.failed, fs.haveShm
	format, w, h, stride := fs.format, fs.w, fs.h, fs.stride
	fs.mu.Unlock()
	switch {
	case failed:
		return nil, ErrFailed
	case !haveShm:
		return nil, errors.New("screencopy: compositor offered no wl_shm buffer")
	case !Supported(format):
		return nil, fmt.Errorf("screencopy: unsupported format %s", FormatName(format))
	case w <= 0 || h <= 0 || stride < w*4:
		return nil, fmt.Errorf("screencopy: bad buffer geometry %dx%d stride %d", w, h, stride)
	}
	b.mu.Lock()
	advertised := b.formats[format]
	b.mu.Unlock()
	if !advertised && format != FormatARGB8888 && format != FormatXRGB8888 {
		// The two base formats are mandatory for every wl_shm.
		return nil, fmt.Errorf("screencopy: format %s not offered by wl_shm", FormatName(format))
	}

	size := stride * h
	shm, err := wl.NewShmFile(size)
	if err != nil {
		return nil, err
	}
	defer shm.Close()
	pool := b.c.NewObject(nil)
	buf := b.c.NewObject(nil)
	defer func() {
		_ = b.c.Send(buf, opBufferDestroy, nil)
		b.c.Forget(buf)
	}()
	if err := b.c.Send(b.shm, opShmCreatePool, wl.NewMsg().NewID(pool).Fd(shm.Fd).Int(int32(size))); err != nil {
		return nil, err
	}
	if err := b.c.Send(pool, opPoolCreateBuffer, wl.NewMsg().NewID(buf).Int(0).
		Int(int32(w)).Int(int32(h)).Int(int32(stride)).Uint(format)); err != nil {
		return nil, err
	}
	// The buffer keeps the pool's memory alive.
	_ = b.c.Send(pool, opPoolDestroy, nil)
	b.c.Forget(pool)
	if err := b.c.Send(frame, opFrameCopy, wl.NewMsg().Object(buf)); err != nil {
		return nil, err
	}
	if err := b.wait(ctx, fs.done); err != nil {
		return nil, err
	}
	fs.mu.Lock()
	failed, flags, readyAt := fs.failed, fs.flags, fs.readyAt
	fs.mu.Unlock()
	if failed {
		return nil, ErrFailed
	}
	yInvert := flags&flagYInvert != 0
	img, err := Convert(shm.Data, format, w, h, stride, yInvert)
	if err != nil {
		return nil, err
	}
	return &Frame{Image: img, Format: format, YInvert: yInvert, Ready: readyAt}, nil
}

func (b *Backend) wait(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-b.c.Done():
		return b.c.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
