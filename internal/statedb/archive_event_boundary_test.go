package statedb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const archiveBoundaryHelperDBEnv = "AGENT_DECK_ARCHIVE_BOUNDARY_HELPER_DB"

func TestArchiveEventBoundarySubprocessHelper(t *testing.T) {
	t.Run("holder", func(t *testing.T) {
		db := openArchiveBoundaryHelperDB(t)
		if err := db.WithArchiveEventBoundary(func() error {
			if _, err := fmt.Fprintln(os.Stdout, "boundary-held"); err != nil {
				return err
			}
			_, err := bufio.NewReader(os.Stdin).ReadByte()
			return err
		}); err != nil {
			t.Fatalf("hold boundary: %v", err)
		}
	})
	t.Run("archiver", func(t *testing.T) {
		db := openArchiveBoundaryHelperDB(t)
		if _, err := fmt.Fprintln(os.Stdout, "archiver-ready"); err != nil {
			t.Fatalf("signal readiness: %v", err)
		}
		if _, err := bufio.NewReader(os.Stdin).ReadByte(); err != nil {
			t.Fatalf("wait for start: %v", err)
		}
		if _, err := fmt.Fprintln(os.Stdout, "archiver-calling"); err != nil {
			t.Fatalf("signal call: %v", err)
		}
		if err := db.SetArchived("child", time.Now().UTC()); err != nil {
			t.Fatalf("archive child: %v", err)
		}
		_, _ = fmt.Fprintln(os.Stdout, "archive-complete")
	})
}

func openArchiveBoundaryHelperDB(t *testing.T) *StateDB {
	t.Helper()
	dbPath := os.Getenv(archiveBoundaryHelperDBEnv)
	if dbPath == "" {
		t.Skip("subprocess helper")
	}
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open helper database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type archiveBoundaryProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *bytes.Buffer
}

func startArchiveBoundaryProcess(t *testing.T, ctx context.Context, mode, dbPath string) *archiveBoundaryProcess {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestArchiveEventBoundarySubprocessHelper$/"+mode)
	cmd.Env = []string{archiveBoundaryHelperDBEnv + "=" + dbPath}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("%s stdin: %v", mode, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("%s stdout: %v", mode, err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", mode, err)
	}
	p := &archiveBoundaryProcess{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), stderr: stderr}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return p
}

func readArchiveBoundaryLine(t *testing.T, ctx context.Context, p *archiveBoundaryProcess) string {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		line, err := p.stdout.ReadString('\n')
		resultCh <- result{line: line, err: err}
	}()
	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("read subprocess output: %v; stderr=%s", result.err, p.stderr.String())
		}
		return result.line
	case <-ctx.Done():
		t.Fatalf("subprocess output deadline: %v; stderr=%s", ctx.Err(), p.stderr.String())
		return ""
	}
}

func TestSetArchivedSerializesAcrossProcesses(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open parent database: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.SaveInstance(&InstanceRow{ID: "child", Title: "child", ProjectPath: t.TempDir(), GroupPath: "default", Tool: "shell", Status: "running", CreatedAt: time.Now(), ToolData: json.RawMessage("{}")}); err != nil {
		t.Fatalf("save child: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	holder := startArchiveBoundaryProcess(t, ctx, "holder", dbPath)
	if got := readArchiveBoundaryLine(t, ctx, holder); got != "boundary-held\n" {
		t.Fatalf("holder readiness = %q", got)
	}

	lockFile, err := os.OpenFile(dbPath+".archive-event.lock", os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open boundary lockfile: %v", err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		t.Fatal("subprocess readiness was reported without holding the OS lock")
	} else if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
		t.Fatalf("inspect boundary lock state: %v", err)
	}

	archiver := startArchiveBoundaryProcess(t, ctx, "archiver", dbPath)
	if got := readArchiveBoundaryLine(t, ctx, archiver); got != "archiver-ready\n" {
		t.Fatalf("archiver readiness = %q", got)
	}
	if _, err := archiver.stdin.Write([]byte{1}); err != nil {
		t.Fatalf("start archiver: %v", err)
	}
	if got := readArchiveBoundaryLine(t, ctx, archiver); got != "archiver-calling\n" {
		t.Fatalf("archiver call signal = %q", got)
	}

	complete := make(chan string, 1)
	go func() {
		line, _ := archiver.stdout.ReadString('\n')
		complete <- line
	}()
	select {
	case line := <-complete:
		t.Fatalf("SetArchived crossed held boundary: %q", line)
	case <-time.After(250 * time.Millisecond):
	}
	row, err := db.LoadInstanceByID("child")
	if err != nil {
		t.Fatalf("load blocked child: %v", err)
	}
	if !row.ArchivedAt.IsZero() {
		t.Fatal("child was archived while another process held the boundary")
	}

	if _, err := holder.stdin.Write([]byte{1}); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	if err := holder.cmd.Wait(); err != nil {
		t.Fatalf("wait for holder: %v; stderr=%s", err, holder.stderr.String())
	}
	select {
	case line := <-complete:
		if line != "archive-complete\n" {
			t.Fatalf("archiver completion = %q; stderr=%s", line, archiver.stderr.String())
		}
	case <-ctx.Done():
		t.Fatalf("archiver completion deadline: %v; stderr=%s", ctx.Err(), archiver.stderr.String())
	}
	if err := archiver.cmd.Wait(); err != nil {
		t.Fatalf("wait for archiver: %v; stderr=%s", err, archiver.stderr.String())
	}
	row, err = db.LoadInstanceByID("child")
	if err != nil || row == nil || row.ArchivedAt.IsZero() {
		t.Fatalf("child was not archived after release: row=%v err=%v", row, err)
	}
}
