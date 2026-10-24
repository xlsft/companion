//go:build linux

package hw

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func cpuModel() string { return cpuModelFrom("/proc/cpuinfo") }

func cpuModelFrom(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	// x86 says "model name"; arm kernels say "Model" or "Hardware".
	keys := []string{"model name", "Model", "Hardware"}
	found := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if _, seen := found[k]; !seen {
			found[k] = strings.TrimSpace(v)
		}
	}
	for _, k := range keys {
		if v := found[k]; v != "" {
			return v
		}
	}
	return ""
}

func detectMemory(context.Context) Memory { return memoryFrom("/proc/meminfo") }

func memoryFrom(path string) Memory {
	f, err := os.Open(path)
	if err != nil {
		return Memory{}
	}
	defer f.Close()
	var m Memory
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "MemTotal":
			m.TotalBytes = kb << 10
		case "MemAvailable":
			m.AvailableBytes = kb << 10
		}
	}
	return m
}

func detectGPU(context.Context) GPUInfo { return gpuFrom("/", exec.LookPath) }

// gpuFrom probes under root so tests can fake the filesystem. Presence of
// files is only a hint that a backend may work; the sidecar still has to
// initialise it and fall back to CPU on failure.
func gpuFrom(root string, lookPath func(string) (string, error)) GPUInfo {
	g := GPUInfo{Known: true}
	exists := func(p string) bool {
		_, err := os.Stat(filepath.Join(root, p))
		return err == nil
	}
	glob := func(p string) bool {
		m, _ := filepath.Glob(filepath.Join(root, p))
		return len(m) > 0
	}
	if exists("proc/driver/nvidia/version") {
		g.Backends = append(g.Backends, BackendCUDA)
	}
	if exists("opt/rocm") {
		g.Backends = append(g.Backends, BackendROCm)
	}
	hasVulkanLib := glob("usr/lib/libvulkan.so.1") || glob("usr/lib64/libvulkan.so.1") ||
		glob("usr/lib/*/libvulkan.so.1") || glob("lib/*/libvulkan.so.1")
	_, vkErr := lookPath("vulkaninfo")
	// Vulkan needs both a loader and a render node, else it is llvmpipe at best.
	if (hasVulkanLib || vkErr == nil) && glob("dev/dri/renderD*") {
		g.Backends = append(g.Backends, BackendVulkan)
	}
	return g
}
