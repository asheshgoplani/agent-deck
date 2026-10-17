package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSessionSwitchAccount_SharedProjectsStore(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "remote-ssh"}[remote], func(t *testing.T) {
			for _, sharedStore := range []bool{true, false} {
				t.Run(map[bool]string{true: "shared", false: "different"}[sharedStore], func(t *testing.T) {
					if testing.Short() {
						t.Skip("subprocess CLI test skipped in short mode")
					}
					home := t.TempDir()
					adDir := filepath.Join(home, ".agent-deck")
					project := filepath.Join(home, "project")
					shared := filepath.Join(home, "shared")
					for _, dir := range []string{adDir, shared, project} {
						if err := os.MkdirAll(dir, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					var config strings.Builder
					for _, account := range []string{"personal", "work"} {
						root := filepath.Join(home, ".claude-"+account)
						if err := os.MkdirAll(root, 0o700); err != nil {
							t.Fatal(err)
						}
						target := shared
						if account == "work" && !sharedStore {
							target = filepath.Join(home, "different-store")
							if err := os.MkdirAll(target, 0o700); err != nil {
								t.Fatal(err)
							}
						}
						if err := os.Symlink(target, filepath.Join(root, "projects")); err != nil {
							t.Fatal(err)
						}
						fmt.Fprintf(&config, "[profiles.%s.claude]\nconfig_dir = %q\n", account, root)
					}
					if err := os.WriteFile(filepath.Join(adDir, "config.toml"), []byte(config.String()), 0o600); err != nil {
						t.Fatal(err)
					}
					id := sessionMoveAddSession(t, home, project, "shared-switch", "--account", "personal")
					sid := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
					stdout, stderr, code := runAgentDeck(t, home, "session", "set", id, "claude-session-id", sid)
					if code != 0 {
						t.Fatalf("seed session ID: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
					}
					p := filepath.Join(shared, session.ConvertToClaudeDirName(project), sid+".jsonl")
					if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
						t.Fatal(err)
					}
					body := []byte(fmt.Sprintf("{\"sessionId\":%q,\"type\":\"user\",\"timestamp\":\"2026-10-10T00:00:00Z\",\"message\":\"shared\"}\n", sid))
					if err := os.WriteFile(p, body, 0o600); err != nil {
						t.Fatal(err)
					}
					args := []string{"session", "switch-account", id, "work", "--no-restart", "--json"}
					if remote {
						controller, shim := t.TempDir(), t.TempDir()
						startParitySSH(t, home, shim)
						configPath := filepath.Join(controller, ".config", "agent-deck", "config.toml")
						if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
							t.Fatal(err)
						}
						config := fmt.Sprintf("[remotes.lab]\nhost = 'test-host'\nprofile = 'ch_support_test'\nagent_deck_path = %q\n", channelsCLIBinary(t))
						if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
							t.Fatal(err)
						}
						stdout, stderr, code = runAgentDeckEnv(t, controller, "",
							[]string{"PATH=" + shim + ":" + os.Getenv("PATH")}, append([]string{"remote", "lab"}, args...)...)
						out, remoteErr, showCode := runAgentDeck(t, home, "session", "show", id, "--json")
						if showCode != 0 {
							t.Fatalf("remote state read: %d %s %s", showCode, out, remoteErr)
						}
						var state struct {
							Account string `json:"account"`
						}
						if err := json.Unmarshal([]byte(out), &state); err != nil {
							t.Fatal(err)
						}
						wantAccount := "personal"
						if sharedStore {
							wantAccount = "work"
						}
						if state.Account != wantAccount {
							t.Fatalf("remote persisted account=%q, want %q", state.Account, wantAccount)
						}
					} else {
						stdout, stderr, code = runAgentDeck(t, home, args...)
					}
					t.Logf("switch-account exit=%d stdout=%s stderr=%s", code, stdout, stderr)
					if sharedStore {
						if code != 0 {
							t.Fatalf("already-shared store refused: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
						}
						var response struct {
							Success    bool   `json:"success"`
							NewAccount string `json:"new_account"`
						}
						if err := json.Unmarshal([]byte(stdout), &response); err != nil || !response.Success || response.NewAccount != "work" {
							t.Fatalf("invalid successful switch: %s, %v", stdout, err)
						}
					} else if code == 0 || !strings.Contains(stdout+stderr, "symlink") {
						t.Fatalf("different symlink stores must refuse with nonzero exit: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
					}
					got, err := os.ReadFile(p)
					if err != nil || string(got) != string(body) {
						t.Fatalf("source transcript changed: %q, %v", got, err)
					}
				})
			}
		})
	}
}
