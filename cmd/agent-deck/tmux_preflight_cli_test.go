package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSparsePathPreflightHonorsParsedCommandBehavior(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantOutput string
		fallback   bool
	}{
		{
			name:       "web help does not require tmux",
			args:       []string{"web", "--help"},
			wantExit:   0,
			wantOutput: "Usage: agent-deck web [options]",
		},
		{
			name:       "web help token is parsed as a token value",
			args:       []string{"web", "--token", "--help", "--no-tui"},
			wantExit:   1,
			wantOutput: "Error: tmux not found",
		},
		{
			name:       "launch help does not require tmux",
			args:       []string{"launch", "--help"},
			wantExit:   0,
			wantOutput: "Usage: agent-deck launch [path] [options]",
		},
		{
			name:       "launch help token is parsed as a message value",
			args:       []string{"launch", "--message", "--help", "."},
			wantExit:   1,
			wantOutput: "Error: tmux not found",
		},
		{
			name:       "try no-session token is parsed as a command value",
			args:       []string{"try", "--cmd", "--no-session", "demo"},
			wantExit:   1,
			wantOutput: "Error: tmux not found",
		},
		{
			name:       "run-task help token is parsed as a title value",
			args:       []string{"run-task", "--child", "abc", "--title", "--help", "--", "true"},
			wantExit:   1,
			wantOutput: "Error: tmux not found",
		},
		{
			name:       "list all remains storage only",
			args:       []string{"list", "--all"},
			wantExit:   0,
			wantOutput: "No profiles found.",
		},
		{
			name:       "invalid stale threshold wins before tmux preflight",
			args:       []string{"status", "--stale", "--threshold", "invalid"},
			wantExit:   1,
			wantOutput: "invalid --threshold",
		},
		{
			name:       "status requires tmux after parsing",
			args:       []string{"status"},
			wantExit:   1,
			wantOutput: "Error: tmux not found",
		},
		{
			name:       "fallback directory repairs path before status",
			args:       []string{"status"},
			wantExit:   0,
			wantOutput: "No sessions in profile '_test'.",
			fallback:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sandbox := t.TempDir()
			emptyPath := filepath.Join(sandbox, "empty-path")
			if err := os.Mkdir(emptyPath, 0o755); err != nil {
				t.Fatal(err)
			}
			fallbackDir := ""
			if tt.fallback {
				fallbackDir = filepath.Join(sandbox, "fallback-bin")
				if err := os.Mkdir(fallbackDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fallbackDir, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			cmdArgs := append([]string{"-test.run=TestSparsePathPreflightHelperProcess", "--"}, tt.args...)
			cmd := exec.Command(os.Args[0], cmdArgs...)
			env := make([]string, 0, len(os.Environ())+2)
			for _, entry := range os.Environ() {
				if strings.HasPrefix(entry, "PATH=") || strings.HasPrefix(entry, "HOME=") ||
					strings.HasPrefix(entry, "XDG_") || strings.HasPrefix(entry, "AGENTDECK_TMUX_") {
					continue
				}
				env = append(env, entry)
			}
			cmd.Env = append(env,
				"AGENTDECK_TMUX_PREFLIGHT_HELPER=1",
				"AGENTDECK_TMUX_FALLBACK_DIR="+fallbackDir,
				"AGENTDECK_SUPPRESS_TMUX_WARNING=1",
				"PATH="+emptyPath,
				"HOME="+sandbox,
				"XDG_CONFIG_HOME="+filepath.Join(sandbox, "config"),
				"XDG_DATA_HOME="+filepath.Join(sandbox, "data"),
				"XDG_CACHE_HOME="+filepath.Join(sandbox, "cache"),
			)
			output, err := cmd.CombinedOutput()
			if exitErr, ok := err.(*exec.ExitError); ok {
				if exitErr.ExitCode() != tt.wantExit {
					t.Fatalf("exit code = %d, want %d; output:\n%s", exitErr.ExitCode(), tt.wantExit, output)
				}
			} else if err != nil {
				t.Fatalf("run helper: %v", err)
			} else if tt.wantExit != 0 {
				t.Fatalf("exit code = 0, want %d; output:\n%s", tt.wantExit, output)
			}
			if !strings.Contains(string(output), tt.wantOutput) {
				t.Fatalf("output missing %q:\n%s", tt.wantOutput, output)
			}
		})
	}
}

func TestSparsePathPreflightHelperProcess(t *testing.T) {
	if os.Getenv("AGENTDECK_TMUX_PREFLIGHT_HELPER") != "1" {
		return
	}

	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator == 0 {
		t.Fatal("missing command separator")
	}

	if fallbackDir := os.Getenv("AGENTDECK_TMUX_FALLBACK_DIR"); fallbackDir != "" {
		tmuxInstallDirs = []string{fallbackDir}
	} else {
		tmuxInstallDirs = nil
	}
	os.Args = append([]string{"agent-deck"}, os.Args[separator+1:]...)
	main()
}
