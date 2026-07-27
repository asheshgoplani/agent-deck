//go:build darwin

package procfd

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unsafe"
)

// TestStructLayoutMatchesProcInfoHeader pins the Go struct layout expected by
// the libproc calls. It does not read SDK headers; the OpenVnodePaths behavior
// tests verify these assumptions against the running macOS kernel.
func TestStructLayoutMatchesProcInfoHeader(t *testing.T) {
	if got := unsafe.Sizeof(procFDInfo{}); got != 8 {
		t.Errorf("sizeof(proc_fdinfo) = %d, want 8", got)
	}
	var info vnodeFDInfoWithPath
	if got := unsafe.Sizeof(info); got != 1200 {
		t.Errorf("sizeof(vnode_fdinfowithpath) = %d, want 1200", got)
	}
	if got := unsafe.Offsetof(info.Path); got != 176 {
		t.Errorf("offsetof(vnode_fdinfowithpath, path) = %d, want 176", got)
	}
}

func TestOpenVnodePathsRejectsPossiblyTruncatedFDList(t *testing.T) {
	previous := procPidinfoFn
	t.Cleanup(func() { procPidinfoFn = previous })
	procPidinfoFn = func(_ int, _ int, _ uint64, buf unsafe.Pointer, size int) (int, error) {
		if buf == nil {
			return int(unsafe.Sizeof(procFDInfo{})), nil
		}
		return size, nil
	}

	_, err := openVnodePaths(123)
	if err == nil || !strings.Contains(err.Error(), "may be truncated") {
		t.Fatalf("openVnodePaths full fd buffer error = %v, want truncation error", err)
	}
}

func TestOpenVnodePathsSelf(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "procfd-self-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// The kernel reports the real (symlink-resolved) vnode path; the temp dir
	// on macOS lives under /var -> /private/var.
	want, err := filepath.EvalSymlinks(f.Name())
	if err != nil {
		t.Fatal(err)
	}

	paths, err := OpenVnodePaths(os.Getpid())
	if err != nil {
		t.Fatalf("OpenVnodePaths(self): %v", err)
	}
	if !slices.Contains(paths, want) {
		t.Errorf("open file %q not found in %d vnode paths: %v", want, len(paths), paths)
	}
}

// TestOpenVnodePathsChildProcess exercises the cross-process case the session
// probe actually uses: another (same-uid) process holding a file open.
func TestOpenVnodePathsChildProcess(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "procfd-child-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	cmd := exec.Command("/bin/sleep", "30")
	cmd.Stdout = out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	want, err := filepath.EvalSymlinks(out.Name())
	if err != nil {
		t.Fatal(err)
	}

	paths, err := OpenVnodePaths(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("OpenVnodePaths(child %d): %v", cmd.Process.Pid, err)
	}
	if !slices.Contains(paths, want) {
		t.Errorf("child's stdout file %q not found in %d vnode paths: %v", want, len(paths), paths)
	}
}

func TestOpenVnodePathsDeadPID(t *testing.T) {
	// Spawn and reap a process so its PID is (almost certainly) unused.
	cmd := exec.Command("/usr/bin/true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVnodePaths(cmd.Process.Pid); err == nil {
		t.Error("expected an error probing a dead PID, got nil")
	}
}

func BenchmarkOpenVnodePaths(b *testing.B) {
	pid := os.Getpid()
	for b.Loop() {
		if _, err := OpenVnodePaths(pid); err != nil {
			b.Fatal(err)
		}
	}
}
