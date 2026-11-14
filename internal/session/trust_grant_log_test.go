package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// trustGrantRecords decodes buf and returns every folder_trust_granted record.
func trustGrantRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v\n%s", err, line)
		}
		if rec["msg"] == "folder_trust_granted" {
			out = append(out, rec)
		}
	}
	return out
}

// assertOneTrustGrant checks buf holds exactly one folder_trust_granted record
// naming tool, dir and the given location attribute.
func assertOneTrustGrant(t *testing.T, buf *bytes.Buffer, tool, dir, whereKey, where string) {
	t.Helper()
	recs := trustGrantRecords(t, buf)
	if len(recs) != 1 {
		t.Fatalf("folder_trust_granted records = %d, want 1:\n%s", len(recs), buf.String())
	}
	rec := recs[0]
	if rec["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", rec["level"])
	}
	if rec["tool"] != tool {
		t.Errorf("tool = %v, want %q", rec["tool"], tool)
	}
	if rec["dir"] != dir {
		t.Errorf("dir = %v, want %q", rec["dir"], dir)
	}
	if rec[whereKey] != where {
		t.Errorf("%s = %v, want %q", whereKey, rec[whereKey], where)
	}
}

func TestFolderTrustGrantLog_Claude(t *testing.T) {
	buf := captureSessionLogJSON(t)
	cfg := filepath.Join(t.TempDir(), ".claude.json")
	dir := filepath.Join(t.TempDir(), "proj")

	if err := PreAcceptClaudeTrust(cfg, dir); err != nil {
		t.Fatalf("first PreAcceptClaudeTrust: %v", err)
	}
	assertOneTrustGrant(t, buf, "claude", dir, "config", cfg)

	buf.Reset()
	if err := PreAcceptClaudeTrust(cfg, dir); err != nil {
		t.Fatalf("second PreAcceptClaudeTrust: %v", err)
	}
	if recs := trustGrantRecords(t, buf); len(recs) != 0 {
		t.Fatalf("already-trusted dir logged %d grants:\n%s", len(recs), buf.String())
	}
}

func TestFolderTrustGrantLog_ClaudePreexistingTrust(t *testing.T) {
	buf := captureSessionLogJSON(t)
	cfg := filepath.Join(t.TempDir(), ".claude.json")
	dir := filepath.Join(t.TempDir(), "proj")
	seed, _ := json.Marshal(map[string]any{"projects": map[string]any{dir: map[string]any{"hasTrustDialogAccepted": true}}})
	if err := os.WriteFile(cfg, seed, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := PreAcceptClaudeTrust(cfg, dir); err != nil {
		t.Fatalf("PreAcceptClaudeTrust: %v", err)
	}
	if recs := trustGrantRecords(t, buf); len(recs) != 0 {
		t.Fatalf("pre-trusted dir logged %d grants:\n%s", len(recs), buf.String())
	}
}

func TestFolderTrustGrantLog_Codex(t *testing.T) {
	buf := captureSessionLogJSON(t)
	cfg := filepath.Join(t.TempDir(), "config.toml")
	dir := filepath.Join(t.TempDir(), "proj")

	if err := PreAcceptCodexTrust(cfg, dir); err != nil {
		t.Fatalf("first PreAcceptCodexTrust: %v", err)
	}
	assertOneTrustGrant(t, buf, "codex", dir, "config", cfg)

	buf.Reset()
	if err := PreAcceptCodexTrust(cfg, dir); err != nil {
		t.Fatalf("second PreAcceptCodexTrust: %v", err)
	}
	if recs := trustGrantRecords(t, buf); len(recs) != 0 {
		t.Fatalf("already-trusted dir logged %d grants:\n%s", len(recs), buf.String())
	}
}

func TestFolderTrustGrantLog_CursorLocal(t *testing.T) {
	buf := captureSessionLogJSON(t)
	cursorDir := filepath.Join(t.TempDir(), ".cursor")
	dir := filepath.Join(t.TempDir(), "proj")
	trustPath, _, err := cursorWorkspaceTrustPath(cursorDir, dir)
	if err != nil {
		t.Fatalf("cursorWorkspaceTrustPath: %v", err)
	}

	if err := PreAcceptCursorTrust(cursorDir, dir); err != nil {
		t.Fatalf("first PreAcceptCursorTrust: %v", err)
	}
	assertOneTrustGrant(t, buf, "cursor", dir, "config", trustPath)

	buf.Reset()
	if err := PreAcceptCursorTrust(cursorDir, dir); err != nil {
		t.Fatalf("second PreAcceptCursorTrust: %v", err)
	}
	if recs := trustGrantRecords(t, buf); len(recs) != 0 {
		t.Fatalf("already-trusted dir logged %d grants:\n%s", len(recs), buf.String())
	}
}

func TestFolderTrustGrantLog_CursorSSH(t *testing.T) {
	fakeBin := t.TempDir()
	remoteHome := t.TempDir()
	// Fake ssh: drop -o options and the host, then run the script locally
	// with HOME pointed at a temp "remote" home.
	if err := os.WriteFile(filepath.Join(fakeBin, "ssh"), []byte(`#!/bin/sh
while [ "$1" = "-o" ]; do shift 2; done
shift
export HOME="$FAKE_REMOTE_HOME"
exec sh -s
`), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_REMOTE_HOME", remoteHome)
	buf := captureSessionLogJSON(t)
	dir := "/remote/proj"

	if err := PreAcceptCursorTrustSSH("user@remote", dir); err != nil {
		t.Fatalf("first PreAcceptCursorTrustSSH: %v", err)
	}
	assertOneTrustGrant(t, buf, "cursor", dir, "host", "user@remote")

	buf.Reset()
	if err := PreAcceptCursorTrustSSH("user@remote", dir); err != nil {
		t.Fatalf("second PreAcceptCursorTrustSSH: %v", err)
	}
	if recs := trustGrantRecords(t, buf); len(recs) != 0 {
		t.Fatalf("already-trusted dir logged %d grants:\n%s", len(recs), buf.String())
	}
}

func TestFolderTrustGrantLog_ClaudePreseed(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real-proj")
	link := filepath.Join(root, "link-proj")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "dst")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dst, ".claude.json")
	// One spelling is already trusted: only the other is a new grant.
	seed, _ := json.Marshal(map[string]any{"projects": map[string]any{link: map[string]any{"hasTrustDialogAccepted": true}}})
	if err := os.WriteFile(cfg, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	buf := captureSessionLogJSON(t)

	if _, err := PreseedClaudeFirstStart("", dst, link, false); err != nil {
		t.Fatalf("first PreseedClaudeFirstStart: %v", err)
	}
	assertOneTrustGrant(t, buf, "claude", resolvedReal, "config", cfg)

	buf.Reset()
	if _, err := PreseedClaudeFirstStart("", dst, link, false); err != nil {
		t.Fatalf("second PreseedClaudeFirstStart: %v", err)
	}
	if recs := trustGrantRecords(t, buf); len(recs) != 0 {
		t.Fatalf("already-trusted dir logged %d grants:\n%s", len(recs), buf.String())
	}
}

func TestFolderTrustGrantLog_ClaudePreseedEverySpelling(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real-proj")
	link := filepath.Join(root, "link-proj")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "dst")
	cfg := filepath.Join(dst, ".claude.json")
	buf := captureSessionLogJSON(t)

	if _, err := PreseedClaudeFirstStart("", dst, link, false); err != nil {
		t.Fatalf("PreseedClaudeFirstStart: %v", err)
	}
	recs := trustGrantRecords(t, buf)
	got := map[string]bool{}
	for _, rec := range recs {
		if rec["tool"] != "claude" || rec["config"] != cfg {
			t.Errorf("record = %v, want tool=claude config=%s", rec, cfg)
		}
		got[rec["dir"].(string)] = true
	}
	if len(recs) != 2 || !got[link] || !got[resolvedReal] {
		t.Fatalf("grants = %v, want one each for %s and %s:\n%s", got, link, resolvedReal, buf.String())
	}
}

func TestFolderTrustGrantLog_SettleDialogKeypress(t *testing.T) {
	cfgDir := t.TempDir()
	buf := captureSessionLogJSON(t)
	f := &fakeSettle{procs: liveProcs("inst-1", cfgDir), now: time.Unix(0, 0)}
	f.set(trustPane(settleProject))
	f.onKey = trustDialogKeys(settleProject, readyPane)
	if report, err := SettleSwitch(f.deps(), settleOpts(cfgDir)); err != nil {
		t.Fatalf("settle: %v (%s)", err, report.Receipt())
	}
	assertOneTrustGrant(t, buf, "claude", settleProject, "config_dir", cfgDir)
	if via := trustGrantRecords(t, buf)[0]["via"]; via != "dialog_keypress" {
		t.Errorf("via = %v, want dialog_keypress", via)
	}
}

func TestFolderTrustGrantLog_SanitizesValues(t *testing.T) {
	buf := captureSessionLogJSON(t)
	cfg := filepath.Join(t.TempDir(), ".claude.json")
	dir := "/tmp/proj\nlevel=ERROR msg=forged\r"

	if err := PreAcceptClaudeTrust(cfg, dir); err != nil {
		t.Fatalf("PreAcceptClaudeTrust: %v", err)
	}
	assertOneTrustGrant(t, buf, "claude", logging.SanitizeValue(dir), "config", cfg)
	if d, _ := trustGrantRecords(t, buf)[0]["dir"].(string); strings.ContainsAny(d, "\n\r") {
		t.Errorf("dir carries a raw control character: %q", d)
	}
}
