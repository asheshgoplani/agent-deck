// Command bench measures an isolated, persisted synthetic fleet.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/creack/pty"
)

type metric struct {
	Size    int       `json:"size"`
	Name    string    `json:"name"`
	Unit    string    `json:"unit"`
	Samples []float64 `json:"samples"`
	P50     float64   `json:"p50"`
	P95     float64   `json:"p95"`
}
type report struct {
	SchemaVersion int               `json:"schema_version"`
	Platform      string            `json:"platform"`
	Machine       string            `json:"machine"`
	Revision      string            `json:"revision"`
	Seed          int64             `json:"seed"`
	Runs          int               `json:"runs"`
	Metrics       []metric          `json:"metrics"`
	Unavailable   map[string]string `json:"unavailable,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) > 1 && os.Args[1] == "compare" {
		return compare(os.Args[2:])
	}
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	binary := fs.String("binary", "", "prebuilt agent-deck executable (required)")
	harness := fs.String("ui-harness", "", "prebuilt internal/ui test executable (required)")
	out := fs.String("out", "bench/run.json", "JSON output; markdown uses same stem")
	sizes := fs.String("sizes", "10,100,500", "comma-separated fleet sizes")
	runs := fs.Int("runs", 3, "samples per metric")
	seed := fs.Int64("seed", 2286, "deterministic fixture seed")
	machine := fs.String("machine", "unspecified", "machine class")
	revision := fs.String("revision", "unknown", "source revision")
	worker := fs.Bool("worker", false, "internal isolated worker")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *runs < 1 || *binary == "" || *harness == "" {
		return fmt.Errorf("binary, ui-harness and positive runs are required")
	}
	if *worker {
		return work(*binary, *harness, *out, *sizes, *runs, *seed)
	}
	bin, err := filepath.Abs(*binary)
	if err != nil {
		return err
	}
	ui, err := filepath.Abs(*harness)
	if err != nil {
		return err
	}
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	rep := report{SchemaVersion: 1, Platform: runtime.GOOS + "-" + runtime.GOARCH, Machine: *machine, Revision: *revision, Seed: *seed, Runs: *runs}
	for _, size := range strings.Split(*sizes, ",") {
		n, e := strconv.Atoi(size)
		if e != nil || n < 1 {
			return fmt.Errorf("invalid fleet size %q", size)
		}
		ms, e := isolated(ctx, self, realTmux, bin, ui, n, *runs, *seed)
		if e != nil {
			return e
		}
		rep.Metrics = append(rep.Metrics, ms...)
	}
	rep.Unavailable = map[string]string{"rss_bytes": "shared runtime-health sampler is not available in the base revision", "open_fds": "shared runtime-health sampler is not available in the base revision"}
	for _, m := range rep.Metrics {
		delete(rep.Unavailable, m.Name)
	}
	if err := writeJSON(*out, rep); err != nil {
		return err
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# Fleet benchmark\n\nPlatform: %s. Machine: %s. Revision: %s. Seed: %d. Runs: %d.\n\n| Sessions | Metric | Unit | p50 | p95 |\n|---:|---|---|---:|---:|\n", rep.Platform, rep.Machine, rep.Revision, rep.Seed, rep.Runs)
	for _, m := range rep.Metrics {
		fmt.Fprintf(&md, "| %d | %s | %s | %.3f | %.3f |\n", m.Size, m.Name, m.Unit, m.P50, m.P95)
	}
	fmt.Print(md.String())
	return os.WriteFile(strings.TrimSuffix(*out, filepath.Ext(*out))+".md", []byte(md.String()), 0600)
}
func isolated(ctx context.Context, self, realTmux, binary, harness string, size, runs int, seed int64) (result []metric, err error) {
	root, err := os.MkdirTemp("", "adeck-bench-")
	if err != nil {
		return nil, err
	}
	// Scratch directories are retained for inspectability. No recursive deletion is needed.
	home := filepath.Join(root, "home")
	sockdir := filepath.Join(root, "sockets")
	bindir := filepath.Join(root, "bin")
	for _, p := range []string{home, sockdir, bindir} {
		if err = os.MkdirAll(p, 0700); err != nil {
			return nil, err
		}
	}
	socket := "fleet"
	log := filepath.Join(root, "tmux.log")
	env := []string{"HOME=" + home, "PATH=" + bindir + ":" + os.Getenv("PATH"), "TERM=xterm-256color", "LANG=C.UTF-8", "DO_NOT_TRACK=1", "AGENTDECK_SKIP_UPDATE_CHECK=1", "TMPDIR=" + root, "TMUX_TMPDIR=" + sockdir, "SHELL=/bin/sh", "AGENTDECK_PROFILE=default", "AGENT_DECK_TEST_HOME_ISOLATED=1", "AGENTDECK_BENCH_HOME=" + home, "AGENTDECK_BENCH_TMUX_TMPDIR=" + sockdir, "AGENTDECK_BENCH_SOCKET=" + socket, "AGENTDECK_BENCH_PROFILE=default", "AGENTDECK_BENCH_TMUX_LOG=" + log, "BENCH_REAL_TMUX=" + realTmux, "BENCH_SOCKET=" + socket, "BENCH_ROOT=" + root}
	// All executable callers are forced onto the same private socket. Reject explicit paths.
	wrapper := `#!/bin/sh
printf 'tmux\n' >> "$AGENTDECK_BENCH_TMUX_LOG"
expect_socket=0
for arg do
 if [ "$expect_socket" = 1 ]; then
  [ "$arg" = "$BENCH_SOCKET" ] || exit 97
  expect_socket=0
  continue
 fi
 case "$arg" in
 -L) expect_socket=1 ;;
 -L*|-S*|-f*) exit 97 ;;
 -u|-2|-C|-V) ;;
 -*) exit 97 ;;
 *) break ;;
 esac
done
[ "$expect_socket" = 0 ] || exit 97
exec "$BENCH_REAL_TMUX" -L "$BENCH_SOCKET" "$@"
`
	if err = os.WriteFile(filepath.Join(bindir, "tmux"), []byte(wrapper), 0700); err != nil {
		return nil, err
	}
	ssh := `#!/bin/sh
printf 'ssh\n' >> "$BENCH_ROOT/ssh.log"
case "$*" in
 *bench-auth*) printf 'Permission denied (publickey).\n' >&2; exit 255 ;;
 *bench-slow*) sleep 6 ;;
esac
printf '[{"id":"remote-fixture","title":"synthetic remote","tool":"shell","status":"idle"}]\n'
`
	if err = os.WriteFile(filepath.Join(bindir, "ssh"), []byte(ssh), 0700); err != nil {
		return nil, err
	}
	// Registered before first server creation; cleanup uses the exact original binary,
	// -L name and TMUX_TMPDIR even after cancellation.
	defer func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		c := exec.CommandContext(cleanupCtx, realTmux, "-L", socket, "kill-server")
		c.Env = env
		_ = c.Run()
		if cleanupCtx.Err() != nil {
			err = fmt.Errorf("private tmux teardown timed out at %s: %w", root, cleanupCtx.Err())
			return
		}
		probeCtx, cancelProbe := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelProbe()
		p := exec.CommandContext(probeCtx, realTmux, "-L", socket, "list-sessions")
		p.Env = env
		if p.Run() == nil || probeCtx.Err() != nil {
			err = fmt.Errorf("private tmux teardown could not be verified: %s", root)
			return
		}
		fmt.Fprintf(os.Stderr, "fixture %d retained at %s; private server stopped\n", size, root)
	}()
	output := filepath.Join(root, "metrics.json")
	c := exec.CommandContext(ctx, self, "-worker", "-binary", binary, "-ui-harness", harness, "-out", output, "-sizes", strconv.Itoa(size), "-runs", strconv.Itoa(runs), "-seed", strconv.FormatInt(seed, 10))
	bindChildLifetime(c)
	c.Env = env
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	if err = c.Run(); err != nil {
		return nil, fmt.Errorf("fleet %d: %w", size, err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(data, &result)
	return
}
func work(binary, harness, out, sizes string, runs int, seed int64) error {
	size, err := strconv.Atoi(sizes)
	if err != nil {
		return err
	}
	home := os.Getenv("HOME")
	root := os.Getenv("BENCH_ROOT")
	socket := os.Getenv("BENCH_SOCKET")
	if root == "" || home != filepath.Join(root, "home") || os.Getenv("TMUX_TMPDIR") != filepath.Join(root, "sockets") || socket != "fleet" {
		return fmt.Errorf("refusing unisolated worker")
	}
	config := fmt.Sprintf(`[tmux]
socket_name = %q
[feedback]
disabled = true
[remotes.fast]
host = "bench-fast"
[remotes.slow]
host = "bench-slow"
[remotes.auth]
host = "bench-auth"
`, socket)
	configDir, err := agentpaths.LegacyDir()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(configDir, 0700); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0600); err != nil {
		return err
	}
	storage, err := session.NewStorageWithProfile("default")
	if err != nil {
		return err
	}
	defer storage.Close()
	// Real fixture executables and configured commands keep runtime detection
	// from silently converting the synthetic Claude/Codex rows into shells.
	for tool, banner := range map[string]string{"claude": "Claude Code\n❯ \n", "codex": "OpenAI Codex\n› synthetic prompt\n"} {
		body := "#!/bin/sh\nprintf '" + banner + "'\nexec cat\n"
		if err = os.WriteFile(filepath.Join(root, "bin", tool), []byte(body), 0700); err != nil {
			return err
		}
	}
	rng := rand.New(rand.NewSource(seed))
	instances := make([]*session.Instance, 0, size)
	var shellID, shellName string
	for i := 0; i < size; i++ {
		tool := []string{"shell", "claude", "codex"}[i%3]
		inst := session.NewInstanceWithGroupAndTool(fmt.Sprintf("bench-%04d", i), root, fmt.Sprintf("work/team-%d/project-%d", i%3, rng.Intn(10)), tool)
		inst.ID = fmt.Sprintf("bench-%d-%04d", seed, i)
		inst.CreatedAt = time.Unix(1700000000+int64(i), 0)
		if tool != "shell" {
			inst.Command = filepath.Join(root, "bin", tool)
			inst.Status = session.StatusWaiting
		}
		if tool == "codex" {
			inst.CodexSessionID = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		}
		if tool == "claude" {
			inst.ClaudeSessionID = fmt.Sprintf("10000000-0000-4000-8000-%012d", i)
		}
		inst.TmuxSocketName = socket
		if i%3 == 1 {
			inst.Status = session.StatusRunning
		}
		tm := inst.GetTmuxSession()
		tm.Name = fmt.Sprintf("agentdeck_bench-%04d", i)
		tm.SocketName = socket
		tm.InstanceID = inst.ID
		tm.Command = inst.Command
		args := []string{"new-session", "-d", "-s", tm.Name, "-x", "120", "-y", "40", "/bin/sh"}
		if tool != "shell" {
			args = append(args, inst.Command)
		}
		if _, err = command("tmux", args...); err != nil {
			return err
		}
		if tool == "codex" {
			if _, err = command("tmux", "set-environment", "-t", tm.Name, "CODEX_SESSION_ID", inst.CodexSessionID); err != nil {
				return err
			}
		}
		if i == 0 {
			shellID = inst.ID
			shellName = tm.Name
		}
		instances = append(instances, inst)
	}
	if err = storage.Save(instances); err != nil {
		return err
	}
	// Install only into the worker's throwaway Claude config so first-run
	// consent cannot hide the fleet. Exercise the real watcher at startup.
	if _, err = session.InjectClaudeHooks(session.GetClaudeConfigDir()); err != nil {
		return err
	}
	values := map[string][]float64{}
	groupTree := session.NewGroupTree(instances)
	measure := func(name string, fn func() error) error {
		before := lines(os.Getenv("AGENTDECK_BENCH_TMUX_LOG"))
		start := time.Now()
		if e := fn(); e != nil {
			return e
		}
		values[name+"_ms"] = append(values[name+"_ms"], float64(time.Since(start).Microseconds())/1000)
		values[name+"_tmux_subprocesses"] = append(values[name+"_tmux_subprocesses"], float64(lines(os.Getenv("AGENTDECK_BENCH_TMUX_LOG"))-before))
		return nil
	}
	for i := 0; i < runs; i++ {
		if err = seedOrphanHooks(size); err != nil {
			return err
		}
		// Average 100 complete calls per sample to reduce clock noise while
		// retaining the real persisted fleet's nested group distribution.
		groupStart := time.Now()
		for j := 0; j < 100; j++ {
			if len(groupTree.GroupActivityMap(false)) == 0 {
				return fmt.Errorf("empty group activity")
			}
		}
		values["group_activity_map_ms"] = append(values["group_activity_map_ms"], float64(time.Since(groupStart))/float64(time.Millisecond)/100)

		before := lines(os.Getenv("AGENTDECK_BENCH_TMUX_LOG"))
		elapsed, e := terminalFrame(binary)
		if e != nil {
			return e
		}
		values["cold_start_populated_terminal_frame_ms"] = append(values["cold_start_populated_terminal_frame_ms"], float64(elapsed)/float64(time.Millisecond))
		values["cold_start_populated_terminal_frame_tmux_subprocesses"] = append(values["cold_start_populated_terminal_frame_tmux_subprocesses"], float64(lines(os.Getenv("AGENTDECK_BENCH_TMUX_LOG"))-before))
		entries, e := os.ReadDir(session.GetHooksDir())
		if e != nil {
			return e
		}
		remaining := 0
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "orphan-") {
				remaining++
			}
		}
		values["orphan_hook_files_remaining"] = append(values["orphan_hook_files_remaining"], float64(remaining))
		if err = measure("list_json", func() error {
			b, e := command(binary, "list", "--json")
			if e != nil {
				return e
			}
			var rows []struct {
				Tool string `json:"tool"`
			}
			if e = json.Unmarshal(b, &rows); e != nil {
				return e
			}
			if len(rows) != size {
				return fmt.Errorf("list count %d want %d", len(rows), size)
			}
			mix := map[string]int{}
			for _, row := range rows {
				mix[row.Tool]++
			}
			if mix["shell"] != (size+2)/3 || mix["claude"] != (size+1)/3 || mix["codex"] != size/3 {
				return fmt.Errorf("runtime tool mix changed: %v", mix)
			}
			return nil
		}); err != nil {
			return err
		}
		if err = measure("session_show", func() error { _, e := command(binary, "session", "show", "--json", shellID); return e }); err != nil {
			return err
		}
		if err = measure("send_round_trip", func() error {
			token := fmt.Sprintf("BENCH_ACK_%d", i)
			_, e := command(binary, "session", "send", shellID, "printf '"+token+"\\n'")
			// Shells do not expose an agent composer. Preserve the CLI's
			// unconfirmed verdict separately and require an executed ACK below.
			unconfirmed := 0.0
			if e != nil {
				if !strings.Contains(e.Error(), "never confirmed submitted") {
					return e
				}
				unconfirmed = 1
			}
			values["send_cli_unconfirmed"] = append(values["send_cli_unconfirmed"], unconfirmed)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				b, e := command("tmux", "capture-pane", "-p", "-t", shellName)
				if e != nil {
					return e
				}
				for _, line := range strings.Split(string(b), "\n") {
					if strings.TrimSpace(line) == token {
						return nil
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			return fmt.Errorf("send acknowledgement timeout")
		}); err != nil {
			return err
		}
		for _, host := range []string{"fast", "slow", "auth"} {
			name := "remote_" + host + "_refresh"
			beforeSSH := lines(filepath.Join(root, "ssh.log"))
			start := time.Now()
			rows, e := session.NewSSHRunner(host, session.RemoteConfig{Host: "bench-" + host}).FetchSessions(context.Background())
			if host == "auth" {
				if e == nil {
					return fmt.Errorf("auth fixture unexpectedly succeeded")
				}
			} else if e != nil || len(rows) != 1 {
				return fmt.Errorf("remote %s: rows %d, %v", host, len(rows), e)
			}
			values[name+"_ms"] = append(values[name+"_ms"], float64(time.Since(start).Microseconds())/1000)
			values[name+"_ssh_subprocesses"] = append(values[name+"_ssh_subprocesses"], float64(lines(filepath.Join(root, "ssh.log"))-beforeSSH))
		}
	}
	for sample := 0; sample < runs; sample++ {
		uiout := filepath.Join(root, fmt.Sprintf("ui-%d.json", sample))
		c := exec.Command(harness, "-test.run", "^TestPerfFleetUIHarness$", "-test.timeout", "10m")
		bindChildLifetime(c)
		c.Env = append(os.Environ(), "AGENTDECK_BENCH_UI=1", "AGENTDECK_BENCH_UI_OUTPUT="+uiout, "AGENTDECK_BENCH_RUNS=1", "AGENTDECK_BENCH_SIZE="+strconv.Itoa(size))
		c.Stdout = os.Stderr
		c.Stderr = os.Stderr
		if err = c.Run(); err != nil {
			return err
		}
		b, err := os.ReadFile(uiout)
		if err != nil {
			return err
		}
		var ui map[string][]float64
		if err = json.Unmarshal(b, &ui); err != nil {
			return err
		}
		for k, v := range ui {
			values[k] = append(values[k], v...)
		}
	}
	names := make([]string, 0, len(values))
	for k := range values {
		names = append(names, k)
	}
	sort.Strings(names)
	var metrics []metric
	for _, name := range names {
		v := values[name]
		unit := "count"
		if strings.HasSuffix(name, "_ms") {
			unit = "ms"
		}
		if strings.HasSuffix(name, "_bytes") {
			unit = "bytes"
		}
		ordered := append([]float64(nil), v...)
		sort.Float64s(ordered)
		metrics = append(metrics, metric{size, name, unit, v, quantile(ordered, .5), quantile(ordered, .95)})
	}
	return writeJSON(out, metrics)
}
func quantile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return v[int(math.Ceil(float64(len(v))*p))-1]
}
func lines(path string) int { b, _ := os.ReadFile(path); return bytes.Count(b, []byte("\n")) }
func command(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	bindChildLifetime(c)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, e := c.Output()
	if e != nil {
		return b, fmt.Errorf("%s %v: %w: %s", name, args, e, stderr.String())
	}
	return b, nil
}
func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}

// terminalFrame measures process spawn through delivery of a populated frame to
// a PTY, including startup workers. The marker is a synthetic fleet title.
func terminalFrame(binary string) (elapsed time.Duration, err error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, binary)
	bindChildLifetime(c)
	term, err := pty.StartWithSize(c, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return 0, err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	defer func() { _ = term.Close(); _ = c.Process.Kill(); <-done }()
	received := make(chan error, 1)
	go func() {
		var output strings.Builder
		buf := make([]byte, 16384)
		for {
			n, e := term.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
				if strings.Contains(output.String(), "bench-0") {
					received <- nil
					return
				}
				if output.Len() > 4<<20 {
					received <- fmt.Errorf("terminal output exceeds limit without fleet frame")
					return
				}
			}
			if e != nil {
				received <- fmt.Errorf("terminal closed before fleet frame: %w; %s", e, output.String())
				return
			}
		}
	}()
	select {
	case err = <-received:
		return time.Since(started), err
	case <-ctx.Done():
		_ = term.Close()
		readResult := <-received
		return 0, fmt.Errorf("populated terminal frame: %w; %v", ctx.Err(), readResult)
	}
}

func seedOrphanHooks(size int) error {
	hooksDir := session.GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0700); err != nil {
		return err
	}
	for i := 0; i < size*3; i++ {
		path := filepath.Join(hooksDir, fmt.Sprintf("orphan-%04d.json", i))
		if err := os.WriteFile(path, []byte(`{"status":"idle"}`), 0600); err != nil {
			return err
		}
		old := time.Unix(1700000000, 0)
		if err := os.Chtimes(path, old, old); err != nil {
			return err
		}
	}
	return nil
}
