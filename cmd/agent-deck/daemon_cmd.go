package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"reflect"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/core"
	"github.com/asheshgoplani/agent-deck/internal/core/daemon"
	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const daemonUsage = `Usage: agent-deck daemon <serve|status|stop>

Serve the command registry and the event bus on a 0600 unix socket in the
profile's runtime dir (docs/daemon-protocol.md). Direct mode stays the
default: the CLI only talks to the daemon when [core] daemon = true, and
then only for --json=envelope requests, falling back to running in process
when no daemon answers.

Commands:
  serve            Run the daemon in the foreground until SIGINT/SIGTERM or stop
  status [--json]  Report running, stale (owner died) or absent; exit 1 unless running
  stop             Ask a running daemon to shut down`

// handleDaemon dispatches `agent-deck daemon ...`.
func handleDaemon(profile string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, daemonUsage)
		os.Exit(1)
	}
	switch args[0] {
	case "serve":
		daemonServe(profile, args[1:])
	case "status":
		daemonStatusCmd(profile, args[1:])
	case "stop":
		daemonStop(profile, args[1:])
	case "help", "--help", "-h":
		fmt.Println(daemonUsage)
	default:
		fmt.Fprintf(os.Stderr, "Unknown daemon command: %s\n\n%s\n", args[0], daemonUsage)
		os.Exit(1)
	}
}

// daemonPaths resolves the profile the daemon serves and its socket paths.
func daemonPaths(profile string) (string, daemon.Paths, error) {
	resolved, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return "", daemon.Paths{}, err
	}
	paths, err := daemon.PathsFor(resolved)
	return resolved, paths, err
}

func parseDaemonFlags(name string, args []string, setup func(fs *flag.FlagSet)) {
	fs := flag.NewFlagSet("daemon "+name, flag.ExitOnError)
	if setup != nil {
		setup(fs)
	}
	fs.Usage = func() {
		fmt.Println(daemonUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "Error: daemon %s takes no arguments\n", name)
		os.Exit(1)
	}
}

func daemonServe(profile string, args []string) {
	parseDaemonFlags("serve", args, nil)
	resolved, paths, err := daemonPaths(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	owner, err := daemon.Acquire(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer owner.Close()

	bus := events.Default()
	defer bus.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := daemon.New(daemon.Options{
		Registry: coreRegistry(),
		Bus:      bus,
		Profile:  resolved,
		OwnerUID: os.Getuid(),
		Version:  Version,
		Socket:   paths.Socket,
	})
	fmt.Printf("agent-deck daemon serving profile %s on %s (pid %d)\n", resolved, paths.Socket, os.Getpid())
	if err := srv.Serve(ctx, owner.Listener()); err != nil {
		fmt.Fprintf(os.Stderr, "Error: daemon: %v\n", err)
		os.Exit(1)
	}
}

// daemonStatusOut is `daemon status --json`.
type daemonStatusOut struct {
	State  daemon.State   `json:"state"`
	PID    int            `json:"pid,omitempty"`
	Socket string         `json:"socket"`
	Status *daemon.Status `json:"status,omitempty"`
}

func daemonStatusCmd(profile string, args []string) {
	var asJSON bool
	parseDaemonFlags("status", args, func(fs *flag.FlagSet) { fs.BoolVar(&asJSON, "json", false, "Output as JSON") })
	_, paths, err := daemonPaths(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	probe := daemon.Probe(paths)
	out := daemonStatusOut{State: probe.State, PID: probe.PID, Socket: paths.Socket}
	if probe.State == daemon.StateRunning {
		out.Status = &probe.Status
	}
	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	} else {
		switch probe.State {
		case daemon.StateRunning:
			st := probe.Status
			fmt.Printf("running: pid %d, profile %s, version %s, since %s, %d calls, %d connections\nsocket: %s\n",
				st.PID, st.Profile, st.Version, st.StartedAt, st.Calls, st.Connections, paths.Socket)
		case daemon.StateStale:
			fmt.Printf("not running: stale socket from pid %d (serve can replace it if the recorded pid is dead)\nsocket: %s\n", probe.PID, paths.Socket)
		case daemon.StateUnknown:
			fmt.Printf("unknown: socket exists but the daemon did not answer\nsocket: %s\n", paths.Socket)
		default:
			fmt.Printf("not running\nsocket: %s\n", paths.Socket)
		}
	}
	if probe.State != daemon.StateRunning {
		os.Exit(1)
	}
}

func daemonStop(profile string, args []string) {
	parseDaemonFlags("stop", args, nil)
	_, paths, err := daemonPaths(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	probe := daemon.Probe(paths)
	if probe.State == daemon.StateUnknown {
		fmt.Fprintf(os.Stderr, "Error: daemon socket %s accepts connections but did not answer; cannot confirm stop\n", paths.Socket)
		os.Exit(1)
	}
	if probe.State != daemon.StateRunning {
		fmt.Println("daemon not running")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := daemon.Dial(ctx, paths.Socket)
	if err == nil {
		err = c.Shutdown()
		_ = c.Close()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: stop daemon: %v\n", err)
		os.Exit(1)
	}
	deadline := time.Now().Add(5 * time.Second)
	for daemon.Probe(paths).State == daemon.StateRunning {
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "Error: daemon (pid %d) still running after 5s\n", probe.PID)
			os.Exit(1)
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Printf("stopped daemon (pid %d)\n", probe.PID)
}

// coreDaemonEnabled reports `[core] daemon = true`.
func coreDaemonEnabled() bool {
	cfg, err := session.LoadUserConfig()
	return err == nil && cfg != nil && cfg.Core.Daemon
}

// runViaDaemon sends one registry command to the profile's daemon and turns
// its envelope back into a Result the CLI adapters render. ok is false when
// no daemon answered the dial, so nothing was sent and the command may run in
// process instead. Once the call was sent, a transport failure is a command
// failure, never a retry: the daemon may already have run a mutation.
func runViaDaemon(profile, id string, in any) (res *core.Result, ok bool) {
	def, ok := coreRegistry().Lookup(id)
	if !ok {
		return nil, false
	}
	_, paths, err := daemonPaths(profile)
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := daemon.Dial(ctx, paths.Socket)
	if err != nil {
		return nil, false
	}
	defer c.Close()
	raw, err := c.Call(id, in)
	if err != nil {
		return &core.Result{ID: id, Err: core.Errorf(core.CodeInternal, "daemon: %v", err)}, true
	}
	return decodeEnvelope(def, raw), true
}

// decodeEnvelope rebuilds a Result from an envelope: typed output on
// success, a core.Error with the envelope's code and raw data on failure.
func decodeEnvelope(def core.Def, raw json.RawMessage) *core.Result {
	var env struct {
		OK       bool            `json:"ok"`
		Data     json.RawMessage `json:"data"`
		Warnings []string        `json:"warnings"`
		Error    *struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	res := &core.Result{ID: def.ID}
	if err := json.Unmarshal(raw, &env); err != nil {
		res.Err = core.Errorf(core.CodeInternal, "daemon: bad envelope: %v", err)
		return res
	}
	res.Warnings = env.Warnings
	if !env.OK {
		if env.Error == nil {
			res.Err = core.Errorf(core.CodeInternal, "daemon: failed envelope without error")
			return res
		}
		ce := &core.Error{Code: env.Error.Code, Message: env.Error.Message}
		if len(env.Error.Data) > 0 {
			ce.Data = env.Error.Data
		}
		res.Err = ce
		return res
	}
	out := reflect.New(def.OutType())
	if err := json.Unmarshal(env.Data, out.Interface()); err != nil {
		res.Err = core.Errorf(core.CodeInternal, "daemon: bad %s output: %v", def.ID, err)
		return res
	}
	res.Out = out.Elem().Interface()
	return res
}
