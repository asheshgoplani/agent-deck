package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// substateDetailFixturePane is the captured codex usage-limit tail (status-light
// audit 2026-09-17, defect A); the retry time it prints is the only
// substate_detail value that exists today.
const substateDetailFixturePane = "■ You've hit your usage limit. To continue using Codex and get access to GPT-5.3-Codex, start a free\n" +
	"trial of Plus today (https://chatgpt.com/explore/plus), or try again at Oct 10th, 2026 8:03 AM.\n" +
	"› Ask Codex to do anything\n" +
	"  gpt-5.6-luna · ~/work/disposable-project · Context 100% left…\n"

const substateDetailFixtureWant = "try again at Oct 10th, 2026 8:03 AM"

// startCodexBannerInstance renders the fixture in an isolated tmux pane and
// returns a codex instance reading it.
func startCodexBannerInstance(t *testing.T) *session.Instance {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	panePath := filepath.Join(home, "pane.txt")
	if err := os.WriteFile(panePath, []byte(substateDetailFixturePane), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := session.NewInstanceWithTool("codex-usage-limit", home, "codex")
	ts := inst.GetTmuxSession()
	if err := ts.Start(fmt.Sprintf("sh -c 'cat %q; exec sleep 3600'", panePath)); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	t.Cleanup(func() { _ = ts.Kill() })
	inst.Command = "codex"
	ts.Command = "codex"
	time.Sleep(2 * time.Second) // past UpdateStatus's tmux grace window
	return inst
}

// Review P2-8: substate_detail rides beside substate on every CLI surface,
// with the same omitempty contract. `list --json` is exercised end to end
// through buildListJSON on a real pane; the other shapes are pinned by their
// JSON tags.
func TestSubstateDetail_ListJSONParity(t *testing.T) {
	inst := startCodexBannerInstance(t)
	session.RefreshInstancesForCLIStatus([]*session.Instance{inst})
	out, err := buildListJSON("_test", []*session.Instance{inst})
	if err != nil {
		t.Fatalf("buildListJSON: %v", err)
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row["status"] != "error" || row["substate"] != "usage-limit" {
		t.Fatalf("status/substate = %v/%v, want error/usage-limit", row["status"], row["substate"])
	}
	if row["substate_detail"] != substateDetailFixtureWant {
		t.Fatalf("substate_detail = %v, want %q", row["substate_detail"], substateDetailFixtureWant)
	}

	// A session with no detail omits the key (existing consumers unaffected).
	plain, err := buildListJSON("_test", []*session.Instance{{ID: "claude", Title: "claude", Tool: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	var plainRows []map[string]interface{}
	if err := json.Unmarshal(plain, &plainRows); err != nil {
		t.Fatal(err)
	}
	if _, present := plainRows[0]["substate_detail"]; present {
		t.Fatal("substate_detail must be omitted when empty")
	}
}

func TestSubstateDetail_StaleCandidateShape(t *testing.T) {
	out, err := json.Marshal(staleCandidate{ID: "x", Substate: "usage-limit", SubstateDetail: substateDetailFixtureWant})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["substate_detail"] != substateDetailFixtureWant {
		t.Fatalf("status --stale --json: substate_detail = %v", m["substate_detail"])
	}
	out, _ = json.Marshal(staleCandidate{ID: "x"})
	m = map[string]interface{}{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if _, present := m["substate_detail"]; present {
		t.Fatal("status --stale --json: substate_detail must be omitted when empty")
	}
}
