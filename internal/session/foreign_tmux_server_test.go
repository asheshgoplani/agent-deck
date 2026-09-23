package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// rc feedback 2026-09-23 (1.16.17-rc1): from 10:53:34 local every live
// session of the personal profile, Claude (Fable 5.1 and Opus), Codex and
// shell alike, flipped running -> error again and again while working, and
// each flip landed in the parent's inbox. The notify daemon was only the
// messenger: a TUI is alive, so it publishes the rows of the shared status
// table. The rows came from a second `agent-deck -p personal` TUI that a UX
// audit session had started inside a private tmux server (`tmux -L uxaudit`,
// AGENT_DECK_ALLOW_OUTER_TMUX=1) 4 s before the first flip. Its socket-less
// tmux calls followed $TMUX to that private server, which holds none of the
// fleet's sessions, so every session read as gone and it wrote "error".
//
// The Claude transcripts at the flip offsets ("jsonl:<size>" is only the
// transcript size, the dedup key) hold ordinary tool calls and results: no
// isApiErrorMessage record, nothing a status rule reads as an error.

var (
	foreignTUIBinOnce sync.Once
	foreignTUIBin     string
	foreignTUIBinErr  error
)

// foreignTUIBinary builds the agent-deck binary once, with the caller's
// environment (call it before HOME is redirected, or the module cache moves).
func foreignTUIBinary(t *testing.T) string {
	t.Helper()
	foreignTUIBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ad-foreign-bin-")
		if err != nil {
			foreignTUIBinErr = err
			return
		}
		foreignTUIBin = filepath.Join(dir, "agent-deck")
		out, err := exec.Command("go", "build", "-o", foreignTUIBin, "../../cmd/agent-deck").CombinedOutput()
		if err != nil {
			foreignTUIBinErr = fmt.Errorf("%v: %s", err, out)
		}
	})
	if foreignTUIBinErr != nil {
		t.Fatalf("build agent-deck: %v", foreignTUIBinErr)
	}
	return foreignTUIBin
}

// startTUIInForeignTmuxServer replays the audit's command: a private tmux
// server (tmux -L uxaudit) whose pane runs `AGENT_DECK_ALLOW_OUTER_TMUX=1
// agent-deck -p <profile>`. tmux itself sets the pane's $TMUX to the private
// server, exactly as it did for the nested TUI. Returns a pane capture func
// for diagnostics.
func startTUIInForeignTmuxServer(t *testing.T, bin, profile string) func() string {
	t.Helper()
	configPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("[claude]\nhooks_enabled = false\n"), 0o600); err != nil { // no first-run dialogs
		t.Fatal(err)
	}
	sock, cleanup := testutil.ShortTmuxSocket()
	t.Cleanup(cleanup)
	cmdline := fmt.Sprintf("exec env HOME=%q TMUX_TMPDIR=%q AGENT_DECK_ALLOW_OUTER_TMUX=1 AGENTDECK_SKIP_UPDATE_CHECK=1 AGENTDECK_TELEMETRY=0 TERM=xterm-256color %q -p %q",
		os.Getenv("HOME"), os.Getenv("TMUX_TMPDIR"), bin, profile)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := tmux.ExecContext(ctx, "", "-S", sock, "new-session", "-d", "-s", "tui", "-x", "160", "-y", "45", cmdline).CombinedOutput(); err != nil {
		t.Fatalf("foreign tmux server: %v: %s", err, out)
	}
	return func() string {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, _ := tmux.ExecContext(ctx, "", "-S", sock, "capture-pane", "-p", "-t", "tui").CombinedOutput()
		return string(out)
	}
}

// startFramePane starts a pane on the default (test-isolated) tmux server that
// prints frame and stays alive, without touching HOME.
func startFramePane(t *testing.T, tool, name, frame string) *Instance {
	t.Helper()
	skipIfNoTmuxBinary(t)
	panePath := filepath.Join(t.TempDir(), "pane.txt")
	if err := os.WriteFile(panePath, []byte(frame), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := NewInstanceWithTool(name, t.TempDir(), tool)
	inst.Command = tool
	inst.tmuxSession.Command = tool
	if err := inst.tmuxSession.Start(fmt.Sprintf("sh -c 'cat %q; exec sleep 3600'", panePath)); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	t.Cleanup(func() { _ = inst.tmuxSession.Kill() })
	time.Sleep(2 * time.Second)
	return inst
}

const uxAuditFrame = "claude-local-macapp-ux-audit-lollygagging_8a428ef2"

// The incident end to end. The child shows the real Fable 5.1 frame of
// macapp-ux-audit on the default tmux server; the parent's TUI is alive, so
// the notify daemon publishes the shared status rows. A second TUI starts
// inside a private tmux server. It must not write error for the live child,
// and the daemon must send the parent nothing. A child that is gone on every
// server still reads error and still reaches the parent.
func TestForeignTmuxServer_NestedTUIDoesNotFlipLiveSessionsToError(t *testing.T) {
	skipIfNoTmuxBinary(t)
	bin := foreignTUIBinary(t)
	for n, c := range []struct {
		name string
		gone bool
		want int
	}{
		{"session-alive", false, 0},
		{"session-gone", true, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			profile := fmt.Sprintf("_test_foreign_tmux_tui_%d", n)
			d, storage := bootstrapDaemonProfile(t, profile)
			ResetInboxFingerprintCacheForTest()
			t.Cleanup(ResetInboxFingerprintCacheForTest)

			child := startFramePane(t, "claude", "macapp-ux-audit", piCorpusFrame(t, uxAuditFrame))
			parentID := fmt.Sprintf("foreign-parent-%d", n)
			child.ParentSessionID = parentID
			child.Status = StatusRunning
			parent := &Instance{ID: parentID, Title: "conductor", ProjectPath: "/tmp/" + parentID, GroupPath: DefaultGroupPath,
				Tool: "claude", Status: StatusRunning, CreatedAt: time.Now()}
			if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
				t.Fatal(err)
			}
			db := storage.GetDB()
			if err := db.RegisterInstance(false); err != nil { // the operator's own TUI
				t.Fatal(err)
			}
			for _, id := range []string{child.ID, parentID} {
				if err := db.WriteStatus(id, "running", "claude"); err != nil {
					t.Fatal(err)
				}
			}
			d.syncProfile(profile) // baseline: running
			if c.gone {
				_ = child.tmuxSession.Kill()
			}

			capture := startTUIInForeignTmuxServer(t, bin, profile)
			sawNestedTUI := false
			childStatus := ""
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(500 * time.Millisecond)
				if count, err := db.AliveInstanceCount(); err == nil && count >= 2 {
					sawNestedTUI = true
				}
				rows, err := db.ReadAllStatuses()
				if err != nil {
					t.Fatal(err)
				}
				childStatus = rows[child.ID].Status
				if childStatus == string(StatusError) {
					break
				}
			}
			if !sawNestedTUI {
				t.Fatalf("the nested TUI never registered; pane:\n%s", capture())
			}
			if wantErr := c.gone; (childStatus == string(StatusError)) != wantErr {
				t.Fatalf("nested TUI left the child row %q (gone=%v); pane:\n%s", childStatus, c.gone, capture())
			}

			d.syncProfile(profile)
			events, err := DrainInboxForParent(parentID)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != c.want {
				t.Fatalf("daemon delivered %d events, want %d: %+v", len(events), c.want, events)
			}
			for _, ev := range events {
				if ev.ToStatus != string(StatusError) {
					t.Fatalf("event to %q, want error for a session that is gone", ev.ToStatus)
				}
			}
		})
	}
}

// The redacted transcript records at every flip offset of both affected
// sessions. None is an API error record, and the transcript rule forms no
// usage-limit verdict from them: the transcript did not cause the flips.
func TestForeignTmuxServer_FlipTranscriptTailsCarryNoError(t *testing.T) {
	path := filepath.Join("testdata", "claude-error-flips-20260923", "transcript-tails.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r transcriptRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("record %d: %v", records, err)
		}
		if r.IsAPIErrorMessage || r.Error != "" {
			t.Fatalf("record %d is an API error record: %s", records, sc.Text())
		}
		records++
	}
	if records == 0 {
		t.Fatal("no transcript records")
	}
	at, _ := time.Parse(time.RFC3339, "2026-09-23T08:55:00Z")
	if limited, _ := latestAssistantTurnIsRateLimited(path, at); limited {
		t.Fatal("transcript tails read as usage-limited")
	}
}
