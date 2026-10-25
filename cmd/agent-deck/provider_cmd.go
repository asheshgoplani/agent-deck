package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/provider"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// providerCLIDeps are the seams the provider commands use for everything
// that would otherwise run ssh or read the terminal, so tests drive the
// whole flow with the fake provider and no sandbox.
type providerCLIDeps struct {
	// waitSSH and install are passed to provider.Provision (nil waitSSH =
	// real ssh probe).
	waitSSH func(ctx context.Context, name string, rc session.RemoteConfig) error
	install func(ctx context.Context, name string, rc session.RemoteConfig) error
	// remoteExec runs `agent-deck remote <name> <args...>`.
	remoteExec func(name string, args []string) (int, error)
	// listSessions and stopSession reach the remote's agent-deck.
	listSessions func(ctx context.Context, name string, rc session.RemoteConfig) ([]session.RemoteSessionInfo, error)
	stopSession  func(ctx context.Context, name string, rc session.RemoteConfig, id string) error
	isTTY        func() bool
	stdin        io.Reader
	stdout       io.Writer
	stderr       io.Writer
}

// providerDeps is set in init: its default install step prints through it.
var providerDeps providerCLIDeps

func init() { providerDeps = defaultProviderDeps() }

func defaultProviderDeps() providerCLIDeps {
	return providerCLIDeps{
		install:    installOnSandbox,
		remoteExec: runRemoteExec,
		listSessions: func(ctx context.Context, name string, rc session.RemoteConfig) ([]session.RemoteSessionInfo, error) {
			sessions, _, err := session.NewSSHRunner(name, rc).FetchSessions(ctx)
			return sessions, err
		},
		stopSession: func(ctx context.Context, name string, rc session.RemoteConfig, id string) error {
			return session.NewSSHRunner(name, rc).StopSession(ctx, id)
		},
		isTTY:  stdinStdoutIsTerminal,
		stdin:  os.Stdin,
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

// installOnSandbox is the remote-add install path for a fresh sandbox:
// report the version when agent-deck answers, else deploy this build. A
// failure fails the provisioning (and rolls the sandbox back), since a
// sandbox without agent-deck cannot run sessions.
func installOnSandbox(ctx context.Context, name string, rc session.RemoteConfig) error {
	runner := session.NewSSHRunner(name, rc)
	if remoteVersion, found := runner.CheckBinary(ctx); found {
		fmt.Fprintf(providerDeps.stdout, "  Remote agent-deck: v%s\n", remoteVersion)
		if update.CompareVersions(remoteVersion, Version) < 0 {
			fmt.Fprintf(providerDeps.stdout, "  Note: remote is older than local (v%s). Run 'agent-deck remote update %s' to update.\n", Version, name)
		}
		return nil
	}
	fmt.Fprintf(providerDeps.stdout, "  agent-deck not found on the sandbox at '%s'\n", rc.GetAgentDeckPath())
	fmt.Fprintf(providerDeps.stdout, "  Installing v%s...\n", Version)
	if err := installOnRemote(runner, ctx); err != nil {
		return err
	}
	fmt.Fprintf(providerDeps.stdout, "  ✓ Installed agent-deck v%s on remote '%s'\n", Version, name)
	return nil
}

// providerSignalContext is cancelled by SIGINT/SIGTERM so a long create can
// be interrupted (Provision still rolls back on its own context).
func providerSignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func handleProvider(profile string, args []string) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printProviderUsage(os.Stdout)
		return
	}
	var code int
	switch args[0] {
	case "list", "ls":
		code = runProviderList(args[1:])
	case "test":
		code = runProviderTest(profile, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown provider command %q\n\n", args[0])
		printProviderUsage(os.Stderr)
		code = 2
	}
	if code != 0 {
		exitCLI(code)
	}
}

func printProviderUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck provider <command> [options]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Sandbox providers create sandboxes that agent-deck registers as remotes.")
	fmt.Fprintln(w, "Configure one as [providers.<name>] (command = \"...\") in config.toml.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  list [--json]             List configured providers and whether they answer")
	fmt.Fprintln(w, "  test <name> [--create]    Run the provider's list (and with --create a")
	fmt.Fprintln(w, "                            create -> status -> destroy round trip)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related:")
	fmt.Fprintln(w, "  agent-deck remote add --provider <name> [--name x]   Create a sandbox and register it")
	fmt.Fprintln(w, "  agent-deck launch --provider <name> [--name x] <path> [launch flags]")
	fmt.Fprintln(w, "  agent-deck remote destroy <name> [--yes] [--force]  Stop its sessions, destroy the sandbox")
}

func newProviderFlagSet(name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintln(out, usage)
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Options:")
		fs.PrintDefaults()
	}
	return fs
}

func runProviderList(args []string) int {
	fs := newProviderFlagSet("provider list", "Usage: agent-deck provider list [--json]")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	if err := parseCLIFlags(fs, args); err != nil {
		return 2
	}
	cfg, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(providerDeps.stderr, "Error: loading config: %v\n", err)
		return 1
	}
	type row struct {
		Name      string `json:"name"`
		Command   string `json:"command"`
		Reachable bool   `json:"reachable"`
		Sandboxes int    `json:"sandboxes"`
		Error     string `json:"error,omitempty"`
	}
	ctx, stop := providerSignalContext()
	defer stop()
	rows := []row{}
	for _, name := range provider.Names(cfg) {
		client, _ := provider.Lookup(cfg, name)
		r := row{Name: name, Command: client.Config.Command}
		list, err := client.List(ctx)
		if err != nil {
			r.Error = err.Error()
		} else {
			r.Reachable, r.Sandboxes = true, len(list)
		}
		rows = append(rows, r)
	}
	if *jsonOut {
		data, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			fmt.Fprintf(providerDeps.stderr, "Error: formatting JSON: %v\n", err)
			return 1
		}
		fmt.Fprintln(providerDeps.stdout, string(data))
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(providerDeps.stdout, "No providers configured.")
		fmt.Fprintln(providerDeps.stdout, "\nAdd one to config.toml:\n  [providers.docker]\n  command = \"/path/to/provider\"")
		return 0
	}
	fmt.Fprintf(providerDeps.stdout, "%-15s %-10s %-10s %s\n", "NAME", "REACHABLE", "SANDBOXES", "COMMAND")
	for _, r := range rows {
		reach, count := "yes", fmt.Sprint(r.Sandboxes)
		if !r.Reachable {
			reach, count = "no", "-"
		}
		fmt.Fprintf(providerDeps.stdout, "%-15s %-10s %-10s %s\n", r.Name, reach, count, r.Command)
		if r.Error != "" {
			fmt.Fprintf(providerDeps.stdout, "  error: %s\n", r.Error)
		}
	}
	return 0
}

func runProviderTest(profile string, args []string) int {
	fs := newProviderFlagSet("provider test", "Usage: agent-deck provider test <name> [--create]")
	create := fs.Bool("create", false, "Also create a sandbox, check its status and destroy it")
	if err := parseCLIFlags(fs, reorderRemoteArgs(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(providerDeps.stderr, "Error: requires exactly one provider name")
		fs.SetOutput(providerDeps.stderr)
		fs.Usage()
		return 2
	}
	name := fs.Arg(0)
	cfg, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(providerDeps.stderr, "Error: loading config: %v\n", err)
		return 1
	}
	client, err := provider.Lookup(cfg, name)
	if err != nil {
		fmt.Fprintf(providerDeps.stderr, "Error: %v\n", err)
		return 1
	}
	ctx, stop := providerSignalContext()
	defer stop()
	out := providerDeps.stdout
	fail := func(step string, err error) int {
		fmt.Fprintf(out, "%s: FAILED\n", step)
		fmt.Fprintf(providerDeps.stderr, "Error: %v\n", err)
		return 1
	}

	list, err := client.List(ctx)
	if err != nil {
		return fail("list", err)
	}
	fmt.Fprintf(out, "list: ok (%d sandboxes)\n", len(list))
	if !*create {
		return 0
	}

	var suffix [3]byte
	_, _ = rand.Read(suffix[:])
	testName := "agentdeck-test-" + hex.EncodeToString(suffix[:])
	labels := map[string]string{provider.LabelRemote: testName, provider.LabelProfile: session.GetEffectiveProfile(profile)}
	sb, err := client.Create(ctx, testName, labels)
	if err != nil {
		if sb != nil {
			destroyAfterFailedTest(client, sb.ID)
		}
		return fail("create", err)
	}
	fmt.Fprintf(out, "create: ok (sandbox %s at %s@%s:%d, auth %s)\n", sb.ID, sb.User, sb.Host, sb.Port, sb.Auth.Type)
	got, err := client.Status(ctx, sb.ID)
	if err != nil {
		destroyAfterFailedTest(client, sb.ID)
		return fail("status", err)
	}
	if got.ID != sb.ID {
		destroyAfterFailedTest(client, sb.ID)
		return fail("status", fmt.Errorf("status returned sandbox %q, want %q", got.ID, sb.ID))
	}
	fmt.Fprintf(out, "status: ok (state %s)\n", firstNonEmpty(got.State, "unreported"))
	if err := client.Destroy(ctx, sb.ID); err != nil {
		return fail("destroy", fmt.Errorf("%w (sandbox %s may still exist)", err, sb.ID))
	}
	fmt.Fprintln(out, "destroy: ok")
	return 0
}

// destroyAfterFailedTest cleans up the round-trip sandbox, reporting only a
// failure.
func destroyAfterFailedTest(client *provider.Client, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), client.Config.GetTimeout())
	defer cancel()
	if err := client.Destroy(ctx, id); err != nil {
		fmt.Fprintf(providerDeps.stderr, "Warning: could not destroy test sandbox %s: %v\n", id, err)
	}
}

// remoteAddProviderRequested reports whether `remote add` args ask for a
// provider sandbox.
func remoteAddProviderRequested(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--provider" || a == "-provider" || strings.HasPrefix(a, "--provider=") || strings.HasPrefix(a, "-provider=") {
			return true
		}
	}
	return false
}

func handleRemoteAddProvider(profile string, args []string) {
	if code := runRemoteAddProvider(profile, args); code != 0 {
		exitCLI(code)
	}
}

// runRemoteAddProvider is `remote add --provider <name> [--name x]`.
func runRemoteAddProvider(profile string, args []string) int {
	fs := newProviderFlagSet("remote add", "Usage: agent-deck remote add --provider <name> [--name <remote>]")
	providerName := fs.String("provider", "", "Sandbox provider to create the remote with ([providers.<name>])")
	remoteName := fs.String("name", "", "Remote name (default: <provider>-<random suffix>)")
	if err := parseCLIFlags(fs, reorderRemoteArgs(fs, args)); err != nil {
		return 2
	}
	name := *remoteName
	switch {
	case fs.NArg() == 1 && name == "":
		name = fs.Arg(0)
	case fs.NArg() > 0:
		fmt.Fprintf(providerDeps.stderr, "Error: unexpected argument(s) with --provider: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	p, code := provisionRemote(profile, *providerName, name)
	if code != 0 {
		return code
	}
	fmt.Fprintf(providerDeps.stdout, "Added remote '%s' (%s, sandbox %s)\n", p.RemoteName, provider.DisplayKind(p.Remote), p.Sandbox.ID)
	fmt.Fprintf(providerDeps.stdout, "  Launch a session: agent-deck remote %s launch <path> -c claude\n", p.RemoteName)
	fmt.Fprintf(providerDeps.stdout, "  Destroy it:       agent-deck remote destroy %s\n", p.RemoteName)
	return 0
}

// provisionRemote validates the name and runs provider.Provision with the
// CLI's seams and progress lines.
func provisionRemote(profile, providerName, name string) (*provider.Provisioned, int) {
	if strings.TrimSpace(providerName) == "" {
		fmt.Fprintln(providerDeps.stderr, "Error: --provider requires a provider name")
		return nil, 2
	}
	if name != "" && (!isValidRemoteName(name) || !provider.ValidRemoteName(name)) {
		fmt.Fprintln(providerDeps.stderr, "Error: remote name must use letters, digits, '-' and '_' and not match a remote management command")
		return nil, 1
	}
	ctx, stop := providerSignalContext()
	defer stop()
	p, err := provider.Provision(ctx, provider.ProvisionOptions{
		Provider:   providerName,
		RemoteName: name,
		Profile:    session.GetEffectiveProfile(profile),
		Progress:   func(line string) { fmt.Fprintf(providerDeps.stdout, "%s\n", line) },
		WaitSSH:    providerDeps.waitSSH,
		Install:    providerDeps.install,
	})
	if err != nil {
		fmt.Fprintf(providerDeps.stderr, "Error: %v\n", err)
		return nil, 1
	}
	return p, 0
}

// splitLaunchProviderArgs pulls --provider and --name out of launch args
// (before any "--"); rest is forwarded to the remote launch unchanged.
func splitLaunchProviderArgs(args []string) (providerName, name string, rest []string, ok bool, err error) {
	set := func(key, value string) {
		if key == "provider" {
			providerName, ok = value, true
		} else {
			name = value
		}
	}
	pending := "" // flag waiting for its value in the next argument
	for i, a := range args {
		if pending != "" {
			set(pending, a)
			pending = ""
			continue
		}
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		key, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || (key != "provider" && key != "name") {
			rest = append(rest, a)
			continue
		}
		if !hasValue {
			pending = key
			continue
		}
		set(key, value)
	}
	if pending != "" {
		return "", "", nil, true, fmt.Errorf("flag --%s needs a value", pending)
	}
	return providerName, name, rest, ok, nil
}

// launchProviderRequested reports whether launch args carry --provider.
func launchProviderRequested(args []string) bool {
	_, _, _, ok, _ := splitLaunchProviderArgs(args)
	return ok
}

func handleLaunchProvider(profile string, args []string) {
	if code := runLaunchProvider(profile, args); code != 0 {
		exitCLI(code)
	}
}

// runLaunchProvider is `launch --provider <name> [--name x] <path> [launch
// flags...]`: provision a sandbox remote, then run the usual
// `remote <name> launch <path> ...` on it. The path is on the sandbox.
func runLaunchProvider(profile string, args []string) int {
	providerName, name, rest, _, err := splitLaunchProviderArgs(args)
	if err != nil {
		fmt.Fprintf(providerDeps.stderr, "Error: %v\n", err)
		return 2
	}
	p, code := provisionRemote(profile, providerName, name)
	if code != 0 {
		return code
	}
	fmt.Fprintf(providerDeps.stdout, "Added remote '%s' (%s, sandbox %s)\n", p.RemoteName, provider.DisplayKind(p.Remote), p.Sandbox.ID)
	code, err = providerDeps.remoteExec(p.RemoteName, append([]string{"launch"}, rest...))
	if err != nil {
		fmt.Fprintf(providerDeps.stderr, "Error: %v\n", err)
	}
	if code != 0 {
		fmt.Fprintf(providerDeps.stderr, "The sandbox is still running as remote '%s'; retry with 'agent-deck remote %s launch ...' or remove it with 'agent-deck remote destroy %s'.\n", p.RemoteName, p.RemoteName, p.RemoteName)
	}
	return code
}

func handleRemoteDestroy(args []string) {
	if code := runRemoteDestroy(args); code != 0 {
		exitCLI(code)
	}
}

// runRemoteDestroy is `remote destroy <name> [--yes] [--force]`.
func runRemoteDestroy(args []string) int {
	fs := newProviderFlagSet("remote destroy", "Usage: agent-deck remote destroy <name> [--yes] [--force]")
	yes := fs.Bool("yes", false, "Stop the remote's sessions without asking")
	force := fs.Bool("force", false, "Destroy even when the remote's sessions cannot be listed")
	if err := parseCLIFlags(fs, reorderRemoteArgs(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(providerDeps.stderr, "Error: requires exactly one remote name")
		fs.SetOutput(providerDeps.stderr)
		fs.Usage()
		return 2
	}
	name := fs.Arg(0)
	out, errOut := providerDeps.stdout, providerDeps.stderr

	cfg, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(errOut, "Error: loading config: %v\n", err)
		return 1
	}
	rc, ok := cfg.Remotes[name]
	if !ok {
		fmt.Fprintf(errOut, "Error: remote '%s' not found\n", name)
		return 1
	}
	if !provider.IsSandbox(rc) {
		fmt.Fprintf(errOut, "Error: remote '%s' is a plain SSH remote, not a provider sandbox; use 'agent-deck remote remove %s' to forget it\n", name, name)
		return 1
	}

	ctx, stop := providerSignalContext()
	defer stop()
	listCtx, cancelList := context.WithTimeout(ctx, rc.GetCommandTimeout())
	sessions, listErr := providerDeps.listSessions(listCtx, name, rc)
	cancelList()
	if listErr != nil {
		if !*force {
			fmt.Fprintf(errOut, "Error: cannot list sessions on '%s': %v\nNothing was changed. Use --force to destroy the sandbox anyway.\n", name, listErr)
			return 1
		}
		fmt.Fprintf(errOut, "Warning: cannot list sessions on '%s' (%v); destroying anyway (--force)\n", name, listErr)
		sessions = nil
	}

	if len(sessions) > 0 && !*yes {
		if !providerDeps.isTTY() {
			fmt.Fprintf(errOut, "Error: remote '%s' has %s; rerun with --yes to stop them and destroy the sandbox. Nothing was changed.\n", name, pluralSessions(len(sessions)))
			return 1
		}
		fmt.Fprintf(out, "Stop %s on %s and destroy the sandbox? [y/N] ", pluralSessions(len(sessions)), name)
		answer, _ := bufio.NewReader(providerDeps.stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Fprintln(out, "Aborted. Nothing was changed.")
			return 1
		}
	}

	for _, s := range sessions {
		if s.Status == "stopped" {
			continue
		}
		stopCtx, cancelStop := context.WithTimeout(ctx, rc.GetCommandTimeout())
		err := providerDeps.stopSession(stopCtx, name, rc, s.ID)
		cancelStop()
		label := firstNonEmpty(s.Title, s.ID)
		if err != nil {
			// The destroy below ends it anyway; say so rather than abort.
			fmt.Fprintf(errOut, "Warning: stopping session %s: %v\n", label, err)
			continue
		}
		fmt.Fprintf(out, "Stopped session %s\n", label)
	}

	destroyCtx, cancelDestroy := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelDestroy()
	if err := provider.DestroyRemote(destroyCtx, name); err != nil {
		fmt.Fprintf(errOut, "Error: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "Destroyed sandbox %s (provider %s) and removed remote '%s'\n", rc.SandboxID, rc.Provider, name)
	return 0
}

// pluralSessions renders n as "1 session" or "<n> sessions".
func pluralSessions(n int) string {
	if n == 1 {
		return "1 session"
	}
	return fmt.Sprintf("%d sessions", n)
}
