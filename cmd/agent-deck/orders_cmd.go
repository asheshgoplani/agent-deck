package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
	"github.com/asheshgoplani/agent-deck/internal/orders"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const ordersUsage = `Usage: agent-deck orders <command> [options]

Standing orders: "check X every N, wake me only when it changes". Each
conductor keeps them in ORDERS.md in its directory (edit it in vim, or let
the conductor write it). One timer per host runs 'orders tick --all' every
minute. A due order runs its check: line (shell, 60 s timeout, no AI). Same
output as last time: nothing is sent and no tokens are spent. Changed: the
conductor gets one message with the change, the do: text and the ask_first
list. The output is marked seen only after that send was delivered.

Commands:
  list                        Orders, their state and wakes today [--json]
  add <name> --check CMD      Add an order [--every 15m] [--active "08:00-22:00 Europe/Berlin"]
                              [--do TEXT] [--ask-first LIST] [--max-wakes-per-day 6] [--expires YYYY-MM-DD]
  pause <name>                Stop checking an order (kept in the file)
  resume <name>               Check it again
  rm <name>                   Remove an order
  tick                        Run due orders once [--all] [--json] [--source NAME]
  install | uninstall         Install or remove this host's one-minute timer

Common options:
  --conductor NAME            Which conductor (default: the conductor running this
                              command, else the only conductor of the profile)

A turn started by an order may not add, change or remove orders: those
commands refuse (exit 2) from an order-started turn. A permission prompt in
such a turn is denied and a NEED goes to the owner. Docs: docs/orders.md.`

// ordersSend delivers one order message to a conductor; tests replace it.
var ordersSend = sendOrderViaCLI

// ordersRun runs one check; tests replace it.
var ordersRun = orders.RunCheck

// ordersNow is the tick clock; tests replace it.
var ordersNow = time.Now

func handleOrders(profile string, args []string) {
	err := runOrders(os.Stdout, profile, args)
	var refused errOrderTurn
	if errors.As(err, &refused) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		exitCLI(2)
	}
	exitOnCLIError(err)
}

func runOrders(stdout io.Writer, profile string, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stdout, ordersUsage)
		return nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		return runOrdersList(stdout, profile, rest)
	case "add":
		return runOrdersAdd(stdout, profile, rest)
	case "pause":
		return runOrdersSetStatus(stdout, profile, rest, orders.StatusPaused)
	case "resume":
		return runOrdersSetStatus(stdout, profile, rest, orders.StatusActive)
	case "rm", "remove":
		return runOrdersRemove(stdout, profile, rest)
	case "tick":
		return runOrdersTick(stdout, profile, rest)
	case "install":
		return runOrdersInstall(stdout, rest, true)
	case "uninstall":
		return runOrdersInstall(stdout, rest, false)
	}
	fmt.Fprintln(os.Stderr, ordersUsage)
	return fmt.Errorf("unknown orders command %q", sub)
}

// errOrderTurn is the refusal for an order-started turn (exit 2).
type errOrderTurn struct{ origin string }

func (e errOrderTurn) Error() string {
	return fmt.Sprintf("refused: this turn was started by %s; orders cannot be added, changed or removed from an order-started turn (ask the owner with NEED:)", e.origin)
}

// refuseFromOrderTurn enforces "jobs can't make jobs": the calling session's
// current turn must not have been started by a standing order.
func refuseFromOrderTurn() error {
	id := strings.TrimSpace(os.Getenv("AGENTDECK_INSTANCE_ID"))
	if id == "" {
		return nil
	}
	if origin := session.LoadTurnOrigin(id); session.IsOrderTurnOrigin(origin) {
		return errOrderTurn{origin: origin}
	}
	return nil
}

// resolveOrdersConductor picks the conductor: --conductor, else the one
// running this command, else the only conductor of the profile.
func resolveOrdersConductor(explicit, profile string) (*session.ConductorMeta, error) {
	name := strings.TrimSpace(explicit)
	if name == "" {
		if n, err := resolveHumanConductor("", profile); err == nil {
			name = n
		}
	}
	if name == "" {
		eff, err := session.ResolveProfileForStorage(profile)
		if err != nil {
			return nil, err
		}
		list, err := session.ListConductorsForProfile(eff)
		if err != nil {
			return nil, err
		}
		if len(list) != 1 {
			return nil, fmt.Errorf("profile %q has %d conductors; pass --conductor NAME", eff, len(list))
		}
		name = list[0].Name
	}
	meta, err := session.LoadConductorMeta(name)
	if err != nil {
		return nil, fmt.Errorf("conductor %q: %w", name, err)
	}
	return meta, nil
}

func conductorDirFor(meta *session.ConductorMeta) (string, error) {
	return session.ConductorNameDir(meta.Name)
}

func readOrdersFile(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, orders.FileName))
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

// editOrders applies one edit under the orders lock (it waits for a
// running tick) after the order-turn check.
func editOrders(profile, conductor string, edit func(text string) (string, error)) (*session.ConductorMeta, error) {
	if err := refuseFromOrderTurn(); err != nil {
		return nil, err
	}
	meta, err := resolveOrdersConductor(conductor, profile)
	if err != nil {
		return nil, err
	}
	dir, err := conductorDirFor(meta)
	if err != nil {
		return nil, err
	}
	release, err := orders.Lock(dir, true)
	if err != nil {
		return nil, err
	}
	defer release()
	text, err := readOrdersFile(dir)
	if err != nil {
		return nil, err
	}
	next, err := edit(text)
	if err != nil {
		return nil, err
	}
	return meta, orders.WriteFile(dir, next)
}

func runOrdersAdd(stdout io.Writer, profile string, args []string) error {
	fs := flag.NewFlagSet("orders add", flag.ContinueOnError)
	conductor := fs.String("conductor", "", "conductor name")
	check := fs.String("check", "", "shell command whose output is watched (required)")
	every := fs.String("every", "15m", "how often: 15m, 2h, 1d")
	active := fs.String("active", "", `active hours, e.g. "08:00-22:00 Europe/Berlin"`)
	do := fs.String("do", "", "what the conductor should do when the output changes")
	askFirst := fs.String("ask-first", "", "actions the conductor must ask the owner before (comma list)")
	maxWakes := fs.Int("max-wakes-per-day", orders.DefaultMaxWakesPerDay, "wake cap per day")
	expires := fs.String("expires", "", "last day the order runs (YYYY-MM-DD)")
	fs.SetOutput(stdout)
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 1 || strings.TrimSpace(*check) == "" {
		return errors.New("usage: agent-deck orders add <name> --check CMD [options]")
	}
	d, err := orders.ParseEvery(*every)
	if err != nil {
		return err
	}
	o := orders.Order{Name: fs.Arg(0), Every: d, Active: *active, Check: *check, Do: *do,
		AskFirst: *askFirst, MaxWakesPerDay: *maxWakes, Expires: *expires}
	meta, err := editOrders(profile, *conductor, func(text string) (string, error) { return orders.Add(text, o) })
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "added order %s to %s (first check records a baseline; it wakes only on change)\n", o.Name, meta.Name)
	return nil
}

func runOrdersSetStatus(stdout io.Writer, profile string, args []string, status string) error {
	fs := flag.NewFlagSet("orders "+status, flag.ContinueOnError)
	conductor := fs.String("conductor", "", "conductor name")
	fs.SetOutput(stdout)
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: agent-deck orders pause|resume <name> [--conductor NAME]")
	}
	name := fs.Arg(0)
	meta, err := editOrders(profile, *conductor, func(text string) (string, error) { return orders.SetStatus(text, name, status) })
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "order %s of %s is %s\n", name, meta.Name, status)
	return nil
}

func runOrdersRemove(stdout io.Writer, profile string, args []string) error {
	fs := flag.NewFlagSet("orders rm", flag.ContinueOnError)
	conductor := fs.String("conductor", "", "conductor name")
	fs.SetOutput(stdout)
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: agent-deck orders rm <name> [--conductor NAME]")
	}
	name := fs.Arg(0)
	meta, err := editOrders(profile, *conductor, func(text string) (string, error) { return orders.Remove(text, name) })
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "removed order %s from %s\n", name, meta.Name)
	return nil
}

type orderListRow struct {
	Conductor   string    `json:"conductor"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Every       string    `json:"every"`
	Active      string    `json:"active,omitempty"`
	Check       string    `json:"check"`
	NextCheck   time.Time `json:"next_check,omitempty"`
	LastCheck   time.Time `json:"last_check,omitempty"`
	LastWake    time.Time `json:"last_wake,omitempty"`
	WakesToday  int       `json:"wakes_today"`
	MaxWakes    int       `json:"max_wakes_per_day"`
	SilentCheck int       `json:"silent_checks"`
	Expires     string    `json:"expires,omitempty"`
}

func runOrdersList(stdout io.Writer, profile string, args []string) error {
	fs := flag.NewFlagSet("orders list", flag.ContinueOnError)
	conductor := fs.String("conductor", "", "conductor name")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.SetOutput(stdout)
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	meta, err := resolveOrdersConductor(*conductor, profile)
	if err != nil {
		return err
	}
	dir, err := conductorDirFor(meta)
	if err != nil {
		return err
	}
	text, err := readOrdersFile(dir)
	if err != nil {
		return err
	}
	list, errs := orders.Parse(text)
	st := orders.LoadState(dir)
	today := time.Now().Format("2006-01-02")
	rows := []orderListRow{}
	for _, o := range list {
		r := orderListRow{Conductor: meta.Name, Name: o.Name, Status: o.Status, Every: orders.FormatEvery(o.Every),
			Active: o.Active, Check: o.Check, MaxWakes: o.MaxWakesPerDay, Expires: o.Expires}
		if s := st[o.Name]; s != nil {
			r.NextCheck, r.LastCheck, r.LastWake, r.SilentCheck = s.NextCheck, s.LastCheck, s.LastWake, s.SilentChecks
			if s.WakeDay == today {
				r.WakesToday = s.WakesToday
			}
		}
		rows = append(rows, r)
	}
	if *asJSON {
		var problems []string
		for _, e := range errs {
			problems = append(problems, e.Error())
		}
		return json.NewEncoder(stdout).Encode(map[string]any{"conductor": meta.Name, "file": filepath.Join(dir, orders.FileName), "orders": rows, "errors": problems})
	}
	if len(rows) == 0 && len(errs) == 0 {
		fmt.Fprintf(stdout, "No standing orders for %s (%s).\n", meta.Name, filepath.Join(dir, orders.FileName))
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATUS\tEVERY\tNEXT CHECK\tLAST WAKE\tWAKES TODAY\tSILENT")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d/%d\t%d\n", r.Name, r.Status, r.Every, fmtWhen(r.NextCheck), fmtWhen(r.LastWake), r.WakesToday, r.MaxWakes, r.SilentCheck)
	}
	_ = tw.Flush()
	for _, e := range errs {
		fmt.Fprintf(stdout, "invalid: %v\n", e)
	}
	return nil
}

func fmtWhen(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("Jan 02 15:04")
}

type tickReport struct {
	Conductor string           `json:"conductor"`
	Outcomes  []orders.Outcome `json:"outcomes"`
	Error     string           `json:"error,omitempty"`
}

func runOrdersTick(stdout io.Writer, profile string, args []string) error {
	fs := flag.NewFlagSet("orders tick", flag.ContinueOnError)
	conductor := fs.String("conductor", "", "conductor name")
	all := fs.Bool("all", false, "every conductor on this host (what the timer runs)")
	source := fs.String("source", session.OrdersTickSource, "trigger source the wakes are tagged with (agent-deck triggers)")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.SetOutput(stdout)
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	var targets []session.ConductorMeta
	if *all {
		list, err := session.ListConductors()
		if err != nil {
			return err
		}
		targets = list
	} else {
		meta, err := resolveOrdersConductor(*conductor, profile)
		if err != nil {
			return err
		}
		targets = []session.ConductorMeta{*meta}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
	var reports []tickReport
	failed := false
	for _, meta := range targets {
		dir, err := session.ConductorNameDir(meta.Name)
		if err != nil {
			continue
		}
		meta := meta
		env := orders.Env{
			Dir: dir, Now: ordersNow(), Loc: time.Local, Run: ordersRun,
			Send: func(order, text string) (orders.SendResult, error) {
				return ordersSend(meta.Profile, session.ConductorSessionTitle(meta.Name), *source, text)
			},
		}
		out, err := orders.Tick(env)
		rep := tickReport{Conductor: meta.Name, Outcomes: out}
		switch {
		case errors.Is(err, orders.ErrLocked):
			rep.Error = "another tick holds the lock; skipped"
		case err != nil:
			rep.Error = err.Error()
			failed = true
		}
		if out == nil && rep.Error == "" {
			continue // no ORDERS.md
		}
		reports = append(reports, rep)
	}
	if *asJSON {
		if reports == nil {
			reports = []tickReport{}
		}
		if err := json.NewEncoder(stdout).Encode(reports); err != nil {
			return err
		}
	} else {
		for _, r := range reports {
			if r.Error != "" {
				fmt.Fprintf(stdout, "%s: %s\n", r.Conductor, r.Error)
			}
			for _, o := range r.Outcomes {
				line := fmt.Sprintf("%s/%s: %s", r.Conductor, o.Order, o.Result)
				if o.Detail != "" {
					line += " (" + o.Detail + ")"
				}
				fmt.Fprintln(stdout, line)
			}
		}
	}
	if failed {
		return errors.New("orders tick failed for at least one conductor")
	}
	return nil
}

// sendOrderViaCLI delivers through `agent-deck session send`, so the send
// gets the same idle gate, mute, budget and triggers record as any other.
// --json tells a muted (accepted, not typed) send apart from a delivered one.
func sendOrderViaCLI(profile, target, source, text string) (orders.SendResult, error) {
	bin := session.FindAgentDeck()
	if bin == "" {
		if exe, err := os.Executable(); err == nil {
			bin = exe
		} else {
			bin = "agent-deck"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var argv []string
	if profile != "" {
		argv = append(argv, "-p", profile)
	}
	argv = append(argv, "session", "send", target, "--message-file", "-", "--no-wait", "--no-tag", "--json", "--source", source)
	cmd := exec.CommandContext(ctx, bin, argv...) //nolint:gosec // G204: our own binary, fixed verbs
	cmd.Env = childenv.ForSelfReexec()
	cmd.Stdin = strings.NewReader(text)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		return orders.SendResult{}, fmt.Errorf("session send to %s: %v: %s", target, err, msg)
	}
	return parseOrderSendJSON(out.Bytes()), nil
}

// parseOrderSendJSON reads `session send --json`: muted or skipped means the
// receiving end accepted the send but nothing was typed.
func parseOrderSendJSON(data []byte) orders.SendResult {
	var res struct {
		Muted   bool `json:"muted"`
		Skipped bool `json:"skipped"`
	}
	if json.Unmarshal(bytes.TrimSpace(data), &res) != nil {
		return orders.SendResult{Delivered: true}
	}
	switch {
	case res.Muted:
		return orders.SendResult{Delivered: false, Detail: "trigger muted"}
	case res.Skipped:
		return orders.SendResult{Delivered: false, Detail: "send skipped"}
	}
	return orders.SendResult{Delivered: true}
}

func runOrdersInstall(stdout io.Writer, args []string, install bool) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, "Usage: agent-deck orders install|uninstall")
		return nil
	}
	if !install {
		if err := session.UninstallOrdersDaemon(); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "standing-orders timer removed")
		return nil
	}
	path, err := session.InstallOrdersDaemon()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "standing-orders timer installed (every minute): %s\n", path)
	return nil
}
