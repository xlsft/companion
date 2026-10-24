//go:build !linux && !darwin && !windows

package hw

import "context"

func cpuModel() string                    { return "" }
func detectMemory(context.Context) Memory { return Memory{} }
func detectGPU(context.Context) GPUInfo   { return GPUInfo{} }
