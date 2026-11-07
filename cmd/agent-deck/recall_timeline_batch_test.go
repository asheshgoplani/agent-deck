package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type timelineBatchJSON struct {
	Schema   string `json:"schema"`
	Sessions []struct {
		Session  string          `json:"session"`
		OK       bool            `json:"ok"`
		Timeline json.RawMessage `json:"timeline"`
		Error    string          `json:"error"`
		Code     string          `json:"code"`
	} `json:"sessions"`
}

func decodeAny(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("JSON: %v %s", err, raw)
	}
	return v
}

// TestRecallTimelineBatch_MissingIDInBatchOfThree: a batch of three with a
// missing id in the middle answers all three in input order, exit 0; the
// missing one fails alone with NOT_FOUND and the others carry the exact
// object the single-id call prints.
func TestRecallTimelineBatch_MissingIDInBatchOfThree(t *testing.T) {
	home, id, _ := rowsTestSession(t)
	const native = "11111111-2222-3333-4444-555555555555"
	refs := []string{id, "no-such-session", native}
	stdout, stderr, code := runAgentDeck(t, home, "recall", "timeline", "--json", "--tail", "3", "--sessions", strings.Join(refs, ","))
	if code != 0 {
		t.Fatalf("batch: exit %d %s %s", code, stdout, stderr)
	}
	var b timelineBatchJSON
	if err := json.Unmarshal([]byte(stdout), &b); err != nil {
		t.Fatalf("batch JSON: %v %s", err, stdout)
	}
	if b.Schema != "agent-deck.recall.timeline-batch/v1" || len(b.Sessions) != 3 {
		t.Fatalf("batch shape: %s", stdout)
	}
	for i, ref := range refs {
		if b.Sessions[i].Session != ref {
			t.Fatalf("entry %d is %q, want %q (input order)", i, b.Sessions[i].Session, ref)
		}
	}
	miss := b.Sessions[1]
	if miss.OK || miss.Code != ErrCodeNotFound || miss.Error == "" || len(miss.Timeline) != 0 {
		t.Fatalf("missing id entry: %+v", miss)
	}
	for _, i := range []int{0, 2} {
		e := b.Sessions[i]
		if !e.OK || e.Error != "" || e.Code != "" {
			t.Fatalf("entry %d not ok: %+v", i, e)
		}
		single, serr, scode := runAgentDeck(t, home, "recall", "timeline", refs[i], "--json", "--tail", "3")
		if scode != 0 {
			t.Fatalf("single %s: %d %s %s", refs[i], scode, single, serr)
		}
		if got, want := decodeAny(t, e.Timeline), decodeAny(t, []byte(single)); !reflect.DeepEqual(got, want) {
			t.Fatalf("entry %d differs from the single-id call:\nbatch:  %s\nsingle: %s", i, e.Timeline, single)
		}
	}
}

// TestRecallTimelineBatch_FlagRejections: only --tail goes with --sessions.
func TestRecallTimelineBatch_FlagRejections(t *testing.T) {
	home, id, _ := rowsTestSession(t)
	for _, extra := range [][]string{
		{"--since", "c1"},
		{"--before", "c1", "--limit", "5"},
		{"--agent", "a1"},
		{"--v1"},
		{"--limit", "5"},
		{"--transcript", "/dev/null", "--harness", "claude"},
		{id}, // a positional <session> next to --sessions
	} {
		args := append([]string{"recall", "timeline", "--json", "--sessions", id}, extra...)
		stdout, stderr, code := runAgentDeck(t, home, args...)
		if code != 2 || !strings.Contains(stdout, `"code": "`+ErrCodeInvalidOperation+`"`) {
			t.Fatalf("%v: want exit 2 with %s, got %d %s %s", extra, ErrCodeInvalidOperation, code, stdout, stderr)
		}
	}
	for _, list := range []string{"", ",", id + ",," + id} {
		stdout, stderr, code := runAgentDeck(t, home, "recall", "timeline", "--json", "--sessions", list)
		if code != 2 || !strings.Contains(stdout, ErrCodeInvalidOperation) {
			t.Fatalf("--sessions %q: want exit 2, got %d %s %s", list, code, stdout, stderr)
		}
	}
	// --json stays required, as on the single path.
	if _, _, code := runAgentDeck(t, home, "recall", "timeline", "--sessions", id); code != 2 {
		t.Fatalf("--sessions without --json: exit %d", code)
	}
}

// TestRecallTimelineBatch_FiftyIDLimit: 50 ids are answered, 51 refused.
func TestRecallTimelineBatch_FiftyIDLimit(t *testing.T) {
	home, _, _ := rowsTestSession(t)
	ids := func(n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("missing-%02d", i)
		}
		return strings.Join(out, ",")
	}
	stdout, stderr, code := runAgentDeck(t, home, "recall", "timeline", "--json", "--tail", "40", "--sessions", ids(50))
	var b timelineBatchJSON
	if code != 0 || json.Unmarshal([]byte(stdout), &b) != nil || len(b.Sessions) != 50 {
		t.Fatalf("50 ids: exit %d %s %s", code, stdout, stderr)
	}
	for i, e := range b.Sessions {
		if e.Session != fmt.Sprintf("missing-%02d", i) || e.OK || e.Code != ErrCodeNotFound {
			t.Fatalf("entry %d: %+v", i, e)
		}
	}
	stdout, stderr, code = runAgentDeck(t, home, "recall", "timeline", "--json", "--tail", "40", "--sessions", ids(51))
	if code != 2 || !strings.Contains(stdout, ErrCodeInvalidOperation) || !strings.Contains(stdout, "50") {
		t.Fatalf("51 ids: want exit 2 naming the limit, got %d %s %s", code, stdout, stderr)
	}
}

// TestRecallTimelineBatch_RecallGate: the batch is served only with
// [recall] enabled, like the single path.
func TestRecallTimelineBatch_RecallGate(t *testing.T) {
	home, id, _ := rowsTestSession(t)
	writeMacappConfig(t, home, "[recall]\nenabled = false\n")
	if stdout, _, code := runAgentDeck(t, home, "recall", "timeline", "--json", "--sessions", id); code != 2 || !strings.Contains(stdout, "enabled = true") {
		t.Fatalf("batch bypassed the recall gate: exit %d %s", code, stdout)
	}
}

// TestRecallTimelineBatch_RemoteForwarding: `remote <box> recall timeline
// --sessions` passes the closed option set; the batch restrictions still
// hold on the remote itself.
func TestRecallTimelineBatch_RemoteForwarding(t *testing.T) {
	for _, args := range [][]string{
		{"timeline", "--json", "--tail", "40", "--sessions", "a,b,c"},
		{"timeline", "--json", "--tail=40", "--sessions=a,b,c"},
	} {
		if err := validateRemoteRecallArgs(args); err != nil {
			t.Fatalf("%v refused: %v", args, err)
		}
	}
	if err := validateRemoteRecallArgs([]string{"timeline", "--json", "--sessions"}); err == nil {
		t.Fatal("--sessions without a value forwarded")
	}
	if err := validateRemoteRecallArgs([]string{"follow", "--jsonl", "--after", "end", "--sessions", "a,b"}); err == nil {
		t.Fatal("--sessions forwarded on follow")
	}
}

// TestVersionJSONAdvertisesTimelineBatch: the capability the Mac app reads
// before it sends --sessions; plain `version` keeps its one line.
func TestVersionJSONAdvertisesTimelineBatch(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runAgentDeck(t, home, "version", "--json")
	if code != 0 {
		t.Fatalf("version --json: %d %s %s", code, stdout, stderr)
	}
	var v struct {
		Version  string   `json:"version"`
		Features []string `json:"features"`
	}
	if err := json.Unmarshal([]byte(stdout), &v); err != nil || v.Version == "" {
		t.Fatalf("version JSON: %v %s", err, stdout)
	}
	found := false
	for _, f := range v.Features {
		found = found || f == "recall_timeline_batch"
	}
	if !found {
		t.Fatalf("features lack recall_timeline_batch: %s", stdout)
	}
	stdout, _, code = runAgentDeck(t, home, "version")
	if code != 0 || !strings.HasPrefix(stdout, "Agent Deck v") {
		t.Fatalf("plain version changed: %d %q", code, stdout)
	}
}

// TestRecallTimelineBatch_DuplicateAndPaddedIDs: a repeated id answers
// twice and spaces around ids are trimmed; the entry keeps the trimmed id.
func TestRecallTimelineBatch_DuplicateAndPaddedIDs(t *testing.T) {
	home, id, _ := rowsTestSession(t)
	stdout, stderr, code := runAgentDeck(t, home, "recall", "timeline", "--json", "--tail", "2", "--sessions", id+", "+id+" ,rows-cli")
	var b timelineBatchJSON
	if code != 0 || json.Unmarshal([]byte(stdout), &b) != nil || len(b.Sessions) != 3 {
		t.Fatalf("batch: exit %d %s %s", code, stdout, stderr)
	}
	for i, want := range []string{id, id, "rows-cli"} {
		e := b.Sessions[i]
		if e.Session != want || !e.OK {
			t.Fatalf("entry %d: %+v", i, e)
		}
	}
	if !reflect.DeepEqual(decodeAny(t, b.Sessions[0].Timeline), decodeAny(t, b.Sessions[1].Timeline)) {
		t.Fatalf("duplicate ids answered differently: %s", stdout)
	}
	if _, _, code := runAgentDeck(t, home, "recall", "timeline", "--json", "--tail", "-1", "--sessions", id); code != 2 {
		t.Fatalf("negative --tail accepted: exit %d", code)
	}
}

// TestRecallTimelineBatch_IndexUnavailable: when an id falls through to a
// recall index that cannot be opened, that id fails alone and the deck
// session in the same batch is still answered with exit 0.
func TestRecallTimelineBatch_IndexUnavailable(t *testing.T) {
	home, id, _ := rowsTestSession(t)
	// A directory where recall.db should be: the index cannot open.
	if err := os.MkdirAll(filepath.Join(home, ".local", "share", "agent-deck", "recall.db", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runAgentDeck(t, home, "recall", "timeline", "--json", "--tail", "2", "--sessions", id+",#9")
	var b timelineBatchJSON
	if code != 0 || json.Unmarshal([]byte(stdout), &b) != nil || len(b.Sessions) != 2 {
		t.Fatalf("batch: exit %d %s %s", code, stdout, stderr)
	}
	if !b.Sessions[0].OK || b.Sessions[1].OK || b.Sessions[1].Code != ErrCodeInvalidOperation || !strings.Contains(b.Sessions[1].Error, "recall index unavailable") {
		t.Fatalf("entries: %s", stdout)
	}
	// The single path keeps its behaviour: exit 1 with one error object.
	if _, _, code := runAgentDeck(t, home, "recall", "timeline", "#9", "--json"); code != 1 {
		t.Fatalf("single path on a broken index: exit %d", code)
	}
}

// TestRecallTimelineBatch_OldRemoteIsNamed: an older remote that rejects
// --sessions as an unknown flag is reported as predating the batch, so
// the forwarder answers {error, remote, remote_version} instead of usage.
func TestRecallTimelineBatch_OldRemoteIsNamed(t *testing.T) {
	stderr := "flag provided but not defined: -sessions\nUsage: agent-deck recall timeline <session> --json ...\n"
	args := []string{"recall", "timeline", "--json", "--tail", "40", "--sessions", "a,b"}
	if reason, ok := remoteRecallUnsupported(args, 2, "", stderr); !ok || reason != "predates recall timeline --sessions" {
		t.Fatalf("old remote not classified: %q %v", reason, ok)
	}
	if msg := remoteRecallUnsupportedMessage("lab", "1.16.28", "predates recall timeline --sessions"); !strings.Contains(msg, "agent-deck remote update lab") {
		t.Fatalf("message lacks the update hint: %s", msg)
	}
	if _, ok := remoteRecallUnsupported([]string{"recall", "timeline", "x", "--json"}, 1, "", "Error: no session"); ok {
		t.Fatal("an ordinary timeline failure was classified as an old remote")
	}
}
