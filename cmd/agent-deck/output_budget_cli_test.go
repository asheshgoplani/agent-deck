package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionOutputHelpDocumentsBudget(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runAgentDeck(t, home, "session", "output", "--help")
	if code != 0 {
		t.Fatalf("--help exited %d: %s%s", code, stdout, stderr)
	}
	for _, want := range []string{
		"--max-tokens",
		"25000",
		"ANSI",
		"beginning and end",
		"--json",
		"--copy",
		"full output",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--help missing %q:\n%s", want, stdout)
		}
	}
}

func TestRecordOutputReadRotatesLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	path, err := outputReadLogPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", outputReadLogMaxBytes)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := recordOutputRead("p", outputReadEvent{SessionID: "s", Source: "pane"}); err != nil {
		t.Fatal(err)
	}
	rotated, err := os.Stat(path + ".1")
	if err != nil || rotated.Size() != int64(outputReadLogMaxBytes) {
		t.Fatalf("rotated log: %v size=%v", err, rotated)
	}
	fresh, err := os.ReadFile(path)
	if err != nil || strings.Count(string(fresh), "\n") != 1 || !strings.Contains(string(fresh), `"session_id":"s"`) {
		t.Fatalf("fresh log after rotation: %q %v", fresh, err)
	}
}

func TestRecordOutputReadReportsUnwritableLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	path, err := outputReadLogPath()
	if err != nil {
		t.Fatal(err)
	}
	// A file where the logs directory should be makes MkdirAll fail.
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(path)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Dir(path), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := recordOutputRead("p", outputReadEvent{SessionID: "s", Source: "pane"}); err == nil {
		t.Fatal("recordOutputRead() = nil, want error when the log cannot be written")
	}
}
