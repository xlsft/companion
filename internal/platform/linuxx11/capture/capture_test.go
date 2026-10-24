package capture

import (
	"bytes"
	"testing"
)

func TestConvert(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		lsbFirst bool
		want     []byte
	}{
		{"LSBFirst is B,G,R,x", []byte{3, 2, 1, 0, 6, 5, 4, 9}, true, []byte{1, 2, 3, 255, 4, 5, 6, 255}},
		{"MSBFirst is x,R,G,B", []byte{0, 1, 2, 3, 9, 4, 5, 6}, false, []byte{1, 2, 3, 255, 4, 5, 6, 255}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img, err := Convert(tt.data, 2, 1, tt.lsbFirst)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(img.Pix, tt.want) {
				t.Fatalf("got %v want %v", img.Pix, tt.want)
			}
		})
	}
	if _, err := Convert(make([]byte, 7), 2, 1, true); err == nil {
		t.Fatal("short data must fail")
	}
}
