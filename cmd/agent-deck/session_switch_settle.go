package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Exit codes of `session switch-settle`, also used by switch-account and
// switch when their automatic settle does not end healthy.
const (
	settleExitFailed  = 1 // settled, but a check failed (the receipt names it)
	settleExitStopped = 3 // stopped at a prompt it may not answer; nothing pressed
	switchExitSettle  = 4 // switch-account/switch --settle-strict: switched, settle not healthy
)

// handleSessionSwitchSettle finishes an account switch without a human
// (SWFB1018): optional restart, wait for ready, answer only the known
// first-start prompts, confirm plugins and MCPs (one retry), send the
// continue kick, verify the account in use, print one receipt line.
func handleSessionSwitchSettle(profile string, args []string) {
	fs := flag.NewFlagSet("session switch-settle", flag.ContinueOnError)
	account := fs.String("account", "", "Expected account; refuse when the session is on another one")
	restart := fs.Bool("restart", false, "Restart the session first (the self-switch path: after `session set account`)")
	kick := fs.String("kick", "", "Continue message to send once ready (default: a switch notice)")
	noKick := fs.Bool("no-kick", false, "Do not send a continue message")
	timeout := fs.Duration("timeout", 3*time.Minute, "How long to wait for the session to become ready")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("q", false, "Print only the receipt line")
	oldPIDs := fs.String("old-pids", "", "Comma-separated pids of the replaced process tree to wait for (set by the TUI)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session switch-settle <id|title> [options]")
		fmt.Println()
		fmt.Println("Bring a session that was just switched to another Claude account back to work:")
		fmt.Println("  1. wait for the replaced process's pollers and sockets to be gone,")
		fmt.Println("  2. wait for ready; answer ONLY these first-start prompts: folder trust for the")
		fmt.Println("     session's own project path, the theme/onboarding screens, and the bypass")
		fmt.Println("     permissions warning when the session runs with it; anything else stops")
		fmt.Println("     the settle with nothing pressed (exit 3),")
		fmt.Println("  3. confirm plugins and MCPs connected; restart once if not; name failures,")
		fmt.Println("  4. send a continue message telling the session to re-check its background work,")
		fmt.Println("  5. verify the live process runs on the account's config dir, print one receipt.")
		fmt.Println()
		fmt.Println("session switch-account and session switch run this automatically after a restart")
		fmt.Println("(--no-settle skips it). Exit: 0 healthy, 1 a check failed, 3 stopped at a prompt.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	if fs.NArg() < 1 {
		fs.Usage()
		exitCLI(1)
	}
	out := NewCLIOutput(*jsonOutput, *quiet)
	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}
	inst, errMsg, errCode := ResolveSession(fs.Arg(0), instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		exitCLI(2)
		return
	}
	if want := strings.TrimSpace(*account); want != "" && want != inst.Account {
		out.Error(fmt.Sprintf("session %s is on account %q, not %q; switch it first", inst.Title, inst.Account, want), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	if !session.IsClaudeCompatible(inst.Tool) {
		out.Error(fmt.Sprintf("switch-settle supports Claude sessions; %s runs %s", inst.Title, inst.Tool), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	message := *kick
	if *noKick {
		message = ""
	} else if message == "" {
		message = session.DefaultSettleKick(inst.Account)
	}
	persist := func() error { return saveSessionData(storage, instances, groups) }
	report, settleErr := runSwitchSettle(inst, settleRun{restart: *restart, kick: message, timeout: *timeout, persist: persist, oldPIDs: parsePIDList(*oldPIDs),
		log: func(format string, a ...any) {
			if !*quiet && !*jsonOutput {
				fmt.Fprintf(os.Stderr, "settle: "+format+"\n", a...)
			}
		}})
	emitSettleReport(out, *jsonOutput, report, settleErr)
	if code := settleExitCode(report, settleErr); code != 0 {
		exitCLI(code)
	}
}

type settleRun struct {
	restart bool
	oldPIDs []int
	kick    string
	timeout time.Duration
	persist func() error
	log     func(format string, args ...any)
}

// runSwitchSettle wires session.SettleSwitch to the live tmux pane, the
// process table, the normal send path and `claude auth status`.
func runSwitchSettle(inst *session.Instance, run settleRun) (*session.SettleReport, error) {
	cfg, _ := session.LoadUserConfig()
	procs := session.NewHostSettleProcReader()
	restart := func() error {
		if err := inst.Restart(); err != nil {
			return err
		}
		if err := inst.VerifySpawned(spawnVerifyWait); err != nil {
			return err
		}
		inst.LastStartedAt = time.Now()
		if run.persist != nil {
			return run.persist()
		}
		return nil
	}
	oldPIDs := append([]int(nil), run.oldPIDs...)
	if run.restart {
		oldPIDs = append(oldPIDs, session.SnapshotPaneProcesses(inst)...)
		if err := restart(); err != nil {
			return &session.SettleReport{Title: inst.Title, ID: inst.ID, Account: inst.Account, Kick: "not-sent",
				Problems: []string{"restart: " + err.Error()}}, nil
		}
	}
	deps := settleTmuxDeps(inst.GetTmuxSession)
	deps.Restart = restart
	deps.Procs = procs
	deps.Kick = func(message string) error { return sendSettleKick(inst, message) }
	deps.AuthInfo = session.ClaudeAuthIdentity
	deps.Log = run.log
	return session.SettleSwitch(deps, session.SettleOptions{
		InstanceID:   inst.ID,
		Title:        inst.Title,
		Account:      inst.Account,
		ConfigDir:    settleExpectedConfigDir(inst),
		ProjectPath:  inst.EffectiveWorkingDir(),
		Bypass:       session.SessionRunsBypass(inst, cfg),
		Plugins:      settleChannelPlugins(inst),
		Kick:         run.kick,
		ReadyTimeout: run.timeout,
		OldPIDs:      oldPIDs,
	})
}

// settleTmuxDeps reads and answers the session's agent window: the primary
// window, whichever window the operator has focused. tm is looked up per call
// because a restart can replace the session.
func settleTmuxDeps(tm func() *tmux.Session) session.SettleDeps {
	get := func() (*tmux.Session, error) {
		if s := tm(); s != nil {
			return s, nil
		}
		return nil, fmt.Errorf("no tmux session")
	}
	return session.SettleDeps{
		Capture: func() (string, error) {
			s, err := get()
			if err != nil {
				return "", err
			}
			return s.CapturePrimaryPaneFresh()
		},
		SendKey: func(key string) error {
			s, err := get()
			if err != nil {
				return err
			}
			return s.SendNamedKeyToPrimaryWindow(key)
		},
		PanePID: func() (int, error) {
			s, err := get()
			if err != nil {
				return 0, err
			}
			return s.PrimaryPanePID()
		},
		SessionPanePIDs: func() ([]int, error) {
			s, err := get()
			if err != nil {
				return nil, err
			}
			return s.AllPanePIDs()
		},
	}
}

// settleExpectedConfigDir is the account dir the restarted process must
// run on. A worker runs on a scratch mirror of it; verification resolves the
// mirror's source marker back to this dir (the scratch path itself is not
// persisted, so a fresh CLI process cannot know it).
func settleExpectedConfigDir(inst *session.Instance) string {
	return session.GetClaudeConfigDirForInstance(inst)
}

// settleChannelPlugins names the channel plugins the session owns
// ("plugin:telegram@…" -> "telegram"); each must have a poller afterwards.
func settleChannelPlugins(inst *session.Instance) []string {
	var names []string
	for _, ch := range inst.Channels {
		name := strings.TrimPrefix(ch, "plugin:")
		if i := strings.IndexAny(name, "@:"); i > 0 {
			name = name[:i]
		}
		if name != "" && name != ch {
			names = append(names, name)
		}
	}
	return names
}

// sendSettleKick delivers the continue message through the normal send
// path: readiness guard, composer-draft guard and submit verification.
func sendSettleKick(inst *session.Instance, message string) error {
	tm := inst.GetTmuxSession()
	if tm == nil {
		return fmt.Errorf("no tmux session")
	}
	transport, _ := sendTransportFromConfig()
	_, err := performSend(inst, tm, message, false, defaultSendTuning(), transport, false, nil, nil, nil)
	return err
}

func emitSettleReport(out *CLIOutput, jsonOutput bool, report *session.SettleReport, settleErr error) {
	if report == nil {
		msg := "settle failed"
		if settleErr != nil {
			msg = settleErr.Error()
		}
		out.Error(msg, ErrCodeInvalidOperation)
		return
	}
	if jsonOutput {
		out.Success(report.Receipt(), map[string]interface{}{"success": report.OK(), "receipt": report.Receipt(), "settle": report})
		return
	}
	fmt.Println(report.Receipt())
}

func settleExitCode(report *session.SettleReport, settleErr error) int {
	switch {
	case errors.Is(settleErr, session.ErrSettleStopped):
		return settleExitStopped
	case report == nil || settleErr != nil || !report.OK():
		return settleExitFailed
	}
	return 0
}

func parsePIDList(value string) []int {
	var out []int
	for _, part := range strings.Split(value, ",") {
		if pid, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && pid > 1 {
			out = append(out, pid)
		}
	}
	return out
}

// settleAfterSwitch is the automatic settle of switch-account and switch.
// oldPIDs is the pane snapshot taken before the switch stopped the session;
// persist saves the registry after a plugin-retry restart.
func settleAfterSwitch(inst *session.Instance, quiet bool, oldPIDs []int, persist func() error) (*session.SettleReport, error) {
	return runSwitchSettle(inst, settleRun{kick: session.DefaultSettleKick(inst.Account), timeout: 3 * time.Minute, oldPIDs: oldPIDs, persist: persist,
		log: func(format string, a ...any) {
			if !quiet {
				fmt.Fprintf(os.Stderr, "settle: "+format+"\n", a...)
			}
		}})
}

// switchSettleExit is the exit code of switch-account/switch after a
// switch that succeeded: 0 unless --settle-strict asked for the settle's
// verdict (3 stopped at a prompt, 4 any other unhealthy settle). The receipt
// and the JSON "settle" object carry the verdict either way.
func switchSettleExit(strict bool, report *session.SettleReport, err error) int {
	if !strict || report == nil {
		return 0
	}
	switch settleExitCode(report, err) {
	case 0:
		return 0
	case settleExitStopped:
		return settleExitStopped
	}
	return switchExitSettle
}

// loadUserConfigOrNil is the config for best-effort decisions; a load error
// means "no config defaults".
func loadUserConfigOrNil() *session.UserConfig {
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return nil
	}
	return cfg
}
