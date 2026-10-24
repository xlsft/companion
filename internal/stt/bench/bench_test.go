package bench

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Hello, World!", "hello world"},
		{"  Don't  stop—now ", "dont stop now"},
		{"Ёлка, ёж.", "елка еж"},
		{"one-hundred percent", "one hundred percent"},
		{"Привет, МИР", "привет мир"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestScore(t *testing.T) {
	tests := []struct {
		name, ref, hyp string
		wordEdits      int
		wordRef        int
		charEdits      int
	}{
		{"exact ignoring case and punctuation", "The cat sat.", "the cat sat", 0, 3, 0},
		{"one substitution", "the cat sat", "the cap sat", 1, 3, 1},
		{"deletion", "the cat sat", "the sat", 1, 3, 4},
		{"insertion", "a b", "a x b", 1, 2, 2},
		{"empty hypothesis", "a b c", "", 3, 3, 5},
		{"russian yo fold", "Всё хорошо", "все хорошо", 0, 2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Score(tt.ref, tt.hyp)
			if e.WordEdits != tt.wordEdits || e.WordRef != tt.wordRef || e.CharEdits != tt.charEdits {
				t.Fatalf("got %+v", e)
			}
		})
	}
}

func TestErrorCountsAggregate(t *testing.T) {
	var e ErrorCounts
	e.Add(Score("a b c d", "a b c x")) // 1/4
	e.Add(Score("e f", "e f"))         // 0/2
	if got := e.WER(); math.Abs(got-1.0/6) > 1e-9 {
		t.Fatalf("corpus WER weighs by words: got %v", got)
	}
	if (ErrorCounts{}).WER() != 0 {
		t.Fatal("empty reference must not divide by zero")
	}
}

func TestPercentile(t *testing.T) {
	xs := []float64{40, 10, 30, 20}
	tests := []struct {
		p    float64
		want float64
	}{{0, 10}, {50, 25}, {100, 40}, {95, 38.5}}
	for _, tt := range tests {
		if got := Percentile(xs, tt.p); math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("p%v = %v, want %v", tt.p, got, tt.want)
		}
	}
	if Percentile(nil, 50) != 0 {
		t.Fatal("empty sample")
	}
	if xs[0] != 40 {
		t.Fatal("input must not be reordered")
	}
}

func wav(format, channels uint16, rate uint32, bits uint16, data []byte) []byte {
	var b []byte
	le := binary.LittleEndian
	b = append(b, "RIFF\x00\x00\x00\x00WAVEfmt "...)
	b = le.AppendUint32(b, 16)
	b = le.AppendUint16(b, format)
	b = le.AppendUint16(b, channels)
	b = le.AppendUint32(b, rate)
	b = le.AppendUint32(b, rate*uint32(channels)*uint32(bits)/8)
	b = le.AppendUint16(b, channels*bits/8)
	b = le.AppendUint16(b, bits)
	b = append(b, "data"...)
	b = le.AppendUint32(b, uint32(len(data)))
	return append(b, data...)
}

func f32(vs ...float32) []byte {
	var b []byte
	for _, v := range vs {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(v))
	}
	return b
}

func i16(vs ...int16) []byte {
	var b []byte
	for _, v := range vs {
		b = binary.LittleEndian.AppendUint16(b, uint16(v))
	}
	return b
}

func TestParseWAV(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		peak float64
		want []int16
		err  string
	}{
		{"pcm16", wav(1, 1, 16000, 16, i16(0, 1000, -1000)), 0, []int16{0, 1000, -1000}, ""},
		{"float32", wav(3, 1, 16000, 32, f32(0, 0.5, -0.5)), 0, []int16{0, 16384, -16384}, ""},
		{"peak normalise boosts quiet clip", wav(3, 1, 16000, 32, f32(0.001, -0.002)), 0.5, []int16{8192, -16384}, ""},
		{"silence stays silent", wav(3, 1, 16000, 32, f32(0, 0)), 0.9, []int16{0, 0}, ""},
		{"stereo rejected", wav(1, 2, 16000, 16, i16(1, 2)), 0, nil, "mono"},
		{"44.1k rejected", wav(1, 1, 44100, 16, i16(1)), 0, nil, "16 kHz"},
		{"24-bit rejected", wav(1, 1, 16000, 24, []byte{1, 2, 3}), 0, nil, "unsupported"},
		{"garbage", []byte("nope"), 0, nil, "RIFF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseWAV(tt.in, tt.peak)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want containing %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v want %v", got, tt.want)
				}
			}
		})
	}
}

func TestParseVmHWM(t *testing.T) {
	tests := []struct {
		name, in string
		mb       float64
		ok       bool
	}{
		{"typical", "Name:\tstt\nVmHWM:\t  204800 kB\nVmRSS:\t100 kB\n", 200, true},
		{"missing", "Name:\tstt\n", 0, false},
		{"garbage", "VmHWM:\tabc kB\n", 0, false},
	}
	for _, tt := range tests {
		mb, ok := parseVmHWM(tt.in)
		if ok != tt.ok || mb != tt.mb {
			t.Errorf("%s: got %v,%v", tt.name, mb, ok)
		}
	}
	if _, ok := PeakRSSMB(0); ok {
		t.Fatal("pid 0 has no RSS")
	}
}

func TestLoadCorpus(t *testing.T) {
	dir := t.TempDir()
	clip := wav(1, 1, 16000, 16, i16(1, 2, 3, 4))
	if err := os.WriteFile(filepath.Join(dir, "a.wav"), clip, 0o644); err != nil {
		t.Fatal(err)
	}
	tsv := "en\ta.wav\tHello there\nen\ta.wav\tsecond\nru\ta.wav\tПривет\n\nfr\ta.wav\tbonjour\n"
	if err := os.WriteFile(filepath.Join(dir, "corpus.tsv"), []byte(tsv), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		want map[string]int
		n    map[string]int
	}{
		{"cap per language", map[string]int{"en": 1, "ru": 5}, map[string]int{"en": 1, "ru": 1}},
		{"unwanted language skipped", map[string]int{"en": 5}, map[string]int{"en": 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			us, err := LoadCorpus(dir, tt.want, 0)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]int{}
			for _, u := range us {
				got[u.Lang]++
				if len(u.PCM) != 4 {
					t.Fatalf("pcm = %v", u.PCM)
				}
			}
			for l, n := range tt.n {
				if got[l] != n {
					t.Fatalf("%s: got %d want %d", l, got[l], n)
				}
			}
			if len(got) != len(tt.n) {
				t.Fatalf("languages = %v", got)
			}
		})
	}

	if err := os.WriteFile(filepath.Join(dir, "corpus.tsv"), []byte("en\tonly-two-fields\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCorpus(dir, map[string]int{"en": 1}, 0); err == nil {
		t.Fatal("malformed line must fail")
	}
}

func TestWriteMarkdown(t *testing.T) {
	var sb strings.Builder
	WriteMarkdown(&sb, []Result{
		{Model: "whisper-tiny", Lang: "ru", BackendInUse: "cpu", LoadMS: 100, WER: 0.25, CER: 0.1, PeakRSSMB: 200, NoSpeech: 1},
		{Model: "whisper-small", Lang: "en", BackendAsked: "auto", Error: "boom | bang"},
		{Model: "whisper-base", Lang: "en", BackendInUse: "Vulkan0"},
	})
	out := sb.String()
	for _, want := range []string{"### en", "### ru", "| whisper-tiny | cpu |", "25.0", "failed: boom / bang", "n/a"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Index(out, "### en") > strings.Index(out, "### ru") {
		t.Error("languages should be sorted")
	}
}
