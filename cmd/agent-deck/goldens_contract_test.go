package main

import (
	"strings"
	"testing"
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

func seenHelpPath(path string) bool {
	for _, spec := range helpSpecs() {
		if strings.Join(spec.args, " ") == path+" --help" {
			return true
		}
	}
	return false
}
