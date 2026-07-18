package source

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSessionMeta(t *testing.T) {
	s, err := ParseSessionMeta(filepath.Join("testdata", "session_titled.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Great Title" {
		t.Errorf("title = %q, want ai-title", s.Title)
	}
	if s.CWD != "/tmp/proj" || s.GitBranch != "main" {
		t.Errorf("cwd/branch = %q/%q", s.CWD, s.GitBranch)
	}
	if s.LastPrompt != "latest prompt" {
		t.Errorf("lastPrompt = %q", s.LastPrompt)
	}
	if s.MsgCount != 2 {
		t.Errorf("msgCount = %d, want 2", s.MsgCount)
	}
}

func TestParseFallsBackToFirstUserMessage(t *testing.T) {
	s, _ := ParseSessionMeta(filepath.Join("testdata", "session_firstmsg.jsonl"))
	if s.Title != "only user text here" {
		t.Errorf("title = %q, want first user msg", s.Title)
	}
}

func TestParseCollapsesMultilineTitle(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	body := "{\"type\":\"user\",\"cwd\":\"/x\",\"message\":{\"role\":\"user\",\"content\":\"line one\\n  line two\\n\\tline three\"}}\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := ParseSessionMeta(p)
	if s.Title != "line one line two line three" {
		t.Fatalf("title = %q, want single line", s.Title)
	}
}

func TestParseSkipsCommandWrapperTitle(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	body := `{"type":"user","cwd":"/x","message":{"role":"user","content":"<local-command-caveat>Caveat: blah</local-command-caveat>"}}` + "\n" +
		`{"type":"user","cwd":"/x","message":{"role":"user","content":"real first request"}}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := ParseSessionMeta(p)
	if s.Title != "real first request" {
		t.Fatalf("title = %q, want the real message (wrapper skipped)", s.Title)
	}
}

func TestParseSkipsCorruptLines(t *testing.T) {
	s, err := ParseSessionMeta(filepath.Join("testdata", "session_corrupt.jsonl"))
	if err != nil {
		t.Fatalf("should not error on corrupt line: %v", err)
	}
	if s.CWD != "/tmp/p3" {
		t.Errorf("cwd = %q, want recovered value", s.CWD)
	}
}
