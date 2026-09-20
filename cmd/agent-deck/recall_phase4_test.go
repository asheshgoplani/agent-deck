package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/cards"
	"github.com/asheshgoplani/agent-deck/internal/recall/enrich"
)

// ---- the remote allowlist --------------------------------------------------

func TestRemoteCommandArgs_RecallVerbsAreClosed(t *testing.T) {
	ok := [][]string{
		{"recall", "search", "clock skew", "--json", "--harness", "codex", "--hint", "ticket=SB-1", "--phrase", "--limit", "5"},
		{"recall", "sessions", "--since", "30d", "--json"},
		{"recall", "show", "#3", "--tier", "card", "--json"},
		{"recall", "context", "abc", "--tier", "brief", "--budget", "300"},
		{"recall", "export", "--cards", "--since", "1758000000"},
		{"recall", "status", "--json"},
		{"recall", "search", "--help"},
		{"recall", "--help"},
	}
	for _, args := range ok {
		if _, err := remoteCommandArgs(args); err != nil {
			t.Errorf("%v must forward: %v", args, err)
		}
	}
	refused := [][]string{
		{"recall", "context", "abc", "--into", "current"},   // a delivery never travels
		{"recall", "search", "q", "--remote", "lab"},        // no second hop
		{"recall", "search", "q", "--all-remotes"},          // no second hop
		{"recall", "backfill"},                              // never a write
		{"recall", "import", "--host", "x"},                 // never a write
		{"recall", "pull", "lab"},                           // never a write
		{"recall", "mcp"},                                   // a server, not a command
		{"recall", "search", "q", "--message-file", "/etc"}, // no controller path
		{"recall", "search", "q", "--phrase=yes"},           // a boolean with a value
		{"recall", "search", "q", "--limit"},                // a value-taking option without one
		{"recall", "sessions", "--role", "user"},            // not an option of that verb
	}
	for _, args := range refused {
		if _, err := remoteCommandArgs(args); err == nil {
			t.Errorf("%v must be refused before SSH", args)
		}
	}
}

// ---- older remotes ---------------------------------------------------------

func TestRemoteRecallUnsupported_Detection(t *testing.T) {
	old := `Error: "recall" is not a recognized command and stdout is not a terminal, so the interactive UI cannot open; run 'agent-deck help' for the command list`
	if reason, ok := remoteRecallUnsupported([]string{"recall", "search", "q", "--json"}, 2, "", old); !ok || reason != "predates recall" {
		t.Fatalf("pre-recall remote: %q %v", reason, ok)
	}
	off := `{"success": false, "error": "recall is off: set [recall] enabled = true in config.toml (docs/recall.md); hints and 'session annotate' work without it", "code": "INVALID_OPERATION"}`
	if reason, ok := remoteRecallUnsupported([]string{"recall", "search", "q", "--json"}, 2, off, ""); !ok || reason != "has [recall] enabled = false" {
		t.Fatalf("recall-off remote: %q %v", reason, ok)
	}
	if _, ok := remoteRecallUnsupported([]string{"recall", "search", "q"}, 1, "", "Error: recall: no such session"); ok {
		t.Fatal("other errors pass through")
	}
	if _, ok := remoteRecallUnsupported([]string{"recall", "search", "q"}, 0, "", old); ok {
		t.Fatal("exit 0 never degrades")
	}
	if _, ok := remoteRecallUnsupported([]string{"session", "show", "x"}, 2, "", old); ok {
		t.Fatal("only recall verbs")
	}
	msg := remoteRecallUnsupportedMessage("lab", "1.16.12", "predates recall")
	if msg != `remote "lab" runs v1.16.12 that predates recall; update it with 'agent-deck remote update lab'` {
		t.Fatalf("message = %q", msg)
	}
	msg = remoteRecallUnsupportedMessage("lab", "1.16.13", "has [recall] enabled = false")
	if !strings.Contains(msg, "runs v1.16.13 that has [recall] enabled = false; set [recall] enabled = true in its config.toml") {
		t.Fatalf("message = %q", msg)
	}
	var obj map[string]string
	if err := json.Unmarshal(remoteRecallUnsupportedJSON("lab", "", "predates recall"), &obj); err != nil || obj["remote_version"] != remoteVersionUnknown || obj["remote"] != "lab" || !strings.Contains(obj["error"], "unknown agent-deck version") {
		t.Fatalf("json = %v %v", obj, err)
	}
	// A v1.16.13 remote with the phase-1-3 verbs but not a phase-4 one
	// (pull's export, remote context, remote export) answers its own
	// usage text on stdout, then the "unknown recall command" line on
	// stderr, exit 1: the verb name is folded into the reason so the
	// message names the fix without the remote's usage ever appearing.
	usage := "Usage: agent-deck recall <command> [options]\n\nCommands:\n  search \"<q>\"     Full-text search\n"
	unknown := "Error: unknown recall command: context\n\n"
	if reason, ok := remoteRecallUnsupported([]string{"recall", "context", "abc"}, 1, usage, unknown); !ok || reason != "predates recall context" {
		t.Fatalf("v1.16.13 without context: %q %v", reason, ok)
	}
	if reason, ok := remoteRecallUnsupported([]string{"recall", "export", "--cards"}, 1, usage, "Error: unknown recall command: export\n\n"); !ok || reason != "predates recall export" {
		t.Fatalf("v1.16.13 without export: %q %v", reason, ok)
	}
	msg = remoteRecallUnsupportedMessage("lab", "1.16.13", "predates recall context")
	if msg != `remote "lab" runs v1.16.13 that predates recall context; update it with 'agent-deck remote update lab'` {
		t.Fatalf("message = %q", msg)
	}
}

// fakeRemoteSSH puts an `ssh` shim first on PATH. mode "old" answers like
// v1.16.12 (recall is not a command); "off" like v1.16.13 with [recall]
// enabled = false; "new" answers `recall search --json` with one hit and
// `recall export` with a two-line stream.
func fakeRemoteSSH(t *testing.T, mode string) string {
	t.Helper()
	shim := t.TempDir()
	log := filepath.Join(shim, "calls.log")
	var body string
	switch mode {
	case "old":
		body = `  *"'recall'"*)
    printf 'Error: "recall" is not a recognized command and stdout is not a terminal, so the interactive UI cannot open; run '"'"'agent-deck help'"'"' for the command list\n' >&2
    exit 2 ;;`
	case "off":
		body = `  *"'recall'"*)
    printf '{"success": false, "error": "recall is off: set [recall] enabled = true in config.toml (docs/recall.md); hints and '"'"'session annotate'"'"' work without it", "code": "INVALID_OPERATION"}\n'
    exit 2 ;;`
	case "new":
		body = `  *"'recall' 'export'"*)
    printf '{"kind":"header","version":1,"host_uid":"ffffffffffffffffffffffffffffffff","exported_at":1758000000}\n'
    printf '{"kind":"session","harness":"codex","native_id":"remote-conv-1","title":"remote retry budget","turns":2,"tool_calls":1,"errors":0,"interrupts":0,"compacts":0,"derived_rev":1}\n'
    printf '{"kind":"card","native_id":"remote-conv-1","harness":"codex","title":"remote retry budget","preview":"raise the retry budget"}\n'
    printf '{"kind":"trailer","sessions":1,"cards":1,"artifacts":0,"edges":0}\n'
    exit 0 ;;
  *"'recall' 'search'"*)
    printf '{"success": true, "result": {"query": "retry budget", "match": "\"retry\" \"budget\"", "hits": [{"sess_id": 4, "harness": "codex", "native_id": "remote-conv-1", "title": "remote retry budget", "body_hits": 2, "card_hit": true, "snippet": "raise the retry budget"}], "candidates": 2, "elapsed_ms": 3}, "index": {"swept": true, "elapsed_ms": 1}}\n'
    exit 0 ;;`
	case "v13nophase4":
		// A real v1.16.13 remote (phases 1-3, no phase-4 verbs): search
		// still answers; context and export (which pull runs on the
		// remote) fall to the generic "unknown recall command" branch
		// and print the remote's own usage text on stdout first.
		body = `  *"'recall' 'context'"*)
    printf 'Usage: agent-deck recall <command> [options]\n\nCommands:\n  search "<q>"     Full-text search\n  show <session>   One session\n\nExamples:\n  agent-deck recall search "clock skew"\n'
    printf 'Error: unknown recall command: context\n\n' >&2
    exit 1 ;;
  *"'recall' 'export'"*)
    printf 'Usage: agent-deck recall <command> [options]\n\nCommands:\n  search "<q>"     Full-text search\n  show <session>   One session\n\nExamples:\n  agent-deck recall search "clock skew"\n'
    printf 'Error: unknown recall command: export\n\n' >&2
    exit 1 ;;
  *"'recall' 'search'"*)
    printf '{"success": true, "result": {"query": "retry budget", "hits": [], "candidates": 0, "elapsed_ms": 1}, "index": {"swept": true, "elapsed_ms": 1}}\n'
    exit 0 ;;`
	}
	version := "1.16.12"
	if mode != "old" {
		version = "1.16.13"
	}
	script := "#!/bin/sh\nfor cmd; do :; done\nprintf '%s\\n' \"$cmd\" >> " + log + "\ncase \"$cmd\" in\n  *\" version\")\n    printf 'Agent Deck v" + version + "\\n'\n    exit 0 ;;\n" + body + "\nesac\nprintf 'unexpected remote command: %s\\n' \"$cmd\" >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(shim, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func remoteHome(t *testing.T) string {
	t.Helper()
	home, _ := recallHome(t, 1)
	cfg := filepath.Join(home, ".config", "agent-deck", "config.toml")
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, append(data, []byte("\n[remotes.lab]\nhost = 'old-host'\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestRemoteRecall_OlderRemoteDegrades drives the built binary against a
// fake older remote for the forwarded form and the federated form: one
// clear line naming the remote's version and the fix, exit 1, and the
// {error, remote, remote_version} object under --json; never the remote's
// own text.
func TestRemoteRecall_OlderRemoteDegrades(t *testing.T) {
	for _, mode := range []string{"old", "off"} {
		t.Run(mode, func(t *testing.T) {
			home := remoteHome(t)
			callLog := fakeRemoteSSH(t, mode)
			want := `remote "lab" runs v1.16.12 that predates recall; update it with 'agent-deck remote update lab'`
			if mode == "off" {
				want = `remote "lab" runs v1.16.13 that has [recall] enabled = false; set [recall] enabled = true in its config.toml`
			}
			stdout, stderr, code := runAgentDeck(t, home, "remote", "lab", "recall", "search", "retry budget")
			if code != 1 || stdout != "" || strings.TrimSpace(stderr) != "Error: "+want {
				t.Fatalf("forwarded: exit %d\nstdout: %q\nstderr: %q", code, stdout, stderr)
			}
			stdout, stderr, code = runAgentDeck(t, home, "remote", "lab", "recall", "search", "retry budget", "--json")
			if code != 1 || strings.Contains(stderr, "not a recognized") || strings.Contains(stdout, "recall is off") {
				t.Fatalf("forwarded --json: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			var got map[string]string
			if err := json.Unmarshal([]byte(stdout), &got); err != nil || got["error"] != want || got["remote"] != "lab" || got["remote_version"] == "" {
				t.Fatalf("forwarded --json = %s (%v)", stdout, err)
			}

			// The federated form: local hits still print, the remote's
			// failure is one line, the exit is 1.
			stdout, stderr, code = runAgentDeck(t, home, "recall", "search", "test", "--remote", "lab", "--json")
			if code != 1 {
				t.Fatalf("federated: exit %d\n%s\n%s", code, stdout, stderr)
			}
			var fed struct {
				Success bool                 `json:"success"`
				Result  recallSearchJSON     `json:"result"`
				Remotes []RemoteSearchResult `json:"remotes"`
			}
			mustJSON(t, stdout, &fed)
			if fed.Success || len(fed.Remotes) != 1 || fed.Remotes[0].Error != want || fed.Remotes[0].Remote != "lab" || fed.Remotes[0].RemoteVersion == "" {
				t.Fatalf("federated json: %+v", fed.Remotes)
			}
			human, _, code := runAgentDeck(t, home, "recall", "search", "test", "--remote", "lab")
			if code != 1 || !strings.Contains(human, "remote lab: "+want) || !strings.Contains(human, "session(s) for") {
				t.Fatalf("federated human: exit %d\n%s", code, human)
			}
			calls, _ := os.ReadFile(callLog)
			if !strings.Contains(string(calls), "'recall' 'search'") || !strings.Contains(string(calls), " version") {
				t.Fatalf("calls:\n%s", calls)
			}
		})
	}
}

// TestRemoteRecall_OlderRemoteDegrades_Phase4Verbs is finding 1 of the
// round-2 review: a real v1.16.13 remote (phase 1-3 verbs, no phase-4 ones)
// answers a forwarded `context`, a forwarded `export`, or the `export`
// `pull` runs on it with its own usage text and "unknown recall command:
// <verb>", exit 1. All three must classify the same as the pre-recall and
// recall-off shapes: one line, exit 1, {error, remote, remote_version}
// under --json, the remote's usage text never on stdout.
func TestRemoteRecall_OlderRemoteDegrades_Phase4Verbs(t *testing.T) {
	home := remoteHome(t)
	fakeRemoteSSH(t, "v13nophase4")
	cfg := filepath.Join(home, ".config", "agent-deck", "config.toml")
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(strings.Replace(string(data), "[recall]\n", "[recall]\nremote_cards = true\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}

	wantContext := `remote "lab" runs v1.16.13 that predates recall context; update it with 'agent-deck remote update lab'`
	stdout, stderr, code := runAgentDeck(t, home, "remote", "lab", "recall", "context", "abc", "--tier", "card")
	if code != 1 || stdout != "" || strings.TrimSpace(stderr) != "Error: "+wantContext {
		t.Fatalf("remote context: exit %d\nstdout: %q\nstderr: %q", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeck(t, home, "remote", "lab", "recall", "context", "abc", "--tier", "card", "--json")
	if code != 1 || strings.Contains(stdout, "Usage: agent-deck recall") || strings.Contains(stderr, "Usage: agent-deck recall") {
		t.Fatalf("remote context --json leaked usage text: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || got["error"] != wantContext || got["remote"] != "lab" || got["remote_version"] != "1.16.13" {
		t.Fatalf("remote context --json = %s (%v)", stdout, err)
	}

	wantExport := `remote "lab" runs v1.16.13 that predates recall export; update it with 'agent-deck remote update lab'`
	stdout, stderr, code = runAgentDeck(t, home, "remote", "lab", "recall", "export", "--cards", "--json")
	if code != 1 || strings.Contains(stdout, "Usage: agent-deck recall") {
		t.Fatalf("remote export --json: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || got["error"] != wantExport || got["remote"] != "lab" || got["remote_version"] != "1.16.13" {
		t.Fatalf("remote export --json = %s (%v)", stdout, err)
	}

	// `recall pull lab` runs `recall export` on the remote; the same
	// classifier must catch it there too (recall_remote_cmd.go's pull path).
	stdout, stderr, code = runAgentDeck(t, home, "recall", "pull", "lab")
	if code != 1 || stdout != "" || strings.TrimSpace(stderr) != "Error: "+wantExport {
		t.Fatalf("pull: exit %d\nstdout: %q\nstderr: %q", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeck(t, home, "recall", "pull", "lab", "--json")
	if code != 1 || strings.Contains(stdout, "Usage: agent-deck recall") {
		t.Fatalf("pull --json: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || got["error"] != wantExport || got["remote"] != "lab" || got["remote_version"] != "1.16.13" {
		t.Fatalf("pull --json = %s (%v)", stdout, err)
	}
}

// TestRecallSearch_FederatedMergesAndLabels: a remote that answers gets its
// hits printed under its name, labelled, with the forwarded filters, and
// the exit is 0.
func TestRecallSearch_FederatedMergesAndLabels(t *testing.T) {
	home := remoteHome(t)
	callLog := fakeRemoteSSH(t, "new")
	stdout, stderr, code := runAgentDeck(t, home, "recall", "search", "retry budget", "--all-remotes", "--harness", "codex", "--since", "30d", "--limit", "3", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	var fed struct {
		Success bool                 `json:"success"`
		Remotes []RemoteSearchResult `json:"remotes"`
	}
	mustJSON(t, stdout, &fed)
	if !fed.Success || len(fed.Remotes) != 1 || fed.Remotes[0].Error != "" || len(fed.Remotes[0].Hits) != 1 {
		t.Fatalf("%+v", fed)
	}
	h := fed.Remotes[0].Hits[0]
	if h.Remote != "lab" || h.NativeID != "remote-conv-1" || !h.CardHit {
		t.Fatalf("hit not labelled: %+v", h)
	}
	calls, _ := os.ReadFile(callLog)
	if !strings.Contains(string(calls), "'recall' 'search' '--harness' 'codex' '--since' '30d' 'retry budget' '--json' '--limit' '3'") {
		t.Fatalf("filters must be forwarded verbatim:\n%s", calls)
	}
	human, _, code := runAgentDeck(t, home, "recall", "search", "retry budget", "--remote", "lab", "--remote", "lab")
	if code != 0 || !strings.Contains(human, "remote lab: 1 session(s)") || !strings.Contains(human, "remote lab]") || strings.Count(human, "remote lab: 1 session(s)") != 1 {
		t.Fatalf("human (a repeated --remote runs once):\n%s", human)
	}
	if _, stderr, code := runAgentDeck(t, home, "recall", "search", "q", "--remote", "nope"); code != 2 || !strings.Contains(stderr, `remote "nope" not found`) {
		t.Fatalf("unknown remote: %d %s", code, stderr)
	}
}

// ---- card sync -------------------------------------------------------------

func TestRecallCardSync_OffByDefaultThenPullImportsDigestOnly(t *testing.T) {
	home := remoteHome(t)
	fakeRemoteSSH(t, "new")
	if _, _, code := runAgentDeck(t, home, "recall", "backfill", "--json"); code != 0 {
		t.Fatal("backfill")
	}
	for _, args := range [][]string{{"recall", "export", "--cards"}, {"recall", "pull", "lab"}, {"recall", "import", "--host", "lab", "-"}} {
		stdout, stderr, code := runAgentDeck(t, home, args...)
		if code != 2 || !strings.Contains(stderr, "remote_cards = true") || stdout != "" {
			t.Fatalf("%v with remote_cards off: exit %d\n%s\n%s", args, code, stdout, stderr)
		}
	}
	cfg := filepath.Join(home, ".config", "agent-deck", "config.toml")
	data, _ := os.ReadFile(cfg)
	if err := os.WriteFile(cfg, []byte(strings.Replace(string(data), "[recall]\n", "[recall]\nremote_cards = true\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	// export: NDJSON with a header carrying this machine's host_uid, one
	// session line per indexed session (the corpus has a subagent
	// transcript too, so the count comes from the trailer), and none of
	// the keys or text the cards package forbids: no path, no offset, no
	// span, no message. An artifact line's "body" is derived text
	// ("13 call(s) over 1m0s"), not a message, and is allowed.
	stdout, stderr, code := runAgentDeck(t, home, "recall", "export", "--cards")
	if code != 0 || !strings.HasPrefix(stdout, `{"kind":"header"`) {
		t.Fatalf("export: %d\n%s\n%s", code, stdout, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	var trailer struct {
		Kind     string `json:"kind"`
		Sessions int    `json:"sessions"`
		Cards    int    `json:"cards"`
	}
	mustJSON(t, lines[len(lines)-1], &trailer)
	if trailer.Kind != "trailer" || trailer.Sessions < 1 || trailer.Cards != trailer.Sessions || strings.Count(stdout, `{"kind":"session"`) != trailer.Sessions ||
		!strings.Contains(stderr, fmt.Sprintf("exported %d session(s), %d card(s)", trailer.Sessions, trailer.Cards)) {
		t.Fatalf("export trailer %+v does not match the stream:\n%s\n%s", trailer, stdout, stderr)
	}
	forbidden := []string{home, "/projects/", ".jsonl", `"role"`, `"tool_use"`}
	for _, k := range cards.ForbiddenKeys {
		forbidden = append(forbidden, `"`+k+`"`)
	}
	for _, k := range forbidden {
		if strings.Contains(stdout, k) {
			t.Fatalf("export carries %s:\n%s", k, stdout)
		}
	}
	// import without a host: refused; own cards: refused.
	if _, stderr, code := runAgentDeckStdin(t, home, stdout, "recall", "import", "-"); code != 2 || !strings.Contains(stderr, "explicit --host") {
		t.Fatalf("import without host: %d %s", code, stderr)
	}
	if _, stderr, code := runAgentDeckStdin(t, home, stdout, "recall", "import", "--host", "me", "-"); code != 2 || !strings.Contains(stderr, "exported by this machine") {
		t.Fatalf("own cards: %d %s", code, stderr)
	}
	// pull: the fake remote's two-line stream lands as one digest-only row.
	stdout, stderr, code = runAgentDeck(t, home, "recall", "pull", "lab", "--json")
	if code != 0 {
		t.Fatalf("pull: %d\n%s\n%s", code, stdout, stderr)
	}
	var pulled struct {
		Import struct {
			HostUID  string `json:"host_uid"`
			Sessions int    `json:"sessions"`
			Cards    int    `json:"cards"`
		} `json:"import"`
	}
	mustJSON(t, stdout, &pulled)
	if pulled.Import.Sessions != 1 || pulled.Import.Cards != 1 || pulled.Import.HostUID != strings.Repeat("f", 32) {
		t.Fatalf("pull: %+v", pulled.Import)
	}
	stdout, _, code = runAgentDeck(t, home, "recall", "search", "retry budget", "--no-sweep")
	if code != 0 || !strings.Contains(stdout, "card from ffffffffffffffffffffffffffffffff (no messages here)") {
		t.Fatalf("pulled card must be labelled in search:\n%s", stdout)
	}
	stdout, stderr, code = runAgentDeck(t, home, "recall", "context", "remote-conv-1", "--tier", "excerpt")
	if code != 2 || !strings.Contains(stderr, "card pulled from another machine") || !strings.Contains(stderr, "agent-deck remote lab recall show remote-conv-1") {
		t.Fatalf("excerpt over a pulled card: %d %s %s", code, stdout, stderr)
	}
	stdout, _, code = runAgentDeck(t, home, "recall", "context", "remote-conv-1", "--tier", "brief")
	if code != 0 || !strings.Contains(stdout, "from another machine (card only)") {
		t.Fatalf("brief over a pulled card: %d\n%s", code, stdout)
	}
	// The same stream under another alias: refused (one machine, one alias).
	stream := "{\"kind\":\"header\",\"version\":1,\"host_uid\":\"" + strings.Repeat("f", 32) + "\",\"exported_at\":1}\n"
	if _, stderr, code := runAgentDeckStdin(t, home, stream, "recall", "import", "--host", "lab2", "-"); code != 2 || !strings.Contains(stderr, "already imported as \"lab\"") {
		t.Fatalf("alias taken: %d %s", code, stderr)
	}
	// A different machine under the known alias: refused.
	stream = "{\"kind\":\"header\",\"version\":1,\"host_uid\":\"" + strings.Repeat("e", 32) + "\",\"exported_at\":1}\n"
	if _, stderr, code := runAgentDeckStdin(t, home, stream, "recall", "import", "--host", "lab", "-"); code != 2 || !strings.Contains(stderr, "host_uid mismatch") {
		t.Fatalf("uid mismatch: %d %s", code, stderr)
	}
}

// ---- enrich and the stale marker through the CLI ---------------------------

func TestRecallEnrich_SweepDrainsAndShowMarksStale(t *testing.T) {
	home, stats := recallHome(t, 2)
	stdout, _, code := runAgentDeck(t, home, "recall", "backfill", "--json")
	if code != 0 {
		t.Fatal(stdout)
	}
	var bf struct {
		Result struct {
			Enriched int `json:"enriched"`
		} `json:"result"`
	}
	mustJSON(t, stdout, &bf)
	if bf.Result.Enriched != stats.Files*len(enrich.CheapKinds) {
		t.Fatalf("the backfill must drain the cheap classifiers: %+v (files %d)", bf.Result, stats.Files)
	}
	sess := stats.Sessions[0]
	stdout, _, code = runAgentDeck(t, home, "recall", "show", sess, "--tier", "card", "--json")
	if code != 0 {
		t.Fatal(stdout)
	}
	var shown struct {
		Detail struct {
			Artifacts []struct {
				Kind  string `json:"kind"`
				Body  string `json:"body"`
				Stale bool   `json:"stale"`
			} `json:"artifacts"`
		} `json:"detail"`
	}
	mustJSON(t, stdout, &shown)
	if len(shown.Detail.Artifacts) != len(enrich.CheapKinds) {
		t.Fatalf("show card: %+v", shown.Detail.Artifacts)
	}
	for _, a := range shown.Detail.Artifacts {
		if a.Stale {
			t.Fatalf("fresh: %+v", a)
		}
	}
	human, _, _ := runAgentDeck(t, home, "recall", "show", sess, "--tier", "card")
	if !strings.Contains(human, "  Derived:") || !strings.Contains(human, "session_kind: ") || strings.Contains(human, "[stale") {
		t.Fatalf("human show:\n%s", human)
	}
	// Nothing pending: an explicit drain is a no-op that says so.
	stdout, _, code = runAgentDeck(t, home, "recall", "enrich")
	if code != 0 || !strings.Contains(stdout, "enrich done: 0 pending") {
		t.Fatalf("enrich: %d %s", code, stdout)
	}
	// llm is never automatic.
	if _, stderr, code := runAgentDeck(t, home, "recall", "enrich", "--cost-class", "llm"); code != 1 || !strings.Contains(stderr, "never drained automatically") {
		t.Fatalf("llm: %d %s", code, stderr)
	}
	// The transcript grows: the sweep re-derives; between the two the
	// artifacts read stale.
	var path string
	filepath.WalkDir(filepath.Join(home, ".claude", "projects"), func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, sess+".jsonl") {
			path = p
		}
		return nil
	})
	if path == "" {
		t.Fatal("transcript not found")
	}
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fh.WriteString(`{"type":"user","message":{"role":"user","content":"[Request interrupted by user]"},"uuid":"i-1","timestamp":"2026-09-19T12:00:00Z","sessionId":"` + sess + `"}` + "\n")
	fh.WriteString(`{"type":"user","message":{"role":"user","content":"[Request interrupted by user]"},"uuid":"i-2","timestamp":"2026-09-19T12:00:01Z","sessionId":"` + sess + `"}` + "\n")
	fh.WriteString(`{"type":"user","message":{"role":"user","content":"[Request interrupted by user]"},"uuid":"i-3","timestamp":"2026-09-19T12:00:02Z","sessionId":"` + sess + `"}` + "\n")
	fh.Close()
	// The sweep parses the tail, re-queues the session and drains it.
	if stdout, stderr, code := runAgentDeck(t, home, "recall", "sweep", "--json"); code != 0 {
		t.Fatalf("sweep: %s %s", stdout, stderr)
	}
	human, _, _ = runAgentDeck(t, home, "recall", "show", sess, "--tier", "card")
	if !strings.Contains(human, "outcome: abandoned? (3 interrupts)") {
		t.Fatalf("the sweep's drain must re-derive from the new rows:\n%s", human)
	}
	// Force staleness by hand: derived_rev moves and nothing queues the
	// session (a pass cancelled between the ingest's commit and the card
	// projection used to leave exactly this state, with the queue row
	// done). The marker shows in show and context, and the drain the
	// marker names must pick the session up on its own.
	dbPath := filepath.Join(home, ".local", "share", "agent-deck", "recall.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE session SET derived_rev=derived_rev+1 WHERE native_id=?`, sess); err != nil {
		t.Fatal(err)
	}
	db.Close()
	human, _, _ = runAgentDeck(t, home, "recall", "show", sess, "--tier", "card")
	if strings.Count(human, "[stale: session changed since; run 'agent-deck recall enrich']") != len(enrich.CheapKinds) {
		t.Fatalf("show must mark every stale artifact:\n%s", human)
	}
	for _, tier := range []string{"card", "brief", "excerpt"} {
		stdout, _, code = runAgentDeck(t, home, "recall", "context", sess, "--tier", tier, "--json")
		if code != 0 {
			t.Fatalf("context %s: %s", tier, stdout)
		}
		if !strings.Contains(stdout, `"stale": true`) {
			t.Fatalf("context %s must carry the stale flag:\n%s", tier, stdout)
		}
	}
	stdout, _, code = runAgentDeck(t, home, "recall", "enrich", "--json")
	if code != 0 || !strings.Contains(stdout, `"requeued": 3`) || !strings.Contains(stdout, `"written": 3`) {
		t.Fatalf("enrich after staleness: %d %s", code, stdout)
	}
	human, _, _ = runAgentDeck(t, home, "recall", "show", sess, "--tier", "card")
	if strings.Contains(human, "[stale") || !strings.Contains(human, "outcome: abandoned? (3 interrupts)") {
		t.Fatalf("the drain must refresh the stale artifacts:\n%s", human)
	}
}

// ---- recall context and --into ---------------------------------------------

func TestRecallContext_PrintsAndIntoCurrentNeedsASession(t *testing.T) {
	home, stats := recallHome(t, 1)
	if _, _, code := runAgentDeck(t, home, "recall", "backfill", "--json"); code != 0 {
		t.Fatal("backfill")
	}
	sess := stats.Sessions[0]
	card, _, code := runAgentDeck(t, home, "recall", "context", sess, "--tier", "card")
	if code != 0 || !strings.HasPrefix(card, "Recalled conversation #") || strings.Contains(card, "BEGIN RECALLED") {
		t.Fatalf("card: %d\n%s", code, card)
	}
	excerpt, _, code := runAgentDeck(t, home, "recall", "context", sess, "--budget", "600")
	if code != 0 || !strings.Contains(excerpt, "--- BEGIN RECALLED TRANSCRIPT") || !strings.Contains(excerpt, "[USER]") || !strings.Contains(excerpt, "Derived:") || len(excerpt) > 600*4+400 {
		t.Fatalf("excerpt: %d (%d chars)\n%s", code, len(excerpt), excerpt)
	}
	if _, stderr, code := runAgentDeck(t, home, "recall", "context", sess, "--into", "current"); code != 2 || !strings.Contains(stderr, "AGENTDECK_INSTANCE_ID") {
		t.Fatalf("--into current outside a session: %d %s", code, stderr)
	}
	// Under --json the refusal is the JSON error object on stdout.
	if stdout, stderr, code := runAgentDeck(t, home, "recall", "context", sess, "--into", "x", "--json"); code != 2 || !strings.Contains(stdout, "cannot be combined") {
		t.Fatalf("--into with --json: %d %s %s", code, stdout, stderr)
	}
	if _, stderr, code := runAgentDeck(t, home, "recall", "context", "no-such", "--tier", "brief"); code != 2 || !strings.Contains(stderr, "no such session") {
		t.Fatalf("unknown: %d %s", code, stderr)
	}
	// An --ssh target would carry the local conversation over SSH as
	// keystrokes: refused while [recall] remote_cards is off, before any
	// send is attempted (the session is not even running).
	if stdout, stderr, code := runAgentDeck(t, home, "add", "--no-parent", "--ssh", "alice@host-a", "--remote-path", "/srv/proj", "-t", "remote-target", "-c", "claude", "--json"); code != 0 {
		t.Fatalf("add --ssh: %d %s %s", code, stdout, stderr)
	}
	if _, stderr, code := runAgentDeck(t, home, "recall", "context", sess, "--into", "remote-target"); code != 2 || !strings.Contains(stderr, "remote_cards = true") || !strings.Contains(stderr, "host-a") {
		t.Fatalf("--into an --ssh session with remote_cards off: %d %s", code, stderr)
	}
}

// TestRecallContext_IntoCurrent_ShellCallerEndToEnd: a session that is
// not the Claude one (the caller, identified by AGENTDECK_INSTANCE_ID as
// every session agent-deck starts is) runs `recall context <claude
// session> --into current` and the Claude conversation lands in its own
// pane as a prompt. The caller is a plain shell session on a real tmux
// server, so what this proves is the AGENTDECK_INSTANCE_ID resolution and
// the tmux keystroke delivery, the path a Codex target takes too
// (chooseSendTransport: only a Claude target with send_transport = "auto"
// ever takes the socket); a shell echoes what it receives, so the pane is
// the evidence. It does not prove a Codex composer accepting the prompt:
// that needs a live rollout identity the fixture cannot mint, and stays
// open (docs/recall.md, "Context handoff"). Needs tmux (the Docker image).
func TestRecallContext_IntoCurrent_ShellCallerEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home, stats := recallHome(t, 1)
	if _, _, code := runAgentDeck(t, home, "recall", "backfill", "--json"); code != 0 {
		t.Fatal("backfill")
	}
	claudeSess := stats.Sessions[0]
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	tmuxEnv := []string{"TMUX_TMPDIR=" + os.Getenv("TMUX_TMPDIR")}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", tmuxEnv, "add", "-t", "codex-caller", "-c", "shell", proj, "--json")
	if code != 0 {
		t.Fatalf("add: %s %s", stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	mustJSON(t, stdout, &added)
	t.Cleanup(func() { runAgentDeckEnv(t, home, "", tmuxEnv, "session", "stop", added.ID) })
	if stdout, stderr, code := runAgentDeckEnv(t, home, "", tmuxEnv, "session", "start", added.ID, "--json"); code != 0 {
		t.Fatalf("start: %s %s", stdout, stderr)
	}
	// The caller: inside its own pane every session has AGENTDECK_INSTANCE_ID.
	callerEnv := append(append([]string{}, tmuxEnv...), "AGENTDECK_INSTANCE_ID="+added.ID)
	stdout, stderr, code = runAgentDeckEnv(t, home, "", callerEnv, "recall", "context", claudeSess, "--tier", "card", "--into", "current")
	if code != 0 {
		t.Fatalf("recall context --into current: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "card tier") || !strings.Contains(stdout, "Sent message to 'codex-caller'") {
		t.Fatalf("delivery report:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	deadline := time.Now().Add(15 * time.Second)
	var pane string
	for time.Now().Before(deadline) {
		pane, _, _ = runAgentDeckEnv(t, home, "", tmuxEnv, "session", "output", added.ID, "--pane")
		if strings.Contains(pane, "Recalled conversation #") && strings.Contains(pane, "harness: claude") {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the recalled Claude context never landed in the caller's pane:\n%s", pane)
}

// ---- the built-in MCP entry --------------------------------------------------

func TestMCPList_ShowsRecallWhileEnabled(t *testing.T) {
	home, _ := recallHome(t, 1)
	stdout, _, code := runAgentDeck(t, home, "mcp", "list", "--json")
	if code != 0 {
		t.Fatal(stdout)
	}
	var list struct {
		MCPs []struct {
			Name        string   `json:"name"`
			Command     string   `json:"command"`
			Args        []string `json:"args"`
			Description string   `json:"description"`
		} `json:"mcps"`
	}
	mustJSON(t, stdout, &list)
	found := false
	for _, m := range list.MCPs {
		if m.Name == "recall" {
			found = true
			// The test binary is a dev build outside every install dir, so
			// the entry keeps the bare command (the hook rule: a build
			// path in a project's .mcp.json breaks when the build goes).
			if len(m.Args) != 2 || m.Args[0] != "recall" || m.Args[1] != "mcp" || m.Command != "agent-deck" || !strings.Contains(m.Description, "built-in") {
				t.Fatalf("recall mcp entry: %+v", m)
			}
		}
	}
	if !found {
		t.Fatalf("mcp list lacks the built-in recall entry: %s", stdout)
	}
	names, _, _ := runAgentDeck(t, home, "mcp", "list", "--quiet")
	if !strings.Contains(names, "recall") {
		t.Fatalf("mcp list --quiet: %s", names)
	}
	// Off by default: no entry.
	off := t.TempDir()
	stdout, _, _ = runAgentDeck(t, off, "mcp", "list", "--json")
	if strings.Contains(stdout, `"recall"`) {
		t.Fatalf("recall MCP offered while [recall] is off: %s", stdout)
	}
}

// TestRecallMCP_AnswersToolsListAndSearchOverStdio drives the built binary
// as an MCP server: initialize, tools/list, one recall_search, one
// recall_context; then stdin closes and the server exits 0.
func TestRecallMCP_AnswersToolsListAndSearchOverStdio(t *testing.T) {
	home, stats := recallHome(t, 1)
	if _, _, code := runAgentDeck(t, home, "recall", "backfill", "--json"); code != 0 {
		t.Fatal("backfill")
	}
	stdin := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"recall_search","arguments":{"query":"test","limit":2}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"recall_context","arguments":{"session":"` + stats.Sessions[0] + `","tier":"card"}}}`,
	}, "\n") + "\n"
	stdout, stderr, code := runAgentDeckStdin(t, home, stdin, "recall", "mcp")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 4 {
		t.Fatalf("replies = %d:\n%s", len(lines), stdout)
	}
	var tools struct {
		Result struct {
			Tools []struct{ Name string } `json:"tools"`
		} `json:"result"`
	}
	mustJSON(t, lines[1], &tools)
	if len(tools.Result.Tools) != 3 {
		t.Fatalf("tools/list: %s", lines[1])
	}
	var call struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	mustJSON(t, lines[2], &call)
	if call.Result.IsError || !strings.Contains(call.Result.Content[0].Text, `"native_id": "`+stats.Sessions[0][:8]) {
		t.Fatalf("recall_search: %s", lines[2])
	}
	mustJSON(t, lines[3], &call)
	if call.Result.IsError || !strings.HasPrefix(call.Result.Content[0].Text, "Recalled conversation #") {
		t.Fatalf("recall_context: %s", lines[3])
	}
	// With recall off the server refuses to start with the usual line.
	if _, stderr, code := runAgentDeck(t, t.TempDir(), "recall", "mcp"); code != 2 || !strings.Contains(stderr, "recall is off") {
		t.Fatalf("mcp with recall off: %d %s", code, stderr)
	}
}
