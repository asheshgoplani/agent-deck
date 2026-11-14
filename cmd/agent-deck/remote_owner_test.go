package main

// ASK-172: sandbox owners in `remote list`, and the sandbox's ssh_config
// path in --json so a client does not have to search the data dir for it.

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// setSandboxOwner writes [sandbox] owner into the test HOME's config.
func setSandboxOwner(t *testing.T, owner string) {
	t.Helper()
	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Sandbox.Owner = owner
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
}

type remoteListRow struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Owner     string `json:"owner"`
	SSHConfig string `json:"ssh_config"`
}

func remoteListJSON(t *testing.T, args ...string) map[string]remoteListRow {
	t.Helper()
	session.ClearUserConfigCache()
	out := captureStdout(t, func() { handleRemoteList(append([]string{"--json"}, args...)) })
	var rows []remoteListRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("remote list --json is not a JSON array: %v\n%s", err, out)
	}
	byName := map[string]remoteListRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	return byName
}

func TestRemoteList_ShowsSandboxOwnerAndSSHConfig(t *testing.T) {
	env := newProviderTestEnv(t, map[string]session.RemoteConfig{"plain": {Host: "me@box"}})
	setSandboxOwner(t, "alice")
	rc := provisionForTest(t, env, "own1")
	if rc.Owner != "alice" {
		t.Fatalf("registered owner = %q, want alice", rc.Owner)
	}

	rows := remoteListJSON(t)
	if got := rows["own1"]; got.Owner != "alice" || got.SSHConfig != rc.SSHConfig || got.SSHConfig == "" {
		t.Fatalf("sandbox row = %+v, want owner alice and ssh_config %q", got, rc.SSHConfig)
	}
	session.ClearUserConfigCache()
	raw := captureStdout(t, func() { handleRemoteList([]string{"--json"}) })
	if strings.Count(raw, `"owner"`) != 1 || strings.Count(raw, `"ssh_config"`) != 1 {
		t.Fatalf("plain remote got an owner or ssh_config field:\n%s", raw)
	}

	session.ClearUserConfigCache()
	text := captureStdout(t, func() { handleRemoteList(nil) })
	var line, header string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "own1 ") {
			line = l
		}
		if strings.HasPrefix(l, "NAME ") {
			header = l
		}
	}
	if !strings.Contains(line, "sandbox (provider fake)") || !strings.Contains(line, "owner: alice") {
		t.Fatalf("remote list row for own1 = %q, want kind and \"owner: alice\"\n%s", line, text)
	}
	// The wider host cell must not push PATH out of its column.
	col := func(s, sub string) int { return utf8.RuneCountInString(s[:strings.Index(s, sub)]) }
	if col(header, "PATH") != col(line, "agent-deck") {
		t.Fatalf("PATH column misaligned:\n%s\n%s", header, line)
	}
}

// --owner keeps only the sandboxes of that person; "me" is this machine's
// own owner name.
func TestRemoteList_OwnerFilter(t *testing.T) {
	env := newProviderTestEnv(t, map[string]session.RemoteConfig{
		"plain":  {Host: "me@box"},
		"bobs":   {Host: "agentdeck-sb-bobs", Provider: "fake", SandboxID: "sb-b", Owner: "bob"},
		"legacy": {Host: "agentdeck-sb-legacy", Provider: "fake", SandboxID: "sb-l"},
	})
	setSandboxOwner(t, "alice")
	provisionForTest(t, env, "mine")

	for _, tt := range []struct {
		filter string
		want   []string
	}{
		{"me", []string{"mine"}},
		{"alice", []string{"mine"}},
		{"bob", []string{"bobs"}},
		{"nobody", nil},
	} {
		rows := remoteListJSON(t, "--owner", tt.filter)
		var got []string
		for name := range rows {
			got = append(got, name)
		}
		if len(got) != len(tt.want) || (len(tt.want) == 1 && got[0] != tt.want[0]) {
			t.Fatalf("--owner %s: rows %v, want %v", tt.filter, got, tt.want)
		}
	}
	session.ClearUserConfigCache()
	if raw := strings.TrimSpace(captureStdout(t, func() { handleRemoteList([]string{"--json", "--owner", "nobody"}) })); raw != "[]" {
		t.Fatalf("--owner with no match must print [] (parseable), got %q", raw)
	}
	if all := remoteListJSON(t); len(all) != 4 {
		t.Fatalf("without --owner: %d rows, want all 4", len(all))
	}
}

// Review of #3019: an owner read from config.toml (a hand-edited remote,
// [sandbox] owner behind --owner me, or --owner itself) is printed without
// its control characters, in text and in --json.
func TestRemoteList_OwnerControlCharsAreNotPrinted(t *testing.T) {
	const evil = "bo\x1b]0;pwned\x07b\nx\x1b[2J"
	newProviderTestEnv(t, map[string]session.RemoteConfig{
		"bobs": {Host: "agentdeck-sb-bobs", Provider: "fake", SandboxID: "sb-b", Owner: evil},
	})
	setSandboxOwner(t, "me\x1b[31mtoo\nz")
	check := func(what, out string) {
		t.Helper()
		for _, bad := range []string{"\x1b", "\x07", "b\nx", "too\nz"} {
			if strings.Contains(out, bad) {
				t.Fatalf("%s prints the owner's control character %q:\n%q", what, bad, out)
			}
		}
	}
	session.ClearUserConfigCache()
	text := captureStdout(t, func() { handleRemoteList(nil) })
	check("remote list", text)
	if !strings.Contains(text, "owner: bo") {
		t.Fatalf("remote list lost the printable owner:\n%s", text)
	}
	session.ClearUserConfigCache()
	check("remote list --owner me", captureStdout(t, func() { handleRemoteList([]string{"--owner", "me"}) }))
	session.ClearUserConfigCache()
	check("remote list --owner <evil>", captureStdout(t, func() { handleRemoteList([]string{"--owner", "x\x1b]0;y\x07"}) }))
	if got := remoteListJSON(t)["bobs"].Owner; strings.ContainsAny(got, "\x1b\x07\n") {
		t.Fatalf("remote list --json owner keeps control characters: %q", got)
	}
}
