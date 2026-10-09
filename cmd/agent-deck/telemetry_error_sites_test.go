package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	"github.com/asheshgoplani/agent-deck/internal/vcs"
)

func cliSpooledErrors(t *testing.T) []map[string]any {
	t.Helper()
	p, err := telemetry.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(filepath.Dir(p), telemetry.SpoolFileName))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l struct {
			E string         `json:"e"`
			P map[string]any `json:"p"`
		}
		if json.Unmarshal(sc.Bytes(), &l) == nil && l.E == "error" {
			out = append(out, l.P)
		}
	}
	return out
}

func grantCLITelemetry(t *testing.T) {
	t.Helper()
	isolateTelemetryHome(t)
	telemetry.SetTerminalForTest(t, true)
	st := telemetry.LoadState()
	if err := telemetry.Grant(st, "9.9.9", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.SaveState(st); err != nil {
		t.Fatal(err)
	}
}

// failingBackend is a non-git backend whose worktree creation fails.
type failingBackend struct{ vcs.Backend }

func (failingBackend) Type() vcs.Type                   { return vcs.TypeJujutsu }
func (failingBackend) RepoDir() string                  { return "/repo" }
func (failingBackend) CreateWorktree(_, _ string) error { return errors.New("workspace add failed") }

// A failed worktree create (add -w, launch -w) reports error area=worktree.
func TestWorktreeCreateFailureRecordsError(t *testing.T) {
	grantCLITelemetry(t)
	if _, err := createWorktreeWithSetup(failingBackend{}, "/repo/wt", "b", git.WorktreeCreateOptions{}, io.Discard, io.Discard, time.Second); err == nil {
		t.Fatal("createWorktreeWithSetup succeeded")
	}
	errs := cliSpooledErrors(t)
	if len(errs) != 1 || errs[0]["area"] != "worktree" || errs[0]["kind"] != "other" {
		t.Fatalf("error events = %v, want one area=worktree kind=other", errs)
	}
}
