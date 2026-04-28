package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/feedback"
	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/ui"
	"github.com/asheshgoplani/agent-deck/internal/update"
	"github.com/asheshgoplani/agent-deck/internal/web"
)

var Version = "1.7.71" // overridden at build time via -ldflags "-X main.Version=..."

// Table column widths for list command output
const (
	tableColTitle     = 20
	tableColGroup     = 15
	tableColPath      = 40
	tableColIDDisplay = 12
)

// init sets up color profile for consistent terminal colors across environments
func init() {
	initColorProfile()
	initUpdateSettings()
}

// initUpdateSettings configures update checking from user config
func initUpdateSettings() {
	settings := session.GetUpdateSettings()
	update.SetCheckInterval(settings.CheckIntervalHours)
}

// writeVersionOutput prints `Agent Deck vX.Y.Z` to `w`, appending
// ` (update available: vA.B.C)` when the on-disk cache says the user
// is behind. Offline — never touches the network. Conductor task #45.
func writeVersionOutput(w io.Writer, currentVersion string) {
	fmt.Fprintf(w, "Agent Deck v%s", currentVersion)
	info, err := update.CachedUpdateInfo(currentVersion)
	if err == nil && info != nil && info.Available {
		fmt.Fprintf(w, " (update available: v%s)", info.LatestVersion)
	}
	fmt.Fprintln(w)
}

// printUpdateNotice checks for updates and prints a one-liner if available
// Uses cache to avoid API calls - only prints if update was already detected
func printUpdateNotice() {
	settings := session.GetUpdateSettings()
	if !settings.CheckEnabled || !settings.NotifyInCLI {
		return
	}

	info, err := update.CheckForUpdate(Version, false)
	if err != nil || info == nil || !info.Available {
		return
	}

	// Print update notice to stderr so it doesn't interfere with JSON output
	fmt.Fprintf(os.Stderr, "\n💡 Update available: v%s → v%s (run: agent-deck update)\n",
		info.CurrentVersion, info.LatestVersion)
}

// promptForUpdate checks for updates and prompts user if auto_update is enabled
func promptForUpdate() bool {
	settings := session.GetUpdateSettings()
	if !settings.CheckEnabled {
		return false
	}

	info, err := update.CheckForUpdate(Version, false)
	if err != nil || info == nil || !info.Available {
		return false
	}

	// If auto_update is disabled, just show notification (don't prompt)
	if !settings.AutoUpdate {
		fmt.Fprintf(os.Stderr, "\n💡 Update available: v%s → v%s (run: agent-deck update)\n",
			info.CurrentVersion, info.LatestVersion)
		return false
	}

	// auto_update is enabled - prompt user
	fmt.Printf("\n⬆ Update available: v%s → v%s\n", info.CurrentVersion, info.LatestVersion)
	fmt.Print("Update now? [Y/n]: ")

	var response string
	_, _ = fmt.Scanln(&response)
	response = strings.TrimSpace(strings.ToLower(response))

	// Default to yes (empty or "y" or "yes")
	if response != "" && response != "y" && response != "yes" {
		fmt.Println("Skipped. Run 'agent-deck update' later.")
		return false
	}

	fmt.Println()
	if err := update.PerformUpdate(info.DownloadURL); err != nil {
		fmt.Fprintf(os.Stderr, "Update failed: %v\n", err)
		return false
	}

	fmt.Println("Restart agent-deck to use the new version.")
	return true
}

// initColorProfile configures lipgloss color profile based on terminal capabilities.
// Prefers TrueColor for best visuals, falls back to ANSI256 for compatibility.
func initColorProfile() {
	// Allow user override via environment variable
	// AGENTDECK_COLOR: truecolor, 256, 16, none
	if colorEnv := os.Getenv("AGENTDECK_COLOR"); colorEnv != "" {
		switch strings.ToLower(colorEnv) {
		case "truecolor", "true", "24bit":
			lipgloss.SetColorProfile(termenv.TrueColor)
			return
		case "256", "ansi256":
			lipgloss.SetColorProfile(termenv.ANSI256)
			return
		case "16", "ansi", "basic":
			lipgloss.SetColorProfile(termenv.ANSI)
			return
		case "none", "off", "ascii":
			lipgloss.SetColorProfile(termenv.Ascii)
			return
		}
	}

	// Auto-detect with TrueColor preference
	// Most modern terminals support TrueColor even if not advertised

	// Explicit TrueColor support
	colorTerm := os.Getenv("COLORTERM")
	if colorTerm == "truecolor" || colorTerm == "24bit" {
		lipgloss.SetColorProfile(termenv.TrueColor)
		return
	}

	// Check TERM for capability hints
	term := os.Getenv("TERM")

	// Known TrueColor-capable terminals
	trueColorTerms := []string{
		"xterm-256color",
		"screen-256color",
		"tmux-256color",
		"xterm-direct",
		"alacritty",
		"kitty",
		"wezterm",
	}
	for _, t := range trueColorTerms {
		if strings.Contains(term, t) || term == t {
			// These terminals typically support TrueColor
			lipgloss.SetColorProfile(termenv.TrueColor)
			return
		}
	}

	// Check for common terminal emulators via env vars
	// Windows Terminal, iTerm2, etc. set these
	if os.Getenv("WT_SESSION") != "" || // Windows Terminal
		os.Getenv("ITERM_SESSION_ID") != "" || // iTerm2
		os.Getenv("TERMINAL_EMULATOR") != "" || // JetBrains terminals
		os.Getenv("KONSOLE_VERSION") != "" { // Konsole
		lipgloss.SetColorProfile(termenv.TrueColor)
		return
	}

	// Fallback: Use ANSI256 for maximum compatibility
	// Works in SSH, basic terminals, and older emulators
	lipgloss.SetColorProfile(termenv.ANSI256)
}

func main() {
	// Extract global -p/--profile flag before subcommand dispatch
	profile, args := extractProfileFlag(os.Args[1:])
	if profile != "" {
		// Propagate explicit profile selection so config lookups (e.g., per-profile Claude config)
		// resolve consistently across all command paths in this process.
		_ = os.Setenv("AGENTDECK_PROFILE", profile)
	}

	// Seed the tmux socket-isolation default from `[tmux].socket_name` once
	// per process (v1.7.50+, issue #687). Package-level tmux probes
	// (KillSessionsWithEnvValue, ListAllSessions, version check, stale-
	// socket recovery) read this value to decide which tmux server to
	// target. Empty string preserves pre-v1.7.50 behavior. Per-Instance
	// calls use Instance.TmuxSocketName directly — this default is only
	// the installation-wide fallback for callers without a session handle.
	tmux.SetDefaultSocketName(session.GetTmuxSettings().GetSocketName())

	// Nudge macOS users whose tmux predates the upstream fix for the
	// control-mode NULL-deref (tmux #4980, issue #737). Once per process,
	// no-op on non-macOS, suppressible via AGENTDECK_SUPPRESS_TMUX_WARNING.
	tmux.WarnIfVulnerableTmux()

	var webEnabled bool
	var webArgs []string

	// Handle subcommands
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			writeVersionOutput(os.Stdout, Version)
			return
		case "help", "--help", "-h":
			printHelp()
			return
		case "add":
			handleAdd(profile, args[1:])
			return
		case "list", "ls":
			handleList(profile, args[1:])
			return
		case "remove", "rm":
			handleRemove(profile, args[1:])
			return
		case "rename", "mv":
			handleRename(profile, args[1:])
			return
		case "status":
			handleStatus(profile, args[1:])
			return
		case "profile":
			handleProfile(args[1:])
			return
		case "update":
			handleUpdate(args[1:])
			return
		case "session":
			handleSession(profile, args[1:])
			return
		case "mcp":
			handleMCP(profile, args[1:])
			return
		case "skill":
			handleSkill(profile, args[1:])
			return
		case "mcp-proxy":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "Usage: agent-deck mcp-proxy <socket-path>")
				os.Exit(1)
			}
			runMCPProxy(args[1])
			return
		case "group":
			handleGroup(profile, args[1:])
			return
		case "try":
			handleTry(profile, args[1:])
			return
		case "launch":
			handleLaunch(profile, args[1:])
			return
		case "conductor":
			handleConductor(profile, args[1:])
			return
		case "watcher":
			handleWatcher(profile, args[1:])
			return
		case "openclaw", "oc":
			handleOpenClaw(profile, args[1:])
			return
		case "remote":
			handleRemote(profile, args[1:])
			return
		case "worktree", "wt":
			handleWorktree(profile, args[1:])
			return
		case "costs":
			handleCosts(profile, args[1:])
			return
		case "web":
			webEnabled = true
			webArgs = append(webArgs, args[1:]...)
			// fall through to TUI launch below
		case "uninstall":
			handleUninstall(args[1:])
			return
		case "hook-handler":
			handleHookHandler()
			return
		case "codex-notify":
			handleCodexNotify()
			return
		case "hooks":
			handleHooks(args[1:])
			return
		case "codex-hooks":
			handleCodexHooks(args[1:])
			return
		case "gemini-hooks":
			handleGeminiHooks(args[1:])
			return
		case "notify-daemon":
			handleNotifyDaemon(args[1:])
			return
		case "feedback":
			handleFeedback(args[1:])
			return
		case "debug-dump":
			handleDebugDump()
			return
		}
	}

	// Startup reviver scan (v1.7.8, REPORT-D). Fire-and-forget — rebuilds
	// control pipes for any instance whose tmux server is alive but whose
	// pipe got killed by e.g. an SSH logout scope cleanup. Runs in the
	// background so it never blocks TUI boot. See .planning/v178-ssh-reviver/PLAN.md.
	go reviveOnStartup(profile)

	// Block TUI launch inside a managed session to prevent infinite nesting.
	// CLI commands (add, session start/stop, mcp attach, etc.) still work fine.
	if isNestedSession() {
		fmt.Fprintln(os.Stderr, "Error: Cannot launch the agent-deck TUI inside an agent-deck session.")
		fmt.Fprintln(os.Stderr, "This would create a recursive nested session.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "CLI commands work inside sessions. For example:")
		fmt.Fprintln(os.Stderr, "  agent-deck add /path -t \"Title\"    # Add a new session")
		fmt.Fprintln(os.Stderr, "  agent-deck session start <id>      # Start a session")
		fmt.Fprintln(os.Stderr, "  agent-deck mcp attach <id> <mcp>   # Attach MCP")
		fmt.Fprintln(os.Stderr, "  agent-deck list                    # List sessions")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "To open the TUI, detach first with Ctrl+Q.")
		os.Exit(1)
	}

	// Block TUI launch inside a *generic* (non-agentdeck) tmux session (#560).
	// Detach semantics get confusing when nested: Ctrl+Q returns to the outer
	// tmux instead of a clean shell. CLI subcommands still work inside tmux —
	// this guard only fires on the interactive TUI path.
	if isOuterTmuxWithoutOptIn() {
		fmt.Fprintln(os.Stderr, "Error: The agent-deck TUI is designed to run OUTSIDE of tmux.")
		fmt.Fprintln(os.Stderr, "You are inside a tmux session, so Ctrl+Q detach and nested")
		fmt.Fprintln(os.Stderr, "tmux behavior will be surprising. agent-deck manages its own")
		fmt.Fprintln(os.Stderr, "tmux sessions internally.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Options:")
		fmt.Fprintln(os.Stderr, "  • Detach from tmux (Ctrl+B d) and run agent-deck from a clean shell.")
		fmt.Fprintln(os.Stderr, "  • Run CLI subcommands — they work fine inside tmux:")
		fmt.Fprintln(os.Stderr, "      agent-deck list                    # List sessions")
		fmt.Fprintln(os.Stderr, "      agent-deck add /path -t \"Title\"  # Add a new session")
		fmt.Fprintln(os.Stderr, "      agent-deck session start <id>      # Start a session")
		fmt.Fprintln(os.Stderr, "  • If you really want to run the TUI anyway, set:")
		fmt.Fprintln(os.Stderr, "      AGENT_DECK_ALLOW_OUTER_TMUX=1 agent-deck")
		os.Exit(1)
	}

	// Set version for UI update checking
	ui.SetVersion(Version)

	// Initialize theme from config (resolves "system" to actual dark/light)
	theme := session.ResolveTheme()
	ui.InitTheme(theme)

	// Check for updates and prompt user before launching TUI
	if promptForUpdate() {
		// Update was performed, exit so user can restart with new version
		return
	}

	// Check if tmux is available (with fallback path search)
	if err := ensureTmuxInPath(); err != nil {
		fmt.Fprintln(os.Stderr, "Error: tmux not found")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Agent Deck requires tmux. Install with:")
		switch runtime.GOOS {
		case "darwin":
			fmt.Fprintln(os.Stderr, "  brew install tmux")
		case "linux":
			fmt.Fprintln(os.Stderr, "  sudo apt install tmux    # Debian/Ubuntu")
			fmt.Fprintln(os.Stderr, "  sudo dnf install tmux    # Fedora/RHEL")
			fmt.Fprintln(os.Stderr, "  sudo pacman -S tmux      # Arch")
		default:
			fmt.Fprintln(os.Stderr, "  See: https://github.com/tmux/tmux/wiki/Installing")
		}
		fmt.Fprintf(os.Stderr, "\nSearched PATH: %s\n", os.Getenv("PATH"))
		os.Exit(1)
	}

	// Create storage early to register instance via SQLite
	earlyStorage, err := session.NewStorageWithProfile(profile)
	if err == nil {
		if db := earlyStorage.GetDB(); db != nil {
			statedb.SetGlobal(db)
			_ = db.RegisterInstance(false)
		}
	}

	// Check if multiple instances are allowed (uses primary election as single-instance gate)
	instanceSettings := session.GetInstanceSettings()
	if !instanceSettings.GetAllowMultiple() {
		if db := statedb.GetGlobal(); db != nil {
			isFirst, electErr := db.ElectPrimary(30 * time.Second)
			if electErr == nil && !isFirst {
				fmt.Println("Error: agent-deck is already running for this profile")
				fmt.Println("Set [instances] allow_multiple = true in config.toml to allow multiple instances")
				os.Exit(1)
			}
		}
	}

	// Set up signal handling for graceful shutdown and crash dumps
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		if db := statedb.GetGlobal(); db != nil {
			_ = db.ResignPrimary()
			_ = db.UnregisterInstance()
		}
		os.Exit(0)
	}()

	// Set up structured logging (JSONL format with rotation)
	// When AGENTDECK_DEBUG is set, logs go to ~/.agent-deck/debug.log
	// When not set, logs are discarded to avoid TUI interference
	debugMode := os.Getenv("AGENTDECK_DEBUG") != ""
	if baseDir, err := session.GetAgentDeckDir(); err == nil {
		logCfg := logging.Config{
			Debug:                 debugMode,
			LogDir:                baseDir,
			Level:                 "debug",
			Format:                "json",
			MaxSizeMB:             10,
			MaxBackups:            5,
			MaxAgeDays:            10,
			Compress:              true,
			RingBufferSize:        10 * 1024 * 1024,
			AggregateIntervalSecs: 30,
		}

		// Override defaults from user config if available
		if userCfg, err := session.LoadUserConfig(); err == nil {
			ls := userCfg.Logs
			if ls.DebugLevel != "" {
				logCfg.Level = ls.DebugLevel
			}
			if ls.DebugFormat != "" {
				logCfg.Format = ls.DebugFormat
			}
			if ls.DebugMaxMB > 0 {
				logCfg.MaxSizeMB = ls.DebugMaxMB
			}
			if ls.DebugBackups > 0 {
				logCfg.MaxBackups = ls.DebugBackups
			}
			if ls.DebugRetentionDays > 0 {
				logCfg.MaxAgeDays = ls.DebugRetentionDays
			}
			if ls.DebugCompress {
				logCfg.Compress = ls.DebugCompress
			}
			if ls.RingBufferMB > 0 {
				logCfg.RingBufferSize = ls.RingBufferMB * 1024 * 1024
			}
			if ls.PprofEnabled {
				logCfg.PprofEnabled = ls.PprofEnabled
			}
			if ls.AggregateIntervalS > 0 {
				logCfg.AggregateIntervalSecs = ls.AggregateIntervalS
			}
		}

		logging.Init(logCfg)
		defer logging.Shutdown()

		// OBS-01: emit the cgroup-isolation decision exactly once on TUI
		// startup. The line lands in ~/.agent-deck/debug.log via the
		// dynamicHandler + lumberjack pipeline that logging.Init wires up.
		// See internal/session/userconfig.go LogCgroupIsolationDecision.
		session.LogCgroupIsolationDecision()

		if debugMode {
			logging.ForComponent(logging.CompUI).Info("instance_started",
				slog.Int("pid", os.Getpid()))
		}

		// SIGUSR1 dumps the ring buffer for post-mortem debugging
		usr1Chan := make(chan os.Signal, 1)
		signal.Notify(usr1Chan, syscall.SIGUSR1)
		go func() {
			for range usr1Chan {
				dumpPath := filepath.Join(baseDir, fmt.Sprintf("crash-dump-%d.jsonl", time.Now().Unix()))
				if err := logging.DumpRingBuffer(dumpPath); err != nil {
					logging.ForComponent(logging.CompUI).Error("crash_dump_failed",
						slog.String("error", err.Error()))
				} else {
					logging.ForComponent(logging.CompUI).Info("crash_dump_written",
						slog.String("path", dumpPath))
				}
			}
		}()
	}

	// Extract --group / -g flag here (TUI-only path; subcommands consume their own -g)
	var groupScope string
	groupScope, args = extractGroupFlag(args)
	// Extract --select flag (#709): preselect a session without scoping groups.
	var initialSelect string
	initialSelect, _ = extractSelectFlag(args)

	// v1.7.41: record TUI launch for feedback-prompt pacing. Seeds
	// FirstSeenAt on the very first launch and bumps LaunchCount on every
	// subsequent launch, so feedback.ShouldShow can enforce the min-days +
	// min-launches threshold for new users. Non-TUI subcommands (add, list,
	// feedback, etc.) deliberately skip this so scripted usage doesn't
	// inflate the counter.
	if fbSt, _ := feedback.LoadState(); fbSt != nil {
		feedback.RecordLaunch(fbSt, time.Now())
		_ = feedback.SaveState(fbSt)
	}

	// Start TUI with the specified profile
	homeModel := ui.NewHomeWithProfileAndMode(profile)
	// Apply group scope if specified via --group / -g flag
	if groupScope != "" {
		normalizedGroup := normalizeGroupPath(groupScope)
		// Validate group exists by loading current sessions
		if storage, err := session.NewStorageWithProfile(profile); err == nil {
			if _, groups, err := storage.LoadWithGroups(); err == nil {
				groupTree := session.NewGroupTreeWithGroups(nil, groups)
				if _, exists := groupTree.Groups[normalizedGroup]; !exists {
					fmt.Fprintf(os.Stderr, "Error: group '%s' not found\n", groupScope)
					os.Exit(2)
				}
			} else {
				fmt.Fprintf(os.Stderr, "Warning: could not verify group '%s' (storage error)\n", groupScope)
			}
		} else {
			fmt.Fprintf(os.Stderr, "Warning: could not verify group '%s' (storage error)\n", groupScope)
		}
		homeModel.SetGroupScope(normalizedGroup)
	}
	// Apply preselection if specified via --select (#709).
	// When both -g and --select are given, the preselect runs AFTER the group
	// scope is applied: Home.applyInitialSelection will fail silently if the
	// session is outside the scope; we pre-warn here so the user sees both
	// outputs without digging through logs.
	if initialSelect != "" {
		homeModel.SetInitialSelection(initialSelect)
		if groupScope != "" {
			if storage, err := session.NewStorageWithProfile(profile); err == nil {
				if instances, _, err := storage.LoadWithGroups(); err == nil {
					normalizedGroup := normalizeGroupPath(groupScope)
					found := false
					for _, inst := range instances {
						if inst == nil {
							continue
						}
						if inst.ID != initialSelect && !strings.EqualFold(inst.Title, initialSelect) {
							continue
						}
						gp := inst.GroupPath
						if gp == normalizedGroup || strings.HasPrefix(gp, normalizedGroup+"/") {
							found = true
						}
						break
					}
					if !found {
						fmt.Fprintf(os.Stderr, "Warning: --select %q is not in group %q; cursor will not be repositioned\n", initialSelect, groupScope)
					}
				}
			}
		}
	}

	// ═══════════════════════════════════════════════════════════════════
	// Cost Tracking Initialization
	// ═══════════════════════════════════════════════════════════════════
	var costStore *costs.Store
	if db := statedb.GetGlobal(); db != nil {
		costStore = costs.NewStore(db.DB())

		// Load user config for pricing overrides and budgets
		userCfg, _ := session.LoadUserConfig()

		// Set up pricer with overrides
		homeDir, _ := os.UserHomeDir()
		cacheDir := filepath.Join(homeDir, ".agent-deck")
		pricerCfg := costs.PricerConfig{CachePath: cacheDir}
		if userCfg != nil && len(userCfg.Costs.Pricing.Overrides) > 0 {
			pricerCfg.Overrides = make(map[string]costs.PriceOverride)
			for model, ov := range userCfg.Costs.Pricing.Overrides {
				pricerCfg.Overrides[model] = costs.PriceOverride{
					InputPerMtok:      ov.InputPerMtok,
					OutputPerMtok:     ov.OutputPerMtok,
					CacheReadPerMtok:  ov.CacheReadPerMtok,
					CacheWritePerMtok: ov.CacheWritePerMtok,
				}
			}
		}
		pricer := costs.NewPricer(pricerCfg)
		_ = pricer.LoadCache()

		// Start daily price fetcher
		fetchCtx, fetchCancel := context.WithCancel(context.Background())
		defer fetchCancel()
		fetcher := &costs.Fetcher{CachePath: filepath.Join(cacheDir, "pricing.json"), Pricer: pricer}
		go fetcher.StartDaily(fetchCtx)

		// Set up budget checker
		var budgetCfg costs.BudgetConfig
		if userCfg != nil {
			bc := userCfg.Costs.Budgets
			budgetCfg.DailyLimit = int64(math.Round(bc.DailyLimit * 1_000_000))
			budgetCfg.WeeklyLimit = int64(math.Round(bc.WeeklyLimit * 1_000_000))
			budgetCfg.MonthlyLimit = int64(math.Round(bc.MonthlyLimit * 1_000_000))
			if len(bc.Groups) > 0 {
				budgetCfg.GroupLimits = make(map[string]int64)
				for name, g := range bc.Groups {
					budgetCfg.GroupLimits[name] = int64(math.Round(g.DailyLimit * 1_000_000))
				}
			}
		}
		budgetChecker := costs.NewBudgetChecker(budgetCfg, costStore)

		// Wire into TUI
		homeModel.SetCostStore(costStore)
		homeModel.SetCostPricer(pricer)
		homeModel.SetCostBudget(budgetChecker)

		// Start cost event watcher (for Claude hook events)
		costEventsDir := filepath.Join(homeDir, ".agent-deck", "cost-events")
		costWatcher, watchErr := costs.NewCostEventWatcher(costEventsDir)
		if watchErr == nil {
			go costWatcher.Start()
			defer costWatcher.Stop()

			// Process incoming cost events from hooks
			go func() {
				for raw := range costWatcher.EventCh() {
					ev := costs.CostEvent{
						ID:               fmt.Sprintf("%s_%d", raw.InstanceID, raw.Timestamp),
						SessionID:        raw.InstanceID,
						Timestamp:        time.Unix(0, raw.Timestamp),
						Model:            raw.Model,
						InputTokens:      raw.InputTokens,
						OutputTokens:     raw.OutputTokens,
						CacheReadTokens:  raw.CacheReadTokens,
						CacheWriteTokens: raw.CacheWriteTokens,
						CostMicrodollars: pricer.ComputeCost(raw.Model, raw.InputTokens, raw.OutputTokens, raw.CacheReadTokens, raw.CacheWriteTokens),
					}
					_ = costStore.WriteCostEvent(ev)
				}
			}()
		}

		// Run retention cleanup on startup
		if userCfg != nil {
			retDays := userCfg.Costs.GetRetentionDays()
			if retDays > 0 {
				_, _ = costStore.PurgeOlderThan(retDays)
			}
		}
	}

	// Start web server alongside TUI if "web" subcommand was used
	if webEnabled {
		effectiveProfile := session.GetEffectiveProfile(profile)
		fallbackMenuData := web.NewSessionDataService(effectiveProfile)
		liveMenuData := web.NewMemoryMenuData(fallbackMenuData)
		homeModel.SetWebMenuData(liveMenuData)

		server, err := buildWebServer(effectiveProfile, webArgs, liveMenuData)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: web server setup failed: %v\n", err)
			os.Exit(1)
		}
		if costStore != nil {
			server.SetCostStore(costStore)
		}
		go func() {
			if err := server.Start(); err != nil {
				logging.ForComponent(logging.CompWeb).Error("web_server_error",
					slog.String("error", err.Error()))
			}
		}()
		fmt.Printf("Web server: http://%s\n", server.Addr())
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
		}()
	}

	// Disable the Kitty keyboard protocol before starting the TUI.
	// Wayland terminals (Ghostty, Foot, Alacritty) send keys using CSI u
	// encoding by default; Bubble Tea v1.3.10 does not parse those sequences,
	// so uppercase shortcuts and uppercase text input (including '_') are
	// silently dropped. Pushing keyboard mode 0 (legacy) restores standard
	// key reporting. Terminals that don't support the protocol ignore this
	// sequence safely.
	//
	// As a belt-and-suspenders fallback, we also wrap os.Stdin with
	// NewCSIuReader, which translates any remaining CSI u sequences (including
	// Shift+hyphen → '_', codepoint 95) to their legacy byte equivalents
	// before Bubble Tea sees them. This handles terminals that send CSI u
	// sequences even after the disable request (e.g. tmux with extended-keys).
	ui.DisableKittyKeyboard(os.Stdout)
	defer ui.RestoreKittyKeyboard(os.Stdout)

	p := tea.NewProgram(
		homeModel,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithInput(ui.NewCSIuReader(os.Stdin)),
	)

	// Start maintenance worker (background goroutine, respects config toggle)
	maintenanceCtx, maintenanceCancel := context.WithCancel(context.Background())
	defer maintenanceCancel()
	session.StartMaintenanceWorker(maintenanceCtx, func(result session.MaintenanceResult) {
		p.Send(ui.MaintenanceCompleteMsg{Result: result})
	})

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}

// extractProfileFlag extracts -p or --profile from args, returning the profile and remaining args
func extractProfileFlag(args []string) (string, []string) {
	var profile string
	var remaining []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Check for -p=value or --profile=value
		if strings.HasPrefix(arg, "-p=") {
			profile = strings.TrimPrefix(arg, "-p=")
			continue
		}
		if strings.HasPrefix(arg, "--profile=") {
			profile = strings.TrimPrefix(arg, "--profile=")
			continue
		}

		// Check for -p value or --profile value
		if arg == "-p" || arg == "--profile" {
			if i+1 < len(args) {
				profile = args[i+1]
				i++ // Skip the value
				continue
			}
		}

		remaining = append(remaining, arg)
	}

	return profile, remaining
}

// extractGroupFlag extracts -g or --group from args, returning the group path and remaining args.
// This only applies to the TUI launch path; subcommands like add/launch have their own -g flag.
func extractGroupFlag(args []string) (string, []string) {
	var group string
	var remaining []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Check for -g=value or --group=value
		if strings.HasPrefix(arg, "-g=") {
			group = strings.TrimPrefix(arg, "-g=")
			continue
		}
		if strings.HasPrefix(arg, "--group=") {
			group = strings.TrimPrefix(arg, "--group=")
			continue
		}

		// Check for -g value or --group value
		if arg == "-g" || arg == "--group" {
			if i+1 < len(args) {
				group = args[i+1]
				i++ // Skip the value
				continue
			}
		}

		remaining = append(remaining, arg)
	}

	return group, remaining
}

// extractSelectFlag extracts --select <session-id-or-title> from args (#709).
// Unlike -g / --group, --select does NOT scope the TUI to one group — it only
// positions the cursor on a matching session while keeping every group visible.
func extractSelectFlag(args []string) (string, []string) {
	var selectVal string
	var remaining []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if strings.HasPrefix(arg, "--select=") {
			selectVal = strings.TrimPrefix(arg, "--select=")
			continue
		}
		if arg == "--select" {
			if i+1 < len(args) {
				selectVal = args[i+1]
				i++
				continue
			}
		}

		remaining = append(remaining, arg)
	}

	return selectVal, remaining
}

// reorderArgsForFlagParsing moves the path argument to the end of args
// so Go's flag package can parse all flags correctly.
// Go's flag package stops parsing at the first non-flag argument,
// so "add . -c claude" would fail to parse -c without this fix.
// This reorders to "add -c claude ." which parses correctly.
func reorderArgsForFlagParsing(args []string) []string {
	if len(args) == 0 {
		return args
	}

	// Known flags that take a value (need to skip their values)
	// Note: -b/--new-branch are boolean flags (no value), so not included here
	valueFlags := map[string]bool{
		"-t": true, "--title": true,
		"-g": true, "--group": true,
		"-c": true, "--cmd": true,
		"-m": true, "--message": true,
		"-p": true, "--parent": true,
		"--mcp":       true,
		"--channel":   true,
		"--extra-arg": true,
		"--wrapper":   true,
		"-w":          true, "--worktree": true,
		"--location":       true,
		"--resume-session": true,
		"--sandbox-image":  true,
		"--ssh":            true,
		"--remote-path":    true,
	}

	var flags []string
	var positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Check if it's a flag
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)

			// Check if this flag takes a value (and value is separate)
			// Handle both "-c value" and "-c=value" formats
			if !strings.Contains(arg, "=") && valueFlags[arg] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			// Non-flag argument (path)
			positional = append(positional, arg)
		}
	}

	// Return flags first, then positional args
	return append(flags, positional...)
}

// isDuplicateSession checks if a session with the same title AND path already exists.
// Returns (isDuplicate, existingInstance)
// Paths are normalized by removing trailing slashes for comparison.
func isDuplicateSession(instances []*session.Instance, title, path string) (bool, *session.Instance) {
	// Normalize path by removing trailing slash (except for root "/")
	normalizedPath := strings.TrimSuffix(path, "/")
	if normalizedPath == "" {
		normalizedPath = "/"
	}

	for _, inst := range instances {
		// Normalize existing path for comparison
		existingPath := strings.TrimSuffix(inst.ProjectPath, "/")
		if existingPath == "" {
			existingPath = "/"
		}

		if existingPath == normalizedPath && inst.Title == title {
			return true, inst
		}
	}
	return false, nil
}

// generateUniqueTitle generates a unique title for sessions at the same path.
// If "project" exists at path, returns "project (2)", then "project (3)", etc.
func generateUniqueTitle(instances []*session.Instance, baseTitle, path string) string {
	// Check if base title is available at this path
	titleExists := func(title string) bool {
		for _, inst := range instances {
			if inst.ProjectPath == path && inst.Title == title {
				return true
			}
		}
		return false
	}

	if !titleExists(baseTitle) {
		return baseTitle
	}

	// Find next available number
	for i := 2; i <= 100; i++ { // Cap at 100 to prevent infinite loop
		candidate := fmt.Sprintf("%s (%d)", baseTitle, i)
		if !titleExists(candidate) {
			return candidate
		}
	}

	// Fallback: use timestamp
	return fmt.Sprintf("%s (%d)", baseTitle, time.Now().Unix())
}

// isWorktreeAlreadyExistsError detects whether git worktree creation failed because
// the destination path already exists. This preserves friendly UX while avoiding
// TOCTOU race windows from separate filesystem pre-checks.
func isWorktreeAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}

func resolveAutoParentInstance(instances []*session.Instance) *session.Instance {
	candidates := []string{
		strings.TrimSpace(os.Getenv("AGENT_DECK_SESSION_ID")),
		strings.TrimSpace(os.Getenv("AGENTDECK_INSTANCE_ID")),
	}

	if tmuxCurrent := strings.TrimSpace(GetCurrentSessionID()); tmuxCurrent != "" {
		candidates = append(candidates, tmuxCurrent)
	}

	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		if inst, _, _ := ResolveSession(candidate, instances); inst != nil {
			return inst
		}
	}
	return nil
}

// resolveGroupPathForAdd resolves a user-provided group selector to a stored group path.
// It accepts exact paths, normalized paths, and case-insensitive group display names.
func resolveGroupPathForAdd(groupTree *session.GroupTree, groupSelector string) string {
	if groupTree == nil || groupSelector == "" {
		return groupSelector
	}

	if _, exists := groupTree.Groups[groupSelector]; exists {
		return groupSelector
	}

	normalized := strings.ToLower(strings.ReplaceAll(groupSelector, " ", "-"))
	if _, exists := groupTree.Groups[normalized]; exists {
		return normalized
	}

	for path, group := range groupTree.Groups {
		if strings.EqualFold(group.Name, groupSelector) {
			return path
		}
	}

	return groupSelector
}

// handleAdd adds a new session from CLI
func handleAdd(profile string, args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	title := fs.String("title", "", "Session title (defaults to folder name)")
	titleShort := fs.String("t", "", "Session title (short)")
	group := fs.String("group", "", "Group path (defaults to parent folder)")
	groupShort := fs.String("g", "", "Group path (short)")
	command := fs.String("cmd", "", "Tool/command to run (e.g., 'claude' or 'codex --dangerously-bypass-approvals-and-sandbox')")
	commandShort := fs.String("c", "", "Tool/command to run (short)")
	wrapper := fs.String(
		"wrapper",
		"",
		"Wrapper command (use {command} to include tool command, e.g., 'nvim +\"terminal {command}\"')",
	)
	parent := fs.String("parent", "", "Parent session (creates sub-session, inherits group)")
	parentShort := fs.String("p", "", "Parent session (short)")
	noParent := fs.Bool("no-parent", false, "Disable automatic parent linking (use 'session set-parent' later to link manually)")
	noTransitionNotify := fs.Bool("no-transition-notify", false, "Suppress transition event notifications to parent session")
	// #697: conductor-friendly title lock. When set, Claude's session name
	// (--name / /rename) never overwrites the agent-deck title. --no-title-sync
	// is an alias for discoverability.
	titleLock := fs.Bool("title-lock", false, "Lock session title so Claude's session name never overrides it (#697)")
	noTitleSync := fs.Bool("no-title-sync", false, "Alias for --title-lock")
	quickCreate := fs.Bool("quick", false, "Auto-generate session name (adjective-noun)")
	quickCreateShort := fs.Bool("Q", false, "Auto-generate session name (short)")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	// Worktree flags
	worktreeBranch := fs.String("w", "", "Create session in git worktree for branch")
	worktreeBranchLong := fs.String("worktree", "", "Create session in git worktree for branch")
	newBranch := fs.Bool("b", false, "Create new branch (use with --worktree)")
	newBranchLong := fs.Bool("new-branch", false, "Create new branch")
	worktreeLocation := fs.String("location", "", "Worktree location: sibling, subdirectory, or custom path")

	// MCP flag - can be specified multiple times
	var mcpFlags []string
	fs.Func("mcp", "MCP to attach (can specify multiple times)", func(s string) error {
		mcpFlags = append(mcpFlags, s)
		return nil
	})

	// Plugin channel flag - can be specified multiple times; requires -c claude.
	// Persisted on Instance.Channels and emitted as --channels <csv> on every
	// claude Start/Restart so plugin channels deliver inbound messages.
	var channelFlags []string
	fs.Func("channel", "Plugin channel id (can specify multiple times); requires -c claude", func(s string) error {
		channelFlags = append(channelFlags, s)
		return nil
	})

	// Extra claude CLI tokens - repeatable; each invocation is one already-
	// tokenised arg (e.g. --extra-arg --agent --extra-arg reviewer).
	// Persisted on Instance.ExtraArgs (plaintext — do NOT pass secrets) and
	// appended verbatim to every claude Start/Restart/Fork command via
	// buildClaudeExtraFlags.
	var extraArgFlags []string
	fs.Func("extra-arg", "Extra claude CLI token (can specify multiple times); requires -c claude; persisted plaintext — no secrets", func(s string) error {
		extraArgFlags = append(extraArgFlags, s)
		return nil
	})

	// Sandbox flags
	sandbox := fs.Bool("sandbox", false, "Run session in Docker sandbox")
	sandboxImage := fs.String("sandbox-image", "", "Docker image for sandbox (overrides config default)")

	// SSH remote flags
	sshHost := fs.String("ssh", "", "SSH destination (e.g., user@host)")
	sshRemotePath := fs.String("remote-path", "", "Remote working directory (used with --ssh)")

	// Resume session flag
	resumeSession := fs.String("resume-session", "", "Claude session ID to resume (skips new session creation)")
	yoloMode := fs.Bool("yolo", false, "Enable YOLO mode for Gemini or Codex sessions")
	geminiYoloMode := fs.Bool("gemini-yolo", false, "Enable YOLO mode (alias for --yolo)")

	// Socket isolation (v1.7.50+, issue #687). Overrides the installation-
	// wide `[tmux].socket_name` for this one session. Empty = fall back to
	// config. Captured once at creation and persisted on the Instance —
	// subsequent start/restart/revive always target the same socket.
	tmuxSocket := fs.String("tmux-socket", "", "tmux -L socket name for this session (overrides [tmux].socket_name)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck add [path] [options]")
		fmt.Println()
		fmt.Println("Add a new session to Agent Deck.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  [path]    Project directory (defaults to current directory)")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck add                       # Use current directory")
		fmt.Println("  agent-deck add /path/to/project")
		fmt.Println("  agent-deck add -t \"My Project\" -g \"work\"")
		fmt.Println("  agent-deck add -c claude .")
		fmt.Println("  agent-deck -p work add               # Add to 'work' profile")
		fmt.Println("  agent-deck add -t \"Sub-task\" --parent \"Main Project\"  # Create sub-session")
		fmt.Println("  agent-deck add -t \"Research\" -c claude --mcp memory --mcp sequential-thinking /tmp/x")
		fmt.Println("  agent-deck add -t \"Bot\" -c claude --channel plugin:telegram@user/repo .  # subscribe to plugin channel")
		fmt.Println("  agent-deck add -c opencode --wrapper \"nvim +'terminal {command}' +'startinsert'\" .")
		fmt.Println("  agent-deck add -c \"codex --dangerously-bypass-approvals-and-sandbox\" .")
		fmt.Println("  agent-deck add -c gemini --yolo .")
		fmt.Println("  agent-deck add -c claude -g work .   # -c is shorthand for --cmd")
		fmt.Println("  agent-deck add -g ard --no-parent -c claude .")
		fmt.Println("  agent-deck add --quick -c claude .   # Auto-generated name")
		fmt.Println()
		fmt.Println("Worktree Examples:")
		fmt.Println("  agent-deck add -w feature/login .    # Create worktree for existing branch")
		fmt.Println("  agent-deck add -w feature/new -b .   # Create worktree with new branch")
		fmt.Println("  agent-deck add --worktree fix/bug-123 --new-branch /path/to/repo")
		fmt.Println()
		fmt.Println("SSH Examples:")
		fmt.Println("  agent-deck add --ssh user@host --remote-path ~/project -c claude")
		fmt.Println("  agent-deck add --ssh user@host -c claude -t \"remote-dev\"")
	}

	// Reorder args: move path to end so flags are parsed correctly
	// Go's flag package stops parsing at first non-flag argument
	// This allows: "add . -c claude" to work same as "add -c claude ."
	args = reorderArgsForFlagParsing(args)

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	// Path argument is optional; if omitted with -g/--group, we'll try group default_path.
	// Fix: sanitize input to remove surrounding quotes that cause issues.
	rawPathArg := strings.Trim(fs.Arg(0), "'\"")
	explicitPathProvided := rawPathArg != ""
	path := ""

	// Resolve worktree flags
	wtBranch := *worktreeBranch
	if *worktreeBranchLong != "" {
		wtBranch = *worktreeBranchLong
	}
	createNewBranch := *newBranch || *newBranchLong

	// Merge short and long flags
	sessionTitle := mergeFlags(*title, *titleShort)
	sessionGroup := mergeFlags(*group, *groupShort)
	explicitGroupProvided := strings.TrimSpace(sessionGroup) != ""
	sessionCommandInput := mergeFlags(*command, *commandShort)
	sessionCommandTool, sessionCommandResolved, sessionWrapperResolved, sessionCommandNote := resolveSessionCommand(sessionCommandInput, *wrapper)
	sessionParent := mergeFlags(*parent, *parentShort)
	if sessionParent != "" && *noParent {
		fmt.Println("Error: --parent and --no-parent cannot be used together")
		os.Exit(1)
	}

	// Validate --resume-session requires Claude
	if *resumeSession != "" {
		tool := firstNonEmpty(sessionCommandTool, detectTool(sessionCommandInput))
		if tool != "claude" {
			fmt.Println("Error: --resume-session only works with Claude sessions (-c claude)")
			os.Exit(1)
		}
	}

	// Load existing sessions with profile
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		fmt.Printf("Error: failed to initialize storage: %v\n", err)
		os.Exit(1)
	}

	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		fmt.Printf("Error: failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	groupTree := session.NewGroupTreeWithGroups(instances, groups)

	// Resolve parent session if specified
	var parentInstance *session.Instance
	if sessionParent != "" {
		var errMsg string
		parentInstance, errMsg, _ = ResolveSession(sessionParent, instances)
		if parentInstance == nil {
			fmt.Printf("Error: %s\n", errMsg)
			os.Exit(1)
			return // unreachable, satisfies staticcheck SA5011
		}
		// Sub-sessions cannot have sub-sessions (single level only)
		if parentInstance.IsSubSession() {
			fmt.Printf("Error: cannot create sub-session of a sub-session (single level only)\n")
			os.Exit(1)
		}
		sessionGroup = resolveGroupSelection(sessionGroup, parentInstance.GroupPath, explicitGroupProvided)
	} else if !*noParent {
		parentInstance = resolveAutoParentInstance(instances)
		if parentInstance != nil && !parentInstance.IsSubSession() {
			sessionGroup = resolveGroupSelection(sessionGroup, parentInstance.GroupPath, explicitGroupProvided)
		} else {
			parentInstance = nil
		}
	}

	// Resolve group selector to a canonical path when possible.
	if sessionGroup != "" {
		sessionGroup = resolveGroupPathForAdd(groupTree, sessionGroup)
	}

	if explicitPathProvided {
		if rawPathArg == "." {
			path, err = os.Getwd()
			if err != nil {
				fmt.Printf("Error: failed to get current directory: %v\n", err)
				os.Exit(1)
			}
		} else {
			path, err = filepath.Abs(rawPathArg)
			if err != nil {
				fmt.Printf("Error: failed to resolve path: %v\n", err)
				os.Exit(1)
			}
		}
	} else {
		// No explicit path provided: use group default path first, then cwd fallback.
		if sessionGroup != "" {
			path = groupTree.DefaultPathForGroup(sessionGroup)
		}
		if path == "" {
			path, err = os.Getwd()
			if err != nil {
				fmt.Printf("Error: failed to get current directory: %v\n", err)
				os.Exit(1)
			}
		}
	}

	// Verify path exists and is a directory (skip for SSH remote sessions)
	if *sshHost != "" {
		// For SSH sessions, use CWD as local placeholder path (project lives on remote)
		if path == "" {
			path, err = os.Getwd()
			if err != nil {
				fmt.Printf("Error: failed to get current directory: %v\n", err)
				os.Exit(1)
			}
		}
	} else {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Printf("Error: path does not exist: %s\n", path)
			os.Exit(1)
		}
		if !info.IsDir() {
			fmt.Printf("Error: path is not a directory: %s\n", path)
			os.Exit(1)
		}
	}

	// Handle worktree creation
	var worktreePath, worktreeRepoRoot string
	if wtBranch != "" {
		// Validate path is a git repo (or a bare-repo project root with nested .bare/)
		if !git.IsGitRepoOrBareProjectRoot(path) {
			fmt.Fprintf(os.Stderr, "Error: %s is not a git repository\n", path)
			os.Exit(1)
		}

		// Get repo root (resolve through worktrees to prevent nesting)
		repoRoot, err := git.GetWorktreeBaseRoot(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to get repo root: %v\n", err)
			os.Exit(1)
		}

		// Determine worktree settings and apply configured branch prefix
		// (e.g., "$USER/" -> "dani.fernandez/") before validation/existence checks
		wtSettings := session.GetWorktreeSettings()
		wtBranch = wtSettings.ApplyBranchPrefix(wtBranch)

		// Pre-validate branch name for better error messages
		if err := git.ValidateBranchName(wtBranch); err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid branch name: %v\n", err)
			os.Exit(1)
		}

		// Check -b flag logic: if -b is passed, branch must NOT exist (user wants new branch)
		branchExists := git.BranchExists(repoRoot, wtBranch)
		if createNewBranch && branchExists {
			fmt.Fprintf(
				os.Stderr,
				"Error: branch '%s' already exists (remove -b flag to use existing branch)\n",
				wtBranch,
			)
			os.Exit(1)
		}

		location := wtSettings.DefaultLocation
		if *worktreeLocation != "" {
			location = *worktreeLocation
		}

		// Generate worktree path
		worktreePath = git.WorktreePath(git.WorktreePathOptions{
			Branch:    wtBranch,
			Location:  location,
			RepoDir:   repoRoot,
			SessionID: git.GeneratePathID(),
			Template:  wtSettings.Template(),
		})

		// Check for an existing worktree for this branch before creating a new one
		if existingPath, err := git.GetWorktreeForBranch(repoRoot, wtBranch); err == nil && existingPath != "" {
			fmt.Fprintf(os.Stderr, "Reusing existing worktree at %s for branch %s\n", existingPath, wtBranch)
			worktreePath = existingPath
		} else {
			// Ensure parent directory exists (needed for subdirectory mode)
			if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to create parent directory: %v\n", err)
				os.Exit(1)
			}

			// Create worktree atomically (git handles existence checks).
			// This avoids a TOCTOU race from separate check-then-create steps.
			setupErr, err := git.CreateWorktreeWithSetup(repoRoot, worktreePath, wtBranch, os.Stdout, os.Stderr, session.GetWorktreeSettings().SetupTimeout())
			if err != nil {
				if isWorktreeAlreadyExistsError(err) {
					fmt.Fprintf(os.Stderr, "Error: worktree already exists at %s\n", worktreePath)
					fmt.Fprintf(os.Stderr, "Tip: Use 'agent-deck add %s' to add the existing worktree\n", worktreePath)
					os.Exit(1)
				}
				fmt.Fprintf(os.Stderr, "Error: failed to create worktree: %v\n", err)
				os.Exit(1)
			}
			if setupErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: worktree setup script failed: %v\n", setupErr)
			}

			fmt.Printf("Created worktree at: %s\n", worktreePath)
		}
		worktreeRepoRoot = repoRoot
		// Update path to point to worktree so session uses worktree as working directory
		path = worktreePath
	}

	// Default title to folder name
	if sessionTitle == "" {
		sessionTitle = filepath.Base(path)
	}

	// Track if user provided explicit title or we auto-generated from folder name
	userProvidedTitle := (mergeFlags(*title, *titleShort) != "")
	isQuick := *quickCreate || *quickCreateShort

	if isQuick && !userProvidedTitle {
		// Quick mode: use auto-generated adjective-noun name
		sessionTitle = session.GenerateUniqueSessionName(instances, sessionGroup)
	} else if !userProvidedTitle {
		// User didn't provide title - auto-generate unique title for this path
		sessionTitle = generateUniqueTitle(instances, sessionTitle, path)
	} else {
		// User provided explicit title - check for exact duplicate (same title AND path)
		if isDupe, existingInst := isDuplicateSession(instances, sessionTitle, path); isDupe {
			fmt.Printf("Session already exists with same title and path: %s (%s)\n", existingInst.Title, existingInst.ID)
			os.Exit(0)
		}
	}

	// Create new instance (without starting tmux)
	var newInstance *session.Instance
	if sessionGroup != "" {
		newInstance = session.NewInstanceWithGroup(sessionTitle, path, sessionGroup)
	} else {
		newInstance = session.NewInstance(sessionTitle, path)
	}

	// Socket-isolation CLI override (issue #687 phase 1, v1.7.50). The
	// `--tmux-socket` flag beats `[tmux].socket_name`. Whitespace-only
	// values fall back to the config default via the GetSocketName trim
	// logic already applied during NewInstance, so we only override when
	// the user typed something non-empty.
	if flagSocket := strings.TrimSpace(*tmuxSocket); flagSocket != "" {
		newInstance.TmuxSocketName = flagSocket
		if ts := newInstance.GetTmuxSession(); ts != nil {
			ts.SocketName = flagSocket
		}
	}

	// Set parent if specified (includes parent's project path for --add-dir access)
	if parentInstance != nil {
		newInstance.SetParentWithPath(parentInstance.ID, parentInstance.ProjectPath)
	}

	// Suppress transition notifications if requested
	if *noTransitionNotify {
		newInstance.NoTransitionNotify = true
	}

	// #697: title-lock blocks Claude's session-name sync. Either flag triggers it.
	if *titleLock || *noTitleSync {
		newInstance.TitleLocked = true
	}

	// Set command if provided
	if sessionCommandInput != "" {
		newInstance.Tool = firstNonEmpty(sessionCommandTool, detectTool(sessionCommandInput))
		newInstance.Command = sessionCommandResolved
	}

	// Apply --channel flags (claude only — channels is a Claude Code CLI flag).
	if len(channelFlags) > 0 {
		if newInstance.Tool != "claude" {
			fmt.Println("Error: --channel only supported for claude sessions (use -c claude); requires --channels on the claude binary")
			os.Exit(1)
		}
		newInstance.Channels = channelFlags
	}

	// Apply --extra-arg flags (claude only for now — these are passed to the
	// claude binary via buildClaudeExtraFlags; other tools have their own builders).
	if len(extraArgFlags) > 0 {
		if newInstance.Tool != "claude" {
			fmt.Println("Error: --extra-arg only supported for claude sessions (use -c claude); claude is the only tool whose builder appends user extra args")
			os.Exit(1)
		}
		newInstance.ExtraArgs = extraArgFlags
	}

	// Set wrapper if provided
	if sessionWrapperResolved != "" {
		newInstance.Wrapper = sessionWrapperResolved
	}

	// Set worktree fields if created
	if worktreePath != "" {
		newInstance.WorktreePath = worktreePath
		newInstance.WorktreeRepoRoot = worktreeRepoRoot
		newInstance.WorktreeBranch = wtBranch
	}

	// Apply sandbox config if requested.
	if *sandbox {
		newInstance.Sandbox = session.NewSandboxConfig(*sandboxImage)
	}

	// Apply SSH remote config if requested.
	if *sshHost != "" {
		if *sandbox {
			fmt.Println("Error: --ssh and --sandbox cannot be used together")
			os.Exit(1)
		}
		newInstance.SSHHost = *sshHost
		newInstance.SSHRemotePath = *sshRemotePath
	}

	// Handle --resume-session: set Claude session ID and resume mode
	if *resumeSession != "" {
		newInstance.ClaudeSessionID = *resumeSession
		newInstance.ClaudeDetectedAt = time.Now()

		opts := newInstance.GetClaudeOptions()
		if opts == nil {
			userConfig, _ := session.LoadUserConfig()
			opts = session.NewClaudeOptions(userConfig)
		}
		opts.SessionMode = "resume"
		opts.ResumeSessionID = *resumeSession
		if err := newInstance.SetClaudeOptions(opts); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to set resume options: %v\n", err)
		}
	}

	if err := applyCLIYoloOverride(newInstance, *yoloMode || *geminiYoloMode); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	// Add to instances
	instances = append(instances, newInstance)

	// Rebuild group tree and save
	groupTree = session.NewGroupTreeWithGroups(instances, groups)
	// Ensure the session's group exists
	if newInstance.GroupPath != "" {
		groupTree.CreateGroup(newInstance.GroupPath)
	}

	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		fmt.Printf("Error: failed to save session: %v\n", err)
		os.Exit(1)
	}

	// Attach MCPs if specified
	if len(mcpFlags) > 0 {
		// Validate MCPs exist in config.toml
		availableMCPs := session.GetAvailableMCPs()
		for _, mcpName := range mcpFlags {
			if _, exists := availableMCPs[mcpName]; !exists {
				fmt.Printf("Error: MCP '%s' not found in config.toml\n", mcpName)
				fmt.Println("\nAvailable MCPs:")
				for name := range availableMCPs {
					fmt.Printf("  • %s\n", name)
				}
				os.Exit(1)
			}
		}

		// Write MCPs to .mcp.json
		if err := session.WriteMCPJsonFromConfig(path, mcpFlags); err != nil {
			fmt.Printf("Error: failed to write MCPs: %v\n", err)
			os.Exit(1)
		}
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Build human-readable output
	var humanLines []string
	humanLines = append(humanLines, fmt.Sprintf("Added session: %s", sessionTitle))
	humanLines = append(humanLines, fmt.Sprintf("  Profile: %s", storage.Profile()))
	humanLines = append(humanLines, fmt.Sprintf("  Path:    %s", path))
	humanLines = append(humanLines, fmt.Sprintf("  Group:   %s", newInstance.GroupPath))
	humanLines = append(humanLines, fmt.Sprintf("  ID:      %s", newInstance.ID))
	if sessionCommandInput != "" {
		humanLines = append(humanLines, fmt.Sprintf("  Cmd:     %s", sessionCommandInput))
		if newInstance.Wrapper != "" {
			humanLines = append(humanLines, fmt.Sprintf("  Wrapper: %s", newInstance.Wrapper))
		}
		if sessionCommandNote != "" {
			humanLines = append(humanLines, fmt.Sprintf("  Note:    %s", sessionCommandNote))
		}
	}
	if len(mcpFlags) > 0 {
		humanLines = append(humanLines, fmt.Sprintf("  MCPs:    %s", strings.Join(mcpFlags, ", ")))
	}
	if parentInstance != nil {
		humanLines = append(humanLines, fmt.Sprintf("  Parent:  %s (%s)", parentInstance.Title, parentInstance.ID[:8]))
	}
	if worktreePath != "" {
		humanLines = append(humanLines, fmt.Sprintf("  Worktree: %s (branch: %s)", worktreePath, wtBranch))
		humanLines = append(humanLines, fmt.Sprintf("  Repo:    %s", worktreeRepoRoot))
	}
	if *sshHost != "" {
		humanLines = append(humanLines, fmt.Sprintf("  SSH:     %s", *sshHost))
		if *sshRemotePath != "" {
			humanLines = append(humanLines, fmt.Sprintf("  Remote:  %s", *sshRemotePath))
		}
	}
	if *resumeSession != "" {
		humanLines = append(humanLines, fmt.Sprintf("  Resume:  %s", *resumeSession))
	}
	humanLines = append(humanLines, "")
	humanLines = append(humanLines, "Next steps:")
	humanLines = append(humanLines, fmt.Sprintf("  agent-deck session start %s   # Start the session", sessionTitle))
	humanLines = append(humanLines, "  agent-deck                         # Open TUI and press Enter to attach")

	// Build JSON data
	jsonData := map[string]interface{}{
		"success": true,
		"id":      newInstance.ID,
		"title":   newInstance.Title,
		"path":    path,
		"tool":    newInstance.Tool,
		"group":   newInstance.GroupPath,
		"profile": storage.Profile(),
	}
	if sessionCommandInput != "" {
		jsonData["command"] = sessionCommandInput
		jsonData["resolved_command"] = newInstance.Command
		if newInstance.Wrapper != "" {
			jsonData["wrapper"] = newInstance.Wrapper
		}
		if sessionCommandNote != "" {
			jsonData["command_note"] = sessionCommandNote
		}
	}
	if len(mcpFlags) > 0 {
		jsonData["mcps"] = mcpFlags
	}
	if parentInstance != nil {
		jsonData["parent_id"] = parentInstance.ID
		jsonData["parent_title"] = parentInstance.Title
	}
	if worktreePath != "" {
		jsonData["worktree_path"] = worktreePath
		jsonData["worktree_branch"] = wtBranch
		jsonData["worktree_repo_root"] = worktreeRepoRoot
	}
	if *resumeSession != "" {
		jsonData["resume_session"] = *resumeSession
	}
	if *sandbox {
		jsonData["sandbox"] = true
		humanLines = append(humanLines[:len(humanLines)-3],
			"  Sandbox: enabled",
		)
		humanLines = append(humanLines, "", "Next steps:",
			fmt.Sprintf("  agent-deck session start %s   # Start the session", sessionTitle),
			"  agent-deck                         # Open TUI and press Enter to attach",
		)
	}

	out.Success(humanLines[0], jsonData)
	if !*jsonOutput && !quietMode {
		for _, line := range humanLines[1:] {
			fmt.Println(line)
		}
	}
}

// handleList lists all sessions
func handleList(profile string, args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	allProfiles := fs.Bool("all", false, "List sessions from all profiles")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck list [options]")
		fmt.Println()
		fmt.Println("List all sessions.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck list                    # List from default profile")
		fmt.Println("  agent-deck -p work list            # List from 'work' profile")
		fmt.Println("  agent-deck list --all              # List from all profiles")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	if *allProfiles {
		handleListAllProfiles(*jsonOutput)
		return
	}

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		fmt.Printf("Error: failed to initialize storage: %v\n", err)
		os.Exit(1)
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		fmt.Printf("Error: failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	if len(instances) == 0 {
		fmt.Printf("No sessions found in profile '%s'.\n", storage.Profile())
		return
	}

	if *jsonOutput {
		// JSON output for scripting
		type sessionJSON struct {
			ID            string    `json:"id"`
			Title         string    `json:"title"`
			Path          string    `json:"path"`
			Group         string    `json:"group"`
			Tool          string    `json:"tool"`
			Command       string    `json:"command,omitempty"`
			Status        string    `json:"status"`
			TmuxSession   string    `json:"tmux_session,omitempty"`
			Profile       string    `json:"profile"`
			CreatedAt     time.Time `json:"created_at"`
			SSHHost       string    `json:"ssh_host,omitempty"`
			SSHRemotePath string    `json:"ssh_remote_path,omitempty"`
			Channels      []string  `json:"channels,omitempty"`
			ExtraArgs     []string  `json:"extra_args,omitempty"`
			Color         string    `json:"color,omitempty"` // issue #391
		}
		// Warm tmux pane-title cache + load hook statuses so the CLI
		// reports the same Status the TUI and /api/menu do (issue #610).
		session.RefreshInstancesForCLIStatus(instances)
		sessions := make([]sessionJSON, len(instances))
		for i, inst := range instances {
			_ = inst.UpdateStatus()
			sj := sessionJSON{
				ID:            inst.ID,
				Title:         inst.Title,
				Path:          inst.ProjectPath,
				Group:         inst.GroupPath,
				Tool:          inst.Tool,
				Command:       inst.Command,
				Status:        StatusString(inst.Status),
				Profile:       storage.Profile(),
				CreatedAt:     inst.CreatedAt,
				SSHHost:       inst.SSHHost,
				SSHRemotePath: inst.SSHRemotePath,
				Channels:      inst.Channels,
				ExtraArgs:     inst.ExtraArgs,
				Color:         inst.Color,
			}
			if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
				sj.TmuxSession = tmuxSess.Name
			}
			sessions[i] = sj
		}
		output, err := json.MarshalIndent(sessions, "", "  ")
		if err != nil {
			fmt.Printf("Error: failed to format JSON output: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(output))
		return
	}

	// Table output
	fmt.Printf("Profile: %s\n\n", storage.Profile())
	fmt.Printf("%-*s %-*s %-*s %s\n", tableColTitle, "TITLE", tableColGroup, "GROUP", tableColPath, "PATH", "ID")
	fmt.Println(strings.Repeat("-", tableColTitle+tableColGroup+tableColPath+tableColIDDisplay+5))
	for _, inst := range instances {
		title := truncate(inst.Title, tableColTitle)
		group := truncate(inst.GroupPath, tableColGroup)
		path := truncate(inst.ProjectPath, tableColPath)
		// Safe ID display with bounds check to prevent panic
		idDisplay := inst.ID
		if len(idDisplay) > tableColIDDisplay {
			idDisplay = idDisplay[:tableColIDDisplay]
		}
		fmt.Printf("%-*s %-*s %-*s %s\n", tableColTitle, title, tableColGroup, group, tableColPath, path, idDisplay)
	}
	fmt.Printf("\nTotal: %d sessions\n", len(instances))

	// Show update notice if available
	printUpdateNotice()
}

// handleListAllProfiles lists sessions from all profiles
func handleListAllProfiles(jsonOutput bool) {
	profiles, err := session.ListProfiles()
	if err != nil {
		fmt.Printf("Error: failed to list profiles: %v\n", err)
		os.Exit(1)
	}

	if len(profiles) == 0 {
		fmt.Println("No profiles found.")
		return
	}

	if jsonOutput {
		type sessionJSON struct {
			ID            string    `json:"id"`
			Title         string    `json:"title"`
			Path          string    `json:"path"`
			Group         string    `json:"group"`
			Tool          string    `json:"tool"`
			Command       string    `json:"command,omitempty"`
			Profile       string    `json:"profile"`
			CreatedAt     time.Time `json:"created_at"`
			SSHHost       string    `json:"ssh_host,omitempty"`
			SSHRemotePath string    `json:"ssh_remote_path,omitempty"`
		}
		var allSessions []sessionJSON

		for _, profileName := range profiles {
			storage, err := session.NewStorageWithProfile(profileName)
			if err != nil {
				continue
			}
			instances, _, err := storage.LoadWithGroups()
			if err != nil {
				continue
			}
			for _, inst := range instances {
				allSessions = append(allSessions, sessionJSON{
					ID:            inst.ID,
					Title:         inst.Title,
					Path:          inst.ProjectPath,
					Group:         inst.GroupPath,
					Tool:          inst.Tool,
					Command:       inst.Command,
					Profile:       profileName,
					CreatedAt:     inst.CreatedAt,
					SSHHost:       inst.SSHHost,
					SSHRemotePath: inst.SSHRemotePath,
				})
			}
		}

		output, err := json.MarshalIndent(allSessions, "", "  ")
		if err != nil {
			fmt.Printf("Error: failed to format JSON output: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(output))
		return
	}

	// Table output grouped by profile
	totalSessions := 0
	for _, profileName := range profiles {
		storage, err := session.NewStorageWithProfile(profileName)
		if err != nil {
			continue
		}
		instances, _, err := storage.LoadWithGroups()
		if err != nil {
			continue
		}

		if len(instances) == 0 {
			continue
		}

		fmt.Printf("\n═══ Profile: %s ═══\n\n", profileName)
		fmt.Printf("%-*s %-*s %-*s %s\n", tableColTitle, "TITLE", tableColGroup, "GROUP", tableColPath, "PATH", "ID")
		fmt.Println(strings.Repeat("-", tableColTitle+tableColGroup+tableColPath+tableColIDDisplay+5))

		for _, inst := range instances {
			title := truncate(inst.Title, tableColTitle)
			group := truncate(inst.GroupPath, tableColGroup)
			path := truncate(inst.ProjectPath, tableColPath)
			idDisplay := inst.ID
			if len(idDisplay) > tableColIDDisplay {
				idDisplay = idDisplay[:tableColIDDisplay]
			}
			fmt.Printf("%-*s %-*s %-*s %s\n", tableColTitle, title, tableColGroup, group, tableColPath, path, idDisplay)
		}
		fmt.Printf("(%d sessions)\n", len(instances))
		totalSessions += len(instances)
	}

	fmt.Printf("\n═══════════════════════════════════════\n")
	fmt.Printf("Total: %d sessions across %d profiles\n", totalSessions, len(profiles))
}

// handleRemove removes a session by ID or title
func handleRemove(profile string, args []string) {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck remove <id|title>")
		fmt.Println()
		fmt.Println("Remove a session by ID or title.")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck remove abc12345")
		fmt.Println("  agent-deck remove \"My Project\"")
		fmt.Println("  agent-deck -p work remove abc12345   # Remove from 'work' profile")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	identifier := fs.Arg(0)
	if identifier == "" {
		out.Error("session ID or title is required", ErrCodeNotFound)
		if !*jsonOutput {
			fs.Usage()
		}
		os.Exit(1)
	}

	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Use shared ResolveSession for consistent matching (ambiguity detection, min prefix length)
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(fmt.Sprintf("%s (profile '%s')", errMsg, storage.Profile()), errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
	}

	removedID := inst.ID
	removedTitle := inst.Title

	// Always attempt to kill the tmux session, even if Exists() returns false.
	// The saved status may be stale (e.g., "error" in DB but tmux session still alive).
	// KillAndWait is safe to call on non-existent sessions (returns error which we handle).
	// Uses the synchronous variant so the SIGTERM→SIGKILL escalation finishes
	// before this short-lived CLI exits — otherwise SIGHUP-immune claude
	// processes survive as orphans (issue #59, v1.7.68).
	if err := inst.KillAndWait(); err != nil {
		// Only warn if the session actually existed (ignore "not found" errors)
		if inst.Exists() && !*jsonOutput {
			fmt.Printf("Warning: failed to kill tmux session: %v\n", err)
			fmt.Println("Session removed from Agent Deck but may still be running in tmux")
		}
	}

	// v1.7.21+: if this session was spawned via LaunchAs=service, the
	// transient systemd-user service unit survives a plain `tmux
	// kill-server` (Restart=on-failure would respawn it). Best-effort
	// stop + reset-failed the unit here so `agent-deck remove` is truly
	// terminal. No-op on non-service-mode sessions and on non-systemd
	// hosts.
	_ = inst.StopServiceUnit()

	// Clean up worktree directory if this is a worktree session
	if inst.IsWorktree() {
		if err := git.RemoveWorktree(inst.WorktreeRepoRoot, inst.WorktreePath, false); err != nil {
			if !*jsonOutput {
				fmt.Printf("Warning: failed to remove worktree: %v\n", err)
			}
		}
		_ = git.PruneWorktrees(inst.WorktreeRepoRoot)
	}

	// Direct SQL DELETE first to prevent resurrection by concurrent TUI force saves.
	// The TUI's forceSaveInstances() can race with CLI deletion and re-insert the session.
	// By deleting the row directly, we ensure it's gone even if SaveWithGroups races.
	if err := storage.DeleteInstance(removedID); err != nil {
		if !*jsonOutput {
			fmt.Printf("Warning: direct delete failed: %v\n", err)
		}
	}

	// Rebuild instance list without the deleted session and save with groups
	newInstances := make([]*session.Instance, 0, len(instances)-1)
	for _, s := range instances {
		if s.ID != removedID {
			newInstances = append(newInstances, s)
		}
	}
	groupTree := session.NewGroupTreeWithGroups(newInstances, groups)

	if err := storage.SaveWithGroups(newInstances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	out.Success(
		fmt.Sprintf("Removed session: %s (from profile '%s')", removedTitle, storage.Profile()),
		map[string]interface{}{
			"success": true,
			"id":      removedID,
			"title":   removedTitle,
			"removed": true,
			"profile": storage.Profile(),
		},
	)
}

func handleRename(profile string, args []string) {
	fs := flag.NewFlagSet("rename", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck rename <id|title> <new-title>")
		fmt.Println()
		fmt.Println("Rename a session by ID or title.")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck rename abc12345 \"New Name\"")
		fmt.Println("  agent-deck rename \"Old Name\" \"New Name\"")
		fmt.Println("  agent-deck -p work rename abc12345 \"New Name\"   # Rename in 'work' profile")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	identifier := fs.Arg(0)
	newTitle := fs.Arg(1)
	if identifier == "" || newTitle == "" {
		out.Error("session ID/title and new title are required", ErrCodeInvalidOperation)
		if !*jsonOutput {
			fs.Usage()
		}
		os.Exit(1)
	}

	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(fmt.Sprintf("%s (profile '%s')", errMsg, storage.Profile()), errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
	}

	oldTitle := inst.Title

	// Check for duplicate title at the same path (but allow renaming to same title)
	if newTitle != oldTitle {
		if isDup, existing := isDuplicateSession(instances, newTitle, inst.ProjectPath); isDup {
			out.Error(
				fmt.Sprintf("session with title %q already exists at path %q (id: %s)", newTitle, inst.ProjectPath, existing.ID),
				ErrCodeInvalidOperation,
			)
			os.Exit(1)
		}
	}

	inst.Title = newTitle
	inst.SyncTmuxDisplayName()

	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	out.Success(
		fmt.Sprintf("Renamed session: %q → %q (profile '%s')", oldTitle, newTitle, storage.Profile()),
		map[string]interface{}{
			"success":   true,
			"id":        inst.ID,
			"old_title": oldTitle,
			"new_title": newTitle,
			"profile":   storage.Profile(),
		},
	)
}

// statusCounts holds session counts by status
type statusCounts struct {
	running int
	waiting int
	idle    int
	err     int
	stopped int
	total   int
}

// countByStatus counts sessions by their status
func countByStatus(instances []*session.Instance) statusCounts {
	// Warm tmux pane-title cache + load hook statuses so `status`/`status --json`
	// reports the same counts the TUI and /api/menu do (issue #610).
	session.RefreshInstancesForCLIStatus(instances)
	var counts statusCounts
	for _, inst := range instances {
		_ = inst.UpdateStatus() // Refresh status from tmux
		switch inst.Status {
		case session.StatusRunning:
			counts.running++
		case session.StatusWaiting:
			counts.waiting++
		case session.StatusIdle:
			counts.idle++
		case session.StatusError:
			counts.err++
		case session.StatusStopped:
			counts.stopped++
		}
		counts.total++
	}
	return counts
}

// handleStatus shows session status summary
func handleStatus(profile string, args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	verbose := fs.Bool("verbose", false, "Show detailed session list")
	verboseShort := fs.Bool("v", false, "Show detailed session list (short)")
	quiet := fs.Bool("quiet", false, "Only output waiting count (for scripts)")
	quietShort := fs.Bool("q", false, "Only output waiting count (short)")
	jsonOutput := fs.Bool("json", false, "Output as JSON")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck status [options]")
		fmt.Println()
		fmt.Println("Show a summary of session statuses.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck status              # Quick summary")
		fmt.Println("  agent-deck status -v           # Detailed list")
		fmt.Println("  agent-deck status -q           # Just waiting count")
		fmt.Println("  agent-deck -p work status      # Status for 'work' profile")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	// Load sessions
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		fmt.Printf("Error: failed to initialize storage: %v\n", err)
		os.Exit(1)
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		fmt.Printf("Error: failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	if len(instances) == 0 {
		if *jsonOutput {
			fmt.Println(`{"waiting": 0, "running": 0, "idle": 0, "error": 0, "stopped": 0, "total": 0}`)
		} else if *quiet || *quietShort {
			fmt.Println("0")
		} else {
			fmt.Printf("No sessions in profile '%s'.\n", storage.Profile())
		}
		return
	}

	// Count by status
	counts := countByStatus(instances)

	// Output based on flags
	if *jsonOutput {
		type statusJSON struct {
			Waiting int `json:"waiting"`
			Running int `json:"running"`
			Idle    int `json:"idle"`
			Error   int `json:"error"`
			Stopped int `json:"stopped"`
			Total   int `json:"total"`
		}
		output, _ := json.Marshal(statusJSON{
			Waiting: counts.waiting,
			Running: counts.running,
			Idle:    counts.idle,
			Error:   counts.err,
			Stopped: counts.stopped,
			Total:   counts.total,
		})
		fmt.Println(string(output))
	} else if *quiet || *quietShort {
		fmt.Println(counts.waiting)
	} else if *verbose || *verboseShort {
		// Detailed output grouped by status
		printStatusGroup := func(label, symbol string, status session.Status) {
			var matching []*session.Instance
			for _, inst := range instances {
				if inst.Status == status {
					matching = append(matching, inst)
				}
			}
			if len(matching) == 0 {
				return
			}
			fmt.Printf("%s (%d):\n", label, len(matching))
			for _, inst := range matching {
				path := inst.ProjectPath
				home, _ := os.UserHomeDir()
				if strings.HasPrefix(path, home) {
					path = "~" + path[len(home):]
				}
				fmt.Printf("  %s %-16s %-10s %s\n", symbol, inst.Title, inst.Tool, path)
			}
			fmt.Println()
		}

		printStatusGroup("WAITING", "◐", session.StatusWaiting)
		printStatusGroup("RUNNING", "●", session.StatusRunning)
		printStatusGroup("IDLE", "○", session.StatusIdle)
		printStatusGroup("STOPPED", "■", session.StatusStopped)
		printStatusGroup("ERROR", "✕", session.StatusError)

		fmt.Printf("Total: %d sessions in profile '%s'\n", counts.total, storage.Profile())
	} else {
		// Compact output
		fmt.Printf("%d waiting • %d running • %d idle\n",
			counts.waiting, counts.running, counts.idle)
	}

	// Show update notice if available (skip for JSON/quiet output)
	if !*jsonOutput && !*quiet && !*quietShort {
		printUpdateNotice()
	}
}

// handleProfile manages profiles (list, create, delete, default)
func handleProfile(args []string) {
	// Extract --json and -q/--quiet flags from anywhere in args
	var jsonMode, quietMode bool
	var filteredArgs []string
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonMode = true
		case "--quiet", "-q":
			quietMode = true
		default:
			filteredArgs = append(filteredArgs, arg)
		}
	}
	out := NewCLIOutput(jsonMode, quietMode)

	if len(filteredArgs) == 0 {
		// Default to list
		handleProfileList(out, jsonMode)
		return
	}

	if filteredArgs[0] == "help" || filteredArgs[0] == "--help" || filteredArgs[0] == "-h" {
		printProfileHelp()
		return
	}

	switch filteredArgs[0] {
	case "list", "ls":
		handleProfileList(out, jsonMode)
	case "create", "new":
		if len(filteredArgs) >= 2 && isHelpArg(filteredArgs[1]) {
			printProfileCreateHelp()
			return
		}
		if len(filteredArgs) < 2 {
			out.Error("profile name is required", ErrCodeInvalidOperation)
			if !jsonMode {
				printProfileCreateHelp()
			}
			os.Exit(1)
		}
		handleProfileCreate(out, filteredArgs[1])
	case "delete", "rm":
		if len(filteredArgs) >= 2 && isHelpArg(filteredArgs[1]) {
			printProfileDeleteHelp()
			return
		}
		if len(filteredArgs) < 2 {
			out.Error("profile name is required", ErrCodeInvalidOperation)
			if !jsonMode {
				printProfileDeleteHelp()
			}
			os.Exit(1)
		}
		handleProfileDelete(out, jsonMode, filteredArgs[1])
	case "default":
		if len(filteredArgs) >= 2 && isHelpArg(filteredArgs[1]) {
			printProfileDefaultHelp()
			return
		}
		if len(filteredArgs) < 2 {
			// Show current default
			config, err := session.LoadConfig()
			if err != nil {
				out.Error(fmt.Sprintf("failed to load config: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			}
			out.Success(fmt.Sprintf("Default profile: %s", config.DefaultProfile), map[string]interface{}{
				"success":         true,
				"default_profile": config.DefaultProfile,
			})
			return
		}
		handleProfileSetDefault(out, filteredArgs[1])
	default:
		out.Error(fmt.Sprintf("unknown profile command: %s", filteredArgs[0]), ErrCodeInvalidOperation)
		if !jsonMode {
			fmt.Println()
			printProfileHelp()
		}
		os.Exit(1)
	}
}

func printProfileHelp() {
	fmt.Println("Usage: agent-deck profile <command>")
	fmt.Println()
	fmt.Println("Manage named Agent Deck profiles.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  list              List all profiles")
	fmt.Println("  create <name>     Create a new profile")
	fmt.Println("  delete <name>     Delete a profile")
	fmt.Println("  default [name]    Show or set default profile")
}

func printProfileCreateHelp() {
	fmt.Println("Usage: agent-deck profile create <name>")
}

func printProfileDeleteHelp() {
	fmt.Println("Usage: agent-deck profile delete <name>")
}

func printProfileDefaultHelp() {
	fmt.Println("Usage: agent-deck profile default [name]")
}

func isHelpArg(arg string) bool {
	return arg == "help" || arg == "--help" || arg == "-h"
}

func handleProfileList(out *CLIOutput, jsonMode bool) {
	profiles, err := session.ListProfiles()
	if err != nil {
		out.Error(fmt.Sprintf("failed to list profiles: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	config, _ := session.LoadConfig()
	defaultProfile := session.DefaultProfile
	if config != nil {
		defaultProfile = config.DefaultProfile
	}

	if jsonMode {
		var profileList []map[string]interface{}
		for _, p := range profiles {
			profileList = append(profileList, map[string]interface{}{
				"name":       p,
				"is_default": p == defaultProfile,
			})
		}
		out.Success("", map[string]interface{}{
			"success":         true,
			"profiles":        profileList,
			"default_profile": defaultProfile,
			"total":           len(profiles),
		})
		return
	}

	if len(profiles) == 0 {
		fmt.Println("No profiles found.")
		fmt.Println("Run 'agent-deck' to create the default profile automatically.")
		return
	}

	fmt.Println("Profiles:")
	for _, p := range profiles {
		if p == defaultProfile {
			fmt.Printf("  * %s (default)\n", p)
		} else {
			fmt.Printf("    %s\n", p)
		}
	}
	fmt.Printf("\nTotal: %d profiles\n", len(profiles))
}

func handleProfileCreate(out *CLIOutput, name string) {
	if err := session.CreateProfile(name); err != nil {
		out.Error(fmt.Sprintf("%v", err), ErrCodeAlreadyExists)
		os.Exit(1)
	}
	out.Success(fmt.Sprintf("Created profile: %s", name), map[string]interface{}{
		"success": true,
		"name":    name,
		"created": true,
	})
}

func handleProfileDelete(out *CLIOutput, jsonMode bool, name string) {
	// Skip confirmation in JSON mode (for automation)
	if !jsonMode {
		fmt.Printf(
			"Are you sure you want to delete profile '%s'? This will remove all sessions in this profile. [y/N] ",
			name,
		)
		var response string
		_, _ = fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Cancelled.")
			return
		}
	}

	if err := session.DeleteProfile(name); err != nil {
		out.Error(fmt.Sprintf("%v", err), ErrCodeNotFound)
		os.Exit(1)
	}
	out.Success(fmt.Sprintf("Deleted profile: %s", name), map[string]interface{}{
		"success": true,
		"name":    name,
		"deleted": true,
	})
}

func handleProfileSetDefault(out *CLIOutput, name string) {
	if err := session.SetDefaultProfile(name); err != nil {
		out.Error(fmt.Sprintf("%v", err), ErrCodeNotFound)
		os.Exit(1)
	}
	out.Success(fmt.Sprintf("Default profile set to: %s", name), map[string]interface{}{
		"success":         true,
		"name":            name,
		"default_profile": name,
	})
}

// handleUpdate checks for and performs updates
func handleUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	checkOnly := fs.Bool("check", false, "Only check for updates, don't install")
	targetVersion := fs.String("version", "", "Install a specific released version (e.g. 1.7.3); may be a downgrade")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck update [options]")
		fmt.Println()
		fmt.Println("Check for and install updates (always checks GitHub for latest).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck update              # Check and install latest if available")
		fmt.Println("  agent-deck update --check      # Only check, don't install")
		fmt.Println("  agent-deck update --version 1.7.3  # Install a specific version (may downgrade)")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	if strings.TrimSpace(*targetVersion) != "" {
		handleUpdateToSpecificVersion(*targetVersion, *checkOnly)
		return
	}

	fmt.Printf("Agent Deck v%s\n", Version)
	fmt.Println("Checking for updates...")

	// Always force check when user explicitly runs 'update' command
	// Cache is only useful for background checks (TUI startup), not explicit requests
	info, err := update.CheckForUpdate(Version, true)
	if err != nil {
		fmt.Printf("Error checking for updates: %v\n", err)
		os.Exit(1)
	}

	if !info.Available {
		fmt.Println("✓ You're running the latest version!")
		return
	}

	fmt.Printf("\n⬆ Update available: v%s → v%s\n", info.CurrentVersion, info.LatestVersion)
	fmt.Printf("  Release: %s\n", info.ReleaseURL)

	// Fetch and display changelog
	displayChangelog(info.CurrentVersion, info.LatestVersion)

	installPath, homebrewUpgradeCmd, homebrewManaged, hbErr := update.DetectHomebrewManagedInstall()
	if hbErr != nil {
		// Non-fatal: fall back to direct updater flow.
		homebrewManaged = false
	}
	homebrewInstallCmd := homebrewUpgradeCmd
	if homebrewManaged {
		homebrewInstallCmd = fmt.Sprintf("brew update && %s", homebrewUpgradeCmd)
	}

	if *checkOnly {
		if homebrewManaged {
			fmt.Printf("\nHomebrew-managed install detected at %s\n", installPath)
			fmt.Printf("Run `%s` to install.\n", homebrewInstallCmd)
		} else {
			fmt.Println("\nRun 'agent-deck update' to install.")
		}
		return
	}

	if homebrewManaged {
		fmt.Printf("\nHomebrew-managed install detected at %s\n", installPath)
		fmt.Printf("Will run: %s\n", homebrewInstallCmd)
	}

	// Confirm update - drain any buffered input first to avoid garbage
	drainStdin()
	if homebrewManaged {
		fmt.Print("\nInstall update via Homebrew now? [Y/n] ")
	} else {
		fmt.Print("\nInstall update? [Y/n] ")
	}
	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(response)
	if response != "" && response != "y" && response != "Y" {
		fmt.Println("Update cancelled.")
		return
	}

	// Perform update (direct binary replacement or Homebrew upgrade)
	fmt.Println()
	if homebrewManaged {
		if err := runHomebrewUpgradeWithRefresh(homebrewUpgradeCmd); err != nil {
			fmt.Printf("Error installing update via Homebrew: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := update.PerformUpdate(info.DownloadURL); err != nil {
			fmt.Printf("Error installing update: %v\n", err)
			os.Exit(1)
		}
	}

	// Update bridge.py if conductor is installed
	if err := update.UpdateBridgePy(); err != nil {
		fmt.Printf("Warning: Failed to update bridge.py: %v\n", err)
		fmt.Println("  You can manually refresh it with: agent-deck conductor setup <name>")
	}

	fmt.Printf("\n✓ Updated to v%s\n", info.LatestVersion)
	fmt.Println("  Restart agent-deck to use the new version.")

	// Offer to update remotes
	updateRemotesAfterLocalUpdate(info.LatestVersion)
}

// handleUpdateToSpecificVersion installs a user-specified release version.
// Unlike the default update flow, this bypasses the "is this newer?" check so
// callers can reinstall or downgrade to a prior release on purpose.
func handleUpdateToSpecificVersion(requested string, checkOnly bool) {
	fmt.Printf("Agent Deck v%s\n", Version)

	normalized := update.NormalizeReleaseTag(requested)
	if normalized == "" {
		fmt.Println("Error: --version requires a non-empty version (e.g. 1.7.3)")
		os.Exit(1)
	}
	targetVersion := strings.TrimPrefix(normalized, "v")

	installPath, homebrewUpgradeCmd, homebrewManaged, hbErr := update.DetectHomebrewManagedInstall()
	if hbErr != nil {
		homebrewManaged = false
	}
	if homebrewManaged {
		fmt.Printf("\nHomebrew-managed install detected at %s\n", installPath)
		fmt.Printf("Pinning to a specific version is not supported via this command.\n")
		fmt.Printf("Use Homebrew directly, or run `%s` for the latest.\n", homebrewUpgradeCmd)
		os.Exit(1)
	}

	fmt.Printf("Fetching release %s...\n", normalized)
	release, err := update.FetchReleaseByTag(normalized)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	downloadURL := update.GetAssetURLForPlatform(release, runtime.GOOS, runtime.GOARCH)
	if downloadURL == "" {
		fmt.Printf("Error: release %s has no binary for %s/%s\n", normalized, runtime.GOOS, runtime.GOARCH)
		os.Exit(1)
	}

	cmp := update.CompareVersions(Version, targetVersion)
	switch {
	case cmp == 0:
		fmt.Printf("\n↻ Reinstalling v%s (current = requested)\n", targetVersion)
	case cmp < 0:
		fmt.Printf("\n⬆ Installing v%s → v%s\n", Version, targetVersion)
	default:
		fmt.Printf("\n⬇ Downgrading v%s → v%s\n", Version, targetVersion)
	}
	fmt.Printf("  Release: %s\n", release.HTMLURL)

	if checkOnly {
		fmt.Println("\nRun without --check to install.")
		return
	}

	drainStdin()
	defaultYes := cmp <= 0
	prompt := fmt.Sprintf("\nInstall v%s now? [Y/n] ", targetVersion)
	if !defaultYes {
		prompt = fmt.Sprintf("\nDowngrade to v%s now? [y/N] ", targetVersion)
	}
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	confirmed := response == "y" || response == "yes" || (defaultYes && response == "")
	if !confirmed {
		fmt.Println("Update cancelled.")
		return
	}

	fmt.Println()
	if err := update.PerformUpdate(downloadURL); err != nil {
		fmt.Printf("Error installing v%s: %v\n", targetVersion, err)
		os.Exit(1)
	}

	if err := update.UpdateBridgePy(); err != nil {
		fmt.Printf("Warning: Failed to update bridge.py: %v\n", err)
		fmt.Println("  You can manually refresh it with: agent-deck conductor setup <name>")
	}

	fmt.Printf("\n✓ Installed v%s\n", targetVersion)
	fmt.Println("  Restart agent-deck to use this version.")
}

func runHomebrewUpgradeWithRefresh(homebrewUpgradeCmd string) error {
	cmdParts := strings.Fields(homebrewUpgradeCmd)
	if len(cmdParts) == 0 {
		return fmt.Errorf("empty Homebrew upgrade command")
	}

	brewBin := cmdParts[0]
	refreshCmd := exec.Command(brewBin, "update")
	refreshCmd.Stdout = os.Stdout
	refreshCmd.Stderr = os.Stderr
	refreshCmd.Stdin = os.Stdin
	if err := refreshCmd.Run(); err != nil {
		return fmt.Errorf("failed to refresh Homebrew metadata: %w", err)
	}

	upgradeCmd := exec.Command(brewBin, cmdParts[1:]...)
	upgradeCmd.Stdout = os.Stdout
	upgradeCmd.Stderr = os.Stderr
	upgradeCmd.Stdin = os.Stdin
	if err := upgradeCmd.Run(); err != nil {
		return fmt.Errorf("failed to run `%s`: %w", homebrewUpgradeCmd, err)
	}

	return nil
}

// displayChangelog fetches and displays changelog between versions
func displayChangelog(currentVersion, latestVersion string) {
	changelog, err := update.FetchChangelog()
	if err != nil {
		fmt.Println("\n  (Could not fetch changelog. See release notes at the URL above.)")
		return
	}

	entries := update.ParseChangelog(changelog)
	changes := update.GetChangesBetweenVersions(entries, currentVersion, latestVersion)

	if len(changes) > 0 {
		fmt.Print(update.FormatChangelogForDisplay(changes))
	}
}

// drainStdin discards any pending input in stdin to prevent garbage from being read
// This is needed before prompts because ANSI escape sequences or user keypresses
// may have buffered during the changelog display
func drainStdin() {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return
	}

	// Use TCIFLUSH via ioctl to flush the terminal input queue
	// This is the proper Unix way to discard pending input
	// TCIFLUSH = 0 (flush input), TCIOFLUSH = 2 (flush both)
	// The syscall is: ioctl(fd, TCFLSH, TCIFLUSH)
	// On macOS/Darwin, TCFLSH = 0x80047410 (from termios.h)
	// On Linux, TCFLSH = 0x540B
	const (
		tcflshDarwin = 0x80047410
		tcflshLinux  = 0x540B
		tciflush     = 0 // flush input queue
	)

	// Try Darwin first, then Linux
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tcflshDarwin, tciflush)
	if errno != 0 {
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tcflshLinux, tciflush)
	}
}

func printHelp() {
	fmt.Printf("Agent Deck v%s\n", Version)
	fmt.Println("Terminal session manager for AI coding agents")
	fmt.Println()
	fmt.Println("Usage: agent-deck [-p profile] [-g group] [--select id|title] [command]")
	fmt.Println()
	fmt.Println("Global Options:")
	fmt.Println("  -p, --profile <name>   Use specific profile (default: 'default')")
	fmt.Println("  -g, --group <name>     Launch TUI scoped to a specific group")
	fmt.Println("  --select <id|title>    Launch TUI with cursor on a specific session (all groups stay visible)")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  (none)           Start the TUI")
	fmt.Println("  add <path>       Add a new session")
	fmt.Println("  launch [path]    Add, start, and optionally send a message in one step")
	fmt.Println("  try <name>       Quick experiment (create/find dated folder + session)")
	fmt.Println("  list, ls         List all sessions")
	fmt.Println("  remove, rm       Remove a session")
	fmt.Println("  rename, mv       Rename a session")
	fmt.Println("  status           Show session status summary")
	fmt.Println("  session          Manage session lifecycle")
	fmt.Println("  mcp              Manage MCP servers")
	fmt.Println("  skill            Manage project skills")
	fmt.Println("  codex-hooks      Manage Codex notify hook integration")
	fmt.Println("  gemini-hooks     Manage Gemini hook integration")
	fmt.Println("  group            Manage groups")
	fmt.Println("  worktree, wt     Manage git worktrees")
	fmt.Println("  web              Start TUI with web UI server running alongside")
	fmt.Println("  remote           Manage remote agent-deck instances")
	fmt.Println("  conductor        Manage conductor meta-agent orchestration")
	fmt.Println("  profile          Manage profiles")
	fmt.Println("  update           Check for and install updates")
	fmt.Println("  debug-dump       Dump debug ring buffer to file for sharing")
	fmt.Println("  uninstall        Uninstall Agent Deck")
	fmt.Println("  version          Show version")
	fmt.Println("  help             Show this help")
	fmt.Println()
	fmt.Println("Session Commands:")
	fmt.Println("  session start <id>        Start a session's tmux process")
	fmt.Println("  session stop <id>         Stop session process")
	fmt.Println("  session restart <id>      Restart session (reload MCPs)")
	fmt.Println("  session fork <id>         Fork Claude session with context")
	fmt.Println("  session attach <id>       Attach to session interactively")
	fmt.Println("  session show [id]         Show session details")
	fmt.Println()
	fmt.Println("MCP Commands:")
	fmt.Println("  mcp list                  List available MCPs from config.toml")
	fmt.Println("  mcp attached [id]         Show MCPs attached to a session")
	fmt.Println("  mcp attach <id> <mcp>     Attach MCP to session")
	fmt.Println("  mcp detach <id> <mcp>     Detach MCP from session")
	fmt.Println()
	fmt.Println("Skill Commands:")
	fmt.Println("  skill list                List discoverable skills")
	fmt.Println("  skill attached [id]       Show skills attached to a session")
	fmt.Println("  skill attach <id> <name>  Attach skill to session project")
	fmt.Println("  skill detach <id> <name>  Detach skill from session project")
	fmt.Println("  skill source list         List global skill sources")
	fmt.Println()
	fmt.Println("Codex Hook Commands:")
	fmt.Println("  codex-hooks install       Install or upgrade Codex notify hook")
	fmt.Println("  codex-hooks uninstall     Remove Codex notify hook")
	fmt.Println("  codex-hooks status        Show Codex hook install status")
	fmt.Println("  gemini-hooks install      Install Gemini hooks")
	fmt.Println("  gemini-hooks uninstall    Remove Gemini hooks")
	fmt.Println("  gemini-hooks status       Show Gemini hooks install status")
	fmt.Println()
	fmt.Println("Group Commands:")
	fmt.Println("  group list                List all groups")
	fmt.Println("  group create <name>       Create a new group")
	fmt.Println("  group delete <name>       Delete a group")
	fmt.Println("  group move <id> <group>   Move session to group")
	fmt.Println()
	fmt.Println("Conductor Commands:")
	fmt.Println("  conductor setup           Set up conductor (Telegram bridge + sessions)")
	fmt.Println("  conductor teardown        Stop conductor and remove bridge daemon")
	fmt.Println("  conductor status          Show conductor health across profiles")
	fmt.Println("  conductor list            List configured conductors")
	fmt.Println()
	fmt.Println("Remote Commands:")
	fmt.Println("  remote add <name> <user@host>             Register a remote agent-deck instance")
	fmt.Println("    --agent-deck-path <path>                Path to agent-deck binary on remote (default: agent-deck)")
	fmt.Println("    --profile <name>                        Remote profile to use (default: default)")
	fmt.Println("  remote remove, rm <name>                  Remove a remote")
	fmt.Println("  remote list, ls [--json]                  List configured remotes")
	fmt.Println("  remote sessions [name] [--json]           Show sessions on remote(s)")
	fmt.Println("  remote attach <name> <session>            Attach to a remote session")
	fmt.Println("  remote rename <name> <session> <title>    Rename a remote session")
	fmt.Println("  remote update [name]                      Install/upgrade agent-deck on remote(s)")
	fmt.Println()
	fmt.Println("Worktree Commands:")
	fmt.Println("  worktree list             List worktrees with session associations")
	fmt.Println("  worktree info <session>   Show worktree info for a session")
	fmt.Println("  worktree cleanup          Find and remove orphaned worktrees/sessions")
	fmt.Println()
	fmt.Println("Profile Commands:")
	fmt.Println("  profile list              List all profiles")
	fmt.Println("  profile create <name>     Create a new profile")
	fmt.Println("  profile delete <name>     Delete a profile")
	fmt.Println("  profile default [name]    Show or set default profile")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck                            # Start TUI with default profile")
	fmt.Println("  agent-deck -p work                    # Start TUI with 'work' profile")
	fmt.Println("  agent-deck add .                      # Add current directory")
	fmt.Println("  agent-deck add -t \"My App\" -g dev .   # With title and group")
	fmt.Println("  agent-deck session start my-project   # Start a session")
	fmt.Println("  agent-deck session show               # Show current session (in tmux)")
	fmt.Println("  agent-deck mcp list --json            # List MCPs as JSON")
	fmt.Println("  agent-deck mcp attach my-app exa      # Attach MCP to session")
	fmt.Println("  agent-deck skill attach my-app react  # Attach skill to project")
	fmt.Println("  agent-deck group move my-app work     # Move session to group")
	fmt.Println("  agent-deck web                        # TUI + web server on 127.0.0.1:8420")
	fmt.Println("  agent-deck web --listen :9000         # TUI + web on custom port")
	fmt.Println("  agent-deck web --read-only            # TUI + web in read-only mode")
	fmt.Println("  agent-deck web --token secret         # TUI + web with auth token")
	fmt.Println("  agent-deck web --help                 # Show web command flags")
	fmt.Println()
	fmt.Println("Environment Variables:")
	fmt.Println("  AGENTDECK_PROFILE    Default profile to use")
	fmt.Println("  AGENTDECK_COLOR      Color mode: truecolor, 256, 16, none")
	fmt.Println()
	fmt.Println("Keyboard shortcuts (in TUI):")
	fmt.Println("  n          New session")
	fmt.Println("  g          New group")
	fmt.Println("  Enter      Attach to session")
	fmt.Println("  m          MCP Manager")
	fmt.Println("  s          Skills Manager")
	fmt.Println("  M          Move session to group")
	fmt.Println("  r          Rename session/group")
	fmt.Println("  R          Restart session")
	fmt.Println("  d          Delete session/group")
	fmt.Println("  S          Settings")
	fmt.Println("  /          Search")
	fmt.Println("  Ctrl+Q     Detach from session")
	fmt.Println("  q          Quit")
}

// mergeFlags returns the non-empty value, preferring the first
func mergeFlags(long, short string) string {
	if long != "" {
		return long
	}
	return short
}

// truncate shortens a string to max length with ellipsis
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// detectTool determines the tool type from command
func detectTool(cmd string) string {
	// Check custom tools first (exact match on original case)
	if session.GetToolDef(cmd) != nil {
		return cmd
	}

	cmd = strings.ToLower(cmd)
	switch {
	case strings.Contains(cmd, "claude"):
		return "claude"
	case strings.Contains(cmd, "opencode") || strings.Contains(cmd, "open-code"):
		return "opencode"
	case strings.Contains(cmd, "gemini"):
		return "gemini"
	case strings.Contains(cmd, "codex"):
		return "codex"
	case hasCommandToken(cmd, "pi"):
		return "pi"
	case strings.Contains(cmd, "copilot"):
		return "copilot"
	case strings.Contains(cmd, "cursor"):
		return "cursor"
	default:
		return "shell"
	}
}

// hasCommandToken reports whether `want` appears as a whitespace-delimited
// token in `cmd` (case-insensitive). Used for short, ambiguous tool names
// like "pi" where strings.Contains would falsely match "epic", "tapioca",
// etc. Longer names like "copilot" or "claude" don't need this.
func hasCommandToken(cmd, want string) bool {
	return slices.Contains(strings.Fields(strings.ToLower(cmd)), want)
}

// handleUninstall removes agent-deck from the system
func handleDebugDump() {
	baseDir, err := session.GetAgentDeckDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot determine agent-deck dir: %v\n", err)
		os.Exit(1)
	}

	// Initialize logging just enough to populate the ring buffer from the log file
	logging.Init(logging.Config{
		Debug:  true,
		LogDir: baseDir,
		Level:  "debug",
	})
	defer logging.Shutdown()

	dumpPath := filepath.Join(baseDir, fmt.Sprintf("debug-dump-%d.jsonl", time.Now().Unix()))
	if err := logging.DumpRingBuffer(dumpPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to dump ring buffer: %v\n", err)
		os.Exit(1)
	}

	// Also check if the debug.log file exists and report its path
	debugLogPath := filepath.Join(baseDir, "debug.log")
	if info, statErr := os.Stat(debugLogPath); statErr == nil {
		fmt.Printf("Debug log: %s (%.1f MB)\n", debugLogPath, float64(info.Size())/(1024*1024))
	}
	fmt.Printf("Ring buffer dumped to: %s\n", dumpPath)
	fmt.Println("Share this file when reporting lag or stuck issues.")
}

func handleUninstall(args []string) {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	keepData := fs.Bool("keep-data", false, "Keep ~/.agent-deck/ (sessions, config, logs)")
	keepTmuxConfig := fs.Bool("keep-tmux-config", false, "Keep tmux configuration")
	dryRun := fs.Bool("dry-run", false, "Show what would be removed without removing")
	yes := fs.Bool("y", false, "Skip confirmation prompts")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck uninstall [options]")
		fmt.Println()
		fmt.Println("Uninstall Agent Deck from your system.")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --dry-run           Show what would be removed without removing")
		fmt.Println("  --keep-data         Keep ~/.agent-deck/ (sessions, config, logs)")
		fmt.Println("  --keep-tmux-config  Keep tmux configuration")
		fmt.Println("  -y                  Skip confirmation prompts")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck uninstall              # Interactive uninstall")
		fmt.Println("  agent-deck uninstall --dry-run    # Preview what would be removed")
		fmt.Println("  agent-deck uninstall --keep-data  # Remove binary only, keep sessions")
		fmt.Println("  agent-deck uninstall -y           # Uninstall without prompts")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	fmt.Println("╔════════════════════════════════════════╗")
	fmt.Println("║       Agent Deck Uninstaller           ║")
	fmt.Println("╚════════════════════════════════════════╝")
	fmt.Println()

	if *dryRun {
		fmt.Println("DRY RUN MODE - Nothing will be removed")
		fmt.Println()
	}

	homeDir, _ := os.UserHomeDir()
	dataDir := filepath.Join(homeDir, ".agent-deck")

	// Track what we find
	type foundItem struct {
		itemType    string
		path        string
		description string
	}
	var foundItems []foundItem

	// Check for Homebrew installation
	homebrewInstalled := false
	if _, err := exec.LookPath("brew"); err == nil {
		cmd := exec.Command("brew", "list", "agent-deck")
		if cmd.Run() == nil {
			homebrewInstalled = true
			foundItems = append(foundItems, foundItem{"homebrew", "", "Homebrew package: agent-deck"})
			fmt.Println("Found: Homebrew installation")
		}
	}

	// Check common binary locations
	binaryLocations := []string{
		filepath.Join(homeDir, ".local", "bin", "agent-deck"),
		"/usr/local/bin/agent-deck",
		filepath.Join(homeDir, "bin", "agent-deck"),
	}

	for _, loc := range binaryLocations {
		info, err := os.Lstat(loc)
		if err != nil {
			continue
		}

		if info.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(loc)
			foundItems = append(
				foundItems,
				foundItem{"binary-symlink", loc, fmt.Sprintf("Binary (symlink) → %s", target)},
			)
			fmt.Printf("Found: Binary (symlink) at %s\n", loc)
			fmt.Printf("       → %s\n", target)
		} else {
			foundItems = append(foundItems, foundItem{"binary", loc, "Binary"})
			fmt.Printf("Found: Binary at %s\n", loc)
		}
	}

	// Check for data directory
	if info, err := os.Stat(dataDir); err == nil && info.IsDir() {
		// Count sessions and profiles
		sessionCount := 0
		profileCount := 0
		profilesDir := filepath.Join(dataDir, "profiles")
		if entries, err := os.ReadDir(profilesDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					// Check for state.db (SQLite, v0.11.0+) or sessions.json (legacy)
					dbFile := filepath.Join(profilesDir, entry.Name(), "state.db")
					jsonFile := filepath.Join(profilesDir, entry.Name(), "sessions.json")
					if _, err := os.Stat(dbFile); err == nil {
						profileCount++
						if s, err := session.NewStorageWithProfile(entry.Name()); err == nil {
							if instances, _, err := s.LoadWithGroups(); err == nil {
								sessionCount += len(instances)
							}
						}
					} else if data, err := os.ReadFile(jsonFile); err == nil {
						profileCount++
						sessionCount += strings.Count(string(data), `"id"`)
					}
				}
			}
		}

		// Get total size
		var totalSize int64
		_ = filepath.Walk(dataDir, func(_ string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				totalSize += info.Size()
			}
			return nil
		})
		sizeStr := formatSize(totalSize)

		foundItems = append(
			foundItems,
			foundItem{
				"data",
				dataDir,
				fmt.Sprintf("%d profiles, %d sessions, %s", profileCount, sessionCount, sizeStr),
			},
		)
		fmt.Printf("Found: Data directory at %s\n", dataDir)
		fmt.Printf("       %d profiles, %d sessions, %s\n", profileCount, sessionCount, sizeStr)
	}

	// Check for tmux config
	tmuxConf := filepath.Join(homeDir, ".tmux.conf")
	if data, err := os.ReadFile(tmuxConf); err == nil {
		if strings.Contains(string(data), "# agent-deck configuration") {
			foundItems = append(foundItems, foundItem{"tmux", tmuxConf, "tmux configuration block"})
			fmt.Println("Found: tmux configuration in ~/.tmux.conf")
		}
	}

	fmt.Println()

	// Nothing found?
	if len(foundItems) == 0 {
		fmt.Println("Agent Deck does not appear to be installed.")
		fmt.Println()
		fmt.Println("Checked locations:")
		for _, loc := range binaryLocations {
			fmt.Printf("  - %s\n", loc)
		}
		fmt.Printf("  - %s\n", dataDir)
		fmt.Printf("  - %s (for agent-deck config)\n", tmuxConf)
		return
	}

	// Summary of what will be removed
	fmt.Println("The following will be removed:")
	fmt.Println()

	for _, item := range foundItems {
		switch item.itemType {
		case "homebrew":
			fmt.Println("  • Homebrew package: agent-deck")
		case "binary", "binary-symlink":
			fmt.Printf("  • Binary: %s\n", item.path)
		case "data":
			if *keepData {
				fmt.Printf("  ○ Data directory: %s (keeping)\n", item.path)
			} else {
				fmt.Printf("  • Data directory: %s\n", item.path)
				fmt.Println("    Including: sessions, logs, config")
			}
		case "tmux":
			if *keepTmuxConfig {
				fmt.Println("  ○ tmux config: ~/.tmux.conf (keeping)")
			} else {
				fmt.Println("  • tmux config block in ~/.tmux.conf")
			}
		}
	}

	fmt.Println()

	// Confirm unless -y flag
	if !*yes && !*dryRun {
		fmt.Print("Proceed with uninstall? [y/N] ")
		var response string
		_, _ = fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			fmt.Println("Uninstall cancelled.")
			return
		}
		fmt.Println()
	}

	// Dry run stops here
	if *dryRun {
		fmt.Println("Dry run complete. No changes made.")
		return
	}

	fmt.Println("Uninstalling...")
	fmt.Println()

	// Track the current binary path for self-deletion at the end
	currentBinary, _ := os.Executable()
	currentBinary, _ = filepath.EvalSymlinks(currentBinary)

	// 1. Homebrew
	if homebrewInstalled {
		fmt.Println("Removing Homebrew package...")
		cmd := exec.Command("brew", "uninstall", "agent-deck")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("Warning: failed to uninstall via Homebrew: %v\n", err)
		} else {
			fmt.Println("✓ Homebrew package removed")
		}
	}

	// 2. Binary files
	for _, item := range foundItems {
		if item.itemType != "binary" && item.itemType != "binary-symlink" {
			continue
		}

		fmt.Printf("Removing binary at %s...\n", item.path)

		// Resolve symlink to check if it points to current binary
		realPath, _ := filepath.EvalSymlinks(item.path)

		// Check if we need sudo
		dir := filepath.Dir(item.path)
		testFile := filepath.Join(dir, ".agent-deck-write-test")
		if f, err := os.Create(testFile); err != nil {
			// Need elevated permissions
			fmt.Printf("Requires sudo to remove %s\n", item.path)
			cmd := exec.Command("sudo", "rm", "-f", item.path)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Printf("Warning: failed to remove %s: %v\n", item.path, err)
			} else {
				fmt.Printf("✓ Binary removed: %s\n", item.path)
			}
		} else {
			f.Close()
			os.Remove(testFile)

			// Skip if this is our own binary (delete last)
			if realPath == currentBinary {
				continue
			}

			if err := os.Remove(item.path); err != nil {
				fmt.Printf("Warning: failed to remove %s: %v\n", item.path, err)
			} else {
				fmt.Printf("✓ Binary removed: %s\n", item.path)
			}
		}
	}

	// 3. tmux config
	if !*keepTmuxConfig {
		for _, item := range foundItems {
			if item.itemType != "tmux" {
				continue
			}

			fmt.Println("Removing tmux configuration...")

			data, err := os.ReadFile(tmuxConf)
			if err != nil {
				fmt.Printf("Warning: failed to read tmux config: %v\n", err)
				continue
			}

			// Create backup
			backupPath := tmuxConf + ".bak.agentdeck-uninstall"
			if err := os.WriteFile(backupPath, data, 0o644); err != nil {
				fmt.Printf("Warning: failed to create backup: %v\n", err)
			}

			// Remove the agent-deck config block
			content := string(data)
			startMarker := "# agent-deck configuration"
			endMarker := "# End agent-deck configuration"

			startIdx := strings.Index(content, startMarker)
			endIdx := strings.Index(content, endMarker)

			if startIdx != -1 && endIdx != -1 {
				// Include the end marker line in removal
				endIdx += len(endMarker)
				// Also remove trailing newline
				if endIdx < len(content) && content[endIdx] == '\n' {
					endIdx++
				}

				newContent := content[:startIdx] + content[endIdx:]
				// Clean up multiple blank lines
				for strings.Contains(newContent, "\n\n\n") {
					newContent = strings.ReplaceAll(newContent, "\n\n\n", "\n\n")
				}
				newContent = strings.TrimRight(newContent, "\n") + "\n"

				if err := os.WriteFile(tmuxConf, []byte(newContent), 0o644); err != nil {
					fmt.Printf("Warning: failed to update tmux config: %v\n", err)
				} else {
					fmt.Printf("✓ tmux configuration removed (backup: %s)\n", backupPath)
				}
			}
		}
	}

	// 4. Data directory
	if !*keepData {
		for _, item := range foundItems {
			if item.itemType != "data" {
				continue
			}

			// Offer backup unless -y flag
			if !*yes {
				fmt.Print("Create backup of data before removing? [Y/n] ")
				var response string
				_, _ = fmt.Scanln(&response)
				if strings.ToLower(response) != "n" {
					backupFile := filepath.Join(
						homeDir,
						fmt.Sprintf("agent-deck-backup-%s.tar.gz", time.Now().Format("20060102-150405")),
					)
					fmt.Printf("Creating backup at %s...\n", backupFile)

					cmd := exec.Command("tar", "-czf", backupFile, "-C", homeDir, ".agent-deck")
					if err := cmd.Run(); err != nil {
						fmt.Printf("Warning: failed to create backup: %v\n", err)
					} else {
						fmt.Printf("✓ Backup created: %s\n", backupFile)
					}
				}
			}

			fmt.Println("Removing data directory...")
			if err := os.RemoveAll(dataDir); err != nil {
				fmt.Printf("Warning: failed to remove data directory: %v\n", err)
			} else {
				fmt.Printf("✓ Data directory removed: %s\n", dataDir)
			}
		}
	}

	fmt.Println()
	fmt.Println("╔════════════════════════════════════════╗")
	fmt.Println("║     Uninstall complete!                ║")
	fmt.Println("╚════════════════════════════════════════╝")
	fmt.Println()

	if *keepData {
		fmt.Printf("Note: Data directory preserved at %s\n", dataDir)
		fmt.Println("      Remove manually with: rm -rf ~/.agent-deck")
	}

	if *keepTmuxConfig {
		fmt.Println("Note: tmux config preserved in ~/.tmux.conf")
		fmt.Println("      Remove the '# agent-deck configuration' block manually if desired")
	}

	fmt.Println()
	fmt.Println("Thank you for using Agent Deck!")
	fmt.Println("Feedback: https://github.com/asheshgoplani/agent-deck/issues")
}

// isNestedSession returns true if we're running inside an agent-deck managed tmux session.
// Uses GetCurrentSessionID() which checks if the current tmux session name matches agentdeck_*.
func isNestedSession() bool {
	return GetCurrentSessionID() != ""
}

// isOuterTmuxWithoutOptIn reports true when the user is launching the
// interactive TUI from inside a NON-agentdeck tmux session without the
// AGENT_DECK_ALLOW_OUTER_TMUX=1 opt-in. See issue #560: nesting the TUI
// inside an outer tmux leads to confusing detach semantics (Ctrl+Q returns
// to the outer tmux, not a clean shell). The guard fires only on the TUI
// path — CLI subcommands remain usable inside tmux.
func isOuterTmuxWithoutOptIn() bool {
	if os.Getenv("TMUX") == "" {
		return false
	}
	if isNestedSession() {
		return false
	}
	if os.Getenv("AGENT_DECK_ALLOW_OUTER_TMUX") == "1" {
		return false
	}
	return true
}

// ensureTmuxInPath checks that tmux is reachable. If exec.LookPath fails
// (common when the Go binary inherits a minimal PATH from a desktop launcher,
// systemd unit, or non-login shell), it probes well-known installation
// directories. When tmux is found via fallback, the containing directory is
// prepended to PATH so every subsequent exec.Command("tmux", …) succeeds.
func ensureTmuxInPath() error {
	if _, err := exec.LookPath("tmux"); err == nil {
		return nil
	}

	// Well-known paths where tmux is commonly installed.
	fallbacks := []string{
		"/usr/bin/tmux",
		"/usr/local/bin/tmux",
		"/opt/homebrew/bin/tmux",
		"/home/linuxbrew/.linuxbrew/bin/tmux",
		"/snap/bin/tmux",
	}

	for _, p := range fallbacks {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		// Must be a regular file (or symlink target) with at least one execute bit.
		if info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			dir := filepath.Dir(p)
			_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			return nil
		}
	}

	return fmt.Errorf("tmux not found in PATH or common locations")
}

// formatSize formats bytes into human-readable size
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
