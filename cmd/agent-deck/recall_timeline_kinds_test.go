package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
)

// rowsFixtureDir holds the Claude fixture the timeline goldens were made
// from. The CLI runs with it as the working directory and a relative
// --transcript, so session.path and every cursor are machine independent.
var rowsFixtureDir = filepath.Join("..", "..", "internal", "recall", "query", "testdata", "rows")

// runTimelineIn runs the agent-deck binary in dir with an isolated home.
func runTimelineIn(t *testing.T, home, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(channelsCLIBinary(t), args...)
	cmd.Env = agentDeckTestEnv(home, nil)
	cmd.Dir = abs
	var out, diag bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diag
	err = cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("run binary: %v", err)
	}
	return out.String(), diag.String(), cmd.ProcessState.ExitCode()
}

func recallHomeEnabled(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	writeMacappConfig(t, home, "[recall]\nenabled = true\nmax_loadavg = 0\n")
	return home
}

type kindsTimelineJSON struct {
	Turns []struct {
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		Body     string `json:"body"`
		Finished *bool  `json:"finished"`
	} `json:"turns"`
	ThroughCursor string  `json:"through_cursor"`
	BeforeCursor  string  `json:"before_cursor"`
	NextCursor    *string `json:"next_cursor"`
}

func decodeKindsTimeline(t *testing.T, stdout string) kindsTimelineJSON {
	t.Helper()
	var tl kindsTimelineJSON
	if err := json.Unmarshal([]byte(stdout), &tl); err != nil {
		t.Fatalf("timeline JSON: %v\n%s", err, stdout)
	}
	return tl
}

// TestRecallTimelineDefaultOutputUnchanged (#3203): without --kinds or
// --skip-empty-body the timeline is byte-identical to what the core printed
// before the filter existed. The goldens were produced by the unchanged
// binary at the base commit.
func TestRecallTimelineDefaultOutputUnchanged(t *testing.T) {
	home := recallHomeEnabled(t)
	for golden, flags := range map[string][]string{
		"full.json":   nil,
		"tail3.json":  {"--tail", "3"},
		"limit4.json": {"--limit", "4"},
	} {
		want, err := os.ReadFile(filepath.Join("testdata", "recall-timeline-default", golden))
		if err != nil {
			t.Fatal(err)
		}
		args := append([]string{"recall", "timeline", "--json", "--transcript", "claude-session.jsonl", "--harness", "claude"}, flags...)
		stdout, stderr, code := runTimelineIn(t, home, rowsFixtureDir, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d %s", flags, code, stderr)
		}
		if stdout != string(want) {
			t.Fatalf("%v: output differs from %s\ngot:\n%s", flags, golden, stdout)
		}
	}
}

// TestRecallTimelineKindsCLI (#3203): --kinds and --skip-empty-body filter
// on the owner host, the answer carries next_cursor, unknown kinds are
// refused with the valid list, and --v1 (no row kinds) is refused.
func TestRecallTimelineKindsCLI(t *testing.T) {
	home := recallHomeEnabled(t)
	base := []string{"recall", "timeline", "--json", "--transcript", "claude-session.jsonl", "--harness", "claude"}
	stdout, stderr, code := runTimelineIn(t, home, rowsFixtureDir, base...)
	if code != 0 {
		t.Fatalf("unfiltered: %d %s", code, stderr)
	}
	full := decodeKindsTimeline(t, stdout)

	stdout, stderr, code = runTimelineIn(t, home, rowsFixtureDir, append(base, "--kinds", "user,assistant")...)
	if code != 0 {
		t.Fatalf("--kinds: exit %d %s %s", code, stdout, stderr)
	}
	got := decodeKindsTimeline(t, stdout)
	var want []string
	for _, r := range full.Turns {
		if r.Kind == "user" || r.Kind == "assistant" {
			want = append(want, r.ID)
		}
	}
	var gotIDs []string
	for _, r := range got.Turns {
		gotIDs = append(gotIDs, r.ID)
	}
	if len(want) == 0 || !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("--kinds user,assistant: %v want %v", gotIDs, want)
	}
	if got.NextCursor == nil || *got.NextCursor != got.ThroughCursor || cursorPosition(t, *got.NextCursor) != cursorPosition(t, full.ThroughCursor) {
		t.Fatalf("next_cursor %v, want the unfiltered through_cursor's position %q", got.NextCursor, full.ThroughCursor)
	}
	if len(stdout) >= len(mustRead(t, filepath.Join("testdata", "recall-timeline-default", "full.json"))) {
		t.Fatalf("filtered payload is not smaller: %d bytes", len(stdout))
	}

	stdout, stderr, code = runTimelineIn(t, home, rowsFixtureDir, append(base, "--skip-empty-body")...)
	if code != 0 {
		t.Fatalf("--skip-empty-body: exit %d %s", code, stderr)
	}
	skipped := decodeKindsTimeline(t, stdout)
	if len(skipped.Turns) == 0 || len(skipped.Turns) >= len(full.Turns) {
		t.Fatalf("--skip-empty-body kept %d of %d rows", len(skipped.Turns), len(full.Turns))
	}
	for _, r := range skipped.Turns {
		if strings.TrimSpace(r.Body) == "" && (r.Finished == nil || *r.Finished) {
			t.Fatalf("row %s with an empty body kept", r.ID)
		}
	}

	// --tail N counts rows that pass the filter.
	stdout, stderr, code = runTimelineIn(t, home, rowsFixtureDir, append(base, "--tail", "2", "--kinds", "user")...)
	if code != 0 {
		t.Fatalf("--tail --kinds: exit %d %s", code, stderr)
	}
	tail := decodeKindsTimeline(t, stdout)
	var users []string
	for _, r := range full.Turns {
		if r.Kind == "user" {
			users = append(users, r.ID)
		}
	}
	var tailIDs []string
	for _, r := range tail.Turns {
		tailIDs = append(tailIDs, r.ID)
	}
	if !reflect.DeepEqual(tailIDs, users[len(users)-2:]) || tail.NextCursor == nil || tail.BeforeCursor == "" {
		t.Fatalf("--tail 2 --kinds user: %v want %v (next %v)", tailIDs, users[len(users)-2:], tail.NextCursor)
	}

	for _, flags := range [][]string{{"--kinds", "user,bogus"}, {"--kinds", ""}, {"--kinds", "user", "--v1"}} {
		stdout, stderr, code = runTimelineIn(t, home, rowsFixtureDir, append(base, flags...)...)
		if code != 2 {
			t.Fatalf("%v accepted: exit %d %s %s", flags, code, stdout, stderr)
		}
		if flags[1] == "user,bogus" && !strings.Contains(stdout+stderr, "valid kinds: user, assistant, thinking, tool, bash") {
			t.Fatalf("unknown kind error does not list the valid kinds: %s %s", stdout, stderr)
		}
	}

	stdout, stderr, _ = runTimelineIn(t, home, rowsFixtureDir, "recall", "timeline", "--help")
	for _, flag := range []string{"--kinds", "--skip-empty-body", "next_cursor"} {
		if !strings.Contains(stdout+stderr, flag) {
			t.Fatalf("help does not document %s: %s %s", flag, stdout, stderr)
		}
	}
}

// cursorPosition is a rows cursor without the dropped-row list a filtered
// read adds: the file, offset, anchor and parser state it resumes from.
func cursorPosition(t *testing.T, cursor string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("cursor %q: %v", cursor, err)
	}
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("cursor %q: %v", cursor, err)
	}
	delete(c, "d")
	b, _ := json.Marshal(c)
	return string(b)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRecallTimelineKindsCursorCLI (#3203): a client that resumes from
// next_cursor neither re-pulls the filtered window nor misses a new row.
func TestRecallTimelineKindsCursorCLI(t *testing.T) {
	home := recallHomeEnabled(t)
	dir := t.TempDir()
	src := mustRead(t, filepath.Join(rowsFixtureDir, "claude-session.jsonl"))
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), src, 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"recall", "timeline", "--json", "--transcript", "s.jsonl", "--harness", "claude", "--kinds", "user,assistant"}
	stdout, stderr, code := runTimelineIn(t, home, dir, base...)
	if code != 0 {
		t.Fatalf("first: %d %s", code, stderr)
	}
	first := decodeKindsTimeline(t, stdout)
	if first.NextCursor == nil || *first.NextCursor == "" {
		t.Fatalf("no next_cursor: %s", stdout)
	}
	stdout, stderr, code = runTimelineIn(t, home, dir, append(base, "--since", *first.NextCursor)...)
	if code != 0 {
		t.Fatalf("resume: %d %s", code, stderr)
	}
	if again := decodeKindsTimeline(t, stdout); len(again.Turns) != 0 {
		t.Fatalf("resume re-pulled %d rows", len(again.Turns))
	}
	f, err := os.OpenFile(filepath.Join(dir, "s.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"system","subtype":"turn_duration","durationMs":5,"uuid":"sx","timestamp":"2026-11-18T10:00:00Z"}` + "\n" +
		`{"type":"user","uuid":"unew","message":{"role":"user","content":"after the filter"},"timestamp":"2026-11-18T10:00:01Z"}` + "\n")
	f.Close()
	stdout, stderr, code = runTimelineIn(t, home, dir, append(base, "--since", *first.NextCursor)...)
	if code != 0 {
		t.Fatalf("after append: %d %s", code, stderr)
	}
	next := decodeKindsTimeline(t, stdout)
	if len(next.Turns) != 1 || next.Turns[0].ID != "unew" {
		t.Fatalf("after append: %+v", next.Turns)
	}
}

// TestRecallTimelineBatchKinds: --kinds and --skip-empty-body combine with
// --sessions; each entry is filtered as the single-id call would be.
func TestRecallTimelineBatchKinds(t *testing.T) {
	home, id, _ := rowsTestSession(t)
	stdout, stderr, code := runAgentDeck(t, home, "recall", "timeline", "--json", "--tail", "5", "--kinds", "user", "--skip-empty-body", "--sessions", id)
	if code != 0 {
		t.Fatalf("batch --kinds: %d %s %s", code, stdout, stderr)
	}
	var batch timelineBatchJSON
	if err := json.Unmarshal([]byte(stdout), &batch); err != nil || len(batch.Sessions) != 1 || !batch.Sessions[0].OK {
		t.Fatalf("batch JSON: %v %s", err, stdout)
	}
	tl := decodeKindsTimeline(t, string(batch.Sessions[0].Timeline))
	if len(tl.Turns) == 0 || tl.NextCursor == nil {
		t.Fatalf("batch entry: %s", batch.Sessions[0].Timeline)
	}
	for _, r := range tl.Turns {
		if r.Kind != "user" {
			t.Fatalf("batch entry kept a %s row", r.Kind)
		}
	}
	single, _, _ := runAgentDeck(t, home, "recall", "timeline", id, "--json", "--tail", "5", "--kinds", "user", "--skip-empty-body")
	var a, b map[string]any
	_ = json.Unmarshal([]byte(single), &a)
	_ = json.Unmarshal(batch.Sessions[0].Timeline, &b)
	delete(a, "status")
	delete(b, "status")
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("batch entry differs from the single-id answer:\n%s\n%s", single, batch.Sessions[0].Timeline)
	}
}

// TestRemoteRecallTimelineKinds (#3203): `remote <box> recall timeline`
// forwards --kinds and --skip-empty-body to the owner host in one ssh round
// trip, so the filter runs there; an owner core that predates the flags is
// named as such instead of failing with usage text.
func TestRemoteRecallTimelineKinds(t *testing.T) {
	for _, args := range [][]string{
		{"timeline", "id", "--json", "--kinds", "user,assistant", "--since", "c"},
		{"timeline", "id", "--json", "--kinds=user", "--skip-empty-body", "--tail", "40"},
		{"timeline", "--json", "--tail", "40", "--kinds", "user", "--sessions", "a,b"},
	} {
		if err := validateRemoteRecallArgs(args); err != nil {
			t.Fatalf("%v refused: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"timeline", "id", "--json", "--kinds"},
		{"timeline", "id", "--json", "--skip-empty-body=true"},
	} {
		if err := validateRemoteRecallArgs(args); err == nil {
			t.Fatalf("%v forwarded", args)
		}
	}

	home := t.TempDir()
	callLog, _ := fakeOwnerHost(t, home, false)
	stdout, stderr, code := runAgentDeck(t, home, "remote", "lab", "recall", "timeline", "sess", "--json", "--kinds", "user,assistant", "--skip-empty-body", "--tail", "400")
	if code != 0 || !strings.Contains(stdout, `"schema":"agent-deck.recall.rows/v2"`) {
		t.Fatalf("remote --kinds: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	calls := sshCalls(t, callLog)
	unquote := strings.NewReplacer(`'\''`, "", `'`, "")
	if len(calls) != 1 || !strings.Contains(unquote.Replace(calls[0]), "exec "+filepath.Join(filepath.Dir(callLog), "remote-agent-deck")+" recall timeline sess --json --kinds user,assistant --skip-empty-body --tail 400") {
		t.Fatalf("filter not forwarded to the owner host in one call: %q", calls)
	}

	old := "flag provided but not defined: -kinds\nUsage: agent-deck recall timeline <session> --json ...\n"
	if reason, ok := remoteRecallUnsupported([]string{"recall", "timeline", "x", "--json", "--kinds", "user"}, 2, "", old); !ok || reason != "predates recall timeline --kinds" {
		t.Fatalf("old owner core not classified: %q %v", reason, ok)
	}
	old = "flag provided but not defined: -skip-empty-body\n"
	if reason, ok := remoteRecallUnsupported([]string{"recall", "timeline", "x", "--json", "--skip-empty-body"}, 2, "", old); !ok || reason != "predates recall timeline --skip-empty-body" {
		t.Fatalf("old owner core not classified: %q %v", reason, ok)
	}
}

// TestVersionJSONAdvertisesTimelineKinds: clients pick the filtered call
// shape from `version --json` features instead of trial and error.
func TestVersionJSONAdvertisesTimelineKinds(t *testing.T) {
	stdout, stderr, code := runAgentDeck(t, t.TempDir(), "version", "--json")
	if code != 0 {
		t.Fatalf("version --json: %d %s %s", code, stdout, stderr)
	}
	var v struct {
		Features []string `json:"features"`
	}
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("version JSON: %v %s", err, stdout)
	}
	for _, f := range v.Features {
		if f == "recall_timeline_kinds" {
			return
		}
	}
	t.Fatalf("features lack recall_timeline_kinds: %s", stdout)
}

// TestBoundIndexRows: index-backed rows (no cursor) are filtered before
// --tail and --limit, which count kept rows; without a filter --limit is
// ignored there as before, so that answer stays byte-identical.
func TestBoundIndexRows(t *testing.T) {
	rows := []query.Row{{ID: "u1", Kind: "user"}, {ID: "b1", Kind: "bash"}, {ID: "u2", Kind: "user"}, {ID: "b2", Kind: "bash"}, {ID: "u3", Kind: "user"}}
	ids := func(rs []query.Row) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}
	user := []string{"user"}
	for _, tc := range []struct {
		opts query.RowsOptions
		want []string
	}{
		{query.RowsOptions{Kinds: user, Limit: 2}, []string{"u1", "u2"}},
		{query.RowsOptions{Kinds: user, Tail: 2}, []string{"u2", "u3"}},
		{query.RowsOptions{Kinds: user}, []string{"u1", "u2", "u3"}},
		{query.RowsOptions{Limit: 2}, []string{"u1", "b1", "u2", "b2", "u3"}},
		{query.RowsOptions{Tail: 2}, []string{"b2", "u3"}},
		{query.RowsOptions{Kinds: user, Tail: 3, Limit: 2}, []string{"u1", "u2", "u3"}},
		{query.RowsOptions{Kinds: user, Tail: 5, Limit: 1}, []string{"u1", "u2", "u3"}},
	} {
		if got := ids(boundIndexRows(append([]query.Row(nil), rows...), tc.opts)); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%+v: %v want %v", tc.opts, got, tc.want)
		}
	}
}
