package tray

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestIconPNGDecodes(t *testing.T) {
	img, err := png.Decode(bytes.NewReader(iconPNG()))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != iconSize || b.Dy() != iconSize {
		t.Errorf("bounds = %v", b)
	}
}

func TestIconICOHeader(t *testing.T) {
	p := iconPNG()
	ico := iconICO(p)
	le := binary.LittleEndian
	if le.Uint16(ico[2:]) != 1 || le.Uint16(ico[4:]) != 1 {
		t.Fatalf("header = %x", ico[:6])
	}
	size, off := le.Uint32(ico[14:]), le.Uint32(ico[18:])
	if int(size) != len(p) || off != 22 || !bytes.Equal(ico[off:], p) {
		t.Errorf("entry size=%d off=%d len=%d", size, off, len(ico))
	}
}
