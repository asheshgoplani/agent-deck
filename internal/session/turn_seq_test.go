package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Issue #3012 step 1: the hook file's turn_seq is read back, and both start
// tokens record it, so a judge can later ask "a turn since the start?" as
// turn_seq > tok.TurnSeq. No judge reads it yet.

func writeHookFileForTest(t *testing.T, id, body string) {
	t.Helper()
	path := hookStatusFilePath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIssue3012_ReadHookTurnSeq(t *testing.T) {
	isolatedHomeDir(t)
	writeHookFileForTest(t, "seq-new", `{"status":"waiting","event":"Stop","ts":1700000000,"turn_seq":42}`)
	writeHookFileForTest(t, "seq-legacy", `{"status":"waiting","event":"Stop","ts":1700000000}`)
	if hs := readHookStatusFile("seq-new"); hs == nil || hs.TurnSeq != 42 {
		t.Fatalf("readHookStatusFile turn_seq: %+v, want 42", hs)
	}
	if got := readHookTurnSeq("seq-new"); got != 42 {
		t.Fatalf("readHookTurnSeq = %d, want 42", got)
	}
	if got := readHookTurnSeq("seq-legacy"); got != 0 {
		t.Fatalf("a legacy hook file reads %d, want 0", got)
	}
	if got := readHookTurnSeq("seq-missing"); got != 0 {
		t.Fatalf("no hook file reads %d, want 0", got)
	}
}

func TestIssue3012_RestartTokenRecordsTurnSeq(t *testing.T) {
	f := newTurnTestFixture(t)
	stubRestartPIDs(t)
	f.finishedTurn(t)
	writeHookFileForTest(t, f.child.ID, `{"status":"waiting","event":"Stop","ts":1700000000,"turn_seq":7}`)
	noteRestartToken(f.child, time.Now())
	tok, ok := readRestartToken(f.child.ID)
	if !ok || tok.TurnSeq != 7 {
		t.Fatalf("restart token %+v ok=%v, want turn_seq 7", tok, ok)
	}
}

func TestIssue3012_FreshStartTokenRecordsTurnSeq(t *testing.T) {
	home := isolatedHomeDir(t)
	installClaudeHooks(t, home)
	inst := newGuardInstance(t, home)
	inst.ID = "fresh-3012"
	writeHookFileForTest(t, inst.ID, `{"status":"dead","event":"SessionEnd","ts":1700000000,"turn_seq":5}`)
	_ = inst.buildClaudeCommand("claude")
	noteFreshStart(inst, time.Now())
	tok, ok := readFreshStartToken(inst.ID)
	if !ok || tok.TurnSeq == nil || *tok.TurnSeq != 5 {
		t.Fatalf("fresh-start token %+v ok=%v, want turn_seq 5", tok, ok)
	}
}
