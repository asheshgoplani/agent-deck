package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #3206 (requested by dpalic): per-session instruction files.

func TestIssue3206_CapabilitiesMarkInstructionsFileRepeatable(t *testing.T) {
	catalog, err := buildCreationCatalog("issue3206-static")
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"add", "launch"} {
		var found *session.RemoteCreationField
		for k, f := range catalog.Commands[command] {
			if f.Name == "instructions-file" {
				found = &catalog.Commands[command][k]
			} else if f.Repeatable {
				t.Errorf("%s: only instructions-file is marked repeatable, %q is too", command, f.Name)
			}
		}
		if found == nil || !found.TakesValue || !found.Repeatable {
			t.Fatalf("%s capabilities must list instructions-file as a repeatable value flag, got %+v", command, found)
		}
	}
}

func TestIssue3206_AddShowPrimerSetRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	role := filepath.Join(home, "team", "review.md")
	if err := os.MkdirAll(filepath.Dir(role), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(role, []byte("CLI-ROLE: review only\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A relative path is refused before anything is created.
	if out, stderr, code := runAgentDeck(t, home, "add", "-c", "codex", "-t", "bad", "--no-parent", "--json", "--instructions-file", "team/review.md", project); code == 0 {
		t.Fatalf("relative instructions file must be refused: %s %s", out, stderr)
	} else if !strings.Contains(out+stderr, "not an absolute path") {
		t.Fatalf("refusal must say why: %s %s", out, stderr)
	}

	out, stderr, code := runAgentDeck(t, home, "add", "-c", "codex", "-t", "Review", "--no-parent", "--json",
		"--instructions-file", "~/team/review.md", "--instructions-file", "~/team/missing.md", project)
	if code != 0 {
		t.Fatalf("add: %d %s %s", code, out, stderr)
	}

	type fileState struct {
		Path, State, Reason string
	}
	show := func() map[string]json.RawMessage {
		t.Helper()
		out, stderr, code := runAgentDeck(t, home, "session", "show", "Review", "--json")
		if code != 0 {
			t.Fatalf("show: %d %s %s", code, out, stderr)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("decode show: %v %s", err, out)
		}
		return m
	}
	var files []fileState
	if err := json.Unmarshal(show()["instructions_files"], &files); err != nil {
		t.Fatalf("show --json lacks instructions_files: %v", err)
	}
	missing := filepath.Join(home, "team", "missing.md")
	if len(files) != 2 || files[0] != (fileState{role, "ok", ""}) || files[1] != (fileState{missing, "unreadable", "file not found"}) {
		t.Fatalf("show --json instructions_files = %+v", files)
	}

	out, stderr, code = runAgentDeck(t, home, "session", "primer", "Review", "--json")
	if code != 0 {
		t.Fatalf("primer: %d %s %s", code, out, stderr)
	}
	var primer struct {
		Text              string      `json:"text"`
		InstructionsFiles []fileState `json:"instructions_files"`
	}
	if err := json.Unmarshal([]byte(out), &primer); err != nil {
		t.Fatalf("decode primer: %v %s", err, out)
	}
	if !strings.Contains(primer.Text, "## Session instructions\n") || !strings.Contains(primer.Text, "\n### "+role+"\nCLI-ROLE: review only\n") {
		t.Fatalf("primer text must be what the harness gets, instructions included:\n%s", primer.Text)
	}
	if len(primer.InstructionsFiles) != 2 || primer.InstructionsFiles[0].State != "ok" {
		t.Fatalf("primer instructions_files = %+v", primer.InstructionsFiles)
	}

	if out, stderr, code := runAgentDeck(t, home, "session", "set", "Review", "instructions-file", ""); code != 0 {
		t.Fatalf("clear: %d %s %s", code, out, stderr)
	}
	if _, ok := show()["instructions_files"]; ok {
		t.Fatal("a cleared list must leave instructions_files out of show --json")
	}
}

// #3215 review R-1: a FIFO as an instruction file must not hang the CLI.
// `session show --json` and `session primer --json` return and report it as
// unreadable (they used to block in open(2) until a writer appeared).
func TestIssue3206_CLIShowPrimerDoNotHangOnFIFO(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(home, "fifo.md")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, stderr, code := runAgentDeck(t, home, "add", "-c", "codex", "-t", "Fifo", "--no-parent", "--json", "--instructions-file", fifo, project); code != 0 {
		t.Fatalf("add: %d %s %s", code, out, stderr)
	}
	type fileState struct{ Path, State, Reason string }
	want := fileState{fifo, "unreadable", "not a regular file"}
	for _, args := range [][]string{{"session", "show", "Fifo", "--json"}, {"session", "primer", "Fifo", "--json"}} {
		done := make(chan struct{})
		var out, stderr string
		var code int
		go func() {
			defer close(done)
			out, stderr, code, _ = execAgentDeckEnv(channelsCLIBinary(t), home, "", nil, args...)
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatalf("%v hangs on a FIFO instruction file", args)
		}
		if code != 0 {
			t.Fatalf("%v: %d %s %s", args, code, out, stderr)
		}
		var got struct {
			InstructionsFiles []fileState `json:"instructions_files"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		if len(got.InstructionsFiles) != 1 || got.InstructionsFiles[0] != want {
			t.Fatalf("%v instructions_files = %+v, want %+v", args, got.InstructionsFiles, want)
		}
	}
}
