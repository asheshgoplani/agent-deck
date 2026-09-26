package telemetry

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
)

// Kind is the type of a published property.
type Kind string

const (
	KindEnum    Kind = "enum"    // one of Values
	KindBucket  Kind = "bucket"  // a label of Bucket
	KindBool    Kind = "bool"    // JSON boolean
	KindBitmask Kind = "bitmask" // non-negative integer below 2^Bits
	KindHash    Kind = "hash"    // HexLen lowercase hex characters from a salted local hash
	KindPattern Kind = "pattern" // a short string matching Pattern (versions, ISO week)
	KindInt     Kind = "int"     // integer in [Min, Max]
)

// Prop is one published property of an event.
type Prop struct {
	Key     string
	Kind    Kind
	Values  []string
	Bucket  Bucket
	Bits    int
	HexLen  int
	Pattern *regexp.Regexp
	Min     int
	Max     int
	Doc     string
}

// EventDef is one published event.
type EventDef struct {
	Name     string
	Tier     int
	Ships    string // release carrying its call sites
	Basic    bool   // also recorded at level "basic"
	Rollup   bool   // built from local daily counters at upload time
	Props    []Prop
	Emitted  string
	Question string
}

const (
	shipsNow   = "1.16.18"
	shipsLater = "1.16.19+"
	toolOther  = "other"
)

var (
	reMinor   = regexp.MustCompile(`^([0-9]{1,3}\.[0-9]{1,3}|other)$`)
	reVersion = regexp.MustCompile(`^([0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}|dev)$`)
	reDay     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	reWeek    = regexp.MustCompile(`^[0-9]{4}-W[0-9]{2}$`)
)

func enum(key string, values ...string) Prop { return Prop{Key: key, Kind: KindEnum, Values: values} }
func bucket(key string, b Bucket) Prop       { return Prop{Key: key, Kind: KindBucket, Bucket: b} }
func boolean(key string) Prop                { return Prop{Key: key, Kind: KindBool} }
func bitmask(key string, bits int) Prop      { return Prop{Key: key, Kind: KindBitmask, Bits: bits} }

func (p Prop) doc(d string) Prop { p.Doc = d; return p }

// Enum value tables. Each is the complete allow-list for its property.
var (
	toolValues      = append(append([]string{}, toolBits...), toolOther)
	startKinds      = []string{"first_ever", "normal", "after_update", "after_crash"}
	exitKinds       = []string{"quit", "signal", "update_restart", "panic"}
	createVias      = []string{"tui_new", "tui_fork", "tui_quick", "cli_add", "cli_launch", "try", "fleet", "conductor", "web"}
	endKinds        = []string{"stop", "delete", "tool_exit", "crash", "restart"}
	sendVias        = []string{"tui", "cli_send", "conductor", "inbox", "telegram", "web"}
	attachVias      = []string{"tui", "cli", "web", "remote"}
	errorAreas      = []string{"tmux", "session_start", "send", "worktree", "mcp", "remote", "update", "config", "hook", "db", "web", "conductor", "telemetry"}
	errorKinds      = []string{"tmux_missing", "tmux_too_old", "tool_not_found", "tool_auth", "worktree_dirty", "mcp_spawn_failed", "ssh_auth", "ssh_unreachable", "config_parse", "db_locked", "timeout", "permission", "disk_full", "panic", "other"}
	updateKinds     = []string{"auto", "manual", "timer", "remote_sweep"}
	updateOutcomes  = []string{"ok", "error", "rolled_back"}
	terminals       = []string{"iterm", "ghostty", "wezterm", "kitty", "apple", "alacritty", "vscode", "warp", "tmux_nested", "other"}
	shells          = []string{"zsh", "bash", "fish", "other"}
	installMethods  = []string{"brew", "go_install", "script", "release_tarball", "other"}
	colorModes      = []string{"truecolor", "256", "ascii"}
	consentSources  = []string{"tui_first_run", "tui_settings", "cli_on"}
	consentPrevious = []string{"none", "v1_granted", "v1_declined", "v1_undecided"}
	uninstallWhy    = []string{"not_needed", "too_complex", "bugs", "switching_tool", "skip"}
	outcomes        = []string{"ok", "error"}
	remoteOps       = []string{"add", "attach", "exec", "update_sweep", "drain"}
	worktreeOps     = []string{"create", "finish_merge", "finish_discard", "cleanup"}
	vcsKinds        = []string{"git", "jj"}
	extKinds        = []string{"mcp", "skill", "plugin"}
	extOps          = []string{"attach", "detach", "pool_add"}
	extSources      = []string{"builtin", "pool", "custom"}
	// extNames are well-known public server/skill names; everything else is "custom".
	extNames        = []string{"github", "playwright", "filesystem", "context7", "memory", "fetch", "sequential-thinking", "exa", "brave-search", "postgres", "slack", "notion", "custom"}
	switchTriggers  = []string{"rate_limit", "auth", "manual"}
	searchScopes    = []string{"sessions", "recall", "global"}
	tuiViews        = []string{"home", "new", "fork", "mcp", "skill", "group", "settings", "help", "worktree_finish", "session_picker", "costs", "usage", "recall", "inbox", "other"}
	keybindActions  = []string{"new_session", "quick_new", "fork", "delete", "restart", "rename", "move", "attach", "search", "filter", "group_create", "mcp_manager", "skill_manager", "settings", "help", "send", "preview_toggle", "worktree_finish", "collapse_group", "quit", "other"}
	perfOps         = []string{"tui_start", "session_start", "list", "status_poll"}
	panicTypes      = []string{"nil_deref", "index", "slice", "map_concurrent", "closed_chan", "custom", "other"}
	actorValues     = []string{"human", "agent"}
	osValues        = []string{"darwin", "linux", "freebsd", "openbsd", "netbsd", "windows", "other"}
	archValues      = []string{"amd64", "arm64", "386", "arm", "riscv64", "other"}
	surfaceValues   = []string{"tui", "cli", "web"}
	levelValues     = []string{"full", "basic"}
	onboardingSteps = append([]string{"none"}, milestoneNames[:6]...)
)

// FeatureValues is the feature enum (feature.daily).
var FeatureValues = []string{
	"fork", "restart", "restart_all", "rename", "move_group", "group_create", "search", "filter",
	"worktree_create", "worktree_finish", "mcp_attach", "mcp_detach", "skill_attach", "plugin_install",
	"session_send", "send_keys", "send_queue", "session_children", "session_handoff", "session_context",
	"session_annotate", "session_approve", "inbox_drain", "fleet_launch", "launch", "try", "conductor_start",
	"conductor_telegram", "watcher", "remote_add", "remote_attach", "remote_agent", "recall_search",
	"recall_timeline", "costs", "usage", "limits", "accounts_switch", "creds_refresh", "web_ui", "daemon",
	"notify_daemon", "openclaw", "deepseek", "harness", "doctor", "health", "update", "migrate_paths",
	"config_edit", "profile_switch", "theme_change", "feedback", "events", "revive", "session_cleanup",
	"window", "hooks_install", "other",
}

// milestoneNames is indexed by funnel bit (F0..F15).
var milestoneNames = []string{
	"first_run", "consented", "first_session_created", "first_session_running", "first_attach",
	"first_send", "second_session", "second_tool", "first_fork", "first_worktree", "first_mcp_attach",
	"first_group", "first_conductor", "first_remote", "first_fleet", "activated",
}

var (
	propTool      = enum("tool", toolValues...).doc("built-in tool, else other")
	propDSSession = Prop{Key: "ds_session", Kind: KindHash, HexLen: 16, Doc: "first 16 hex of HMAC-SHA256(local salt, session id); omitted at level basic"}
	propOutcome   = enum("outcome", outcomes...)
)

// Envelope lists the properties added to every event by the client.
var Envelope = []Prop{
	{Key: "install_id", Kind: KindHash, HexLen: 32, Doc: "random, created on consent, rotatable; sent as PostHog distinct_id"},
	{Key: "schema", Kind: KindInt, Min: SchemaVersion, Max: SchemaVersion, Doc: "constant"},
	{Key: "v", Kind: KindPattern, Pattern: reVersion, Doc: "release X.Y.Z or dev"},
	enum("os", osValues...).doc("Go GOOS; no OS version"),
	enum("arch", archValues...).doc("Go GOARCH"),
	{Key: "day", Kind: KindPattern, Pattern: reDay, Doc: "local YYYY-MM-DD"},
	{Key: "hour_local", Kind: KindInt, Min: 0, Max: 23, Doc: "local hour; omitted at level basic"},
	{Key: "weekday_local", Kind: KindInt, Min: 0, Max: 6, Doc: "0 = Sunday; omitted at level basic"},
	{Key: "seq", Kind: KindInt, Min: 0, Max: 1 << 30, Doc: "per-install counter, ordering only, resets on reset-id"},
	enum("actor", actorValues...).doc("human (TTY, not in a session) or agent (TTY inside an agent session)"),
	enum("surface", surfaceValues...),
	enum("level", levelValues...),
	bucket("install_age", BucketAge).doc("from the local first-seen day; the date is never sent"),
	{Key: "install_week", Kind: KindPattern, Pattern: reWeek, Doc: "ISO week of first seen, e.g. 2026-W39"},
	boolean("pre_v2").doc("install first seen before the v2 build"),
}

// Events is the complete schema 2 event table.
var Events = []EventDef{
	{Name: "app.start", Tier: 1, Ships: shipsNow, Basic: true,
		Props: []Prop{enum("start_kind", startKinds...), bucket("sessions_total", BucketN), bucket("groups", BucketN),
			bucket("profiles", BucketN), bucket("remotes", BucketN), bucket("conductors", BucketN)},
		Emitted: "each TUI start; each human CLI command at most once per local hour", Question: "DAU/WAU/MAU, versions, TUI vs CLI, configured fleet size"},
	{Name: "app.exit", Tier: 1, Ships: shipsNow,
		Props:   []Prop{bucket("open_dur", BucketDur), enum("exit_kind", exitKinds...)},
		Emitted: "TUI exit (spool write only, no network)", Question: "How long the TUI stays open"},
	{Name: "session.create", Tier: 1, Ships: shipsNow,
		Props: []Prop{propTool, enum("via", createVias...), boolean("worktree"), bucket("mcps", BucketN), bucket("skills", BucketN),
			boolean("in_group"), boolean("remote"), boolean("parented"), propDSSession},
		Emitted: "session creation", Question: "Harness preference, creation paths, worktree/MCP/skill adoption"},
	{Name: "session.end", Tier: 1, Ships: shipsNow,
		Props:   []Prop{propTool, enum("end_kind", endKinds...), bucket("lifetime", BucketDur), bucket("restarts", BucketN), propDSSession},
		Emitted: "stop, delete, restart", Question: "Session lengths per tool; crash rate per tool"},
	{Name: "activity.hourly", Tier: 1, Ships: shipsNow,
		Props: []Prop{bucket("running", BucketN), bucket("waiting", BucketN), bucket("idle", BucketN), bucket("error", BucketN),
			bucket("sampled_min", BucketN), boolean("human_active"), bitmask("tools_running", 32)},
		Emitted: "once per local hour in which a TUI was open (one sampling TUI per machine)", Question: "Active vs idle, hour x weekday heatmap, concurrency"},
	{Name: "usage.daily", Tier: 1, Ships: shipsNow, Basic: true, Rollup: true,
		Props: []Prop{bitmask("human_hours", 24), bitmask("agent_hours", 24), bucket("cli_cmds", BucketN), bucket("tui_starts", BucketN),
			bucket("sends", BucketN), bucket("attaches", BucketN), bucket("dropped", BucketN)},
		Emitted: "one per local day with activity, built at upload time", Question: "Activity timing incl. CLI-only users; cap health"},
	{Name: "feature.daily", Tier: 1, Ships: shipsNow, Rollup: true,
		Props:   []Prop{enum("feature", FeatureValues...), bucket("count", BucketN), bucket("errors", BucketN)},
		Emitted: "one per feature used that day", Question: "Feature adoption ranking"},
	{Name: "send.daily", Tier: 1, Ships: shipsNow, Rollup: true,
		Props:   []Prop{propTool, enum("via", sendVias...), bucket("count", BucketN), bucket("len_mode", BucketLen), bucket("queued", BucketN)},
		Emitted: "one per (tool, via) that day", Question: "How people talk to agents; automation vs manual"},
	{Name: "attach.daily", Tier: 1, Ships: shipsNow, Rollup: true,
		Props:   []Prop{propTool, enum("via", attachVias...), bucket("count", BucketN), bucket("total_dur", BucketDur)},
		Emitted: "one per (tool, via) that day", Question: "Time inside sessions vs on the dashboard"},
	{Name: "error", Tier: 1, Ships: shipsNow,
		Props: []Prop{enum("area", errorAreas...), enum("kind", errorKinds...), propTool, boolean("before_first_success"),
			enum("onboarding_step", onboardingSteps...)},
		Emitted: "per occurrence, deduped per (area, kind) per hour, max 20/day", Question: "Top errors, regressions per version"},
	{Name: "update", Tier: 1, Ships: shipsNow,
		Props: []Prop{{Key: "from_minor", Kind: KindPattern, Pattern: reMinor, Doc: "e.g. 1.16"}, {Key: "to_minor", Kind: KindPattern, Pattern: reMinor, Doc: "e.g. 1.16"},
			enum("kind", updateKinds...), enum("outcome", updateOutcomes...), boolean("restart")},
		Emitted: "after an update attempt", Question: "Update speed, failed updates"},
	{Name: "env.snapshot", Tier: 1, Ships: shipsNow, Basic: true,
		Props: []Prop{enum("terminal", terminals...), {Key: "tmux_minor", Kind: KindPattern, Pattern: reMinor, Doc: "e.g. 3.4, else other"},
			enum("shell", shells...), enum("install_method", installMethods...), enum("color", colorModes...),
			bitmask("tools_installed", 32).doc("tool bits found on PATH"), bitmask("config_sections", 32).doc("known config sections present")},
		Emitted: "once per local day, only from the TUI", Question: "Support matrix; tools installed vs used"},
	{Name: "telemetry.consent", Tier: 1, Ships: shipsNow,
		Props:   []Prop{enum("answer", "yes"), enum("source", consentSources...), enum("previous", consentPrevious...), enum("prompt_variant", "v2a")},
		Emitted: "on consent (queued like any event)", Question: "Where consent comes from; v1 re-consent"},
	{Name: "onboard.baseline", Tier: 1, Ships: shipsNow,
		Props: []Prop{enum("install_method", installMethods...), boolean("tmux_ok"), bitmask("tools_found", 32), boolean("had_config"),
			bitmask("milestones_before", 16), bucket("sessions_total", BucketN)},
		Emitted: "once, right after consent, computed from existing local state", Question: "What a new install looks like; how far upgraders got"},
	{Name: "onboard.milestone", Tier: 1, Ships: shipsNow,
		Props:   []Prop{enum("step", milestoneNames...), propTool, enum("via", createVias...), bucket("since", BucketSince), boolean("before_consent")},
		Emitted: "the first time each funnel step is reached", Question: "Time to first value, where people get stuck"},
	{Name: "uninstall", Tier: 1, Ships: shipsNow,
		Props:   []Prop{bucket("sessions_total", BucketN), enum("last_tool", toolValues...), enum("reason", uninstallWhy...)},
		Emitted: "agent-deck uninstall, only with consent; one synchronous send (2 s timeout)", Question: "Why people leave"},

	{Name: "fleet.launch", Tier: 2, Ships: shipsLater,
		Props: []Prop{bucket("children", BucketN), bitmask("tools_mix", 32), boolean("worktrees")}, Question: "Is fleet mode used, at what size"},
	{Name: "conductor.daily", Tier: 2, Ships: shipsLater, Rollup: true,
		Props: []Prop{bucket("conductors", BucketN), bucket("children", BucketN), bucket("heartbeats", BucketN), boolean("telegram")}, Question: "Conductor adoption and scale"},
	{Name: "remote.op", Tier: 2, Ships: shipsLater,
		Props: []Prop{enum("op", remoteOps...), bucket("remotes", BucketN), propOutcome}, Question: "Remote workflows, sweep reliability"},
	{Name: "worktree.op", Tier: 2, Ships: shipsLater,
		Props: []Prop{enum("op", worktreeOps...), enum("vcs", vcsKinds...), propOutcome}, Question: "Worktree flow completion"},
	{Name: "ext.op", Tier: 2, Ships: shipsLater,
		Props: []Prop{enum("kind", extKinds...), enum("op", extOps...), enum("name", extNames...), enum("source", extSources...), bucket("count", BucketN)}, Question: "Which MCPs/skills matter"},
	{Name: "account.switch", Tier: 2, Ships: shipsLater,
		Props: []Prop{propTool, enum("trigger", switchTriggers...), bucket("accounts", BucketN)}, Question: "How often limits drive switching"},
	{Name: "search.daily", Tier: 2, Ships: shipsLater, Rollup: true,
		Props: []Prop{enum("scope", searchScopes...), bucket("count", BucketN), bucket("zero_results", BucketN)}, Question: "Is search used and does it find things"},
	{Name: "tui.view.daily", Tier: 2, Ships: shipsLater, Rollup: true,
		Props: []Prop{enum("view", tuiViews...), bucket("count", BucketN)}, Question: "Dialog discoverability"},
	{Name: "keybind.daily", Tier: 2, Ships: shipsLater, Rollup: true,
		Props: []Prop{enum("action", keybindActions...), bucket("count", BucketN)}, Question: "Which shortcuts matter"},

	{Name: "perf", Tier: 3, Ships: shipsLater,
		Props: []Prop{enum("op", perfOps...), bucket("ms", BucketMS)}, Emitted: "sampled at 10% locally", Question: "Startup and poll regressions"},
	{Name: "crash", Tier: 3, Ships: shipsLater,
		Props: []Prop{enum("area", errorAreas...), enum("panic_type", panicTypes...),
			{Key: "frames_hash", Kind: KindHash, HexLen: 12, Doc: "SHA-256 over agent-deck function names of the top 8 frames; no paths, lines or values"}},
		Question: "Crash clusters without stack text"},
	{Name: "doctor.run", Tier: 3, Ships: shipsLater,
		Props: []Prop{bitmask("checks_failed", 32)}, Question: "Common setup problems"},
	{Name: "feedback.rating", Tier: 3, Ships: shipsLater,
		Props: []Prop{bucket("rating", BucketRating)}, Question: "Satisfaction trend; feedback text never enters telemetry"},
}

var eventIndex = func() map[string]*EventDef {
	m := make(map[string]*EventDef, len(Events))
	for i := range Events {
		m[Events[i].Name] = &Events[i]
	}
	return m
}()

// LookupEvent returns the published definition of an event name.
func LookupEvent(name string) (*EventDef, bool) {
	d, ok := eventIndex[name]
	return d, ok
}

func (d *EventDef) prop(key string) (Prop, bool) {
	for _, p := range d.Props {
		if p.Key == key {
			return p, true
		}
	}
	return Prop{}, false
}

// validValue checks one value against its property definition.
func (p Prop) validValue(v any) bool {
	switch p.Kind {
	case KindEnum:
		s, ok := v.(string)
		return ok && contains(p.Values, s)
	case KindBucket:
		s, ok := v.(string)
		return ok && validBucket(p.Bucket, s)
	case KindBool:
		_, ok := v.(bool)
		return ok
	case KindBitmask:
		n, ok := asInt(v)
		return ok && n >= 0 && (p.Bits >= 63 || n < int64(1)<<uint(p.Bits))
	case KindHash:
		s, ok := v.(string)
		if !ok || len(s) != p.HexLen {
			return false
		}
		_, err := hex.DecodeString(s)
		return err == nil && s == toLowerHex(s)
	case KindPattern:
		s, ok := v.(string)
		return ok && len(s) <= 16 && p.Pattern.MatchString(s)
	case KindInt:
		n, ok := asInt(v)
		return ok && n >= int64(p.Min) && n <= int64(p.Max)
	}
	return false
}

func toLowerHex(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'F' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func asInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case uint32:
		return int64(n), true
	case float64:
		if n != float64(int64(n)) {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Validate checks an event name and every property against the schema.
func Validate(name string, props map[string]any) error {
	def, ok := LookupEvent(name)
	if !ok {
		return fmt.Errorf("telemetry: unknown event %q", name)
	}
	for k, v := range props {
		p, ok := def.prop(k)
		if !ok {
			return fmt.Errorf("telemetry: %s: unknown property %q", name, k)
		}
		if !p.validValue(v) {
			return fmt.Errorf("telemetry: %s.%s: value not allowed", name, k)
		}
	}
	return nil
}

// sortedKeys returns map keys in order, for deterministic encoding.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
