package procowner

import (
	"errors"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OpenCode 2.x starts one background service per host (`opencode serve
// --service`) from whichever `opencode` process runs first, and every other
// OpenCode TUI on the host attaches to it. When the first one ran inside an
// agent-deck pane, the service and its MCP children are recorded as that
// session's descendants. They are not the session's to count or to kill.

var openCodeServiceArgs = []string{"/opt/homebrew/Cellar/opencode/2.0.20/bin/opencode", "serve", "--service"}

func TestIsOpenCodeSharedService(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"homebrew binary", openCodeServiceArgs, true},
		{"bare name on PATH", []string{"opencode", "serve", "--service"}, true},
		{"flag after other flags", []string{"opencode", "serve", "--print-logs", "--service"}, true},
		{"node shim", []string{"node", "/usr/lib/node_modules/opencode-ai/bin/opencode", "serve", "--service"}, true},
		{"1.x per-TUI server", []string{"opencode", "serve", "--port", "4096"}, false},
		{"TUI", []string{"opencode", "-s", "ses_123"}, false},
		{"service flag on another subcommand", []string{"opencode", "run", "--service"}, false},
		{"another tool named serve", []string{"/usr/bin/python3", "serve", "--service"}, false},
		{"name only as a suffix", []string{"/usr/bin/notopencode", "serve", "--service"}, false},
		{"empty argv", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isOpenCodeSharedService(tc.args))
		})
	}
}

// openCodeTree builds the shape the issue reports: the pane leader (gone or
// alive), the OpenCode TUI, the shared service it started (reparented to pid 1
// once the TUI exited) and one MCP child of that service.
type openCodeTree struct {
	p                             *fakeProber
	leader, tui, service, mcp     ProcInfo
	leaderM, tuiM, serviceM, mcpM Member
}

func newOpenCodeTree(t *testing.T) openCodeTree {
	t.Helper()
	p := newFakeProber()
	tr := openCodeTree{p: p}
	tr.leader = p.add(100, 1, "5000", 1000)
	tr.tui = p.add(101, 100, "5100", 1000)
	p.setArgs(101, "opencode")
	tr.service = p.add(102, 1, "5200", 1000)
	p.setArgs(102, openCodeServiceArgs...)
	tr.mcp = p.add(103, 102, "5300", 1000)
	p.setArgs(103, "npm", "exec", "@playwright/mcp@latest")
	tr.leaderM = memberOf(tr.leader, RoleLeader)
	tr.tuiM = memberOf(tr.tui, RoleDescendant)
	tr.serviceM = memberOf(tr.service, RoleDescendant)
	tr.mcpM = memberOf(tr.mcp, RoleDescendant)
	return tr
}

func (tr openCodeTree) receipt() *Receipt {
	return liveReceipt("inst", tr.leaderM, tr.tuiM, tr.serviceM, tr.mcpM)
}

func statesByPID(report Report) map[int]MemberState {
	out := map[int]MemberState{}
	for _, s := range report.Members {
		out[s.Member.PID] = s.State
	}
	return out
}

func TestVerify_OpenCodeSharedServiceIsNotOwned(t *testing.T) {
	for _, tc := range []struct {
		name        string
		setup       func(tr openCodeTree)
		wantVerdict Verdict
		wantStates  map[int]MemberState
	}{
		{
			name: "pane and TUI gone, service reparented to pid 1",
			setup: func(tr openCodeTree) {
				tr.p.remove(100)
				tr.p.remove(101)
			},
			wantVerdict: VerdictClear,
			wantStates:  map[int]MemberState{100: StateGone, 101: StateGone, 102: StateShared, 103: StateShared},
		},
		{
			name: "service still a child of the live TUI",
			setup: func(tr openCodeTree) {
				tr.p.add(102, 101, "5200", 1000)
			},
			wantVerdict: VerdictOwned,
			wantStates:  map[int]MemberState{100: StateOwned, 101: StateOwned, 102: StateShared, 103: StateShared},
		},
		{
			name: "an ordinary escaped child still blocks",
			setup: func(tr openCodeTree) {
				tr.p.remove(100)
				tr.p.add(101, 1, "5100", 1000) // TUI survived its pane
			},
			wantVerdict: VerdictOwned,
			wantStates:  map[int]MemberState{100: StateGone, 101: StateOwned, 102: StateShared, 103: StateShared},
		},
		{
			name: "1.x server on a port stays owned",
			setup: func(tr openCodeTree) {
				tr.p.remove(100)
				tr.p.remove(101)
				tr.p.setArgs(102, "opencode", "serve", "--port", "4096")
			},
			wantVerdict: VerdictOwned,
			wantStates:  map[int]MemberState{100: StateGone, 101: StateGone, 102: StateOwned, 103: StateOwned},
		},
		{
			name: "command lines unreadable: unchanged, fail closed",
			setup: func(tr openCodeTree) {
				tr.p.remove(100)
				tr.p.remove(101)
				tr.p.cmdsErr = errors.New("ps failed")
			},
			wantVerdict: VerdictOwned,
			wantStates:  map[int]MemberState{100: StateGone, 101: StateGone, 102: StateOwned, 103: StateOwned},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newOpenCodeTree(t)
			tc.setup(tr)
			report := Verify(tr.p, tr.receipt())
			assert.Equal(t, tc.wantVerdict, report.Verdict, report.Describe())
			assert.Equal(t, tc.wantStates, statesByPID(report), report.Describe())
			for _, m := range report.Owned() {
				assert.NotEqual(t, StateShared, tc.wantStates[m.PID], "pid %d: a shared service is never owned", m.PID)
			}
		})
	}
}

// The leader is the session itself, whatever its command line says.
func TestVerify_LeaderIsNeverASharedService(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	p.setArgs(100, openCodeServiceArgs...)
	report := Verify(p, liveReceipt("inst", memberOf(leader, RoleLeader)))
	require.Equal(t, VerdictOwned, report.Verdict, report.Describe())
	assert.Equal(t, map[int]MemberState{100: StateOwned}, statesByPID(report))
}

// The ancestry walk stops at the leader: what runs ABOVE the session (the tmux
// server, whatever launched agent-deck) never turns the session's own
// processes into a shared service.
func TestVerify_ServiceAboveTheLeaderDoesNotExemptTheSession(t *testing.T) {
	p := newFakeProber()
	service := p.add(50, 1, "4000", 1000)
	p.setArgs(50, openCodeServiceArgs...)
	leader := p.add(100, 50, "5000", 1000)
	child := p.add(101, 100, "5100", 1000)
	report := Verify(p, liveReceipt("inst", memberOf(leader, RoleLeader), memberOf(child, RoleDescendant)))
	require.Equal(t, VerdictOwned, report.Verdict, report.Describe())
	assert.Equal(t, map[int]MemberState{100: StateOwned, 101: StateOwned}, statesByPID(report))
	_ = service
}

func TestReap_NeverSignalsTheOpenCodeSharedService(t *testing.T) {
	tr := newOpenCodeTree(t)
	tr.p.remove(100)
	tr.p.add(101, 1, "5100", 1000) // TUI escaped its pane: reconcile's real target
	sig := newRecordingSignaler()
	sig.onSend = func(pid int, _ syscall.Signal) { tr.p.remove(pid) }

	report := Reap(tr.p, sig, tr.receipt(), fastReapOptions())

	require.Equal(t, VerdictClear, report.Verdict, report.Describe())
	assert.Equal(t, 1, sig.sentTo(101), "the escaped TUI is reaped")
	assert.Zero(t, sig.sentTo(102), "the shared service is never signalled")
	assert.Zero(t, sig.sentTo(103), "the shared service's MCP children are never signalled")
	assert.Equal(t, 1, report.Signalled())
	outcomes := map[int]string{}
	for _, o := range report.Outcomes {
		outcomes[o.Member.PID] = o.Outcome
	}
	assert.Equal(t, OutcomeSharedService, outcomes[102])
	assert.Equal(t, OutcomeSharedService, outcomes[103])
	assert.Contains(t, report.Reason, "shared")
}

func TestReap_UnreadableCommandLinesKeepTheOldBehaviour(t *testing.T) {
	tr := newOpenCodeTree(t)
	tr.p.remove(100)
	tr.p.remove(101)
	tr.p.cmdsErr = errors.New("ps failed")
	sig := newRecordingSignaler()
	sig.onSend = func(pid int, _ syscall.Signal) { tr.p.remove(pid) }

	report := Reap(tr.p, sig, tr.receipt(), fastReapOptions())
	require.Equal(t, VerdictClear, report.Verdict, report.Describe())
	assert.Equal(t, 1, sig.sentTo(102))
	assert.Equal(t, 1, sig.sentTo(103))
}

func TestParsePSCommandTable(t *testing.T) {
	table := parsePSCommandTable(`
    1     0 /sbin/launchd
50713     1 /opt/homebrew/Cellar/opencode/2.0.20/bin/opencode serve --service
 8314 50713 npm exec chrome-devtools-mcp@1.9.0
garbage
 4242   100
`)
	require.Len(t, table, 4)
	assert.Equal(t, ProcCommand{PPID: 1, Args: openCodeServiceArgs}, table[50713])
	assert.Equal(t, 50713, table[8314].PPID)
	assert.Equal(t, []string{"/sbin/launchd"}, table[1].Args)
	assert.Empty(t, table[4242].Args, "a process with no readable argv is kept with its parent link")
}

func TestParseProcCmdline(t *testing.T) {
	assert.Equal(t, openCodeServiceArgs,
		parseProcCmdline([]byte("/opt/homebrew/Cellar/opencode/2.0.20/bin/opencode\x00serve\x00--service\x00")))
	assert.Equal(t, []string{"a b", "c"}, parseProcCmdline([]byte("a b\x00c")))
	assert.Empty(t, parseProcCmdline(nil))
}
