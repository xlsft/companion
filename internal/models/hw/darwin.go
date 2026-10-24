//go:build darwin

package hw

import (
	"context"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"

	"golang.org/x/sys/unix"
)

func cpuModel() string {
	s, _ := unix.Sysctl("machdep.cpu.brand_string")
	return s
}

func detectMemory(ctx context.Context) Memory {
	var m Memory
	m.TotalBytes, _ = unix.SysctlUint64("hw.memsize")
	m.AvailableBytes = vmAvailable(ctx)
	return m
}

var vmLine = regexp.MustCompile(`^(Pages free|Pages inactive|Pages speculative|Pages purgeable):\s+(\d+)\.`)
var vmPage = regexp.MustCompile(`page size of (\d+) bytes`)

// vmAvailable approximates "available" from vm_stat since there is no cgo
// host_statistics here: free + inactive + speculative + purgeable pages.
func vmAvailable(ctx context.Context) uint64 {
	out, err := exec.CommandContext(ctx, "vm_stat").Output()
	if err != nil {
		return 0
	}
	return parseVMStat(string(out))
}

func parseVMStat(s string) uint64 {
	page := uint64(4096)
	if m := vmPage.FindStringSubmatch(s); m != nil {
		if v, err := strconv.ParseUint(m[1], 10, 64); err == nil {
			page = v
		}
	}
	var pages uint64
	for _, line := range splitLines(s) {
		if m := vmLine.FindStringSubmatch(line); m != nil {
			v, _ := strconv.ParseUint(m[2], 10, 64)
			pages += v
		}
	}
	return pages * page
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// Metal ships with every Apple Silicon Mac; Intel Macs are left to the CPU
// path because their GPUs are not worth the ggml Metal setup risk.
func detectGPU(context.Context) GPUInfo {
	g := GPUInfo{Known: true}
	if runtime.GOARCH == "arm64" {
		g.Backends = []string{BackendMetal}
	}
	return g
}
