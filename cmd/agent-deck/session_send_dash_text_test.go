package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionSendDashTextAfterDoubleDashIsTheMessage: a chat bridge relays
// whatever a person typed, so a message that starts with "-" must reach the
// target verbatim when it follows "--", never be parsed as a send flag or a
// global flag. Each send's journal record carries a keyed hash of the text
// that was actually delivered.
func TestSessionSendDashTextAfterDoubleDashIsTheMessage(t *testing.T) {
	parallelSubprocessTest(t)
	skipIfNoTmuxBinaryCLI(t)
	home := t.TempDir()
	var extra []string
	if d := os.Getenv("TMUX_TMPDIR"); d != "" {
		extra = append(extra, "TMUX_TMPDIR="+d)
	}
	run := func(args ...string) (string, string, int) {
		return runAgentDeckEnv(t, home, "", extra, args...)
	}
	project := filepath.Join(home, "sh")
	_ = os.MkdirAll(project, 0o755)
	stdout, stderr, code := run("add", "-t", "dash-shell", "-c", "shell", "--no-parent", "--json", project)
	if code != 0 {
		t.Fatalf("add: %d %s %s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(stdout), &added)
	if stdout, stderr, code := run("session", "start", added.ID, "--json"); code != 0 {
		t.Fatalf("start: %d %s %s", code, stdout, stderr)
	}
	t.Cleanup(func() { _, _, _ = run("session", "stop", added.ID) })
	time.Sleep(time.Second)

	secret := filepath.Join(home, "not-for-chat.txt")
	if err := os.WriteFile(secret, []byte("file body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	texts := []string{"--help-me", "-x", "--run-hooks", "--json", "--message-file=" + secret}
	for _, text := range texts {
		// The chat bridge's shape: flags, then "--", then target and text.
		if stdout, stderr, code := run("session", "send", "--no-wait", "--ignore-budget", "--", added.ID, text); code != 0 {
			t.Fatalf("send %q (flags first): exit %d\nstdout: %s\nstderr: %s", text, code, stdout, stderr)
		}
	}
	// The shape typed by hand: target, then "--", then the text.
	if stdout, stderr, code := run("session", "send", added.ID, "--no-wait", "--", "--trust"); code != 0 {
		t.Fatalf("send --trust (target first): exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	texts = append(texts, "--trust")

	sends := readJournalSends(t, home)
	if len(sends) != len(texts) {
		t.Fatalf("journaled %d sends, want %d: %v", len(sends), len(texts), sends)
	}
	got := map[string]bool{}
	for _, ev := range sends {
		detail, _ := ev["detail"].(map[string]any)
		hash, _ := detail["text_hmac"].(string)
		got[hash] = true
	}
	for _, text := range texts {
		if !got[keyedTextHash(t, home, text)] {
			t.Errorf("no send delivered the text %q verbatim (journal: %v)", text, sends)
		}
	}
}
