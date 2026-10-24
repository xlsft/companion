package displays

import (
	"bufio"
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/xlsft/openclick/pkg/contracts"
)

func TestMarshalReadRoundTrip(t *testing.T) {
	b := marshal(7, 3, uint32(1), int32(-5), "DP-1")
	// header: obj=7, size=8+4+4+(4+8)=28, op=3
	if got := order.Uint32(b[4:]); got != 28<<16|3 {
		t.Fatalf("header word %#x", got)
	}
	obj, op, body, err := readMessage(bufio.NewReader(bytesReader(b)))
	if err != nil || obj != 7 || op != 3 {
		t.Fatalf("obj=%d op=%d err=%v", obj, op, err)
	}
	r := &argReader{b: body}
	if r.u32() != 1 || r.i32() != -5 || r.str() != "DP-1" || r.err != nil || len(r.b) != 0 {
		t.Fatalf("decode failed: %+v", r)
	}
}

type byteReader struct{ b []byte }

func bytesReader(b []byte) *byteReader { return &byteReader{b} }
func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, net.ErrClosed
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

func TestReadMessageRejectsBadSize(t *testing.T) {
	bad := order.AppendUint32(nil, 1)
	bad = order.AppendUint32(bad, 4<<16) // size < header
	if _, _, _, err := readMessage(bytesReader(bad)); err == nil {
		t.Fatal("expected error")
	}
}

func TestArgReaderTruncated(t *testing.T) {
	r := &argReader{b: order.AppendUint32(nil, 99)} // string claims 99 bytes
	r.str()
	if r.err == nil {
		t.Fatal("expected error")
	}
	r = &argReader{b: []byte{1, 2}}
	r.u32()
	if r.err == nil {
		t.Fatal("expected error")
	}
}

// ev hand-encodes an event body.
func ev(obj uint32, op uint16, args ...any) (uint32, uint16, *argReader) {
	m := marshal(obj, op, args...)
	return obj, op, &argReader{b: m[8:]}
}

func apply(t *testing.T, o *output, op uint16, args ...any) {
	t.Helper()
	_, op, r := ev(1, op, args...)
	if err := o.onOutputEvent(op, r); err != nil {
		t.Fatal(err)
	}
}

func applyXDG(t *testing.T, o *output, op uint16, args ...any) {
	t.Helper()
	_, op, r := ev(1, op, args...)
	if err := o.onXDGEvent(op, r); err != nil {
		t.Fatal(err)
	}
}

func TestOutputEvents(t *testing.T) {
	tests := []struct {
		name  string
		build func(t *testing.T, o *output)
		want  contracts.Display
	}{
		{
			name: "1x full HD",
			build: func(t *testing.T, o *output) {
				apply(t, o, opOutputGeometry, int32(0), int32(0), int32(530), int32(300), int32(0), "Dell", "U2415", int32(0))
				apply(t, o, opOutputMode, uint32(0), int32(1280), int32(720), int32(60000)) // not current
				apply(t, o, opOutputMode, uint32(modeCurrent|2), int32(1920), int32(1080), int32(60000))
				apply(t, o, opOutputScale, int32(1))
				apply(t, o, opOutputName, "DP-1")
				apply(t, o, opOutputDescription, "Dell U2415")
				applyXDG(t, o, opXDGLogicalPosition, int32(0), int32(0))
				applyXDG(t, o, opXDGLogicalSize, int32(1920), int32(1080))
				apply(t, o, opOutputDone)
			},
			want: contracts.Display{ID: "DP-1", Name: "Dell U2415", Bounds: contracts.RectXYWH(0, 0, 1920, 1080),
				Native: contracts.PixelSize{W: 1920, H: 1080}, Scale: 1},
		},
		{
			name: "fractional 1.5x with rounded logical size",
			build: func(t *testing.T, o *output) {
				apply(t, o, opOutputGeometry, int32(0), int32(0), int32(0), int32(0), int32(0), "", "", int32(0))
				apply(t, o, opOutputMode, uint32(modeCurrent), int32(2560), int32(1440), int32(60000))
				apply(t, o, opOutputScale, int32(2))
				apply(t, o, opOutputName, "eDP-1")
				applyXDG(t, o, opXDGLogicalPosition, int32(1920), int32(-100))
				applyXDG(t, o, opXDGLogicalSize, int32(1707), int32(960))
				apply(t, o, opOutputDone)
			},
			want: contracts.Display{ID: "eDP-1", Name: "eDP-1", Bounds: contracts.RectXYWH(1920, -100, 1707, 960),
				Native: contracts.PixelSize{W: 2560, H: 1440}, Scale: 1.5},
		},
		{
			name: "transform 270 (CCW) is 90 clockwise and swaps native",
			build: func(t *testing.T, o *output) {
				apply(t, o, opOutputGeometry, int32(0), int32(0), int32(0), int32(0), int32(0), "", "", int32(3))
				apply(t, o, opOutputMode, uint32(modeCurrent), int32(1920), int32(1080), int32(60000))
				apply(t, o, opOutputName, "HDMI-A-1")
				applyXDG(t, o, opXDGLogicalSize, int32(1080), int32(1920))
				apply(t, o, opOutputDone)
			},
			want: contracts.Display{ID: "HDMI-A-1", Name: "HDMI-A-1", Bounds: contracts.RectXYWH(0, 0, 1080, 1920),
				Native: contracts.PixelSize{W: 1080, H: 1920}, Scale: 1, Rotation: 90},
		},
		{
			name: "flipped-90 (CCW) is 270 clockwise",
			build: func(t *testing.T, o *output) {
				apply(t, o, opOutputGeometry, int32(0), int32(0), int32(0), int32(0), int32(0), "", "", int32(5))
				apply(t, o, opOutputMode, uint32(modeCurrent), int32(100), int32(50), int32(0))
				apply(t, o, opOutputName, "X")
				apply(t, o, opOutputDone)
			},
			want: contracts.Display{ID: "X", Name: "X", Bounds: contracts.RectXYWH(0, 0, 50, 100),
				Native: contracts.PixelSize{W: 50, H: 100}, Scale: 1, Rotation: 270},
		},
		{
			name: "no xdg-output falls back to wl_output",
			build: func(t *testing.T, o *output) {
				apply(t, o, opOutputGeometry, int32(1920), int32(0), int32(0), int32(0), int32(0), "", "", int32(0))
				apply(t, o, opOutputMode, uint32(modeCurrent), int32(3840), int32(2160), int32(0))
				apply(t, o, opOutputScale, int32(2))
				apply(t, o, opOutputName, "DP-2")
				apply(t, o, opOutputDone)
			},
			want: contracts.Display{ID: "DP-2", Name: "DP-2", Bounds: contracts.RectXYWH(1920, 0, 1920, 1080),
				Native: contracts.PixelSize{W: 3840, H: 2160}, Scale: 2},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &output{factor: 1}
			tc.build(t, o)
			if !o.ready(o.hasLogical) {
				t.Fatal("output not ready")
			}
			if got := o.display(); got != tc.want {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestOutputReadiness(t *testing.T) {
	o := &output{factor: 1}
	apply(t, o, opOutputMode, uint32(modeCurrent), int32(10), int32(10), int32(0))
	if o.ready(false) {
		t.Fatal("ready before done")
	}
	apply(t, o, opOutputDone)
	if !o.ready(false) || o.ready(true) {
		t.Fatal("with xdg manager the logical size is required")
	}
	if o.display().ID != "output-0" {
		t.Fatalf("unnamed output id %q", o.display().ID)
	}
}

func TestMarkPrimary(t *testing.T) {
	ds := []contracts.Display{
		{ID: "A", Bounds: contracts.RectXYWH(1920, 0, 10, 10)},
		{ID: "B", Bounds: contracts.RectXYWH(0, 0, 10, 10)},
	}
	markPrimary(ds)
	if ds[0].Primary || !ds[1].Primary {
		t.Fatalf("%+v", ds)
	}
	ds = []contracts.Display{{ID: "A", Bounds: contracts.RectXYWH(5, 5, 10, 10)}, {ID: "B"}}
	ds[1].Bounds = contracts.RectXYWH(20, 0, 1, 1)
	markPrimary(ds)
	if !ds[0].Primary || ds[1].Primary {
		t.Fatalf("first must be primary without an origin output: %+v", ds)
	}
}

// fakeCompositor speaks just enough Wayland to drive the backend.
type fakeCompositor struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func startCompositor(t *testing.T) (*fakeCompositor, net.Conn) {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "wl"))
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	cc, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	sc, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Close() })
	return &fakeCompositor{t: t, conn: sc, r: bufio.NewReader(sc)}, cc
}

func (f *fakeCompositor) send(b ...[]byte) {
	for _, m := range b {
		if _, err := f.conn.Write(m); err != nil {
			f.t.Errorf("compositor write: %v", err)
		}
	}
}

// request reads one client request.
func (f *fakeCompositor) request() (uint32, uint16, *argReader) {
	obj, op, body, err := readMessage(f.r)
	if err != nil {
		f.t.Errorf("compositor read: %v", err)
		return 0, 0, &argReader{}
	}
	return obj, op, &argReader{b: body}
}

func (f *fakeCompositor) output(id, xdg uint32, name string, x, w, h, lw, lh int32) {
	f.send(
		marshal(id, opOutputGeometry, x, int32(0), int32(0), int32(0), int32(0), "", "", int32(0)),
		marshal(id, opOutputMode, uint32(modeCurrent), w, h, int32(60000)),
		marshal(id, opOutputScale, int32(1)),
		marshal(id, opOutputName, name),
		marshal(xdg, opXDGLogicalPosition, x, int32(0)),
		marshal(xdg, opXDGLogicalSize, lw, lh),
		marshal(id, opOutputDone),
	)
}

func TestBackendEndToEnd(t *testing.T) {
	comp, cc := startCompositor(t)
	done := make(chan *Backend, 1)
	go func() {
		b, err := newBackend(context.Background(), cc)
		if err != nil {
			t.Error(err)
		}
		done <- b
	}()

	// Handshake: get_registry, sync.
	if obj, op, r := comp.request(); obj != idDisplay || op != 1 || r.u32() != idRegistry {
		t.Fatal("expected get_registry")
	}
	if obj, op, r := comp.request(); obj != idDisplay || op != 0 || r.u32() != idSync {
		t.Fatal("expected sync")
	}
	comp.send(
		marshal(idRegistry, opRegistryGlobal, uint32(10), "wl_compositor", uint32(6)),
		marshal(idRegistry, opRegistryGlobal, uint32(11), "wl_output", uint32(4)),
		marshal(idRegistry, opRegistryGlobal, uint32(12), "zxdg_output_manager_v1", uint32(3)),
		marshal(idSync, opCallbackDone, uint32(0)),
	)
	bindID := func(wantName uint32, wantIface string) uint32 {
		obj, op, r := comp.request()
		name, iface, ver, id := r.u32(), r.str(), r.u32(), r.u32()
		if obj != idRegistry || op != opRegistryBind || name != wantName || iface != wantIface || ver == 0 {
			t.Fatalf("bind: obj=%d op=%d name=%d iface=%q ver=%d", obj, op, name, iface, ver)
		}
		return id
	}
	out1 := bindID(11, "wl_output")
	mgr := bindID(12, "zxdg_output_manager_v1")
	obj, op, r := comp.request() // get_xdg_output(id, output)
	xdg1 := r.u32()
	if obj != mgr || op != opXDGManagerGetOutput || r.u32() != out1 {
		t.Fatal("expected get_xdg_output")
	}
	_, _, r = comp.request() // second sync
	cb := r.u32()
	comp.output(out1, xdg1, "DP-1", 0, 1920, 1080, 1920, 1080)
	comp.send(marshal(cb, opCallbackDone, uint32(0)))

	b := <-done
	if b == nil {
		t.Fatal("no backend")
	}
	defer b.Close()
	ds, err := b.Displays()
	if err != nil || len(ds) != 1 || ds[0].ID != "DP-1" || !ds[0].Primary {
		t.Fatalf("initial: %+v %v", ds, err)
	}

	// Hotplug a second output.
	comp.send(marshal(idRegistry, opRegistryGlobal, uint32(13), "wl_output", uint32(4)))
	out2 := bindID(13, "wl_output")
	_, _, r = comp.request()
	xdg2 := r.u32()
	comp.output(out2, xdg2, "HDMI-A-1", 1920, 1280, 720, 1280, 720)
	waitFor(t, b, func(ds []contracts.Display) bool { return len(ds) == 2 && !ds[1].Primary })

	// Unplug it again; the client must release both objects.
	comp.send(marshal(idRegistry, opRegistryGlobalRemove, uint32(13)))
	if obj, op, _ := comp.request(); obj != xdg2 || op != opXDGOutputDestroy {
		t.Fatalf("expected xdg destroy, got obj=%d op=%d", obj, op)
	}
	if obj, op, _ := comp.request(); obj != out2 || op != opOutputRelease {
		t.Fatalf("expected release, got obj=%d op=%d", obj, op)
	}
	waitFor(t, b, func(ds []contracts.Display) bool { return len(ds) == 1 })

	// Mode change on the first output is a change signal too.
	drain(b)
	comp.output(out1, xdg1, "DP-1", 0, 2560, 1440, 1280, 720)
	waitFor(t, b, func(ds []contracts.Display) bool { return ds[0].Scale == 2 })

	// Connection loss closes Changed and fails Displays.
	comp.conn.Close()
	select {
	case <-b.done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not stop")
	}
	if _, err := b.Displays(); err == nil {
		t.Fatal("expected error after disconnect")
	}
}

func drain(b *Backend) {
	select {
	case <-b.changed:
	default:
	}
}

func waitFor(t *testing.T, b *Backend, ok func([]contracts.Display) bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		ds, err := b.Displays()
		if err == nil && ok(ds) {
			return
		}
		select {
		case <-b.changed:
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatalf("condition not met, displays: %+v err=%v", ds, err)
		}
	}
}

func TestProtocolErrorFailsHandshake(t *testing.T) {
	comp, cc := startCompositor(t)
	errc := make(chan error, 1)
	go func() { _, err := newBackend(context.Background(), cc); errc <- err }()
	comp.request()
	comp.request()
	comp.send(marshal(idDisplay, opDisplayError, uint32(2), uint32(1), "nope"))
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("expected error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handshake hung")
	}
}
