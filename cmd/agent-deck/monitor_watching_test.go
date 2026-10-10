package main

import (
	"encoding/json"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestMonitorWatchingJSONParity(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	frame := readAcceptanceFixture(t, "..", "..", "internal", "session", "testdata", "background_work", "idle-monitor.txt")
	tx := strings.Split(strings.TrimSpace(readAcceptanceFixture(t, "..", "..", "internal", "session", "testdata", "background_work", "idle-monitor.jsonl")), "\n")
	pane := newAcceptancePane(t, home)
	pane.show(t, frame)
	inst := session.NewInstanceWithTool("observer", home, "claude")
	ts := inst.GetTmuxSession()
	if err := ts.Start(pane.command); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ts.Kill() })
	inst.Command = "claude"
	ts.Command = "claude"
	inst.ClaudeSessionID = "sess-610"
	time.Sleep(2 * time.Second)
	acceptanceTranscript(t, inst, tx, 1)
	acceptanceHook(t, inst.ID, "waiting", "Stop")
	storage, err := session.NewStorageWithProfile("_test-monitor")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.Close() })
	for _, fresh := range []bool{false, true} {
		target := inst
		if fresh {
			target = acceptanceReload(t, storage, inst)
		}
		for name, fields := range map[string]map[string]interface{}{"show": sessionShowStatusFields(target), "list": acceptanceListRow(t, target)} {
			b, _ := json.Marshal(fields)
			t.Logf("fresh=%v %s %s", fresh, name, b)
			if fields["status"] != "waiting" || fields["substate"] != "watching" {
				t.Fatalf("%s: %s", name, b)
			}
			work, _ := json.Marshal(fields["background_work"])
			var w map[string]interface{}
			_ = json.Unmarshal(work, &w)
			if w["kind"] != "monitor" || w["count"] != float64(1) {
				t.Fatalf("%s background_work=%s", name, work)
			}
			if !strings.Contains(fields["substate_detail"].(string), "1 watching") {
				t.Fatal(fields)
			}
		}
		if target.CachedSubstate() != session.SubstateWatching {
			t.Fatal("TUI cached substate disagrees")
		}
	}
}
