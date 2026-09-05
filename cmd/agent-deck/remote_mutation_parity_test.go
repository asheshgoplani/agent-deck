package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Two independent registries receive equivalent local and SSH operations.
// Compare successful JSON schemas and independently inspect each side effect.
func TestRemoteSuccessfulMutationParity(t *testing.T) {
	bin := channelsCLIBinary(t)
	controller, server, local, shim := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	startParitySSH(t, server, shim)
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(controller, ".config/agent-deck/config.toml"), fmt.Sprintf("[remotes.lab]\nhost = 'test-host'\nagent_deck_path = '%s'\n", bin))
	homes := []string{local, server}
	sockets := []string{fmt.Sprintf("parity-local-%d", time.Now().UnixNano()), fmt.Sprintf("parity-server-%d", time.Now().UnixNano())}
	git := func(home string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", home}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	for i, home := range homes {
		write(filepath.Join(home, ".config/agent-deck/config.toml"), "[tmux]\nsocket_name = '"+sockets[i]+"'\n[profiles.person_a.claude]\nconfig_dir = '"+filepath.Join(home, "claude-a")+"'\n[mcps.parity]\ncommand = 'true'\n")
		git(home, "init", "-b", "main")
		git(home, "config", "user.name", "Test")
		git(home, "config", "user.email", "test@example.invalid")
		git(home, "commit", "--allow-empty", "-m", "fixture")
		git(home, "update-ref", "refs/remotes/origin/main", "HEAD")
		write(filepath.Join(home, ".config/agent-deck/skills/pool/parity/SKILL.md"), "---\nname: parity\ndescription: Test fixture\n---\nExact skill content.\n")
		write(filepath.Join(home, "receiver.py"), `import os, tty, hashlib
tty.setraw(0)
print("READY", flush=True)
buf = b""
while True:
    data = os.read(0, 8192)
    if not data: break
    for byte in data:
        if byte in (10,13):
            cleaned = buf.replace(b"\x1b[200~",b"").replace(b"\x1b[201~",b"")
            print("\r\nRX:"+hashlib.sha256(cleaned).hexdigest()+":"+str(len(cleaned)),flush=True)
            buf=b""
        else: buf+=bytes([byte])
`)
	}
	run := func(side int, stdin string, args ...string) (string, string, int) {
		t.Helper()
		home := homes[side]
		if side == 1 {
			home = controller
			args = append([]string{"remote", "lab"}, args...)
		}
		cmd := exec.Command(bin, args...)
		cmd.Dir = home
		for _, kv := range os.Environ() {
			key := strings.SplitN(kv, "=", 2)[0]
			if key == "HOME" || key == "PATH" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "AGENTDECK_") || strings.HasPrefix(key, "TMUX") {
				continue
			}
			cmd.Env = append(cmd.Env, kv)
		}
		cmd.Env = append(cmd.Env, "HOME="+home, "PATH="+shim+":"+os.Getenv("PATH"))
		cmd.Stdin = strings.NewReader(stdin)
		var out, stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		return out.String(), stderr.String(), code
	}
	must := func(side int, stdin string, args ...string) string {
		t.Helper()
		out, err, code := run(side, stdin, args...)
		if code != 0 {
			t.Fatalf("side %d %v: %d %s %s", side, args, code, out, err)
		}
		return out
	}
	var shape func(any) any
	shape = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			m := map[string]any{}
			for k, v := range x {
				m[k] = shape(v)
			}
			return m
		case []any:
			a := make([]any, len(x))
			for i, v := range x {
				a[i] = shape(v)
			}
			return a
		default:
			return fmt.Sprintf("%T", v)
		}
	}
	decode := func(out string) any {
		t.Helper()
		var v any
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("JSON required: %v %q", err, out)
		}
		if m, ok := v.(map[string]any); ok && len(m) == 0 {
			t.Fatalf("successful JSON object is empty: %q", out)
		}
		return v
	}
	waitFor := func(label string, ready func() bool) {
		t.Helper()
		until := time.Now().Add(10 * time.Second)
		for time.Now().Before(until) {
			if ready() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal(label + " timeout")
	}
	exists := func(side int, title string) bool {
		t.Helper()
		state := decode(must(side, "", "session", "show", title, "--json")).(map[string]any)
		name, _ := state["tmux_session"].(string)
		cmd := exec.Command("tmux", "-L", sockets[side], "has-session", "-t", name)
		cmd.Env = []string{"HOME=" + homes[side], "PATH=" + os.Getenv("PATH")}
		return cmd.Run() == nil
	}
	for side := range homes {
		side := side
		t.Cleanup(func() {
			for _, title := range []string{"receiver", "launched"} {
				run(side, "", "session", "stop", title)
			}
		})
	}
	cases := []struct {
		name  string
		args  func(int) []string
		stdin string
		check func(int)
	}{
		{name: "add", args: func(i int) []string {
			return []string{"add", homes[i], "--title", "receiver", "--cmd", "shell", "--wrapper", "python3 -u " + filepath.Join(homes[i], "receiver.py"), "--json"}
		}, check: func(i int) {
			if exists(i, "receiver") {
				t.Fatal("add unexpectedly started receiver")
			}
		}},
		{name: "start", args: func(int) []string { return []string{"session", "start", "receiver", "--json"} }, check: func(i int) {
			if !exists(i, "receiver") {
				t.Fatal("start left no session")
			}
			waitFor("ready", func() bool { return strings.Contains(must(i, "", "session", "output", "receiver", "--json"), "READY") })
		}},
		{name: "send-inline", args: func(int) []string {
			return []string{"session", "send", "receiver", strings.Repeat("abcd", 1024), "--no-wait", "--json"}
		}, check: func(i int) {
			receipt := fmt.Sprintf("RX:%x:4096", sha256.Sum256([]byte(strings.Repeat("abcd", 1024))))
			waitFor("inline receipt", func() bool { return strings.Contains(must(i, "", "session", "output", "receiver", "--json"), receipt) })
		}},
		{name: "send-stdin", stdin: strings.Repeat("wxyz", 1024), args: func(int) []string {
			return []string{"session", "send", "receiver", "--message-file", "-", "--no-wait", "--json"}
		}, check: func(i int) {
			receipt := fmt.Sprintf("RX:%x:4096", sha256.Sum256([]byte(strings.Repeat("wxyz", 1024))))
			waitFor("stdin receipt", func() bool { return strings.Contains(must(i, "", "session", "output", "receiver", "--json"), receipt) })
		}},
		{name: "show-alias", args: func(i int) []string {
			if i == 1 {
				return []string{"show", "receiver", "--json"}
			}
			return []string{"session", "show", "receiver", "--json"}
		}},
		{name: "output", args: func(int) []string { return []string{"session", "output", "receiver", "--json"} }},
		{name: "restart", args: func(int) []string { return []string{"session", "restart", "receiver", "--force", "--json"} }, check: func(i int) {
			if !exists(i, "receiver") {
				t.Fatal("restart left no session")
			}
		}},
		{name: "stop", args: func(int) []string { return []string{"session", "stop", "receiver", "--json"} }, check: func(i int) {
			if exists(i, "receiver") {
				t.Fatal("stop left session alive")
			}
		}},
		{name: "add-worktree", args: func(i int) []string {
			return []string{"add", homes[i], "--title", "worktree", "--cmd", "claude", "--worktree", "added", "--new-branch", "--location", "subdirectory", "--account", "person_a", "--json"}
		}},
		{name: "launch-worktree", args: func(i int) []string {
			return []string{"launch", homes[i], "--title", "launched", "--cmd", "shell", "--worktree", "launched", "--new-branch", "--location", "subdirectory", "--account", "person_a", "--no-wait", "--json"}
		}, check: func(i int) {
			if !exists(i, "launched") {
				t.Fatal("launch left no session")
			}
		}},
		{name: "worktree-list", args: func(int) []string { return []string{"worktree", "list", "--json"} }},
		{name: "worktree-info", args: func(int) []string { return []string{"worktree", "info", "worktree", "--json"} }},
		{name: "mcp-attach", args: func(int) []string { return []string{"mcp", "attach", "worktree", "parity", "--json"} }},
		{name: "skill-attach", args: func(int) []string { return []string{"skill", "attach", "worktree", "parity", "--json"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results := make([]any, 2)
			errors := make([]string, 2)
			for side := range homes {
				out, stderr, code := run(side, tc.stdin, tc.args(side)...)
				if code != 0 {
					t.Fatalf("side %d: %d %s %s", side, code, out, stderr)
				}
				results[side] = shape(decode(out))
				errors[side] = strings.ReplaceAll(stderr, homes[side], "<fixture>")
				if tc.check != nil {
					tc.check(side)
				}
			}
			if errors[0] != errors[1] {
				t.Fatalf("stderr differs: %q %q", errors[0], errors[1])
			}
			if !reflect.DeepEqual(results[0], results[1]) {
				t.Fatalf("schemas differ: %#v %#v", results[0], results[1])
			}
		})
	}
	for side, home := range homes {
		// Inspect each independent SQLite registry, including selected account
		// and the actual project/worktree link, rather than trusting CLI text.
		seen := map[string]bool{}
		err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, "/profiles/default/state.db") {
				return nil
			}
			db, err := statedb.Open(path)
			if err != nil {
				return err
			}
			defer db.Close()
			rows, err := db.LoadInstances()
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row.Title == "worktree" || row.Title == "launched" {
					seen[row.Title] = true
					if row.Account != "person_a" || row.ProjectPath != row.WorktreePath || row.WorktreeRepo != home {
						t.Errorf("side %d persisted worktree/account mismatch: %+v", side, row)
					}
				}
			}
			return nil
		})
		if err != nil || len(seen) != 2 {
			t.Fatalf("side %d persisted worktrees: %v %v", side, seen, err)
		}

		for _, pair := range [][2]string{{"worktree", "added"}, {"launched", "launched"}} {
			state := decode(must(side, "", "worktree", "info", pair[0], "--json")).(map[string]any)
			if state["worktree_exists"] != true {
				t.Fatalf("missing worktree: %+v", state)
			}
			path, ok := state["worktree_path"].(string)
			if !ok {
				t.Fatalf("missing path: %+v", state)
			}
			if branch := strings.TrimSpace(git(path, "branch", "--show-current")); branch != "feature/"+pair[1] {
				t.Fatalf("branch=%q", branch)
			}
			if pair[0] == "worktree" {
				data, err := os.ReadFile(filepath.Join(path, ".mcp.json"))
				if err != nil {
					t.Fatal(err)
				}
				var m map[string]any
				if json.Unmarshal(data, &m) != nil || m["mcpServers"].(map[string]any)["parity"].(map[string]any)["command"] != "true" {
					t.Fatalf("MCP config: %s", data)
				}
				actual, err := os.ReadFile(filepath.Join(path, ".claude/skills/parity/SKILL.md"))
				expected, _ := os.ReadFile(filepath.Join(home, ".config/agent-deck/skills/pool/parity/SKILL.md"))
				if err != nil || !bytes.Equal(actual, expected) {
					t.Fatalf("skill content differs: %v", err)
				}
			}
		}
		orphan := filepath.Join(home, "orphan")
		git(home, "worktree", "add", "-b", "orphan", orphan)
		must(side, "yes\n", "worktree", "cleanup", "--force")
		if _, err := os.Stat(orphan); !os.IsNotExist(err) {
			t.Fatalf("side %d orphan remains: %v", side, err)
		}
		if strings.Contains(git(home, "worktree", "list", "--porcelain"), orphan) {
			t.Fatal("orphan remains registered")
		}
	}
}
