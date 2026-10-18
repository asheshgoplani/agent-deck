package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

// The generated heartbeat script, run by its unit, scans as ONE row keyed
// heartbeat:<name>, aimed at conductor-<name> in the script's profile, not
// at a literal "$PROFILE" (review of #2738, M1).
func TestGeneratedHeartbeatScansAsOneLiveRow(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "heartbeat.sh")
	if err := os.WriteFile(script, []byte(renderConductorHeartbeatScript("ops", "work")), 0o755); err != nil {
		t.Fatal(err)
	}
	units := filepath.Join(dir, "systemd")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(units, "agent-deck-conductor-heartbeat-ops.service"), []byte("[Service]\nExecStart=/bin/bash "+script+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	found := triggers.Scan(triggers.ScanDirs{SystemdUser: units})
	if len(found) != 1 {
		t.Fatalf("found %+v", found)
	}
	f := found[0]
	if f.ID() != "heartbeat:ops" || f.Target != "conductor-ops" || f.TargetProfile != "work" || f.Verb != "send" {
		t.Fatalf("heartbeat scanned as %s target %q profile %q verb %q", f.ID(), f.Target, f.TargetProfile, f.Verb)
	}
	tagged := triggers.Resolve("heartbeat:ops", "", triggers.Env{Getenv: func(string) string { return "" }})
	if tagged.ID() != f.ID() {
		t.Fatalf("send keys %s, scan keys %s", tagged.ID(), f.ID())
	}
}
