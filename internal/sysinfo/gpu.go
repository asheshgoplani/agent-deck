package sysinfo

import (
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// gpuProbe caches the result of GPU tool detection at startup.
var gpuProbe struct {
	once      sync.Once
	tool      string // "nvidia-smi", "rocm-smi", or "" (none)
	available bool
}

// probeGPU detects available GPU monitoring tools. Called once.
func probeGPU() {
	gpuProbe.once.Do(func() {
		// Try nvidia-smi first (most common)
		if path, err := exec.LookPath("nvidia-smi"); err == nil && path != "" {
			gpuProbe.tool = "nvidia-smi"
			gpuProbe.available = true
			return
		}
		// Try rocm-smi for AMD GPUs
		if path, err := exec.LookPath("rocm-smi"); err == nil && path != "" {
			gpuProbe.tool = "rocm-smi"
			gpuProbe.available = true
			return
		}
	})
}

func collectGPU() GPUStat {
	probeGPU()

	if !gpuProbe.available {
		return GPUStat{}
	}

	switch gpuProbe.tool {
	case "nvidia-smi":
		return collectGPUNvidia()
	case "rocm-smi":
		return collectGPURocm()
	default:
		return GPUStat{}
	}
}

// collectGPUNvidia queries NVIDIA GPU utilization.
func collectGPUNvidia() GPUStat {
	out, err := exec.Command(
		"nvidia-smi",
		"--query-gpu=utilization.gpu,name",
		"--format=csv,noheader,nounits",
	).Output()
	if err != nil {
		return GPUStat{}
	}

	// Output: "45, NVIDIA GeForce RTX 3090"
	line := strings.TrimSpace(string(out))
	// Take first GPU if multiple
	if idx := strings.IndexByte(line, '\n'); idx != -1 {
		line = line[:idx]
	}

	parts := strings.SplitN(line, ", ", 2)
	if len(parts) < 1 {
		return GPUStat{}
	}

	usage, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return GPUStat{}
	}

	stat := GPUStat{Available: true, UsagePercent: usage}
	if len(parts) >= 2 {
		stat.Name = strings.TrimSpace(parts[1])
	}
	return stat
}

// collectGPURocm queries AMD GPU utilization via rocm-smi.
func collectGPURocm() GPUStat {
	out, err := exec.Command("rocm-smi", "--showuse", "--csv").Output()
	if err != nil {
		return GPUStat{}
	}

	// Parse CSV output for GPU use percentage
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return GPUStat{}
	}

	// Find "GPU use (%)" column index from header
	headers := strings.Split(lines[0], ",")
	useIdx := -1
	for i, h := range headers {
		if strings.Contains(strings.ToLower(strings.TrimSpace(h)), "gpu use") {
			useIdx = i
			break
		}
	}
	if useIdx == -1 {
		return GPUStat{}
	}

	// Parse first GPU's data row
	values := strings.Split(lines[1], ",")
	if useIdx >= len(values) {
		return GPUStat{}
	}

	usage, err := strconv.ParseFloat(strings.TrimSpace(values[useIdx]), 64)
	if err != nil {
		return GPUStat{}
	}

	return GPUStat{Available: true, UsagePercent: usage}
}

// GPUAvailable returns whether a GPU monitoring tool was detected.
func GPUAvailable() bool {
	probeGPU()
	return gpuProbe.available
}
