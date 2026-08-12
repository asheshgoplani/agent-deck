package statedb

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	archiveBoundaryHelperDBEnv    = "AGENT_DECK_ARCHIVE_BOUNDARY_HELPER_DB"
	archiveBoundaryHelperTokenEnv = "AGENT_DECK_ARCHIVE_BOUNDARY_HELPER_TOKEN"
	archiveBoundaryHelperArg      = "archive-boundary-subprocess"
)

func TestArchiveEventBoundarySubprocessHelper(t *testing.T) {
	mode, token, ok := archiveBoundaryHelperInvocation()
	if !ok {
		t.Skip("subprocess helper")
	}
	if token == "" || token != os.Getenv(archiveBoundaryHelperTokenEnv) {
		t.Fatal("invalid subprocess helper token")
	}
	switch mode {
	case "holder":
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
	case "archiver":
		db := openArchiveBoundaryHelperDB(t)
		done := make(chan error, 1)
		go func() { done <- db.SetArchived("child", time.Now().UTC()) }()
		for {
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("archive child: %v", err)
				}
				t.Fatal("archive completed before blocking on the boundary")
			default:
			}
			if archiveBoundaryFlockIsBlocked() {
				break
			}
			runtime.Gosched()
		}
		if _, err := fmt.Fprintln(os.Stdout, "boundary-blocked"); err != nil {
			t.Fatalf("signal blocked boundary: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatalf("archive child: %v", err)
		}
		if _, err := fmt.Fprintln(os.Stdout, "archive-complete"); err != nil {
			t.Fatalf("signal completion: %v", err)
		}
	default:
		t.Fatalf("unknown subprocess helper mode %q", mode)
	}
}

func archiveBoundaryFlockIsBlocked() bool {
	trace := string(fullGoroutineStack(runtime.Stack))
	return strings.Contains(trace, "syscall.Flock") && strings.Contains(trace, "WithArchiveEventBoundary")
}

func fullGoroutineStack(capture func([]byte, bool) int) []byte {
	for size := 64 << 10; ; size *= 2 {
		stack := make([]byte, size)
		n := capture(stack, true)
		if n < len(stack) {
			return stack[:n]
		}
	}
}

func TestFullGoroutineStackGrowsAfterTruncation(t *testing.T) {
	var sizes []int
	stack := fullGoroutineStack(func(buf []byte, all bool) int {
		if !all {
			t.Fatal("capture did not request all goroutines")
		}
		sizes = append(sizes, len(buf))
		if len(sizes) < 3 {
			return len(buf)
		}
		return copy(buf, "complete-stack")
	})
	if got, want := fmt.Sprint(sizes), "[65536 131072 262144]"; got != want {
		t.Fatalf("capture sizes = %s, want %s", got, want)
	}
	if got, want := string(stack), "complete-stack"; got != want {
		t.Fatalf("stack = %q, want %q", got, want)
	}
}

func archiveBoundaryHelperInvocation() (mode, token string, ok bool) {
	for i := 0; i+3 < len(os.Args); i++ {
		if os.Args[i] == "--" && os.Args[i+1] == archiveBoundaryHelperArg {
			return os.Args[i+2], os.Args[i+3], true
		}
	}
	return "", "", false
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
	stderr *synchronizedBuffer
	cancel context.CancelFunc
}

type synchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func startArchiveBoundaryProcess(t *testing.T, mode, dbPath string, needsStdin bool) *archiveBoundaryProcess {
	t.Helper()
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		t.Fatalf("generate %s helper token: %v", mode, err)
	}
	token := hex.EncodeToString(tokenBytes)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestArchiveEventBoundarySubprocessHelper$", "--", archiveBoundaryHelperArg, mode, token)
	cmd.Env = []string{archiveBoundaryHelperDBEnv + "=" + dbPath, archiveBoundaryHelperTokenEnv + "=" + token}
	var stdin io.WriteCloser
	if needsStdin {
		var err error
		stdin, err = cmd.StdinPipe()
		if err != nil {
			cancel()
			t.Fatalf("%s stdin: %v", mode, err)
		}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		if stdin != nil {
			_ = stdin.Close()
		}
		cancel()
		t.Fatalf("%s stdout: %v", mode, err)
	}
	stderr := &synchronizedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		if stdin != nil {
			_ = stdin.Close()
		}
		_ = stdout.Close()
		cancel()
		t.Fatalf("start %s: %v", mode, err)
	}
	p := &archiveBoundaryProcess{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), stderr: stderr, cancel: cancel}
	t.Cleanup(func() {
		if stdin != nil {
			_ = stdin.Close()
		}
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		cancel()
	})
	return p
}

func readArchiveBoundaryLine(t *testing.T, p *archiveBoundaryProcess) string {
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
	case <-time.After(5 * time.Second):
		t.Fatalf("subprocess output deadline; stderr=%s", p.stderr.String())
		return ""
	}
}

func waitArchiveBoundaryProcess(t *testing.T, p *archiveBoundaryProcess) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait for subprocess: %v; stderr=%s", err, p.stderr.String())
		}
	case <-time.After(5 * time.Second):
		p.cancel()
		<-done
		t.Fatalf("subprocess exit deadline; stderr=%s", p.stderr.String())
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

	holder := startArchiveBoundaryProcess(t, "holder", dbPath, true)
	if got := readArchiveBoundaryLine(t, holder); got != "boundary-held\n" {
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

	archiver := startArchiveBoundaryProcess(t, "archiver", dbPath, false)
	if got := readArchiveBoundaryLine(t, archiver); got != "boundary-blocked\n" {
		t.Fatalf("archiver blocked signal = %q; stderr=%s", got, archiver.stderr.String())
	}

	row, err := db.LoadInstanceByID("child")
	if err != nil || row == nil {
		t.Fatalf("load blocked child: row=%v err=%v", row, err)
	}
	if !row.ArchivedAt.IsZero() {
		t.Fatal("child was archived while another process held the boundary")
	}

	if _, err := holder.stdin.Write([]byte{1}); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	waitArchiveBoundaryProcess(t, holder)
	if got := readArchiveBoundaryLine(t, archiver); got != "archive-complete\n" {
		t.Fatalf("archiver completion = %q", got)
	}
	waitArchiveBoundaryProcess(t, archiver)
	row, err = db.LoadInstanceByID("child")
	if err != nil || row == nil || row.ArchivedAt.IsZero() {
		t.Fatalf("child was not archived after release: row=%v err=%v", row, err)
	}
}
