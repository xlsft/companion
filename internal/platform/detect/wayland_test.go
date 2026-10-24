package detect

import (
	"bytes"
	"testing"
)

// globalEvent hand-encodes wl_registry.global the way a compositor would.
func globalEvent(name uint32, iface string, version uint32) []byte {
	s := append([]byte(iface), 0)
	for len(s)%4 != 0 {
		s = append(s, 0)
	}
	size := 8 + 4 + 4 + len(s) + 4
	var b []byte
	b = order.AppendUint32(b, idRegistry)
	b = order.AppendUint32(b, uint32(size)<<16|opRegistryGlobal)
	b = order.AppendUint32(b, name)
	b = order.AppendUint32(b, uint32(len(iface)+1))
	b = append(b, s...)
	return order.AppendUint32(b, version)
}

func doneEvent() []byte {
	b := order.AppendUint32(nil, idCallback)
	return order.AppendUint32(b, 12<<16|opCallbackDone) // done carries callback_data
}

func TestParseRegistry(t *testing.T) {
	done := append(doneEvent(), 0, 0, 0, 0)
	var errMsg []byte
	errMsg = order.AppendUint32(errMsg, idDisplay)
	errMsg = order.AppendUint32(errMsg, 8<<16|opDisplayError)

	tests := []struct {
		name    string
		in      []byte
		want    []Global
		wantErr bool
	}{
		{"two globals", bytes.Join([][]byte{
			globalEvent(1, "wl_compositor", 6), globalEvent(7, "zwlr_layer_shell_v1", 4), done}, nil),
			[]Global{{1, "wl_compositor", 6}, {7, "zwlr_layer_shell_v1", 4}}, false},
		{"aligned name (len+NUL multiple of 4)", bytes.Join([][]byte{globalEvent(2, "wl_shm_xyz", 1), done}, nil),
			[]Global{{2, "wl_shm_xyz", 1}}, false},
		{"empty", done, nil, false},
		{"display error", errMsg, nil, true},
		{"truncated", globalEvent(1, "wl_compositor", 6)[:10], nil, true},
		{"no done", globalEvent(1, "wl_compositor", 6), []Global{{1, "wl_compositor", 6}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRegistry(bytes.NewReader(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] got %v want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestRequestRegistry(t *testing.T) {
	var want []byte
	want = order.AppendUint32(want, 1)
	want = order.AppendUint32(want, 12<<16|1)
	want = order.AppendUint32(want, 2)
	want = order.AppendUint32(want, 1)
	want = order.AppendUint32(want, 12<<16|0)
	want = order.AppendUint32(want, 3)
	if got := requestRegistry(); !bytes.Equal(got, want) {
		t.Fatalf("got % x want % x", got, want)
	}
}

func TestWaylandSocketPath(t *testing.T) {
	if got := waylandSocketPath("wayland-1", "/run/user/1000"); got != "/run/user/1000/wayland-1" {
		t.Error(got)
	}
	if got := waylandSocketPath("/tmp/sock", "/run/user/1000"); got != "/tmp/sock" {
		t.Error(got)
	}
}
