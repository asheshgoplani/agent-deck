package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A deliberately broken binary exits successfully for every command. The
// evaluator must fail on missing state, even though every exit code is green.
func TestBrokenBinaryFails(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("run in Docker")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatal("tmux required for functional evaluator tests")
	}
	t.Setenv("FUNCCHECK_SOURCE_CHECKS", "0")
	dir := t.TempDir()
	fake := filepath.Join(dir, "broken-agent-deck")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if code := runMain([]string{fake}); code != 1 {
		t.Fatalf("broken binary exit=%d, want 1", code)
	}
	data, err := os.ReadFile("funccheck-report.json")
	if err != nil {
		t.Fatal(err)
	}
	var report report
	if err = json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	for _, r := range report.Results {
		if r.Name == "session add" && r.Status == "FAIL" {
			t.Log("Deliberate failure proved: successful no-op binary fails session add")
			return
		}
	}
	t.Fatal("no failing session add check in broken-binary report")
}

func TestSandboxRejectsSocketOverrideAndDoesNotInheritIdentity(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatal("tmux required")
	}
	t.Setenv("AGENTDECK_SESSION_ID", "host-parent")
	t.Setenv("CLAUDE_CONFIG_DIR", "/host/private-account")
	t.Setenv("TMUX", "/host/private-socket,1,0")
	s := &suite{bin: "/bin/true", ctx: context.Background()}
	defer s.cleanup()
	if err := s.setup(); err != nil {
		t.Fatal(err)
	}
	for _, v := range s.env {
		if strings.Contains(v, "host-parent") || strings.Contains(v, "/host/") {
			t.Fatalf("host identity leaked: %s", v)
		}
	}
	for _, args := range [][]string{{"-S", "/host/private-socket", "list-sessions"}, {"-Ldefault", "list-sessions"}, {"-f", "/dev/null", "-L", "default", "list-sessions"}} {
		out, err := s.exec("tmux", args...)
		if err == nil || !strings.Contains(out, "refusing socket override") {
			t.Fatalf("override not refused: %q %v", out, err)
		}
	}
	if _, err := s.exec("tmux", "new-session", "-d", "-s", "isolation-proof", "sleep 30"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.exec("tmux", "has-session", "-t", "isolation-proof"); err != nil {
		t.Fatal(err)
	}
	// -S after capture-pane is a scrollback option, not a socket override.
	if _, err := s.exec("tmux", "capture-pane", "-p", "-t", "isolation-proof", "-S", "-10"); err != nil {
		t.Fatal(err)
	}
	root := s.root
	s.cleanup()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("sandbox not cleaned up: %v", err)
	}
	s.root = ""
}

func assertionFixture(t *testing.T, output string, exitCode int) *suite {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "fake-agent-deck")
	script := "#!/bin/sh\nprintf '%s\\n' " + shQuote(output) + "\nexit " + fmt.Sprint(exitCode) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return &suite{root: root, project: root, bin: bin, ctx: context.Background(), env: []string{"HOME=" + root, "PATH=/usr/bin:/bin"}}
}

func TestRequireMissingNeedsExplicitNotFound(t *testing.T) {
	for _, tc := range []struct {
		name      string
		output    string
		exitCode  int
		wantError bool
	}{
		{"explicit missing", `{"success":false,"code":"NOT_FOUND","error":"session not found"}`, 2, false},
		{"storage failure", `{"success":false,"code":"NOT_FOUND","error":"database unavailable"}`, 1, true},
		{"malformed response", `not JSON`, 2, true},
		{"empty response", ``, 2, true},
		{"other error code", `{"success":false,"code":"INVALID_OPERATION"}`, 2, true},
		{"contradictory success", `{"success":true,"code":"NOT_FOUND"}`, 2, true},
		{"missing success field", `{"code":"NOT_FOUND"}`, 2, true},
		{"successful exit", `{"success":false,"code":"NOT_FOUND"}`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := assertionFixture(t, tc.output, tc.exitCode)
			err := s.requireMissing("fixture-id")
			if (err != nil) != tc.wantError {
				t.Fatalf("requireMissing error=%v; want error=%v", err, tc.wantError)
			}
		})
	}
	t.Run("canceled probe", func(t *testing.T) {
		s := assertionFixture(t, `{"success":false,"code":"NOT_FOUND"}`, 2)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s.ctx = ctx
		if err := s.requireMissing("fixture-id"); err == nil {
			t.Fatal("cancellation was accepted as absence")
		}
	})
}

func TestSessionListedNeedsValidArrayAndExactID(t *testing.T) {
	for _, tc := range []struct {
		name      string
		output    string
		wantFound bool
		wantError bool
	}{
		{"exact ID", `[{"id":"fixture-id"}]`, true, false},
		{"empty array", `[]`, false, false},
		{"legacy empty state", "No sessions found in profile 'default'.", false, false},
		{"unrelated empty prose", "No sessions found", false, true},
		{"different ID", `[{"id":"other-id"}]`, false, false},
		{"substring ID", `[{"id":"fixture-id-longer"}]`, false, false},
		{"title contains ID", `[{"id":"other-id","title":"fixture-id"}]`, false, false},
		{"null", `null`, false, true},
		{"empty output", ``, false, true},
		{"object", `{}`, false, true},
		{"missing ID", `[{"title":"fixture-id"}]`, false, true},
		{"null row", `[null]`, false, true},
		{"malformed JSON", `[{`, false, true},
		{"bad row after match", `[{"id":"fixture-id"},{}]`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := assertionFixture(t, tc.output, 0)
			found, err := s.sessionListed("fixture-id")
			if (err != nil) != tc.wantError || found != tc.wantFound {
				t.Fatalf("sessionListed=(%v,%v); want found=%v error=%v", found, err, tc.wantFound, tc.wantError)
			}
		})
	}
}

func TestTeardownRetainsSandboxWhenPrivateTmuxFails(t *testing.T) {
	s := assertionFixture(t, "unexpected private tmux failure", 1)
	s.realTmux = s.bin
	s.socket = "funccheck-failing-fixture"
	root := s.root
	if err := s.teardown(); err == nil || !strings.Contains(err.Error(), "private tmux teardown") {
		t.Fatalf("teardown must report shutdown failure, got %v", err)
	}
	if s.root != root {
		t.Fatalf("teardown discarded sandbox path: %q", s.root)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("sandbox must remain inspectable after failed shutdown: %v", err)
	}
}
