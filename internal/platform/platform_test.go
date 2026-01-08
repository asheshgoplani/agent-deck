package platform

import (
	"runtime"
	"testing"
)

func TestDetect(t *testing.T) {
	// Reset detection cache for clean test
	detectionDone = false
	detectedPlatform = ""

	p := Detect()

	// Should return a valid platform
	if p == "" {
		t.Error("Detect() returned empty platform")
	}

	// On macOS, should detect macOS
	if runtime.GOOS == "darwin" {
		if p != PlatformMacOS {
			t.Errorf("Expected PlatformMacOS on darwin, got %s", p)
		}
	}

	// Detection should be cached
	p2 := Detect()
	if p != p2 {
		t.Errorf("Detect() not cached: got %s then %s", p, p2)
	}
}

func TestPlatformString(t *testing.T) {
	tests := []struct {
		platform Platform
		expected string
	}{
		{PlatformMacOS, "macOS"},
		{PlatformLinux, "Linux"},
		{PlatformWSL1, "WSL1"},
		{PlatformWSL2, "WSL2"},
		{PlatformWindows, "Windows"},
		{PlatformUnknown, "Unknown"},
	}

	for _, tt := range tests {
		if got := tt.platform.String(); got != tt.expected {
			t.Errorf("Platform(%s).String() = %s, want %s", tt.platform, got, tt.expected)
		}
	}
}

func TestSupportsUnixSockets(t *testing.T) {
	// Reset detection cache
	detectionDone = false
	detectedPlatform = ""

	// Test each platform manually
	tests := []struct {
		platform Platform
		expected bool
	}{
		{PlatformMacOS, true},
		{PlatformLinux, true},
		{PlatformWSL2, true},
		{PlatformWSL1, false},
		{PlatformWindows, false},
		{PlatformUnknown, false},
	}

	for _, tt := range tests {
		// Override detection for testing
		detectedPlatform = tt.platform
		detectionDone = true

		if got := SupportsUnixSockets(); got != tt.expected {
			t.Errorf("SupportsUnixSockets() for %s = %v, want %v", tt.platform, got, tt.expected)
		}
	}

	// Reset for other tests
	detectionDone = false
}

func TestIsWSL(t *testing.T) {
	tests := []struct {
		platform Platform
		isWSL    bool
		isWSL1   bool
		isWSL2   bool
	}{
		{PlatformMacOS, false, false, false},
		{PlatformLinux, false, false, false},
		{PlatformWSL1, true, true, false},
		{PlatformWSL2, true, false, true},
		{PlatformWindows, false, false, false},
	}

	for _, tt := range tests {
		// Override detection
		detectedPlatform = tt.platform
		detectionDone = true

		if got := IsWSL(); got != tt.isWSL {
			t.Errorf("IsWSL() for %s = %v, want %v", tt.platform, got, tt.isWSL)
		}
		if got := IsWSL1(); got != tt.isWSL1 {
			t.Errorf("IsWSL1() for %s = %v, want %v", tt.platform, got, tt.isWSL1)
		}
		if got := IsWSL2(); got != tt.isWSL2 {
			t.Errorf("IsWSL2() for %s = %v, want %v", tt.platform, got, tt.isWSL2)
		}
	}

	// Reset
	detectionDone = false
}

func TestDetectOnCurrentPlatform(t *testing.T) {
	// Reset cache
	detectionDone = false
	detectedPlatform = ""

	p := Detect()

	// Basic sanity checks based on runtime.GOOS
	switch runtime.GOOS {
	case "darwin":
		if p != PlatformMacOS {
			t.Errorf("On darwin, expected macOS, got %s", p)
		}
		if !SupportsUnixSockets() {
			t.Error("macOS should support Unix sockets")
		}
	case "linux":
		// Could be Linux or WSL
		if p != PlatformLinux && p != PlatformWSL1 && p != PlatformWSL2 {
			t.Errorf("On linux, expected Linux/WSL, got %s", p)
		}
	case "windows":
		if p != PlatformWindows {
			t.Errorf("On windows, expected Windows, got %s", p)
		}
		if SupportsUnixSockets() {
			t.Error("Windows should not support Unix sockets")
		}
	}
}
