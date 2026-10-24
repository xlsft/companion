package hw

import (
	"context"
	"testing"

	"github.com/xlsft/openclick/internal/models"
)

func mustCatalog(t *testing.T) models.Catalog {
	t.Helper()
	c, err := models.BuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func profile(ramGB int, simd bool, gpu ...string) Profile {
	return Profile{
		CPU:    CPU{Cores: 8, FastSIMD: simd, AVX2: simd},
		Memory: Memory{TotalBytes: uint64(ramGB) * gib, AvailableBytes: uint64(ramGB) * gib * 8 / 10},
		GPU:    GPUInfo{Known: true, Backends: gpu},
	}
}

func TestRecommend(t *testing.T) {
	cat := mustCatalog(t)
	tests := []struct {
		name  string
		p     Profile
		lang  string
		class string
		multi bool
	}{
		{"2GB", profile(2, true), "auto", "tiny", true},
		{"3GB", profile(3, true), "auto", "base", true},
		{"no avx2", profile(32, false), "auto", "base", true},
		{"8GB cpu", profile(8, true), "auto", "small", true},
		{"12GB cpu", profile(12, true), "ru", "small", true},
		{"16GB cpu", profile(16, true), "auto", "medium", true},
		{"32GB cpu never large", profile(32, true), "auto", "medium", true},
		{"8GB gpu", profile(8, true, BackendVulkan), "auto", "medium", true},
		{"4GB gpu", profile(4, true, BackendCUDA), "auto", "small", true},
		{"en english-only", profile(8, true), "en", "small", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, reason := Recommend(tt.p, cat, tt.lang)
			if classes[classRank(m)] != tt.class {
				t.Fatalf("got %s (%s), want class %s", m.ID, reason, tt.class)
			}
			if m.Multilingual() != tt.multi {
				t.Fatalf("got %s multilingual=%v, want %v", m.ID, m.Multilingual(), tt.multi)
			}
			if reason == "" {
				t.Fatal("empty reason")
			}
		})
	}
}

func TestRecommendQuantization(t *testing.T) {
	cat := mustCatalog(t)
	if m, _ := Recommend(profile(8, true), cat, "auto"); !isQuantized(m) {
		t.Fatalf("cpu should prefer quantized, got %s", m.ID)
	}
	if m, _ := Recommend(profile(8, true, BackendMetal), cat, "auto"); isQuantized(m) {
		t.Fatalf("gpu should prefer full precision, got %s", m.ID)
	}
}

func TestRecommendLowAvailableRAM(t *testing.T) {
	p := profile(16, true)
	p.Memory.AvailableBytes = 1 * gib // plenty total, but busy machine
	m, _ := Recommend(p, mustCatalog(t), "auto")
	if m.RAMRequiredMB > 800 {
		t.Fatalf("got %s needing %d MB with 1 GB available", m.ID, m.RAMRequiredMB)
	}
}

func TestDetectSmoke(t *testing.T) {
	p := Detect(context.Background(), t.TempDir())
	if p.CPU.Cores < 1 || p.OS == "" {
		t.Fatalf("bad profile %+v", p)
	}
}
