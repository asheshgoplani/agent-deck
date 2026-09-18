package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// usageIngestHelperEnv marks the re-exec'd test binary that runs the real
// `usage ingest claude` path (it calls os.Exit through the wrapped command).
const usageIngestHelperEnv = "AGENT_DECK_USAGE_INGEST_HELPER_PROCESS"

// usageIngestInheritedProfileEnv carries the AGENTDECK_PROFILE the helper
// should treat as inherited (absent: none), see TestUsageIngestHelperProcess.
const usageIngestInheritedProfileEnv = "AGENT_DECK_USAGE_INGEST_INHERITED_PROFILE"

func TestUsageIngestHelperProcess(t *testing.T) {
	if os.Getenv(usageIngestHelperEnv) != "1" {
		return
	}
	// Everything after the "--" that runUsageIngest puts before the argv.
	var args []string
	if i := slices.Index(os.Args, "--"); i >= 0 {
		args = os.Args[i+1:]
	}
	// TestMain forces AGENTDECK_PROFILE=_test on every process, helper or
	// not; put back what the parent test gave this process so the -p step
	// below sees the environment a real invocation inherits.
	if inherited, ok := os.LookupEnv(usageIngestInheritedProfileEnv); ok {
		os.Setenv("AGENTDECK_PROFILE", inherited)
	} else {
		os.Unsetenv("AGENTDECK_PROFILE")
	}
	// Mirrors main: the -p flag first, then the usage subcommand.
	profile, args := extractProfileFlag(args)
	applyProfileFlag(profile)
	if len(args) < 2 || args[0] != "usage" || args[1] != "ingest" {
		os.Exit(2)
	}
	handleUsageIngest(profile, args[2:])
	os.Exit(0)
}

const usageIngestPayload = `{"session_id":"abc","rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":1738425600}}}`

// runUsageIngest re-execs the test binary as `agent-deck <args>` with
// payload on stdin and env (HOME and XDG_* stripped first, then env
// appended). Returns stdout, stderr and the exit code.
func runUsageIngest(t *testing.T, env []string, payload string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestUsageIngestHelperProcess$", "--"}, args...)...)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "XDG_") || strings.HasPrefix(e, "AGENTDECK_PROFILE=") {
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	cmd.Env = append(cmd.Env, usageIngestHelperEnv+"=1", "AGENT_DECK_TASK6_HELPER_PROCESS=1")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = strings.NewReader(payload)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("helper process failed to run: %v: %s", err, errOut.String())
		}
		code = exit.ExitCode()
	}
	return out.String(), errOut.String(), code
}

func usageIngestHome(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	return []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
	}
}

// TestUsageIngest_StoreFailureStillForwards is review finding 1: the wrapper
// must run the wrapped command and forward its bytes and exit code whatever
// goes wrong on our side. A slot the quota cache cannot name and a missing
// HOME both used to exit 1 before the wrapped command ran, which Claude Code
// renders as a blank status line.
func TestUsageIngest_StoreFailureStillForwards(t *testing.T) {
	// The wrapped command echoes its stdin, so the forwarded payload is
	// visible, then fails with a status of its own.
	wrapped := []string{"--", "sh", "-c", "cat; echo TAIL; exit 3"}
	cases := []struct {
		name string
		env  []string
		args []string
		diag string
	}{
		{"unusable slot name", usageIngestHome(t), []string{"-p", "team.a"}, `unusable profile name "team.a"`},
		{"no HOME", nil, []string{"-p", "cA"}, "$HOME is not defined"},
		{"usable slot, cache written", usageIngestHome(t), []string{"-p", "cA"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := slices.Concat(c.args, []string{"usage", "ingest", "claude"}, wrapped)
			stdout, stderr, code := runUsageIngest(t, c.env, usageIngestPayload, args...)
			if stdout != usageIngestPayload+"TAIL\n" {
				t.Errorf("wrapped output not forwarded verbatim: %q\nstderr: %s", stdout, stderr)
			}
			if code != 3 {
				t.Errorf("exit code = %d, want the wrapped command's 3\nstderr: %s", code, stderr)
			}
			if c.diag != "" && (!strings.Contains(stderr, "agent-deck: ") || !strings.Contains(stderr, c.diag)) {
				t.Errorf("stderr must carry the diagnostic %q:\n%s", c.diag, stderr)
			}
			if c.diag == "" && strings.Contains(stderr, "agent-deck:") {
				t.Errorf("unexpected diagnostic:\n%s", stderr)
			}
		})
	}
	// Without a wrapped command the store failure is still no failure of
	// the command: nothing printed, exit 0.
	stdout, stderr, code := runUsageIngest(t, usageIngestHome(t), usageIngestPayload, "-p", "team.a", "usage", "ingest", "claude")
	if stdout != "" || code != 0 || !strings.Contains(stderr, `unusable profile name "team.a"`) {
		t.Errorf("plain ingester: stdout %q code %d stderr %q", stdout, code, stderr)
	}
}

// TestUsageIngest_WrappedCommandEnv is review finding 8: the -p that names
// the slot is this process's own; the wrapped command sees AGENTDECK_PROFILE
// exactly as it was inherited (unset, or the inherited value), so a status
// line script that itself calls agent-deck keeps resolving the profile it
// always did.
func TestUsageIngest_WrappedCommandEnv(t *testing.T) {
	wrapped := []string{"-p", "work", "usage", "ingest", "claude", "--", "sh", "-c", `echo "P=${AGENTDECK_PROFILE-unset}"`}
	stdout, stderr, code := runUsageIngest(t, usageIngestHome(t), usageIngestPayload, wrapped...)
	if stdout != "P=unset\n" || code != 0 {
		t.Errorf("inherited unset: stdout %q code %d stderr %q", stdout, code, stderr)
	}
	stdout, stderr, code = runUsageIngest(t, append(usageIngestHome(t), usageIngestInheritedProfileEnv+"=inherited"), usageIngestPayload, wrapped...)
	if stdout != "P=inherited\n" || code != 0 {
		t.Errorf("inherited value: stdout %q code %d stderr %q", stdout, code, stderr)
	}
}
