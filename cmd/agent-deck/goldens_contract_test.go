package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestGoldensReviewContract(t *testing.T) {
	wantJSON := []string{
		"accounts", "agents", "doctor", "status", "usage", "costs_summary",
		"session_show", "session_viewers", "fleet_status", "mcp_attached",
		"skill_list", "skill_attached", "group_list", "group_show",
		"conductor_status", "conductor_list", "worktree_list", "inbox_export",
		"inbox_writer_status", "watcher_list",
	}
	seen := make(map[string]bool)
	for _, spec := range safeSpecs() {
		seen[spec.name] = true
	}
	for _, name := range wantJSON {
		if !seen[name+"_json"] {
			t.Errorf("missing JSON golden for %s", name)
		}
	}
	if !seen["costs_recompute_dry_run"] {
		t.Error("missing safe costs recompute --dry-run golden")
	}
	for _, path := range []string{"skill source list", "inbox dead-letter list", "inbox dead-letter show", "inbox dead-letter retry", "inbox dead-letter purge"} {
		if !seenHelpPath(path) {
			t.Errorf("missing leaf help golden for %s", path)
		}
	}
	for _, address := range []string{"127.0.0.1:8080", "0.0.0.0:3000", "192.168.1.1"} {
		if got := scrub(address, ""); got != address {
			t.Errorf("scrubbed address %q into %q", address, got)
		}
	}
}

func TestGoldenStreamsStaySeparate(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	out, errOut, exit := runGoldensStreamsIn(t, sh, os.Environ(), "", []string{"-c", "printf output; printf diagnostic >&2"})
	if out != "output" || errOut != "diagnostic" || exit != 0 {
		t.Fatalf("stdout=%q stderr=%q exit=%d", out, errOut, exit)
	}
}

func TestStorageGoldenIncludesEveryPersistedColumn(t *testing.T) {
	home, _ := goldensSandbox(t)
	profileDir, err := session.GetProfileDir(goldensProfile)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Instances []map[string]any `json:"instances"`
	}
	if err := json.Unmarshal([]byte(dumpStateDBRows(t, filepath.Join(profileDir, "state.db"))), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Instances) == 0 {
		t.Fatal("missing seeded instance rows")
	}
	for _, column := range []string{"sort_order", "command", "wrapper", "tmux_socket_name", "created_at", "last_accessed", "tool_data", "acknowledged"} {
		if _, ok := doc.Instances[0][column]; !ok {
			t.Errorf("storage golden omits persisted column %s under %s", column, home)
		}
	}
}

func seenHelpPath(path string) bool {
	for _, spec := range helpSpecs() {
		if strings.Join(spec.args, " ") == path+" --help" {
			return true
		}
	}
	return false
}
