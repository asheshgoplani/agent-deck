//go:build unix

package filelinks

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO under a root must be refused at once, not block the request until
// something writes to it.
func TestOpenRefusesAFIFOWithoutBlocking(t *testing.T) {
	_, root := layout(t)
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := Open([]string{root}, fifo)
		if f != nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Fatalf("Open(fifo) = %v, want ErrNotRegular", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Open(fifo) blocked")
	}
}

// A file that grows after the size check is still served only up to the
// size that passed the cap.
func TestReaderStopsAtTheCheckedSize(t *testing.T) {
	_, root := layout(t)
	path := filepath.Join(root, "notes.txt")
	f, err := Open([]string{root}, path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.WriteString("grown past the check"); err != nil {
		t.Fatal(err)
	}
	g.Close()
	buf := make([]byte, 64)
	n, _ := f.Reader().ReadAt(buf, 0)
	if string(buf[:n]) != "hello\n" {
		t.Fatalf("Reader read %q, want only the checked %d bytes", buf[:n], f.Size)
	}
}
