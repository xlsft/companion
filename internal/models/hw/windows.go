//go:build windows

package hw

import (
	"context"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func cpuModel() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	s, _, _ := k.GetStringValue("ProcessorNameString")
	return s
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func detectMemory(context.Context) Memory {
	st := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	if r, _, _ := proc.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		return Memory{}
	}
	return Memory{TotalBytes: st.TotalPhys, AvailableBytes: st.AvailPhys}
}

// TODO(OC-30): probe DXGI adapters / nvcuda.dll / vulkan-1.dll. Until then the
// GPU stays Unknown and Recommend assumes CPU-only.
func detectGPU(context.Context) GPUInfo { return GPUInfo{} }
