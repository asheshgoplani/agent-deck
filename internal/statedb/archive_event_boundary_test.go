package statedb

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const archiveBoundaryHelperEnv = "AGENT_DECK_ARCHIVE_BOUNDARY_HELPER"

func TestArchiveEventBoundarySubprocessHelper(t *testing.T) {
	dbPath := os.Getenv(archiveBoundaryHelperEnv)
	if dbPath == "" {
		t.Skip("subprocess helper")
	}
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open helper database: %v", err)
	}
	defer db.Close()
	if err := db.WithArchiveEventBoundary(func() error {
		if _, err := fmt.Fprintln(os.Stdout, "boundary-held"); err != nil {
			return err
		}
		_, err := bufio.NewReader(os.Stdin).ReadByte()
		return err
	}); err != nil {
		t.Fatalf("hold helper boundary: %v", err)
	}
}

func TestArchiveEventBoundarySerializesAcrossProcesses(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open parent database: %v", err)
	}
	defer db.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestArchiveEventBoundarySubprocessHelper$")
	cmd.Env = append(os.Environ(), archiveBoundaryHelperEnv+"="+dbPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("helper stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("helper stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			_, _ = stdin.Write([]byte{1})
		}
		_ = stdin.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read helper readiness: %v", err)
	}
	if line != "boundary-held\n" {
		t.Fatalf("helper readiness = %q", line)
	}

	entered := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- db.WithArchiveEventBoundary(func() error {
			close(entered)
			return nil
		})
	}()
	select {
	case <-entered:
		t.Fatal("parent crossed boundary while subprocess held it")
	case <-time.After(250 * time.Millisecond):
	}

	if _, err := stdin.Write([]byte{1}); err != nil {
		t.Fatalf("release helper: %v", err)
	}
	released = true
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for helper: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not enter boundary after subprocess released it")
	}
	if err := <-finished; err != nil {
		t.Fatalf("parent boundary: %v", err)
	}
}
