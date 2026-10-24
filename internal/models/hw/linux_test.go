//go:build linux

package hw

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func touch(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGPUFrom(t *testing.T) {
	noTool := func(string) (string, error) { return "", errors.New("nf") }
	tests := []struct {
		name  string
		files []string
		tool  bool
		want  []string
	}{
		{"none", nil, false, nil},
		{"nvidia", []string{"proc/driver/nvidia/version"}, false, []string{BackendCUDA}},
		{"rocm", []string{"opt/rocm/x"}, false, []string{BackendROCm}},
		{"vulkan lib+render", []string{"usr/lib/x86_64-linux-gnu/libvulkan.so.1", "dev/dri/renderD128"}, false, []string{BackendVulkan}},
		{"vulkaninfo+render", []string{"dev/dri/renderD128"}, true, []string{BackendVulkan}},
		{"vulkan without render node", []string{"usr/lib/libvulkan.so.1"}, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, f := range tt.files {
				touch(t, root, f)
			}
			lp := noTool
			if tt.tool {
				lp = func(string) (string, error) { return "/usr/bin/vulkaninfo", nil }
			}
			g := gpuFrom(root, lp)
			if !g.Known || !reflect.DeepEqual(g.Backends, tt.want) {
				t.Fatalf("got %+v want %v", g, tt.want)
			}
		})
	}
}

func TestProcParsers(t *testing.T) {
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	os.WriteFile(mi, []byte("MemTotal:       16000000 kB\nMemFree: 1 kB\nMemAvailable:    8000000 kB\n"), 0o644)
	m := memoryFrom(mi)
	if m.TotalBytes != 16000000<<10 || m.AvailableBytes != 8000000<<10 {
		t.Fatalf("%+v", m)
	}
	ci := filepath.Join(dir, "cpuinfo")
	os.WriteFile(ci, []byte("processor : 0\nmodel name\t: Test CPU @ 3GHz\nmodel name\t: other\n"), 0o644)
	if got := cpuModelFrom(ci); got != "Test CPU @ 3GHz" {
		t.Fatalf("got %q", got)
	}
}

func TestFreeDiskSpace(t *testing.T) {
	n, err := FreeDiskSpace(t.TempDir())
	if err != nil || n == 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
