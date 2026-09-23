package main

import (
	"bufio"
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
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/harness"
	"github.com/asheshgoplani/agent-deck/internal/quota"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// harnessAccountJSON is one named account of a harness.
type harnessAccountJSON struct {
	Name      string `json:"name"`
	ConfigDir string `json:"config_dir"`
	LoggedIn  *bool  `json:"logged_in"`
}

// harnessJSON is one `harness list --json` entry (macapp-core-needs §5).
type harnessJSON struct {
	harness.Entry
	Installed      bool                 `json:"installed"`
	Path           string               `json:"path,omitempty"`
	Version        string               `json:"version,omitempty"`
	LoggedIn       *bool                `json:"logged_in"`
	Accounts       []harnessAccountJSON `json:"accounts"`
	HooksInstalled *bool                `json:"hooks_installed"`
	LastUsed       string               `json:"last_used,omitempty"`
	Sessions       int                  `json:"sessions"`
	LimitReached   bool                 `json:"limit_reached"`
	State          string               `json:"state"` // ready, not_logged_in, not_installed, limit_reached
}

func boolPtr(b bool) *bool { return &b }

var versionRe = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?(?:[-+][0-9A-Za-z.-]+)?`)

// harnessVersion runs `<binary> --version` with a short timeout.
func harnessVersion(path string, args []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "CI=1")
	out, _ := cmd.CombinedOutput()
	if m := versionRe.Find(out); m != nil {
		return string(m)
	}
	return strings.TrimSpace(firstLineOf(string(out)))
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func nonEmptyFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// claudeLoggedIn reads the account marker Claude Code keeps next to its
// config: .claude.json in the config dir (or ~/.claude.json for the default
// dir) carries oauthAccount after /login. The token itself may live in the
// keychain, which this never reads.
func claudeLoggedIn(configDir string) *bool {
	home, _ := os.UserHomeDir()
	candidates := []string{filepath.Join(configDir, ".claude.json")}
	if def := filepath.Join(home, ".claude"); filepath.Clean(configDir) == def || configDir == "" {
		candidates = append(candidates, filepath.Join(home, ".claude.json"))
	}
	for _, c := range candidates {
		b, err := os.ReadFile(c)
		if err != nil {
			continue
		}
		var v struct {
			OAuth map[string]any `json:"oauthAccount"`
		}
		if json.Unmarshal(b, &v) == nil {
			return boolPtr(len(v.OAuth) > 0 || nonEmptyFile(filepath.Join(configDir, ".credentials.json")))
		}
	}
	if nonEmptyFile(filepath.Join(configDir, ".credentials.json")) {
		return boolPtr(true)
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return boolPtr(true)
	}
	return boolPtr(false)
}

func codexLoggedIn(codexHome string) *bool {
	return boolPtr(nonEmptyFile(filepath.Join(codexHome, "auth.json")) || os.Getenv("OPENAI_API_KEY") != "")
}

// loginFacts answers logged_in and the per-account list for one harness.
func loginFacts(name string, cfg *session.UserConfig) (*bool, []harnessAccountJSON) {
	home, _ := os.UserHomeDir()
	accounts := []harnessAccountJSON{}
	switch name {
	case "claude":
		defDir := session.GetClaudeConfigDir()
		logged := claudeLoggedIn(defDir)
		for _, a := range configuredAccountSlotsForHarness(cfg, "claude") {
			li := claudeLoggedIn(a.ConfigDir)
			accounts = append(accounts, harnessAccountJSON{Name: a.Name, ConfigDir: a.ConfigDir, LoggedIn: li})
			if *li {
				logged = li
			}
		}
		return logged, accounts
	case "codex":
		defHome := os.Getenv("CODEX_HOME")
		if defHome == "" {
			defHome = filepath.Join(home, ".codex")
			if cfg != nil && cfg.Codex.ConfigDir != "" {
				defHome = session.ExpandPath(cfg.Codex.ConfigDir)
			}
		}
		logged := codexLoggedIn(defHome)
		for _, a := range configuredAccountSlotsForHarness(cfg, "codex") {
			li := codexLoggedIn(a.ConfigDir)
			accounts = append(accounts, harnessAccountJSON{Name: a.Name, ConfigDir: a.ConfigDir, LoggedIn: li})
			if *li {
				logged = li
			}
		}
		return logged, accounts
	case "gemini":
		dir := session.GetGeminiConfigDir()
		return boolPtr(nonEmptyFile(filepath.Join(dir, "oauth_creds.json")) || os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("GOOGLE_API_KEY") != ""), accounts
	case "opencode":
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		return boolPtr(nonEmptyFile(filepath.Join(data, "opencode", "auth.json"))), accounts
	case "pi":
		return boolPtr(nonEmptyFile(filepath.Join(home, ".pi", "agent", "auth.json"))), accounts
	case "hermes":
		dir := session.GetHermesConfigDir()
		return boolPtr(nonEmptyFile(filepath.Join(dir, ".env")) || nonEmptyFile(filepath.Join(dir, "auth.json"))), accounts
	}
	return nil, accounts
}

// hooksFacts reports whether agent-deck's status hooks are installed for the
// harness's default config (nil when the harness has no hook integration).
func hooksFacts(name string) *bool {
	home, _ := os.UserHomeDir()
	switch name {
	case "claude":
		return boolPtr(session.CheckClaudeHooksInstalled(session.GetClaudeConfigDir()))
	case "codex":
		codexHome := os.Getenv("CODEX_HOME")
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
		return boolPtr(codexHooksStateForConfig(filepath.Join(codexHome, "config.toml")) == codexHooksInstalled)
	case "gemini":
		return boolPtr(session.CheckGeminiHooksInstalled(session.GetGeminiConfigDir()))
	case "hermes":
		return boolPtr(session.CheckHermesHooksInstalled(session.GetHermesConfigDir()))
	case "pi":
		return boolPtr(session.CheckPiHooksInstalled(session.PiExtensionsDir()))
	}
	return nil
}

// harnessUsage is the last time a session of each harness was used and how
// many sessions exist, from the profile's store.
func harnessUsage(profile string) (map[string]time.Time, map[string]int) {
	last, count := map[string]time.Time{}, map[string]int{}
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		return last, count
	}
	for _, inst := range instances {
		name := rowsHarness(inst.Tool)
		count[name]++
		t := inst.LastAccessedAt
		if t.IsZero() {
			t = inst.CreatedAt
		}
		if t.After(last[name]) {
			last[name] = t
		}
	}
	return last, count
}

func collectHarnesses(profile string, only string) []harnessJSON {
	cfg, _ := session.LoadUserConfig()
	var overrides map[string]harness.Override
	if cfg != nil {
		overrides = cfg.Harnesses
	}
	last, count := harnessUsage(profile)
	limited := limitReachedByHarness()
	var out []harnessJSON
	for _, e := range harness.All(overrides) {
		if only != "" && e.Name != only {
			continue
		}
		h := harnessJSON{Entry: e, Accounts: []harnessAccountJSON{}}
		if p, err := exec.LookPath(e.Binary); err == nil {
			h.Installed, h.Path = true, p
			h.Version = harnessVersion(p, e.VersionArgs)
		}
		h.LoggedIn, h.Accounts = loginFacts(e.Name, cfg)
		h.HooksInstalled = hooksFacts(e.Name)
		if t, ok := last[e.Name]; ok && !t.IsZero() {
			h.LastUsed = t.UTC().Format(time.RFC3339)
		}
		h.Sessions = count[e.Name]
		h.LimitReached = limited[e.Name]
		switch {
		case !h.Installed:
			h.State = "not_installed"
		case h.LoggedIn != nil && !*h.LoggedIn:
			h.State = "not_logged_in"
		case h.LimitReached:
			h.State = "limit_reached"
		default:
			h.State = "ready"
		}
		out = append(out, h)
	}
	return out
}

func handleHarness(profile string, args []string) {
	usage := func() {
		fmt.Println("Usage: agent-deck harness list [--json]")
		fmt.Println("       agent-deck harness status <name> [--json]")
		fmt.Println()
		fmt.Println("Every harness agent-deck runs (claude, codex, gemini, opencode, pi, hermes) with:")
		fmt.Println("installed, path, version, logged_in, accounts, hooks_installed, last_used, sessions,")
		fmt.Println("state (ready | not_logged_in | not_installed | limit_reached) and the official")
		fmt.Println("install_command, login_command and docs_url from the core table (internal/harness),")
		fmt.Println("overridable under [harnesses.<name>] in config.toml. Read-only.")
	}
	if len(args) == 0 || helpRequested(args[:1]) {
		usage()
		if len(args) == 0 {
			os.Exit(2)
		}
		return
	}
	fs := flag.NewFlagSet("harness "+args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = usage
	if err := fs.Parse(normalizeArgs(fs, args[1:])); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	switch args[0] {
	case "list":
		list := collectHarnesses(profile, "")
		if *jsonOutput {
			printJSONValue(list)
			return
		}
		for _, h := range list {
			fmt.Printf("%-9s %-14s %-10s %s\n", h.Name, h.State, h.Version, firstNonEmpty(h.Path, h.InstallCommand))
		}
	case "status":
		if fs.NArg() != 1 {
			usage()
			os.Exit(2)
		}
		list := collectHarnesses(profile, fs.Arg(0))
		if len(list) == 0 {
			NewCLIOutput(*jsonOutput, false).Error(fmt.Sprintf("unknown harness %q (known: %s)", fs.Arg(0), strings.Join(harness.Names(), ", ")), ErrCodeNotFound)
			os.Exit(2)
		}
		h := list[0]
		if *jsonOutput {
			printJSONValue(h)
			return
		}
		fmt.Printf("%s: %s\n", h.DisplayName, h.State)
		fmt.Printf("  path:    %s\n  version: %s\n", h.Path, h.Version)
		if !h.Installed {
			fmt.Printf("  install: %s\n", h.InstallCommand)
		}
		if h.LoggedIn != nil && !*h.LoggedIn {
			fmt.Printf("  login:   %s\n", h.LoginCommand)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func printJSONValue(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: encode JSON: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(b))
}

// ---- limits ------------------------------------------------------------

type limitWindowJSON struct {
	Window   string  `json:"window"`
	UsedPct  float64 `json:"used_pct"`
	ResetsAt string  `json:"resets_at,omitempty"`
}

type limitAccountJSON struct {
	Harness   string            `json:"harness"`
	Name      string            `json:"name"`
	Windows   []limitWindowJSON `json:"windows"`
	Source    string            `json:"source"`
	UpdatedAt string            `json:"updated_at,omitempty"`
	Stale     bool              `json:"stale"`
	Error     string            `json:"error,omitempty"`
}

func epochRFC3339(sec *int64) string {
	if sec == nil || *sec <= 0 {
		return ""
	}
	return time.Unix(*sec, 0).UTC().Format(time.RFC3339)
}

// claudeLimits reads each Claude account's quota cache (fed by the
// statusLine ingester `agent-deck hooks install` wires).
func claudeLimits(cfg *session.UserConfig, now time.Time) []limitAccountJSON {
	var out []limitAccountJSON
	for _, a := range configuredAccountSlotsForHarness(cfg, "claude") {
		acc := limitAccountJSON{Harness: "claude", Name: a.Name, Windows: []limitWindowJSON{}, Source: "quota cache (statusLine ingester)"}
		store, err := quota.NewStore(a.Name)
		if err == nil {
			snaps, _ := store.Load()
			for _, s := range snaps {
				if s.ID != quota.ProviderClaude {
					continue
				}
				acc.Error = s.Error
				if s.UpdatedAt > 0 {
					acc.UpdatedAt = time.Unix(s.UpdatedAt, 0).UTC().Format(time.RFC3339)
					acc.Stale = session.AccountUsageStale(true, time.Unix(s.UpdatedAt, 0), now)
				}
				for _, w := range s.Windows {
					label := map[quota.WindowKind]string{quota.WindowFiveHour: "5h", quota.WindowSevenDay: "7d"}[w.Kind]
					if label == "" {
						label = w.Label
					}
					acc.Windows = append(acc.Windows, limitWindowJSON{Window: label, UsedPct: w.UsedPercentage, ResetsAt: epochRFC3339(w.ResetsAt)})
				}
			}
		}
		if len(acc.Windows) == 0 && acc.Error == "" {
			if !session.UsageFeedStatus(a.ConfigDir, a.Name).Wired {
				acc.Error = "no feed: run agent-deck hooks install"
			} else {
				acc.Error = "no data yet"
			}
		}
		out = append(out, acc)
	}
	return out
}

// codexRateLimits finds the newest `token_count` frame with rate_limits in
// a Codex home's most recently written rollouts. That frame is where Codex
// itself reads the weekly figure its footer shows.
func codexRateLimits(codexHome string) (windows []limitWindowJSON, updated time.Time, found bool) {
	files, _ := filepath.Glob(filepath.Join(codexHome, "sessions", "*", "*", "*", "rollout-*.jsonl"))
	type fi struct {
		p string
		t time.Time
	}
	var list []fi
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			list = append(list, fi{f, info.ModTime()})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].t.After(list[j].t) })
	for i, f := range list {
		if i >= 5 {
			break
		}
		if w, ok := lastRateLimits(f.p); ok {
			return w, f.t, true
		}
	}
	return nil, time.Time{}, false
}

func lastRateLimits(path string) ([]limitWindowJSON, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	info, _ := file.Stat()
	const tail = 1 << 20
	start := int64(0)
	if info != nil && info.Size() > tail {
		start = info.Size() - tail
	}
	sc := bufio.NewScanner(io.NewSectionReader(file, start, 1<<62))
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	var last []byte
	for sc.Scan() {
		if bytes.Contains(sc.Bytes(), []byte(`"rate_limits"`)) {
			last = append(last[:0], sc.Bytes()...)
		}
	}
	if last == nil {
		return nil, false
	}
	var rec struct {
		Payload struct {
			RateLimits map[string]json.RawMessage `json:"rate_limits"`
		} `json:"payload"`
	}
	if json.Unmarshal(last, &rec) != nil {
		return nil, false
	}
	var out []limitWindowJSON
	for _, key := range []string{"primary", "secondary"} {
		var w struct {
			UsedPercent   *float64 `json:"used_percent"`
			WindowMinutes int      `json:"window_minutes"`
			ResetsAt      *int64   `json:"resets_at"`
		}
		if raw, ok := rec.Payload.RateLimits[key]; !ok || json.Unmarshal(raw, &w) != nil || w.UsedPercent == nil {
			continue
		}
		label := fmt.Sprintf("%dm", w.WindowMinutes)
		switch w.WindowMinutes {
		case 10080:
			label = "weekly"
		case 300:
			label = "5h"
		}
		out = append(out, limitWindowJSON{Window: label, UsedPct: *w.UsedPercent, ResetsAt: epochRFC3339(w.ResetsAt)})
	}
	return out, len(out) > 0
}

func codexLimits(cfg *session.UserConfig, now time.Time) []limitAccountJSON {
	home, _ := os.UserHomeDir()
	type slot struct{ name, dir string }
	slots := []slot{}
	seen := map[string]bool{}
	add := func(name, dir string) {
		if dir == "" || seen[filepath.Clean(dir)] {
			return
		}
		seen[filepath.Clean(dir)] = true
		slots = append(slots, slot{name, dir})
	}
	for _, a := range configuredAccountSlotsForHarness(cfg, "codex") {
		add(a.Name, a.ConfigDir)
	}
	if env := os.Getenv("CODEX_HOME"); env != "" {
		add("default", env)
	}
	add("default", filepath.Join(home, ".codex"))
	var out []limitAccountJSON
	for _, s := range slots {
		if info, err := os.Stat(s.dir); err != nil || !info.IsDir() {
			continue
		}
		acc := limitAccountJSON{Harness: "codex", Name: s.name, Windows: []limitWindowJSON{}, Source: "last token_count in " + filepath.Join(s.dir, "sessions")}
		if w, t, ok := codexRateLimits(s.dir); ok {
			acc.Windows = w
			acc.UpdatedAt = t.UTC().Format(time.RFC3339)
			acc.Stale = now.Sub(t) > session.AccountUsageStaleAfter
		} else {
			acc.Error = "no rate-limit frame in recent rollouts"
		}
		out = append(out, acc)
	}
	return out
}

// limitReachedByHarness marks a harness whose every known window of every
// account is fresh and at or above 100%.
func limitReachedByHarness() map[string]bool {
	cfg, _ := session.LoadUserConfig()
	now := time.Now()
	out := map[string]bool{}
	for _, accs := range [][]limitAccountJSON{claudeLimits(cfg, now), codexLimits(cfg, now)} {
		if len(accs) == 0 {
			continue
		}
		all := true
		for _, a := range accs {
			hit := false
			for _, w := range a.Windows {
				if w.UsedPct >= 100 && !a.Stale {
					hit = true
				}
			}
			all = all && hit
		}
		out[accs[0].Harness] = all
	}
	return out
}

func handleLimits(args []string) {
	fs := flag.NewFlagSet("limits", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck limits [--json]")
		fmt.Println()
		fmt.Println("Usage limits per account: Claude 5h and 7d windows per account slot (from the quota")
		fmt.Println("cache the statusLine ingester fills; `agent-deck hooks install` wires it) and Codex")
		fmt.Println("weekly/5h windows per CODEX_HOME (from the newest token_count frame in its rollouts),")
		fmt.Println("each with used_pct and resets_at. Needs [macapp] plugins = true (docs/macapp-core.md).")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	out := NewCLIOutput(*jsonOutput, false)
	cfg, _ := session.LoadUserConfig()
	if cfg == nil || !cfg.Macapp.Plugins {
		out.Error("limits is off: set [macapp] plugins = true in config.toml (docs/macapp-core.md)", ErrCodeInvalidOperation)
		os.Exit(2)
	}
	now := time.Now()
	accounts := append(claudeLimits(cfg, now), codexLimits(cfg, now)...)
	if accounts == nil {
		accounts = []limitAccountJSON{}
	}
	if *jsonOutput {
		printJSONValue(map[string]any{"accounts": accounts})
		return
	}
	for _, a := range accounts {
		var parts []string
		for _, w := range a.Windows {
			parts = append(parts, fmt.Sprintf("%s %.0f%%", w.Window, w.UsedPct))
		}
		if a.Error != "" {
			parts = append(parts, a.Error)
		}
		fmt.Printf("%-7s %-12s %s\n", a.Harness, a.Name, strings.Join(parts, "  "))
	}
}
