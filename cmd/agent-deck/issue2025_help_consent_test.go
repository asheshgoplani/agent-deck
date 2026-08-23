package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCommandRegistryMatchesMainDispatch(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	mainFile := filepath.Join(filepath.Dir(testFile), "main.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, mainFile, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	dispatched := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || !isArgsZero(sw.Tag) {
			return true
		}
		for _, clauseNode := range sw.Body.List {
			clause := clauseNode.(*ast.CaseClause)
			for _, expr := range clause.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err == nil {
					dispatched[value] = true
				}
			}
		}
		return false
	})
	if diff := mapKeyDiff(commandRegistry, dispatched); diff != "" {
		t.Fatalf("command registry and main dispatch differ:\n%s", diff)
	}
}

func isArgsZero(expr ast.Expr) bool {
	index, ok := expr.(*ast.IndexExpr)
	if !ok {
		return false
	}
	ident, ok := index.X.(*ast.Ident)
	if !ok || ident.Name != "args" {
		return false
	}
	lit, ok := index.Index.(*ast.BasicLit)
	return ok && lit.Value == "0"
}

func mapKeyDiff(want, got map[string]bool) string {
	var lines []string
	for key := range want {
		if !got[key] {
			lines = append(lines, "registry only: "+key)
		}
	}
	for key := range got {
		if !want[key] {
			lines = append(lines, "dispatch only: "+key)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func TestEveryRegisteredCommandHelpIsReadOnly(t *testing.T) {
	// These are protocol/version entry points, not human-facing command
	// dispatchers, and intentionally define no nested help surface.
	noHelpSurface := map[string]bool{
		"--version": true, "-v": true, "version": true,
		"codex-notify": true, "hook-handler": true,
	}
	var commands []string
	for command := range commandRegistry {
		if noHelpSurface[command] {
			continue
		}
		commands = append(commands, command)
	}
	sort.Strings(commands)
	// Flag-shaped help is unambiguous for every command. Bare "help" is a
	// legitimate value on some surfaces (for example a launch message), so
	// nested dispatchers that define it as help receive focused pins below.
	helpShapes := []string{"--help", "-h"}
	for _, command := range commands {
		for _, shape := range helpShapes {
			t.Run(command+"/"+shape, func(t *testing.T) {
				home := t.TempDir()
				out, err := runIssue2025Helper(t, home, []string{command, shape})
				if err != nil {
					t.Fatalf("help request failed: %v\n%s", err, out)
				}
				if !strings.Contains(string(out), "Usage") {
					t.Fatalf("help request did not print usage:\n%s", out)
				}
				assertNoFiles(t, home)
			})
		}
	}
}

func assertNoFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			t.Errorf("help request created %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIssue2025TrailingHelpIsReadOnly(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"deepseek sessions", []string{"deepseek", "sessions", "help"}},
		{"remote add", []string{"remote", "add", "test", "example.invalid", "help"}},
		{"notify daemon", []string{"notify-daemon", "help"}},
		{"creds refresh", []string{"creds-refresh", "--config-dir", "CONFIG_DIR", "help"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			configDir := filepath.Join(home, "creds")
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(configDir, ".credentials.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := append([]string(nil), tt.args...)
			for i := range args {
				if args[i] == "CONFIG_DIR" {
					args[i] = configDir
				}
			}

			out, err := runIssue2025Helper(t, home, args)
			if err != nil {
				t.Fatalf("help request failed: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "Usage: agent-deck") {
				t.Fatalf("help request did not print usage:\n%s", out)
			}
			if _, err := os.Stat(filepath.Join(home, ".config", "agent-deck", "config.toml")); err == nil {
				t.Fatal("help request wrote user config")
			}
			if _, err := os.Stat(filepath.Join(home, ".cache", "agent-deck", "debug.log")); err == nil {
				t.Fatal("help request created the daemon debug log")
			}
		})
	}
}

func TestRemoteSubcommandHelpRoutesToSelectedCommand(t *testing.T) {
	tests := []struct {
		command string
		usage   string
	}{
		{"add", "Usage: agent-deck remote add <name> <user@host> [options]"},
		{"remove", "Usage: agent-deck remote remove <name>"},
		{"rm", "Usage: agent-deck remote remove <name>"},
		{"list", "Usage: agent-deck remote list [options]"},
		{"ls", "Usage: agent-deck remote list [options]"},
		{"sessions", "Usage: agent-deck remote sessions [name] [options]"},
		{"attach", "Usage: agent-deck remote attach <remote-name> <session-title-or-id>"},
		{"rename", "Usage: agent-deck remote rename <remote-name> <session-title-or-id> <new-title>"},
		{"update", "Usage: agent-deck remote update [name]"},
	}

	for _, tt := range tests {
		for _, help := range []string{"--help", "-h", "help"} {
			t.Run(tt.command+"/"+help, func(t *testing.T) {
				home := t.TempDir()
				out, err := runIssue2025Helper(t, home, []string{"remote", tt.command, help})
				if err != nil {
					t.Fatalf("help request failed: %v\n%s", err, out)
				}
				if !strings.Contains(string(out), tt.usage) {
					t.Fatalf("help routed to wrong usage; want %q:\n%s", tt.usage, out)
				}
				assertNoFiles(t, home)
			})
		}
	}
}

func runIssue2025Helper(t *testing.T, home string, args []string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmdArgs := append([]string{"-test.run=^TestIssue2025HelperProcess$", "--"}, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_ISSUE2025_HELPER=1",
		"AGENT_DECK_TASK6_HELPER_PROCESS=1",
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("help request did not return promptly (command started work):\n%s", out)
	}
	return out, err
}

func TestIssue2025HelperProcess(t *testing.T) {
	if os.Getenv("AGENT_DECK_ISSUE2025_HELPER") != "1" {
		return
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	os.Args = append([]string{"agent-deck"}, os.Args[separator+1:]...)
	main()
}
