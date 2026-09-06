package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCurrentMainHelpPreservesSelectedDetailsAndState(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"remote-drain", []string{"remote", "drain"}, []string{
			"Usage: agent-deck remote drain <remote-name|user@host>",
			"--into <session-id>", "--json", "into this machine's inbox", "remote is read-only",
		}},
		{"inbox-root", []string{"inbox"}, []string{
			"Usage: agent-deck inbox <session-id>", "agent-deck inbox export [--json]",
			"agent-deck inbox writer-status [--json]", "without consuming anything",
		}},
		{"inbox-export", []string{"inbox", "export"}, []string{
			"Usage: agent-deck inbox export [--json]", "completion/transition records without consuming them",
		}},
		{"inbox-writer-status", []string{"inbox", "writer-status"}, []string{
			"Usage: agent-deck inbox writer-status [--json]", "notify-daemon is recording transitions",
		}},
	}
	for _, tc := range cases {
		for _, flag := range []string{"--help", "-h"} {
			t.Run(tc.name+"/"+flag, func(t *testing.T) {
				home := t.TempDir()
				marker := filepath.Join(home, "owner-record")
				if err := os.WriteFile(marker, []byte("existing owner data\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("owner-record", filepath.Join(home, "owner-link")); err != nil {
					t.Fatal(err)
				}
				before := snapshotTree(t, home)
				args := append(append([]string{}, tc.args...), flag)
				out, err := runIssue2025Helper(t, home, args)
				if err != nil {
					t.Fatalf("help failed: %v\n%s", err, out)
				}
				for _, want := range tc.want {
					if !strings.Contains(string(out), want) {
						t.Errorf("missing selected help detail %q:\n%s", want, out)
					}
				}
				if after := snapshotTree(t, home); !reflect.DeepEqual(before, after) {
					t.Errorf("help changed existing HOME state\nbefore: %v\nafter: %v", before, after)
				}
			})
		}
	}
}
