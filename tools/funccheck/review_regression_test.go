package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompletionCollectionContract(t *testing.T) {
	const delivered = `{"child_session_id":"child","done_status":"ok","done_summary":"synthetic prompt completed"}`
	for _, payload := range []string{
		`null`, `[{}]`, `[null]`, `[{"child_session_id":"child"}]`,
		`[{"child_session_id":"other","done_status":"ok"}]`,
		`[` + delivered + `,{}]`, `[` + delivered + `,null]`,
	} {
		t.Run(payload, func(t *testing.T) {
			if count, err := completionCount("["+delivered+"]", "child"); err != nil || count != 1 {
				t.Fatalf("initial delivery: count=%d err=%v", count, err)
			}
			if _, err := completionCount(payload, "child"); err == nil {
				t.Fatalf("malformed drain accepted after delivery: %s", payload)
			}
		})
	}
	for _, payload := range []string{`[]`, `[{"child_session_id":"other","done_status":"fail","done_summary":"other completion"}]`, `[{"child_session_id":"child","to_status":"waiting"}]`} {
		if count, err := completionCount(payload, "child"); err != nil || count != 0 {
			t.Fatalf("valid consumed drain: count=%d err=%v", count, err)
		}
	}
}

func writeReviewFixture(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
}

// The fake CLI succeeds at creation and retrieval before its cleanup responses
// change. This exercises the actual inverse-operation checks, not just decoding.
func remoteReviewFixture(t *testing.T, sessions, remotes string, helpExit int) *suite {
	t.Helper()
	s := assertionFixture(t, "", 0)
	if err := os.MkdirAll(filepath.Join(s.root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeReviewFixture(t, s.bin, fmt.Sprintf(`case "$*" in
  'remote add '*) exit 0;;
  'remote list --json')
    if [ -f "$HOME/remote-removed" ]; then printf '%%s\n' %s
    else printf '%%s\n' '[{"name":"funccheck-remote","host":"funccheck.invalid","profile":"funccheck_remote"}]'; fi;;
  'remote funccheck-remote add '*) touch "$HOME/created";;
  '-p funccheck_remote session show --json funccheck-remote-session')
    if [ -f "$HOME/session-removed" ]; then printf '%%s\n' '{"success":false,"code":"NOT_FOUND"}'; exit 2; fi
    printf '%%s\n' '{"id":"remote-id","title":"funccheck-remote-session","profile":"funccheck_remote"}';;
  'session show --json funccheck-remote-session') printf '%%s\n' '{"success":false,"code":"NOT_FOUND"}'; exit 2;;
  'remote sessions --json funccheck-remote')
    [ -f "$HOME/created" ]
    if [ -f "$HOME/session-removed" ]; then printf '%%s\n' %s
    else printf '%%s\n' '[{"id":"remote-id","title":"funccheck-remote-session"}]'; fi;;
  'session switch --help') printf 'switch help\n'; exit %d;;
  '-p funccheck_remote session remove --force funccheck-remote-session') touch "$HOME/session-removed";;
  'remote remove funccheck-remote') touch "$HOME/remote-removed";;
  *) exit 99;;
esac
`, shQuote(remotes), shQuote(sessions), helpExit))
	return s
}

func requireReviewResult(t *testing.T, s *suite, name, status string) {
	t.Helper()
	for _, result := range s.results {
		if result.Name == name {
			if result.Status != status {
				t.Fatalf("%s: got %s, want %s: %+v", name, result.Status, status, result)
			}
			return
		}
	}
	t.Fatalf("missing result %s", name)
}

func TestRemoteCleanupCollectionContract(t *testing.T) {
	const emptyRemotes = "No remotes configured.\n\nAdd one with: agent-deck remote add <name> <user@host>"
	for _, tc := range []struct {
		name, sessions, remotes, status string
	}{
		{"empty arrays", `[]`, `[]`, "PASS"},
		{"documented empties", `null`, emptyRemotes, "PASS"},
		{"session missing ID", `[{"title":"other"}]`, `[]`, "FAIL"},
		{"session empty row", `[{}]`, `[]`, "FAIL"},
		{"session null row", `[null]`, `[]`, "FAIL"},
		{"session bad row after valid", `[{"id":"other","title":"other"},{}]`, `[]`, "FAIL"},
		{"remote null", `[]`, `null`, "FAIL"},
		{"remote empty row", `[]`, `[{}]`, "FAIL"},
		{"remote null row", `[]`, `[null]`, "FAIL"},
		{"remote bad row after valid", `[]`, `[{"name":"other"},{}]`, "FAIL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := remoteReviewFixture(t, tc.sessions, tc.remotes, 0)
			s.auxRemote()
			requireReviewResult(t, s, "Remote create", "PASS")
			requireReviewResult(t, s, "Remote sessions", "PASS")
			requireReviewResult(t, s, "Remote cleanup", tc.status)
		})
	}
}

func TestRemotePresenceValidatesRowsAfterMatch(t *testing.T) {
	for _, tc := range []struct {
		name, valid string
	}{
		{"Remote list", `[{"name":"funccheck-remote","host":"funccheck.invalid","profile":"funccheck_remote"}]`},
		{"Remote sessions", `[{"id":"remote-id","title":"funccheck-remote-session"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := remoteReviewFixture(t, `[]`, `[]`, 0)
			script, err := os.ReadFile(s.bin)
			if err != nil {
				t.Fatal(err)
			}
			broken := strings.TrimSuffix(tc.valid, "]") + ",{}]"
			if err := os.WriteFile(s.bin, []byte(strings.ReplaceAll(string(script), tc.valid, broken)), 0o700); err != nil {
				t.Fatal(err)
			}
			s.auxRemote()
			requireReviewResult(t, s, tc.name, "FAIL")
		})
	}
}

func TestRemoteSwitchProbeFailureIsUnknown(t *testing.T) {
	s := remoteReviewFixture(t, `[]`, `[]`, 1)
	s.auxRemote()
	requireReviewResult(t, s, "Remote switch", "UNKNOWN")
}

func TestCompletionRemovalRequiresPaneAbsence(t *testing.T) {
	for _, requireHooks := range []bool{false, true} {
		for _, orphan := range []bool{false, true} {
			t.Run(fmt.Sprintf("hooks=%v/orphan=%v", requireHooks, orphan), func(t *testing.T) {
				s := &suite{bin: "/bin/true", ctx: context.Background()}
				defer s.cleanup()
				if err := s.setup(); err != nil {
					t.Fatal(err)
				}
				// The prefix-sharing neighbour must never be mistaken for the target.
				for _, name := range []string{"completion-pane", "completion-pane-neighbour"} {
					if _, err := s.exec("tmux", "new-session", "-d", "-s", name, "sleep 60"); err != nil {
						t.Fatal(err)
					}
				}
				hookDir := filepath.Join(s.root, "hooks")
				if err := os.MkdirAll(hookDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(hookDir, "child.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				removePane := ":"
				if !orphan {
					removePane = `tmux kill-session -t '=completion-pane'`
				}
				s.bin = filepath.Join(s.root, "bin", "fixture")
				writeReviewFixture(t, s.bin, `case "$*" in
  'session show --json child')
    if [ -f "$HOME/removed" ]; then printf '%s\n' '{"success":false,"code":"NOT_FOUND"}'; exit 2; fi
    printf '%s\n' '{"id":"child","tmux_session":"completion-pane"}';;
  'remove --json child') touch "$HOME/removed"; mv "$HOME/hooks/child.json" "$HOME/removed-hook"; `+removePane+` ;;
  'list --json') printf '[]\n';;
  *) exit 99;;
esac
`)
				_, err := s.removeCompletionSession("child", requireHooks)
				if orphan {
					if err == nil || !strings.Contains(err.Error(), "pane remains") {
						t.Fatalf("orphan pane must fail independently of hooks: %v", err)
					}
				} else if err != nil {
					t.Fatalf("fully removed session: %v", err)
				}
			})
		}
	}
}
