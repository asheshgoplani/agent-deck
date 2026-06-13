// Package main — `agent-deck creds-refresh` subcommand.
//
// The keep-warm OAuth refresh daemon (Part B of the recurring "/login" outage
// fix). It keeps each profile's canonical `.credentials.json` token fresh so
// the N symlinked worker sessions never hit an expired access token and never
// race Anthropic's single-use rotating refresh token. See the package doc in
// internal/credrefresh and /tmp/oauth-fix/SUBSCRIPTION-FIX.md.
//
// Subscription-safe: it uses the profile's existing OAuth refresh token and
// clientId — NO API key, no per-token billing. It exchanges the refresh token
// at the OAuth /token endpoint and atomically rewrites canonical under the same
// proper-lockfile Claude uses, so a running session and the daemon never
// refresh at the same instant.
//
// Usage:
//
//	agent-deck creds-refresh --once                 # single pass, exit (cron-friendly)
//	agent-deck creds-refresh                         # run forever (systemd --user)
//	agent-deck creds-refresh --config-dir ~/.claude --config-dir ~/.claude-work
//	agent-deck creds-refresh --interval 25m --threshold 20m
//
// Default config dirs (when none given): the legacy ~/.claude + ~/.claude-work
// pair PLUS every Claude config_dir declared in config.toml ([claude],
// [profiles.*.claude], [groups.*.claude], [conductors.*.claude]) — whichever
// of those has a .credentials.json present, deduplicated by canonical path.
// Issue #1414: extra account profiles (e.g. ~/.claude-seminno) used to be
// silently excluded from keep-warm coverage.
//
// NOTE: not started automatically anywhere. Enable it deliberately via the
// systemd --user unit in scripts/systemd/ (see that file's header).

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/credrefresh"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// stringSliceFlag collects a repeatable string flag (--config-dir a --config-dir b).
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func handleCredsRefresh(args []string) {
	fs := flag.NewFlagSet("creds-refresh", flag.ExitOnError)
	once := fs.Bool("once", false, "run a single refresh pass and exit (cron-friendly)")
	interval := fs.Duration("interval", credrefresh.DefaultInterval, "refresh cadence when running as a daemon")
	threshold := fs.Duration("threshold", credrefresh.DefaultThreshold, "refresh when the access token expires within this window")
	endpoint := fs.String("endpoint", credrefresh.DefaultTokenEndpoint, "OAuth token endpoint (override for testing only)")
	var configDirs stringSliceFlag
	fs.Var(&configDirs, "config-dir", "profile config dir to keep warm (repeatable); defaults to ~/.claude, ~/.claude-work and every config_dir declared in config.toml")
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	// Only consult config.toml when the operator did not pin the set. The
	// load is read-only (LoadUserConfig never writes a default file); on
	// error the returned default config simply yields the legacy pair.
	var userCfg *session.UserConfig
	if len(configDirs) == 0 {
		userCfg, _ = session.LoadUserConfig()
	}
	dirs := resolveCredConfigDirs(configDirs, userCfg)
	if len(dirs) == 0 {
		fmt.Fprintln(os.Stderr, "creds-refresh: no profile config dir with a .credentials.json found; pass --config-dir explicitly")
		os.Exit(1)
	}

	cfg := credrefresh.DaemonConfig{
		ConfigDirs: dirs,
		Interval:   *interval,
		Refresh: credrefresh.RefreshConfig{
			TokenEndpoint: *endpoint,
			Threshold:     *threshold,
		},
		OnResult: logCredsRefreshResult,
	}

	if *once {
		// Single pass: a tick with an already-cancelled context returns after
		// the immediate refresh.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		credrefresh.Run(ctx, cfg)
		return
	}

	fmt.Fprintf(os.Stderr, "creds-refresh: keeping %d profile(s) warm every %s (threshold %s)\n", len(dirs), *interval, *threshold)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	credrefresh.Run(ctx, cfg)
}

// resolveCredConfigDirs expands the explicit --config-dir flags, or derives
// the default keep-warm set from the agent-deck user config plus the legacy
// dual-profile pair. Only dirs that actually hold a .credentials.json survive
// (so a declared-but-never-logged-in profile doesn't error every tick), and
// aliases of one canonical (symlinked profile dirs) are deduplicated so each
// canonical is refreshed exactly once per tick.
//
// Issue #1414: the defaults were hardcoded to ~/.claude + ~/.claude-work, so
// hosts with extra account profiles in config.toml (e.g. ~/.claude-seminno)
// silently left those canonicals un-warmed — their concurrent sessions kept
// racing the single-use rotating refresh token (mid-turn 401 → "/login").
func resolveCredConfigDirs(explicit stringSliceFlag, cfg *session.UserConfig) []string {
	candidates := []string(explicit)
	if len(candidates) == 0 {
		candidates = credConfigDirCandidates(cfg)
	}
	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, d := range candidates {
		dir := session.ExpandPath(d)
		if _, err := os.Stat(credrefresh.CanonicalCredPath(dir)); err != nil {
			continue
		}
		key := dir
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			key = resolved
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, dir)
	}
	return out
}

// credConfigDirCandidates lists every Claude config dir agent-deck could
// spawn a session into: the legacy ~/.claude + ~/.claude-work pair, the
// global [claude].config_dir, and every [profiles.*.claude],
// [groups.*.claude] and [conductors.*.claude] config_dir override. Map keys
// are walked in sorted order so the daemon's startup log is deterministic.
func credConfigDirCandidates(cfg *session.UserConfig) []string {
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".claude"),
			filepath.Join(home, ".claude-work"),
		)
	}
	if cfg == nil {
		return candidates
	}
	if cfg.Claude.ConfigDir != "" {
		candidates = append(candidates, cfg.Claude.ConfigDir)
	}
	for _, name := range sortedKeys(cfg.Profiles) {
		if d := cfg.Profiles[name].Claude.ConfigDir; d != "" {
			candidates = append(candidates, d)
		}
	}
	for _, name := range sortedKeys(cfg.Groups) {
		if d := cfg.Groups[name].Claude.ConfigDir; d != "" {
			candidates = append(candidates, d)
		}
	}
	for _, name := range sortedKeys(cfg.Conductors) {
		if d := cfg.Conductors[name].Claude.ConfigDir; d != "" {
			candidates = append(candidates, d)
		}
	}
	return candidates
}

// sortedKeys returns the map's keys in ascending order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func logCredsRefreshResult(credPath string, res credrefresh.RefreshResult, err error) {
	ts := time.Now().Format(time.RFC3339)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "%s creds-refresh ERROR %s: %v\n", ts, credPath, err)
	case res.Refreshed:
		fmt.Fprintf(os.Stderr, "%s creds-refresh OK %s rotated; next expiry %s\n", ts, credPath, res.ExpiresAt.Format(time.RFC3339))
	default:
		fmt.Fprintf(os.Stderr, "%s creds-refresh skip %s (%s)\n", ts, credPath, res.Reason)
	}
}
