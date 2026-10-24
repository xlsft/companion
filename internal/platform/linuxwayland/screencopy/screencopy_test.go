//go:build linux

package screencopy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"net"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xlsft/openclick/pkg/contracts"
)

// fakeCompositor speaks just enough Wayland to serve screencopy frames from
// a fixed pixel buffer. Requests are hand-decoded, events hand-encoded.
type fakeCompositor struct {
	t    *testing.T
	conn *net.UnixConn

	withManager bool
	format      uint32 // offered for every frame
	w, h, strd  int
	yInvert     bool
	pixels      []byte // what copy writes into the client's buffer
	failNext    bool

	mu       sync.Mutex
	ifaces   map[uint32]string // client object id -> interface
	fds      []int
	pools    map[uint32]int    // pool id -> fd
	buffers  map[uint32]int    // buffer id -> fd
	regions  [][4]int32        // capture_output_region args
	captured map[uint32]uint32 // frame -> output
}

func (f *fakeCompositor) send(obj uint32, op uint16, args ...any) {
	var body []byte
	for _, a := range args {
		switch v := a.(type) {
		case uint32:
			body = binary.LittleEndian.AppendUint32(body, v)
		case int32:
			body = binary.LittleEndian.AppendUint32(body, uint32(v))
		case string:
			body = binary.LittleEndian.AppendUint32(body, uint32(len(v)+1))
			body = append(body, v...)
			body = append(body, 0)
			for len(body)%4 != 0 {
				body = append(body, 0)
			}
		}
	}
	msg := binary.LittleEndian.AppendUint32(nil, obj)
	msg = binary.LittleEndian.AppendUint32(msg, uint32(8+len(body))<<16|uint32(op))
	if _, err := f.conn.Write(append(msg, body...)); err != nil {
		f.t.Logf("compositor write: %v", err)
	}
}

func (f *fakeCompositor) run() {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	oob := make([]byte, syscall.CmsgSpace(4*4))
	for {
		n, oobn, _, _, err := f.conn.ReadMsgUnix(tmp, oob)
		if err != nil {
			return
		}
		if oobn > 0 {
			msgs, _ := syscall.ParseSocketControlMessage(oob[:oobn])
			for _, m := range msgs {
				fds, _ := syscall.ParseUnixRights(&m)
				f.fds = append(f.fds, fds...)
			}
		}
		buf = append(buf, tmp[:n]...)
		for len(buf) >= 8 {
			size := int(binary.LittleEndian.Uint32(buf[4:]) >> 16)
			if len(buf) < size {
				break
			}
			obj := binary.LittleEndian.Uint32(buf)
			op := uint16(binary.LittleEndian.Uint32(buf[4:]))
			f.request(obj, op, buf[8:size])
			buf = buf[size:]
		}
	}
}

func u32(b []byte, i int) uint32 { return binary.LittleEndian.Uint32(b[4*i:]) }

func (f *fakeCompositor) request(obj uint32, op uint16, b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	iface := f.ifaces[obj]
	switch {
	case obj == 1 && op == 1: // get_registry
		reg := u32(b, 0)
		f.ifaces[reg] = "wl_registry"
		f.send(reg, opRegGlobal, uint32(1), "wl_shm", uint32(1))
		if f.withManager {
			f.send(reg, opRegGlobal, uint32(2), ifaceManager, uint32(3))
		}
		f.send(reg, opRegGlobal, uint32(3), "wl_output", uint32(4))
		f.send(reg, opRegGlobal, uint32(4), "wl_compositor", uint32(6))
	case obj == 1 && op == 0: // sync
		f.send(u32(b, 0), 0, uint32(0))
	case iface == "wl_registry" && op == opRegistryBind:
		n := int(u32(b, 1))
		rest := b[8+(n+3)&^3:]
		id := binary.LittleEndian.Uint32(rest[4:])
		ifaceName := string(b[8 : 8+n-1])
		f.ifaces[id] = ifaceName
		switch ifaceName {
		case "wl_shm":
			f.send(id, opShmFormat, FormatARGB8888)
			f.send(id, opShmFormat, FormatXRGB8888)
			f.send(id, opShmFormat, FormatXBGR8888)
		case "wl_output":
			f.send(id, opOutputName, "DP-1")
		}
	case iface == "wl_shm" && op == opShmCreatePool:
		f.ifaces[u32(b, 0)] = "wl_shm_pool"
		f.pools[u32(b, 0)] = f.fds[0]
		f.fds = f.fds[1:]
	case iface == "wl_shm_pool" && op == opPoolCreateBuffer:
		f.ifaces[u32(b, 0)] = "wl_buffer"
		f.buffers[u32(b, 0)] = f.pools[obj]
	case iface == "wl_shm_pool" && op == opPoolDestroy:
		delete(f.pools, obj) // the buffer keeps the fd
	case iface == ifaceManager && (op == opMgrCaptureOutput || op == opMgrCaptureOutputRegion):
		frame := u32(b, 0)
		f.ifaces[frame] = "frame"
		f.captured[frame] = u32(b, 2)
		if op == opMgrCaptureOutputRegion {
			f.regions = append(f.regions, [4]int32{int32(u32(b, 3)), int32(u32(b, 4)), int32(u32(b, 5)), int32(u32(b, 6))})
		}
		if f.failNext {
			f.failNext = false
			f.send(frame, evFrameFailed)
			return
		}
		// A DMA-BUF offer must be ignored in favour of the SHM one.
		f.send(frame, evFrameLinuxDmabuf, uint32(0x34325258), uint32(f.w), uint32(f.h))
		f.send(frame, evFrameBuffer, f.format, uint32(f.w), uint32(f.h), uint32(f.strd))
		f.send(frame, evFrameBufferDone)
	case iface == "frame" && op == opFrameCopy:
		fd := f.buffers[u32(b, 0)]
		mem, err := unix.Mmap(fd, 0, len(f.pixels), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil {
			f.t.Errorf("compositor mmap: %v", err)
			f.send(obj, evFrameFailed)
			return
		}
		copy(mem, f.pixels)
		_ = unix.Munmap(mem)
		var flags uint32
		if f.yInvert {
			flags = flagYInvert
		}
		f.send(obj, evFrameFlags, flags)
		f.send(obj, evFrameReady, uint32(0), uint32(1), uint32(0))
	case iface == "frame" && op == opFrameDestroy, iface == "wl_buffer" && op == opBufferDestroy:
		if fd, ok := f.buffers[obj]; ok {
			syscall.Close(fd)
			delete(f.buffers, obj)
		}
		delete(f.ifaces, obj)
		f.send(1, 1, obj) // delete_id
	}
}

func startFake(t *testing.T, f *fakeCompositor) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wayland-test")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	t.Setenv("WAYLAND_SOCKET", "")
	t.Setenv("WAYLAND_DISPLAY", path)
	f.t = t
	f.ifaces = map[uint32]string{}
	f.pools = map[uint32]int{}
	f.buffers = map[uint32]int{}
	f.captured = map[uint32]uint32{}
	go func() {
		c, err := ln.AcceptUnix()
		if err != nil {
			return
		}
		f.conn = c
		t.Cleanup(func() { c.Close() })
		f.run()
	}()
}

func TestCaptureEndToEnd(t *testing.T) {
	// 2x2 XBGR (R,G,B,x in memory), stride padded to 12.
	px := []byte{
		1, 2, 3, 0, 4, 5, 6, 0, 0, 0, 0, 0,
		7, 8, 9, 0, 10, 11, 12, 0, 0, 0, 0, 0,
	}
	f := &fakeCompositor{withManager: true, format: FormatXBGR8888, w: 2, h: 2, strd: 12, yInvert: true, pixels: px}
	startFake(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if got := b.Outputs(); len(got) != 1 || got[0] != "DP-1" {
		t.Fatalf("outputs = %v", got)
	}

	fr, err := b.Capture(ctx, "DP-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{7, 8, 9, 255, 10, 11, 12, 255, 1, 2, 3, 255, 4, 5, 6, 255} // y-inverted
	if !bytes.Equal(fr.Image.Pix, want) || !fr.YInvert || fr.Format != FormatXBGR8888 || fr.Ready.IsZero() {
		t.Fatalf("frame = %+v pix %v", fr, fr.Image.Pix)
	}

	// Region capture sends the output-local box.
	if _, err := b.Capture(ctx, "DP-1", &image.Rectangle{Min: image.Pt(10, 20), Max: image.Pt(12, 22)}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	regions := f.regions
	f.failNext = true
	f.mu.Unlock()
	if len(regions) != 1 || regions[0] != [4]int32{10, 20, 2, 2} {
		t.Fatalf("regions = %v", regions)
	}

	if _, err := b.Capture(ctx, "DP-1", nil); !errors.Is(err, ErrFailed) {
		t.Fatalf("failed frame: err = %v", err)
	}
	if _, err := b.Capture(ctx, "HDMI-A-1", nil); err == nil {
		t.Fatal("unknown output must fail")
	}

	// Frames and buffers are destroyed after each capture.
	if err := b.c.Roundtrip(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, iface := range f.ifaces {
		if iface == "frame" || iface == "wl_buffer" {
			t.Errorf("leaked %s %d", iface, id)
		}
	}
}

func TestCaptureCancel(t *testing.T) {
	f := &fakeCompositor{withManager: true, format: FormatXRGB8888, w: 1, h: 1, strd: 4, pixels: make([]byte, 4)}
	startFake(t, f)
	b, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Capture(ctx, "DP-1", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenWithoutManager(t *testing.T) {
	startFake(t, &fakeCompositor{})
	_, err := Open(context.Background())
	if !errors.Is(err, contracts.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
