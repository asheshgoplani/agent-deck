package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveMessageInput(t *testing.T) {
	writeTemp := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "msg.txt")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("inline only passes through", func(t *testing.T) {
		got, err := resolveMessageInput("hello", "", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})

	t.Run("neither returns empty without error", func(t *testing.T) {
		got, err := resolveMessageInput("", "", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("file only reads content", func(t *testing.T) {
		path := writeTemp(t, "line one\nline two `with` $pecial \"chars\"\n")
		got, err := resolveMessageInput("", path, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "line one\nline two `with` $pecial \"chars\""
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("trailing newlines trimmed but inner preserved", func(t *testing.T) {
		path := writeTemp(t, "a\n\nb\r\n\r\n\n")
		got, err := resolveMessageInput("", path, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "a\n\nb" {
			t.Errorf("got %q, want %q", got, "a\n\nb")
		}
	})

	t.Run("dash reads stdin", func(t *testing.T) {
		got, err := resolveMessageInput("", "-", strings.NewReader("from stdin\n"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "from stdin" {
			t.Errorf("got %q, want %q", got, "from stdin")
		}
	})

	t.Run("both inline and file errors", func(t *testing.T) {
		path := writeTemp(t, "content")
		if _, err := resolveMessageInput("inline", path, nil); err == nil {
			t.Error("expected error when both -m and --message-file are set")
		}
	})

	t.Run("missing file errors", func(t *testing.T) {
		if _, err := resolveMessageInput("", "/nonexistent/msg.txt", nil); err == nil {
			t.Error("expected error for missing file")
		}
	})

	t.Run("empty file errors", func(t *testing.T) {
		path := writeTemp(t, "  \n\n")
		if _, err := resolveMessageInput("", path, nil); err == nil {
			t.Error("expected error for empty message file")
		}
	})
}
