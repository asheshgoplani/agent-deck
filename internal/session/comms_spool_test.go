package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommsSpoolWriteReadOrderAndRemove(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := time.Now().UnixMilli()
	for i, text := range []string{"first", "second", "third"} {
		err := WriteCommsSpool(CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Edge: CommsEdgeTurnEnd,
			Instance: "child-1", Text: text, TSignal: base + int64(i)})
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	entries, err := ReadCommsSpool("child-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Text != "first" || entries[2].Text != "third" {
		t.Fatalf("spool order: %+v", entries)
	}
	for _, e := range entries {
		if e.path == "" || !strings.HasSuffix(e.path, ".json") || e.Instance != "child-1" || e.TSignal == 0 {
			t.Fatalf("entry %+v", e)
		}
	}
	// tmp + rename: no temp file is left behind.
	files, _ := os.ReadDir(filepath.Dir(entries[0].path))
	for _, f := range files {
		if strings.Contains(f.Name(), ".tmp") {
			t.Fatalf("temp file left in spool: %s", f.Name())
		}
	}
	RemoveCommsSpoolEntry(entries[0])
	entries, _ = ReadCommsSpool("child-1")
	if len(entries) != 2 || entries[0].Text != "second" {
		t.Fatalf("after remove: %+v", entries)
	}
	if got := ListCommsSpoolInstances(); len(got) != 1 || got[0] != "child-1" {
		t.Fatalf("instances: %v", got)
	}
	if got, _ := ReadCommsSpool("nobody"); got != nil {
		t.Fatalf("unknown instance: %+v", got)
	}
}

func TestCommsSpoolRejectsEmptyTurnsBadEdgesAndCapsText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := WriteCommsSpool(CommsSpoolEntry{Harness: "claude", Edge: CommsEdgeTurnEnd, Instance: "c", Text: "   "}); err != nil {
		t.Fatalf("empty text must be a silent no-op: %v", err)
	}
	if got, _ := ReadCommsSpool("c"); len(got) != 0 {
		t.Fatalf("empty turn was spooled: %+v", got)
	}
	if err := WriteCommsSpool(CommsSpoolEntry{Harness: "claude", Edge: "nope", Instance: "c", Text: "x"}); err == nil {
		t.Fatal("unknown edge accepted")
	}
	if err := WriteCommsSpool(CommsSpoolEntry{Harness: "claude", Edge: CommsEdgeTurnEnd, Text: "x"}); err == nil {
		t.Fatal("empty instance accepted")
	}
	long := strings.Repeat("y", 10000)
	if err := WriteCommsSpool(CommsSpoolEntry{Harness: "gemini", Edge: CommsEdgeTurnEnd, Instance: "c", Text: long, Prompt: long}); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadCommsSpool("c")
	if len(got) != 1 || len(got[0].Text) > commsSpoolTextBytes || len(got[0].Prompt) > commsSpoolPromptBytes {
		t.Fatalf("caps not applied: text %d prompt %d", len(got[0].Text), len(got[0].Prompt))
	}
	// A prompt-start edge needs no text.
	if err := WriteCommsSpool(CommsSpoolEntry{Harness: "gemini", Edge: CommsEdgePromptStart, Instance: "c", Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadCommsSpool("c"); len(got) != 2 || got[1].Prompt != "hello" {
		t.Fatalf("prompt edge: %+v", got)
	}
}

func TestCommsSpoolPruneDropsStaleEntriesAndEmptyDirs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := WriteCommsSpool(CommsSpoolEntry{Harness: "codex", Edge: CommsEdgeTurnEnd, Instance: "old", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteCommsSpool(CommsSpoolEntry{Harness: "codex", Edge: CommsEdgeTurnEnd, Instance: "fresh", Text: "y"}); err != nil {
		t.Fatal(err)
	}
	old, _ := ReadCommsSpool("old")
	past := time.Now().Add(-2 * commsSpoolMaxAge)
	if err := os.Chtimes(old[0].path, past, past); err != nil {
		t.Fatal(err)
	}
	PruneCommsSpool(time.Now())
	if got := ListCommsSpoolInstances(); len(got) != 1 || got[0] != "fresh" {
		t.Fatalf("after prune: %v", got)
	}
}

func TestCommsSpoolFullIsAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := commsSpoolInstanceDir("busy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < commsSpoolMaxFiles; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%026d.json", i)), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteCommsSpool(CommsSpoolEntry{Harness: "codex", Edge: CommsEdgeTurnEnd, Instance: "busy", Text: "x"}); err == nil {
		t.Fatal("full spool accepted a write")
	}
}

func TestCommsPromptTrigger(t *testing.T) {
	cases := []struct {
		prompt, trigger, from string
	}{
		{"", TurnTriggerUnknown, ""},
		{"fix the bug", TurnTriggerHuman, ""},
		{"[agent-deck from:conductor-x] please report", TurnTriggerSend, "conductor-x"},
		{"[agent-deck from:", TurnTriggerSend, ""},
		{"[INBOX #abc] child finished", TurnTriggerInbox, ""},
		{"[HEARTBEAT] check", TurnTriggerInbox, ""},
		{"[agent-deck msg] 2 pending", TurnTriggerInbox, ""},
		{"<task-notification>x</task-notification>", TurnTriggerTask, ""},
		{"<system-reminder>y</system-reminder>", TurnTriggerSystem, ""},
	}
	for _, c := range cases {
		trigger, from := commsPromptTrigger(c.prompt)
		if trigger != c.trigger || from != c.from {
			t.Errorf("commsPromptTrigger(%q) = %q,%q want %q,%q", c.prompt, trigger, from, c.trigger, c.from)
		}
	}
}

func TestCommsLedgerEnabledReadsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	if CommsLedgerEnabled() {
		t.Fatal("ledger must be off by default")
	}
	cfgDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[comms]\nledger = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	if !CommsLedgerEnabled() {
		t.Fatal("[comms] ledger = true not honoured")
	}
	restore := SetCommsLedgerForTest(false)
	if CommsLedgerEnabled() {
		t.Fatal("override not honoured")
	}
	restore()
}

// G5: hostile spool contents are rejected and removed, never ingested.
func TestCommsSpoolRejectsSymlinksOversizedAndMalformedEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := WriteCommsSpool(CommsSpoolEntry{Harness: "codex", Edge: CommsEdgeTurnEnd, Instance: "victim", Text: "good"}); err != nil {
		t.Fatal(err)
	}
	dir := commsSpoolInstanceDir("victim")
	secret := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(secret, []byte(`{"edge":"turn_end","instance":"victim","text":"leaked"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "00000000000000000000000001.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000000000002.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, commsSpoolMaxBytes+10)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000000000003.json"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "00000000000000000000000004.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadCommsSpool("victim")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Text != "good" {
		t.Fatalf("hostile entries ingested: %+v", entries)
	}
	for _, name := range []string{"00000000000000000000000001.json", "00000000000000000000000002.json", "00000000000000000000000003.json"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			t.Fatalf("rejected entry %s not removed", name)
		}
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatal("the symlink target must be untouched")
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("spool dir mode %v", info.Mode().Perm())
	}
	if info, err := os.Stat(entries[0].path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("spool file mode %v", info.Mode().Perm())
	}
}
