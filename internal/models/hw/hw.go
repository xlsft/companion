// Package hw detects the hardware facts that decide which STT model fits
// (OC-30). Everything is best effort and pure Go: a probe that cannot answer
// leaves its field zero/unknown instead of failing.
package hw

import (
	"context"
	"runtime"

	"golang.org/x/sys/cpu"

	"github.com/xlsft/companion/internal/models/disk"
)

// GPU backend names; they match Manifest.Backends plus "rocm" which no
// official ggml model targets yet but is useful for diagnostics.
const (
	BackendCUDA   = "cuda"
	BackendROCm   = "rocm"
	BackendVulkan = "vulkan"
	BackendMetal  = "metal"
)

// CPU describes the processor.
type CPU struct {
	Model string `json:"model,omitempty"`
	Arch  string `json:"arch"`
	// Cores is the number of logical CPUs usable by this process.
	Cores    int  `json:"cores"`
	AVX      bool `json:"avx"`
	AVX2     bool `json:"avx2"`
	AVX512   bool `json:"avx512"`
	NEON     bool `json:"neon"`
	FastSIMD bool `json:"fast_simd"` // AVX2 on x86, NEON on arm: enough for real-time small models
}

// GPUInfo lists compute backends that look usable.
type GPUInfo struct {
	// Known is false when the platform has no probe, so "no backends" must
	// not be read as "no GPU".
	Known    bool     `json:"known"`
	Backends []string `json:"backends,omitempty"`
}

// Has reports whether the backend was detected.
func (g GPUInfo) Has(b string) bool {
	for _, x := range g.Backends {
		if x == b {
			return true
		}
	}
	return false
}

// Usable reports whether any backend can run models faster than the CPU.
func (g GPUInfo) Usable() bool { return len(g.Backends) > 0 }

// Memory is physical RAM in bytes; zero means unknown.
type Memory struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

// Profile is the full detection result.
type Profile struct {
	OS     string  `json:"os"`
	CPU    CPU     `json:"cpu"`
	Memory Memory  `json:"memory"`
	GPU    GPUInfo `json:"gpu"`
	// DiskFreeBytes is the free space of the directory passed to Detect.
	DiskFreeBytes uint64 `json:"disk_free_bytes"`
}

// Detect probes the machine. dir is where models will be stored; its
// filesystem's free space is reported.
func Detect(ctx context.Context, dir string) Profile {
	p := Profile{OS: runtime.GOOS, CPU: detectCPU()}
	p.Memory = detectMemory(ctx)
	p.GPU = detectGPU(ctx)
	if dir != "" {
		p.DiskFreeBytes, _ = FreeDiskSpace(dir)
	}
	return p
}

func detectCPU() CPU {
	c := CPU{
		Model: cpuModel(),
		Arch:  runtime.GOARCH,
		Cores: runtime.NumCPU(),
		AVX:   cpu.X86.HasAVX,
		AVX2:  cpu.X86.HasAVX2,
		// F is the AVX-512 foundation; ggml checks it as the gate.
		AVX512: cpu.X86.HasAVX512F,
	}
	// arm64 mandates ASIMD (NEON); 32-bit arm reports it separately.
	c.NEON = runtime.GOARCH == "arm64" || cpu.ARM.HasNEON
	c.FastSIMD = c.AVX2 || c.NEON
	return c
}

// FreeDiskSpace returns the bytes available to the user on the filesystem of dir.
func FreeDiskSpace(dir string) (uint64, error) { return disk.Free(dir) }
