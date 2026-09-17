package web

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

// Review P2-8: the web MenuSession carries substateDetail beside substate
// (the codex usage-limit retry time), omitted when empty. The instance reads
// a real pane rendering the captured codex banner (status-light audit A).
func TestMenuSession_SubstateDetailParity(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	panePath := filepath.Join(home, "pane.txt")
	pane := "■ You've hit your usage limit. To continue using Codex, start a free\n" +
		"trial of Plus today (https://chatgpt.com/explore/plus), or try again at Oct 10th, 2026 8:03 AM.\n" +
		"› Ask Codex to do anything\n"
	if err := os.WriteFile(panePath, []byte(pane), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := session.NewInstanceWithTool("codex-usage-limit-web", home, "codex")
	ts := inst.GetTmuxSession()
	if err := ts.Start(fmt.Sprintf("sh -c 'cat %q; exec sleep 3600'", panePath)); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	t.Cleanup(func() { _ = ts.Kill() })
	inst.Command = "codex"
	ts.Command = "codex"
	time.Sleep(2 * time.Second)

	// The background status pass is what fills the cache the menu reads.
	session.RefreshInstancesForCLIStatus([]*session.Instance{inst})
	if err := inst.UpdateStatus(); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(toMenuSession(inst))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["status"] != "error" || m["substate"] != "usage-limit" {
		t.Fatalf("status/substate = %v/%v, want error/usage-limit", m["status"], m["substate"])
	}
	if m["substateDetail"] != "try again at Oct 10th, 2026 8:03 AM" {
		t.Fatalf("substateDetail = %v", m["substateDetail"])
	}

	plain, _ := json.Marshal(toMenuSession(&session.Instance{ID: "c", Title: "c", Tool: "claude"}))
	m = map[string]interface{}{}
	if err := json.Unmarshal(plain, &m); err != nil {
		t.Fatal(err)
	}
	if _, present := m["substateDetail"]; present {
		t.Fatal("substateDetail must be omitted when empty")
	}
}
