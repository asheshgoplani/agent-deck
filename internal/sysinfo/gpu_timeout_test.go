package sysinfo

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGPUProbeTimesOut(t *testing.T) {
	for _, tc := range []struct {
		tool    string
		collect func() GPUStat
	}{
		{"nvidia-smi", collectGPUNvidia},
		{"rocm-smi", collectGPURocm},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "pid")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GPU_PROBE_PID_FILE", pidFile)
			fake := "#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$GPU_PROBE_PID_FILE\"\nexec sleep 5\n"
			if err := os.WriteFile(filepath.Join(dir, tc.tool), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}

			start := time.Now()
			got := tc.collect()
			if elapsed := time.Since(start); elapsed >= 4*time.Second {
				t.Errorf("GPU probe took %s, want a bounded probe", elapsed)
			}
			if got.Available {
				t.Errorf("timed-out GPU probe reported available: %+v", got)
			}
			pidData, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
			if err != nil || pid <= 1 {
				t.Fatalf("invalid fake GPU process ID %q", pidData)
			}
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Errorf("fake GPU process %d was not reaped: %v", pid, err)
			}
		})
	}
}
