package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSessionStartKimiPermissionFlags(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	for _, mode := range []struct {
		name string
		env  []string
	}{
		{name: "registry"},
		{name: "legacy", env: []string{envCoreRegistry + "=0"}},
	} {
		for _, tc := range []struct {
			name        string
			initialArgs []string
			startArgs   []string
			wantArgs    []string
		}{
			{name: "yolo", startArgs: []string{"--yolo"}, wantArgs: []string{"--yolo"}},
			{name: "explicit false overrides never ask", initialArgs: []string{"--auto"}, startArgs: []string{"--yolo=false"}},
			{name: "auto", startArgs: []string{"--auto"}, wantArgs: []string{"--auto"}},
		} {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				home := t.TempDir()
				work := filepath.Join(home, "proj")
				binDir := filepath.Join(home, "bin")
				if err := os.MkdirAll(work, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(binDir, 0o755); err != nil {
					t.Fatal(err)
				}
				argsFile := filepath.Join(home, "kimi-args")
				if err := os.WriteFile(filepath.Join(binDir, "kimi"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$KIMI_ARGS_FILE\"\nwhile :; do sleep 1; done\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				env := append([]string{}, mode.env...)
				env = append(env, "PATH="+binDir+":"+os.Getenv("PATH"), "KIMI_ARGS_FILE="+argsFile)

				addArgs := []string{"add", "-t", "kimi-start", "-c", "kimi", "--no-parent", "--json"}
				addArgs = append(addArgs, tc.initialArgs...)
				addArgs = append(addArgs, work)
				stdout, stderr, code := runAgentDeckEnv(t, home, "", env, addArgs...)
				if code != 0 {
					t.Fatalf("add failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
				}
				t.Cleanup(func() { runAgentDeckEnv(t, home, "", env, "session", "stop", "kimi-start") })

				startArgs := append([]string{"session", "start", "kimi-start", "--no-wait"}, tc.startArgs...)
				stdout, stderr, code = runAgentDeckEnv(t, home, "", env, startArgs...)
				if code != 0 {
					t.Fatalf("start failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
				}
				data, err := os.ReadFile(argsFile)
				if err != nil {
					t.Fatal(err)
				}
				var gotArgs []string
				if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
					gotArgs = strings.Split(trimmed, "\n")
				}
				if !slices.Equal(gotArgs, tc.wantArgs) {
					t.Fatalf("Kimi argv = %q, want %q", gotArgs, tc.wantArgs)
				}
			})
		}
	}
}
