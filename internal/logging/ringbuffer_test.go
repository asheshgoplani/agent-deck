package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRingBufferBasicWrite(t *testing.T) {
	rb := NewRingBuffer(64)

	n, err := rb.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Errorf("expected n=5, got %d", n)
	}

	got := rb.Bytes()
	if string(got) != "hello" {
		t.Errorf("expected 'hello', got %q", string(got))
	}
}

func TestRingBufferWrap(t *testing.T) {
	rb := NewRingBuffer(10)

	// Write more than buffer size
	_, _ = rb.Write([]byte("abcdefghij")) // fills exactly
	_, _ = rb.Write([]byte("12345"))      // wraps

	got := rb.Bytes()
	// Should contain: fghij12345 (last 10 bytes in order)
	if string(got) != "fghij12345" {
		t.Errorf("expected 'fghij12345', got %q", string(got))
	}
}

func TestRingBufferLargerThanCapacity(t *testing.T) {
	rb := NewRingBuffer(5)

	// Write data larger than buffer
	_, _ = rb.Write([]byte("0123456789"))

	got := rb.Bytes()
	// Should keep only last 5 bytes
	if string(got) != "56789" {
		t.Errorf("expected '56789', got %q", string(got))
	}
}

func TestRingBufferMultipleSmallWrites(t *testing.T) {
	rb := NewRingBuffer(8)

	_, _ = rb.Write([]byte("AA"))
	_, _ = rb.Write([]byte("BB"))
	_, _ = rb.Write([]byte("CC"))
	_, _ = rb.Write([]byte("DD"))
	// Total: 8 bytes exactly fills buffer
	got := rb.Bytes()
	if string(got) != "AABBCCDD" {
		t.Errorf("expected 'AABBCCDD', got %q", string(got))
	}

	// One more write wraps
	_, _ = rb.Write([]byte("EE"))
	got = rb.Bytes()
	// Should be: BBCCDDDEE (oldest data overwritten)
	if string(got) != "BBCCDDEE" {
		t.Errorf("expected 'BBCCDDEE', got %q", string(got))
	}
}

func TestRingBufferDumpToFile(t *testing.T) {
	rb := NewRingBuffer(32)
	_, _ = rb.Write([]byte("dump_test_data"))

	dir := t.TempDir()
	path := filepath.Join(dir, "dump.bin")
	if err := rb.DumpToFile(path); err != nil {
		t.Fatalf("DumpToFile failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read dump: %v", err)
	}

	if !bytes.Equal(data, []byte("dump_test_data")) {
		t.Errorf("expected 'dump_test_data', got %q", string(data))
	}
}

func TestRingBufferConcurrent(t *testing.T) {
	rb := NewRingBuffer(1024)
	done := make(chan struct{})

	// Write from multiple goroutines
	for i := range 10 {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			for range 100 {
				_, _ = rb.Write([]byte("x"))
			}
		}(i)
	}

	for range 10 {
		<-done
	}

	got := rb.Bytes()
	if len(got) != 1000 {
		t.Errorf("expected 1000 bytes, got %d", len(got))
	}
}
