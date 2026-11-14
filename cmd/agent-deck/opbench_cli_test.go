//go:build opbench

// Built only with -tags opbench (bench/ops/run.sh, make bench-ops): the
// operation benchmarks start real processes and tmux servers and must stay
// out of plain `go test -bench .` runs such as CI's advisory bench job.

package main

// Operation benchmarks for the agent-deck CLI (ASK-106).
//
// Every benchmark here execs the built binary in a sandbox HOME, so the file
// only depends on the CLI surface and on internal/statedb row writing. That is
// what lets bench/ops/run.sh copy it unchanged into a worktree of an older
// release and compare numbers across refs. Names: BenchmarkOp_<Name>, sizes
// as sessions=N sub-benchmarks; the catalogue in bench/ops/catalogue.json maps
// each name to its operation and target.
//
// Run: deck-test -run '^$' -bench '^BenchmarkOp_' -benchmem -count=5 ./cmd/agent-deck/

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

var (
	opbBinOnce sync.Once
	opbBinPath string
	opbBinErr  error
	opbSockSeq atomic.Int64
)

// opbBinary builds ./cmd/agent-deck once per test binary. The build directory
// lives under the (container) temp dir for the life of the process.
func opbBinary(b testing.TB) string {
	b.Helper()
	opbBinOnce.Do(func() {
		root, err := opbRepoRoot()
		if err != nil {
			opbBinErr = err
			return
		}
		dir, err := os.MkdirTemp("", "opbench-bin-")
		if err != nil {
			opbBinErr = err
			return
		}
		out := filepath.Join(dir, "agent-deck")
		cmd := exec.Command("go", "build", "-o", out, "./cmd/agent-deck")
		cmd.Dir = root
		if data, err := cmd.CombinedOutput(); err != nil {
			opbBinErr = fmt.Errorf("go build: %v\n%s", err, data)
			return
		}
		opbBinPath = out
	})
	if opbBinErr != nil {
		b.Fatal(opbBinErr)
	}
	return opbBinPath
}

func opbRepoRoot() (string, error) {
	d, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("no go.mod above the test directory")
		}
		d = parent
	}
}

// opbSandbox is one benchmark's scratch HOME with its own tmux socket name.
type opbSandbox struct {
	bin    string
	home   string
	shim   string // prepended to PATH when non-empty
	socket string
	env    []string
}

// opbNewSandbox makes a HOME with XDG dirs, a private TMUX_TMPDIR and a
// config.toml that pins tmux to a socket name unique to this benchmark.
// extraConfig is appended to config.toml.
func opbNewSandbox(b testing.TB, shim bool, extraConfig string) *opbSandbox {
	b.Helper()
	home := b.TempDir()
	sb := &opbSandbox{
		bin:    opbBinary(b),
		home:   home,
		socket: fmt.Sprintf("opb%d-%d", os.Getpid(), opbSockSeq.Add(1)),
	}
	for _, d := range []string{".config/agent-deck", ".local/state", ".local/share", ".cache", "t", "work"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			b.Fatal(err)
		}
	}
	config := fmt.Sprintf("[tmux]\nsocket_name = %q\n\n[feedback]\ndisabled = true\n\n[telemetry]\ndisabled = true\n%s", sb.socket, extraConfig)
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-deck", "config.toml"), []byte(config), 0o600); err != nil {
		b.Fatal(err)
	}
	path := os.Getenv("PATH")
	if shim {
		sb.shim = filepath.Join(home, "shim")
		if err := os.MkdirAll(sb.shim, 0o700); err != nil {
			b.Fatal(err)
		}
		path = sb.shim + string(os.PathListSeparator) + path
	}
	sb.env = []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"TMUX_TMPDIR=" + filepath.Join(home, "t"),
		"PATH=" + path,
		"TERM=dumb",
		"AGENTDECK_COLOR=none",
		"NO_COLOR=1",
		"AGENTDECK_PROFILE=default",
		"AGENTDECK_SKIP_UPDATE_CHECK=1",
		"AGENTDECK_SUPPRESS_TMUX_WARNING=1",
		"AGENTDECK_TELEMETRY=0",
		"DO_NOT_TRACK=1",
	}
	for _, k := range []string{"LANG", "LC_ALL", "SHELL", "USER", "LOGNAME", "TMPDIR"} {
		if v := os.Getenv(k); v != "" {
			sb.env = append(sb.env, k+"="+v)
		}
	}
	b.Cleanup(sb.killTmux)
	return sb
}

// killTmux stops only this sandbox's private tmux server (never the default).
func (sb *opbSandbox) killTmux() {
	if _, err := exec.LookPath("tmux"); err != nil || sb.shim != "" {
		return
	}
	cmd := exec.Command("tmux", "-L", sb.socket, "kill-server")
	cmd.Env = sb.env
	_ = cmd.Run()
}

func (sb *opbSandbox) run(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, sb.bin, args...)
	cmd.Env = sb.env
	cmd.Dir = filepath.Join(sb.home, "work")
	return cmd.CombinedOutput()
}

func (sb *opbSandbox) mustRun(b *testing.B, args ...string) []byte {
	b.Helper()
	out, err := sb.run(args...)
	if err != nil {
		b.Fatalf("agent-deck %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// skipIfUnsupported runs args once and skips the benchmark when this build of
// agent-deck does not know the command or flag (older refs).
func (sb *opbSandbox) skipIfUnsupported(b *testing.B, args ...string) {
	b.Helper()
	out, err := sb.run(args...)
	if err == nil {
		return
	}
	s := strings.ToLower(string(out))
	if strings.Contains(s, "unknown") || strings.Contains(s, "flag provided but not defined") || strings.Contains(s, "usage:") {
		b.Skipf("not supported by this build: agent-deck %s: %s", strings.Join(args, " "), opbFirstLine(s))
	}
	b.Fatalf("agent-deck %s: %v\n%s", strings.Join(args, " "), err, out)
}

func opbFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// opbSeedRows writes n session rows straight into the default profile's
// state.db (no agents, no tmux). Tools rotate shell/claude/codex over 10
// groups; tmux names follow the agentdeck_<id> convention.
func opbSeedRows(b testing.TB, home, socket string, n int) []string {
	b.Helper()
	dir := filepath.Join(home, ".agent-deck", "profiles", "default")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		b.Fatal(err)
	}
	db, err := statedb.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		b.Fatal(err)
	}
	tools := []string{"shell", "claude", "codex"}
	statuses := []string{"idle", "waiting", "running", "error"}
	rows := make([]*statedb.InstanceRow, n)
	names := make([]string, n)
	for i := range rows {
		id := fmt.Sprintf("opb-%04d", i)
		tool := tools[i%len(tools)]
		names[i] = "agentdeck_" + id
		rows[i] = &statedb.InstanceRow{
			ID: id, Title: id, ProjectPath: home, GroupPath: fmt.Sprintf("work/team-%d", i%10),
			Order: i, Tool: tool, Command: map[string]string{"shell": "", "claude": "claude", "codex": "codex"}[tool],
			Status: statuses[i%len(statuses)], TmuxSession: names[i], TmuxSocketName: socket,
			CreatedAt: time.Now().Add(-time.Hour), ToolData: json.RawMessage(`{}`),
		}
	}
	if err := db.SaveInstances(rows); err != nil {
		b.Fatal(err)
	}
	return names
}

// opbListShim installs a fake tmux that answers the list-time queries from
// files, as in list_perf_test.go: the CLI's own work is measured, with no
// tmux server and no real panes. Like real tmux it answers a command
// sequence: the batched pane capture of a status pass (tmux.PrimePaneCaptures,
// `capture-pane -t A -p -e ; display-message -p MARK-0 ; ...`) gets every
// frame followed by its marker line. A stand-in that answered only the first
// command made every session fall back to its own capture-pane process, so
// the benchmark timed one fork per session that real tmux never needs.
func opbListShim(b testing.TB, sb *opbSandbox, names []string) {
	b.Helper()
	var ns, ws, ps strings.Builder
	for i, name := range names {
		tool := []string{"bash", "claude", "codex"}[i%3]
		fmt.Fprintln(&ns, name)
		fmt.Fprintf(&ws, "%s|1|0|%s\n", name, tool)
		fmt.Fprintf(&ps, "%s|%s|0|0|0|ready\n", name, tool)
	}
	for f, data := range map[string]string{"names": ns.String(), "windows": ws.String(), "panes": ps.String()} {
		if err := os.WriteFile(filepath.Join(sb.home, f), []byte(data), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	// Panes whose name ends in 0 or 5 show a busy agent, the rest a shell
	// prompt, so the listing depends on which frame reaches which session.
	shim := `#!/bin/sh
frame() { case "$1" in *[05]|*[05]:*) printf '⠋ Working (esc to interrupt)\n';; *) printf '$ \n';; esac; }
case "$*" in *' ; display-message -p '*)
 while [ "$#" -gt 0 ]; do
  case "$1" in
   capture-pane) frame "$3";;
   display-message) printf '%s\n' "$3"; shift 2;;
  esac
  shift
 done
 exit 0;;
esac
while [ "$#" -gt 0 ]; do
 case "$1" in
  -V) echo "tmux 3.4"; exit 0;;
  list-windows) cat "$HOME/windows"; exit 0;;
  list-sessions) cat "$HOME/names"; exit 0;;
  list-panes) case "$*" in *'pane_pid'*) printf '0\n';; *'-a'*) cat "$HOME/panes";; *) printf '0\n';; esac; exit 0;;
  show-environment) exit 1;;
  capture-pane) frame "$3"; exit 0;;
  has-session) exit 0;;
  display-message) case "$*" in *window_activity*) printf '1\n';; *) printf '0\n';; esac; exit 0;;
 esac
 shift
done
exit 0
`
	if err := os.WriteFile(filepath.Join(sb.shim, "tmux"), []byte(shim), 0o700); err != nil {
		b.Fatal(err)
	}
}

func opbNeedTmux(b *testing.B) {
	b.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		b.Skip("tmux not installed")
	}
}

// BenchmarkOp_CLIVersion: process start to exit of `agent-deck --version`
// (package init, pre-dispatch probes).
func BenchmarkOp_CLIVersion(b *testing.B) {
	sb := opbNewSandbox(b, true, "")
	opbListShim(b, sb, nil)
	sb.mustRun(b, "--version")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sb.mustRun(b, "--version")
	}
}

// BenchmarkOp_CLIList: `agent-deck list --json` over N stored sessions.
func BenchmarkOp_CLIList(b *testing.B) {
	for _, n := range []int{100, 500} {
		b.Run(fmt.Sprintf("sessions=%d", n), func(b *testing.B) {
			sb := opbNewSandbox(b, true, "")
			names := opbSeedRows(b, sb.home, sb.socket, n)
			opbListShim(b, sb, names)
			out := sb.mustRun(b, "list", "--json")
			var rows []json.RawMessage
			if err := json.Unmarshal(opbJSONPart(out), &rows); err != nil || len(rows) != n {
				b.Fatalf("list --json gave %d rows (err %v), want %d\n%.400s", len(rows), err, n, out)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sb.mustRun(b, "list", "--json")
			}
		})
	}
}

// opbJSONPart drops any warning lines printed before the JSON document.
func opbJSONPart(out []byte) []byte {
	s := string(out)
	if i := strings.IndexAny(s, "[{"); i > 0 {
		s = s[i:]
	}
	return []byte(s)
}

// BenchmarkOp_CLIAdd: `agent-deck add` of a shell session (record only, not
// started) into a profile that already holds 100 sessions.
func BenchmarkOp_CLIAdd(b *testing.B) {
	sb := opbNewSandbox(b, true, "")
	names := opbSeedRows(b, sb.home, sb.socket, 100)
	opbListShim(b, sb, names)
	work := filepath.Join(sb.home, "work")
	sb.mustRun(b, "add", work, "-t", "opb-add-warm", "-c", "bash", "-g", "bench", "-q")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sb.mustRun(b, "add", work, "-t", fmt.Sprintf("opb-add-%d-%d", b.N, i), "-c", "bash", "-g", "bench", "-q")
	}
}

// BenchmarkOp_CLIEventsPublish: one `agent-deck events publish` call. Known
// slow (~1 s): Flush waits for the bus writer's 1 s sync tick.
func BenchmarkOp_CLIEventsPublish(b *testing.B) {
	sb := opbNewSandbox(b, true, "\n[macapp]\nplugins = true\n")
	opbListShim(b, sb, nil)
	args := []string{"events", "publish", "--kind", "macapp.probe", "--session", "opb-events", "--data", `{"probe":true}`}
	sb.skipIfUnsupported(b, args...)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sb.mustRun(b, args...)
	}
}

// opbLifecycleSandbox adds one stopped shell session on a private tmux socket.
func opbLifecycleSandbox(b *testing.B) (*opbSandbox, string) {
	b.Helper()
	opbNeedTmux(b)
	sb := opbNewSandbox(b, false, "")
	title := "opb-life"
	sb.mustRun(b, "add", filepath.Join(sb.home, "work"), "-t", title, "-c", "bash", "-g", "bench", "-q")
	return sb, title
}

// BenchmarkOp_CLISessionStart: `agent-deck session start` of a shell session
// on a private tmux socket (tmux session created, spawn verified). The stop
// between iterations is not timed.
func BenchmarkOp_CLISessionStart(b *testing.B) {
	sb, title := opbLifecycleSandbox(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sb.mustRun(b, "session", "start", title, "-q")
		b.StopTimer()
		sb.mustRun(b, "session", "stop", title, "-q")
		b.StartTimer()
	}
}

// BenchmarkOp_CLISessionSend: `agent-deck session send --no-wait` of a short
// command to a running shell session.
func BenchmarkOp_CLISessionSend(b *testing.B) {
	sb, title := opbLifecycleSandbox(b)
	sb.mustRun(b, "session", "start", title, "-q")
	sb.skipIfUnsupported(b, "session", "send", title, "true", "--no-wait", "-q")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sb.mustRun(b, "session", "send", title, "true", "--no-wait", "-q")
	}
	b.StopTimer()
	_, _ = sb.run("session", "stop", title, "-q")
}

// BenchmarkOp_CLISessionStop: `agent-deck session stop` of a running shell
// session. The start between iterations is not timed.
func BenchmarkOp_CLISessionStop(b *testing.B) {
	sb, title := opbLifecycleSandbox(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		sb.mustRun(b, "session", "start", title, "-q")
		b.StartTimer()
		sb.mustRun(b, "session", "stop", title, "-q")
	}
}
