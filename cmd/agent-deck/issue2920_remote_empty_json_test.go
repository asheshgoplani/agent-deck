package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestIssue2920_RemoteEmptyJSON pins that every remote subcommand which
// reports "No remotes configured." keeps stdout valid JSON under --json
// (an empty list, or the command's own empty envelope), and keeps the
// human text without --json.
func TestIssue2920_RemoteEmptyJSON(t *testing.T) {
	cases := []struct {
		name string
		run  func()
		want string // compact JSON
	}{
		{"list --json", func() { handleRemoteList([]string{"--json"}) }, `[]`},
		{"sessions --json", func() { handleRemoteSessions([]string{"--json"}) }, `[]`},
		{"sessions --json-envelope", func() { handleRemoteSessions([]string{"--json-envelope"}) }, `{"sessions":[],"errors":[]}`},
		{"update --all --json", func() { handleRemoteUpdate([]string{"--all", "--json"}) }, `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTempHomeAndConfig(t, "")
			out := captureStdout(t, tc.run)
			var v any
			if err := json.Unmarshal([]byte(out), &v); err != nil {
				t.Fatalf("stdout is not JSON: %v\n%s", err, out)
			}
			got, _ := json.Marshal(v)
			var wantV any
			_ = json.Unmarshal([]byte(tc.want), &wantV)
			want, _ := json.Marshal(wantV)
			if string(got) != string(want) {
				t.Fatalf("stdout = %s, want %s", got, want)
			}
		})
	}

	humans := []struct {
		name string
		run  func()
	}{
		{"list", func() { handleRemoteList(nil) }},
		{"sessions", func() { handleRemoteSessions(nil) }},
		{"update --all", func() { handleRemoteUpdate([]string{"--all"}) }},
	}
	for _, tc := range humans {
		t.Run(tc.name+" human", func(t *testing.T) {
			withTempHomeAndConfig(t, "")
			out := captureStdout(t, tc.run)
			if !strings.Contains(out, "No remotes configured.") {
				t.Fatalf("human output lost the message: %q", out)
			}
		})
	}
}
