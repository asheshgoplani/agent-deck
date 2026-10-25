package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2897: `session switch-account <self> <account>` (and `session
// restart <self>`) run from a tool call inside the target session killed
// itself mid-switch: the restart tears down the session's tmux pane, which
// takes the calling agent-deck process with it, so the account commit never
// ran. When the command targets the session it runs in, it must hand the
// work to a detached child (new session via setsid) that outlives the pane,
// and print the receipt before the pane goes away.

func stubSelfHandoffCaller(t *testing.T, id, tmuxName string) {
	t.Helper()
	prevID, prevTmux := selfHandoffCallerID, selfHandoffTmuxName
	selfHandoffCallerID = func() string { return id }
	selfHandoffTmuxName = func() string { return tmuxName }
	t.Cleanup(func() { selfHandoffCallerID, selfHandoffTmuxName = prevID, prevTmux })
}

func TestCallerRunsInsideTarget(t *testing.T) {
	t.Setenv(selfHandoffParentEnv, "")
	inst := session.NewInstanceWithTool("self", t.TempDir(), "claude")
	other := session.NewInstanceWithTool("other", t.TempDir(), "claude")
	tmuxName := ""
	if ts := inst.GetTmuxSession(); ts != nil {
		tmuxName = ts.Name
	}
	if tmuxName == "" {
		t.Fatal("test instance has no tmux session name")
	}
	cases := []struct {
		name     string
		callerID string
		tmux     string
		target   *session.Instance
		want     bool
	}{
		{"AGENTDECK_INSTANCE_ID names the target", inst.ID, "", inst, true},
		{"AGENTDECK_INSTANCE_ID names another session", other.ID, "", inst, false},
		{"calling tmux session is the target's", "", tmuxName, inst, true},
		{"stale id but the calling pane is the target's", other.ID, tmuxName, inst, true},
		{"outside any session", "", "", inst, false},
		{"no target", inst.ID, "", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubSelfHandoffCaller(t, tc.callerID, tc.tmux)
			if got := callerRunsInsideTarget(tc.target); got != tc.want {
				t.Fatalf("callerRunsInsideTarget = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSelfHandoffCommand_DetachedChildGetsSetsidProfileAndLog(t *testing.T) {
	log, err := os.Create(filepath.Join(t.TempDir(), "handoff.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	sub := []string{"session", "switch-account", "abc123", "work", "--json"}
	cmd := selfHandoffCommand("/bin/agent-deck", "work-profile", sub, []string{"PATH=/bin"}, 4242, log)

	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatalf("detached child must start in its own session (Setsid), got %+v", cmd.SysProcAttr)
	}
	wantArgs := append([]string{"/bin/agent-deck", "-p", "work-profile"}, sub...)
	if strings.Join(cmd.Args, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("argv = %q, want %q (the same command, same profile)", cmd.Args, wantArgs)
	}
	if cmd.Stdin != nil {
		t.Errorf("stdin must not be the dying pane's terminal")
	}
	if cmd.Stdout != log || cmd.Stderr != log {
		t.Errorf("stdout/stderr must go to the handoff log, not the dying pane")
	}
	if !envHas(cmd.Env, selfHandoffParentEnv+"=4242") {
		t.Errorf("child env lacks %s=4242: %q", selfHandoffParentEnv, cmd.Env)
	}
}

func TestSelfHandoffEnviron_DropsInstanceIDAndMarksParent(t *testing.T) {
	in := []string{"PATH=/bin", "AGENTDECK_INSTANCE_ID=abc", "AGENTDECK_PROFILE=work", selfHandoffParentEnv + "=1", "CLAUDE_JOB_DIR=/x"}
	got := selfHandoffEnviron(in, 77)
	for _, kv := range got {
		if strings.HasPrefix(kv, "AGENTDECK_INSTANCE_ID=") {
			t.Errorf("child still claims to run inside the session: %q", kv)
		}
		if strings.HasPrefix(kv, "CLAUDE_JOB_DIR=") {
			t.Errorf("child inherits the Claude background job marker: %q", kv)
		}
	}
	if !envHas(got, "AGENTDECK_PROFILE=work") || !envHas(got, "PATH=/bin") {
		t.Errorf("child must keep the caller's profile and PATH: %q", got)
	}
	if n := envCount(got, selfHandoffParentEnv+"="); n != 1 || !envHas(got, selfHandoffParentEnv+"=77") {
		t.Errorf("want exactly one %s=77, got %q", selfHandoffParentEnv, got)
	}
}

func TestHandOffIfSelfTarget_OnlyForTheCallingSession(t *testing.T) {
	t.Setenv(selfHandoffParentEnv, "")
	inst := session.NewInstanceWithTool("self", t.TempDir(), "claude")
	var gotProfile string
	var gotSub []string
	calls := 0
	prev := selfHandoffStart
	selfHandoffStart = func(profile string, sub []string) (selfHandoff, error) {
		calls++
		gotProfile, gotSub = profile, sub
		return selfHandoff{PID: 999, Log: "/tmp/x.log"}, nil
	}
	t.Cleanup(func() { selfHandoffStart = prev })
	out := NewCLIOutput(false, true)
	args := []string{inst.ID, "work"}

	stubSelfHandoffCaller(t, "someone-else", "")
	if handOffIfSelfTarget(out, "p", "switch-account", args, inst) || calls != 0 {
		t.Fatalf("another session's call must run in place (calls=%d)", calls)
	}

	stubSelfHandoffCaller(t, inst.ID, "")
	if !handOffIfSelfTarget(out, "p", "switch-account", args, inst) {
		t.Fatal("a call from inside the target must hand off")
	}
	want := []string{"session", "switch-account", inst.ID, "work"}
	if calls != 1 || gotProfile != "p" || strings.Join(gotSub, " ") != strings.Join(want, " ") {
		t.Fatalf("handoff start = %d calls, profile %q, sub %q; want 1, \"p\", %q", calls, gotProfile, gotSub, want)
	}
}

func TestHandOffIfSelfTarget_DetachedChildRunsInPlace(t *testing.T) {
	inst := session.NewInstanceWithTool("self", t.TempDir(), "claude")
	stubSelfHandoffCaller(t, inst.ID, "")
	prev := selfHandoffStart
	selfHandoffStart = func(string, []string) (selfHandoff, error) {
		t.Fatal("the detached child must not hand off again")
		return selfHandoff{}, nil
	}
	t.Cleanup(func() { selfHandoffStart = prev })
	// A parent pid that is not ours: the wait for it returns at once.
	t.Setenv(selfHandoffParentEnv, fmt.Sprint(os.Getppid()+100000))
	if handOffIfSelfTarget(NewCLIOutput(false, true), "", "restart", []string{inst.ID}, inst) {
		t.Fatal("the detached child must run the command itself")
	}
	if v, ok := os.LookupEnv(selfHandoffParentEnv); ok && v != "" {
		t.Errorf("%s must be cleared so nothing the child starts inherits it, got %q", selfHandoffParentEnv, v)
	}
}

// The handler-level ordering: a self-targeted switch-account hands off BEFORE
// session.SwitchAccount (whose first destructive step stops the pane) runs in
// the calling process; another session's call still switches in place.
func TestSessionSwitchAccount_SelfTargetHandsOffBeforeTheSwitch(t *testing.T) {
	if os.Getenv("AGENT_DECK_SELF_HANDOFF_HELPER") != "" {
		selfHandoffStart = func(profile string, sub []string) (selfHandoff, error) {
			fmt.Printf("HANDOFF %s\n", strings.Join(sub, " "))
			return selfHandoff{PID: 4242, Log: "/tmp/handoff.log"}, nil
		}
		selfHandoffTmuxName = func() string { return "" }
		// TestMain unsets AGENTDECK_INSTANCE_ID, so the caller comes in on
		// its own variable.
		caller := os.Getenv("AGENT_DECK_SELF_HANDOFF_CALLER")
		selfHandoffCallerID = func() string { return caller }
		switchAccountExecute = func(*session.UserConfig, *session.Instance, string, session.AccountSwitchOptions) (*session.AccountSwitchResult, error) {
			fmt.Println("SWITCH-IN-PLACE")
			return nil, fmt.Errorf("stub")
		}
		for i, arg := range os.Args {
			if arg == "--" {
				rest := os.Args[i+1:]
				switch os.Getenv("AGENT_DECK_SELF_HANDOFF_HELPER") {
				case "switch-account":
					handleSessionSwitchAccount("ch_support_test", rest)
				case "restart":
					handleSessionRestart("ch_support_test", rest)
				case "core-restart":
					cliSessionRestart("ch_support_test", rest)
				case "switch":
					handleSessionSwitch("ch_support_test", rest)
				}
				os.Exit(0)
			}
		}
		os.Exit(3)
	}

	home := intendedSwitchHome(t)
	id := addTestSession(t, home, filepath.Join(home, "proj"), "self")
	run := func(t *testing.T, helper, callerID string, args ...string) string {
		t.Helper()
		argv := append([]string{"-test.run=^TestSessionSwitchAccount_SelfTargetHandsOffBeforeTheSwitch$", "--"}, args...)
		cmd := exec.Command(os.Args[0], argv...)
		cmd.Env = agentDeckTestEnv(home, []string{
			"AGENT_DECK_SELF_HANDOFF_HELPER=" + helper, "AGENT_DECK_TASK6_HELPER_PROCESS=1",
			"AGENT_DECK_SELF_HANDOFF_CALLER=" + callerID, "AGENTDECK_SKIP_UPDATE_CHECK=1", "AGENTDECK_EVENTS_BUS=off",
		})
		out, _ := cmd.CombinedOutput()
		return string(out)
	}

	t.Run("switch-account from inside the target", func(t *testing.T) {
		out := run(t, "switch-account", id, id, "work")
		if !strings.Contains(out, "HANDOFF session switch-account "+id+" work") {
			t.Fatalf("no handoff to a detached child:\n%s", out)
		}
		if strings.Contains(out, "SWITCH-IN-PLACE") {
			t.Fatalf("the switch ran in the process its restart kills:\n%s", out)
		}
		if !strings.Contains(out, "4242") || !strings.Contains(out, "/tmp/handoff.log") {
			t.Errorf("receipt must name the detached pid and its log:\n%s", out)
		}
	})
	t.Run("switch-account from another session", func(t *testing.T) {
		out := run(t, "switch-account", "some-other-session", id, "work")
		if strings.Contains(out, "HANDOFF") || !strings.Contains(out, "SWITCH-IN-PLACE") {
			t.Fatalf("a call from outside the target must switch in place:\n%s", out)
		}
	})
	// session switch restarts the pane on a native switch the same way
	// (#2897, reported for switch too in #2919); --no-start keeps the
	// unfixed path from starting anything here.
	t.Run("switch from inside the target", func(t *testing.T) {
		out := run(t, "switch", id, id, "--to-harness", "claude", "--no-start")
		if !strings.Contains(out, "HANDOFF session switch "+id+" --to-harness claude --no-start") {
			t.Fatalf("no handoff to a detached child:\n%s", out)
		}
	})
	// --if-idle keeps the unfixed path harmless here: the stopped session is
	// skipped instead of really restarted.
	for _, helper := range []string{"restart", "core-restart"} {
		t.Run(helper+" from inside the target", func(t *testing.T) {
			out := run(t, helper, id, id, "--if-idle")
			if !strings.Contains(out, "HANDOFF session restart "+id+" --if-idle") {
				t.Fatalf("no handoff to a detached child:\n%s", out)
			}
		})
	}
}

func envHas(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func envCount(env []string, prefix string) int {
	n := 0
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}
