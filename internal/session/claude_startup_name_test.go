package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func startupNameConfig(t *testing.T, body string) string {
	t.Helper()
	_, config, _, _ := isolateConfigRoots(t)
	path := writeConfigAt(t, xdgAgentDeckConfigDir(config), body)
	ClearUserConfigCache()
	return path
}

func startupArgs(t *testing.T, flags string) []string {
	t.Helper()
	out, err := exec.Command("bash", "-c", "set --"+flags+"; printf '%s\\0' \"$@\"").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
}

func TestStartupNameExactArgv(t *testing.T) {
	startupNameConfig(t, "")
	for _, title := range []string{"Team A", "team-a", "team_a", "日本語", "🚀", "a'b;$(touch MUST_NOT_EXIST)`id`", strings.Repeat("long", 30) + "A", strings.Repeat("long", 30) + "B"} {
		t.Run(title, func(t *testing.T) {
			inst := &Instance{Tool: "claude", Title: title}
			args := startupArgs(t, inst.buildClaudeExtraFlags(nil))
			if len(args) != 2 || args[0] != "--name" || args[1] != title {
				t.Fatalf("argv=%q, want --name %q", args, title)
			}
		})
	}
}

func TestStartupNameRejectsControls(t *testing.T) {
	startupNameConfig(t, "")
	for _, title := range []string{"", "a\nb", "a\rb", "a\tb", "a\x1bb", "a\x00b", "a\u0085b", "a\u202eb", "a\xffb"} {
		if got := (&Instance{Tool: "claude", Title: title}).ClaudeLaunchName(); got != "" {
			t.Errorf("accepted %q as %q", title, got)
		}
	}
}

func TestStartupNamePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		enabled    bool
		unreadable bool
	}{
		{"absent", "", true, false}, {"true", "push_title=true", true, false}, {"false", "push_title=false", false, false}, {"malformed", "push_title=[", false, false}, {"unreadable", "push_title=false", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := startupNameConfig(t, tc.body)
			if tc.unreadable {
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0600) })
			}
			got := (&Instance{Tool: "claude", Title: "Title"}).ClaudeLaunchName()
			if (got != "") != tc.enabled {
				t.Fatalf("name=%q enabled=%v", got, tc.enabled)
			}
		})
	}
}

func TestStartupNameNoLiveRenameCallback(t *testing.T) {
	startupNameConfig(t, "")
	inst := &Instance{Tool: "claude", Title: "old"}
	inst.tmuxSession = &tmux.Session{Name: "startup-name-nonexistent"}
	_, callback, err := SetField(inst, FieldTitle, "new", nil)
	if err != nil {
		t.Fatal(err)
	}
	if callback != nil {
		t.Fatal("rename must not enqueue agent input, including before a failed save")
	}
}

func TestStartupNameCustomCommandAndSelectors(t *testing.T) {
	startupNameConfig(t, "")
	inst := &Instance{Tool: "claude", Title: "Title", Command: "arbitrary-wrapper"}
	if got := inst.ClaudeLaunchName(); got != "" {
		t.Fatalf("custom command receives %q", got)
	}
	for _, args := range [][]string{{"--name", "mine"}, {"-n", "mine"}, {"--name=mine"}, {"-n=mine"}, {"-nmine"}, {"-rforeign"}, {"-cr"}, {"--resume", "foreign"}, {"--resume=foreign"}, {"-c"}, {"--continue"}, {"-r"}, {"--session-id", "foreign"}, {"--fork-session"}} {
		inst := &Instance{Tool: "claude", Title: "Title", ExtraArgs: args}
		if got := inst.ClaudeLaunchName(); got != "" {
			t.Errorf("override %q receives automatic name %q", args, got)
		}
	}
	for _, mode := range []string{"continue", "resume"} {
		inst := &Instance{Tool: "claude", Title: "Title"}
		flags := inst.buildClaudeExtraFlags(&ClaudeOptions{SessionMode: mode})
		if strings.Contains(flags, "--name") {
			t.Errorf("unbound %s receives %s", mode, flags)
		}
	}
}

func TestStartupNameRegistryIndependent(t *testing.T) {
	startupNameConfig(t, "")
	dir := filepath.Join(os.Getenv("HOME"), ".claude", "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "123.json"), []byte(`{"sessionId":"owned","name":"foreign"}`), 0600); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{Tool: "claude", Title: "Exact Title", ClaudeSessionID: "owned"}
	if got := inst.ClaudeLaunchName(); got != "Exact Title" {
		t.Fatalf("name=%q", got)
	}
}

func TestStartupNameForkTarget(t *testing.T) {
	startupNameConfig(t, "")
	parent := &Instance{Tool: "claude", Title: "Parent", ClaudeSessionID: "11111111-1111-4111-8111-111111111111", ClaudeDetectedAt: time.Now()}
	parent.markClaudeSessionIDVerified()
	target := &Instance{Tool: "claude", Title: "Child 日本語", ProjectPath: t.TempDir()}
	cmd, err := parent.buildClaudeForkCommandForTarget(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "--name 'Child 日本語'") || strings.Contains(cmd, "--name Parent") {
		t.Fatalf("wrong fork target: %s", cmd)
	}
}

func TestStartupNameAccountCommand(t *testing.T) {
	for _, account := range []string{"one", "two"} {
		t.Run(account, func(t *testing.T) {
			accountDir := filepath.Join(t.TempDir(), account)
			startupNameConfig(t, fmt.Sprintf("[profiles.%s.claude]\nconfig_dir=%q\n", account, accountDir))
			bin := t.TempDir()
			script := "#!/bin/sh\nprintf '%s\\0' \"$CLAUDE_CONFIG_DIR\" \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			inst := &Instance{Tool: "claude", Title: "Exact 日本語", Account: account, ID: "deck-owned", ProjectPath: t.TempDir()}
			command := inst.buildClaudeCommandWithMessage("claude", "")
			out, err := exec.Command("bash", "-c", command).CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			args := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
			if len(args) < 5 || args[0] != accountDir {
				t.Fatalf("account argv=%q command=%s", args, command)
			}
			joined := strings.Join(args, "|")
			if !strings.Contains(joined, "--name|Exact 日本語") || !strings.Contains(joined, "--session-id|"+inst.ClaudeSessionID) {
				t.Fatalf("unbound name: %q", args)
			}
		})
	}
}

func TestStartupNamePermissionChangeFailsClosed(t *testing.T) {
	path := startupNameConfig(t, "push_title=true")
	inst := &Instance{Tool: "claude", Title: "Title"}
	if inst.ClaudeLaunchName() == "" {
		t.Fatal("enabled precondition")
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0600) })
	if inst.ClaudeLaunchName() != "" {
		t.Fatal("cached true bypassed unreadable policy")
	}
}

func TestStartupNameOwnedResumeAndScratch(t *testing.T) {
	for _, scratch := range []bool{false, true} {
		t.Run(fmt.Sprint(scratch), func(t *testing.T) {
			root := t.TempDir()
			configDir := filepath.Join(root, "account")
			startupNameConfig(t, fmt.Sprintf("[profiles.work.claude]\nconfig_dir=%q\n", configDir))
			inst := &Instance{Tool: "claude", Title: "Owned 日本語", Account: "work", ID: "deck-owned", ProjectPath: root, ClaudeSessionID: "22222222-2222-4222-8222-222222222222"}
			if scratch {
				inst.WorkerScratchConfigDir = filepath.Join(root, "scratch")
				configDir = inst.WorkerScratchConfigDir
			}
			dir := claudeProjectDirForTest(t, filepath.Join(root,"account"), root)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, inst.ClaudeSessionID+".jsonl"), []byte(`{"type":"user","sessionId":"`+inst.ClaudeSessionID+`","text":"hi"}`+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			inst.markClaudeSessionIDVerified()
            inst.SetClaudeOptions(&ClaudeOptions{SessionMode:"resume"})
			command := inst.buildClaudeResumeCommand()
			if !strings.Contains(command, "--name 'Owned 日本語'") || !strings.Contains(command, "--resume "+inst.ClaudeSessionID) || !strings.Contains(command, "CLAUDE_CONFIG_DIR="+configDir) {
				t.Fatalf("wrong bound resume: %s", command)
			}
			inst.Command = "custom-wrapper"
			if command := inst.buildClaudeResumeCommand(); strings.Contains(command, "--name") {
				t.Fatalf("custom resume: %s", command)
			}
		})
	}
}

func TestStartupNameForkExplicitOverrideAndRestart(t *testing.T) {
	startupNameConfig(t, "")
	parent := &Instance{Tool: "claude", Title: "Parent", ExtraArgs: []string{"--name", "operator"}, ClaudeSessionID: "11111111-1111-4111-8111-111111111111", ClaudeDetectedAt: time.Now()}
	parent.markClaudeSessionIDVerified()
	target := &Instance{Tool: "claude", Title: "Child", ProjectPath: t.TempDir(), ExtraArgs: append([]string(nil), parent.ExtraArgs...)}
	command, err := parent.buildClaudeForkCommandForTarget(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(command, "--name") != 1 || !strings.Contains(command, "--name operator") {
		t.Fatalf("lost override: %s", command)
	}
	target.Command = command
	target.ExtraArgs = nil
	if name := target.ClaudeLaunchName(); name != "Child" {
		t.Fatalf("generated fork restart name=%q", name)
	}
}

func TestStartupNameAliasAndUnsupported(t *testing.T) {
	startupNameConfig(t, "[claude]\ncommand='configured-alias'\n")
	inst := &Instance{Tool: "claude", Title: "Alias Title", ID: "alias", ProjectPath: t.TempDir()}
	cmd := inst.buildClaudeCommandWithMessage("claude", "initial message")
	if !strings.Contains(cmd, "configured-alias") || !strings.Contains(cmd, "--name 'Alias Title'") {
		t.Fatalf("alias lost title: %s", cmd)
	}
	for _, tool := range []string{"shell", "codex", "gemini", "opencode"} {
		if name := (&Instance{Tool: tool, Title: "Other"}).ClaudeLaunchName(); name != "" {
			t.Errorf("%s received %q", tool, name)
		}
	}
}

func BenchmarkStartupNameFlags(b *testing.B) {
	inst := &Instance{Tool: "claude", Title: "Title"}
	for n := 0; n < b.N; n++ {
		inst.buildClaudeExtraFlags(nil)
	}
}
