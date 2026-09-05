package main

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestPrepareAgentBoundaryOutputMarksOmittedContent(t *testing.T) {
	got, truncated := prepareAgentBoundaryOutput(strings.Repeat("abcdefghij", 30), 25, "/tmp/full.txt")
	if !truncated || !strings.Contains(got, "output omitted") {
		t.Fatalf("truncated output has no explicit seam marker: %q", got)
	}
}

func TestPrepareAgentBoundaryOutputAlwaysIncludesRecoveryFooter(t *testing.T) {
	got, truncated := prepareAgentBoundaryOutput(strings.Repeat("x", 100), 1, "/tmp/full.txt")
	if !truncated || !strings.Contains(got, "full output at /tmp/full.txt") {
		t.Fatalf("tiny budget lost recovery footer: %q", got)
	}
}

func TestPrepareAgentBoundaryOutputSaturatesOverflow(t *testing.T) {
	raw := "complete output"
	got, truncated := prepareAgentBoundaryOutput(raw, math.MaxInt, "/unused")
	if truncated || got != raw {
		t.Fatalf("overflowed budget changed output: got (%q, %v)", got, truncated)
	}
}

func TestAgentBoundaryModesEnumeration(t *testing.T) {
	tests := []struct {
		name                  string
		json, quiet, copyMode bool
		wantBound             bool
	}{
		{name: "default", wantBound: true},
		{name: "json remote compatible", json: true},
		{name: "quiet compatibility", quiet: true},
		{name: "copy keeps full clipboard", copyMode: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldBoundAgentOutput(tt.json, tt.quiet, tt.copyMode); got != tt.wantBound {
				t.Fatalf("shouldBoundAgentOutput() = %v, want %v", got, tt.wantBound)
			}
		})
	}
}

func TestOutputSnapshotPathRejectsDotOnlySessionIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	for _, id := range []string{"", ".", "..", "..."} {
		if path, err := outputSnapshotPath(id, "pane"); err == nil {
			t.Errorf("outputSnapshotPath(%q) = %q, want error", id, path)
		}
	}
}

type closeErrorWriter struct{ strings.Builder }

func (w *closeErrorWriter) Close() error { return errors.New("close failed") }

func TestWriteOutputReadLineReturnsCloseError(t *testing.T) {
	w := &closeErrorWriter{}
	if err := writeOutputReadLine(w, []byte("event\n")); err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("writeOutputReadLine() error = %v, want close failure", err)
	}
}

func TestPrepareAgentBoundaryOutputCapsStripsANSIAndKeepsHeadTail(t *testing.T) {
	raw := "\x1b[31mHEAD\x1b[0m" + strings.Repeat("middle", 20) + "TAIL"
	got, truncated := prepareAgentBoundaryOutput(raw, 30, "/tmp/full.txt")
	if !truncated {
		t.Fatal("long agent-boundary output was not truncated")
	}
	if len(got) > 30*outputBytesPerToken {
		t.Fatalf("output is %d bytes, budget is %d", len(got), 30*outputBytesPerToken)
	}
	for _, want := range []string{"HEAD", "TAIL", "full output at /tmp/full.txt"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bounded output missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("bounded output retained ANSI: %q", got)
	}
}

func TestPrepareAgentBoundaryOutputDoesNotSplitUTF8(t *testing.T) {
	raw := strings.Repeat("🙂", 40)
	got, truncated := prepareAgentBoundaryOutput(raw, 20, "/x")
	if !truncated || !utf8.ValidString(got) {
		t.Fatalf("truncated=%v valid_utf8=%v output=%q", truncated, utf8.ValidString(got), got)
	}
}

func TestPrepareAgentBoundaryOutputStripsANSIEvenBelowBudget(t *testing.T) {
	got, truncated := prepareAgentBoundaryOutput("ok \x1b[32mgreen\x1b[0m", 100, "/unused")
	if truncated || got != "ok green" {
		t.Fatalf("got (%q, %v), want (%q, false)", got, truncated, "ok green")
	}
}

func TestOutputSnapshotAndReadEventAreDurable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	path, err := outputSnapshotPath("child/one", "pane")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeOutputSnapshot(path, "full raw output"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "full raw output" {
		t.Fatalf("snapshot got %q, %v", got, err)
	}
	if err := recordOutputRead("test", outputReadEvent{SessionID: "child", Source: "pane", Truncated: true, MaxTokens: 7}); err != nil {
		t.Fatal(err)
	}
	dataDir, err := session.GetAgentDeckDir()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dataDir, "logs", "session-output-reads.jsonl")
	line, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var event outputReadEvent
	if err := json.Unmarshal(line, &event); err != nil {
		t.Fatalf("guardrail log is not JSONL: %v (%q)", err, line)
	}
	if event.SessionID != "child" || event.Profile != "test" || !event.Truncated {
		t.Fatalf("unexpected guardrail event: %+v", event)
	}
}
