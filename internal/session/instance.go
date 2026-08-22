package session

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"al.essio.dev/pkg/shellescape"
	"golang.org/x/sync/singleflight"

	"github.com/asheshgoplani/agent-deck/internal/docker"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

var (
	sessionLog                  = logging.ForComponent(logging.CompSession)
	mcpLog                      = logging.ForComponent(logging.CompMCP)
	codexSessionIDPathPatternRE = regexp.MustCompile(`/sessions/\S*/rollout-\S*-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl`)
	uuidPatternRE               = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	geminiPromptRE              = regexp.MustCompile(`^(>|>>>|\$|❯|➜|gemini>|✦)\s*$`)
	shellPromptRE               = regexp.MustCompile(`^[\s]*(>|>>>|\$|❯|➜|#|%)\s*$`)
	openCodeSessionHTTPClient   = &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	openCodeCLIQueryGroup singleflight.Group
	openCodeCLIQueryCache = struct {
		sync.Mutex
		entries map[string]openCodeCLIQueryCacheEntry
	}{entries: make(map[string]openCodeCLIQueryCacheEntry)}
)

// Status represents the current state of a session
type Status string

const (
	StatusRunning  Status = "running"
	StatusWaiting  Status = "waiting"
	StatusIdle     Status = "idle"
	StatusError    Status = "error"
	StatusStarting Status = "starting" // Session is being created (tmux initializing)
	StatusStopped  Status = "stopped"  // Session intentionally stopped by user (not crashed)
	// StatusQueued: session is waiting for group capacity. v1.9.1 introduces
	// group max_concurrent caps; a launch into a group at cap stores the
	// instance with this status and starts it once a running session ends.
	StatusQueued Status = "queued"
)

// Substate is the additive Honest-Status-v2 refinement of a session's coarse
// status (see tmux.Substate). It explains WHY a session is in its status
// (model-unavailable, auth-401, idle-at-empty-prompt, running) without altering
// the byte-stable canonical status. Re-exported here so the CLI/TUI can consume
// it without importing the tmux package directly.
type Substate = tmux.Substate

const (
	SubstateNone              = tmux.SubstateNone
	SubstateRunning           = tmux.SubstateRunning
	SubstateIdleAtEmptyPrompt = tmux.SubstateIdleAtEmptyPrompt
	SubstateModelUnavailable  = tmux.SubstateModelUnavailable
	SubstateAuth401           = tmux.SubstateAuth401
	SubstateUsageLimit        = tmux.SubstateUsageLimit
)

const wrapperPlaceholder = "{command}"

// PinMode anchors a session to a fixed slot within its group, exempt from the
// status/recency actionable sort (pin-sessions feature). The empty value is the
// default so existing rows migrate cleanly through the `pin` column default.
type PinMode string

const (
	PinNone   PinMode = ""       // default; not pinned, participates in the normal sort
	PinTop    PinMode = "top"    // fixed at the top of the group's session list
	PinBottom PinMode = "bottom" // fixed at the bottom of the group's session list
)

const (
	hookFastPathWindow             = 2 * time.Minute
	codexHookRunningFastPathWindow = 20 * time.Second
	codexHookWaitingFastPathWindow = 2 * time.Minute
	codexBootstrapScanInterval     = 2 * time.Second
	codexRotationScanInterval      = 30 * time.Second
	opencodeRotationScanInterval   = 15 * time.Second
	opencodeRotationActivityWindow = 30 * time.Second
	opencodeStartupTimeSkew        = 5 * time.Second
	// openCodeSessionResponseLimit bounds allocation if a persisted OpenCode
	// port is later occupied by an unrelated loopback listener.
	openCodeSessionResponseLimit = 8 << 20
	// opencodeSSEFreshnessWindow bounds how long an SSE-derived status stays
	// authoritative without stream traffic (issue #1614). OpenCode heartbeats
	// its /event stream roughly every 10s, and the watcher refreshes the
	// timestamp on every received line, so 30s of silence means the stream
	// (and likely the process) is gone — fall back to tmux polling.
	opencodeSSEFreshnessWindow = 30 * time.Second
	codexProbeMissingSentinel  = "__AGENT_DECK_MISSING_TOOL__"
	// codexLsofProbeTimeout hard-caps a single lsof invocation so a slow or
	// hung child can never stall the shared status pass. lsof is also run with
	// -n -P (no host/port name resolution) to avoid reverse-DNS PTR lookups on
	// the codex process's open sockets, which block ~30s each on resolvers that
	// silently drop PTR queries (issue #1581).
	codexLsofProbeTimeout = 2 * time.Second
)

// Instance represents a single agent/shell session
type Instance struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	ProjectPath string `json:"project_path"`
	GroupPath   string `json:"group_path"` // e.g., "projects/devops"
	Order       int    `json:"order"`      // Position within group (for reorder persistence)
	// Pin anchors this session to the top or bottom of its group, exempt from
	// the status/recency sort (pin-sessions feature). PinNone is the default.
	Pin                PinMode `json:"pin,omitempty"`
	ParentSessionID    string  `json:"parent_session_id,omitempty"`    // Links to parent session (makes this a sub-session)
	ParentProjectPath  string  `json:"parent_project_path,omitempty"`  // Parent's project path (for --add-dir access)
	IsConductor        bool    `json:"is_conductor,omitempty"`         // True if this session is a conductor orchestrator
	NoTransitionNotify bool    `json:"no_transition_notify,omitempty"` // Suppress transition event dispatch for this session

	// TitleLocked, when true, blocks Claude's session name from syncing into
	// the agent-deck Title (issue #697). Conductors launch workers with a
	// semantic title (e.g. "SCRUM-351") that Claude would otherwise overwrite
	// with its auto-generated summary on the next hook event. Set via
	// `--title-lock` on add/launch or `session set-title-lock`.
	TitleLocked bool `json:"title_locked,omitempty"`

	// SubcommandPassthrough, when true, marks a Tool=="shell" instance whose
	// Command was routed here BY NAME — i.e. resolveSessionCommand
	// (cmd/agent-deck/cli_utils.go) itself validated that the user's raw
	// `-c`/`--cmd` input was a real `claude <subcommand>` /
	// `codex <subcommand>` invocation (e.g. "claude remote-control --name X")
	// and deliberately skipped agent-deck's own flag injection for it (#1800).
	// Set exactly once, at creation, by the CLI `add`/`launch` call sites.
	//
	// buildShellPassthroughCommand (instance.go) gates its claude/codex
	// env/binary substitution on this field rather than re-deriving intent
	// from Command at every spawn. Without it, an ordinary Tool=="shell"
	// session — including one created before this field existed, or a
	// custom TUI command that merely mentions "claude"/"codex" — would be
	// reclassified as a claude/codex spawn on every restart purely because
	// MatchTool's builtin detection is a whole-line substring match, up to
	// and including silently swapping its binary/account (Claude review,
	// PR #1821 HIGH #1). This field is the one thing a re-derived string
	// match can never fake: real provenance from the route that actually
	// checked.
	SubcommandPassthrough bool `json:"subcommand_passthrough,omitempty"`

	// AutoName, when true, marks Title as a machine-generated adjective-noun
	// handle (from a --quick / TUI-Q create). The TUI then displays the
	// session's live Claude task description (tmux pane title) in place of the
	// handle. Any explicit rename clears this so the user-chosen name is shown
	// verbatim. See docs/superpowers/specs/2026-06-01-quick-session-claude-name-design.md.
	// Guarded by i.mu for runtime reads/writes; use GetAutoName/SetAutoName once
	// an Instance is shared with background workers or the TUI render loop.
	AutoName bool `json:"auto_name,omitempty"`

	// autoNameDescription is the last non-empty Claude task description (the
	// cleaned tmux pane title) captured for an AutoName session. It is persisted
	// via the auto_name_description column so the meaningful name survives an
	// app reopen even when the session is stopped/idle and no live pane title is
	// available — render order is live pane title → this saved description →
	// handle. Guarded by i.mu: written from the background status loop, read
	// during render. Unexported because persistence flows through InstanceData,
	// not Instance's own JSON tags.
	autoNameDescription string

	// Git worktree support
	WorktreePath     string `json:"worktree_path,omitempty"`      // Path to worktree (if session is in worktree)
	WorktreeRepoRoot string `json:"worktree_repo_root,omitempty"` // Original repo root
	WorktreeBranch   string `json:"worktree_branch,omitempty"`    // Branch name in worktree
	WorktreeType     string `json:"worktree_type,omitempty"`      // "git", "jujutsu", or "" (legacy = git)

	// Account is the per-session named account slot (issue #924). Maps to
	// `[profiles.<account>.claude].config_dir` in ~/.agent-deck/config.toml
	// at spawn time and becomes the most-specific level in the
	// CLAUDE_CONFIG_DIR resolution chain — beating conductor / group / env.
	// Switching the value requires a session restart (the Option 1 MVP
	// tradeoff): the in-flight Claude conversation is lost since the new
	// account's settings.json and history live elsewhere. Empty means
	// "fall through to conductor/group/env/profile/global/default" so
	// pre-#924 sessions keep their existing behavior unchanged.
	Account string `json:"account,omitempty"`

	// Multi-repo support
	MultiRepoEnabled   bool                `json:"multi_repo_enabled,omitempty"`
	AdditionalPaths    []string            `json:"additional_paths,omitempty"`    // Paths beyond ProjectPath
	MultiRepoTempDir   string              `json:"multi_repo_temp_dir,omitempty"` // Temp cwd for multi-repo sessions
	MultiRepoWorktrees []MultiRepoWorktree `json:"multi_repo_worktrees,omitempty"`

	Command        string    `json:"command"`
	Wrapper        string    `json:"wrapper,omitempty"` // Optional wrapper command with {command} placeholder
	Tool           string    `json:"tool"`
	Status         Status    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	LastAccessedAt time.Time `json:"last_accessed_at,omitempty"` // When user last attached
	// ArchivedAt is set when the user archives the session (non-zero = archived).
	ArchivedAt time.Time `json:"archived_at,omitempty"`

	// LastStartedAt is the wall-clock time of the most recent successful
	// Start() / StartWithMessage() / Restart() call. Persisted so short-lived
	// CLI invocations can see it across processes (issue #30): a restart
	// queued seconds after a start must detect the session is already fresh
	// and skip the teardown. Zero value means "unknown" (old record or
	// never started) and callers MUST NOT treat zero as "just now".
	LastStartedAt time.Time `json:"last_started_at,omitempty"`

	// GenericSessionID is the conversation id for a custom [tools.*] tool
	// that declares resume_flag. Persisted in tool_data so Restart can rebuild
	// `<cmd> <resume_flag> <id>` after a reboot (when tmux session_id_env is
	// gone). Live tmux env still wins when present (GetGenericSessionID).
	GenericSessionID  string    `json:"generic_session_id,omitempty"`
	GenericDetectedAt time.Time `json:"generic_detected_at,omitempty"`
	// GenericSessionTool and GenericSessionLocation record the tool and the
	// execution location the id above was captured under. A persisted id is
	// only eligible for resume while both still match — see
	// generic_session_scope.go for why an id that outlives a reboot must not
	// outlive the tool or the host it belongs to.
	GenericSessionTool     string `json:"generic_session_tool,omitempty"`
	GenericSessionCommand  string `json:"generic_session_command,omitempty"`
	GenericSessionLocation string `json:"generic_session_location,omitempty"`
	// genericSessionIDCleared is set by intentional clear paths (SetField
	// tool-session-id "", clearSessionBindingForFreshStart). When true,
	// instanceToRow writes an explicit empty generic_session_id so sticky
	// MergeToolDataExtras does not resurrect a prior binding. Not persisted;
	// a non-empty GenericSessionID always clears this flag.
	genericSessionIDCleared bool
	// genericSessionPersistErr holds why the last custom-tool conversation-id
	// write-through failed, so a bind that never reached disk can be told from
	// one that did. Read through GenericSessionPersistError.
	genericSessionPersistMu  sync.Mutex
	genericSessionPersistErr error

	// Claude Code integration
	ClaudeSessionID  string    `json:"claude_session_id,omitempty"`
	ClaudeDetectedAt time.Time `json:"claude_detected_at,omitempty"`
	// claudeSessionIDsFromDiskScan records every conversation id this
	// instance ever obtained from an mtime-based DISK SCAN rather than from a
	// source identifying THIS session (own tmux env, own hook payload, an
	// explicit --session-id/--resume-session in its own command, an
	// operator's explicit set, or an id this process minted). While an id is
	// in this set it may not authorize `--resume` — see resume_identity.go
	// (#1815).
	//
	// A SET, not one value: a scanned candidate stays suspect even after the
	// field moves on and comes back, so no writer can launder it by simply
	// re-assigning it. Membership is persisted for the current id (see
	// resume_identity_persist.go), so a process restart cannot launder it
	// either. Clearing is explicit and only from a source that identifies
	// this session (markClaudeSessionIDVerified).
	claudeSessionIDsFromDiskScan map[string]bool
	// claudeSessionIDsFromDiskScanMu guards claudeSessionIDsFromDiskScan.
	// It is a DEDICATED lock rather than a reuse of mu: mu is held across
	// UpdateHookStatus (backgroundStatusUpdate's goroutine), which is one of
	// the writers of this map, so reusing mu here would self-deadlock on that
	// path. The map is also read concurrently from the TUI goroutine (preview
	// rendering, NoteClaudeSessionIDFromOwnPane on refresh) while the status
	// goroutine writes it — a concurrent, unguarded map read+write is a Go
	// runtime FATAL error (not a recoverable panic), so every access must go
	// through this lock (review finding on #1830).
	claudeSessionIDsFromDiskScanMu sync.Mutex

	// Gemini CLI integration
	GeminiSessionID  string                  `json:"gemini_session_id,omitempty"`
	GeminiDetectedAt time.Time               `json:"gemini_detected_at,omitempty"`
	GeminiYoloMode   *bool                   `json:"gemini_yolo_mode,omitempty"` // Per-session override (nil = use global config)
	GeminiModel      string                  `json:"gemini_model,omitempty"`     // Active model for this session
	GeminiAnalytics  *GeminiSessionAnalytics `json:"gemini_analytics,omitempty"` // Per-session analytics

	// OpenCode CLI integration
	OpenCodeSessionID  string    `json:"opencode_session_id,omitempty"`
	OpenCodeDetectedAt time.Time `json:"opencode_detected_at,omitempty"`
	OpenCodeStartedAt  int64     `json:"-"`                       // Unix millis when we started OpenCode (for session matching, not persisted)
	OpenCodePort       int       `json:"opencode_port,omitempty"` // Localhost port of OpenCode's event server (issue #1614); 0 = none
	lastOpenCodeScanAt time.Time // Rate-limits OpenCode session-discovery scans

	// Codex CLI integration
	CodexSessionID  string    `json:"codex_session_id,omitempty"`
	CodexDetectedAt time.Time `json:"codex_detected_at,omitempty"`
	CodexStartedAt  int64     `json:"-"` // Unix millis when we started Codex (for session matching, not persisted)
	lastCodexScanAt time.Time // Rate-limits expensive ~/.codex/sessions scans
	// Usage-limit detection (#1802), all guarded by i.mu.
	// lastUsageLimitScanAt throttles the transcript read (records scan START).
	// usageLimitedCached memoises the verdict for that window, and
	// usageLimitSessionID records the session id it was formed for so a rebind
	// cannot inherit it. usageLimitScanGen versions in-flight scans so one slower
	// than the interval cannot publish over a newer result.
	lastUsageLimitScanAt time.Time
	usageLimitedCached   bool
	usageLimitSessionID  string
	usageLimitScanGen    uint64
	lastCodexProbeAt     time.Time // Rate-limits expensive Codex process-file probes
	// pendingCodexRestartWarning is consumed by UI/CLI after Restart() succeeds.
	// It is intentionally transient and never persisted.
	pendingCodexRestartWarning string `json:"-"`

	// Hermes CLI integration. Hermes mints a new session ID each launch and does
	// not export it (no env var / hook), so agent-deck captures it from
	// `hermes sessions list` at restart time and resumes with --resume. Not
	// persisted (json:"-"): it is re-captured on every restart, so it needs no
	// lifetime beyond a single Restart() call — which also keeps it out of the
	// JSON marshaling that could otherwise race the restart-time write.
	HermesSessionID      string `json:"-"`
	HermesHookGeneration string `json:"-"`

	// DeepSeek Harness integration. dsh exports no session ID (no env var, no
	// hook, no `sessions list` subcommand), so agent-deck discovers it from the
	// on-disk workspace index under $DSH_HOME at restart time. Not persisted
	// (json:"-") for the same reason as HermesSessionID: it is re-discovered on
	// every restart, so it needs no lifetime beyond a single Restart() call.
	DeepSeekSessionID string `json:"-"`

	// DeepSeekTask is the positional task a headless-profile session was
	// launched with. Unlike DeepSeekSessionID this IS persisted (tool_data
	// extras zone, see deepseek_task_persist.go): for a one-shot the task is the
	// whole invocation, so a restart that forgot it would rebuild
	// `dsh --profile headless` — a usage error that replaces the retained answer
	// (PR #1942 review, P1c). Empty for every other profile.
	DeepSeekTask string `json:"deepseek_task,omitempty"`
	// restartEnv contains one-shot environment overrides while RestartWithEnv is
	// building the replacement process. It is cleared before the call returns.
	restartEnv map[string]string

	// GitHub Copilot CLI integration
	CopilotSessionID  string    `json:"copilot_session_id,omitempty"`
	CopilotDetectedAt time.Time `json:"copilot_detected_at,omitempty"`
	CopilotStartedAt  int64     `json:"-"`                           // Unix millis when we started Copilot (for session matching, not persisted)
	CopilotModel      string    `json:"copilot_model,omitempty"`     // Active model for this session
	CopilotAllowAll   bool      `json:"copilot_allow_all,omitempty"` // Per-session --allow-all override

	// Latest user input for context (extracted from session files)
	LatestPrompt      string    `json:"latest_prompt,omitempty"`
	Notes             string    `json:"notes,omitempty"`
	lastPromptModTime time.Time // mtime cache for updateGeminiLatestPrompt (not serialized)

	// Color is an optional user-chosen tint for this session's TUI row (issue #391).
	// Accepts a lipgloss-compatible color spec:
	//   - "#RRGGBB"      - truecolor hex
	//   - "0".."255"     - ANSI 256-palette index as a decimal string
	//   - ""             - default (no tint, current rendering unchanged)
	// Validation happens at CLI/API boundary in cmd/agent-deck/session_cmd.go.
	// Empty string is the default so the field is fully opt-in and never
	// changes rendering for users who don't set it.
	Color string `json:"color,omitempty"`

	// JSONL tail-read cache: skip re-reading if file hasn't grown
	lastJSONLSize int64
	lastJSONLPath string
	cachedPrompt  string

	// Docker sandbox support.
	Sandbox          *SandboxConfig `json:"sandbox,omitempty"`
	SandboxContainer string         `json:"sandbox_container,omitempty"` // Container name when running in sandbox.

	// SSH remote support
	SSHHost       string `json:"ssh_host,omitempty"`
	SSHRemotePath string `json:"ssh_remote_path,omitempty"`

	// TmuxSocketName is the tmux `-L <name>` socket selector captured when
	// this instance was created (v1.7.50+, issue #687). Empty string keeps
	// the pre-v1.7.50 behavior of targeting the user's default tmux server
	// — zero change for existing installations.
	//
	// Precedence at creation time: the `--tmux-socket` CLI flag on
	// `agent-deck add` / `agent-deck launch` wins, else
	// `[tmux].socket_name` from config.toml, else empty. Once persisted,
	// this value is IMMUTABLE — lifecycle operations (start/stop/restart/
	// revive) MUST target this same socket even if the installation-wide
	// config is later edited. Mixing sockets would leave the session
	// orphaned on an unreachable tmux server.
	TmuxSocketName string `json:"tmux_socket_name,omitempty"`

	// MCP tracking - which MCPs were loaded when session started/restarted
	// Used to detect pending MCPs (added after session start) and stale MCPs (removed but still running)
	LoadedMCPNames []string `json:"loaded_mcp_names,omitempty"`

	// TrackedMCPPIDs holds the OS PIDs of stdio MCP children spawned for
	// this session (issue #965). Session stop must SIGTERM (then SIGKILL
	// after a grace period) each PID so children aren't reparented to
	// PID 1 and leaked. Mutated only via RegisterMCPChild /
	// UnregisterMCPChild to keep concurrent access safe.
	TrackedMCPPIDs []int `json:"tracked_mcp_pids,omitempty"`
	mcpPIDsMu      sync.Mutex

	// Channels are Claude Code plugin-channel ids (e.g. "plugin:telegram@user/repo").
	// When non-empty on a claude session, buildClaudeExtraFlags emits
	// `--channels <csv>` so the session subscribes to inbound plugin messages.
	// Without this flag the channel plugin runs as a plain MCP (tools only,
	// no inbound delivery) which silently drops Telegram/Discord/Slack
	// messages on conductor restart.
	Channels []string `json:"channels,omitempty"`

	// Plugins is the catalog-key list of Claude Code plugins enabled for
	// this session via `agent-deck add --plugin <name>` /
	// `session set <id> plugins <csv>`. Names are short catalog keys (NOT
	// fully-qualified `<name>@<source>` ids) and resolve through the
	// [plugins.<name>] table in ~/.agent-deck/config.toml at spawn time.
	// When non-empty on a claude session, EnsureWorkerScratchConfigDir
	// writes enabledPlugins[<id>] = true into the scratch settings.json so
	// the plugin loads only for this session, not globally.
	// RFC: docs/rfc/PLUGIN_ATTACH.md.
	Plugins []string `json:"plugins,omitempty"`

	// InheritTelegramEnv is the explicit opt-in for #1133: when true, a
	// non-channel-owning claude child KEEPS the conductor's TELEGRAM_*
	// env vars (TELEGRAM_STATE_DIR, TELEGRAM_BOT_TOKEN, etc.). Default
	// false strips them so a child can't spawn a duplicate `bun telegram`
	// poller that races the conductor for getUpdates (Telegram 409
	// Conflict + dropped inbound messages). CLI flag:
	// `--inherit-telegram-env` on `agent-deck launch`. Rare use case;
	// existing behavior is preserved when the flag is absent.
	InheritTelegramEnv bool `json:"inherit_telegram_env,omitempty"`

	// PluginChannelLinkDisabled opts the session out of the catalog-driven
	// auto-link between Plugins and Channels (RFC §4.7). When true, an
	// `--plugin foo` whose catalog entry has EmitsChannel=true does NOT
	// auto-add `plugin:foo@source` to Channels. Useful for tools-only
	// usage of channel-emitting plugins. CLI flag: `--no-channel-link`.
	PluginChannelLinkDisabled bool `json:"plugin_channel_link_disabled,omitempty"`

	// AutoLinkedChannels is the persisted set of channel ids that
	// syncPluginChannels last added via the auto-link mechanism. Lets
	// reconciliation distinguish "channel I owned" from "channel the
	// user added manually" — without it, a plugin removed from the
	// catalog or an opt-out toggle would leave stale autolinks behind
	// (G4 / C2). Updated on every Plugins mutation; never written
	// directly by users.
	AutoLinkedChannels []string `json:"auto_linked_channels,omitempty"`

	// WorkerScratchConfigDir is the ephemeral CLAUDE_CONFIG_DIR prepared
	// for a non-conductor claude worker (issue #59, v1.7.68). The
	// scratch dir copies the ambient profile's settings.json with the
	// telegram plugin explicitly disabled, symlinks the rest of the
	// profile, and is cleaned up on session stop/remove. Empty for
	// conductor sessions, explicit telegram channel owners, and
	// non-claude tools — they use the ambient profile as-is.
	WorkerScratchConfigDir string `json:"worker_scratch_config_dir,omitempty"`

	// IdleTimeoutSecs is the auto-stop threshold (#1143). When > 0, a central
	// watcher poll triggers Kill() if the tmux pane content stays unchanged
	// for this many seconds. 0 = disabled (current behavior). Default is 0
	// so existing sessions are unaffected on upgrade.
	IdleTimeoutSecs int64 `json:"idle_timeout_secs,omitempty"`

	// IsForkAwaitingStart signals that this instance was produced by a
	// fork builder and must run a pre-built fork command verbatim on the
	// first Start() (#745). Claude fork targets usually store that command
	// in Command. Pi fork targets keep Command as the normal restart
	// command (so later restarts use --continue) and store the first-start
	// command in ForkStartCommand instead.
	//
	// Transient (json:"-"): persisting this would cause a restart of the
	// forked session to re-emit the tool-specific fork command and
	// double-count the parent transcript.
	IsForkAwaitingStart bool `json:"-"`

	// ForkStartCommand optionally carries the pre-built command to run while
	// IsForkAwaitingStart is true. When empty, Start() falls back to Command
	// for backwards compatibility with Claude fork targets.
	ForkStartCommand string `json:"-"`

	// ExtraArgs are user-supplied claude CLI tokens appended verbatim to every
	// start/resume/fork command (e.g. ["--agent","reviewer","--model","opus"]).
	// Each token is shellescape-quoted on emission so values with spaces
	// survive the bash -c wrapper.
	ExtraArgs []string `json:"extra_args,omitempty"`

	// ExitToShell is the per-session override for the [shell] exit_to_shell
	// toggle (issue #1161). nil → inherit the global config default (off);
	// non-nil → force on/off for this session regardless of config. When on
	// and the tool is a built-in agent, the spawn command is wrapped so that
	// exiting the agent drops the pane to an interactive shell at the same cwd.
	ExitToShell *bool `json:"exit_to_shell,omitempty"`

	// LaunchShell is the per-session override for the [shell] launch_shell
	// toggle (issue #1218). nil → inherit the global config default (off);
	// non-nil → force on/off for this session regardless of config. When on,
	// the spawn command is wrapped with "$SHELL -l -c '<cmd>'" so that
	// environment variables from ~/.zshrc, ~/.bashrc etc. are available to
	// the agent process. This solves MCP config {env:VAR} failures when
	// launching from the TUI without going through the user's shell.
	LaunchShell *bool `json:"launch_shell,omitempty"`

	// StartupQuery is the claude-code positional "startup query" (#725,
	// v1.7.67). Set from the new-session dialog's "Start query" field and
	// emitted as a single shell-quoted positional arg on the claude
	// new-session command line only.
	//
	// Per-session, NEVER persisted — the `json:"-"` tag is load-bearing.
	// On Restart/Resume the field is empty, so the query does NOT replay.
	// This is the whole point of having a dedicated field instead of
	// overloading ExtraArgs (which persists and space-splits).
	StartupQuery string `json:"-"`

	// ToolOptions stores tool-specific launch options (Claude, Codex, Gemini, etc.)
	// JSON structure: {"tool": "claude", "options": {...}}
	ToolOptionsJSON json.RawMessage `json:"tool_options,omitempty"`

	tmuxSession *tmux.Session // Internal tmux session

	paneDeadExitStatusForTest func() (int, bool) // nil uses tmuxSession.PaneDeadExitStatus

	// Hook-based status detection (set by StatusFileWatcher from Claude Code hooks)
	hookStatus                  string    // running, idle, waiting, dead (empty = no hook data)
	hookEvent                   string    // Hook event name that caused the last status (e.g. "PermissionRequest")
	hookSessionID               string    // Session ID from hook payload
	hookLastUpdate              time.Time // When hook status was last received
	codexStartedGeneration      string
	codexCompletedGeneration    string
	codexStartedSessionID       string
	codexCompletedSessionID     string
	codexInvalidatingGeneration string

	// Durable last-activity record (issue #1846). Unlike hookLastUpdate this
	// survives ClearHookStatus and, via tool_data.last_activity_at, TUI
	// restarts. lastActivityPersisted tracks the last value flushed to the
	// DB so the hook observation path can throttle its targeted UPDATEs;
	// lastActivityPersistMu serializes those flushes WITHOUT holding i.mu
	// across the DB write. See last_activity_persist.go.
	lastActivityAt        time.Time
	lastActivityPersisted time.Time
	lastActivityPersistMu sync.Mutex

	// restartTmuxRecordErr holds why the last restart could not record the
	// tmux session name it minted, or nil once one did. Callers that have no
	// other save on their path (the CLI --restart commands) read it through
	// RestartTmuxNameRecorded so they can report a live-but-unrecorded restart
	// instead of announcing a success that never reached disk. Guarded by its
	// own mutex rather than i.mu so the DB write never runs under the lock the
	// TUI reads status through. See restart_tmux_persist.go.
	restartTmuxRecordMu     sync.Mutex
	restartTmuxRecordErr    error
	restartTmuxRecordStamps statedb.WriteStamps

	// SSE-based status detection for OpenCode (set by OpenCodeSSEWatcher,
	// issue #1614). Not persisted; rebuilt from the live event stream.
	sseStatus     string    // "running" or "waiting" (empty = no SSE data)
	sseLastUpdate time.Time // When SSE status was last confirmed

	// mu protects fields written by backgroundStatusUpdate and read by the TUI goroutine.
	// Use GetStatus()/SetStatus() and GetTool()/SetTool() for thread-safe access.
	// UpdateStatus() acquires the write lock internally.
	mu sync.RWMutex

	// spawnGen is bumped on every Start/StartWithMessage/Stop so the fast-death
	// watcher (#1580) can detect that a newer spawn or a deliberate stop has
	// superseded it — race-free, without reading the mutex-guarded status fields
	// from its own goroutine.
	spawnGen atomic.Uint64

	// spawnGenMu guards spawnGenWake. spawnGenWake lets a generation bump wake a
	// running watchForFastDeath goroutine immediately instead of it only
	// noticing the bump on its next spawnFastDeathTick (250ms) poll (#1775).
	// That up-to-250ms tail after a deliberate Stop/Kill was long enough for
	// the watcher's eventual WriteSessionIDLifecycleEvent call — which
	// re-resolves its target path from the live $HOME env var, not a value
	// captured at goroutine-start — to land inside whatever OTHER test's
	// isolated HOME happened to be active at that instant, racing that
	// test's t.TempDir() cleanup (observed as
	// TestPersistence_DiscoverLatestClaudeJSONL_Unit's flaky
	// "TempDir RemoveAll cleanup: ... /.local: directory not empty").
	spawnGenMu   sync.Mutex
	spawnGenWake chan struct{}

	// spawnWriteMu serializes a watcher's writes against teardown. A generation
	// check alone leaves a check-then-write gap: the watcher can pass the check
	// and be descheduled a moment before its write while the bumping goroutine
	// races ahead and tears down (in tests, removes the very $HOME the watcher
	// captured). The watcher takes this mutex around its write AND re-checks the
	// generation while holding it, so a teardown that bumps and then takes the
	// same mutex is guaranteed that every write is either already complete or
	// will be suppressed. Only the file writes are covered — never the tmux
	// calls — so the barrier costs teardown at most one file write and can never
	// be held hostage by a wedged tmux subprocess.
	spawnWriteMu sync.Mutex

	// lastErrorCheck tracks when we last confirmed the session doesn't exist
	// Used to skip expensive Exists() checks for ghost sessions (sessions in JSON but not in tmux)
	// Not serialized - resets on load, but that's fine since we'll recheck on first poll
	lastErrorCheck time.Time

	// Tiered polling: skip expensive checks for idle sessions with no activity
	lastIdleCheck     time.Time // When we last did a full check for an idle session
	lastKnownActivity int64     // Last window_activity timestamp seen

	// lastStartTime tracks when Start() was called
	// Used to provide grace period for tmux session creation (prevents error flash)
	// Not serialized - only relevant for current TUI session
	lastStartTime time.Time

	// tmuxFlipFromRunningPending debounces a purely tmux-inferred flip AWAY from
	// running (→ waiting/error). A long single tool-call (past the hook freshness
	// window) or transient subprocess churn can momentarily present the pane as a
	// shell prompt or a capture error, then recover; without a confirming second
	// sample that single transient read fires a false completion/error to the
	// conductor. Set when we hold the first such sample at running; cleared on any
	// settled (running/idle) outcome or once the flip is confirmed. Not serialized.
	tmuxFlipFromRunningPending bool

	// Auth-hold state (see auth_hold.go). authFailureSeenAt is when a live
	// credential-failure banner was last observed, used to attribute a LATER
	// pane death to authentication within authHoldDeathWindow. authHeld mirrors
	// the durable sidecar so the TUI render hot path (CachedSubstate, once per
	// row per frame) never touches the filesystem. authHoldCheckedAt throttles
	// the sidecar read on the death path. None are serialized — the sidecar is
	// the cross-process source of truth and these re-derive from it.
	// authHoldClearedOnce records that this process has already swept any stale
	// sidecar for this session (see clearAuthHoldLocked).
	authFailureSeenAt   time.Time
	authHeld            bool
	authHoldCheckedAt   time.Time
	authHoldClearedOnce bool

	// addedThisProcess is true only for instances built by a NewInstance*
	// constructor in the current process (i.e. just `add`ed), and false for
	// instances reloaded from storage (built as struct literals). Combined with
	// a zero lastStartTime it identifies a session that was added but never
	// started, whose absent tmux is expected (idle), not a fault (error).
	// Not serialized — a reloaded "never started" row that genuinely ran would
	// surface a stored non-idle status; a brand-new row reloads as idle and
	// re-derives correctly once the user starts it.
	addedThisProcess bool

	// Rate-limits expensive session metadata sync work (Claude/Gemini/Codex)
	// that runs from UpdateStatus while this instance lock is held.
	lastSessionMetaSync time.Time

	// SkipMCPRegenerate skips .mcp.json regeneration on next Restart()
	// Set by MCP dialog Apply() to avoid race condition where Apply writes
	// config then Restart immediately overwrites it with different pool state
	SkipMCPRegenerate bool `json:"-"` // Don't persist, transient flag

	// Gateway health cache for Hermes sessions (volatile, not persisted).
	hermesGatewayCheckedAt time.Time
	hermesGatewayOK        bool
}

// newSpawnGenWatch bumps the generation and hands back both the new generation
// and the channel that the NEXT bump will close.
//
// It exists so the caller — not the watcher goroutine — subscribes to the wake.
// If the watcher fetched its own channel after being started, a bump landing in
// the gap between `go watchForFastDeath(...)` and that fetch would close a nil
// (or already-replaced) channel, the watcher would install a fresh one, and the
// supersession would only be noticed on the next spawnFastDeathTick — exactly
// the up-to-250ms tail the wake channel exists to remove. Producing gen and the
// channel together under one lock closes that gap: any bump ordered after this
// call is guaranteed to close the very channel the watcher is selecting on.
//
// It also takes the write barrier, exactly as teardown does: superseding an old
// watcher is not enough if that watcher had already passed its generation check
// and was mid-write, since its stale record would then land after the new spawn
// had begun. Waiting the barrier out here means a new watcher never starts
// while its predecessor still has a write in flight.
func (i *Instance) newSpawnGenWatch() (uint64, <-chan struct{}) {
	gen, wake := i.bumpSpawnGen(true)
	i.awaitWatcherWrites()
	return gen, wake
}

// awaitWatcherWrites blocks until no watcher write is in flight. Callers must
// bump the generation FIRST: that ordering is what makes this a guarantee
// rather than a narrow window, because any write not yet holding the mutex
// re-checks the generation under it and suppresses itself.
func (i *Instance) awaitWatcherWrites() {
	i.spawnWriteMu.Lock()
	i.spawnWriteMu.Unlock() //nolint:staticcheck // barrier: waits out an in-flight watcher write
}

// bumpSpawnGenAndBarrier supersedes the running watcher and then waits out any
// write it already has in flight, so a teardown can proceed knowing no watcher
// will write again.
//
// Ordering is what makes it a guarantee rather than a small window: the bump
// happens first, and the watcher re-checks the generation while holding
// spawnWriteMu. Any write that has not yet taken the mutex will therefore see
// the new generation and suppress itself, and any write that already holds it
// completes before this returns.
//
// What it waits on is deliberately minimal: two small file writes to paths the
// caller is about to touch anyway. Never a tmux call, and never the watcher's
// log line — those stay outside the mutex — so a wedged tmux or a slow log
// handler cannot delay a deliberate stop.
func (i *Instance) bumpSpawnGenAndBarrier() uint64 {
	gen, _ := i.bumpSpawnGen(false)
	i.awaitWatcherWrites()
	return gen
}

// bumpSpawnGen advances the generation and wakes the current watcher. When
// resubscribe is set it also installs the channel the next bump will close and
// returns it, so a new watcher's subscription is established under the same
// lock acquisition as its own bump.
func (i *Instance) bumpSpawnGen(resubscribe bool) (uint64, <-chan struct{}) {
	i.spawnGenMu.Lock()
	defer i.spawnGenMu.Unlock()
	// Increment under the same lock that publishes the wake, so a bump can
	// never be observed as "channel closed but generation not yet advanced".
	gen := i.spawnGen.Add(1)
	if i.spawnGenWake != nil {
		close(i.spawnGenWake)
		i.spawnGenWake = nil
	}
	if !resubscribe {
		return gen, nil
	}
	i.spawnGenWake = make(chan struct{})
	return gen, i.spawnGenWake
}

// commitSpawnWatchWrite runs a watcher's write under spawnWriteMu, skipping it
// if the watcher has been superseded in the meantime, and reports whether it
// ran. Every watcher write goes through here; that is the whole contract
// bumpSpawnGenAndBarrier relies on. Keep the callback to the writes themselves
// — anything slow or unbounded (tmux, log handlers) belongs outside, since
// teardown blocks on this mutex.
func (i *Instance) commitSpawnWatchWrite(gen uint64, write func()) bool {
	i.spawnWriteMu.Lock()
	defer i.spawnWriteMu.Unlock()
	if i.spawnGen.Load() != gen {
		return false
	}
	write()
	return true
}

// SandboxConfig holds per-session Docker sandbox settings.
type SandboxConfig struct {
	// Enabled indicates the session runs inside a container.
	Enabled bool `json:"enabled"`

	// Image is the Docker image name (e.g. "ghcr.io/asheshgoplani/agent-deck-sandbox:latest").
	Image string `json:"image"`

	// CPULimit is the optional CPU quota for the container (e.g. "2.0").
	CPULimit *string `json:"cpu_limit,omitempty"`

	// MemoryLimit is the optional memory cap for the container (e.g. "4g").
	MemoryLimit *string `json:"memory_limit,omitempty"`

	// ExtraVolumes maps host paths to container paths for additional bind mounts.
	ExtraVolumes map[string]string `json:"extra_volumes,omitempty"`
}

// resolveRealPath resolves symlinks to get the canonical path for comparison.
// Falls back to the original path on error (e.g., path doesn't exist yet).
func resolveRealPath(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

// DeduplicateDirnames returns unique directory names for the given paths.
// When multiple paths share the same basename, a numeric suffix is appended (e.g., "src-1").
func DeduplicateDirnames(paths []string) []string {
	seen := make(map[string]int)
	result := make([]string, len(paths))
	for i, p := range paths {
		dirname := filepath.Base(p)
		if n := seen[dirname]; n > 0 {
			result[i] = fmt.Sprintf("%s-%d", dirname, n)
		} else {
			result[i] = dirname
		}
		seen[dirname]++
	}
	return result
}

// MultiRepoWorktree tracks a worktree created for one repo in a multi-repo session.
type MultiRepoWorktree struct {
	OriginalPath string `json:"original_path"`
	WorktreePath string `json:"worktree_path"`
	RepoRoot     string `json:"repo_root"`
	Branch       string `json:"branch"`
}

// IsMultiRepo returns true if this session has multi-repo mode enabled.
func (inst *Instance) IsMultiRepo() bool {
	return inst.MultiRepoEnabled
}

// AllProjectPaths returns all project paths: [ProjectPath] + AdditionalPaths.
func (inst *Instance) AllProjectPaths() []string {
	paths := []string{inst.ProjectPath}
	paths = append(paths, inst.AdditionalPaths...)
	return paths
}

// EffectiveWorkingDir returns the working directory for this session.
// For multi-repo sessions, this is the temp dir; otherwise the ProjectPath.
func (inst *Instance) EffectiveWorkingDir() string {
	if inst.MultiRepoEnabled && inst.MultiRepoTempDir != "" {
		return inst.MultiRepoTempDir
	}
	return inst.ProjectPath
}

// hookCwdIsForeign reports whether a hook-reported cwd is provably OUTSIDE
// every path this instance is declared to own: EffectiveWorkingDir,
// ProjectPath, and each user-declared additional path — including their
// subdirectories, since a session may legitimately `cd` deeper inside its
// project tree between hook events (issue #1729).
//
// This is same-session evidence for the bind/rebind decision only; it never
// gates status updates for the already-bound session id. Empty cwd returns
// false (no evidence — legacy hook files and agents that send no cwd must not
// be penalized). Symlinks are resolved on both sides so macOS /tmp vs
// /private/tmp (and worktree symlinks) compare equal.
func (i *Instance) hookCwdIsForeign(cwd string) bool {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return false
	}
	resolve := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Clean(resolved)
		}
		return filepath.Clean(p)
	}
	candidate := resolve(cwd)
	owned := append([]string{i.EffectiveWorkingDir()}, i.AllProjectPaths()...)
	for _, p := range owned {
		if p == "" {
			continue
		}
		root := resolve(p)
		if candidate == root || strings.HasPrefix(candidate, root+string(os.PathSeparator)) {
			return false
		}
	}
	return true
}

// CleanupMultiRepoTempDir removes the multi-repo temporary directory.
func (inst *Instance) CleanupMultiRepoTempDir() error {
	if inst.MultiRepoTempDir == "" {
		return nil
	}
	return os.RemoveAll(inst.MultiRepoTempDir)
}

// IsSandboxed returns true if this instance is configured to run in a Docker sandbox.
func (inst *Instance) IsSandboxed() bool {
	return inst.Sandbox != nil && inst.Sandbox.Enabled
}

// IsSSH returns true if this instance runs on a remote host via SSH.
func (inst *Instance) IsSSH() bool {
	return inst.SSHHost != ""
}

// NewSandboxConfig builds a SandboxConfig from CLI flags and user settings.
// imageOverride takes precedence; when empty the global default image is used.
// CPU and memory limits are applied from DockerSettings when configured.
func NewSandboxConfig(imageOverride string) *SandboxConfig {
	dockerSettings := GetDockerSettings()
	image := dockerSettings.DefaultImage
	if imageOverride != "" {
		image = imageOverride
	}
	if image == "" {
		image = docker.DefaultImage()
	}
	cfg := &SandboxConfig{
		Enabled: true,
		Image:   image,
	}
	if dockerSettings.CPULimit != "" {
		cfg.CPULimit = &dockerSettings.CPULimit
	}
	if dockerSettings.MemoryLimit != "" {
		cfg.MemoryLimit = &dockerSettings.MemoryLimit
	}
	return cfg
}

// GetStatusThreadSafe returns the session status with read-lock protection.
// Use this when reading Status from a goroutine concurrent with backgroundStatusUpdate.
func (inst *Instance) GetStatusThreadSafe() Status {
	inst.mu.RLock()
	s := inst.Status
	inst.mu.RUnlock()
	return s
}

// SetStatusThreadSafe sets the session status with write-lock protection.
func (inst *Instance) SetStatusThreadSafe(s Status) {
	inst.mu.Lock()
	inst.Status = s
	inst.mu.Unlock()
}

// GetToolThreadSafe returns the tool name with read-lock protection.
func (inst *Instance) GetToolThreadSafe() string {
	inst.mu.RLock()
	t := inst.Tool
	inst.mu.RUnlock()
	return t
}

// GetTitleThreadSafe returns the session title with read-lock protection.
// Use this when reading Title from a goroutine concurrent with title syncs
// (SetField, ReconcileTitleFromClaude, pending-title reapplication) — those
// writers go through SetTitleThreadSafe below, so this getter is the other
// half of the pair readers on a different goroutine need.
func (inst *Instance) GetTitleThreadSafe() string {
	inst.mu.RLock()
	t := inst.Title
	inst.mu.RUnlock()
	return t
}

// SetTitleThreadSafe sets the session title with write-lock protection. All
// cross-goroutine writers (SetField, ReconcileTitleFromClaude, pending-title
// reapplication) must go through this rather than assigning Title directly,
// or GetTitleThreadSafe's read lock buys nothing.
func (inst *Instance) SetTitleThreadSafe(t string) {
	inst.mu.Lock()
	inst.Title = t
	inst.mu.Unlock()
}

// SetToolThreadSafe sets the tool name with write-lock protection.
func (inst *Instance) SetToolThreadSafe(t string) {
	inst.mu.Lock()
	inst.Tool = t
	inst.mu.Unlock()
}

// MarkAccessed updates the LastAccessedAt timestamp to now.
//
// #1846: also persists it via a targeted single-row UPDATE. The in-memory
// value used to reach SQLite only on the next full save cycle, which may
// never come before the TUI exits — leaving the preview's fallback hours
// stale after a restart. A full saveInstances here would be too heavy for
// the attach path (see attachSession's no-synchronous-save note); one
// UPDATE is not.
func (inst *Instance) MarkAccessed() {
	inst.LastAccessedAt = time.Now()
	if db := statedb.GetGlobal(); db != nil {
		if err := db.WriteLastAccessed(inst.ID, inst.LastAccessedAt); err != nil {
			sessionLog.Debug("last_accessed_persist_failed",
				slog.String("instance", inst.ID),
				slog.String("error", err.Error()),
			)
		}
	}
}

// GetLastActivityTime returns when the session was last active (content changed)
// Returns CreatedAt if no activity has been tracked yet
func (inst *Instance) GetLastActivityTime() time.Time {
	if inst.tmuxSession != nil {
		activityTime := inst.tmuxSession.GetLastActivityTime()
		if !activityTime.IsZero() {
			return activityTime
		}
	}
	// Fallback to CreatedAt
	return inst.CreatedAt
}

// DisplayLastActivityTime returns the timestamp to show as "last active" in
// the UI. It intentionally differs from GetLastActivityTime: that method
// returns the tmux tracker's raw lastChangeTime (which is seeded with
// time.Now() when the tracker is lazily created, so it leaks ~ the TUI load
// time for sessions that never confirm real activity — e.g. error/idle/
// stopped panes) and also feeds OpenCode rotation windows, so its semantics
// must not change.
//
// Here we consult only real activity evidence: CONFIRMED tmux activity
// (LastObservedActivity guards on realActivityConfirmed) and the durable
// last-activity record (#1846), whichever is newer. When neither exists we
// fall back to the persisted last-accessed time — matching what the web
// serves — and finally to CreatedAt. This keeps the TUI "⏱ last active"
// line in agreement with the web instead of resetting to the most recent
// TUI load. A LastAccessedAt newer than recorded activity deliberately does
// NOT win: attaching to look at a quiet session is a peek, not activity —
// the same principle the row badge's pickBadgeTime documents.
func (inst *Instance) DisplayLastActivityTime() time.Time {
	ts, ok := inst.LastObservedActivity()
	if la := inst.LastActivityAt(); la.After(ts) {
		ts, ok = la, true
	}
	if ok {
		return ts
	}
	if !inst.LastAccessedAt.IsZero() {
		return inst.LastAccessedAt
	}
	return inst.CreatedAt
}

// LastObservedActivity returns the last time the tmux tracker confirmed a
// real busy spike for this session, and a bool that is false when no
// confirmation has happened (the instance has no tmux session, or the
// tracker has never observed activity). When the bool is false the time
// value is zero.
func (inst *Instance) LastObservedActivity() (time.Time, bool) {
	if inst.tmuxSession == nil {
		return time.Time{}, false
	}
	return inst.tmuxSession.LastObservedActivity()
}

// GetWaitingSince returns when the session transitioned to waiting status
// Used for sorting notification bar (newest waiting sessions first)
func (inst *Instance) GetWaitingSince() time.Time {
	if inst.tmuxSession != nil {
		waitingSince := inst.tmuxSession.GetWaitingSince()
		if !waitingSince.IsZero() {
			return waitingSince
		}
	}
	// Fallback to CreatedAt if no waiting time tracked
	return inst.CreatedAt
}

// IsSubSession returns true if this session has a parent
func (inst *Instance) IsSubSession() bool {
	return inst.ParentSessionID != ""
}

// IsWorktree returns true if this session is running in a git worktree
func (inst *Instance) IsWorktree() bool {
	return inst.WorktreePath != ""
}

// SetParent sets the parent session ID
func (inst *Instance) SetParent(parentID string) {
	inst.ParentSessionID = parentID
}

// SetParentWithPath sets both parent session ID and parent's project path
// The project path is used to grant subagent access via --add-dir
func (inst *Instance) SetParentWithPath(parentID, parentProjectPath string) {
	inst.ParentSessionID = parentID
	inst.ParentProjectPath = parentProjectPath
}

// ClearParent removes the parent session link
func (inst *Instance) ClearParent() {
	inst.ParentSessionID = ""
	inst.ParentProjectPath = ""
}

// NewInstance creates a new session instance
func NewInstance(title, projectPath string) *Instance {
	id := GenerateID()
	// Seed the tmux socket from the installation-wide config. Callers that
	// want to override (the `--tmux-socket` CLI flag) set
	// inst.TmuxSocketName + inst.tmuxSession.SocketName before Start().
	socket := GetTmuxSettings().GetSocketName()
	tmuxSess := tmux.NewSession(title, projectPath)
	tmuxSess.SocketName = socket
	tmuxSess.InstanceID = id // Pass instance ID for activity hooks
	tmuxSess.SetInjectStatusLine(GetTmuxSettings().GetInjectStatusLine())
	tmuxSess.SetMouse(GetTmuxSettings().GetMouse())
	tmuxSess.SetClearOnRestart(GetTmuxSettings().ClearOnRestart)
	tmuxSess.SetTerminalChromeEnabled(GetTerminalSettings().GetITermBadge())

	inst := &Instance{
		ID:               id,
		Title:            title,
		ProjectPath:      projectPath,
		GroupPath:        extractGroupPath(projectPath), // Auto-assign group from path
		Tool:             "shell",
		Status:           StatusIdle,
		CreatedAt:        time.Now(),
		TmuxSocketName:   socket,
		tmuxSession:      tmuxSess,
		addedThisProcess: true,
	}
	logSessionCreated(inst)
	return inst
}

// logSessionCreated emits one INFO record per new session. Single source of
// truth so each NewInstance* constructor logs identically. See
// logging-review G1 (2026-05-07).
func logSessionCreated(inst *Instance) {
	sessionLog.Info("session_created",
		slog.String("instance_id", inst.ID),
		slog.String("title", inst.Title),
		slog.String("project_path", inst.ProjectPath),
		slog.String("tool", inst.Tool),
		slog.String("group_path", inst.GroupPath),
	)
}

// applyLaunchSettingsFromConfig copies LaunchInUserScope and LaunchAs from
// the live TmuxSettings onto the tmux session, just before each Start().
//
// Regression pin for #958 (SSH-logout session loss): three Start() call
// sites in this file each need this wire-up. Consolidating into one helper
// means dropping a single Start() path can no longer silently regress the
// fix — the field would just stay at its zero value (false / "") and the
// hermetic tests in issue958_launch_settings_wiring_test.go would fail.
func (i *Instance) applyLaunchSettingsFromConfig() {
	settings := GetTmuxSettings()
	i.tmuxSession.LaunchInUserScope = settings.GetLaunchInUserScope()
	i.tmuxSession.LaunchAs = settings.GetLaunchAs()
	// #1713: an SSH session's ProjectPath is only a local placeholder — the
	// project lives on the remote host and the pane just runs an ssh client —
	// so the tmux working-directory guards must not refuse its start over a
	// local directory nothing reads. Every local session keeps the guards.
	i.tmuxSession.WorkDirIsPlaceholder = i.IsSSH()
	i.applyVimModeFromConfig()
}

// applyVimModeFromConfig copies [claude].vim_mode onto the tmux session so the
// keysender prepends an Escape + `i` insert-mode guarantee before each send.
// Only meaningful for Claude-compatible tools; other tools never sit in a vim
// composer, so we leave the flag at its zero value (false) for them to keep
// their send path byte-identical (issue #1264).
func (i *Instance) applyVimModeFromConfig() {
	if i.tmuxSession == nil || !IsClaudeCompatible(i.Tool) {
		return
	}
	cfg, _ := LoadUserConfig()
	if cfg == nil {
		return
	}
	i.tmuxSession.VimMode = cfg.Claude.GetVimMode()
}

// NewInstanceWithGroup creates a new session instance with explicit group
func NewInstanceWithGroup(title, projectPath, groupPath string) *Instance {
	inst := NewInstance(title, projectPath)
	inst.GroupPath = groupPath
	return inst
}

// NewInstanceWithTool creates a new session with tool-specific initialization
func NewInstanceWithTool(title, projectPath, tool string) *Instance {
	id := GenerateID()
	socket := GetTmuxSettings().GetSocketName()
	tmuxSess := tmux.NewSession(title, projectPath)
	tmuxSess.SocketName = socket
	tmuxSess.InstanceID = id // Pass instance ID for activity hooks
	tmuxSess.SetInjectStatusLine(GetTmuxSettings().GetInjectStatusLine())
	tmuxSess.SetMouse(GetTmuxSettings().GetMouse())
	tmuxSess.SetClearOnRestart(GetTmuxSettings().ClearOnRestart)
	tmuxSess.SetTerminalChromeEnabled(GetTerminalSettings().GetITermBadge())

	inst := &Instance{
		ID:               id,
		Title:            title,
		ProjectPath:      projectPath,
		GroupPath:        extractGroupPath(projectPath),
		Tool:             tool,
		Status:           StatusIdle,
		CreatedAt:        time.Now(),
		TmuxSocketName:   socket,
		tmuxSession:      tmuxSess,
		addedThisProcess: true,
	}

	// Claude session ID will be detected from files Claude creates
	// No pre-assignment needed

	logSessionCreated(inst)
	return inst
}

// NewInstanceWithGroupAndTool creates a new session with explicit group and tool
func NewInstanceWithGroupAndTool(title, projectPath, groupPath, tool string) *Instance {
	inst := NewInstanceWithTool(title, projectPath, tool)
	inst.GroupPath = groupPath
	return inst
}

// GroupPathForProject is the exported wrapper around extractGroupPath. It
// gives CLI callers (issue #972) a single source of truth for "what group
// does this project path imply" — matching what NewInstance assigns by
// default — so launch/add can prefer cwd-derived groups over inherited
// parent groups without duplicating the heuristic.
func GroupPathForProject(projectPath string) string {
	return extractGroupPath(projectPath)
}

// extractGroupPath extracts a group path from project path
// e.g., "/home/user/projects/devops" -> "projects"
func extractGroupPath(projectPath string) string {
	parts := strings.Split(projectPath, "/")
	// Find meaningful directory (skip Users, home, etc.)
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		if part != "" && part != "Users" && part != "home" && !strings.HasPrefix(part, ".") {
			// Return parent directory as group if we're at project level
			if i > 0 && i == len(parts)-1 {
				parent := parts[i-1]
				if parent != "" && parent != "Users" && parent != "home" && !strings.HasPrefix(parent, ".") {
					return parent
				}
			}
			return part
		}
	}
	return DefaultGroupName
}

// buildClaudeCommand builds the claude command with session capture
// For new sessions: captures session ID via print mode, stores in tmux env, then resumes
// This ensures we always know the session ID for fork/restart features
// Respects: CLAUDE_CONFIG_DIR, dangerous_mode from user config, and [shell].env_files
func (i *Instance) buildClaudeCommand(baseCommand string) string {
	envPrefix := i.buildEnvSourceCommand()
	cmd := i.buildClaudeCommandWithMessage(baseCommand, "")
	return envPrefix + cmd
}

// buildClaudeCommandWithMessage builds the command with optional initial message
// Respects ClaudeOptions from instance if set, otherwise falls back to config defaults
func (i *Instance) buildClaudeCommandWithMessage(baseCommand, message string) string {
	if !IsClaudeCompatible(i.Tool) {
		return baseCommand
	}

	// Default empty baseCommand to "claude" so the Claude-build branch below
	// runs. An Instance row with tool=claude and an empty Command field
	// (e.g. a session whose tool_data lost its ClaudeSessionID and was
	// never assigned an explicit Command) otherwise falls all the way
	// through to the custom-command branch and returns just the env
	// prefix — pane runs `export ...;` and exits, status loops to error.
	// See feature/sessions-dispear-on-restart, Smithy repro 2026-04-27.
	if baseCommand == "" {
		baseCommand = "claude"
	}

	// Get the configured Claude command (e.g., "claude", "cdw", "cdp"),
	// resolved per instance: conductor > group (ancestor-walk) > global.
	claudeCmd := GetClaudeCommandForInstance(i)

	// #1791/#1822 F5: every branch below (continue/-c, resume/--resume,
	// resume-picker/-r, and fresh --session-id) shares a single `export ...;`
	// prefix rather than each building its own inline `KEY=val cmd` form.
	// A bare `KEY=val cmd` prefix scopes the variable to that ONE command
	// only (standard shell semantics) — when exit_to_shell (#1161) then
	// appends `; exec "$SHELL" -i` after the agent exits, that fallback
	// shell does NOT inherit a non-exported prefix var, so CLAUDE_CONFIG_DIR
	// silently reverts to the ambient ~/.claude root the moment the user
	// exits Claude and gets dropped into the pane's shell — the exact
	// wrong-account symptom #1791 exists to fix, just one step later than
	// the original repro (Codex review, PR #1822). `export` persists the
	// variable in the wrapping bash process's own environment, so it
	// survives into anything that process execs afterward, including the
	// exit-to-shell fallback. See buildBashExportPrefix for what it emits
	// (AGENTDECK_INSTANCE_ID/PROFILE always; CLAUDE_CONFIG_DIR only when
	// IsClaudeConfigDirExplicitForInstance(i) AND claudeCmd is the literal
	// "claude" binary — #1822 F3: when claudeCmd is a custom alias like
	// "cdw"/"cdp", the deck must not also export CLAUDE_CONFIG_DIR here, or
	// it overrides the alias's own resolution of which account to use).
	bashExportPrefix := i.buildBashExportPrefix(claudeCmd != "claude")

	// Get options - either from instance or create defaults from config
	opts := i.GetClaudeOptions()
	if opts == nil {
		// Fall back to config defaults
		userConfig, _ := LoadUserConfig()
		opts = NewClaudeOptions(userConfig)
	}

	// S8 (v1.7.40) defense-in-depth: non-channel-owning claude spawns
	// wrap the final exec in `env -u TELEGRAM_*` so the child process
	// is guaranteed to start without telegram env even if the shell
	// unset in buildEnvSourceCommand is somehow bypassed. #1133
	// broadens the flag list from TELEGRAM_STATE_DIR alone to every
	// var in telegramEnvVarsToStrip. Empty string for conductors,
	// explicit telegram channel owners, --inherit-telegram-env opt-in,
	// and non-claude tools (see telegramStateDirStripExpr predicate).
	execEnvPrefix := ""
	if flags := telegramExecEnvStripFlags(i); flags != "" {
		execEnvPrefix = "env " + flags + " "
	}

	// If baseCommand is just "claude", build the appropriate command
	if baseCommand == "claude" {
		// Build extra flags string from options (includes --add-dir if ParentProjectPath set)
		extraFlags := i.buildClaudeExtraFlags(opts)

		// Handle different session modes
		switch opts.SessionMode {
		case "continue":
			// Simple -c mode: continue last session.
			//
			// #1815: `-c` asks Claude to pick "the latest conversation in this
			// directory" — the same mtime guess that started the incident,
			// made inside the CLI where this guard cannot see it. When the
			// session has a conversation id of its own, resume THAT instead:
			// same intent, verified identity. `-c` is left as-is only when
			// there is no owned id to prefer, since it is then the operator's
			// explicit instruction and there is nothing better to offer.
			//
			// KNOWN OPEN RESIDUAL (review finding on #1830, not yet fixed):
			// this function (buildClaudeCommand) is only dispatched to when
			// i.ClaudeSessionID == "" (Start/StartWithMessage/the restart
			// recreate fallback all route to buildClaudeResumeCommand
			// whenever the id is non-empty), and recordedClaudeSessionID()
			// derives from that same field. So on every REACHABLE production
			// path `recorded` is always "" here and this mitigation branch
			// never fires -- `claude -c` still ships, leaving the CLI's own
			// newest-in-directory guess as the residual exposure this branch
			// was meant to close. Only the test that calls this builder
			// directly exercises it. Fixing this needs buildClaudeResumeCommand
			// (or the three dispatch call sites) reworked to route continue-mode
			// through a point where the id can be non-empty, which is deferred
			// as a follow-up rather than attempted blind here.
			if recorded := i.recordedClaudeSessionID(); recorded != "" {
				return fmt.Sprintf(`%sexec %s%s --resume %s%s`,
					bashExportPrefix, execEnvPrefix, claudeCmd, recorded, extraFlags)
			}
			sessionLog.Warn("resume: continue_mode_unverifiable",
				slog.String("instance_id", logging.SanitizeValue(i.ID)),
				slog.String("path", logging.SanitizeValue(i.ProjectPath)),
				slog.String("reason", "continue_flag_picks_newest_conversation_in_dir"))
			return fmt.Sprintf(`%sexec %s%s -c%s`, bashExportPrefix, execEnvPrefix, claudeCmd, extraFlags)

		case "resume":
			// Resume specific session by ID
			if opts.ResumeSessionID != "" {
				// #1815: identity first (the id must be this session's own),
				// then the conversation-data / project-dir check. Both live
				// behind canResumeClaudeSession.
				if canResumeClaudeSession(i, opts.ResumeSessionID) {
					// Session has conversation history - use normal --resume
					return fmt.Sprintf(`%sexec %s%s --resume %s%s`,
						bashExportPrefix, execEnvPrefix, claudeCmd, opts.ResumeSessionID, extraFlags)
				}
				// Session was never interacted with - use --session-id with same UUID.
				// CLAUDE_SESSION_ID is propagated via host-side SyncSessionIDsToTmux after start.
				//
				// #1815: when the refusal was an IDENTITY refusal the id may
				// belong to another session, so it must not be reused via
				// --session-id either (a shared id also makes the duplicate
				// sweeper kill one of the pair). Mint a fresh one.
				freshID := opts.ResumeSessionID
				if !i.resumeIdentityAllowed(opts.ResumeSessionID).Allow {
					freshID = i.replaceRefusedClaudeSessionID()
				}
				return fmt.Sprintf(
					`%sexec %s%s --session-id "%s"%s`,
					bashExportPrefix, execEnvPrefix, claudeCmd, freshID, extraFlags)
			}
			// No session ID provided - use -r flag for interactive picker
			return fmt.Sprintf(`%sexec %s%s -r%s`, bashExportPrefix, execEnvPrefix, claudeCmd, extraFlags)
		}

		// Default: new session with capture-resume pattern
		// 1. Starts Claude in print mode to get session ID
		// 2. Stores session ID in tmux environment (if capture succeeded)
		// 3. Resumes that session interactively
		// Fallback ensures Claude starts (without fork/restart support) rather than failing completely
		//
		// NOTE: These commands get wrapped in `bash -c` for fish compatibility (#47),
		// so shell aliases won't work — but real binaries/scripts are fine.
		// bashExportPrefix is the shared export prefix built above.

		// Pre-generate UUID in Go to avoid shell uuidgen (may be absent in Docker sandbox).
		// CLAUDE_SESSION_ID is also propagated via host-side SetEnvironment after tmux start.
		// Use `exec` before the final claude invocation so that when this compound
		// command is wrapped in `bash -c` (for fish compatibility), exec replaces
		// the bash process with claude, enabling proper job control (Ctrl+Z suspend / fg resume).
		sessionUUID := generateUUID()
		i.ClaudeSessionID = sessionUUID
		// #1815: minted here for this session — vouched ownership.
		i.markClaudeSessionIDVerified()

		// Startup query (#725, v1.7.67): appended as one shell-quoted
		// positional arg so multi-word queries survive bash -c. Empty
		// string means no suffix — do NOT emit empty quotes (claude would
		// treat them as an empty prompt and block).
		startupQuerySuffix := ""
		if i.StartupQuery != "" {
			startupQuerySuffix = " " + shellescape.Quote(i.StartupQuery)
		}

		var baseCmd string
		// Use pre-generated literal UUID with --session-id flag.
		// CLAUDE_SESSION_ID is propagated via host-side SetEnvironment after tmux start.
		baseCmd = fmt.Sprintf(
			`%sexec %s%s --session-id "%s"%s%s`,
			bashExportPrefix, execEnvPrefix, claudeCmd, sessionUUID, extraFlags, startupQuerySuffix)

		// If message provided, append wait-and-send logic in background.
		if message != "" {
			// Escape single quotes in message for bash
			escapedMsg := strings.ReplaceAll(message, "'", "'\"'\"'")

			// The background subshell runs independently; exec replaces
			// the current shell with claude for proper job control.
			baseCmd = fmt.Sprintf(
				`(sleep 2; SESSION_NAME=$(tmux display-message -p '#S'); `+
					`while ! tmux capture-pane -p -t "$SESSION_NAME" | tail -5 | grep -qE "^>"; do sleep 0.2; done; `+
					`tmux send-keys -l -t "$SESSION_NAME" -- '%s' \; send-keys -t "$SESSION_NAME" Enter) & `+
					`%sexec %s%s --session-id "%s"%s`,
				escapedMsg,
				bashExportPrefix, execEnvPrefix, claudeCmd, sessionUUID, extraFlags)
		}

		return baseCmd
	}

	// For custom commands (e.g., fork commands or conductor wrappers), prepend
	// the env-source prefix (CFG-03) and the bash export prefix (CFG-02) so
	// group env_file exports AND CLAUDE_CONFIG_DIR both land in the spawn env
	// before exec'ing the wrapper. baseCommand here is the per-session
	// Command field itself (a distinct axis from GetClaudeCommandForInstance,
	// which never sees an instance-level custom command — see its doc
	// comment), so the F3 custom-command gate does not apply: pass false.
	return i.buildEnvSourceCommand() + i.buildBashExportPrefix(false) + baseCommand
}

// buildBashExportPrefix builds the export prefix used in bash -c commands.
// Always exports AGENTDECK_INSTANCE_ID. CLAUDE_CONFIG_DIR is exported only
// when the user has an explicit config_dir resolved for this instance AND
// skipConfigDirForCustomCommand is false; when that gate is open, a
// prepared WorkerScratchConfigDir overrides the resolved value — same
// priority as buildClaudeCommandWithMessage and buildClaudeResumeCommand.
// See the comment there (issue #949) for why the gate is required.
//
// skipConfigDirForCustomCommand (#1822 F3): pass true when the command this
// prefix is about to precede resolves to a custom Claude alias (e.g.
// "cdw"/"cdp" from GetClaudeCommandForInstance) rather than the literal
// "claude" binary — the alias is expected to set CLAUDE_CONFIG_DIR itself
// (that's the point of aliasing), so exporting the deck's own resolved
// value here would fight it. Pass false when the caller always execs the
// literal "claude" binary (e.g. the fork-command builder) or when
// baseCommand is an unrelated instance-level custom command string
// (GetClaudeCommandForInstance never sees those — see its doc comment) —
// in both cases there is no alias downstream to defer to.
func (i *Instance) buildBashExportPrefix(skipConfigDirForCustomCommand bool) string {
	prefix := fmt.Sprintf("export AGENTDECK_INSTANCE_ID=%s; export AGENTDECK_PROFILE=%s; ", i.ID, shellescape.Quote(sessionProfileEnvValue()))
	if IsClaudeConfigDirExplicitForInstance(i) && !skipConfigDirForCustomCommand {
		// Issue #922 (reporter @bautrey): see applyWorkerScratchOverride.
		configDir := i.applyWorkerScratchOverride(GetClaudeConfigDirForInstance(i))
		// configDirShellExpr quotes the resolved config_dir — it lands in the
		// same `bash -c` payload as the quoted AGENTDECK_RESOLVED_* exports
		// below, and a config_dir containing ;/$() would otherwise inject
		// (audit F2) — and, for an --ssh session, expresses a local-home path
		// relative to $HOME so it resolves against the REMOTE user's home
		// instead of a local path that does not exist there (#1858).
		prefix += fmt.Sprintf("export CLAUDE_CONFIG_DIR=%s; ", i.configDirShellExpr(configDir))
	}
	prefix += i.buildResolvedAccountHintExports()
	return prefix
}

// buildResolvedAccountHintExports emits the three "intended account"
// hint env vars introduced by issue #925 (reporter @bautrey): the
// resolved config dir, group path, and source label from the priority
// chain. These mirror the user's *intent* and intentionally bypass
// the worker-scratch override applied to CLAUDE_CONFIG_DIR — consumer
// scripts (statusline, custom prompts, telemetry, hooks) need a stable
// label of which account this session belongs to, not agent-deck's
// per-session scratch path. Always emitted for claude-compatible
// instances (including when source resolves to "default") so consumers
// can rely on the vars being present.
//
// AGENTDECK_RESOLVED_CONFIG_DIR goes through configDirShellExpr for the same
// reason CLAUDE_CONFIG_DIR does (#1858, review finding F6): the consumers are
// hooks and statusline scripts running on the host the session runs on, so for
// an --ssh session a literal local-home path names a directory that does not
// exist there — and shipping two config-dir vars that disagree, with the
// readable one wrong, is worse than shipping one. The $HOME indirection is
// resolved by the shell that reads it, so a local session is byte-identical.
func (i *Instance) buildResolvedAccountHintExports() string {
	resolved, source := GetClaudeConfigDirSourceForInstance(i)
	return fmt.Sprintf(
		"export AGENTDECK_RESOLVED_CONFIG_DIR=%s; export AGENTDECK_RESOLVED_GROUP=%s; export AGENTDECK_RESOLVED_SOURCE=%s; ",
		i.configDirShellExpr(resolved),
		shellescape.Quote(i.GroupPath),
		shellescape.Quote(source),
	)
}

// sessionProfileEnvValue returns the effective profile name to inject as
// AGENTDECK_PROFILE into a spawned session's environment, alongside
// AGENTDECK_INSTANCE_ID. Without it, a bare `agent-deck` command run *inside* a
// non-default-profile session has no AGENTDECK_PROFILE in its shell, so
// GetEffectiveProfile falls through to "default" — resolving the wrong profile
// and silently orphaning auto-parent routing (resolveAutoParentInstance looks
// up the caller's instance against the wrong profile's session list). The deck
// process is single-profile (one Storage, one state.db), so this must match
// storage.Profile() for every session it manages. It routes through
// ResolveProfileForStorage rather than a bare GetEffectiveProfile("") because
// the host process itself may have hit the #1790 guard (an inferred,
// nonexistent CLAUDE_CONFIG_DIR-derived name) and fallen back to the
// configured default profile: injecting the raw inferred name here would
// re-open that exact hole from inside every pane the deck spawns — the pane
// classifies AGENTDECK_PROFILE as ProfileSourceEnv (explicit), bypasses the
// guard unconditionally, and silently creates/opens a different state.db
// than the host is using. We inject it explicitly at each spawn site
// (rather than relying on shell inheritance) so a child spawned from a
// child carries its own profile and not a stale inherited one.
func sessionProfileEnvValue() string {
	return resolvedProcessProfile()
}

// resolvedProcessProfile returns the profile this process actually opened
// storage under, routing through the same #1790 guard as the host's own
// startup path (ResolveProfileForStorage) rather than a bare
// GetEffectiveProfile(""). Any caller that uses a resolved profile name to
// look up or select per-profile state (spawned-pane env, per-profile Codex
// config, etc.) must go through this, not GetEffectiveProfile("") directly:
// the guard falls the host back to the configured default when
// CLAUDE_CONFIG_DIR infers a profile that doesn't exist, and
// GetEffectiveProfile("") alone recomputes the rejected inferred name from
// the same unchanged environment, bypassing the fallback (#1822 F1, and its
// Codex-tool analogue in getCodexHomeDir/isCodexHomeExplicit below).
func resolvedProcessProfile() string {
	resolved, err := ResolveProfileForStorage("")
	if err != nil {
		sessionLog.Warn("resolve_profile_env_failed", slog.String("error", err.Error()))
		return GetEffectiveProfile("")
	}
	return resolved
}

// ensureProfileEnv sets AGENTDECK_PROFILE host-side on the instance's tmux
// session so a bare `agent-deck` command run inside the session resolves the
// session's own profile rather than falling back to "default". It is the
// tool-agnostic safety net complementing the inline command-prefix injection in
// the spawn-command builders (which only some tools carry — e.g. gemini/opencode/
// generic respawn rebuild a bare resume command with no AGENTDECK_PROFILE prefix).
// Must run on every spawn/respawn success path, including the Restart()
// respawn-pane branches that return early before reaching the fallback recreate
// path. Best-effort: a failure is logged, not fatal.
func (i *Instance) ensureProfileEnv() {
	if i.tmuxSession == nil {
		return
	}
	if err := i.tmuxSession.SetEnvironment("AGENTDECK_PROFILE", sessionProfileEnvValue()); err != nil {
		sessionLog.Warn("set_profile_failed", slog.String("error", err.Error()))
	}
}

// ensureClaudeConfigDirEnv sets CLAUDE_CONFIG_DIR host-side on the instance's
// tmux session (#1791), mirroring ensureProfileEnv. Without this, the var was
// only ever applied as an inline command-prefix (CLAUDE_CONFIG_DIR=x claude
// ...) or, on some paths, not applied to the persistent pane env at all — a
// fleet survey found it missing from the pane shell in 14/40 sampled panes.
// A hand-restart inside the pane (Ctrl-C, then `claude`) then falls back to
// the default ~/.claude root: wrong billed account, and (when config roots
// share a `projects/` symlink) a transcript the deck can no longer find to
// resume. Same gate as buildBashExportPrefix/buildClaudeCommandWithMessage:
// only set when the instance has an explicit, resolved config_dir AND the
// resolved Claude command is the literal "claude" binary, not a custom
// alias (#1822 F3) — an alias like "cdw"/"cdp" is expected to set
// CLAUDE_CONFIG_DIR itself, and asserting it host-side here would override
// the alias's own resolution the next time it runs in this pane. A bare
// CLAUDE_CONFIG_DIR="" would otherwise mask the profile-scoped identity of
// panes intentionally left on the ambient ~/.claude root. Must run on every
// spawn/respawn success path alongside ensureProfileEnv. Best-effort: a
// failure is logged, not fatal.
func (i *Instance) ensureClaudeConfigDirEnv() {
	if i.tmuxSession == nil {
		return
	}
	// #1822 F7: any early return past this point means CLAUDE_CONFIG_DIR
	// should NOT be set for this instance right now. Clear a stale value a
	// previous call may have left in the tmux session env (e.g. the tool
	// changed away from Claude, or an account override was removed) so a
	// later respawn doesn't inherit an env stuck pointing at the wrong
	// account — the same class of bug #1791 fixes, just in reverse.
	if !IsClaudeCompatible(i.Tool) {
		i.clearClaudeConfigDirEnv()
		return
	}
	// #1822 F3: a custom command alias handles CLAUDE_CONFIG_DIR itself.
	if GetClaudeCommandForInstance(i) != "claude" {
		i.clearClaudeConfigDirEnv()
		return
	}
	if !IsClaudeConfigDirExplicitForInstance(i) {
		i.clearClaudeConfigDirEnv()
		return
	}
	configDir := i.applyWorkerScratchOverride(GetClaudeConfigDirForInstance(i))
	if configDir == "" {
		i.clearClaudeConfigDirEnv()
		return
	}
	if err := i.tmuxSession.SetEnvironment("CLAUDE_CONFIG_DIR", configDir); err != nil {
		sessionLog.Warn("set_claude_config_dir_failed", slog.String("error", err.Error()))
	}
}

// clearClaudeConfigDirEnv unsets CLAUDE_CONFIG_DIR from the tmux session env.
// Best-effort: a failure (e.g. no prior value, session already gone) is
// logged at debug, not warn — unlike SetEnvironment failing, there is no
// stale-account risk if this is a no-op.
func (i *Instance) clearClaudeConfigDirEnv() {
	if i.tmuxSession == nil {
		return
	}
	if err := i.tmuxSession.UnsetEnvironment("CLAUDE_CONFIG_DIR"); err != nil {
		sessionLog.Debug("unset_claude_config_dir_noop", slog.String("error", err.Error()))
	}
}

// logClaudeConfigResolution emits the CFG-07 observability line documenting
// which priority level resolved CLAUDE_CONFIG_DIR for this session.
// Owns the single CFG-07 slog message literal for this package.
//
// Callers MUST gate on IsClaudeCompatible(i.Tool). The helper does not
// re-gate — keeping the guard at each call site makes the three emission
// sites grep-auditable.
//
// Called from: Start, StartWithMessage, Restart.
// NOT called from: Fork (Fork may trigger a subsequent Start() on the
// forked instance which will log), or from any builder function.
func (i *Instance) logClaudeConfigResolution() {
	resolvedPath, source := GetClaudeConfigDirSourceForInstance(i)
	sessionLog.Info("claude config resolution",
		slog.String("session", i.ID),
		slog.String("group", i.GroupPath),
		slog.String("resolved", resolvedPath),
		slog.String("source", source),
	)
}

// ValidateClaudeExtraArgToken rejects a single --extra-arg token that looks
// like a flag mashed together with its value (issue #1431b). Each --extra-arg
// is shell-quoted as ONE argument, so `--extra-arg "--model opus"` reaches
// claude as the literal single arg '--model opus' (embedded space) — an
// unknown flag that makes claude exit on startup and leaves a dead pane the
// registry still reports as running. A token that starts with '-' AND contains
// whitespace is almost always two tokens the user meant to pass separately;
// surfacing it as an error at spawn time beats the silent tmux death. Clean
// flags ("--model") and clean values ("opus", "be concise") pass.
func ValidateClaudeExtraArgToken(token string) error {
	if strings.HasPrefix(token, "-") && strings.ContainsAny(token, " \t\n\r") {
		return fmt.Errorf(
			"--extra-arg %q looks like a flag and its value combined; pass them as separate --extra-arg tokens (e.g. --extra-arg \"--model\" --extra-arg \"opus\"), or use the first-class --model flag",
			token,
		)
	}
	return nil
}

// extraArgsSupplyModel reports whether the persisted --extra-arg tokens already
// carry a `--model` override. ValidateClaudeExtraArgToken forces flag and value
// into separate tokens, so a user-supplied model appears as a bare "--model"
// (or "--model=..." form) token. When present we must NOT also inject
// [claude].default_model, or the launch command would carry two --model flags.
func extraArgsSupplyModel(extraArgs []string) bool {
	for _, tok := range extraArgs {
		if tok == "--model" || strings.HasPrefix(tok, "--model=") {
			return true
		}
	}
	return false
}

// buildClaudeExtraFlags builds extra command-line flags string from ClaudeOptions
// Also handles instance-level flags like --add-dir for subagent access
func (i *Instance) buildClaudeExtraFlags(opts *ClaudeOptions) string {
	var flags []string

	// Instance-level flags (not from ClaudeOptions)
	// --add-dir: Grant subagent access to parent's project directory (for worktrees, etc.)
	if i.ParentProjectPath != "" {
		// shellescape: directory names may legally contain $()/`/;/space; the
		// path is re-parsed by the inner `bash -c` (see bashCWrap), so quote it
		// like --model below. Audit F1.
		flags = append(flags, "--add-dir "+shellescape.Quote(i.ParentProjectPath))
	}

	// Multi-repo: pass all project paths via --add-dir (deduplicated, excluding cwd)
	if i.MultiRepoEnabled {
		seen := make(map[string]bool)
		if i.ParentProjectPath != "" {
			seen[resolveRealPath(i.ParentProjectPath)] = true // already added above
		}
		seen[resolveRealPath(i.EffectiveWorkingDir())] = true // exclude cwd
		for _, p := range i.AllProjectPaths() {
			real := resolveRealPath(p)
			if seen[real] {
				continue
			}
			seen[real] = true
			flags = append(flags, "--add-dir "+shellescape.Quote(p)) // audit F1
		}
	}

	// Launch model resolution (#1431). An explicit per-session opts.Model
	// wins; otherwise fall back to [claude].default_model. The fallback is the
	// load-bearing fix: a session that persisted ANY other Claude option
	// (skip-permissions / chrome / teammate-mode) has a non-nil ClaudeOptions
	// with an empty Model, which bypasses the NewClaudeOptions default_model
	// fallback in the callers. Without resolving the default here, both spawn
	// and restart dropped --model entirely and the child silently booted on
	// Claude's built-in default (Fable, unavailable account-wide) while the
	// registry still showed it running. Because every start/restart/resume
	// command delegates flag assembly here, this single point keeps them all
	// in lockstep.
	// A user-supplied --model via --extra-arg (the form the
	// ValidateClaudeExtraArgToken error message recommends) is an explicit
	// override that must stand alone: the extra-arg tokens are appended verbatim
	// below, so emitting any resolved --model here too would produce a duplicate
	// --model on the command line. claude is last-wins so it would be harmless,
	// but it is confusing and the operator's intent is unambiguous. Suppress the
	// resolved model entirely (whether it came from opts.Model, the
	// NewClaudeOptions default, or [claude].default_model) when extra-args carry
	// their own.
	if !extraArgsSupplyModel(i.ExtraArgs) {
		launchModel := ""
		if opts != nil {
			launchModel = opts.Model
		}
		// Conductor/group model chain (#8): explicit opts.Model wins, then the
		// per-conductor then per-group [*.claude].model overrides.
		launchModel = i.resolveClaudeLaunchModel(launchModel)
		// Finally fall back to the global [claude].default_model (#1437).
		if launchModel == "" {
			if cfg, _ := LoadUserConfig(); cfg != nil {
				launchModel = cfg.Claude.DefaultModel
			}
		}
		if launchModel != "" {
			flags = append(flags, "--model "+shellescape.Quote(launchModel))
		}
	}

	if opts != nil && strings.TrimSpace(opts.Effort) != "" {
		flags = append(flags, "--effort "+shellescape.Quote(strings.TrimSpace(opts.Effort)))
	}

	// Options-level flags
	if opts != nil {
		if opts.SkipPermissions {
			flags = append(flags, "--dangerously-skip-permissions")
		} else if opts.AutoMode {
			flags = append(flags, "--permission-mode auto")
		} else if opts.AllowSkipPermissions {
			flags = append(flags, "--allow-dangerously-skip-permissions")
		}
		if opts.UseChrome {
			flags = append(flags, "--chrome")
		}
		if opts.UseTeammateMode {
			flags = append(flags, "--teammate-mode tmux")
		}
	}

	// Plugin channels: subscribe the claude session to inbound messages from
	// each listed plugin channel. Persisted on Instance.Channels and refreshed
	// on every Start/Restart/resume because every command-build flows here.
	// Heal first: a conductor whose persisted Channels lost the telegram
	// entry (index wipe, record rebuild) is restored from conductor config
	// so the wiring can't silently disappear (telegram_reliability.go).
	reconcileConductorTelegramChannel(i)
	if len(i.Channels) > 0 {
		flags = append(flags, "--channels "+shellescape.Quote(strings.Join(i.Channels, ","))) // audit F1
	}

	// User-supplied extra args: each token is shellescape-quoted before
	// re-emission so values with spaces survive the `bash -c` wrapper
	// without being re-tokenized. Appended last so user flags can override
	// defaults claude accepts in last-wins ordering.
	//
	// KNOWN OPEN RESIDUAL (review finding on #1830, not yet fixed): these
	// tokens are appended AFTER canResumeClaudeSession has already decided
	// --resume vs --session-id above, so an operator-configured ExtraArgs
	// entry of e.g. `--resume <foreign-uuid>` or `-c` still reaches the final
	// command line on a command the chokepoint just refused. #1407 copies a
	// parent's ExtraArgs onto every fork, so a misconfigured parent would
	// propagate the bypass to all children. ExtraArgs is operator-configured
	// (not an external input), which bounds the severity, but this flags is
	// not the "cannot be bypassed" guarantee this file's package doc claims.
	// Stripping/logging --resume/-r/-c/--fork-session/--session-id tokens
	// from ExtraArgs on the claude path is deferred as a follow-up.
	for _, tok := range i.ExtraArgs {
		flags = append(flags, shellescape.Quote(tok))
	}

	if len(flags) == 0 {
		return ""
	}
	return " " + strings.Join(flags, " ")
}

// buildGeminiCommand builds the gemini command with session capture
// For new sessions: captures session ID via stream-json, stores in tmux env, then resumes
// For sessions with known ID: uses simple resume
// This ensures we always know the session ID for restart features
// VERIFIED: gemini --output-format stream-json provides immediate session ID in first message
// Also sources .env files from [shell].env_files and [gemini].env_file
func (i *Instance) buildGeminiCommand(baseCommand string) string {
	if i.Tool != "gemini" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	// Determine if YOLO mode is enabled (per-session overrides global config)
	yoloMode := false
	if i.GeminiYoloMode != nil {
		yoloMode = *i.GeminiYoloMode
	} else {
		// Check global config
		userConfig, _ := LoadUserConfig()
		if userConfig != nil {
			yoloMode = userConfig.Gemini.YoloMode
		}
	}

	yoloFlag := ""
	if yoloMode {
		yoloFlag = " --yolo"
	}

	// Determine model flag
	modelFlag := ""
	if i.GeminiModel != "" {
		modelFlag = " --model " + i.GeminiModel
	} else if i.GeminiSessionID == "" {
		// Only apply default model for NEW sessions (not resumes)
		userConfig, _ := LoadUserConfig()
		if userConfig != nil && userConfig.Gemini.DefaultModel != "" {
			modelFlag = " --model " + userConfig.Gemini.DefaultModel
		}
	}

	// If baseCommand is just "gemini", handle specially
	if baseCommand == "gemini" {
		cmd := GetToolCommand("gemini")
		// If we already have a session ID, use simple resume
		if i.GeminiSessionID != "" {
			// GEMINI_YOLO_MODE and GEMINI_SESSION_ID are propagated via host-side
			// SetEnvironment after tmux start. No inline tmux set-environment.
			return envPrefix + fmt.Sprintf(
				"%s --resume %s%s%s",
				cmd,
				i.GeminiSessionID,
				yoloFlag,
				modelFlag,
			)
		}

		// Start Gemini fresh - session ID will be captured when user interacts
		// The previous capture-resume approach (gemini --output-format json ".") would hang
		// because Gemini processes the "." prompt which takes too long
		// GEMINI_YOLO_MODE is propagated via host-side SetEnvironment after tmux start.
		return envPrefix + fmt.Sprintf(
			`%s%s%s`,
			cmd,
			yoloFlag,
			modelFlag,
		)
	}

	// For custom commands (e.g., resume commands), return as-is
	return envPrefix + baseCommand
}

// buildOpenCodeCommand builds the command for OpenCode CLI
// OpenCode stores sessions in ~/.local/share/opencode/storage/session/
// Session IDs are in format: ses_XXXXX
// Resume: opencode -s <session-id> or opencode --session <session-id>
// Continue last: opencode -c or opencode --continue
// Model: opencode -m provider/model
// Agent: opencode --agent name
// Also sources .env files from [shell].env_files
func (i *Instance) buildOpenCodeCommand(baseCommand string) string {
	if i.Tool != "opencode" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	// If baseCommand is just "opencode", handle specially
	if baseCommand == "opencode" {
		cmd := GetToolCommand("opencode")
		extraFlags := i.buildOpenCodeExtraFlags() + i.buildOpenCodeSSEPortFlag()

		// If we already have a session ID, use resume with -s flag.
		// OPENCODE_SESSION_ID is propagated via host-side SetEnvironment after tmux start.
		if i.OpenCodeSessionID != "" {
			return envPrefix + fmt.Sprintf("%s -s %s%s",
				cmd, i.OpenCodeSessionID, extraFlags)
		}

		// Start OpenCode fresh - session ID will be captured async after startup
		return envPrefix + cmd + extraFlags
	}

	// For custom commands (e.g., fork commands), return as-is. No --port is
	// injected, so clear any stale port from a prior launch — otherwise the
	// SSE watcher could connect to a freed port later reused by an unrelated
	// process (issue #1614). Status falls back to tmux content sniffing.
	i.setOpenCodePort(0)
	return envPrefix + baseCommand
}

// buildOpenCodeExtraFlags returns extra CLI flags from OpenCodeOptions (model, agent).
// Returns a string with leading space, or empty string if no flags.
func (i *Instance) buildOpenCodeExtraFlags() string {
	opts := i.GetOpenCodeOptions()
	if opts == nil {
		// Fall back to global config defaults
		if config, err := LoadUserConfig(); err == nil && config != nil {
			opts = NewOpenCodeOptions(config)
		}
	}
	if opts == nil {
		return ""
	}

	var flags string
	if opts.Model != "" {
		flags += " -m " + opts.Model
	}
	if opts.Agent != "" {
		flags += " --agent " + opts.Agent
	}
	return flags
}

// buildOpenCodeSSEPortFlag allocates a localhost port for OpenCode's event
// server and returns " --port N" (issue #1614). With an explicit --port, the
// OpenCode TUI binds a real HTTP server whose /event SSE stream publishes
// session.status transitions; OpenCodeSSEWatcher consumes it for real-time
// status instead of tmux content sniffing. Returns "" (and clears the stored
// port) when disabled via [opencode].disable_sse_status or when no port could
// be allocated — status detection then falls back to tmux polling.
func (i *Instance) buildOpenCodeSSEPortFlag() string {
	if config, err := LoadUserConfig(); err == nil && config != nil && config.OpenCode.DisableSSEStatus {
		i.setOpenCodePort(0)
		return ""
	}
	port, err := allocateLocalPort()
	if err != nil {
		sessionLog.Warn("opencode_sse_port_alloc_failed", slog.String("error", err.Error()))
		i.setOpenCodePort(0)
		return ""
	}
	i.setOpenCodePort(port)
	return fmt.Sprintf(" --port %d", port)
}

// allocateLocalPort reserves a free 127.0.0.1 port by binding :0 and
// immediately releasing it. The tiny window before OpenCode re-binds it is
// the standard trade-off; a collision surfaces as a failed launch and the
// next restart picks a fresh port.
func allocateLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// GetOpenCodePort returns the event-server port with lock protection.
func (i *Instance) GetOpenCodePort() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.OpenCodePort
}

func (i *Instance) setOpenCodePort(port int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.OpenCodePort = port
}

// UpdateOpenCodeSSEStatus feeds an SSE-derived status ("running"/"waiting")
// into the instance for the SSE fast path in UpdateStatus (issue #1614).
func (i *Instance) UpdateOpenCodeSSEStatus(status string, updatedAt time.Time) {
	if status == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	// A transition means new activity/output the user hasn't seen yet: reset
	// acknowledgment so waiting renders orange, mirroring UpdateHookStatus.
	if status != i.sseStatus && i.tmuxSession != nil {
		i.tmuxSession.ResetAcknowledged()
	}
	i.sseStatus = status
	i.sseLastUpdate = updatedAt
}

// DetectOpenCodeSession is the public wrapper for async OpenCode session detection
// Call this for restored sessions that don't have a session ID yet
func (i *Instance) DetectOpenCodeSession() {
	i.detectOpenCodeSessionAsync()
}

// buildCodexCommand builds the command for OpenAI Codex CLI
// resolveCodexYoloFlag returns " --yolo" if yolo mode is enabled (per-session override > global config), or "".
func (i *Instance) resolveCodexYoloFlag() string {
	opts := i.GetCodexOptions()
	if opts != nil && opts.YoloMode != nil {
		if *opts.YoloMode {
			return " --yolo"
		}
		return ""
	}
	// Fallback to global config
	if config, err := LoadUserConfig(); err == nil && config != nil {
		if config.Codex.YoloMode {
			return " --yolo"
		}
	}
	return ""
}

func (i *Instance) resolveCodexModelFlag() string {
	opts := i.GetCodexOptions()
	if opts != nil && strings.TrimSpace(opts.Model) != "" {
		return " --model " + shellescape.Quote(strings.TrimSpace(opts.Model))
	}
	return ""
}

func (i *Instance) resolveCodexReasoningEffortFlag() string {
	opts := i.GetCodexOptions()
	if opts != nil && strings.TrimSpace(opts.ReasoningEffort) != "" {
		value := "model_reasoning_effort=" + strings.TrimSpace(opts.ReasoningEffort)
		return " --config " + shellescape.Quote(value)
	}
	return ""
}

func (i *Instance) resolveCodexCommand(baseCommand string) string {
	command := strings.TrimSpace(baseCommand)
	if i.Tool == "codex" && (command == "" || command == "codex") {
		return GetCodexCommand()
	}
	if command == "" {
		return "codex"
	}
	return command
}

func codexHomeFromCommand(command string) string {
	rest := strings.TrimSpace(command)
	for rest != "" {
		token, remainder, ok := nextShellWord(rest)
		if !ok {
			return ""
		}
		if !isShellEnvAssignment(token) {
			return ""
		}
		key, value, ok := strings.Cut(token, "=")
		if !ok {
			return ""
		}
		if key == "CODEX_HOME" && strings.TrimSpace(value) != "" {
			return ExpandPath(strings.TrimSpace(value))
		}
		rest = strings.TrimLeft(remainder, " \t\r\n")
	}
	return ""
}

func nextShellWord(s string) (word string, remainder string, ok bool) {
	s = strings.TrimLeft(s, " \t\r\n")
	if s == "" {
		return "", "", false
	}

	var b strings.Builder
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote == 0 {
			if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
				return b.String(), s[i:], true
			}
			switch c {
			case '\'', '"':
				quote = c
			case '\\':
				if i+1 < len(s) {
					i++
					b.WriteByte(s[i])
				} else {
					b.WriteByte(c)
				}
			default:
				b.WriteByte(c)
			}
			continue
		}

		if c == quote {
			quote = 0
			continue
		}
		if quote == '"' && c == '\\' && i+1 < len(s) {
			i++
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(c)
	}
	if quote != 0 {
		return "", "", false
	}
	return b.String(), "", true
}

// accountCodexHomeDir returns the CODEX_HOME mapped to this session's account
// slot via [profiles.<account>.codex].config_dir, or "" when the session has no
// account or the account names no codex block.
//
// Mirrors the account level of resolveClaudeConfigDir (#924): the per-session
// account is the most specific thing the user can say about which credentials a
// session runs under, so it outranks the process-wide CODEX_HOME and the
// profile/global config. An account with no matching block falls through
// silently, matching the permissive style of the Claude chain.
func (i *Instance) accountCodexHomeDir() string {
	if i == nil || strings.TrimSpace(i.Account) == "" {
		return ""
	}
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.GetProfileCodexConfigDir(i.Account)
}

// codexHomeToExport returns the CODEX_HOME this session must launch with, or ""
// when nothing should be exported and codex may use its own default.
//
// Both codex launch paths (normal and fork) call this so they cannot drift: a
// session that resolves its home one way and launches another is exactly the
// #1922 failure — the account was recorded and reported, but the process ran
// against ~/.codex.
func (i *Instance) codexHomeToExport() string {
	if accountHome := strings.TrimSpace(i.accountCodexHomeDir()); accountHome != "" {
		return accountHome
	}
	if isCodexHomeExplicit() {
		return strings.TrimSpace(getCodexHomeDir())
	}
	return ""
}

// codexHomeForCommand returns the CODEX_HOME the launched process will actually
// read and write under, for an already-resolved codex command line.
//
// This is the read-side mirror of codexHomeToExport and must stay in step with
// it: whatever that exports, this resolves to. codexHomeToExport returns "" when
// nothing needs exporting, in which case the process inherits the ambient
// CODEX_HOME or falls back to ~/.codex — exactly what getCodexHomeDir resolves
// to in that case.
//
// A resolver that disagrees with the launch is #1929: buildCodexCommand's
// resume gate looked for the rollout in the DEFAULT home while the launch wrote
// it under the ACCOUNT home, so the gate always missed, dropped the session id,
// and every restart silently opened a fresh conversation.
func (i *Instance) codexHomeForCommand(command string) string {
	// A CODEX_HOME= embedded in the command is the user typing it by hand for
	// this session, so it stays the most explicit signal of all — and the shell
	// agrees, since it is emitted after any exported CODEX_HOME.
	if codexHome := codexHomeFromCommand(command); codexHome != "" {
		return codexHome
	}
	if accountHome := strings.TrimSpace(i.accountCodexHomeDir()); accountHome != "" {
		return accountHome
	}
	return getCodexHomeDir()
}

func (i *Instance) getCodexHomeDir() string {
	if i == nil {
		return getCodexHomeDir()
	}
	return i.codexHomeForCommand(i.resolveCodexCommand(i.Command))
}

// Codex stores sessions in ~/.codex/sessions/YYYY/MM/DD/*.jsonl
// Resume: codex resume <session-id> or codex resume --last
// Also sources .env files from [shell].env_files
func (i *Instance) buildCodexCommand(baseCommand string) string {
	if !IsCodexCompatible(i.Tool) {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	// AGENTDECK_* env injection is required for the hook subprocesses spawned
	// by tools in the codex family to find this session's state, so it is
	// injected BEFORE the custom-command passthrough early-return below.
	// Dropping it on custom-command sessions was the design regression flagged
	// on #951 review — keep AGENTDECK_* on every codex-flavoured launch.
	agentdeckEnvPrefix := fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s AGENTDECK_TITLE=%q AGENTDECK_TOOL=%s AGENTDECK_PROFILE=%s ",
		i.ID, i.Title, i.Tool, shellescape.Quote(sessionProfileEnvValue()))
	envPrefix += agentdeckEnvPrefix

	// Passthrough: if the tool is literally "codex" and user gave a custom command
	// (not the bare "codex" name), return as-is without flag injection.
	// Codex-compatible tools (e.g., "my-codex" with CompatibleWith="codex") always
	// get the full treatment regardless of their command name.
	trimmed := strings.TrimSpace(baseCommand)
	if i.Tool == "codex" && trimmed != "codex" && trimmed != "" {
		return envPrefix + trimmed
	}
	if codexHome := i.codexHomeToExport(); codexHome != "" {
		if err := os.MkdirAll(codexHome, 0o755); err != nil {
			sessionLog.Warn("codex_home_mkdir_failed",
				slog.String("path", codexHome),
				slog.String("error", err.Error()))
		}
		envPrefix += "CODEX_HOME=" + codexHome + " "
	}

	yoloFlag := i.resolveCodexYoloFlag()
	modelFlag := i.resolveCodexModelFlag()
	reasoningFlag := i.resolveCodexReasoningEffortFlag()
	command := i.resolveCodexCommand(baseCommand)
	// Must be the home this launch exports (above), not the process-wide one:
	// the gates below decide whether a rollout exists, and looking in the wrong
	// home destroys a live binding (#1929).
	codexHome := i.codexHomeForCommand(command)

	// Issue #756: Gate `codex resume <sid>` on rollout-file existence.
	// If Codex died before flushing its rollout JSONL (tmux crash, kill -9
	// in the SessionStart→first-flush window), the captured session_id is
	// permanently unresumable. Without this check the bridge appends
	// `resume <stale-uuid>` on every restart and Codex exits immediately,
	// flipping the session back to error in an infinite loop. Drop the
	// stale ID, clear the .sid sidecar so the next hook tick rebinds
	// cleanly, and spawn fresh.
	if i.CodexSessionID != "" && !codexRolloutExistsInHome(i.CodexSessionID, codexHome) {
		sessionLog.Warn("codex_resume_stale_sid_dropped",
			slog.String("instance_id", i.ID),
			slog.String("title", i.Title),
			slog.String("sid", i.CodexSessionID),
			slog.String("codex_home", codexHome))
		i.CodexSessionID = ""
		i.CodexDetectedAt = time.Time{}
		ClearHookSessionAnchor(i.ID)
	}

	// Safety net (incident 2026-07-15): codex loads subagent-sourced threads
	// via `resume` but refuses user-initiated turns on them — the TUI exits
	// status 1 with "turn/start failed in TUI" on the first typed message,
	// killing the tmux session in an error loop. This bites bindings
	// poisoned by a subagent turn-complete hook before the gate existed (or
	// raced past it) AND sessions legitimately living on an adopted subagent
	// thread from an earlier mid-flight restart. `codex fork` carries the
	// thread's full context into a fresh thread_source=user thread that
	// accepts input; the live-process probe then rebinds the instance to the
	// fork's new id. See codex_subagent_gate.go.
	if i.CodexSessionID != "" && codexSessionNeedsFork(i.CodexSessionID, codexHome) {
		sessionLog.Warn("codex_subagent_binding_forked",
			slog.String("instance_id", i.ID),
			slog.String("title", i.Title),
			slog.String("sid", i.CodexSessionID))
		return envPrefix + fmt.Sprintf("%s%s%s%s fork %s",
			command, yoloFlag, modelFlag, reasoningFlag, i.CodexSessionID)
	}

	if i.CodexSessionID != "" {
		return envPrefix + fmt.Sprintf("%s%s%s%s resume %s",
			command, yoloFlag, modelFlag, reasoningFlag, i.CodexSessionID)
	}

	return envPrefix + command + yoloFlag + modelFlag + reasoningFlag
}

// buildCodexCommandWithPrompt builds the Codex launch command with an initial
// prompt delivered as Codex's own positional [PROMPT] argument, mirroring the
// claude-code startup query (#725). It reports whether the prompt was embedded;
// when it was not, the caller must fall back to the post-start typing path.
//
// Typing a large initial prompt into a live Codex TUI is unreliable: the message
// is sent as one literal `tmux send-keys -l` burst followed immediately by Enter,
// and Codex reads a large fast burst as a paste, swallowing the trailing Enter
// into it. The prompt then sits unsubmitted in the composer and the agent never
// starts. `codex [OPTIONS] [PROMPT]` starts the session with the prompt already
// in hand, so nothing is typed and there is no Enter to lose.
//
// The prompt is only embedded on the plain fresh-start path. It is NOT embedded
// when:
//   - the prompt is empty (nothing to deliver);
//   - the user supplied a custom command (buildCodexCommand passes it through
//     verbatim, and an arbitrary wrapper need not accept a positional prompt);
//   - the session resumes (`codex ... resume <sid>`), where a trailing operand
//     would not be the subcommand's prompt.
//
// In each of those cases the caller keeps the existing behaviour unchanged.
func (i *Instance) buildCodexCommandWithPrompt(baseCommand, prompt string) (string, bool) {
	command := i.buildCodexCommand(baseCommand)
	if strings.TrimSpace(prompt) == "" {
		return command, false
	}
	if !IsCodexCompatible(i.Tool) {
		return command, false
	}
	// Custom-command passthrough: buildCodexCommand returned the user's command
	// as-is, so we cannot assume it takes a positional prompt.
	trimmed := strings.TrimSpace(baseCommand)
	if i.Tool == "codex" && trimmed != "codex" && trimmed != "" {
		return command, false
	}
	// Resume path appends `resume <sid>`; leave it to the typing path.
	if i.CodexSessionID != "" {
		return command, false
	}
	return command + " " + shellescape.Quote(prompt), true
}

// piAgentDeckSessionDirExpr returns a target-shell expression for the Pi session
// directory Agent Deck owns for an instance. It intentionally uses target-side
// $HOME rather than resolving the Agent Deck process' home directory, keeping
// local, SSH, and sandbox launch paths consistent.
func piAgentDeckSessionDirExpr(instanceID string) string {
	return "${HOME}/.pi/agent-deck/" + shellescape.Quote(instanceID)
}

// buildPiCommand builds the command for the Pi CLI.
// Pi sessions are JSONL files, not externally named sessions like Claude/Codex.
// Scope Pi's session directory to the Agent Deck instance and always launch
// with --continue so restarts resume that instance without colliding with other
// Agent Deck Pi sessions in the same project.
func (i *Instance) buildPiCommand(baseCommand string) string {
	if i.Tool != "pi" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()
	cmd := strings.TrimSpace(baseCommand)
	if cmd == "" {
		cmd = "pi"
	}

	sessionDir := piAgentDeckSessionDirExpr(i.ID)
	quotedInstanceID := shellescape.Quote(i.ID)
	quotedProfile := shellescape.Quote(sessionProfileEnvValue())

	return envPrefix + fmt.Sprintf(
		"session_dir=%s; mkdir -p \"$session_dir\" && AGENTDECK_INSTANCE_ID=%s AGENTDECK_PROFILE=%s %s --continue --session-dir \"$session_dir\"",
		sessionDir,
		quotedInstanceID,
		quotedProfile,
		cmd,
	)
}

func (i *Instance) buildPiForkCommandForTarget(target *Instance, baseCommand string) (string, error) {
	if target == nil {
		return "", fmt.Errorf("cannot build Pi fork command: target instance is nil")
	}
	if !i.CanForkPi() {
		return "", fmt.Errorf("cannot fork: no Agent Deck Pi session directory")
	}

	envPrefix := target.buildEnvSourceCommand()
	cmd := strings.TrimSpace(baseCommand)
	if cmd == "" {
		cmd = "pi"
	}

	parentSessionDir := piAgentDeckSessionDirExpr(i.ID)
	sessionDir := piAgentDeckSessionDirExpr(target.ID)
	quotedInstanceID := shellescape.Quote(target.ID)
	quotedProfile := shellescape.Quote(sessionProfileEnvValue())

	return envPrefix + fmt.Sprintf(
		"parent_session_dir=%s; session_dir=%s; mkdir -p \"$session_dir\" && source_file=$(find \"$parent_session_dir\" -type f -name '*.jsonl' -exec ls -t {} + 2>/dev/null | head -n 1); if [ -z \"$source_file\" ]; then echo \"No Pi session file found in $parent_session_dir\" >&2; exit 1; fi; AGENTDECK_INSTANCE_ID=%s AGENTDECK_PROFILE=%s %s --fork \"$source_file\" --session-dir \"$session_dir\"",
		parentSessionDir,
		sessionDir,
		quotedInstanceID,
		quotedProfile,
		cmd,
	), nil
}

func (i *Instance) consumeForkStartCommand() string {
	command := i.Command
	if i.ForkStartCommand != "" {
		command = i.ForkStartCommand
		i.ForkStartCommand = ""
	}
	i.IsForkAwaitingStart = false
	return command
}

// cursorTrustWorkspacePath returns the workspace path Cursor should trust for
// this instance: container /workspace for sandboxes, SSHRemotePath for remote
// sessions, otherwise the effective local working directory.
func (i *Instance) cursorTrustWorkspacePath() string {
	if i.IsSandboxed() {
		return cursorSandboxWorkDir
	}
	if i.IsSSH() && i.SSHRemotePath != "" {
		return i.SSHRemotePath
	}
	return i.EffectiveWorkingDir()
}

// preAcceptCursorWorkspaceTrust seeds Cursor workspace trust for the session's
// workspace so interactive launches skip the trust prompt. Local, SSH, and
// sandbox sessions each write trust where Cursor will read it. Failures are
// logged and non-fatal.
func (i *Instance) preAcceptCursorWorkspaceTrust() {
	if i.Tool != "cursor" {
		return
	}
	dir := i.cursorTrustWorkspacePath()
	if dir == "" {
		return
	}
	var err error
	switch {
	case i.IsSandboxed() && i.SandboxContainer != "":
		err = PreAcceptCursorTrustInContainer(i.SandboxContainer, dir)
	case i.IsSSH():
		err = PreAcceptCursorTrustSSH(i.SSHHost, dir)
	default:
		err = PreAcceptCursorTrust(GetCursorConfigDir(), dir)
	}
	if err != nil {
		sessionLog.Warn("cursor_preaccept_trust_failed",
			slog.String("instance_id", i.ID),
			slog.String("dir", dir),
			slog.String("error", err.Error()))
	}
}

// buildCursorCommand builds the command for the Cursor Agent CLI.
// continuePrev adds --continue so Restart resumes the previous chat in the workspace.
// Env files from [shell].env_files are applied via buildEnvSourceCommand.
//
// Default entrypoint resolution prefers the standalone `agent` binary when it is
// on PATH, falling back to `cursor agent`. See DefaultCursorCommand.
func (i *Instance) buildCursorCommand(baseCommand string, continuePrev bool) string {
	if i.Tool != "cursor" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()
	cmd := strings.TrimSpace(baseCommand)
	if isDefaultCursorInvocation(cmd) {
		cmd = GetToolCommand("cursor")
	}

	out := envPrefix + cmd
	if continuePrev && !strings.Contains(strings.ToLower(cmd), "--continue") {
		out += " --continue"
	}
	return out
}

// buildCopilotCommand builds the command for GitHub Copilot CLI.
// If baseCommand is the bare "copilot" name, applies config command override + env prefix.
// Otherwise returns the custom command as-is with env prefix (passthrough).
func (i *Instance) buildCopilotCommand(baseCommand string) string {
	if i.Tool != "copilot" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	if baseCommand != "copilot" {
		return envPrefix + baseCommand
	}

	return envPrefix + GetToolCommand("copilot")
}

// codexRolloutExistsInHome reports whether Codex has flushed a rollout JSONL
// for the given session ID under codexHome/sessions. Used by buildCodexCommand
// to gate `codex resume <sid>` on a real on-disk rollout file (Issue #756).
//
// Codex layout: codexHome/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl
func codexRolloutExistsInHome(sessionID, codexHome string) bool {
	return codexRolloutPathInHome(sessionID, codexHome) != ""
}

// detectOpenCodeSessionAsync detects the OpenCode session ID after startup
// OpenCode generates session IDs internally (format: ses_XXXXX)
// We query the managed OpenCode HTTP server (or the bounded compatibility CLI
// fallback) and match by project directory, picking the most recently updated
// session since OpenCode auto-resumes the last session.
func (i *Instance) detectOpenCodeSessionAsync() {
	time.Sleep(1 * time.Second)

	// Phase 1: Quick detection for existing sessions (5 attempts over ~15s)
	quickDelays := []time.Duration{0, 2 * time.Second, 3 * time.Second, 4 * time.Second, 5 * time.Second}

	for attempt, delay := range quickDelays {
		if delay > 0 {
			time.Sleep(delay)
		}

		if sessionID := i.queryOpenCodeSession(); sessionID != "" {
			i.setOpenCodeSession(sessionID)
			sessionLog.Debug(
				"opencode_session_detected",
				slog.String("session_id", sessionID),
				slog.String("phase", "quick"),
				slog.Int("attempt", attempt+1),
			)
			return
		}

		sessionLog.Debug(
			"opencode_session_not_found",
			slog.Int("attempt", attempt+1),
			slog.Int("total", len(quickDelays)),
		)
	}

	// Phase 2: Long-running background watcher for new sessions
	// OpenCode only persists new sessions after significant user activity
	go i.watchForOpenCodeSession()
}

// watchForOpenCodeSession polls for session creation over an extended period.
// New sessions may take minutes to be persisted by OpenCode.
func (i *Instance) watchForOpenCodeSession() {
	const (
		pollInterval = 10 * time.Second
		maxDuration  = 5 * time.Minute
	)

	deadline := time.Now().Add(maxDuration)
	attempt := 0

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		attempt++

		i.mu.RLock()
		alreadyDetected := i.OpenCodeSessionID != ""
		i.mu.RUnlock()
		if alreadyDetected {
			sessionLog.Debug("opencode_watcher_already_set")
			return
		}

		if sessionID := i.queryOpenCodeSession(); sessionID != "" {
			i.setOpenCodeSession(sessionID)
			sessionLog.Debug(
				"opencode_watcher_detected",
				slog.String("session_id", sessionID),
				slog.Int("attempt", attempt),
			)
			return
		}

		sessionLog.Debug("opencode_watcher_not_found", slog.Int("attempt", attempt))
	}

	sessionLog.Debug("opencode_watcher_timeout", slog.Duration("max_duration", maxDuration))
}

// setOpenCodeSession sets the session ID and stores it in tmux environment.
func (i *Instance) setOpenCodeSession(sessionID string) {
	i.mu.Lock()
	i.setOpenCodeSessionLocked(sessionID)
	tmuxSession := i.tmuxSession
	i.mu.Unlock()
	i.syncOpenCodeSessionEnvironment(tmuxSession, sessionID)
}

// setOpenCodeSessionLocked updates in-memory binding state. The caller must
// hold i.mu for writing.
func (i *Instance) setOpenCodeSessionLocked(sessionID string) {
	i.OpenCodeSessionID = sessionID
	i.OpenCodeDetectedAt = time.Now()
	i.OpenCodeStartedAt = 0
}

func (i *Instance) syncOpenCodeSessionEnvironment(tmuxSession *tmux.Session, sessionID string) {
	if tmuxSession != nil {
		if err := tmuxSession.SetEnvironment("OPENCODE_SESSION_ID", sessionID); err != nil {
			sessionLog.Warn("opencode_set_env_failed", slog.String("error", err.Error()))
		}
	}
}

type openCodeSessionMetadata struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Path      string `json:"path"`
	Created   int64  `json:"created"`
	Updated   int64  `json:"updated"`
}

type openCodeHTTPSessionMetadata struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Path      string `json:"path"`
	Location  struct {
		Directory string `json:"directory"`
	} `json:"location"`
	Time struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

type openCodeCLIQueryCacheEntry struct {
	queriedAt time.Time
	sessions  []openCodeSessionMetadata
}

// findBestOpenCodeSession keeps an existing binding if that session still exists
// for the project. Fresh launches stay unbound until OpenCode persists a session
// created during the current startup, which prevents adopting older same-project
// sessions before the new conversation has an ID. Already-bound sessions only
// rotate to a newer sibling when there was very recent local pane activity,
// which approximates an intentional in-pane `/new` without stealing sessions
// from other tabs in the same project.
func findBestOpenCodeSession(sessions []openCodeSessionMetadata, projectPath, currentID string, startedAt, activityAt int64) string {
	normalizedProjectPath := normalizePath(projectPath)

	var bestMatch string
	var bestMatchTime int64
	var currentMatchTime int64
	var currentExists bool
	var localRotationMatch string
	var localRotationTime int64
	startupThreshold := startedAt - opencodeStartupTimeSkew.Milliseconds()
	activityThreshold := activityAt - opencodeStartupTimeSkew.Milliseconds()

	for _, sess := range sessions {
		sessDir := sess.Directory
		if sessDir == "" {
			sessDir = sess.Path
		}

		if sessDir == "" || normalizePath(sessDir) != normalizedProjectPath {
			continue
		}

		// Multiple OpenCode tabs can share a project path. A newer sibling session
		// is not enough evidence to steal this instance's existing binding.
		updatedAt := sess.Updated
		if updatedAt == 0 {
			updatedAt = sess.Created
		}

		if currentID != "" && sess.ID == currentID {
			currentExists = true
			currentMatchTime = updatedAt
			if bestMatch == "" || updatedAt > bestMatchTime {
				bestMatch = sess.ID
				bestMatchTime = updatedAt
			}
			continue
		}

		if currentID == "" && startedAt > 0 && updatedAt < startupThreshold && sess.Created < startupThreshold {
			continue
		}

		if currentID != "" && activityAt > 0 && (updatedAt >= activityThreshold || sess.Created >= activityThreshold) {
			if localRotationMatch == "" || updatedAt > localRotationTime {
				localRotationMatch = sess.ID
				localRotationTime = updatedAt
			}
		}

		if bestMatch == "" || updatedAt > bestMatchTime {
			bestMatch = sess.ID
			bestMatchTime = updatedAt
		}
	}

	if currentID != "" && currentExists {
		if localRotationMatch != "" && localRotationTime > currentMatchTime {
			return localRotationMatch
		}
		return currentID
	}

	return bestMatch
}

// queryOpenCodeSession queries the managed OpenCode HTTP server for sessions
// matching our project directory. Unbound instances adopt the most recently
// updated session, while already-bound instances keep their current ID as long
// as it still exists. Instances without a managed port retain a coalesced,
// rate-limited CLI compatibility path.
//
// CLI fallback wall-clock bounds:
//   - 5s context deadline for the subprocess itself.
//   - WaitDelay=500ms so cmd.Output() returns after the context fires even if
//     an opencode grandchild keeps stdout pipes open (Go 1.20+).
//
// 5s is the ceiling for cold opencode CLI on large session stores; on slower
// machines this still usually succeeds, and on genuine hangs the cache and
// lastOpenCodeScanAt schedule a later retry instead of spawning a poll storm.
func (i *Instance) queryOpenCodeSession() string {
	i.mu.RLock()
	port := i.OpenCodePort
	projectPath := i.ProjectPath
	currentID := i.OpenCodeSessionID
	startedAt := i.OpenCodeStartedAt
	i.mu.RUnlock()

	var sessions []openCodeSessionMetadata
	if port > 0 {
		var err error
		sessions, err = i.queryOpenCodeSessionsHTTP(port, projectPath)
		if err != nil {
			sessionLog.Debug("opencode_http_query_failed",
				slog.String("dir", logging.SanitizeValue(projectPath)),
				slog.Int("port", port),
				slog.String("error", err.Error()),
			)
			return ""
		}
	} else {
		sessions = i.queryOpenCodeSessionsCLI(projectPath)
	}

	sessionLog.Debug("opencode_parsed_sessions", slog.Int("count", len(sessions)))

	var activityAt int64
	if currentID != "" {
		lastActivity := i.GetLastActivityTime()
		if !lastActivity.IsZero() && time.Since(lastActivity) <= opencodeRotationActivityWindow {
			activityAt = lastActivity.UnixMilli()
		}
	}

	bestMatch := findBestOpenCodeSession(sessions, projectPath, currentID, startedAt, activityAt)
	sessionLog.Debug(
		"opencode_best_match",
		slog.String("session_id", bestMatch),
		slog.String("current_id", currentID),
	)
	return bestMatch
}

func (i *Instance) queryOpenCodeSessionsHTTP(port int, projectPath string) ([]openCodeSessionMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	endpoint := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		Path:   "/session",
	}
	query := endpoint.Query()
	query.Set("directory", projectPath)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := openCodeSessionHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("opencode session endpoint returned %s", resp.Status)
	}

	var payload []openCodeHTTPSessionMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, openCodeSessionResponseLimit)).Decode(&payload); err != nil {
		return nil, err
	}

	sessions := make([]openCodeSessionMetadata, 0, len(payload))
	for _, session := range payload {
		directory := session.Directory
		if directory == "" {
			directory = session.Location.Directory
		}
		sessions = append(sessions, openCodeSessionMetadata{
			ID:        session.ID,
			Directory: directory,
			Path:      session.Path,
			Created:   session.Time.Created,
			Updated:   session.Time.Updated,
		})
	}
	return sessions, nil
}

func (i *Instance) queryOpenCodeSessionsCLI(projectPath string) []openCodeSessionMetadata {
	cacheKey := normalizePath(projectPath)
	if sessions, ok := cachedOpenCodeCLISessions(cacheKey); ok {
		return sessions
	}

	result, _, _ := openCodeCLIQueryGroup.Do(cacheKey, func() (any, error) {
		if sessions, ok := cachedOpenCodeCLISessions(cacheKey); ok {
			return sessions, nil
		}
		sessions := i.runOpenCodeSessionsCLI(projectPath)
		cacheOpenCodeCLISessions(cacheKey, sessions)
		return sessions, nil
	})
	sessions, ok := result.([]openCodeSessionMetadata)
	if !ok {
		return nil
	}
	return append([]openCodeSessionMetadata(nil), sessions...)
}

func cachedOpenCodeCLISessions(cacheKey string) ([]openCodeSessionMetadata, bool) {
	now := time.Now()
	openCodeCLIQueryCache.Lock()
	defer openCodeCLIQueryCache.Unlock()
	if cached, ok := openCodeCLIQueryCache.entries[cacheKey]; ok && now.Sub(cached.queriedAt) < opencodeRotationScanInterval {
		return append([]openCodeSessionMetadata(nil), cached.sessions...), true
	}
	return nil, false
}

func cacheOpenCodeCLISessions(cacheKey string, sessions []openCodeSessionMetadata) {
	now := time.Now()
	openCodeCLIQueryCache.Lock()
	defer openCodeCLIQueryCache.Unlock()
	for key, cached := range openCodeCLIQueryCache.entries {
		if now.Sub(cached.queriedAt) >= opencodeRotationScanInterval {
			delete(openCodeCLIQueryCache.entries, key)
		}
	}
	openCodeCLIQueryCache.entries[cacheKey] = openCodeCLIQueryCacheEntry{
		queriedAt: now,
		sessions:  append([]openCodeSessionMetadata(nil), sessions...),
	}
}

func (i *Instance) runOpenCodeSessionsCLI(projectPath string) []openCodeSessionMetadata {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run: opencode session list --format json
	cmd := exec.CommandContext(ctx, "opencode", "session", "list", "--format", "json")
	cmd.Dir = projectPath
	cmd.WaitDelay = 500 * time.Millisecond

	sessionLog.Debug("opencode_query_sessions", slog.String("dir", logging.SanitizeValue(projectPath)))

	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			sessionLog.Warn("opencode_query_timeout",
				slog.String("dir", logging.SanitizeValue(projectPath)),
				slog.String("instance_id", i.ID),
			)
		} else {
			sessionLog.Debug("opencode_query_failed", slog.String("error", err.Error()))
		}
		return nil
	}

	sessionLog.Debug("opencode_session_data_size", slog.Int("bytes", len(output)))

	// Parse JSON response
	// Expected format: array of session objects with id, directory, created, updated fields
	var sessions []openCodeSessionMetadata

	if err := json.Unmarshal(output, &sessions); err != nil {
		sessionLog.Debug("opencode_parse_failed", slog.String("error", err.Error()))
		return nil
	}
	return sessions
}

// normalizePath normalizes a file path for comparison
func normalizePath(p string) string {
	// Expand home directory
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = strings.Replace(p, "~", home, 1)
		}
	}

	// Clean the path
	p = filepath.Clean(p)

	// Resolve symlinks if possible
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}

	return p
}

// DetectCodexSession is the public wrapper for async Codex session detection
// Call this for restored sessions that don't have a session ID yet
func (i *Instance) DetectCodexSession() {
	i.detectCodexSessionAsync()
}

// detectCodexSessionAsync detects the Codex session ID after startup
// Codex stores sessions in ~/.codex/sessions/YYYY/MM/DD/*.jsonl
// Session ID is a UUID that can be extracted from the filename
// Since Codex has no "session list" command, we scan the filesystem
func (i *Instance) detectCodexSessionAsync() {
	// Brief wait for Codex to initialize
	time.Sleep(1 * time.Second)

	// Try up to 3 times with short delays
	delays := []time.Duration{0, 1 * time.Second, 2 * time.Second}

	for attempt, delay := range delays {
		if delay > 0 {
			time.Sleep(delay)
		}

		sessionID, _ := i.queryCodexSessionFromProcessFiles()
		if sessionID == "" {
			sessionID = i.queryCodexSession(i.collectOtherCodexSessionIDs(), true)
		}
		if sessionID != "" {
			i.CodexSessionID = sessionID
			i.CodexDetectedAt = time.Now()

			// Store in tmux environment for restart
			if i.tmuxSession != nil {
				if err := i.tmuxSession.SetEnvironment("CODEX_SESSION_ID", sessionID); err != nil {
					sessionLog.Warn("codex_set_env_failed", slog.String("error", err.Error()))
				}
			}

			sessionLog.Debug(
				"codex_session_detected",
				slog.String("session_id", sessionID),
				slog.Int("attempt", attempt+1),
			)
			return
		}

		sessionLog.Debug("codex_session_not_found", slog.Int("attempt", attempt+1), slog.Int("total", len(delays)))
	}

	sessionLog.Warn("codex_detection_failed", slog.Int("attempts", len(delays)))
}

func getCodexHomeDir() string {
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return ExpandPath(codexHome)
	}

	if cfg, err := LoadUserConfig(); err == nil && cfg != nil {
		profile := resolvedProcessProfile()
		if profileDir := cfg.GetProfileCodexConfigDir(profile); profileDir != "" {
			return profileDir
		}
		if cfg.Codex.ConfigDir != "" {
			return ExpandPath(cfg.Codex.ConfigDir)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".codex")
	}
	return filepath.Join(home, ".codex")
}

func isCodexHomeExplicit() bool {
	if strings.TrimSpace(os.Getenv("CODEX_HOME")) != "" {
		return true
	}
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		return false
	}
	profile := resolvedProcessProfile()
	if cfg.GetProfileCodexConfigDir(profile) != "" {
		return true
	}
	return strings.TrimSpace(cfg.Codex.ConfigDir) != ""
}

// runWithTimeout runs op in a goroutine and waits up to timeout for it to
// complete. Returns true if op finished, false if it timed out. The
// abandoned goroutine continues running until op returns naturally; its
// effects on shared state after timeout are not consulted by callers, which
// must check the return value before reading any variables op may have
// written.
//
// Used to backstop FS operations under ~/.codex/sessions which can hang
// indefinitely on a stuck FS layer (kernel D-state during readdir on the
// WSL 9p path was observed on 2026-04-28; one thread held a dentry that
// the FS layer never released, blocking every agent-deck CLI command that
// transitively walked the codex sessions tree).
func runWithTimeout(timeout time.Duration, op func()) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		op()
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// codexWalkDirTimeout caps the recursive walk over $CODEX_HOME/sessions/ in
// queryCodexSession. Healthy walks of a year's sessions complete in roughly
// one second; the bound is generous so a slow disk does not false-negative
// while still preventing indefinite hangs.
const codexWalkDirTimeout = 5 * time.Second

// queryCodexSession scans Codex sessions and returns the best candidate.
// Selection strategy:
//  1. Prefer sessions whose JSONL metadata matches this instance's project path.
//  2. Optionally allow unscoped fallback (no cwd metadata) for initial bootstrap.
func (i *Instance) queryCodexSession(excludeIDs map[string]bool, allowUnscoped bool) string {
	sessionsDir := filepath.Join(i.getCodexHomeDir(), "sessions")
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return ""
	}

	uuidPattern := uuidPatternRE

	var bestScopedID string
	var bestScopedTime time.Time
	var bestUnscopedID string
	var bestUnscopedTime time.Time

	normalizedProjectPath := normalizePath(i.ProjectPath)

	var walkErr error
	if !runWithTimeout(codexWalkDirTimeout, func() {
		walkErr = filepath.WalkDir(sessionsDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // Skip errors
			}

			if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}

			sessionID := uuidPattern.FindString(d.Name())
			if sessionID == "" {
				return nil
			}
			if excludeIDs != nil && excludeIDs[sessionID] {
				return nil
			}

			// Subagent-thread gate (incident 2026-07-15): never let the
			// bootstrap disk scan adopt a subagent rollout as the session's
			// main thread — codex refuses user turns on it. Skipping it here
			// (rather than after selection) lets the walk fall through to the
			// best user-sourced match instead of returning nothing when the
			// most-recent match happens to be a subagent. See
			// codex_subagent_gate.go.
			if i.shouldRejectCodexSubagentRebind(sessionID) {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return nil
			}

			// Only consider sessions created after we started this instance.
			if i.CodexStartedAt > 0 {
				startTime := time.UnixMilli(i.CodexStartedAt)
				if info.ModTime().Before(startTime) {
					return nil
				}
			}

			matchesProject, hasProjectMetadata := codexSessionMatchesProject(path, normalizedProjectPath)
			if matchesProject {
				if bestScopedID == "" || info.ModTime().After(bestScopedTime) {
					bestScopedID = sessionID
					bestScopedTime = info.ModTime()
				}
				return nil
			}

			// Use unscoped records only when bootstrapping and metadata is unavailable.
			if allowUnscoped && !hasProjectMetadata {
				if bestUnscopedID == "" || info.ModTime().After(bestUnscopedTime) {
					bestUnscopedID = sessionID
					bestUnscopedTime = info.ModTime()
				}
			}

			return nil
		})
	}) {
		// Walk did not complete in time. The most likely cause is a stuck FS
		// layer (e.g., WSL kernel D-state). Return without consulting the
		// best* variables since they may be partially populated; the caller
		// retries with backoff so a transient stall self-heals.
		sessionLog.Warn("codex_walkdir_timeout",
			slog.String("instance_id", i.ID),
			slog.String("sessions_dir", sessionsDir),
			slog.Duration("timeout", codexWalkDirTimeout))
		return ""
	}
	if walkErr != nil {
		sessionLog.Debug("codex_scan_error", slog.String("error", walkErr.Error()))
	}

	if bestScopedID != "" {
		return bestScopedID
	}
	if allowUnscoped {
		return bestUnscopedID
	}
	return ""
}

// codexSessionMatchesProject checks whether a Codex session file belongs to the
// current project by inspecting JSONL metadata fields (cwd/workdir/path).
// Returns:
//   - match=true if project path matches
//   - known=true if any project metadata field was found
func codexSessionMatchesProject(sessionFilePath, normalizedProjectPath string) (match bool, known bool) {
	if normalizedProjectPath == "" {
		return false, false
	}

	file, err := os.Open(sessionFilePath)
	if err != nil {
		return false, false
	}
	defer file.Close()

	const maxLines = 256

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	lineCount := 0
	foundMetadata := false

	for scanner.Scan() {
		lineCount++
		cwd := extractCodexCWDFromJSONLine(scanner.Bytes())
		if cwd != "" {
			foundMetadata = true
			if normalizePath(cwd) == normalizedProjectPath {
				return true, true
			}
		}
		if lineCount >= maxLines {
			break
		}
	}

	return false, foundMetadata
}

// extractCodexCWDFromJSONLine extracts cwd-like project fields from one JSONL record.
func extractCodexCWDFromJSONLine(line []byte) string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return ""
	}

	keys := []string{"cwd", "workdir", "working_dir", "directory", "path"}
	for _, key := range keys {
		if val := decodeJSONStringField(raw, key); val != "" {
			return val
		}
	}

	if payloadRaw, ok := raw["payload"]; ok && len(payloadRaw) > 0 {
		var payloadObj map[string]json.RawMessage
		if err := json.Unmarshal(payloadRaw, &payloadObj); err == nil {
			for _, key := range keys {
				if val := decodeJSONStringField(payloadObj, key); val != "" {
					return val
				}
			}
		}
	}
	if contextRaw, ok := raw["context"]; ok && len(contextRaw) > 0 {
		var contextObj map[string]json.RawMessage
		if err := json.Unmarshal(contextRaw, &contextObj); err == nil {
			for _, key := range keys {
				if val := decodeJSONStringField(contextObj, key); val != "" {
					return val
				}
			}
		}
	}

	return ""
}

func decodeJSONStringField(raw map[string]json.RawMessage, key string) string {
	value, ok := raw[key]
	if !ok || len(value) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// collectOtherCodexSessionIDs enumerates other managed tmux sessions and returns
// the CODEX_SESSION_ID values they currently own.
func (i *Instance) collectOtherCodexSessionIDs() map[string]bool {
	exclude := make(map[string]bool)

	tmuxSessions, err := tmux.ListAgentDeckSessions()
	if err != nil {
		return exclude
	}

	myTmuxName := ""
	if i.tmuxSession != nil {
		myTmuxName = i.tmuxSession.Name
	}

	for _, sessName := range tmuxSessions {
		if sessName == myTmuxName {
			continue
		}
		other := &tmux.Session{Name: sessName}
		if id, err := other.GetEnvironment("CODEX_SESSION_ID"); err == nil && id != "" {
			exclude[id] = true
		}
	}

	return exclude
}

// shouldScanCodexSession returns whether we should run an expensive filesystem
// scan for Codex session rotation right now.
func (i *Instance) shouldScanCodexSession(allowUnscoped bool) bool {
	interval := codexRotationScanInterval
	if allowUnscoped {
		interval = codexBootstrapScanInterval
	}

	if !i.lastCodexScanAt.IsZero() && time.Since(i.lastCodexScanAt) < interval {
		return false
	}

	i.lastCodexScanAt = time.Now()
	return true
}

// shouldRunCodexProcessProbe returns whether we should run Codex process/file
// probing right now.
func (i *Instance) shouldRunCodexProcessProbe(force bool) bool {
	if force {
		i.lastCodexProbeAt = time.Now()
		return true
	}

	// Fast cadence only while bootstrapping (no session ID yet) — that phase is
	// bounded by discovery succeeding within seconds. Once an ID is known the
	// probe is just a rotation safety net behind the notify hook and tmux env,
	// so it backs off to the rotation cadence (issue #1552).
	interval := codexBootstrapScanInterval
	if i.CodexSessionID != "" {
		interval = codexRotationScanInterval
	}

	if !i.lastCodexProbeAt.IsZero() && time.Since(i.lastCodexProbeAt) < interval {
		return false
	}

	i.lastCodexProbeAt = time.Now()
	return true
}

// collectTmuxPaneProcessTreePIDs returns pane PID + descendant PIDs for this instance.
func (i *Instance) collectTmuxPaneProcessTreePIDs() []int {
	if i.tmuxSession == nil || !i.tmuxSession.Exists() {
		return nil
	}

	target := i.tmuxSession.Name + ":"
	// Target the same tmux server the session was created on (issue #687).
	// A session on an isolated agent-deck socket would return no panes from
	// the default server and we would mistakenly treat it as empty.
	// Bounded — see tmux.OutputBounded. Exec(...).Output() has no deadline, and
	// a tmux client that has exhausted its fd table never exits, so this probe
	// would hang the caller forever.
	out, err := tmux.OutputBounded(i.TmuxSocketName, "list-panes", "-t", target, "-F", "#{pane_pid}")
	if err != nil {
		return nil
	}

	pidStr := strings.TrimSpace(string(out))
	if idx := strings.IndexByte(pidStr, '\n'); idx >= 0 {
		pidStr = pidStr[:idx]
	}
	panePID, err := strconv.Atoi(pidStr)
	if err != nil || panePID <= 0 {
		return nil
	}

	// Single snapshot of the process table is substantially cheaper than
	// spawning pgrep once per node in deep process trees.
	procTable, err := exec.Command("ps", "-eo", "pid=,ppid=").Output()
	if err == nil {
		if allPIDs := collectProcessTreePIDsFromTable(panePID, procTable); len(allPIDs) > 0 {
			return allPIDs
		}
	}

	// Fallback path for environments where ps output is unavailable/unexpected.
	return collectProcessTreePIDsViaPgrep(panePID)
}

func collectProcessTreePIDsFromTable(rootPID int, procTable []byte) []int {
	childrenByParent := parsePSParentChildMap(procTable)
	if len(childrenByParent) == 0 {
		return []int{rootPID}
	}

	var allPIDs []int
	seen := map[int]bool{rootPID: true}
	queue := []int{rootPID}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		allPIDs = append(allPIDs, parent)

		for _, childPID := range childrenByParent[parent] {
			if childPID <= 0 || seen[childPID] {
				continue
			}
			seen[childPID] = true
			queue = append(queue, childPID)
		}
	}
	return allPIDs
}

func parsePSParentChildMap(procTable []byte) map[int][]int {
	childrenByParent := make(map[int][]int)
	scanner := bufio.NewScanner(bytes.NewReader(procTable))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// tolerant of trailing columns so the same snapshot can carry comm=
		// for callers that also need to identify the pane leader
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid <= 0 {
			continue
		}
		childrenByParent[ppid] = append(childrenByParent[ppid], pid)
	}
	return childrenByParent
}

// parsePSCommandNames reads `pid=,ppid=,comm=` output into pid -> command
// name. Comm is the last column and may contain spaces, so everything from
// the third field on is joined. macOS reports comm as an absolute path while
// Linux reports the bare name, so the result is reduced to the base name to
// give callers one shape to compare against.
func parsePSCommandNames(procTable []byte) map[int]string {
	commByPID := make(map[int]string)
	scanner := bufio.NewScanner(bytes.NewReader(procTable))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		commByPID[pid] = filepath.Base(strings.Join(fields[2:], " "))
	}
	return commByPID
}

func collectProcessTreePIDsViaPgrep(rootPID int) []int {
	var allPIDs []int
	seen := map[int]bool{rootPID: true}
	queue := []int{rootPID}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		allPIDs = append(allPIDs, parent)

		// #nosec G204 -- "pgrep" is a fixed binary name and the only argument is
		// strconv.Itoa(int), never reachable from external input.
		childrenRaw, err := exec.Command("pgrep", "-P", strconv.Itoa(parent)).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(childrenRaw)), "\n") {
			childPID, convErr := strconv.Atoi(strings.TrimSpace(line))
			if convErr != nil || childPID <= 0 || seen[childPID] {
				continue
			}
			seen[childPID] = true
			queue = append(queue, childPID)
		}
	}
	return allPIDs
}

func isLikelyCodexProcessPID(pid int) bool {
	// #nosec G204 -- "ps" is a fixed binary; only arg is strconv.Itoa(int).
	argsOut, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(string(argsOut))), "codex")
}

func extractCodexSessionIDFromPath(path string) string {
	normalized := strings.TrimSpace(path)
	normalized = strings.TrimSuffix(normalized, " (deleted)")
	matches := codexSessionIDPathPatternRE.FindStringSubmatch(normalized)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

func extractCodexSessionIDFromLsofOutput(output []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		if sessionID := extractCodexSessionIDFromPath(scanner.Text()); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

func extractCodexSessionIDFromProcFD(pid int) string {
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return ""
	}

	for _, entry := range entries {
		targetPath := filepath.Join(fdDir, entry.Name())
		target, err := os.Readlink(targetPath)
		if err != nil {
			continue
		}
		if sessionID := extractCodexSessionIDFromPath(target); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

func (i *Instance) queryCodexSessionFromHostProcFD() string {
	for _, pid := range i.collectTmuxPaneProcessTreePIDs() {
		if !isLikelyCodexProcessPID(pid) {
			continue
		}
		if sessionID := extractCodexSessionIDFromProcFD(pid); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

func (i *Instance) queryCodexSessionFromDockerProcFD() (string, string) {
	if strings.TrimSpace(i.SandboxContainer) == "" {
		return "", ""
	}

	script := fmt.Sprintf(
		`command -v readlink >/dev/null 2>&1 || {
	echo %q
	exit 0
}
for f in /proc/[0-9]*/fd/*; do
	t=$(readlink "$f" 2>/dev/null || true)
	case "$t" in
		*/sessions/*rollout-*.jsonl*)
			printf '%%s\n' "$t"
			;;
	esac
done`,
		codexProbeMissingSentinel,
	)
	// #nosec G204 -- "docker exec" with internal SandboxContainer name and a
	// hardcoded shell probe script (codexProbeMissingSentinel is a compile-time
	// constant); no external input flows here.
	out, err := exec.Command("docker", "exec", i.SandboxContainer, "sh", "-lc", script).Output()
	if err != nil {
		return "", ""
	}
	if bytes.Contains(out, []byte(codexProbeMissingSentinel)) {
		return "", "readlink"
	}
	if sessionID := extractCodexSessionIDFromLsofOutput(out); sessionID != "" {
		return sessionID, ""
	}
	return "", ""
}

func (i *Instance) queryCodexSessionFromHostLsof() (string, string) {
	if _, err := exec.LookPath("lsof"); err != nil {
		return "", "lsof"
	}

	for _, pid := range i.collectTmuxPaneProcessTreePIDs() {
		if !isLikelyCodexProcessPID(pid) {
			continue
		}

		// -n -P disables reverse-DNS host and port-name resolution so a resolver
		// that drops PTR queries cannot stall the probe (issue #1581); the
		// context timeout bounds the call even if lsof hangs for any other reason.
		ctx, cancel := context.WithTimeout(context.Background(), codexLsofProbeTimeout)
		// #nosec G204 -- "lsof" is a fixed binary; only dynamic arg is strconv.Itoa(int).
		out, err := exec.CommandContext(ctx, "lsof", "-n", "-P", "-p", strconv.Itoa(pid)).Output()
		cancel()
		if err != nil {
			var execErr *exec.Error
			if errors.As(err, &execErr) && execErr.Err == exec.ErrNotFound {
				return "", "lsof"
			}
			sessionLog.Debug("codex_lsof_probe_failed", slog.Int("pid", pid), slog.Any("error", err))
			continue
		}

		if sessionID := extractCodexSessionIDFromLsofOutput(out); sessionID != "" {
			return sessionID, ""
		}
	}

	return "", ""
}

// queryCodexSessionFromProcessFiles inspects live Codex processes and returns
// the active session UUID inferred from open rollout JSONL files.
// The second return value is the missing dependency name (if any).
func (i *Instance) queryCodexSessionFromProcessFiles() (string, string) {
	// Sandboxed sessions run Codex inside Docker; probe container /proc.
	if i.IsSandboxed() {
		return i.queryCodexSessionFromDockerProcFD()
	}

	// Linux/WSL: pure in-process /proc scanning (no lsof dependency).
	if runtime.GOOS == "linux" {
		if sessionID := i.queryCodexSessionFromHostProcFD(); sessionID != "" {
			return sessionID, ""
		}
		return "", ""
	}

	// Non-Linux (e.g. macOS): fallback to lsof compatibility path.
	return i.queryCodexSessionFromHostLsof()
}

// ConsumeCodexRestartWarning returns and clears any pending Codex restart warning.
func (i *Instance) ConsumeCodexRestartWarning() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	warning := strings.TrimSpace(i.pendingCodexRestartWarning)
	i.pendingCodexRestartWarning = ""
	return warning
}

func codexProbeMissingWarning(missingDep string) string {
	missingDep = strings.TrimSpace(missingDep)
	if missingDep == "" {
		return ""
	}
	return fmt.Sprintf("Codex session detection fallback: %s is not available", missingDep)
}

// UpdateCodexSession updates the Codex session ID.
// Primary source: tmux environment.
// Fallback: project-aware filesystem scan.
func (i *Instance) UpdateCodexSession(excludeIDs map[string]bool) {
	i.updateCodexSession(excludeIDs, false)
}

// updateCodexSession refreshes Codex session ID from env/process-files/disk.
// Returns missing dependency name when probe prerequisites are unavailable.
func (i *Instance) updateCodexSession(excludeIDs map[string]bool, forceProbe bool) string {
	if !IsCodexCompatible(i.Tool) {
		return ""
	}

	envSessionID := ""

	// 1. Try to read from tmux environment first (authoritative if set)
	if i.tmuxSession != nil {
		if sessionID, err := i.tmuxSession.GetEnvironment("CODEX_SESSION_ID"); err == nil && sessionID != "" {
			envSessionID = sessionID
			if i.CodexSessionID != sessionID {
				i.CodexSessionID = sessionID
			}
			i.CodexDetectedAt = time.Now()
		}
	}

	// 2. Prefer live-process file detection (Linux /proc, macOS lsof fallback).
	missingProbeDep := ""
	if i.shouldRunCodexProcessProbe(forceProbe) {
		if sessionID, missingDep := i.queryCodexSessionFromProcessFiles(); sessionID != "" {
			// Subagent-thread gate (incident 2026-07-15): a codex TUI holds the
			// rollouts of the subagents it spawned open alongside its main
			// thread, so the FD probe can surface a subagent id. Binding it here
			// is the same poisoning the hook gate prevents, reached by the other
			// rotation path — and once bound, a restart resumes a thread codex
			// refuses user turns on. Reject it and keep the current binding; the
			// probe will pick the main thread on a later cycle. See
			// codex_subagent_gate.go.
			if i.shouldRejectCodexSubagentRebind(sessionID) {
				_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
					InstanceID: i.ID, Tool: i.Tool, Action: "reject",
					Source: "process_probe", OldID: i.CodexSessionID, Candidate: sessionID,
					Reason: "candidate_is_subagent_thread",
				})
				sessionLog.Debug("codex_session_probe_rejected_subagent",
					slog.String("old_id", i.CodexSessionID),
					slog.String("candidate", sessionID))
			} else {
				changed := sessionID != i.CodexSessionID
				if changed {
					sessionLog.Debug(
						"codex_session_update_from_probe",
						slog.String("old_id", i.CodexSessionID),
						slog.String("new_id", sessionID),
					)
				}
				i.CodexSessionID = sessionID
				i.CodexDetectedAt = time.Now()
				if i.tmuxSession != nil && i.tmuxSession.Exists() && (changed || envSessionID == "") {
					_ = i.tmuxSession.SetEnvironment("CODEX_SESSION_ID", i.CodexSessionID)
				}
				return ""
			}
		} else if missingDep != "" {
			missingProbeDep = missingDep
		}
	}

	// 3. Use disk scan only as a bootstrap fallback. Once a session ID is
	// known, rotation must come from authoritative live evidence above (tmux
	// env, hook payload, or the Codex process' open rollout file). Polling the
	// full historical $CODEX_HOME/sessions tree for every active Codex session
	// burns CPU on large histories and has no stronger ownership signal than the
	// current binding.
	if i.CodexSessionID != "" {
		return missingProbeDep
	}

	// Only allow unscoped fallback when we don't have a known session ID yet.
	allowUnscoped := envSessionID == "" && i.CodexSessionID == "" && i.CodexStartedAt > 0
	if !i.shouldScanCodexSession(allowUnscoped) {
		return missingProbeDep
	}

	// When we already have a session ID and the process probe didn't find a
	// running process, add our current ID to the exclude set so the disk scan
	// won't reassign it to another instance that shares the same project path.
	// The disk scan should only discover *new* sessions (e.g. after /new rotation),
	// not re-discover the same ID we already own.
	if i.CodexSessionID != "" && excludeIDs != nil {
		excludeIDs[i.CodexSessionID] = true
	}

	if sessionID := i.queryCodexSession(excludeIDs, allowUnscoped); sessionID != "" {
		// queryCodexSession already filters subagent rollouts out of candidacy
		// (incident 2026-07-15), so sessionID here is always a user thread.
		changed := sessionID != i.CodexSessionID
		if sessionID != i.CodexSessionID {
			sessionLog.Debug(
				"codex_session_update",
				slog.String("old_id", i.CodexSessionID),
				slog.String("new_id", sessionID),
			)
		}
		i.CodexSessionID = sessionID
		i.CodexDetectedAt = time.Now()

		// Sync back to tmux environment for future restarts
		// Skip redundant writes when env already matches: each write is a tmux subprocess.
		if i.tmuxSession != nil && i.tmuxSession.Exists() && (changed || envSessionID == "") {
			_ = i.tmuxSession.SetEnvironment("CODEX_SESSION_ID", i.CodexSessionID)
		}
	}
	return missingProbeDep
}

// buildGenericCommand builds commands for custom tools defined in [tools.*]
// If the tool has session resume config, builds capture-resume command similar to Claude/Gemini
// Otherwise returns the base command as-is
// Also sources .env files from [shell].env_files and [tools.X].env_file
//
// Config fields used:
//   - resume_flag: CLI flag to resume (e.g., "--resume")
//   - session_id_env: tmux env var name (e.g., "VIBE_SESSION_ID")
//   - session_id_json_path: jq path to extract ID (e.g., ".session_id")
//   - output_format_flag: flag to get JSON output (e.g., "--output-format json")
//   - dangerous_flag: flag to skip confirmations (e.g., "--auto-approve")
//   - dangerous_mode: whether to enable dangerous flag by default
//   - env_file: .env file to source for this tool
func (i *Instance) buildGenericCommand(baseCommand string) string {
	envPrefix := i.buildEnvSourceCommand()

	toolDef := GetToolDef(i.Tool)
	if toolDef == nil {
		return envPrefix + baseCommand // No custom config, return with env prefix
	}

	// Resume needs resume_flag; the conversation id may come from live
	// session_id_env and/or persisted generic_session_id (reboot-safe).
	if toolDef.ResumeFlag == "" {
		// No session resume support, just add dangerous flag if configured
		if toolDef.DangerousMode && toolDef.DangerousFlag != "" {
			return envPrefix + fmt.Sprintf("%s %s", baseCommand, toolDef.DangerousFlag)
		}
		return envPrefix + baseCommand
	}

	// Prefer live tmux env, else persisted generic_session_id (reboot-safe).
	existingSessionID := i.GetGenericSessionID()

	// Build dangerous flag if enabled
	dangerousFlag := ""
	if toolDef.DangerousMode && toolDef.DangerousFlag != "" {
		dangerousFlag = toolDef.DangerousFlag
	}

	// If we have an existing session ID, just resume.
	// The session ID env var is propagated via host-side SetEnvironment after tmux start.
	if existingSessionID != "" {
		return envPrefix + formatGenericResumeCommand(
			baseCommand, toolDef.ResumeFlag, existingSessionID, dangerousFlag)
	}

	// No existing session ID - need to capture it on first run
	// This requires output_format_flag and session_id_json_path
	if toolDef.OutputFormatFlag == "" || toolDef.SessionIDJsonPath == "" {
		// Can't capture session ID, just start normally
		if dangerousFlag != "" {
			return envPrefix + baseCommand + " " + dangerousFlag
		}
		return envPrefix + baseCommand
	}

	// Build capture-resume command similar to Claude/Gemini
	// Pattern:
	// 1. Run tool with minimal prompt to get session ID
	// 2. Extract ID using jq
	// 3. Resume that session
	// Note: session ID env var is set via host-side SyncSessionIDsToTmux() once detected.
	// Fallback: If capture fails, start tool fresh.
	// Captured $session_id is shell-quoted via "$session_id"; resume_flag that
	// ends with "=" glues without a space (equals-form CLIs).
	resumeInvoke := formatGenericResumeShellVar(baseCommand, toolDef.ResumeFlag, "session_id", dangerousFlag)
	freshInvoke := baseCommand
	if dangerousFlag != "" {
		freshInvoke += " " + dangerousFlag
	}
	// Publish the captured id into the tmux environment before using it.
	//
	// Without this the id exists only as a shell variable inside the pane: it
	// resumes this one launch and is gone, so a tool that reports its id in
	// JSON but exports no env var of its own got no durability at all — the
	// exact gap this feature is meant to close. SyncSessionIDsFromTmux and
	// GetGenericSessionID read the variable back and write it to tool_data,
	// so a reboot can still find the conversation.
	//
	// Failure is swallowed: publishing is bookkeeping, and a tmux hiccup must
	// not stop the tool from starting. The id reaches the variable via the
	// shell, never via string interpolation, so nothing here is injectable.
	//
	// Only for a LOCAL session. prepareCommand hands this whole fragment to
	// wrapForSSH for an --ssh session, so the tmux call would run on the remote
	// host, find no tmux server there, and be swallowed by the `|| true` -- a
	// silent no-op dressed as durability. The controller's tmux is the one that
	// has to hold the value, and nothing inside the remote shell can reach it.
	publish := ""
	if !i.IsSSH() {
		publish = fmt.Sprintf(`tmux set-environment %s "$session_id" >/dev/null 2>&1 || true; `,
			genericCapturedSessionIDEnv)
	}
	return envPrefix + fmt.Sprintf(
		`session_id=$(%s %s "." 2>/dev/null | jq -r '%s' 2>/dev/null) || session_id=""; `+
			`if [ -n "$session_id" ] && [ "$session_id" != "null" ]; then `+
			`%s%s; `+
			`else %s; fi`,
		baseCommand, toolDef.OutputFormatFlag, toolDef.SessionIDJsonPath,
		publish, resumeInvoke, freshInvoke)
}

// genericCapturedSessionIDEnv is the tmux environment variable agent-deck owns
// for a custom tool's conversation id. It is the fallback for tools that expose
// an id in their JSON output but declare no session_id_env of their own; a tool
// that declares one keeps precedence (see genericSessionEnvNames).
const genericCapturedSessionIDEnv = "AGENTDECK_TOOL_SESSION_ID"

// formatGenericResumeCommand builds:
//
//	<base> <resume_flag> <quoted-id>[ <dangerous_flag>]
//
// When resume_flag ends with "=", the id is glued without a space
// (`tool --session='id'`). The conversation id is always shellescape-quoted
// because the resulting string is executed under bash -lc (tmux respawn /
// start); operator-set or tool-exported ids may contain shell metacharacters.
// Mirrors hermes/opencode/codex session-id quoting.
func formatGenericResumeCommand(baseCommand, resumeFlag, sessionID, dangerousFlag string) string {
	quotedID := shellescape.Quote(sessionID)
	var cmd string
	if strings.HasSuffix(resumeFlag, "=") {
		cmd = fmt.Sprintf("%s %s%s", baseCommand, resumeFlag, quotedID)
	} else {
		cmd = fmt.Sprintf("%s %s %s", baseCommand, resumeFlag, quotedID)
	}
	if dangerousFlag != "" {
		cmd += " " + dangerousFlag
	}
	return cmd
}

// formatGenericResumeShellVar is the capture-path variant that resumes using a
// shell variable (already double-quoted by the caller template) rather than a
// Go-side literal id.
func formatGenericResumeShellVar(baseCommand, resumeFlag, varName, dangerousFlag string) string {
	ref := "\"$" + varName + "\""
	var cmd string
	if strings.HasSuffix(resumeFlag, "=") {
		cmd = fmt.Sprintf("%s %s%s", baseCommand, resumeFlag, ref)
	} else {
		cmd = fmt.Sprintf("%s %s %s", baseCommand, resumeFlag, ref)
	}
	if dangerousFlag != "" {
		cmd += " " + dangerousFlag
	}
	return cmd
}

// buildShellPassthroughCommand builds the launch command for a Tool=="shell"
// instance. Any instance that isn't SubcommandPassthrough — which is every
// Tool=="shell" instance except the ones resolveSessionCommand itself just
// routed here — is returned byte-identical to the pre-#1821 behavior.
//
// #1800/#1821: resolveSessionCommand (cmd/agent-deck/cli_utils.go) routes
// "<tool> <subcommand> ..." invocations here — Tool="shell", Command=raw,
// SubcommandPassthrough=true — specifically to skip agent-deck's own
// --session-id/permission-flag injection (those flags are never valid after
// a subcommand). The spawned binary in that case is still claude or codex,
// though, so this restores the same AGENTDECK_*/CLAUDE_CONFIG_DIR/
// CODEX_HOME/TELEGRAM-strip treatment their own builders apply on an
// equivalent passthrough (buildClaudeCommandWithMessage's custom-command
// branch, buildCodexCommand's `trimmed != "codex"` branch) — without
// resurrecting any of the claude/codex-specific status-detection or hook
// dispatch that motivated routing through Tool="shell" in the first place
// (those stay gated on IsClaudeCompatible/IsCodexCompatible(i.Tool)
// elsewhere, which is false here).
//
// The gate is on SubcommandPassthrough (explicit provenance recorded once
// at creation), not on re-running MatchTool against Command at every spawn.
// MatchTool's builtin claude/codex classification is a whole-line substring
// match (detectSubstrings, toolregistry.go Match()), so classifying by
// re-matching an arbitrary Tool=="shell" command at spawn time would also
// fire for a pre-existing/TUI-created shell session whose command merely
// CONTAINS "claude"/"codex" anywhere in the line — e.g. a custom command
// `claude --resume <id>` persisted before this field existed, or an
// everyday `tail -f ~/.claude/logs/x` / `cd /x && claude`. Routing those
// through account/binary substitution on every future restart could
// silently start them under a different account than the one they were
// created under (Claude review, PR #1821 HIGH #1), and — for the compound
// `cd /x && claude` shape — inject inline env assignments ahead of a `cd`
// that a POSIX shell would simply ignore (Claude review, PR #1821 LOW #4).
// SubcommandPassthrough sidesteps both: it is true only when
// resolveSessionCommand already checked that the FIRST token of Command is
// a real claude/codex subcommand invocation.
func (i *Instance) buildShellPassthroughCommand(baseCommand string) string {
	if !i.SubcommandPassthrough {
		return baseCommand
	}

	// Classify off the FIRST TOKEN only, not MatchTool(baseCommand) against
	// the whole line — see the function doc above (Claude review, PR #1821
	// MEDIUM #3). SubcommandPassthrough guarantees fields[0] is the literal
	// claude/codex tool name or an explicit path containing it (resolveSessionCommand
	// validated that before setting the field), so this is strictly narrower
	// than the whole-line match, never broader.
	fields := strings.Fields(baseCommand)
	if len(fields) == 0 {
		return baseCommand
	}
	matched := MatchTool(fields[0])
	if matched == "shell" {
		// Defensive: should be unreachable given SubcommandPassthrough's
		// invariant, but never treat an unrecognized leading token as
		// claude/codex-shaped.
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	switch matched {
	case "claude":
		instanceIDPrefix := fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s AGENTDECK_PROFILE=%s ",
			i.ID, shellescape.Quote(sessionProfileEnvValue()))
		// claudeCmd/hasCustomClaudeCommand mirrors buildClaudeCommandWithMessage's
		// own gate exactly (instance.go, "the alias handles it"): when a
		// configured [claude].command (conductor/group/global, e.g. "cdw") is
		// a custom alias rather than the literal binary, CLAUDE_CONFIG_DIR is
		// skipped because the alias manages its own config dir selection.
		// This shell-passthrough branch calls GetClaudeCommandForInstance(i)
		// again below for substituteResolvedBinary, so the custom-command
		// information was already available but previously unused for this
		// gate — forcing CLAUDE_CONFIG_DIR onto an alias broke parity with
		// the direct spawn path for any session combining a configured
		// account-switcher command with an explicit config_dir (CodeRabbit
		// review, PR #1821).
		claudeCmd := GetClaudeCommandForInstance(i)
		hasCustomClaudeCommand := claudeCmd != "claude"
		configDirPrefix := ""
		if !hasCustomClaudeCommand && IsClaudeConfigDirExplicitForInstance(i) {
			configDir := i.applyWorkerScratchOverride(GetClaudeConfigDirForInstance(i))
			// Shell-quote: configDir is a filesystem path and may contain
			// spaces (e.g. a macOS $HOME with a space in the username).
			configDirPrefix = fmt.Sprintf("CLAUDE_CONFIG_DIR=%s ", shellescape.Quote(configDir))
		}
		execEnvPrefix := ""
		if flags := telegramExecEnvStripFlags(i); flags != "" {
			execEnvPrefix = "env " + flags + " "
		}
		// Codex bot review (PR #1821): the normal claude spawn path
		// (buildClaudeCommandWithMessage) resolves the configured
		// conductor/group/global claude command (e.g. an account-switcher
		// alias like "cdw"/"cdp") via GetClaudeCommandForInstance and honors
		// it even for a literal "claude" the user typed — losing that here
		// would silently run the wrong binary/account for a subcommand-shaped
		// --cmd. substituteResolvedBinary swaps only the leading word, and
		// ONLY when that leading word is the bare literal "claude" — exactly
		// mirroring buildClaudeCommandWithMessage's own `if baseCommand ==
		// "claude"` gate above, which likewise never touches an explicit
		// non-canonical path (its "custom commands" branch below returns
		// baseCommand untouched). MatchTool detects "claude"-shaped via a
		// substring match, so `matched == "claude"` alone is not sufficient
		// to gate substitution: it also fires for an explicitly configured
		// non-canonical executable (e.g. "/opt/canary/claude",
		// "codex-nightly") that merely contains the tool name. On this
		// maintainer's machines the configured account/group command is
		// often itself an account-selection wrapper, so overwriting an
		// operator-chosen path with it can silently start the subcommand
		// under the wrong account (Codex review, PR #1821 P1). Detection
		// here only classifies ("this is claude-shaped, route accordingly");
		// it must never rewrite an operator's explicit path out from under
		// them — leaving the subcommand + its args (the whole reason this
		// path exists) untouched. It also promises the same
		// AGENTDECK_RESOLVED_CONFIG_DIR/GROUP/SOURCE hint vars the normal
		// path exports for statusline/hook/fleet-placement consumers — kept
		// in this function's inline `VAR=val ` style (not
		// buildResolvedAccountHintExports' `export VAR=val;` style) because
		// envPrefix ends in `&& `, and `;` has lower precedence than `&&` in
		// bash: interleaving `export ...;` here would let this vars-and-command
		// tail run even when an earlier env-source step in envPrefix failed.
		resolvedConfigDir, resolvedSource := GetClaudeConfigDirSourceForInstance(i)
		resolvedHintPrefix := fmt.Sprintf(
			"AGENTDECK_RESOLVED_CONFIG_DIR=%s AGENTDECK_RESOLVED_GROUP=%s AGENTDECK_RESOLVED_SOURCE=%s ",
			shellescape.Quote(resolvedConfigDir), shellescape.Quote(i.GroupPath), shellescape.Quote(resolvedSource))
		resolvedCommand := substituteResolvedBinary(baseCommand, "claude", claudeCmd)
		return envPrefix + instanceIDPrefix + configDirPrefix + resolvedHintPrefix + execEnvPrefix + resolvedCommand
	case "codex":
		// AGENTDECK_TOOL uses `matched` ("codex"), not i.Tool (which is
		// "shell" for this passthrough instance) — buildCodexCommand's
		// equivalent injection uses the real codex identity so hook
		// subprocesses that key off AGENTDECK_TOOL=="codex" still find it.
		// AGENTDECK_TITLE is shell-quoted (not Go-%q-quoted): i.Title is
		// user-editable and %q does not stop $(...) / backtick command
		// substitution inside the double quotes it produces — a title like
		// `x$(touch /tmp/pwned)` would execute at every spawn otherwise.
		agentdeckEnvPrefix := fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s AGENTDECK_TITLE=%s AGENTDECK_TOOL=%s AGENTDECK_PROFILE=%s ",
			i.ID, shellescape.Quote(i.Title), matched, shellescape.Quote(sessionProfileEnvValue()))
		// CODEX_HOME selects which account's Codex config/credentials the
		// spawned process reads — injecting it ahead of an explicitly
		// configured non-canonical binary (e.g. "codex-nightly", itself
		// potentially an account-selection wrapper) risks the process
		// starting under the wrong account, same class of bug as
		// substituteResolvedBinary below (Codex review, PR #1821 follow-up).
		// buildCodexCommand's own custom-command early-return already
		// skips this injection entirely for an explicit command; mirror
		// that here by only injecting when the user typed the bare
		// literal "codex".
		codexHomePrefix := ""
		if isLiteralToolInvocation(baseCommand, "codex") && isCodexHomeExplicit() {
			if codexHome := strings.TrimSpace(getCodexHomeDir()); codexHome != "" {
				codexHomePrefix = "CODEX_HOME=" + shellescape.Quote(codexHome) + " "
			}
		}
		// Same rationale as the claude case above: honor [codex].command
		// (e.g. a wrapper/account-switcher) instead of always running the
		// literal binary name the user typed after "codex" — but ONLY when
		// the user typed the bare literal "codex"; an explicit non-canonical
		// path (e.g. "codex-nightly") is never rewritten. See the extended
		// comment on the "claude" case above.
		resolvedCommand := substituteResolvedBinary(baseCommand, "codex", GetCodexCommand())
		return envPrefix + agentdeckEnvPrefix + codexHomePrefix + resolvedCommand
	default:
		return envPrefix + baseCommand
	}
}

// substituteResolvedBinary replaces the leading whitespace-delimited word of
// baseCommand with resolvedBinary (the configured conductor/group/global
// override for the tool, e.g. an account-switcher alias) — but ONLY when
// that leading word is exactly literalName (the bare tool name, "claude" or
// "codex"), leaving the rest of baseCommand — the subcommand and its args —
// untouched.
//
// literalName gates the substitution deliberately: buildShellPassthroughCommand's
// caller classifies baseCommand via MatchTool, which detects "claude"/"codex"
// by a *substring* match — it also fires for an explicitly configured
// non-canonical executable that merely contains the tool name, e.g.
// "/opt/canary/claude" or "codex-nightly". If the caller chose that path on
// purpose (often itself an account-selection wrapper on this maintainer's
// machines), substituting resolvedBinary in its place would silently run a
// *different* binary/account than the one explicitly configured (Codex
// review, PR #1821 P1). Detection classifies; it must never rewrite an
// operator's explicit path. Substitution is therefore restricted to the one
// case where there is no explicit path to protect: the user typed the bare
// literal tool name, exactly mirroring buildClaudeCommandWithMessage's own
// `if baseCommand == "claude"` gate, whose "custom commands" branch likewise
// returns a non-literal baseCommand untouched.
//
// A no-op (returns baseCommand unchanged) when resolvedBinary is empty, the
// leading word isn't literalName, or it already equals resolvedBinary; this
// keeps the common no-override case byte-identical to before.
func substituteResolvedBinary(baseCommand, literalName, resolvedBinary string) string {
	resolvedBinary = strings.TrimSpace(resolvedBinary)
	if resolvedBinary == "" {
		return baseCommand
	}
	if !isLiteralToolInvocation(baseCommand, literalName) {
		return baseCommand
	}
	trimmed := strings.TrimSpace(baseCommand)
	fields := strings.Fields(trimmed)
	if fields[0] == resolvedBinary {
		return baseCommand
	}
	rest := strings.TrimPrefix(trimmed, fields[0])
	return resolvedBinary + rest
}

// isLiteralToolInvocation reports whether baseCommand's leading
// whitespace-delimited word is exactly literalName — i.e. the user typed
// the bare tool name ("claude"/"codex") rather than an explicit,
// potentially non-canonical, executable path or wrapper. Account-selection
// env/binary substitution in buildShellPassthroughCommand is gated on this:
// an explicit path is the operator's deliberate choice and must never be
// second-guessed, only the bare literal is eligible (see
// substituteResolvedBinary and the codexHomePrefix gate above).
func isLiteralToolInvocation(baseCommand, literalName string) bool {
	fields := strings.Fields(strings.TrimSpace(baseCommand))
	return len(fields) > 0 && fields[0] == literalName
}

// GetGenericSessionID resolves the conversation id for a custom [tools.*]
// tool. Preference order:
//  1. Live tmux env named by [tools.X].session_id_env (when set and present)
//  2. Live tmux env AGENTDECK_TOOL_SESSION_ID, which the capture path publishes
//     for tools that expose an id in their JSON output but export no env var of
//     their own (see buildGenericCommand)
//  3. Persisted GenericSessionID from tool_data (survives reboot)
//
// A live value is written through, so the next cold start resumes the same
// chat. A persisted value is only returned while its recorded tool and
// execution location still match this session — see generic_session_scope.go.
// Whitespace-only values are treated as empty (not resumable).
func (i *Instance) GetGenericSessionID() string {
	// The scope check comes FIRST, before the live pane is consulted at all.
	//
	// A pane outlives the settings it was launched under: change the tool, the
	// command, the project path or the SSH destination on a running session and
	// the pane keeps exporting the id the OLD tool captured. Reading the pane
	// first and adopting whatever it says rebinds that id to the new scope, and
	// one poll later an id this function had just refused is indistinguishable
	// from a legitimate one. Refusing before reading closes that laundering
	// path: while a recorded binding disagrees with the session as it stands,
	// nothing about this session is a source of conversation identity.
	if reason := i.genericSessionScopeMismatch(); reason != "" {
		i.logGenericResumeRefusal(reason)
		return ""
	}

	for _, envName := range i.genericSessionEnvNames() {
		if i.tmuxSession == nil {
			break
		}
		sessionID, err := i.tmuxSession.GetEnvironment(envName)
		if err != nil {
			continue
		}
		if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
			i.persistGenericSessionIDIfChanged(sessionID)
			return sessionID
		}
	}

	return strings.TrimSpace(i.GenericSessionID)
}

// genericSessionEnvNames lists the tmux environment variables that may carry a
// custom tool's live conversation id, most specific first.
//
// The configured session_id_env is the tool's own variable and wins. The
// agent-deck-owned fallback exists because a tool can declare
// output_format_flag + session_id_json_path (so agent-deck can read an id out
// of its JSON) while exporting nothing — before the fallback, that id was
// captured inside the pane's shell, used once, and lost, which is a reboot away
// from being no persistence at all.
func (i *Instance) genericSessionEnvNames() []string {
	return i.genericSessionEnvNamesFor(i.Tool)
}

// genericSessionEnvNamesFor is genericSessionEnvNames over a set of tools,
// deduplicated and always including the agent-deck-owned fallback.
//
// Erasing a binding needs the variables the tool that CAPTURED it declared, not
// only the ones the session's current tool declares — after a tool change those
// are different names, and the one left behind is the one the pane would hand
// back on the next read.
func (i *Instance) genericSessionEnvNamesFor(tools ...string) []string {
	names := make([]string, 0, len(tools)+1)
	seen := make(map[string]bool, len(tools)+1)
	for _, tool := range tools {
		toolDef := GetToolDef(strings.TrimSpace(tool))
		if toolDef == nil || toolDef.SessionIDEnv == "" || seen[toolDef.SessionIDEnv] {
			continue
		}
		seen[toolDef.SessionIDEnv] = true
		names = append(names, toolDef.SessionIDEnv)
	}
	return append(names, genericCapturedSessionIDEnv)
}

// DisplaySessionID returns the session ID the PREVIEW pane surfaces for this
// instance's tool, mirroring the per-tool branching in the right-pane render
// so a copy of the preview info carries the same ID the user sees. Returns ""
// when no session ID is known for the tool.
func (i *Instance) DisplaySessionID() string {
	switch {
	case IsClaudeCompatible(i.Tool):
		return i.ClaudeSessionID
	case i.Tool == "gemini":
		return i.GeminiSessionID
	case i.Tool == "opencode":
		return i.OpenCodeSessionID
	case i.Tool == "codex":
		return i.CodexSessionID
	default:
		return i.GetGenericSessionID()
	}
}

// CanRestartGeneric returns true if a custom tool can be restarted with session resume
func (i *Instance) CanRestartGeneric() bool {
	toolDef := GetToolDef(i.Tool)
	if toolDef == nil {
		return false
	}
	// resume_flag is required; the id may come from live session_id_env OR
	// from the persisted generic_session_id (post-reboot).
	if toolDef.ResumeFlag == "" {
		return false
	}
	return i.GetGenericSessionID() != ""
}

func (i *Instance) applyWrapper(command string) (string, error) {
	wrapper := i.Wrapper
	if wrapper == "" {
		if toolDef := GetToolDef(i.Tool); toolDef != nil {
			wrapper = toolDef.Wrapper
		}
	}
	if wrapper == "" {
		return command, nil
	}
	if strings.Contains(wrapper, wrapperPlaceholder) {
		return strings.ReplaceAll(wrapper, wrapperPlaceholder, command), nil
	}
	return wrapper, nil
}

// hasEffectiveWrapper returns true if the instance has a wrapper configured,
// either directly on the instance or via the tool definition in config.toml.
func (i *Instance) hasEffectiveWrapper() bool {
	if i.Wrapper != "" {
		return true
	}
	if toolDef := GetToolDef(i.Tool); toolDef != nil && toolDef.Wrapper != "" {
		return true
	}
	return false
}

// loadCustomPatternsFromConfig loads detection patterns from built-in defaults + config.toml
// overrides, and sets them on the tmux session for status detection and tool auto-detection.
// Works for ALL tools: built-in (claude, gemini, opencode, codex) and custom.
func (i *Instance) loadCustomPatternsFromConfig() {
	if i.tmuxSession == nil {
		return
	}

	// Merge built-in defaults with any user config overrides/extras
	raw := MergeToolPatterns(i.Tool)
	if raw != nil {
		resolved, err := tmux.CompilePatterns(raw)
		if err != nil {
			sessionLog.Warn("pattern_compile_error", slog.String("tool", i.Tool), slog.String("error", err.Error()))
		}
		if resolved != nil {
			i.tmuxSession.SetPatterns(resolved)
		}
	}

	// Keep detect patterns for DetectTool() (separate from busy/prompt detection)
	if toolDef := GetToolDef(i.Tool); toolDef != nil {
		i.tmuxSession.SetDetectPatterns(i.Tool, toolDef.DetectPatterns)
	}
}

// buildTmuxOptionOverrides returns tmux option overrides from user config,
// adding remain-on-exit for sandbox sessions (needed for dead-pane detection).
// Returns nil if no overrides apply.
func (i *Instance) buildTmuxOptionOverrides() map[string]string {
	var overrides map[string]string
	tmuxCfg := GetTmuxSettings()
	if len(tmuxCfg.Options) > 0 {
		overrides = maps.Clone(tmuxCfg.Options)
	}
	if tmuxCfg.WindowStyleOverride != "" {
		if overrides == nil {
			overrides = make(map[string]string)
		}
		overrides["window-style"] = tmuxCfg.WindowStyleOverride
		overrides["window-active-style"] = tmuxCfg.WindowStyleOverride
	}
	// Sandbox sessions need remain-on-exit so dead-pane detection works.
	// Non-sandbox sessions use default tmux behaviour (pane closes on exit).
	//
	// A documented one-shot needs it for a different reason: the process prints
	// its answer and exits BY DESIGN, and without remain-on-exit tmux tears the
	// pane down with the answer still in it — the user asked a question and got
	// a closed window. Keeping the pane is what makes a one-shot readable.
	if i.IsSandboxed() || i.expectsFastExit() {
		if overrides == nil {
			overrides = make(map[string]string)
		}
		overrides["remain-on-exit"] = "on"
	}
	return overrides
}

// adoptExplicitClaudeSessionID adopts an explicit `--session-id <uuid>` baked
// into i.Command as the authoritative conversation id, correcting any stale or
// disk-hijacked value, and returns true when an explicit id was present (the
// caller must then skip mtime-based disk discovery). An explicit id is the
// user's declaration of WHICH conversation this session owns; honoring it
// before disk discovery is what stops sibling sessions in a shared cwd from
// converging on the newest transcript. Shared by both session-id preludes —
// ensureClaudeSessionIDFromDiskForRestart (#1147) and
// ensureClaudeSessionIDFromDisk (#1465). The reason label distinguishes the
// Start path from the Restart path in the resume log line.
func (i *Instance) adoptExplicitClaudeSessionID(reason string) bool {
	explicit, ok := extractExplicitClaudeSessionID(i.Command)
	if !ok {
		// #1815: a custom command carrying `claude --resume <uuid>` executes
		// verbatim, so without adopting that id the instance would reach the
		// spawn with an empty id and never meet the chokepoint at all.
		// Baking it into this session's own command is the same ownership
		// declaration as --session-id.
		//
		// Review finding on #1830: but ONLY when the command is not shaped
		// like the fork builder's `--resume <SOURCE id> --fork-session`
		// (also reachable via a legacy `--session-id "$VAR"` spelling that
		// makes the --session-id extraction above bail without a UUID,
		// falling through here). Any `--fork-session` or `--session-id`
		// token present means the --resume argument names a DIFFERENT
		// session's (the fork source's) conversation, not this instance's
		// own -- adopting it as verified would be exactly the
		// child-resumes-parent's-conversation shape #1815 exists to stop.
		if !commandHasToken(i.Command, "--fork-session") && !commandHasToken(i.Command, "--session-id") {
			explicit, ok = extractExplicitClaudeResumeID(i.Command)
		}
	}
	if !ok {
		return false
	}
	if i.ClaudeSessionID != explicit {
		i.ClaudeSessionID = explicit
		// #1815 / CodeQL go/log-injection: explicit is parsed out of this
		// session's own command string, which is session-supplied; reason is
		// always one of this file's own string-literal call sites, never a
		// variable from outside, so it needs no sanitizing.
		safeExplicit := logging.SanitizeValue(explicit)
		sessionLog.Info("resume: id="+safeExplicit+" reason="+reason,
			slog.String("instance_id", logging.SanitizeValue(i.ID)),
			slog.String("claude_session_id", safeExplicit),
			slog.String("reason", reason))
	}
	// #1815: vouch AFTER the assignment, so the suspicion cleared is the one
	// on the EXPLICIT value — clearing before would absolve whatever id
	// happened to be current and leave the explicit one's history intact.
	i.markClaudeSessionIDVerified()
	if i.ClaudeDetectedAt.IsZero() {
		i.ClaudeDetectedAt = time.Now()
	}
	return true
}

// ensureClaudeSessionIDFromDisk is the Phase 5 / REQ-7 prelude invoked by
// Start() and StartWithMessage() when an IsClaudeCompatible Instance has an
// empty ClaudeSessionID. It attempts to discover the latest UUID-named
// JSONL under Claude's projects dir for i.ProjectPath and, on success,
// mutates i.ClaudeSessionID to that UUID IN PLACE so the very next check
// (`if i.ClaudeSessionID != ""`) naturally routes through
// buildClaudeResumeCommand, reusing the Phase 3 resume contract verbatim.
//
// PERSIST-11: runs uniformly on every empty-ID Claude-compatible start —
// no branch on i.Command (custom wrapper vs default wrapper), matching
// spec REQ-7 acceptance item 7 and CONTEXT D-04.
//
// PERSIST-12: the write-through onto i.ClaudeSessionID is in-memory; the
// external storage layer (TUI main loop / CLI save cycle / storage
// watcher at internal/session/storage.go) picks up the mutation on its
// next save — identical to how Phase 3's buildClaudeCommand at
// instance.go:567 mutates i.ClaudeSessionID without an inline save.
//
// PERSIST-13: returns quietly (no error, no log) when no JSONL is found.
// Start() then falls through to its existing fresh-session branch.
//
// D-07: emits exactly ONE `resume: id=<uuid> reason=jsonl_discovery`
// sessionLog.Info line on discovery success. buildClaudeResumeCommand
// downstream will emit its OWN `resume: id=<uuid> reason=...` line with
// reason=conversation_data_present or reason=session_id_flag_no_jsonl —
// two grep-stable lines for a Phase 5 discovery start, distinguishable
// by the `reason=` attr.
func (i *Instance) ensureClaudeSessionIDFromDisk() {
	// Issue #1465: an explicit `--session-id <uuid>` baked into i.Command is
	// the authoritative conversation id. Adopt it before the #608 gate and
	// disk discovery so a Start-path session sharing a cwd with newer sibling
	// transcripts (e.g. a removed-then-recreated review session) cannot hijack
	// a sibling's conversation. The Restart prelude already does this for
	// #1147; this closes the same gap on the Start path.
	// Issue #1851: the same bail-out the Restart variant needs, placed before
	// every disk touch below. The #608 gate further down makes the Start path
	// only ACCIDENTALLY safe for remote sessions — it is re-opened by
	// `session set <id> claude-session-id X` (mutators.go), which stamps
	// ClaudeDetectedAt, after which a later clear leaves a remote session
	// eligible for the local mtime walk. The rule is unconditional: a local
	// ~/.claude/projects lookup is never right for a conversation that lives on
	// another host.
	if !i.TranscriptIsResolvableLocally() {
		sessionLog.Info("resume: skipped reason=remote_session",
			slog.String("instance_id", i.ID),
			slog.String("ssh_host", i.SSHHost),
			slog.String("ssh_remote_path", i.SSHRemotePath),
			slog.String("reason", "jsonl_discovery_remote_skip"))
		return
	}
	explicitSessionID := i.adoptExplicitClaudeSessionID("session_id_flag_explicit")
	if i.Tool == "claude" && i.ClaudeSessionID != "" {
		if restored, err := RestoreOrphanedConversationBackup(i, GetClaudeConfigDirForInstance(i)); err == nil && restored != "" {
			sessionLog.Info("resume: restored orphaned conversation backup id="+i.ClaudeSessionID+" reason=orphan_bak_restore",
				slog.String("instance_id", i.ID),
				slog.String("claude_session_id", i.ClaudeSessionID),
				slog.String("path", restored),
				slog.String("reason", "orphan_bak_restore"))
		}
	}
	if explicitSessionID {
		return
	}
	if i.ClaudeSessionID != "" {
		return
	}
	// Fix for https://github.com/asheshgoplani/agent-deck/issues/608:
	// Only attempt JSONL discovery for sessions that previously had a
	// conversation (restart recovery). A zero ClaudeDetectedAt means this
	// session has never been started before — it should get a fresh
	// conversation, not resume another session's history from the same
	// directory.
	if i.ClaudeDetectedAt.IsZero() {
		return
	}
	// Issue #663: multi-repo sessions write their JSONL under
	// ~/.claude/projects/<encoded MultiRepoTempDir>/. ProjectPath is a
	// symlink inside MultiRepoTempDir, so EvalSymlinks would resolve it
	// to the original source repo and miss the JSONL. Use
	// EffectiveWorkingDir() so the encoded-path key matches what Claude
	// actually wrote on the first boot.
	lookupPath := i.EffectiveWorkingDir()
	uuid, found := discoverLatestClaudeJSONL(lookupPath)
	if !found {
		return
	}
	// #1815: discovery returns the newest transcript in this directory, which
	// in a shared directory can belong to another session. Record it as
	// UNVERIFIED so the resume-time identity guard refuses to --resume it.
	i.adoptDiscoveredClaudeSessionID(uuid)
	safeUUID := logging.SanitizeValue(uuid)
	sessionLog.Info("resume: id="+safeUUID+" reason=jsonl_discovery",
		slog.String("instance_id", logging.SanitizeValue(i.ID)),
		slog.String("claude_session_id", safeUUID),
		slog.String("path", logging.SanitizeValue(lookupPath)),
		slog.String("reason", "jsonl_discovery"))
}

// ensureClaudeSessionIDFromDiskForRestart is the Restart()-path variant of
// ensureClaudeSessionIDFromDisk. Issue #956: custom-command Claude sessions
// (Tool=claude with a wrapper Command) bypass happy-path session-id capture,
// and if no hook ever propagated CLAUDE_SESSION_ID back to the Instance the
// ClaudeSessionID field stays empty even after a real conversation has
// written a JSONL transcript to disk. On Restart() the fallback recreate
// branch then re-spawns the wrapper without `--resume`, dropping history.
//
// Start()'s prelude (ensureClaudeSessionIDFromDisk) refuses to discover for
// instances with ClaudeDetectedAt==zero (issue #608) so a brand-new spawn
// does not adopt another session's history from the same project directory.
// Restart() implies the instance previously ran — the tmux session existed
// and (in the bug scenario) had a live Claude conversation — so the gate
// is safe to bypass here. ClaudeDetectedAt is then stamped so subsequent
// callers (status refresh, persistence) see a consistent capture time.
func (i *Instance) ensureClaudeSessionIDFromDiskForRestart() {
	// Issue #1147: an explicit `--session-id <uuid>` in i.Command is the
	// user's authoritative declaration of WHICH conversation this session
	// owns. In multi-session-per-cwd setups (5 tenant sessions sharing one
	// project dir, each with its own --session-id), the pre-#1147
	// disk-discovery walk picks the newest sibling JSONL by mtime and
	// silently hijacks every sibling's id onto whichever transcript was
	// written last. The dup-sweeper then kills 4 of 5 sessions for
	// sharing a CLAUDE_SESSION_ID. Adopting the explicit id BEFORE the
	// non-empty short-circuit ensures it also corrects a previously-
	// hijacked id from an earlier buggy run.
	// Issue #1851: a remote session's conversation lives on the remote host, so
	// a local ~/.claude/projects lookup can never be right for it. Without this
	// bail-out the walk below scans the CONTROLLER's projects dir — keyed on
	// ProjectPath, which for an --ssh session is only a local placeholder
	// defaulting to the controller's cwd — and adopts whichever LOCAL session
	// wrote last. restart() then calls sweepDuplicateToolSessions(), which kills
	// the local tmux session that legitimately owns that conversation.
	//
	// A remote session's local ClaudeSessionID is essentially always empty
	// (SyncSessionIDsToTmux only writes the tmux var when the id is already
	// non-empty), so every remote restart reaches this point.
	if !i.TranscriptIsResolvableLocally() {
		sessionLog.Info("resume: skipped reason=remote_session",
			slog.String("instance_id", i.ID),
			slog.String("ssh_host", i.SSHHost),
			slog.String("ssh_remote_path", i.SSHRemotePath),
			slog.String("reason", "jsonl_discovery_restart_remote_skip"))
		return
	}
	if i.adoptExplicitClaudeSessionID("session_id_flag_explicit_restart") {
		return
	}
	if i.ClaudeSessionID != "" {
		return
	}
	lookupPath := i.EffectiveWorkingDir()
	uuid, found := discoverLatestClaudeJSONL(lookupPath)
	if !found {
		return
	}
	// #1815: see ensureClaudeSessionIDFromDisk — a discovered id is a hint,
	// not proof of ownership, and must not authorize --resume.
	i.adoptDiscoveredClaudeSessionID(uuid)
	if i.ClaudeDetectedAt.IsZero() {
		i.ClaudeDetectedAt = time.Now()
	}
	safeUUID := logging.SanitizeValue(uuid)
	sessionLog.Info("resume: id="+safeUUID+" reason=jsonl_discovery_restart",
		slog.String("instance_id", logging.SanitizeValue(i.ID)),
		slog.String("claude_session_id", safeUUID),
		slog.String("path", logging.SanitizeValue(lookupPath)),
		slog.String("reason", "jsonl_discovery_restart"))
}

// Start starts the session in tmux.
//
// Issue #1040: gated by acquireInstanceSpawnLock plus a "spawned-while-
// we-waited" stamp so concurrent `agent-deck session start <id>`
// invocations after a Claude exit don't each fall through the "tmux
// session does not exist" gate and spawn parallel sessions. The lock
// and gate are inlined here (rather than wrapping the whole body in a
// SpawnAttempt helper) to preserve the structural-grep contract that
// checks Start()'s body for the #745 IsForkAwaitingStart guard.
func (i *Instance) Start() error {
	beforeLock := nowFn()
	release, lockErr := acquireInstanceSpawnLock(i.ID)
	if lockErr != nil {
		return lockErr
	}
	defer release()
	if spawnedSince(i.ID, beforeLock) {
		return nil
	}
	defer recordInstanceSpawn(i.ID)

	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}

	// #1580 diagnosability: clear any stale spawn-failure sidecar and drop a
	// spawn_attempt trace so a spawn that dies before anything else runs still
	// leaves a durable record.
	i.recordSpawnAttempt()

	// Prepare scratch CLAUDE_CONFIG_DIR for non-conductor claude workers
	// (issue #59, v1.7.68). Runs before command-building so the
	// CLAUDE_CONFIG_DIR= prefix picks up the scratch path. No-op for
	// conductors, explicit telegram channel owners, and non-claude tools.
	i.prepareWorkerScratchConfigDirForSpawn() // also runs plugin auto-install per fix C1

	// Pre-accept Codex workspace trust for non-sandbox sessions so first launch
	// does not stall on the trust dialog. Sandbox sessions seed trust after
	// agent config sync in ensureSandboxContainer.
	i.preAcceptCodexWorkspaceTrust()

	// Build command based on tool type
	// Priority: claude-compatible (built-in + custom wrapping claude) → built-in tools → custom tools → raw command
	var command string
	switch {
	case IsClaudeCompatible(i.Tool):
		// #745 fork guard: a fork target arrives here with a pre-built
		// first-start command (`claude --session-id <new> --resume <parent>
		// --fork-session`) and a pre-assigned ClaudeSessionID (the new fork
		// UUID), which would otherwise send us into buildClaudeResumeCommand
		// and silently drop --resume / --fork-session. Run the fork command
		// verbatim and clear the sentinel so a subsequent Restart() takes the
		// normal resume path.
		if i.IsForkAwaitingStart {
			command = i.consumeForkStartCommand()
			sessionLog.Info("resume: none reason=fork_awaiting_start",
				slog.String("instance_id", i.ID),
				slog.String("path", i.ProjectPath),
				slog.String("reason", "fork_awaiting_start"))
			break
		}
		// REQ-2 dispatch: if a Claude session id is already bound to this
		// instance, resume it rather than minting a fresh UUID via
		// buildClaudeCommand (instance.go:566-567). Mirrors Restart()'s
		// respawn-pane branch at instance.go:3788. See CONTEXT Decision 1.
		//
		// OBS-02 emission: buildClaudeResumeCommand emits its own "resume: "
		// Info line (conversation_data_present / session_id_flag_no_jsonl).
		// The fresh-session line is emitted here so every Claude start
		// produces exactly one "resume: " record in the session log. See
		// CONTEXT Decision 2.
		//
		// REQ-7 / PERSIST-11..13 prelude (Phase 5): if ClaudeSessionID is
		// empty, attempt to discover the latest JSONL on disk and populate
		// the field before the existing resume/fresh branch below decides.
		// If discovery finds nothing, the field stays empty and the
		// else-branch fires as today (fresh session).
		i.ensureClaudeSessionIDFromDisk()
		if i.ClaudeSessionID != "" {
			command = i.buildClaudeResumeCommand()
		} else {
			sessionLog.Info("resume: none reason=fresh_session",
				slog.String("instance_id", i.ID),
				slog.String("path", i.ProjectPath),
				slog.String("reason", "fresh_session"))
			command = i.buildClaudeCommand(i.Command)
		}
	case i.Tool == "gemini":
		command = i.buildGeminiCommand(i.Command)
	case i.Tool == "copilot":
		command = buildCopilotCommand(i)
		// Record start time for session ID detection (Unix millis)
		i.CopilotStartedAt = time.Now().UnixMilli()
	case i.Tool == "opencode":
		if i.IsForkAwaitingStart {
			// Wrap the deferred fork command through buildOpenCodeCommand so the
			// env prefix is applied exactly once (the `--fork` command carries
			// none); a later restart falls through to the resume/fresh branch
			// below via the stable "opencode" base Command and the async-detected
			// child session id (re-running `--fork` would re-fork the parent).
			command = i.buildOpenCodeCommand(i.consumeForkStartCommand())
			i.OpenCodeStartedAt = time.Now().UnixMilli()
			sessionLog.Info("resume: none reason=fork_awaiting_start",
				slog.String("instance_id", i.ID),
				slog.String("path", i.ProjectPath),
				slog.String("reason", "fork_awaiting_start"))
			break
		}
		command = i.buildOpenCodeCommand(i.Command)
		// Record start time for session ID detection (Unix millis)
		i.OpenCodeStartedAt = time.Now().UnixMilli()
	case IsCodexCompatible(i.Tool):
		if i.IsForkAwaitingStart {
			command = i.consumeForkStartCommand()
			// Stamp the start time so the session-id disk scan is lower-bounded and
			// can't rebind this fork to an older same-project rollout (e.g. the
			// parent it just forked from). The normal path stamps after
			// buildCodexCommand, which this fork branch skips.
			i.CodexStartedAt = time.Now().UnixMilli()
			sessionLog.Info("resume: none reason=fork_awaiting_start",
				slog.String("instance_id", i.ID),
				slog.String("path", i.ProjectPath),
				slog.String("reason", "fork_awaiting_start"))
			break
		}
		command = i.buildCodexCommand(i.Command)
		// Record start time for session ID detection (Unix millis)
		i.CodexStartedAt = time.Now().UnixMilli()
	case i.Tool == "pi":
		if i.IsForkAwaitingStart {
			command = i.consumeForkStartCommand()
			sessionLog.Info("resume: none reason=fork_awaiting_start",
				slog.String("instance_id", i.ID),
				slog.String("path", i.ProjectPath),
				slog.String("reason", "fork_awaiting_start"))
			break
		}
		command = i.buildPiCommand(i.Command)
	case i.Tool == "copilot":
		command = i.buildCopilotCommand(i.Command)
	case i.Tool == "cursor":
		command = i.buildCursorCommand(i.Command, false)
	case i.Tool == "hermes":
		// Hermes emits no lifecycle event at process launch (on_session_start
		// fires only at the first turn), so seed the "waiting" baseline the
		// prompt represents — otherwise a fresh session has no status icon
		// until the user types.
		if _, err := i.seedHermesHookGeneration("waiting", false); err != nil {
			return err
		}
		command = i.buildHermesCommand(i.Command)
	case i.Tool == "deepseek":
		// The headless profile answers one task and exits, so a plain Start()
		// must replay a recorded task or refuse — `dsh --profile headless` with
		// no positional is a usage error dsh rejects before anything could be
		// delivered (PR #1942 review, P1b/P1c).
		dsCommand, dsErr := i.deepSeekStartCommand()
		if dsErr != nil {
			return dsErr
		}
		command = dsCommand
	default:
		// Check if this is a custom tool with session resume config
		if toolDef := GetToolDef(i.Tool); toolDef != nil {
			command = i.buildGenericCommand(i.Command)
		} else {
			command = i.buildShellPassthroughCommand(i.Command)
		}
	}

	var containerName string
	var err error
	command, containerName, err = i.prepareCommand(command)
	if err != nil {
		// #1924: leave a reason behind. Without this the session sits on
		// StatusError with no tmux session and nothing to diagnose from.
		i.recordPrepareFailure(command, err)
		return err
	}
	if containerName != "" {
		i.SandboxContainer = containerName
	}

	// Load custom patterns for status detection
	i.loadCustomPatternsFromConfig()

	// Build tmux option overrides from config (e.g. allow-passthrough = "all").
	// Sandbox sessions also get remain-on-exit for dead-pane detection.
	i.tmuxSession.OptionOverrides = i.buildTmuxOptionOverrides()
	i.tmuxSession.RunCommandAsInitialProcess = i.IsSandboxed() || i.Tool != "shell"
	i.applyLaunchSettingsFromConfig()

	// Re-assert the declarative per-group/per-conductor skill+mcp loadout
	// BEFORE the tool process starts so project-scope discovery sees it.
	// Idempotent: a healthy floor is a no-op; failures warn, never block.
	ApplyConfiguredLoadout(i)

	i.preAcceptCursorWorkspaceTrust()

	// Start the tmux session
	if err := i.tmuxSession.Start(command); err != nil {
		// #1580: persist the tmux-level failure so the preview / session show /
		// lifecycle log can surface it instead of a bare "error".
		i.recordTmuxStartFailure(command, err)
		return fmt.Errorf("failed to start tmux session: %w", err)
	}

	// #1580: watch for a fast death of the initial process (broken command,
	// bad PATH, immediate non-zero exit). tmux tears the pane down on exit for
	// non-remain-on-exit sessions, so this captures the dying output while the
	// pane is still alive and records it if the session vanishes early. The
	// watcher is handed value snapshots + a supersede generation so it never
	// touches i's mutex-guarded fields from its own goroutine.
	//
	// A documented one-shot is exempt: `dsh --profile headless "<task>"` answers
	// once and exits, so a short life is SUCCESS, not a spawn failure. Without
	// the exemption every completed one-shot would be recorded as died-fast and
	// the preview would paint "⚠ session failed to start" over its answer.
	if command != "" && !i.expectsFastExit() {
		// gen AND the wake channel are both produced here, in the caller, so no
		// bump can slip into the gap before the watcher subscribes (see
		// newSpawnGenWatch).
		gen, wake := i.newSpawnGenWatch()
		// Resolve both write targets HERE, synchronously in the caller, not
		// inside the goroutine: GetSessionIDLifecycleLogPath()/spawnFailureDir()
		// read the live $HOME, and watchForFastDeath's goroutine is never
		// joined — it can still be sleeping on its ticker when a later test
		// changes $HOME out from under it. Capturing the resolved paths as
		// plain values at spawn time (Go evaluates `go` call arguments in the
		// calling goroutine) makes the watcher's writes land in the HOME that
		// was live when this session started, never whichever HOME happens to
		// be live when the ticker next fires.
		go i.watchForFastDeath(command, gen, wake, i.tmuxSession, i.ID, i.Tool, sessionLog, GetSessionIDLifecycleLogPath(), spawnFailureDir())
	}

	// CFG-07: emit a single-shot log line documenting which priority level
	// resolved CLAUDE_CONFIG_DIR for this session. Claude-compatible tools
	// only; Fork inherits from its parent and does not log here.
	if IsClaudeCompatible(i.Tool) {
		i.logClaudeConfigResolution()
	}

	// Set AGENTDECK_INSTANCE_ID for Claude hooks to identify this session
	// This enables real-time status updates via Stop/SessionStart hooks
	if err := i.tmuxSession.SetEnvironment("AGENTDECK_INSTANCE_ID", i.ID); err != nil {
		sessionLog.Warn("set_instance_id_failed", slog.String("error", err.Error()))
	}

	// Set AGENTDECK_PROFILE (host-side, tool-agnostic) so a bare `agent-deck`
	// command run inside this session resolves the session's own profile rather
	// than falling back to "default". Covers shells/OpenCode/etc. that have no
	// inline env-prefix injection of their own.
	i.ensureProfileEnv()
	i.ensureClaudeConfigDirEnv()

	// Propagate tool session IDs into the tmux environment (host-side, works for both
	// sandbox and non-sandbox sessions). This replaces the previous approach of embedding
	// "tmux set-environment" calls in the shell command string, which silently failed
	// inside Docker sandbox containers that have no access to the host tmux socket.
	if i.ClaudeSessionID != "" {
		_ = i.tmuxSession.SetEnvironment("CLAUDE_SESSION_ID", i.ClaudeSessionID)
		// Kill any other agentdeck tmux session with the same Claude session ID
		// to prevent duplicates running `claude --resume` with the same conversation (#596).
		tmux.KillSessionsWithEnvValue("CLAUDE_SESSION_ID", i.ClaudeSessionID, i.tmuxSession.Name)
	}
	if i.GeminiSessionID != "" {
		_ = i.tmuxSession.SetEnvironment("GEMINI_SESSION_ID", i.GeminiSessionID)
	}
	if i.Tool == "gemini" {
		yoloVal := "false"
		if i.GeminiYoloMode != nil && *i.GeminiYoloMode {
			yoloVal = "true"
		}
		_ = i.tmuxSession.SetEnvironment("GEMINI_YOLO_MODE", yoloVal)
	}
	// OpenCode and Codex IDs are detected asynchronously; SyncSessionIDsToTmux() handles
	// propagation once they are available.
	// Copilot session ID propagation (if already known from prior session)
	if i.CopilotSessionID != "" {
		_ = i.tmuxSession.SetEnvironment("COPILOT_SESSION_ID", i.CopilotSessionID)
	}

	// Propagate COLORFGBG into the tmux session environment so that any new
	// shell or process spawned inside the session inherits the correct
	// light/dark hint. The command prefix already exports it for the initial
	// process, but set-environment covers subsequent shells/windows.
	if colorfgbg := ThemeColorFGBG(); colorfgbg != "" {
		_ = i.tmuxSession.SetEnvironment("COLORFGBG", colorfgbg)
	}

	// Capture MCPs that are now loaded (for sync tracking)
	i.CaptureLoadedMCPs()

	// Record start time for grace period (prevents error flash during tmux startup)
	i.lastStartTime = time.Now()
	i.markStarted() // persisted stamp (issue #30 — cross-process freshness guard)

	// New sessions start as STARTING - shows they're initializing
	// After 5s grace period, status will be properly detected from tmux
	if command != "" {
		i.Status = StatusStarting
	}

	// Start async session ID detection for OpenCode
	// This runs in background and captures the session ID once OpenCode creates it
	if i.Tool == "opencode" {
		go i.detectOpenCodeSessionAsync()
	}

	// Start async session ID detection for Codex
	// This runs in background and captures the session ID once Codex creates it
	if IsCodexCompatible(i.Tool) {
		go i.detectCodexSessionAsync()
	}

	// Start async session ID detection for Copilot
	// This runs in background and captures the session ID from events.jsonl
	if i.Tool == "copilot" && i.CopilotSessionID == "" {
		go i.detectCopilotSessionAsync()
	}

	return nil
}

// StartWithMessage starts the session and sends an initial message when ready
// The message is sent synchronously after detecting the agent's prompt
// This approach is more reliable than embedding send logic in the tmux command
// Works for Claude, Gemini, OpenCode, and other agents
//
// Issue #1040: same per-instance spawn lock as Start() — a concurrent
// `launch -m "..."` racing with a poller-triggered Start() must not
// produce two parallel tmux sessions.
func (i *Instance) StartWithMessage(message string) error {
	beforeLock := nowFn()
	release, lockErr := acquireInstanceSpawnLock(i.ID)
	if lockErr != nil {
		return lockErr
	}
	defer release()
	if spawnedSince(i.ID, beforeLock) {
		return nil
	}
	defer recordInstanceSpawn(i.ID)

	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}

	// Refuse a message this session has no way to receive, BEFORE spawning
	// anything (PR #1942 review, P1a). The DeepSeek web profile is an HTTP
	// server with no terminal prompt: the post-start send path below would type
	// the message into the server process's stdin and report success, which is
	// silent data loss — the worst failure class here. Every other tool returns
	// nil from this check, so nothing else changes.
	//
	// Refusing before the spawn matters as much as refusing at all: reporting
	// failure while leaving a running server behind is its own trap.
	if message != "" {
		if err := i.PromptDeliveryError(); err != nil {
			return err
		}
	}

	// #1580 diagnosability: clear any stale spawn-failure sidecar and drop a
	// spawn_attempt trace (same as Start()).
	i.recordSpawnAttempt()

	// Prepare scratch CLAUDE_CONFIG_DIR for non-conductor claude workers
	// (issue #59, v1.7.68). Same call as in Start() — both spawn paths
	// must pin the telegram plugin off for workers.
	i.prepareWorkerScratchConfigDirForSpawn() // also runs plugin auto-install per fix C1

	// Start session normally (no embedded message logic)
	// Priority: built-in tools (claude, gemini, opencode, codex) → custom tools from config.toml → raw command
	var command string
	// Codex takes its initial prompt as a positional argument instead of having it
	// typed into the TUI; when that happens there is nothing left to send. The
	// DeepSeek headless profile is the same shape — `dsh --profile headless
	// "<task>"` IS the whole invocation — so the flag is tool-neutral.
	promptEmbeddedInCommand := false
	switch {
	case IsClaudeCompatible(i.Tool):
		// #745 fork guard: mirrors the Start() branch above. A fork target
		// that arrives through StartWithMessage must also bypass the
		// resume/fresh dispatch and run its first-start command verbatim, or
		// the --resume <parent>/--fork-session flags are silently dropped.
		if i.IsForkAwaitingStart {
			command = i.consumeForkStartCommand()
			sessionLog.Info("resume: none reason=fork_awaiting_start",
				slog.String("instance_id", i.ID),
				slog.String("path", i.ProjectPath),
				slog.String("reason", "fork_awaiting_start"))
			break
		}
		// REQ-2 dispatch: resume over mint when a session id is bound. The
		// initial message passed into StartWithMessage is delivered via the
		// existing post-start PTY send path later in this function (see
		// sendMessageWhenReady below) — not embedded in the command string —
		// so buildClaudeResumeCommand (which does not accept a message
		// argument) is a drop-in replacement here, matching the Start()
		// dispatch at instance.go:1881. See CONTEXT Decision 1 + Decision 11.
		//
		// OBS-02 emission: buildClaudeResumeCommand emits the resume line
		// itself; the fresh-session line is emitted here so StartWithMessage
		// produces exactly one "resume: " record per call (matching Start()).
		// See CONTEXT Decision 2.
		//
		// REQ-7 / PERSIST-11..13 prelude (Phase 5): same disk-scan prelude
		// as Start() above, so the `agent-deck session send --initial-message`
		// path resumes custom-command sessions uniformly.
		i.ensureClaudeSessionIDFromDisk()
		if i.ClaudeSessionID != "" {
			command = i.buildClaudeResumeCommand()
		} else {
			sessionLog.Info("resume: none reason=fresh_session",
				slog.String("instance_id", i.ID),
				slog.String("path", i.ProjectPath),
				slog.String("reason", "fresh_session"))
			command = i.buildClaudeCommand(i.Command)
		}
	case i.Tool == "gemini":
		command = i.buildGeminiCommand(i.Command)
	case i.Tool == "opencode":
		if i.IsForkAwaitingStart {
			command = i.buildOpenCodeCommand(i.consumeForkStartCommand())
			i.OpenCodeStartedAt = time.Now().UnixMilli()
			sessionLog.Info("resume: none reason=fork_awaiting_start",
				slog.String("instance_id", i.ID),
				slog.String("path", i.ProjectPath),
				slog.String("reason", "fork_awaiting_start"))
			break
		}
		command = i.buildOpenCodeCommand(i.Command)
		i.OpenCodeStartedAt = time.Now().UnixMilli()
	case IsCodexCompatible(i.Tool):
		if i.IsForkAwaitingStart {
			command = i.consumeForkStartCommand()
			// Stamp the start time so the session-id disk scan is lower-bounded and
			// can't rebind this fork to an older same-project rollout (e.g. the
			// parent it just forked from). The normal path stamps after
			// buildCodexCommand, which this fork branch skips.
			i.CodexStartedAt = time.Now().UnixMilli()
			sessionLog.Info("resume: none reason=fork_awaiting_start",
				slog.String("instance_id", i.ID),
				slog.String("path", i.ProjectPath),
				slog.String("reason", "fork_awaiting_start"))
			break
		}
		command, promptEmbeddedInCommand = i.buildCodexCommandWithPrompt(i.Command, message)
		i.CodexStartedAt = time.Now().UnixMilli()
	case i.Tool == "pi":
		if i.IsForkAwaitingStart {
			command = i.consumeForkStartCommand()
			sessionLog.Info("resume: none reason=fork_awaiting_start",
				slog.String("instance_id", i.ID),
				slog.String("path", i.ProjectPath),
				slog.String("reason", "fork_awaiting_start"))
			break
		}
		command = i.buildPiCommand(i.Command)
	case i.Tool == "copilot":
		command = i.buildCopilotCommand(i.Command)
	case i.Tool == "crush":
		command = i.buildCrushCommand(i.Command)
	case i.Tool == "cursor":
		command = i.buildCursorCommand(i.Command, false)
	case i.Tool == "hermes":
		// Hermes emits no lifecycle event at process launch (on_session_start
		// fires only at the first turn), so seed the "waiting" baseline the
		// prompt represents — otherwise a fresh session has no status icon
		// until the user types.
		if _, err := i.seedHermesHookGeneration("running", true); err != nil {
			return err
		}
		command = i.buildHermesCommand(i.Command)
	case i.Tool == "deepseek":
		// Only the headless profile takes a task positionally; an installed
		// interactive profile gets the prompt typed into its pane by the
		// post-start send path below. The web profile has no prompt at all and
		// was rejected above, before any spawn.
		if message == "" {
			// StartWithMessage with no message is an ordinary start, and
			// headless still needs its recorded task.
			dsCommand, dsErr := i.deepSeekStartCommand()
			if dsErr != nil {
				return dsErr
			}
			command = dsCommand
			break
		}
		command, promptEmbeddedInCommand = i.buildDeepSeekCommandWithPrompt(i.Command, message)
	default:
		// Check if this is a custom tool with session resume config
		if toolDef := GetToolDef(i.Tool); toolDef != nil {
			command = i.buildGenericCommand(i.Command)
		} else {
			command = i.buildShellPassthroughCommand(i.Command)
		}
	}

	var containerName string
	var err error
	command, containerName, err = i.prepareCommand(command)
	if err != nil {
		// #1924: leave a reason behind. Without this the session sits on
		// StatusError with no tmux session and nothing to diagnose from.
		i.recordPrepareFailure(command, err)
		return err
	}
	if containerName != "" {
		i.SandboxContainer = containerName
	}

	// Load custom patterns for status detection.
	i.loadCustomPatternsFromConfig()

	// Build tmux option overrides from config (e.g. allow-passthrough = "all").
	// Sandbox sessions also get remain-on-exit for dead-pane detection.
	i.tmuxSession.OptionOverrides = i.buildTmuxOptionOverrides()
	i.tmuxSession.RunCommandAsInitialProcess = i.IsSandboxed() || i.Tool != "shell"
	i.applyLaunchSettingsFromConfig()

	// Re-assert the declarative skill+mcp loadout before spawn — sister
	// call to Start(); see ApplyConfiguredLoadout.
	ApplyConfiguredLoadout(i)

	i.preAcceptCursorWorkspaceTrust()

	// Start the tmux session
	if err := i.tmuxSession.Start(command); err != nil {
		// #1580: persist the tmux-level failure (sister path to Start()).
		i.recordTmuxStartFailure(command, err)
		return fmt.Errorf("failed to start tmux session: %w", err)
	}

	// #1580: fast-death watcher (sister path to Start()).
	if command != "" {
		gen, wake := i.newSpawnGenWatch()
		// See the matching comment in Start(): resolve the write targets — and
		// subscribe to the wake — here, not inside the never-joined goroutine.
		go i.watchForFastDeath(command, gen, wake, i.tmuxSession, i.ID, i.Tool, sessionLog, GetSessionIDLifecycleLogPath(), spawnFailureDir())
	}

	// CFG-07: emit a single-shot log line documenting which priority level
	// resolved CLAUDE_CONFIG_DIR for this session. Claude-compatible tools
	// only; sister path to Start().
	if IsClaudeCompatible(i.Tool) {
		i.logClaudeConfigResolution()
	}

	// Set AGENTDECK_INSTANCE_ID for Claude hooks to identify this session
	// This enables real-time status updates via Stop/SessionStart hooks
	if err := i.tmuxSession.SetEnvironment("AGENTDECK_INSTANCE_ID", i.ID); err != nil {
		sessionLog.Warn("set_instance_id_failed", slog.String("error", err.Error()))
	}

	// Set AGENTDECK_PROFILE (host-side, tool-agnostic) so a bare `agent-deck`
	// command run inside this session resolves the session's own profile rather
	// than falling back to "default". Covers shells/OpenCode/etc. that have no
	// inline env-prefix injection of their own.
	i.ensureProfileEnv()
	i.ensureClaudeConfigDirEnv()

	// Propagate tool session IDs into the tmux environment (host-side, works for both
	// sandbox and non-sandbox sessions).
	if i.ClaudeSessionID != "" {
		_ = i.tmuxSession.SetEnvironment("CLAUDE_SESSION_ID", i.ClaudeSessionID)
	}
	if i.GeminiSessionID != "" {
		_ = i.tmuxSession.SetEnvironment("GEMINI_SESSION_ID", i.GeminiSessionID)
	}
	if i.Tool == "gemini" {
		yoloVal := "false"
		if i.GeminiYoloMode != nil && *i.GeminiYoloMode {
			yoloVal = "true"
		}
		_ = i.tmuxSession.SetEnvironment("GEMINI_YOLO_MODE", yoloVal)
	}

	// Propagate COLORFGBG into the tmux session environment so that any new
	// shell or process spawned inside the session inherits the correct
	// light/dark hint.
	if colorfgbg := ThemeColorFGBG(); colorfgbg != "" {
		_ = i.tmuxSession.SetEnvironment("COLORFGBG", colorfgbg)
	}

	// Capture MCPs that are now loaded (for sync tracking)
	i.CaptureLoadedMCPs()

	// Record start time for grace period (prevents error flash during tmux startup)
	i.lastStartTime = time.Now()
	i.markStarted() // persisted stamp (issue #30 — cross-process freshness guard)

	// New sessions start as STARTING
	i.Status = StatusStarting

	// Start async session ID detection for tools that persist IDs out-of-band.
	if i.Tool == "opencode" {
		go i.detectOpenCodeSessionAsync()
	}
	if IsCodexCompatible(i.Tool) {
		go i.detectCodexSessionAsync()
	}

	// Send message synchronously (CLI will wait). Codex may already carry the
	// prompt as a launch argument, in which case there is nothing to type.
	if message != "" && !promptEmbeddedInCommand {
		return i.sendMessageWhenReady(message)
	}

	return nil
}

// sendMessageWhenReady waits for the agent to be ready and sends the message.
// Uses the shared WaitForAgentReady helper (same semantics as `session send`).
func (i *Instance) sendMessageWhenReady(message string) error {
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}

	if err := send.WaitForAgentReady(i.tmuxSession, i.Tool, send.DefaultAgentReadyTimeout, send.PromptGates{
		ClaudeComposer: IsClaudeCompatible(i.Tool),
		CodexPrompt:    IsCodexCompatible(i.Tool),
	}); err != nil {
		return fmt.Errorf("timeout waiting for agent to be ready")
	}

	// Pre-send provenance probe for the #1777 attribution gate: Claude
	// collapses a bulk paste behind "[Pasted text #N +M lines]", so the
	// composer body can no longer be matched against our payload. Observing
	// an unmarked composer immediately before typing is the evidence that a
	// marker seen afterwards is OUR collapse and not a foreign paste parked
	// there. A capture failure leaves it false, and the gate then withholds
	// the nudge.
	composerPasteFreeBeforeSend := false
	if raw, captureErr := i.tmuxSession.CapturePaneFresh(); captureErr == nil {
		composerPasteFreeBeforeSend = !send.ComposerHoldsPasteMarker(raw, tmux.StripANSI)
	}

	if err := i.tmuxSession.SendKeysAndEnter(message); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	// The verify loop below keys off Claude-specific signals (an
	// "active" transition, composer glyph, unsent-paste markers). Non-
	// Claude tools never surface those, so the loop false-negatives a
	// delivered message and Enter-spams the composer; skip it for every
	// non-Claude tool (#1238 — generalizes #1228's codex-only skip).
	if !UsesClaudeDeliveryVerify(i.Tool) {
		return nil
	}

	// Verify the agent accepted Enter and began processing.
	const verifyRetries = 50
	const verifyDelay = 300 * time.Millisecond
	const activeSuccessThreshold = 2
	const waitingAfterActiveThreshold = 2
	waitingNoMarkerChecks := 0
	activeChecks := 0
	sawActiveAfterSend := false
	// attrib is the #1777 attribution gate. EVERY bare Enter below —
	// including the unsent-prompt branch, which used to press unconditionally
	// whenever a "[Pasted text …]" marker appeared anywhere in the pane —
	// routes through attrib.NudgeEnter, so no branch can submit composer
	// content agent-deck cannot attribute to its own delivery.
	attrib := send.EnterAttribution{
		Message:        message,
		OwnPasteMarker: composerPasteFreeBeforeSend,
	}

	for retry := 0; retry < verifyRetries; retry++ {
		time.Sleep(verifyDelay)

		unsentPromptDetected := false
		// paneNow is this iteration's observation (raw ANSI + whether the
		// capture succeeded at all), and is what the attribution gate reads.
		captured, captureErr := i.tmuxSession.CapturePaneFresh()
		paneNow := send.CaptureOutcome(captured, captureErr)
		if paneNow.OK {
			content := tmux.StripANSI(captured)
			unsentPromptDetected = send.ComposerHoldsPasteMarker(captured, tmux.StripANSI) || send.HasUnsentComposerPrompt(content, message)
		}
		verifiedStatus, statusErr := i.tmuxSession.GetStatus()

		if unsentPromptDetected {
			waitingNoMarkerChecks = 0
			activeChecks = 0
			attrib.NudgeEnter(i.tmuxSession, paneNow, tmux.StripANSI)
			continue
		}

		if statusErr == nil && verifiedStatus == "active" {
			sawActiveAfterSend = true
			waitingNoMarkerChecks = 0
			activeChecks++
			if activeChecks >= activeSuccessThreshold {
				return nil
			}
			continue
		}
		activeChecks = 0

		if statusErr == nil && (verifiedStatus == "waiting" || verifiedStatus == "idle") {
			if sawActiveAfterSend {
				waitingNoMarkerChecks++
				if waitingNoMarkerChecks >= waitingAfterActiveThreshold {
					return nil
				}
			} else {
				waitingNoMarkerChecks = 0
				if retry < 5 || retry%2 == 0 {
					attrib.NudgeEnter(i.tmuxSession, paneNow, tmux.StripANSI)
				}
			}
			continue
		}

		waitingNoMarkerChecks = 0
		if retry < 4 {
			attrib.NudgeEnter(i.tmuxSession, paneNow, tmux.StripANSI)
		}
	}

	return nil
}

// errorRecheckInterval - how often to recheck sessions that don't exist
// Ghost sessions (in JSON but not in tmux) are rechecked at this interval
// instead of every 500ms tick, dramatically reducing subprocess spawns
const errorRecheckInterval = 30 * time.Second

// resumeCheckRetryDelay is the wait between the two sessionHasConversationData
// checks in buildClaudeResumeCommand (Issue #662). SessionEnd writes are
// observed to finish within ~100-150ms in practice; 200ms gives headroom
// without noticeably slowing the restart path when there truly is no jsonl.
var resumeCheckRetryDelay = 200 * time.Millisecond

// clearRebindMtimeGrace is the mtime gap (candidate.mtime - current.mtime)
// above which UpdateHookStatus treats a smaller candidate as a legitimate
// user-initiated new session (e.g. /clear) instead of a stale flap (issue
// #856). 5s is well above the ~2s hook poll cadence — a #661 flap touches
// both files within that window — but well below the time it takes a user
// to type /clear and a follow-up prompt.
var clearRebindMtimeGrace = 5 * time.Second

func hookFastPathFreshnessForTool(tool, hookStatus string) time.Duration {
	if !IsCodexCompatible(tool) {
		return hookFastPathWindow
	}

	// Codex hook events are turn-based and can be sparse depending on command mode.
	// Keep running freshness short, but preserve completion/waiting signals longer so
	// the user can reliably see attention-needed state.
	switch hookStatus {
	case "waiting":
		return codexHookWaitingFastPathWindow
	default:
		return codexHookRunningFastPathWindow
	}
}

// shellForegroundRunning reports whether a "shell" tool session currently has a
// genuine non-interactive foreground process running (e.g. "node" from
// `yarn dev`, "java" from `mvn spring-boot:run`). It returns false for the
// interactive shell itself and for interactive foreground programs (editors,
// pagers, system monitors, remote shells, multiplexers) that are really waiting
// for the user rather than doing background work — otherwise opening vim or
// sitting at an ssh prompt would show a perpetual running indicator.
//
// It relies on the pane-info cache warmed once per tick by RefreshPaneInfoCache
// (TUI backgroundStatusUpdate / Web refreshStatuses / CLI status refresh). When
// the cache is cold the lookup misses and this returns false, preserving the
// historical "shell maps to idle" behavior. Caller must hold i.mu.
//
// The feature is opt-in via [status] shell_running_indicator (default false):
// the interactive-program denylist cannot be complete, so without the flag a
// shell sitting at a psql/REPL/fzf prompt would flip everyone's historical
// "shell → idle" default to running.
//
// Staleness guards — only fresh pane info may promote idle→running:
//   - GetCachedPaneInfoSnapshot enforces the cache-wide 4s TTL (2 ticks).
//   - A dead pane (#{pane_dead}) means the command already exited.
//   - A snapshot taken before this instance's last start describes a previous
//     same-name session (kill+recreate within the TTL), not this one.
func (i *Instance) shellForegroundRunning() bool {
	if i.tmuxSession == nil {
		return false
	}
	cfg, _ := LoadUserConfig()
	if cfg == nil || !cfg.Status.ShellRunningIndicator {
		return false
	}
	paneInfo, snapshotAt, ok := tmux.GetCachedPaneInfoSnapshot(i.tmuxSession.Name)
	if !ok || paneInfo.Dead || paneInfo.CurrentCommand == "" {
		return false
	}
	if !i.lastStartTime.IsZero() && snapshotAt.Before(i.lastStartTime) {
		return false
	}
	cmd := paneInfo.CurrentCommand
	if isShellBinary(cmd) || isInteractiveForegroundProgram(cmd) {
		return false
	}
	return true
}

// neverStarted reports whether this session was added but never started, so an
// absent tmux session is expected rather than a fault. Two conditions must both
// hold (caller holds i.mu):
//
//  1. The instance was added in THIS process (addedThisProcess), not reloaded
//     from storage. A reloaded session whose tmux later dies is a genuine error
//     (instance_cli_parity_test.go TestUpdateStatus_CLIvsTUIParity_Error builds
//     a reloaded struct literal, so addedThisProcess is false there).
//  2. Start() was never called (lastStartTime is zero). A started-then-killed
//     session has a non-zero lastStartTime and must surface as error
//     (lifecycle_regression_test.go phase5).
//  3. The status is still the pristine post-add state (idle or starting).
func (i *Instance) neverStarted() bool {
	return i.addedThisProcess && i.lastStartTime.IsZero() &&
		(i.Status == StatusIdle || i.Status == StatusStarting)
}

// UpdateStatus updates the session status by checking tmux.
// Thread-safe: acquires write lock to protect Status, Tool, and internal cache fields.
// debounceFlipFromRunning decides whether a tmux-derived status that flips AWAY
// from running should be held for one confirming sample. It returns the status
// to apply, the next value for the pending marker, and whether the flip was
// HELD (the caller applies `apply` and returns early when held is true).
//
// Held only on the FIRST tmux-inferred running→{waiting,error} sample (pending
// was false): a long single tool-call past the hook freshness window, or a
// transient CapturePane failure during subprocess churn, can present that flip
// for one tick and then recover. A genuinely dead pane (tmux raw "inactive")
// and a "dead" hook are real terminal signals and are never debounced. Pure for
// testability.
func debounceFlipFromRunning(prev, derived Status, tmuxRaw, hookStatus string, pending bool) (apply Status, nextPending bool, held bool) {
	flipAwayFromRunning := (derived == StatusWaiting || derived == StatusError) &&
		tmuxRaw != "inactive" && hookStatus != "dead"
	if prev == StatusRunning && flipAwayFromRunning && !pending {
		return StatusRunning, true, true
	}
	return derived, false, false
}

func shouldDebounceTmuxFlipForTool(tool string) bool {
	return tool == "" || IsClaudeCompatible(tool) || IsCodexCompatible(tool) ||
		tool == "gemini" || tool == "hermes" || tool == "cursor"
}

// setCodexGenerationEvidence copies the durable notify edge pair into the
// instance. Unlike tmuxFlipFromRunningPending, these fields survive a fresh
// CLI process because they are retained in the hook status file.
func (i *Instance) setCodexGenerationEvidence(status *HookStatus) {
	if status == nil || !IsCodexCompatible(i.Tool) {
		return
	}
	if status.codexCompletionConsumed {
		i.codexStartedGeneration, i.codexCompletedGeneration = "", ""
		i.codexStartedSessionID, i.codexCompletedSessionID = "", ""
		return
	}
	if status.CodexStartedGeneration == "" && status.CodexCompletedGeneration == "" &&
		status.CodexStartedSessionID == "" && status.CodexCompletedSessionID == "" {
		return
	}
	i.codexStartedGeneration = strings.TrimSpace(status.CodexStartedGeneration)
	i.codexCompletedGeneration = strings.TrimSpace(status.CodexCompletedGeneration)
	i.codexStartedSessionID = strings.TrimSpace(status.CodexStartedSessionID)
	i.codexCompletedSessionID = strings.TrimSpace(status.CodexCompletedSessionID)
	if i.codexInvalidatingGeneration != i.codexStartedGeneration {
		i.codexInvalidatingGeneration = ""
	}
}

// codexCompletionConverged is deliberately fail-closed. Only the same retained
// generation, for the same non-empty session bound to this instance, proves a
// completed turn. Missing, mismatched, and superseded evidence stays subject
// to the conservative running debounce.
func (i *Instance) codexCompletionConverged() bool {
	if !IsCodexCompatible(i.Tool) || i.codexStartedGeneration == "" ||
		i.codexInvalidatingGeneration == i.codexStartedGeneration ||
		i.codexStartedGeneration != i.codexCompletedGeneration ||
		i.codexStartedSessionID == "" ||
		i.codexStartedSessionID != i.codexCompletedSessionID {
		return false
	}
	return i.CodexSessionID != "" && i.codexStartedSessionID == i.CodexSessionID
}

func (i *Instance) shouldBypassCodexWaitingDebounce(derived Status) bool {
	return derived == StatusWaiting && i.codexCompletionConverged()
}

// invalidateCodexCompletionOnRunning makes completion-only proof one-turn
// evidence. Once tmux authoritatively observes newer running activity, turn
// A's completion must not let a later transient waiting sample masquerade as
// completion of turn B. Persist a host-owned consumption marker first, then
// clear the warm instance only after that write succeeds.
// Caller must hold i.mu. The method drops it around all durable coordination.
func (i *Instance) invalidateCodexCompletionOnRunning() {
	if !IsCodexCompatible(i.Tool) || i.codexStartedGeneration == "" ||
		i.codexStartedGeneration != i.codexCompletedGeneration {
		return
	}
	generation := i.codexCompletedGeneration
	i.codexInvalidatingGeneration = generation
	// The durable operation can briefly retry a non-blocking host lock and can
	// perform filesystem I/O. Never expose Instance.mu across either operation.
	i.mu.Unlock()
	consumed, err := consumeCodexCompletionEvidence(i.ID, generation)
	i.mu.Lock()
	if err != nil {
		sessionLog.Debug("consume_codex_completion_evidence_failed",
			slog.String("instance", i.ID),
			slog.String("error", err.Error()),
		)
		return
	}
	if consumed {
		if i.codexStartedGeneration == generation && i.codexCompletedGeneration == generation {
			i.codexStartedGeneration, i.codexCompletedGeneration = "", ""
			i.codexStartedSessionID, i.codexCompletedSessionID = "", ""
		}
		if i.codexInvalidatingGeneration == generation {
			i.codexInvalidatingGeneration = ""
		}
	}
}

// terminatedPaneStatus classifies a session whose tmux pane/session has
// vanished (or gone dead under remain-on-exit) AFTER having been started.
//
// Exit code first: when tmux still holds the dead pane (remain-on-exit — set
// for sandbox sessions, and opt-in via [tmux] options for any session) the
// real process exit code is available. A one-shot worker that finishes and
// exits 0 is a clean shutdown (done, ■ StatusStopped), NOT a crash — reporting
// it as StatusError (✕) makes a successful completion indistinguishable from a
// real failure. A non-zero exit is a genuine crash → StatusError.
//
// No exit code available (pane torn down without remain-on-exit, so tmux
// discarded the exit status) falls back to a per-tool heuristic. For
// hook-emitting tools a vanished pane genuinely means a crash → StatusError.
// OpenCode emits no lifecycle hooks (issue #1617), so an in-session `/exit`
// closes its pane exactly like a crash would; a vanished OpenCode pane is
// overwhelmingly a user-initiated exit → StatusStopped. Error stays reserved
// for a LIVE pane that renders an actual error banner.
//
// NOTE: PaneDeadExitStatus shells out to tmux and can block up to its probe
// timeout. Callers on the UpdateStatus path (which run under i.mu) must NOT use
// this directly when a live tmux session may be queried — use
// applyTerminatedPaneStatus, which drops i.mu around the query. This method is
// safe to call under i.mu only when i.tmuxSession is nil (no subprocess runs).
func (i *Instance) terminatedPaneStatus() Status {
	exitCode, haveExitCode := 0, false
	if i.tmuxSession != nil {
		exitCode, haveExitCode = i.tmuxSession.PaneDeadExitStatus()
	}
	return classifyTerminatedPane(exitCode, haveExitCode, i.Tool)
}

// applyTerminatedPaneStatus writes the terminated-pane classification to
// i.Status without holding i.mu across the (potentially slow) tmux query.
//
// PaneDeadExitStatus can block for the full tmux probe timeout, and every
// UpdateStatus caller runs under i.mu — so querying tmux under the lock lets a
// wedged tmux server stall concurrent lifecycle operations. Instead: snapshot
// the tmux session and tool under the lock, release i.mu for the query, then
// reacquire and apply the result. The caller MUST hold i.mu on entry; i.mu is
// held again on return.
//
// Because the lock is dropped, i.Status may change under us (e.g. a concurrent
// Stop()), so the stopped-state guard is re-evaluated after reacquiring: a
// session the user stopped mid-query must not be clobbered with error/stopped.
func (i *Instance) applyTerminatedPaneStatus() {
	tmuxSession := i.tmuxSession
	tool := i.Tool
	paneDeadExitStatus := i.paneDeadExitStatusForTest

	i.mu.Unlock()
	exitCode, haveExitCode := 0, false
	if tmuxSession != nil {
		if paneDeadExitStatus == nil {
			paneDeadExitStatus = tmuxSession.PaneDeadExitStatus
		}
		exitCode, haveExitCode = paneDeadExitStatus()
	}
	status := classifyTerminatedPane(exitCode, haveExitCode, tool)
	i.mu.Lock()

	if i.Status != StatusStopped {
		i.Status = status
	}
}

// classifyTerminatedPane is the pure decision behind terminatedPaneStatus,
// split out so the clean-exit-vs-crash rule can be exercised without a live
// tmux server. See terminatedPaneStatus for the full rationale.
func classifyTerminatedPane(exitCode int, haveExitCode bool, tool string) Status {
	if haveExitCode {
		if exitCode == 0 {
			return StatusStopped
		}
		return StatusError
	}
	if tool == "opencode" {
		return StatusStopped
	}
	return StatusError
}

func (i *Instance) UpdateStatus() error {
	// #1846: flush any unpersisted last-activity evidence once the lock is
	// released (declared before Lock so it runs after the Unlock defer).
	// Cheap no-op unless the cold-load fold below (or an earlier
	// UpdateHookStatus within the throttle window) left something behind.
	defer i.persistLastActivity(false)
	i.mu.Lock()
	defer i.mu.Unlock()

	// Short grace period for tmux initialization (not Claude startup)
	// Use lastStartTime for accuracy on restarts, fallback to CreatedAt
	graceTime := i.lastStartTime
	if graceTime.IsZero() {
		graceTime = i.CreatedAt
	}
	// 1.5 seconds is enough for tmux to create the session (<100ms typically)
	// Don't block status detection once tmux session exists
	if time.Since(graceTime) < 1500*time.Millisecond {
		// Only skip if tmux session doesn't exist yet
		if i.tmuxSession == nil || !i.tmuxSession.Exists() {
			if i.Status != StatusRunning && i.Status != StatusIdle {
				i.Status = StatusStarting
			}
			return nil
		}
		// Session exists - allow normal status detection below
	}

	if i.tmuxSession == nil {
		if i.neverStarted() {
			// A session that was added but never started has no tmux yet; it is
			// not an error, just not-yet-running. Keep it idle (✕ → ○).
			i.Status = StatusIdle
		} else if i.Status != StatusStopped {
			i.Status = i.terminatedPaneStatus()
			// Was this death a credential failure? If so the status stays error
			// but the substate now says auth-401, and the automatic boot paths
			// hold off (see auth_hold.go).
			i.refreshAuthHoldOnDeathLocked()
		}
		return nil
	}

	// Optimization: Skip expensive Exists() check for sessions already in error/stopped status
	// Ghost sessions (in JSON but not in tmux) only get rechecked every 30 seconds
	// This reduces subprocess spawns from 74/sec to ~5/sec for 28 ghost sessions
	if (i.Status == StatusError || i.Status == StatusStopped) && !i.lastErrorCheck.IsZero() &&
		time.Since(i.lastErrorCheck) < errorRecheckInterval {
		return nil // Skip - still in error/stopped, checked recently
	}

	// Check if tmux session exists
	if !i.tmuxSession.Exists() {
		if i.neverStarted() {
			// Added but never started: no tmux session was ever created, so an
			// absent tmux is expected — classify as idle, not error (✕ → ○).
			i.Status = StatusIdle
		} else {
			// tmux session is non-nil here, so the exit-status probe can block;
			// applyTerminatedPaneStatus drops i.mu for the query and keeps the
			// stopped-state guard on write.
			i.applyTerminatedPaneStatus()
			// Attribute the death to authentication when the pane's last live
			// sample showed a credential banner: the fleet-death case, which no
			// restart can fix (see auth_hold.go). Runs regardless of the
			// exit-code verdict above — a 401 exit can be a clean exit(0), and it
			// still must not be auto-restarted.
			i.refreshAuthHoldOnDeathLocked()
		}
		i.lastErrorCheck = time.Now() // Record when we confirmed error/stopped
		return nil
	}

	// Session exists again (user manually started it) - clear stopped status
	if i.Status == StatusStopped {
		i.Status = StatusRunning
	}

	// Session exists - clear error check timestamp
	i.lastErrorCheck = time.Time{}

	// Tiered polling: skip expensive checks for idle sessions with no new activity
	if i.Status == StatusIdle {
		currentTS := i.tmuxSession.GetCachedWindowActivity()
		if currentTS == i.lastKnownActivity && !i.lastIdleCheck.IsZero() &&
			time.Since(i.lastIdleCheck) < 10*time.Second {
			return nil // No activity detected, skip full check
		}
		// Activity detected OR recheck interval passed: do full check
		i.lastIdleCheck = time.Now()
		i.lastKnownActivity = currentTS
	}

	// COLD LOAD: CLI doesn't run StatusFileWatcher, so hookStatus is always empty.
	// Read the hook file from disk once to give CLI the same fast path as the TUI.
	if i.hookStatus == "" && (IsClaudeCompatible(i.Tool) || IsCodexCompatible(i.Tool) || i.Tool == "gemini" || i.Tool == "hermes" || i.Tool == "cursor") {
		if hs := readHookStatusFile(i.ID); hs != nil {
			i.hookStatus = hs.Status
			i.hookEvent = hs.Event
			i.hookLastUpdate = hs.UpdatedAt
			i.hookSessionID = hs.SessionID
			// #1846: a disk-read hook sample is activity evidence too.
			// Flushed by this function's persistLastActivity defer.
			i.noteAgentActivityLocked(hs.UpdatedAt)
			i.setCodexGenerationEvidence(hs)
			// Reset stale acknowledged flag from ReconnectSessionLazy.
			// Without this, sessions loaded from SQLite with previousStatus="idle"
			// would report idle even when the hook file says waiting/running.
			if i.tmuxSession != nil && (hs.Status == "running" || hs.Status == "waiting") {
				i.tmuxSession.ResetAcknowledged()
			}
		}
	}

	// HOOK FAST PATH: hook-based status for tools that emit lifecycle events.
	// Freshness is tool- and state-specific (e.g. Codex running vs waiting).
	// When this path is stale/missing, control naturally falls through to tmux
	// polling and tool-specific session sync (tmux env/process-files/disk).
	if (IsClaudeCompatible(i.Tool) || IsCodexCompatible(i.Tool) || i.Tool == "gemini" || i.Tool == "hermes" || i.Tool == "cursor") &&
		i.hookStatus != "" &&
		time.Since(i.hookLastUpdate) < hookFastPathFreshnessForTool(i.Tool, i.hookStatus) {
		switch i.hookStatus {
		case "starting":
			i.Status = StatusStarting
		case "running":
			i.Status = StatusRunning
			// Reset acknowledged: new activity means output not yet seen.
			// Without this, a previously-acknowledged session would go straight
			// to idle (gray) after Stop, skipping the waiting (orange) state.
			if i.tmuxSession != nil {
				i.tmuxSession.ResetAcknowledged()
			}
		case "waiting":
			if IsCodexCompatible(i.Tool) {
				// Codex completion should surface as attention-needed.
				// Keep this as waiting and let tmux settle to idle if the user
				// has acknowledged and no new activity appears.
				if i.tmuxSession != nil {
					i.tmuxSession.ResetAcknowledged()
				}
				i.Status = StatusWaiting
			} else {
				// Claude fires its Stop hook (→ "waiting") when the FOREGROUND turn
				// ends, even while run_in_background shells or a background agent the
				// turn is awaiting keep running. Treat the session as still running
				// so it stays green and the daemon emits no premature "finished"
				// notification; it settles to waiting (and notifies) once the
				// background work completes — so "done" means foreground AND
				// background. BackgroundWorkPending captures the pane (the fast path
				// has no captured content), so release i.mu around it like the
				// GetStatus call below, then re-check for a concurrent Kill().
				bgWorkPending := false
				if i.tmuxSession != nil && IsClaudeCompatible(i.Tool) {
					i.mu.Unlock()
					bgWorkPending = i.tmuxSession.BackgroundWorkPending()
					i.mu.Lock()
					if i.Status == StatusStopped {
						return nil
					}
				}
				switch {
				case bgWorkPending:
					i.Status = StatusRunning
				case i.tmuxSession != nil && i.tmuxSession.IsAcknowledged():
					// Check acknowledgment: orange (waiting) vs gray (idle).
					// Acknowledge() is called when user attaches to a session.
					// ResetAcknowledged() is called by UpdateHookStatus on any new
					// waiting event, and by the u key / new activity.
					i.Status = StatusIdle
				default:
					i.Status = StatusWaiting
				}
			}
		case "dead":
			i.Status = StatusError
		}
		if i.hookSessionID != "" {
			switch {
			case IsClaudeCompatible(i.Tool):
				if i.hookSessionID != i.ClaudeSessionID {
					i.ClaudeSessionID = i.hookSessionID
					// #1815: own hook anchor — verified ownership.
					i.markClaudeSessionIDVerified()
					i.ClaudeDetectedAt = time.Now()
				} else {
					// Review finding on #1830: the values already matching is
					// NOT a no-op case for the taint. A live hook confirming
					// the session's own id is the strongest available vouch
					// (the process itself, not a disk guess) — clear any
					// lingering disk-scan taint on it, or a solo custom-wrapper
					// session whose own id was once disk-scanned stays
					// permanently unresumable even though this hook just
					// proved it right.
					i.markClaudeSessionIDVerified()
				}
			case IsCodexCompatible(i.Tool):
				if i.hookSessionID != i.CodexSessionID {
					i.CodexSessionID = i.hookSessionID
					i.CodexDetectedAt = time.Now()
				}
			case i.Tool == "gemini":
				if i.hookSessionID != i.GeminiSessionID {
					i.GeminiSessionID = i.hookSessionID
					i.GeminiDetectedAt = time.Now()
				}
			}
		}
		// A1: For Hermes, run the gateway reachability check even on the fast path.
		// Without this, a dead gateway can still report running/waiting for the full
		// hook freshness window because the check below is skipped.
		// Use GetHermesGatewayURL() so the common auto-discovery setup (no explicit
		// [hermes].gateway_url in config) still gets gateway-health degradation —
		// reading config.Hermes.GatewayURL directly would skip the discovery path
		// via ~/.hermes/gateway_state.json.
		if i.Tool == "hermes" && (i.Status == StatusRunning || i.Status == StatusWaiting) {
			if gatewayURL := GetHermesGatewayURL(); gatewayURL != "" {
				if time.Since(i.hermesGatewayCheckedAt) > 30*time.Second {
					i.mu.Unlock()
					reachable := IsHermesGatewayReachable(gatewayURL)
					i.mu.Lock()
					// Mirror the stale-stop guard from the tmux path: a concurrent
					// Kill() may have published StatusStopped while we were unlocked.
					if i.Status == StatusStopped {
						return nil
					}
					i.hermesGatewayCheckedAt = time.Now()
					i.hermesGatewayOK = reachable
				}
				if !i.hermesGatewayOK {
					i.Status = StatusError
				}
			}
		}
		// A healthy hook sample proves the credential works again, so release any
		// auth hold. Needed here as well as on the tmux path: a recovered session
		// reports through the hook fast path and returns before the tmux-derived
		// reconciliation below would ever run.
		i.releaseAuthHoldIfHealthyLocked()
		return nil
	}

	// SSE FAST PATH (issue #1614): OpenCode publishes session.status over its
	// /event stream when launched with --port; OpenCodeSSEWatcher feeds the
	// derived status here via UpdateOpenCodeSSEStatus. When the stream drops,
	// the status ages past opencodeSSEFreshnessWindow and control falls
	// through to tmux content sniffing — the same degradation model as hooks.
	if i.Tool == "opencode" && i.sseStatus != "" &&
		time.Since(i.sseLastUpdate) < opencodeSSEFreshnessWindow {
		switch i.sseStatus {
		case "running":
			i.Status = StatusRunning
			// New activity means output not yet seen (mirrors hook fast path).
			if i.tmuxSession != nil {
				i.tmuxSession.ResetAcknowledged()
			}
			i.releaseAuthHoldIfHealthyLocked()
			return nil
		case "waiting":
			if i.tmuxSession != nil && i.tmuxSession.IsAcknowledged() {
				i.Status = StatusIdle
			} else {
				i.Status = StatusWaiting
			}
			i.releaseAuthHoldIfHealthyLocked()
			return nil
		}
	}

	// Release lock for potentially slow tmux calls (GetStatus calls CapturePane)
	i.mu.Unlock()
	status, err := i.tmuxSession.GetStatus()
	i.mu.Lock()

	// Issue #953: a concurrent Kill() may have published StatusStopped
	// while we were unlocked for the GetStatus call above. Honoring a
	// stale tmux-derived status now would clobber the user-initiated
	// stop with idle/running/error and the next render would show the
	// wrong icon (the original v1.9.20 user-visible symptom).
	if i.Status == StatusStopped {
		return nil
	}

	// Prior status, captured before this tmux-derived sample overwrites it, so the
	// debounce below can tell a flip AWAY from running from a steady state.
	prevStatus := i.Status

	if err != nil {
		// Debounce a transient capture failure: subprocess churn can make a single
		// CapturePane fail, then recover. Hold at running for one sample rather than
		// firing a false error. Modeled as a derived error with no tmux raw status.
		if apply, nextPending, held := debounceFlipFromRunning(prevStatus, StatusError, "", i.hookStatus, i.tmuxFlipFromRunningPending); held {
			i.tmuxFlipFromRunningPending = nextPending
			i.Status = apply
			return nil
		}
		i.tmuxFlipFromRunningPending = false
		i.Status = StatusError
		return err
	}

	// Map tmux status to instance status
	switch status {
	case "active":
		i.Status = StatusRunning
	case "waiting":
		// tmux reports a shell prompt ("waiting"), but a non-interactive foreground
		// process may still be running (e.g. "yarn dev", "mvn spring-boot:run").
		// shellForegroundRunning() inspects the cached pane command to tell them apart.
		if i.Tool == "shell" {
			if i.shellForegroundRunning() {
				i.Status = StatusRunning
			} else {
				i.Status = StatusIdle
			}
		} else {
			i.Status = StatusWaiting
		}
	case "idle":
		// Acknowledged shell sessions can still have a foreground process running
		// even after the user has attached; keep surfacing that as running.
		if i.Tool == "shell" && i.shellForegroundRunning() {
			i.Status = StatusRunning
		} else {
			i.Status = StatusIdle
		}
	case "starting":
		i.Status = StatusStarting
	case "error":
		// Pane shows a tool-rendered error banner (#1400): auth failure
		// ("API Error: 401" / "Please run /login") or a dead connection
		// ("socket connection closed"). The process is alive but cannot make
		// progress without user action — report error, not waiting.
		i.Status = StatusError
	case "inactive":
		// Pane is dead/gone. terminatedPaneStatus prefers the real exit code
		// when remain-on-exit still holds the dead pane — a clean exit 0 reads
		// as stopped (■), a non-zero exit or an unknowable exit falls back to a
		// per-tool heuristic (crash → ✕ for hook tools, clean `/exit` → ■ for
		// OpenCode). #1617, and clean one-shot completions. The probe can block,
		// so applyTerminatedPaneStatus runs it without holding i.mu.
		i.applyTerminatedPaneStatus()
	default:
		i.Status = StatusError
	}
	if i.Status == StatusRunning {
		i.invalidateCodexCompletionOnRunning()
	}

	// Reconcile the auth hold with this sample. Runs after the status mapping so
	// it sees the settled verdict, and before the debounce so a held session's
	// substate is already correct when the debounce returns early.
	i.reconcileAuthHoldLocked(status)

	// Debounce a purely tmux-inferred flip away from running (see
	// tmuxFlipFromRunningPending). A long single tool-call past the hook freshness
	// window can momentarily present the pane as a shell prompt ("waiting") or a
	// transient error, then recover; one confirming sample prevents a false
	// completion/error to the conductor. A genuinely dead pane (tmux "inactive")
	// and a "dead" hook are NOT debounced — those are real terminal signals.
	// Skip debounce for tools without hooks (pi, shell): their tmux status is
	// the ground truth and there's no hook fast-path to race against. Without
	// this skip, each fresh CLI invocation (e.g. `agent-deck list --json`) sees
	// tmuxFlipFromRunningPending = false and holds the status at running on the
	// first sample, then exits before the second confirming sample can fire.
	bypassWaitingDebounce := i.shouldBypassCodexWaitingDebounce(i.Status)
	if shouldDebounceTmuxFlipForTool(i.Tool) && !bypassWaitingDebounce {
		if apply, nextPending, held := debounceFlipFromRunning(prevStatus, i.Status, status, i.hookStatus, i.tmuxFlipFromRunningPending); held {
			i.tmuxFlipFromRunningPending = nextPending
			i.Status = apply
			return nil
		}
		// Confirmed flip (second consecutive sample) or a non-debounceable outcome:
		// clear the marker so a later genuine flip starts a fresh debounce.
		i.tmuxFlipFromRunningPending = false
	} else {
		i.tmuxFlipFromRunningPending = false
	}

	// Hermes: augment status with gateway health when a gateway URL is resolvable.
	// Check is throttled to 30s to avoid 1.5s HTTP delays on every status tick.
	// Use GetHermesGatewayURL() so the auto-discovery path (gateway_state.json +
	// loopback probe) gets the same degradation behavior as an explicit config
	// override — without this, users on the documented-easy setup never see a
	// dead gateway flip them to StatusError.
	if i.Tool == "hermes" && i.Status != StatusStopped && i.Status != StatusError {
		if gatewayURL := GetHermesGatewayURL(); gatewayURL != "" {
			if time.Since(i.hermesGatewayCheckedAt) > 30*time.Second {
				// A2: A concurrent Kill() may publish StatusStopped while we are
				// unlocked for the HTTP probe; re-check after reacquiring the lock
				// and skip the write to avoid clobbering the stop.
				i.mu.Unlock()
				reachable := IsHermesGatewayReachable(gatewayURL)
				i.mu.Lock()
				if i.Status == StatusStopped {
					return nil
				}
				i.hermesGatewayCheckedAt = time.Now()
				i.hermesGatewayOK = reachable
			}
			if !i.hermesGatewayOK {
				i.Status = StatusError
			}
		}
	}

	// Update tool detection dynamically (enables fork when wrapped tools start).
	// Only built-in tool identities are rewritten here. Custom tools like
	// "my-codex" should keep their configured identity even when tmux correctly
	// detects the wrapped CLI as Codex.
	if detectedTool := i.tmuxSession.DetectTool(); detectedTool != "" {
		if !isBuiltinToolName(i.Tool) && GetToolDef(i.Tool) != nil {
			// Preserve configured custom tool names.
		} else {
			switch detectedTool {
			case "claude", "gemini", "opencode", "codex":
				i.Tool = detectedTool
			case "shell":
				switch i.Tool {
				case "", "shell", "claude", "gemini", "opencode", "codex":
					i.Tool = detectedTool
				}
			}
		}
	}

	// Update session metadata tracking only for active/waiting sessions.
	// This path can perform filesystem and tmux env reads while i.mu is held, so
	// rate-limit it to reduce intermittent render/key handling stalls under load.
	if i.Status == StatusRunning || i.Status == StatusWaiting {
		interval := 2 * time.Second
		// Bootstrap unknown IDs faster for newly-started sessions.
		switch {
		case IsClaudeCompatible(i.Tool):
			if i.ClaudeSessionID == "" {
				interval = 500 * time.Millisecond
			}
		case i.Tool == "gemini":
			if i.GeminiSessionID == "" {
				interval = 500 * time.Millisecond
			}
		case IsCodexCompatible(i.Tool):
			if i.CodexSessionID == "" {
				interval = 500 * time.Millisecond
			}
		}
		if i.lastSessionMetaSync.IsZero() || time.Since(i.lastSessionMetaSync) >= interval {
			i.lastSessionMetaSync = time.Now()

			// Update Claude session tracking (non-blocking, best-effort)
			i.UpdateClaudeSession(nil)

			// Update Gemini session tracking (non-blocking, best-effort)
			if i.Tool == "gemini" {
				i.UpdateGeminiSession(nil)
			}

			// Update Codex session tracking (non-blocking, best-effort)
			if IsCodexCompatible(i.Tool) {
				// Always collect other instances' session IDs to prevent the
				// disk scan from assigning a session that belongs to another
				// instance. Without this, instances that share the same
				// project_path can all claim the same Codex session file.
				exclude := i.collectOtherCodexSessionIDs()
				i.UpdateCodexSession(exclude)
			}

			// Update OpenCode session tracking (non-blocking, best-effort).
			// The opencode CLI subprocess can take seconds and must not run
			// under i.mu or it starves render-path RLocks and freezes the TUI.
			// updateOpenCodeSession manages its own locking internally — we
			// drop i.mu here and reacquire after it returns.
			if i.Tool == "opencode" {
				i.mu.Unlock()
				i.UpdateOpenCodeSession()
				i.mu.Lock()
			}
		}
	}

	return nil
}

// UpdateClaudeSession updates the Claude session ID from tmux environment.
// The capture-resume pattern (used in Start/Fork/Restart) sets CLAUDE_SESSION_ID
// in the tmux environment, making this the single authoritative source.
//
// No file scanning fallback - we rely on the consistent capture-resume pattern.
func (i *Instance) UpdateClaudeSession(excludeIDs map[string]bool) {
	if !IsClaudeCompatible(i.Tool) {
		return
	}

	// Read from tmux environment (set by capture-resume pattern)
	if sessionID := i.GetSessionIDFromTmux(); sessionID != "" {
		if i.ClaudeSessionID != sessionID {
			rejected := false
			// Quality gate: don't adopt a zombie ID from tmux env when current has real data
			if i.ClaudeSessionID != "" {
				currentHasData := sessionHasConversationData(i, i.ClaudeSessionID)
				candidateHasData := sessionHasConversationData(i, sessionID)
				if currentHasData && !candidateHasData {
					sessionLog.Debug("claude_session_tmux_rejected_zombie",
						slog.String("current_id", i.ClaudeSessionID),
						slog.String("zombie_id", sessionID),
						slog.String("reason", "tmux_env_has_zombie_id"),
					)
					_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
						InstanceID: i.ID, Tool: i.Tool, Action: "reject",
						Source: "tmux_env", OldID: i.ClaudeSessionID, Candidate: sessionID,
						Reason: "zombie_id_no_conversation_data",
					})
					// Don't adopt the zombie; skip the update but still refresh prompt below
					rejected = true
					sessionID = i.ClaudeSessionID
				}
			}
			if !rejected {
				action := "bind"
				if i.ClaudeSessionID != "" {
					action = "rebind"
				}
				_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
					InstanceID: i.ID, Tool: i.Tool, Action: action,
					Source: "tmux_env", OldID: i.ClaudeSessionID, NewID: sessionID,
				})
				i.ClaudeSessionID = sessionID
				// #1815: CLAUDE_SESSION_ID read from this instance's OWN
				// tmux pane is a WEAK vouch (the env is downstream of us).
				i.noteClaudeSessionIDFromOwnPane()
			}
		}
		i.ClaudeDetectedAt = time.Now()
	}

	// Update latest prompt from JSONL file (tail-read with size caching)
	if i.ClaudeSessionID != "" {
		jsonlPath := i.GetJSONLPath()
		if jsonlPath != "" {
			if prompt := i.readJSONLTail(jsonlPath); prompt != "" {
				i.LatestPrompt = prompt
			}
		}
	}
}

// syncClaudeSessionFromDisk is a legacy shim kept for compatibility.
// Disk scan is intentionally NOT authoritative for session identity.
// Session ID binding must come from tmux env and/or hook session anchor.
func (i *Instance) syncClaudeSessionFromDisk() {
	if !IsClaudeCompatible(i.Tool) {
		return
	}
	sessionLog.Debug("claude_session_disk_scan_disabled",
		slog.String("instance", i.ID),
		slog.String("reason", "disk_scan_not_authoritative"),
	)
	_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
		InstanceID: i.ID, Tool: i.Tool, Action: "scan_disabled",
		Source: "disk_scan", Reason: "disk_scan_not_authoritative",
	})
}

// UpdateHookStatus updates the instance's hook-based status fields.
// Called by StatusFileWatcher when a hook status file changes.
func (i *Instance) UpdateHookStatus(status *HookStatus) {
	if status == nil {
		return
	}

	i.mu.Lock()
	// Issue #1846: whatever hookLastUpdate ends up COMMITTED when this call
	// returns is agent-activity evidence — fold it into the durable record.
	// The reject branches below restore the pre-event value first, so a
	// foreign ephemeral's timestamp is never credited to this instance.
	// Defer order (LIFO): the fold runs under the lock, then the lock is
	// released, then the throttled SQLite flush runs OUTSIDE it — the DB
	// write can stall on SQLITE_BUSY and must not hold up i.mu readers.
	defer i.persistLastActivity(false)
	defer i.mu.Unlock()
	defer func() { i.noteAgentActivityLocked(i.hookLastUpdate) }()

	// Snapshot the prior hook-status fields so a candidate that fails the
	// ownership check below can RESTORE them rather than leaving its status
	// applied. This closes the `claude -p` env-pollution flip: a foreign
	// ephemeral session (a `claude -p` child that inherited our
	// AGENTDECK_INSTANCE_ID and fired hooks under our id) has no conversation
	// data, so the bind is rejected — but previously the running/waiting status
	// it carried had already been written here and stuck, flipping this
	// instance's status. See the candidate_has_no_conversation_data branch.
	prevHookStatus, prevHookEvent, prevHookLastUpdate := i.hookStatus, i.hookEvent, i.hookLastUpdate
	prevStartedGen, prevCompletedGen := i.codexStartedGeneration, i.codexCompletedGeneration
	prevStartedSID, prevCompletedSID := i.codexStartedSessionID, i.codexCompletedSessionID
	restoreHook := func() {
		i.hookStatus, i.hookEvent, i.hookLastUpdate = prevHookStatus, prevHookEvent, prevHookLastUpdate
		i.codexStartedGeneration, i.codexCompletedGeneration = prevStartedGen, prevCompletedGen
		i.codexStartedSessionID, i.codexCompletedSessionID = prevStartedSID, prevCompletedSID
	}

	// Detect whether this is genuinely new data (newer timestamp than last seen).
	// Only reset acknowledgment on new events — not on re-application of the same
	// stale hook file, which would undo the user's intentional acknowledge.
	isNewEvent := status.UpdatedAt.After(i.hookLastUpdate)

	i.hookStatus = status.Status
	i.hookEvent = status.Event
	i.hookLastUpdate = status.UpdatedAt
	i.setCodexGenerationEvidence(status)

	// Permission-type events are always attention-needed, even if the user
	// previously acknowledged this session. A mid-task permission block is new
	// activity that the user must respond to — unlike Stop (task complete) which
	// can stay grey if already seen.
	// Handles both PermissionRequest events and Notification/permission_prompt.
	if isNewEvent && status.Status == "waiting" && i.tmuxSession != nil {
		if status.Event == "PermissionRequest" || status.Event == "Notification" {
			i.tmuxSession.ResetAcknowledged()
		}
	}

	// Issue #1349 defense-in-depth #1: never bind a session id from a terminal
	// hook event (e.g. SessionEnd). The status/event/ack bookkeeping above still
	// applies, but a terminal payload's session_id is stale by definition and
	// must not become a bind source — that is exactly what re-binds a
	// stopped/removed session every poll cycle and collides session ids.
	//
	// Accepted tradeoff: if the daemon's first-ever observation of a session is
	// a SessionEnd (e.g. it was down during the SessionStart/UserPromptSubmit
	// edges and only the latest event survives in the hook file), the bind is
	// skipped and the prior ClaudeSessionID stays. That is the correct call —
	// the session is already gone, so binding its (possibly reused) id onto a
	// now-dead instance is the corruption we are preventing, not a feature.
	if isTerminalHookEvent(status.Event) {
		return
	}

	// Resolve session ID from hook payload first, then sidecar anchor.
	sessionID := strings.TrimSpace(status.SessionID)
	hookSource := "hook_payload"
	if sessionID == "" {
		sessionID = ReadHookSessionAnchor(i.ID)
		hookSource = "hook_anchor"
	}
	if sessionID == "" {
		return
	}

	switch {
	case IsClaudeCompatible(i.Tool):
		if sessionID == i.ClaudeSessionID {
			// Review finding on #1830: same rationale as the sibling equality
			// branch above — a live hook payload reporting this instance's
			// own recorded id is the strongest available vouch, and must
			// clear any lingering disk-scan taint on it rather than being
			// treated as a no-op.
			i.markClaudeSessionIDVerified()
			return
		}
		// Issue #1729 guard: a candidate whose hook-reported cwd is provably
		// outside every path this instance owns is a foreign ephemeral — the
		// classic case is a headless `claude -p --no-session-persistence`
		// worker (spawned by a plugin with cwd=$TMPDIR) that inherited our
		// AGENTDECK_INSTANCE_ID from the tmux pane environment. Such
		// candidates must never influence instance state: no bind (including
		// cold start, which previously accepted ANY first candidate), no
		// rebind, and no status flip (restore the pre-event fields). An empty
		// cwd (legacy hook files, agents that send none) is not evidence and
		// never blocks.
		if i.hookCwdIsForeign(status.Cwd) {
			restoreHook()
			_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
				InstanceID: i.ID, Tool: i.Tool, Action: "reject",
				Source: hookSource, OldID: i.ClaudeSessionID, Candidate: sessionID,
				HookEvent: status.Event, Reason: "candidate_cwd_outside_instance_paths",
			})
			return
		}
		// Cold start — no session bound yet. Accept the first candidate
		// unconditionally; there is nothing to protect.
		if i.ClaudeSessionID == "" {
			i.bindClaudeSessionFromHook(sessionID, hookSource, status.Event, "bind")
			return
		}
		// v1.7.7 guard: candidate must have any conversation data at all.
		if !sessionHasConversationData(i, sessionID) {
			// A different session id with NO conversation data on an established
			// instance is a foreign ephemeral (a `claude -p` child that inherited
			// our AGENTDECK_INSTANCE_ID) — it doesn't own this instance, so its
			// status must not stick either. Restore the pre-event status so the
			// foreign hook is a no-op, not a flip. (A real /clear or fork carries
			// conversation data and never reaches this branch.)
			restoreHook()
			_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
				InstanceID: i.ID, Tool: i.Tool, Action: "reject",
				Source: hookSource, OldID: i.ClaudeSessionID, Candidate: sessionID,
				HookEvent: status.Event, Reason: "candidate_has_no_conversation_data",
			})
			return
		}
		// v1.7.23 guard (issue #661): when BOTH current and candidate have
		// data, the candidate must have strictly MORE content to win. This
		// stops the UserPromptSubmit flap where a fresh 1-record jsonl
		// overwrites a rich hundreds-of-KB historic jsonl on every restart.
		// Byte size is a robust proxy for "how much history this session
		// holds" — immune to record-count ties and faster than re-scanning
		// the file.
		//
		// Issue #856: but a strict size-only rule rejects user-initiated
		// new sessions (e.g. /clear) indefinitely, since they're smaller by
		// definition. Mtime gap is the discriminator: in a flap the user
		// keeps typing into the rich session so its mtime stays fresh; in
		// /clear the user abandons the old session, so its mtime stales
		// while the new jsonl's mtime advances. If the candidate's jsonl
		// is significantly newer than the current's (clearRebindMtimeGrace),
		// treat it as a user-initiated new session and rebind regardless
		// of size.
		if sessionHasConversationData(i, i.ClaudeSessionID) {
			currentSize := sessionConversationByteSize(i, i.ClaudeSessionID)
			candidateSize := sessionConversationByteSize(i, sessionID)
			if candidateSize <= currentSize {
				currentMtime := sessionConversationMtime(i, i.ClaudeSessionID)
				candidateMtime := sessionConversationMtime(i, sessionID)
				clearRebind := !currentMtime.IsZero() && !candidateMtime.IsZero() &&
					candidateMtime.Sub(currentMtime) >= clearRebindMtimeGrace
				if !clearRebind {
					_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
						InstanceID: i.ID, Tool: i.Tool, Action: "reject",
						Source: hookSource, OldID: i.ClaudeSessionID, Candidate: sessionID,
						HookEvent: status.Event, Reason: "candidate_has_less_conversation_data",
					})
					return
				}
			}
		}
		i.bindClaudeSessionFromHook(sessionID, hookSource, status.Event, "rebind")
	case IsCodexCompatible(i.Tool):
		if sessionID == i.CodexSessionID {
			return
		}
		// Quality gate (incident 2026-07-15): codex subagent threads fire
		// the same agent-turn-complete notify as the main thread, and a
		// completing subagent's payload id would otherwise usurp the
		// binding. Restarting then resumes a finalized child thread, which
		// refuses turn/start and error-loops the session. See
		// codex_subagent_gate.go.
		if i.shouldRejectCodexSubagentRebind(sessionID) {
			_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
				InstanceID: i.ID, Tool: i.Tool, Action: "reject",
				Source: hookSource, OldID: i.CodexSessionID, Candidate: sessionID,
				HookEvent: status.Event, Reason: "candidate_is_subagent_thread",
			})
			sessionLog.Debug("codex_session_rebind_rejected_subagent",
				slog.String("old_id", i.CodexSessionID),
				slog.String("candidate", sessionID),
				slog.String("event", status.Event),
			)
			return
		}
		i.bindCodexSessionFromHook(sessionID, status.Event)
	case i.Tool == "gemini":
		if sessionID == i.GeminiSessionID {
			return
		}
		// Quality gate: only accept when candidate session appears valid on disk,
		// OR when current session is empty (first detection/bootstrap).
		if i.GeminiSessionID == "" || geminiSessionHasConversationData(sessionID, i.ProjectPath) {
			i.bindGeminiSessionFromHook(sessionID, status.Event)
		}
	}
}

// bindCodexSessionFromHook is the Codex counterpart of
// bindClaudeSessionFromHook (see that function's doc comment for the
// PERSIST-12 rationale). It performs the same bookkeeping that the
// inlined pre-#1139 code did — debug log, in-memory mutation, tmux env
// propagation — and then persists the new binding to SQLite so
// DB-direct consumers and peer agent-deck processes observe the new
// codex_session_id immediately, instead of reloading the stale row and
// clobbering the in-memory mutation on the next save cycle.
func (i *Instance) bindCodexSessionFromHook(sessionID, hookEvent string) {
	sessionLog.Debug("codex_session_update_from_hook",
		slog.String("old_id", i.CodexSessionID),
		slog.String("new_id", sessionID),
		slog.String("event", hookEvent),
	)
	i.CodexSessionID = sessionID
	i.CodexDetectedAt = time.Now()
	i.hookSessionID = sessionID

	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		_ = i.tmuxSession.SetEnvironment("CODEX_SESSION_ID", sessionID)
	}

	// Persist the rebind to SQLite. See bindClaudeSessionFromHook for the
	// full rationale: none of the three UpdateHookStatus callers (TUI
	// tick, web refresh, CLI status refresh) save after a hook-triggered
	// rebind, so tool_data.codex_session_id stays pinned at the stale
	// UUID indefinitely for DB-direct consumers, and peer processes
	// holding stale snapshots keep clobbering the in-memory mutation —
	// producing a runaway loop of fresh "rebind" decisions on every
	// poll. WriteCodexSessionBinding rewrites only the typed schema
	// fields via json_set, leaving every other tool_data key untouched.
	if db := statedb.GetGlobal(); db != nil {
		if err := db.WriteCodexSessionBinding(i.ID, sessionID, i.CodexDetectedAt); err != nil {
			sessionLog.Warn("codex_session_rebind_persist_failed",
				slog.String("instance_id", i.ID),
				slog.String("new_id", sessionID),
				slog.String("error", err.Error()))
		}
	}
}

// bindGeminiSessionFromHook is the Gemini counterpart of
// bindClaudeSessionFromHook. See that function's doc comment for the
// PERSIST-12 rationale. The quality gate (GeminiSessionID == "" ||
// geminiSessionHasConversationData(...)) is enforced by the caller in
// UpdateHookStatus before this function is invoked, mirroring the
// invariant the inlined pre-#1139 code preserved.
func (i *Instance) bindGeminiSessionFromHook(sessionID, hookEvent string) {
	sessionLog.Debug("gemini_session_update_from_hook",
		slog.String("old_id", i.GeminiSessionID),
		slog.String("new_id", sessionID),
		slog.String("event", hookEvent),
	)
	i.GeminiSessionID = sessionID
	i.GeminiDetectedAt = time.Now()
	i.hookSessionID = sessionID

	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		_ = i.tmuxSession.SetEnvironment("GEMINI_SESSION_ID", sessionID)
	}

	// Persist the rebind to SQLite. See bindClaudeSessionFromHook for
	// the full rationale on why the in-memory mutation alone is not
	// enough: the bug pattern (#1138 for Claude, #1139 for
	// Codex/Gemini) is that UpdateHookStatus callers don't call Save
	// afterwards, so peer agent-deck processes keep reloading the stale
	// row and clobbering this instance's in-memory state. The targeted
	// json_set UPDATE atomically rewrites only $.gemini_session_id and
	// $.gemini_detected_at, preserving the rest of tool_data.
	if db := statedb.GetGlobal(); db != nil {
		if err := db.WriteGeminiSessionBinding(i.ID, sessionID, i.GeminiDetectedAt); err != nil {
			sessionLog.Warn("gemini_session_rebind_persist_failed",
				slog.String("instance_id", i.ID),
				slog.String("new_id", sessionID),
				slog.String("error", err.Error()))
		}
	}
}

// GetHookStatus returns the current hook-based status and its freshness.
// Freshness window is tool-specific.
func (i *Instance) GetHookStatus() (string, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()

	if i.hookStatus == "" {
		return "", false
	}
	fresh := time.Since(i.hookLastUpdate) < hookFastPathFreshnessForTool(i.Tool, i.hookStatus)
	return i.hookStatus, fresh
}

// LastHookActivityTime returns the on-disk hook status file's UpdatedAt
// timestamp for this instance, and true when a hook sample has been
// recorded. Unlike LastObservedActivity's process-local tmux tracker, this
// is populated even by a cold, short-lived CLI process: UpdateStatus's
// "COLD LOAD" branch reads the sidecar hook file straight off disk before
// this can be called. That makes it durable cross-process evidence of real
// activity, which status --stale needs (issue #1704 dual-review finding):
// a fresh CLI invocation never gets to observe LastObservedActivity, so
// without this a session that just finished real work and has no
// LastAccessedAt (never attached in the TUI) would misreport its
// last-activity age against CreatedAt instead of the moment it actually
// went idle/waiting.
func (i *Instance) LastHookActivityTime() (time.Time, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()

	if i.hookStatus == "" || i.hookLastUpdate.IsZero() {
		return time.Time{}, false
	}
	return i.hookLastUpdate, true
}

// GetAutoNameDescription returns the last captured Claude task description for
// an AutoName session (empty if none captured yet). Thread-safe.
func (i *Instance) GetAutoNameDescription() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.autoNameDescription
}

// GetAutoName reports whether this session should display a captured/live task
// description instead of its machine-generated handle. Thread-safe.
func (i *Instance) GetAutoName() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.AutoName
}

// SetAutoName updates whether this session should display a captured/live task
// description instead of its machine-generated handle. Thread-safe.
func (i *Instance) SetAutoName(autoName bool) {
	i.mu.Lock()
	i.AutoName = autoName
	i.mu.Unlock()
}

// SetAutoNameDescription records the latest Claude task description for an
// AutoName session so it can be persisted and shown on reopen. Thread-safe.
func (i *Instance) SetAutoNameDescription(desc string) {
	i.mu.Lock()
	if strings.TrimSpace(desc) != "" {
		i.autoNameDescription = desc
	}
	i.mu.Unlock()
}

// ClearHookStatus resets the hook-based status and removes the persisted hook
// record, forcing the next UpdateStatus() to fall through to polling. Used
// when the user manually overrides status (e.g., pressing 'u' to unacknowledge
// after an Escape interrupt where the Stop hook didn't fire).
func (i *Instance) ClearHookStatus() {
	i.mu.Lock()
	// #1846: the file removed below is the only durable record of this
	// session's last real activity, and the in-memory copy is zeroed right
	// after. Fold it into the durable last-activity record first.
	i.noteAgentActivityLocked(i.hookLastUpdate)
	i.hookStatus = ""
	i.hookLastUpdate = time.Time{}
	i.mu.Unlock()
	// Flush BEFORE removing the file below, bypassing the write throttle —
	// this evidence has no other way to survive. Outside i.mu: the SQLite
	// write can stall on SQLITE_BUSY (see persistLastActivity).
	i.persistLastActivity(true)
	if i.Tool == "hermes" {
		i.clearHermesHookStatuses()
		return
	}

	// Remove the persisted status file. Sandbox sessions bridge a PER-INSTANCE
	// scoped subdir (…/hooks/sandbox/<id>/<id>.json) from the container, and the
	// watcher attributes that file to this instance by its OWNING SUBDIR, so the
	// scoped file is the one to clear. Non-sandbox sessions write the flat
	// …/hooks/<id>.json. We remove only the FILE here (not the subdir): this can
	// fire mid-session (attach-return / unacknowledge) while the container still
	// has the subdir bind-mounted, and unlinking the mount source would orphan
	// the live bridge. The subdir + its fsnotify watch are torn down at session
	// end (see killInternal).
	hookPath := filepath.Join(GetHooksDir(), i.ID+".json")
	if i.IsSandboxed() {
		hookPath = filepath.Join(GetHooksDir(), "sandbox", i.ID, i.ID+".json")
	}
	if err := os.Remove(hookPath); err != nil && !os.IsNotExist(err) {
		sessionLog.Debug("clear_hook_status_file_failed",
			slog.String("instance", i.ID),
			slog.String("path", hookPath),
			slog.String("error", err.Error()),
		)
	}
}

// ForceNextStatusCheck clears the idle polling optimization so the next
// UpdateStatus() performs a full check instead of short-circuiting.
// Call this before UpdateStatus() when a status-affecting change was made
// externally (e.g. the u key toggling acknowledged state).
func (i *Instance) ForceNextStatusCheck() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.lastIdleCheck = time.Time{}
}

// SetGeminiYoloMode sets the YOLO mode for Gemini and syncs it to the tmux environment.
// This ensures the background status worker sees the correct state during restarts.
func (i *Instance) SetGeminiYoloMode(enabled bool) {
	if i.Tool != "gemini" {
		return
	}

	i.GeminiYoloMode = &enabled

	// Sync to tmux environment immediately if session exists
	// This ensures background detection (UpdateGeminiSession) sees the new value
	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		val := "false"
		if enabled {
			val = "true"
		}
		_ = i.tmuxSession.SetEnvironment("GEMINI_YOLO_MODE", val)
	}
}

// UpdateGeminiSession updates the Gemini session ID, YOLO mode, analytics, and latest prompt.
// Delegates to focused helpers for each concern.
func (i *Instance) UpdateGeminiSession(excludeIDs map[string]bool) {
	if i.Tool != "gemini" {
		return
	}
	i.syncGeminiSessionFromTmux()
	i.syncGeminiSessionFromDisk()
	i.updateGeminiAnalytics()
	i.updateGeminiLatestPrompt()
}

// UpdateOpenCodeSession refreshes the OpenCode session ID from OpenCode state
// without stealing a different tab's session from the same project.
func (i *Instance) UpdateOpenCodeSession() {
	i.updateOpenCodeSession(false)
}

// updateOpenCodeSession self-manages i.mu: state reads/writes happen under the
// lock but queryOpenCodeSession I/O runs outside it, so a slow OpenCode server
// or compatibility CLI cannot starve render-path RLocks on this instance.
//
// Contract: callers MUST NOT hold i.mu when invoking this function.
func (i *Instance) updateOpenCodeSession(force bool) {
	if i.Tool != "opencode" {
		return
	}

	i.mu.Lock()
	now := time.Now()
	if !force && !i.lastOpenCodeScanAt.IsZero() && now.Sub(i.lastOpenCodeScanAt) < opencodeRotationScanInterval {
		i.mu.Unlock()
		return
	}
	i.lastOpenCodeScanAt = now
	i.mu.Unlock()

	candidate := i.queryOpenCodeSession()
	i.applyOpenCodeSessionCandidate(candidate)
}

func (i *Instance) applyOpenCodeSessionCandidate(candidate string) bool {
	if candidate == "" {
		return false
	}

	i.mu.Lock()
	if candidate == i.OpenCodeSessionID {
		if i.OpenCodeDetectedAt.IsZero() {
			i.OpenCodeDetectedAt = time.Now()
		}
		i.mu.Unlock()
		return false
	}

	if i.OpenCodeSessionID != "" {
		lastActivity := i.GetLastActivityTime()
		if !lastActivity.IsZero() && time.Since(lastActivity) <= opencodeRotationActivityWindow {
			sessionLog.Debug(
				"opencode_session_rebind_recent_activity",
				slog.String("old_id", i.OpenCodeSessionID),
				slog.String("new_id", candidate),
				slog.Time("last_activity", lastActivity),
			)
		}
	}

	sessionLog.Debug(
		"opencode_session_rebind",
		slog.String("old_id", i.OpenCodeSessionID),
		slog.String("new_id", candidate),
	)

	i.setOpenCodeSessionLocked(candidate)
	tmuxSession := i.tmuxSession
	i.mu.Unlock()
	i.syncOpenCodeSessionEnvironment(tmuxSession, candidate)
	return true
}

// syncGeminiSessionFromTmux reads session ID and YOLO mode from tmux environment (authoritative source).
func (i *Instance) syncGeminiSessionFromTmux() {
	if i.tmuxSession == nil {
		return
	}
	if sessionID, err := i.tmuxSession.GetEnvironment("GEMINI_SESSION_ID"); err == nil && sessionID != "" {
		if i.GeminiSessionID != sessionID {
			i.GeminiSessionID = sessionID
		}
		i.GeminiDetectedAt = time.Now()
	}

	// Detect YOLO Mode from environment (authoritative sync)
	if yoloEnv, err := i.tmuxSession.GetEnvironment("GEMINI_YOLO_MODE"); err == nil && yoloEnv != "" {
		enabled := yoloEnv == "true"
		i.GeminiYoloMode = &enabled
	}
}

// syncGeminiSessionFromDisk scans the filesystem for the most recent session.
// Krudony fix: user may have started a NEW session, so always scan rather than using stale cached ID.
func (i *Instance) syncGeminiSessionFromDisk() {
	sessions, err := ListGeminiSessions(i.ProjectPath)
	if err != nil || len(sessions) == 0 {
		return
	}

	// Pick the most recent session (list is sorted by LastUpdated desc)
	mostRecent := sessions[0]
	if mostRecent.SessionID != i.GeminiSessionID {
		sessionLog.Debug(
			"gemini_session_update",
			slog.String("old_id", i.GeminiSessionID),
			slog.String("new_id", mostRecent.SessionID),
		)
	}
	i.GeminiSessionID = mostRecent.SessionID
	i.GeminiDetectedAt = time.Now()

	// Sync back to tmux environment for future restarts
	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		_ = i.tmuxSession.SetEnvironment("GEMINI_SESSION_ID", i.GeminiSessionID)
	}
}

// updateGeminiAnalytics refreshes token counts, cost, and model from the session file.
// Syncs the detected model back to the instance's GeminiModel field.
func (i *Instance) updateGeminiAnalytics() {
	if i.GeminiSessionID == "" {
		return
	}
	if i.GeminiAnalytics == nil {
		i.GeminiAnalytics = &GeminiSessionAnalytics{}
	}
	// Non-blocking update (ignore errors, best effort)
	_ = UpdateGeminiAnalyticsFromDisk(i.ProjectPath, i.GeminiSessionID, i.GeminiAnalytics)

	// Sync detected model from analytics to instance (if not explicitly set by user)
	if i.GeminiModel == "" && i.GeminiAnalytics.Model != "" {
		i.GeminiModel = i.GeminiAnalytics.Model
	}
}

// updateGeminiLatestPrompt extracts the latest user prompt from the session file.
// Uses mtime caching to skip re-reading unchanged files (important for large session files).
func (i *Instance) updateGeminiLatestPrompt() {
	if i.GeminiSessionID == "" || len(i.GeminiSessionID) < 8 {
		return
	}

	sessionsDir := GetGeminiSessionsDir(i.ProjectPath)
	pattern := filepath.Join(sessionsDir, "session-*-"+i.GeminiSessionID[:8]+".json")
	filePath, fileMtime := findNewestFile(pattern)

	// Fallback: cross-project search
	if filePath == "" {
		filePath = findGeminiSessionInAllProjects(i.GeminiSessionID)
		if filePath != "" {
			if info, err := os.Stat(filePath); err == nil {
				fileMtime = info.ModTime()
			}
		}
	}

	if filePath == "" {
		return
	}

	// mtime cache: skip re-read if file hasn't changed since last read
	if !i.lastPromptModTime.IsZero() && !fileMtime.IsZero() && fileMtime.Equal(i.lastPromptModTime) {
		return
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	if prompt, err := parseGeminiLatestUserPrompt(data); err == nil && prompt != "" {
		i.LatestPrompt = prompt
	}
	i.lastPromptModTime = fileMtime
}

// WaitForClaudeSession waits for the tmux environment variable to be set.
// The capture-resume pattern sets CLAUDE_SESSION_ID in tmux env, so we poll for that.
// Returns the detected session ID or empty string after timeout.
func (i *Instance) WaitForClaudeSession(maxWait time.Duration) string {
	if !IsClaudeCompatible(i.Tool) {
		return ""
	}

	// Poll every 200ms for up to maxWait
	interval := 200 * time.Millisecond
	deadline := time.Now().Add(maxWait)

	for time.Now().Before(deadline) {
		// Check tmux environment (set by capture-resume pattern)
		if sessionID := i.GetSessionIDFromTmux(); sessionID != "" {
			i.ClaudeSessionID = sessionID
			// #1815: own pane env is a WEAK vouch — see
			// noteClaudeSessionIDFromOwnPane.
			i.noteClaudeSessionIDFromOwnPane()
			i.ClaudeDetectedAt = time.Now()
			return sessionID
		}
		time.Sleep(interval)
	}

	return ""
}

// WaitForClaudeSessionWithExclude waits for the tmux environment variable to be set.
// The excludeIDs parameter is kept for API compatibility but not used since tmux env
// is authoritative and won't return duplicate IDs.
func (i *Instance) WaitForClaudeSessionWithExclude(maxWait time.Duration, excludeIDs map[string]bool) string {
	// tmux env is authoritative - no need for exclusion logic
	return i.WaitForClaudeSession(maxWait)
}

// PostStartSync captures session IDs from tmux environment after Start() or Restart().
// Designed for CLI commands that exit after starting. The TUI doesn't need this
// because its background worker handles detection.
//
// For Claude: polls tmux env for CLAUDE_SESSION_ID (set by bash uuidgen before exec).
// For Gemini: reads session ID from filesystem.
// For OpenCode/Codex: no-op (async goroutine detection, too slow for sync CLI).
func (i *Instance) PostStartSync(maxWait time.Duration) {
	switch {
	case IsClaudeCompatible(i.Tool):
		i.WaitForClaudeSession(maxWait)
		i.autoConfirmClaudeResumePicker()
	case i.Tool == "gemini":
		i.UpdateGeminiSession(nil)
	case i.Tool == "copilot":
		// Copilot uses async detection via detectCopilotSessionAsync().
		// If the session was not yet detected, attempt a quick sync check.
		if i.CopilotSessionID == "" {
			cwd := i.EffectiveWorkingDir()
			startedAfter := time.Now().Add(-30 * time.Second)
			if i.CopilotStartedAt > 0 {
				startedAfter = time.UnixMilli(i.CopilotStartedAt).Add(-2 * time.Second)
			}
			if sid := detectCopilotSessionFromDisk(cwd, startedAfter); sid != "" {
				i.CopilotSessionID = sid
				i.CopilotDetectedAt = time.Now()
				if i.tmuxSession != nil {
					_ = i.tmuxSession.SetEnvironment("COPILOT_SESSION_ID", sid)
				}
			}
		}
	}
	// OpenCode/Codex: async detection already started by Start(), skip here
}

// autoConfirmClaudeResumePicker handles the "Resume from summary" picker that
// claude --resume shows on long-running sessions (>~250k tokens). Without
// this, an unattended conductor sits frozen on the picker indefinitely.
// See issue #67. Disable via [claude].auto_resume_summary = false.
func (i *Instance) autoConfirmClaudeResumePicker() {
	if i.tmuxSession == nil {
		return
	}
	cfg, _ := LoadUserConfig()
	if cfg != nil && !cfg.Claude.GetAutoResumeSummary() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = autoResolveClaudeResumePicker(ctx, i.tmuxSession, i.tmuxSession, autoResumeOptions{
		PollInterval: 250 * time.Millisecond,
		Timeout:      3 * time.Second,
	})
}

// Preview returns the last 3 lines of terminal output
func (i *Instance) Preview() (string, error) {
	if i.tmuxSession == nil {
		return "", fmt.Errorf("tmux session not initialized")
	}

	content, err := i.tmuxSession.CapturePane()
	if err != nil {
		// #1580: the pane is gone (fast spawn death). Surface the recorded
		// spawn-failure diagnostic instead of a bare error so the preview pane
		// explains what happened.
		if fallback := i.spawnFailurePreview(); fallback != "" {
			return fallback, nil
		}
		return "", err
	}

	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}

	return strings.Join(lines, "\n"), nil
}

// PreviewFull returns all terminal output
func (i *Instance) PreviewFull() (string, error) {
	if i.tmuxSession == nil {
		return "", fmt.Errorf("tmux session not initialized")
	}

	content, err := i.tmuxSession.CaptureFullHistory()
	if err != nil {
		// #1580: pane gone — fall back to the recorded spawn failure.
		if fallback := i.spawnFailurePreview(); fallback != "" {
			return fallback, nil
		}
		return "", err
	}
	return content, nil
}

// spawnFailurePreview returns the formatted diagnostic for a session whose pane
// is gone, or "" when there is none. Used as a preview fallback when the tmux
// pane no longer exists (#1580).
//
// The auth hold is checked FIRST: when a session died because it could not
// authenticate, "run /login, then press R" is the only thing the user needs to
// read, and it must not be buried under a generic spawn-failure block.
func (i *Instance) spawnFailurePreview() string {
	if hold := i.AuthHold(); hold != nil {
		return hold.FormatForDisplay()
	}
	rec, err := readSpawnFailureRecord(i.ID)
	if err != nil || rec == nil {
		return ""
	}
	return rec.FormatForDisplay()
}

// PreviewWindowFull returns the full scrollback of a specific tmux window.
func (i *Instance) PreviewWindowFull(windowIndex int) (string, error) {
	if i.tmuxSession == nil {
		return "", fmt.Errorf("tmux session not initialized")
	}
	return i.tmuxSession.CaptureWindowFullHistory(windowIndex)
}

// HasUpdated checks if there's new output since last check
func (i *Instance) HasUpdated() bool {
	if i.tmuxSession == nil {
		return false
	}

	updated, err := i.tmuxSession.HasUpdated()
	if err != nil {
		return false
	}

	return updated
}

// SyncSessionIDsToTmux syncs session IDs from Instance to tmux environment.
// PERFORMANCE: This is called on-demand (e.g., first attach) rather than at load time
// to reduce subprocess overhead during TUI startup.
//
// Session IDs are needed in tmux environment for restart/resume operations that
// spawn new processes. Without this sync, R key wouldn't resume the correct session.
func (i *Instance) SyncSessionIDsToTmux() {
	if i.tmuxSession == nil || !i.tmuxSession.Exists() {
		return
	}

	// Sync ClaudeSessionID.
	//
	// #1815: never publish a disk-scanned id into the pane env. The env is
	// read back as an ownership signal, so publishing a suspect id would
	// launder it into a resumable one on the next poll — the guard's own
	// bypass. A suspect id is refused at spawn anyway, so the pane never
	// legitimately holds one.
	i.claudeSessionIDsFromDiskScanMu.Lock()
	tainted := i.claudeSessionIDsFromDiskScan[i.ClaudeSessionID]
	i.claudeSessionIDsFromDiskScanMu.Unlock()
	if i.ClaudeSessionID != "" && !tainted {
		_ = i.tmuxSession.SetEnvironment("CLAUDE_SESSION_ID", i.ClaudeSessionID)
	}

	// Sync GeminiSessionID
	if i.GeminiSessionID != "" {
		_ = i.tmuxSession.SetEnvironment("GEMINI_SESSION_ID", i.GeminiSessionID)
	}

	// Sync OpenCodeSessionID
	if i.OpenCodeSessionID != "" {
		_ = i.tmuxSession.SetEnvironment("OPENCODE_SESSION_ID", i.OpenCodeSessionID)
	}

	// Sync CodexSessionID
	if i.CodexSessionID != "" {
		_ = i.tmuxSession.SetEnvironment("CODEX_SESSION_ID", i.CodexSessionID)
	}

	// Sync CopilotSessionID
	if i.CopilotSessionID != "" {
		_ = i.tmuxSession.SetEnvironment("COPILOT_SESSION_ID", i.CopilotSessionID)
	}

	// Custom [tools.*] resume id: re-publish so a restart that still has the
	// old pane (or a respawn) sees the same id. The agent-deck-owned variable
	// is written too, so a tool that exposes no env var of its own is still
	// recoverable from the pane.
	//
	// Gated on the scope check for the same reason the read side is. The pane
	// env is read back as a live ownership signal, so publishing an id that
	// resume has refused would launder it into acceptance on the next poll --
	// the refusal has to hold on both sides of the loop or it holds on neither.
	if i.GenericSessionID != "" && i.genericSessionScopeMismatch() == "" {
		for _, envName := range i.genericSessionEnvNames() {
			_ = i.tmuxSession.SetEnvironment(envName, i.GenericSessionID)
		}
	}
}

func (i *Instance) clearSessionBindingForFreshStart() {
	if IsClaudeCompatible(i.Tool) {
		i.ClaudeSessionID = ""
		i.ClaudeDetectedAt = time.Time{}
	}

	if i.Tool == "gemini" {
		i.GeminiSessionID = ""
		i.GeminiDetectedAt = time.Time{}
	}

	if i.Tool == "opencode" {
		i.OpenCodeSessionID = ""
		i.OpenCodeDetectedAt = time.Time{}
		i.OpenCodeStartedAt = 0
		i.lastOpenCodeScanAt = time.Time{}
	}

	if i.Tool == "codex" {
		i.CodexSessionID = ""
		i.CodexDetectedAt = time.Time{}
		i.CodexStartedAt = 0
		i.lastCodexScanAt = time.Time{}
		i.mu.Lock()
		i.pendingCodexRestartWarning = ""
		i.mu.Unlock()
	}

	if i.Tool == "copilot" {
		i.CopilotSessionID = ""
		i.CopilotDetectedAt = time.Time{}
		i.CopilotStartedAt = 0
	}

	if i.Tool == "hermes" {
		// Drop the captured resume ID so the next launch starts a new session
		// and Restart() re-captures rather than resuming the old conversation.
		i.HermesSessionID = ""
	}

	if i.Tool == "deepseek" {
		// Same contract as hermes: drop the discovered resume ID so the next
		// launch boots a new dsh session instead of reopening the old one.
		//
		// DeepSeekTask is deliberately KEPT. It is not a conversation binding —
		// it is the invocation itself for a one-shot, and a "fresh" restart of
		// `dsh --profile headless "<task>"` still means running that task in a
		// new session. Clearing it here would turn fresh-restart into an
		// unlaunchable command (PR #1942 review, P1c).
		i.DeepSeekSessionID = ""
	}

	// Custom [tools.*]: drop any persisted conversation so a deliberate
	// fresh start does not re-attach resume after reboot. Flag the clear so
	// a subsequent SaveWithGroups writes explicit empty (sticky-safe) even
	// when statedb.GetGlobal() is nil (CLI paths).
	if i.GenericSessionID != "" || !i.GenericDetectedAt.IsZero() || i.genericSessionIDCleared ||
		i.GenericSessionTool != "" || i.GenericSessionCommand != "" || i.GenericSessionLocation != "" {
		cleared := i.GenericSessionID
		i.GenericSessionID = ""
		i.GenericDetectedAt = time.Time{}
		i.GenericSessionTool = ""
		i.GenericSessionCommand = ""
		i.GenericSessionLocation = ""
		i.genericSessionIDCleared = true
		if db := statedb.GetGlobal(); db != nil {
			// The flag above means a later save re-applies this clear, so the
			// outcome is eventually right either way — but a write that keeps
			// failing on a data path would otherwise never be visible, and
			// this one deletes a binding the operator asked to be rid of.
			err := db.WriteGenericSessionBinding(i.ID, "", "", "", "", time.Time{})
			i.setGenericSessionPersistError(err)
			if err != nil {
				sessionLog.Warn("generic_session_clear_persist_failed",
					slog.String("instance_id", logging.SanitizeValue(i.ID)),
					slog.String("tool", logging.SanitizeValue(i.Tool)),
					slog.String("session_id_fingerprint", fingerprintSessionID(cleared)),
					slog.String("error", logging.SanitizeValue(err.Error())))
			}
		}
	}
}

func (i *Instance) recreateTmuxSession() {
	// Issue #663: multi-repo sessions must cwd into MultiRepoTempDir, not
	// ProjectPath (which is a symlink into that parent dir). Delegates to
	// EffectiveWorkingDir so single-repo sessions keep using ProjectPath.
	i.tmuxSession = tmux.NewSession(i.Title, i.EffectiveWorkingDir())
	// Preserve the socket the instance was originally created on (issue
	// #687). A restart/respawn cycle must NOT silently relocate the session
	// to the current default socket — that would strand the old tmux pane
	// on the stored socket and create an invisible duplicate on the new
	// one.
	i.tmuxSession.SocketName = i.TmuxSocketName
	i.tmuxSession.InstanceID = i.ID
	i.tmuxSession.SetInjectStatusLine(GetTmuxSettings().GetInjectStatusLine())
	i.tmuxSession.SetMouse(GetTmuxSettings().GetMouse())
	i.tmuxSession.SetClearOnRestart(GetTmuxSettings().ClearOnRestart)
	i.tmuxSession.SetTerminalChromeEnabled(GetTerminalSettings().GetITermBadge())
}

func (i *Instance) prepareRestartMCPConfig() {
	// Clear flag immediately to prevent it staying set if restart fails.
	skipRegen := i.SkipMCPRegenerate
	i.SkipMCPRegenerate = false

	if IsClaudeCompatible(i.Tool) && !skipRegen {
		if err := i.regenerateMCPConfig(); err != nil {
			mcpLog.Warn("mcp_config_regen_failed", slog.String("error", err.Error()))
		}
	} else if skipRegen {
		mcpLog.Debug("mcp_regen_skipped", slog.String("reason", "flag_set_by_apply"))
	}
}

// SyncSessionIDsFromTmux reads tool session IDs from the tmux environment
// into the Instance struct. This is the reverse of SyncSessionIDsToTmux.
// Used in the stop path to capture IDs that may not have been saved during
// start (e.g., if PostStartSync timed out but the tool started late).
// Only updates fields where the tmux env has a non-empty value; does not
// blank existing IDs if the tmux env is missing the variable.
func (i *Instance) SyncSessionIDsFromTmux() {
	if i.tmuxSession == nil || !i.tmuxSession.Exists() {
		return
	}

	if id, err := i.tmuxSession.GetEnvironment("CLAUDE_SESSION_ID"); err == nil && id != "" {
		i.ClaudeSessionID = id
		// #1815: own pane env — weak vouch.
		i.noteClaudeSessionIDFromOwnPane()
		if i.ClaudeDetectedAt.IsZero() {
			i.ClaudeDetectedAt = time.Now()
		}
	}

	if id, err := i.tmuxSession.GetEnvironment("GEMINI_SESSION_ID"); err == nil && id != "" {
		i.GeminiSessionID = id
	}

	if id, err := i.tmuxSession.GetEnvironment("OPENCODE_SESSION_ID"); err == nil && id != "" {
		i.OpenCodeSessionID = id
	}

	if id, err := i.tmuxSession.GetEnvironment("CODEX_SESSION_ID"); err == nil && id != "" {
		i.CodexSessionID = id
	}

	if id, err := i.tmuxSession.GetEnvironment("COPILOT_SESSION_ID"); err == nil && id != "" {
		i.CopilotSessionID = id
		if i.CopilotDetectedAt.IsZero() {
			i.CopilotDetectedAt = time.Now()
		}
	}

	// Custom tool: pull the live conversation id into GenericSessionID +
	// tool_data, from the tool's own env var or the agent-deck-owned fallback
	// the capture path publishes (see genericSessionEnvNames).
	for _, envName := range i.genericSessionEnvNames() {
		id, err := i.tmuxSession.GetEnvironment(envName)
		if err != nil {
			continue
		}
		if id = strings.TrimSpace(id); id != "" {
			i.persistGenericSessionIDIfChanged(id)
			break
		}
	}
}

// ResponseOutput represents a parsed response from an agent session
type ResponseOutput struct {
	Tool      string `json:"tool"`                 // Tool type (claude, gemini, etc.)
	Role      string `json:"role"`                 // Always "assistant" for now
	Content   string `json:"content"`              // The actual response text
	Timestamp string `json:"timestamp,omitempty"`  // When the response was generated (Claude only)
	SessionID string `json:"session_id,omitempty"` // Claude session ID (if available)
}

// GetLastResponse returns the last assistant response from the session
// For Claude: Parses the JSONL file for the last assistant message
// For Gemini: Parses the JSON session file for the last assistant message
// For Codex/Others: Attempts to parse terminal output
func (i *Instance) GetLastResponse() (*ResponseOutput, error) {
	if IsClaudeCompatible(i.Tool) {
		return i.getClaudeLastResponse()
	}
	if i.Tool == "gemini" {
		return i.getGeminiLastResponse()
	}
	return i.getTerminalLastResponse()
}

// GetLastResponseBestEffortChecked is the collision-aware variant of
// GetLastResponseBestEffort (issue #1400). `session output` (including -q)
// parses the transcript that ClaudeSessionID resolves to; when multiple LIVE
// instances share one claude_session_id they all resolve to the SAME transcript
// and return byte-identical "last responses". Given the instance's profile
// peers, this refuses the read with the same collision semantics #1352 gave
// `session output --stream` (GetJSONLPathChecked: live peers sharing both the
// session id and the transcript dir), instead of silently returning another
// session's output. Non-Claude tools and the no-collision case delegate to
// GetLastResponseBestEffort unchanged.
func (i *Instance) GetLastResponseBestEffortChecked(peers []*Instance) (*ResponseOutput, error) {
	if IsClaudeCompatible(i.Tool) {
		if _, err := i.GetJSONLPathChecked(peers); err != nil {
			return nil, fmt.Errorf("refusing to read a colliding transcript: %w", err)
		}
	}
	return i.GetLastResponseBestEffort()
}

// GetLastResponseBestEffort returns the last assistant response with fallback logic
// intended for CLI read paths (like `session output`) where we prefer useful output
// over hard errors.
//
// Behavior for Claude:
// 1. Try structured JSONL read via stored ClaudeSessionID.
// 2. Refresh ID from tmux env and retry.
// 3. Fallback to terminal parsing.
// 4. If still unavailable, return an empty response (no error).
//
// Behavior for Gemini (mirrors Claude):
// 1. Try structured JSON read via stored GeminiSessionID.
// 2. Refresh ID from tmux env and retry.
// 3. Scan disk for latest session and retry.
// 4. Fallback to terminal parsing.
// 5. If still unavailable, return an empty response (no error).
func (i *Instance) GetLastResponseBestEffort() (*ResponseOutput, error) {
	resp, err := i.GetLastResponse()
	if err == nil {
		return resp, nil
	}

	// Claude-specific recovery path
	if IsClaudeCompatible(i.Tool) {
		// Refresh from tmux env (fast path)
		if sessionID := i.GetSessionIDFromTmux(); sessionID != "" {
			i.ClaudeSessionID = sessionID
			// #1815: own pane env — weak vouch.
			i.noteClaudeSessionIDFromOwnPane()
			i.ClaudeDetectedAt = time.Now()
			if recovered, recoverErr := i.getClaudeLastResponse(); recoverErr == nil {
				return recovered, nil
			}
		}

		// Disk scan: the tmux env var is fixed at launch, so after a /clear or
		// compaction it points at a stale, empty transcript. Find the newest
		// transcript on disk that carries a real assistant reply. Mirrors the
		// Gemini syncGeminiSessionFromDisk fallback below.
		if id, recovered := i.findLatestClaudeTranscriptOnDisk(); recovered != nil {
			// #1815: this is an mtime-based disk scan — the same evidence
			// class as the restart discovery prelude, and in a shared working
			// directory the newest transcript can belong to another session.
			// Good enough to READ a last response from; never ownership. Mark
			// it unverified so it cannot authorize a later `--resume`, and do
			// NOT write it into this pane's CLAUDE_SESSION_ID: that would
			// launder a scanned id into a "bound from my own tmux env" one on
			// the next status poll, which is exactly the resume this guard
			// exists to prevent.
			i.adoptDiscoveredClaudeSessionID(id)
			i.ClaudeDetectedAt = time.Now()
			return recovered, nil
		}
	}

	// Gemini-specific recovery path (mirrors Claude recovery above)
	if i.Tool == "gemini" {
		// Refresh from tmux env (fast path)
		i.syncGeminiSessionFromTmux()
		if i.GeminiSessionID != "" {
			if recovered, recoverErr := i.getGeminiLastResponse(); recoverErr == nil {
				return recovered, nil
			}
		}

		// Fallback: detect latest session on disk (handles startup race / stale ID)
		i.syncGeminiSessionFromDisk()
		if i.GeminiSessionID != "" {
			if recovered, recoverErr := i.getGeminiLastResponse(); recoverErr == nil {
				return recovered, nil
			}
		}
	}

	// Final fallback: terminal parsing (works for all tools).
	if i.tmuxSession != nil {
		if terminalResp, terminalErr := i.getTerminalLastResponse(); terminalErr == nil {
			return terminalResp, nil
		}
	}

	// For Claude and Gemini, prefer a graceful empty response instead of a hard error.
	if IsClaudeCompatible(i.Tool) || i.Tool == "gemini" {
		toolName := i.Tool
		if IsClaudeCompatible(toolName) {
			toolName = "claude"
		}
		return &ResponseOutput{
			Tool:    toolName,
			Role:    "assistant",
			Content: "",
		}, nil
	}

	return nil, err
}

// ClaudeSessionIDCollidesWith reports whether another LIVE instance in peers
// would resolve to the SAME transcript as this instance: it shares this
// instance's (non-empty) ClaudeSessionID AND resolves to the same transcript
// directory (same Claude config dir + same encoded project path). A many-to-one
// session-id → live-instance mapping on one transcript is the data-integrity
// hazard #1349 describes: two live instances pointed at one transcript would
// cross-route input/output. Two instances that happen to share a session id but
// resolve to different transcript dirs (different project/config) are NOT a
// collision and are not blocked.
func (i *Instance) ClaudeSessionIDCollidesWith(peers []*Instance) bool {
	if i.ClaudeSessionID == "" {
		return false
	}
	mine := i.claudeTranscriptDir()
	for _, p := range peers {
		if p == nil || p.ID == i.ID {
			continue
		}
		if p.ClaudeSessionID != i.ClaudeSessionID || !isLiveSessionStatus(p.Status) {
			continue
		}
		if p.claudeTranscriptDir() == mine {
			return true
		}
	}
	return false
}

// conversationIsResumable answers the question the spawn builders actually ask:
// should this session be started with `--resume <id>` (continue the existing
// conversation) or `--session-id <id>` (create a fresh one under that id)?
//
// For a LOCAL session that is the same question as "is there conversation data
// on disk", so it delegates unchanged.
//
// For an --ssh session it is NOT. The transcript lives on the remote host, so
// the controller cannot see it — and answering the local-disk question with a
// hard false (which is the correct answer to *that* question, see
// sessionHasConversationData) would make every remote restart emit
// `--session-id <already-used-uuid>`, asking claude to CREATE a session that
// already exists. That loses the conversation, or claude rejects the id
// outright. Review round 1, finding F1.
//
// The controller's evidence for a remote session is the id it recorded when the
// session last ran. A non-empty ClaudeSessionID means "this session previously
// had this conversation", so `--resume` is the right instruction and the remote
// claude resolves it against the remote disk — the only disk that can answer.
// If the remote transcript is genuinely gone, claude reports that on the remote,
// which is a far better failure than silently starting fresh over a live id.
func conversationIsResumable(inst *Instance, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	if inst != nil && !inst.TranscriptIsResolvableLocally() {
		return true
	}
	return sessionHasConversationData(inst, sessionID)
}

// TranscriptIsResolvableLocally reports whether this instance's conversation
// transcript can be looked up in the CONTROLLER's filesystem.
//
// It is false for an --ssh session, unconditionally (#1851). That session's
// harness runs on another host and writes its transcript into THAT host's config
// dir. All the controller holds is ProjectPath — a local placeholder defaulting
// to the directory `add --ssh` was run from — so a lookup through it does not
// merely miss: it HITS, on whatever local session happens to sit at that
// directory. Every local-disk transcript lookup must ask this first; the harm is
// symmetrical (a remote session reads a local conversation, and by adopting its
// id sends the next restart's duplicate sweeper after the local session that
// owns it).
//
// The complete list of entry points this gates is in the package doc for
// remote_transcript_boundary.go.
func (i *Instance) TranscriptIsResolvableLocally() bool {
	return i != nil && !i.IsSSH()
}

// claudeTranscriptDir returns the key used to decide whether two instances would
// collide on one transcript file (issue #1349 defense-in-depth #2, via
// ClaudeSessionIDCollidesWith). For a local instance it mirrors GetJSONLPath's
// resolution — GetClaudeConfigDir + encoded ProjectPath — exactly, so the
// collision verdict matches the path the guard protects.
//
// Issue #1851: a remote session's transcript is not in this filesystem at all,
// and its ProjectPath is only a local placeholder, so two remote sessions
// sharing a placeholder were judged to share a transcript directory and
// GetJSONLPathChecked refused reads that were never in conflict. Remote sessions
// are therefore keyed on where they actually run (host + canonical remote path).
//
// Changing this key is only safe because it is changed together with every path
// that derives a file location from it: TranscriptIsResolvableLocally gates
// GetJSONLPath, getClaudeLastResponse and findLatestClaudeTranscriptOnDisk (see
// remote_transcript_boundary.go), so no remote instance resolves a local
// transcript path at all and a local/remote pair sharing one claude_session_id
// can no longer land on one file for the guard to have to catch. Without those
// gates this key would DISABLE the #1349/#1352 guard for exactly the pair this
// epic exists to separate. The result is a collision KEY that is never opened as
// a path, and the "remote-transcripts" segment keeps it from ever aliasing a
// real local projects/ directory.
func (i *Instance) claudeTranscriptDir() string {
	configDir := GetClaudeConfigDir()
	if i.IsSSH() {
		loc := LocationOf(i)
		return filepath.Join(configDir, "remote-transcripts", ConvertToClaudeDirName(loc.String()))
	}
	resolvedPath := i.ProjectPath
	if resolved, err := filepath.EvalSymlinks(i.ProjectPath); err == nil {
		resolvedPath = resolved
	}
	return filepath.Join(configDir, "projects", ConvertToClaudeDirName(resolvedPath))
}

// GetJSONLPathChecked is the collision-aware variant of GetJSONLPath (issue
// #1349 defense-in-depth #2). Given the set of instances it shares a profile
// with, it refuses to resolve a transcript path when this instance's
// ClaudeSessionID collides with another LIVE instance's ClaudeSessionID —
// because two live instances mapped to one session-id would otherwise read the
// same transcript, corrupting routing. When there is no collision it delegates
// to GetJSONLPath.
func (i *Instance) GetJSONLPathChecked(peers []*Instance) (string, error) {
	if i.ClaudeSessionIDCollidesWith(peers) {
		_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
			InstanceID: i.ID, Tool: i.Tool, Action: "reject",
			Source: "jsonl_resolve", OldID: i.ClaudeSessionID, Candidate: i.ClaudeSessionID,
			Reason: "claude_session_id_collision_across_live_instances",
		})
		return "", fmt.Errorf("claude_session_id %q is shared by more than one live instance; refusing to resolve a colliding transcript path for instance %s", i.ClaudeSessionID, i.ID)
	}
	return i.GetJSONLPath(), nil
}

// resolveClaudeTranscriptPath returns the path to the Claude JSONL transcript for
// the given session, or "" if it cannot be found. It first tries the projects/
// subdirectory whose name Claude derives from the project path; if that misses, it
// falls back to locating the transcript by its uniquely-named <sessionID>.jsonl
// anywhere under projects/.
//
// The fallback matters on WSL: agent-deck stores a Linux project path (e.g.
// /home/user or /mnt/d/proj), but Claude Code runs as a Windows-native process and
// names its project directory from the Windows/UNC form of the cwd (e.g.
// \\wsl.localhost\Ubuntu\home\user -> --wsl-localhost-Ubuntu-home-user, or
// D:\proj -> D--proj). The two encodings never match, so the computed path misses
// and analytics / last-response silently break. The session ID is a UUID, so
// matching on the filename is unambiguous.
func resolveClaudeTranscriptPath(configDir, projectPath, sessionID string) string {
	if sessionID == "" {
		return ""
	}

	// Resolve symlinks in project path (macOS: /tmp -> /private/tmp).
	resolvedPath := projectPath
	if resolved, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedPath = resolved
	}

	projectsDir := filepath.Join(configDir, "projects")

	// Primary: the directory name Claude derives from the project path. Claude
	// replaces every non-alphanumeric char with a hyphen.
	//
	// Both encodings of the project path are tried as EXACT candidates, resolved
	// first: Claude names the directory from getcwd() (the physical path), but a
	// transcript recorded through a symlinked path — or copied from another host
	// — can carry the unresolved form. Checking the second candidate here keeps
	// the resolution deterministic and, more importantly, keeps it OUT of the
	// glob fallback below, which cannot tell two projects apart when they share
	// a session id (issue #1720).
	candidateDirs := []string{resolvedPath}
	if projectPath != resolvedPath {
		candidateDirs = append(candidateDirs, projectPath)
	}
	for _, candidateDir := range candidateDirs {
		if candidateDir == "" {
			continue
		}
		candidate := filepath.Join(projectsDir, ConvertToClaudeDirName(candidateDir), sessionID+".jsonl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Fallback: the transcript may live under a differently-encoded directory name
	// (notably WSL Linux path vs. Windows/UNC cwd). Locate it by its unique
	// session-id filename. A UUID contains no glob metacharacters.
	if matches, err := filepath.Glob(filepath.Join(projectsDir, "*", sessionID+".jsonl")); err == nil && len(matches) > 0 {
		return matches[0]
	}

	return ""
}

// GetJSONLPath returns the path to the Claude session JSONL file for analytics.
// Returns empty string if this is not a Claude session or the transcript is absent.
//
// A remote session's transcript is never absent-or-here: it is on the remote
// host, so "" is the only truthful answer (see TranscriptIsResolvableLocally).
// Resolving it through the local placeholder ProjectPath would hand a LOCAL
// session's conversation to every caller of this function — analytics, the
// transition notifier, the TUI, and GetJSONLPathChecked.
func (i *Instance) GetJSONLPath() string {
	if !IsClaudeCompatible(i.Tool) || i.ClaudeSessionID == "" {
		return ""
	}
	if !i.TranscriptIsResolvableLocally() {
		return ""
	}
	return resolveClaudeTranscriptPath(GetClaudeConfigDir(), i.ProjectPath, i.ClaudeSessionID)
}

// getClaudeLastResponse extracts the last assistant message from Claude's JSONL file
func (i *Instance) getClaudeLastResponse() (*ResponseOutput, error) {
	// Require stored session ID - no fallback to file scanning
	if i.ClaudeSessionID == "" {
		return nil, fmt.Errorf("no Claude session ID available for this instance")
	}

	// The conversation of an --ssh session is on the remote host; reading the
	// controller's disk for it returns a LOCAL session's reply (#1851).
	if !i.TranscriptIsResolvableLocally() {
		return nil, fmt.Errorf("instance %s runs on %s; its Claude transcript is not on this machine", i.ID, i.SSHHost)
	}

	sessionFile := resolveClaudeTranscriptPath(GetClaudeConfigDir(), i.ProjectPath, i.ClaudeSessionID)
	if sessionFile == "" {
		return nil, fmt.Errorf("session file not found for claude_session_id %s", i.ClaudeSessionID)
	}

	// Read and parse the JSONL file
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	return parseClaudeLastAssistantMessage(data, filepath.Base(sessionFile))
}

// findLatestClaudeTranscriptOnDisk scans the instance's Claude project directory
// for the most recently modified transcript that carries a real (non-sidechain)
// assistant message, returning its session ID and parsed response.
//
// This recovers from a stale CLAUDE_SESSION_ID: when a Claude session rolls over
// (/clear or compaction starts a NEW transcript), the tmux env var — fixed at
// launch — still points at the OLD, now-empty transcript. Without this fallback
// the read path drops to raw tmux-pane parsing, which leaks tool output (e.g. a
// `list --json` dump) into conductor chat replies. Mirrors the Gemini
// syncGeminiSessionFromDisk fallback.
//
// Returns ("", nil) when no suitable transcript is found.
//
// It is the third door into "resolve a conversation against local disk", and the
// most dangerous one: its caller WRITES the recovered id back onto the instance
// and into the tmux pane's environment. For an --ssh session that would adopt a
// LOCAL conversation durably, so the next restart reaches
// sweepDuplicateToolSessions holding a local session's CLAUDE_SESSION_ID and
// kills it — #1851's data loss, reached past the restart bail-out (which returns
// early once ClaudeSessionID is non-empty). Hence the same unconditional gate the
// other two doors have.
func (i *Instance) findLatestClaudeTranscriptOnDisk() (string, *ResponseOutput) {
	if !i.TranscriptIsResolvableLocally() {
		return "", nil
	}

	configDir := GetClaudeConfigDir()

	resolvedPath := i.ProjectPath
	if resolved, err := filepath.EvalSymlinks(i.ProjectPath); err == nil {
		resolvedPath = resolved
	}
	projectDir := filepath.Join(configDir, "projects", ConvertToClaudeDirName(resolvedPath))

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return "", nil
	}

	type candidate struct {
		id  string
		mod time.Time
	}
	var candidates []candidate
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{
			id:  strings.TrimSuffix(e.Name(), ".jsonl"),
			mod: info.ModTime(),
		})
	}

	// Newest first; the current conversation is the most recently written file
	// that still contains a real assistant reply.
	sort.Slice(candidates, func(a, b int) bool {
		return candidates[a].mod.After(candidates[b].mod)
	})

	for _, c := range candidates {
		data, err := os.ReadFile(filepath.Join(projectDir, c.id+".jsonl"))
		if err != nil {
			continue
		}
		// ConvertToClaudeDirName is lossy: it maps every non-alphanumeric byte to
		// a hyphen, so two project paths that share a prefix but differ only at a
		// separator (e.g. /a/b and /a-b) encode to the SAME projects/ subdir. A
		// sibling session's transcript then sits alongside this instance's, and a
		// pure newest-mtime pick would cross-route the sibling's reply into this
		// session's output. Claude stamps the absolute cwd on its records, so when
		// a transcript declares a cwd that is NOT this instance's project path we
		// skip it — the mtime scan must never return a proven-foreign transcript.
		if cwd, ok := claudeTranscriptCWD(data); ok && !sameClaudeCWD(cwd, resolvedPath) {
			continue
		}
		resp, err := parseClaudeLastAssistantMessage(data, c.id+".jsonl")
		if err == nil && resp != nil && strings.TrimSpace(resp.Content) != "" {
			return c.id, resp
		}
	}
	return "", nil
}

// claudeTranscriptCWD returns the working directory Claude Code recorded in a
// transcript, scanning only the leading records (the cwd is stamped on every
// record, so the first one that carries it is authoritative). It returns
// ("", false) when no record within the scan window declares a cwd — e.g. an
// older or truncated transcript — so callers can treat "unknown" as "cannot
// prove foreign" rather than excluding it.
func claudeTranscriptCWD(data []byte) (string, bool) {
	const maxLines = 64
	start := 0
	for lines := 0; start < len(data) && lines < maxLines; lines++ {
		var line []byte
		if nl := bytes.IndexByte(data[start:], '\n'); nl < 0 {
			line = data[start:]
			start = len(data)
		} else {
			line = data[start : start+nl]
			start += nl + 1
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec struct {
			CWD string `json:"cwd"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.CWD != "" {
			return rec.CWD, true
		}
	}
	return "", false
}

// sameClaudeCWD reports whether a transcript-recorded cwd names the same
// directory as the instance's (already symlink-resolved) project path. It
// compares cleaned paths first, then falls back to symlink resolution so a
// transcript that recorded a pre-resolution cwd still matches.
func sameClaudeCWD(transcriptCWD, resolvedProjectPath string) bool {
	if transcriptCWD == resolvedProjectPath {
		return true
	}
	a, b := filepath.Clean(transcriptCWD), filepath.Clean(resolvedProjectPath)
	if a == b {
		return true
	}
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		if ra == b {
			return true
		}
		if rb, err := filepath.EvalSymlinks(b); err == nil && ra == rb {
			return true
		}
	}
	return false
}

// parseClaudeLastAssistantMessage parses a Claude JSONL file to extract the last assistant message.
//
// It anchors at EOF and walks lines BACKWARD over the raw bytes with no
// line-length ceiling (issue #1568): a forward bufio.Scanner capped at 1MB
// stopped silently (bufio.ErrTooLong) at the multi-megabyte single-line
// records that Claude Code /compact inserts mid-file (file-history-snapshot,
// compact summaries), so every post-compact reply was invisible and the last
// PRE-compact assistant text was returned forever.
func parseClaudeLastAssistantMessage(data []byte, sessionID string) (*ResponseOutput, error) {
	// JSONL record structure (same as global_search.go)
	type claudeMessage struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	type claudeRecord struct {
		SessionID   string          `json:"sessionId"`
		Type        string          `json:"type"`
		Message     json.RawMessage `json:"message"`
		Timestamp   string          `json:"timestamp"`
		IsSidechain bool            `json:"isSidechain"`
	}

	var lastAssistantContent string
	var lastTimestamp string
	var foundSessionID string

	// Walk lines from EOF backward; the first non-sidechain assistant record
	// with text content is the newest reply.
	for end := len(data); end > 0; {
		nl := bytes.LastIndexByte(data[:end], '\n')
		line := bytes.TrimSpace(data[nl+1 : end])
		end = nl
		if nl < 0 {
			end = 0
		}
		if len(line) == 0 {
			continue
		}

		var record claudeRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue // Skip malformed lines
		}

		// Skip subagent sidechain records: the conversation's "last response"
		// is the parent agent's reply, not a Task subagent's output.
		if record.IsSidechain {
			continue
		}

		// Capture session ID (backfilled from earlier records when the
		// assistant record itself lacks one).
		if foundSessionID == "" && record.SessionID != "" {
			foundSessionID = record.SessionID
		}

		// Once the newest assistant text is found, keep walking only until a
		// session ID is known.
		if lastAssistantContent != "" {
			if foundSessionID != "" {
				break
			}
			continue
		}

		// Only care about messages
		if len(record.Message) == 0 {
			continue
		}

		var msg claudeMessage
		if err := json.Unmarshal(record.Message, &msg); err != nil {
			continue
		}

		// Only care about assistant messages
		if msg.Role != "assistant" {
			continue
		}

		// Extract content (can be string or array of blocks)
		var contentStr string
		var extractedText string
		if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
			// Simple string content
			extractedText = contentStr
		} else {
			// Try as array of content blocks
			var blocks []map[string]interface{}
			if err := json.Unmarshal(msg.Content, &blocks); err == nil {
				var sb strings.Builder
				for _, block := range blocks {
					// Check for text type blocks
					if blockType, ok := block["type"].(string); ok && blockType == "text" {
						if text, ok := block["text"].(string); ok {
							sb.WriteString(text)
							sb.WriteString("\n")
						}
					}
				}
				extractedText = strings.TrimSpace(sb.String())
			}
		}
		// Only accept records with actual text content (tool_use-only
		// assistant records are skipped).
		if extractedText != "" {
			lastAssistantContent = extractedText
			lastTimestamp = record.Timestamp
			if foundSessionID != "" {
				break
			}
		}
	}

	if lastAssistantContent == "" {
		return nil, fmt.Errorf("no assistant response found in session")
	}

	return &ResponseOutput{
		Tool:      "claude",
		Role:      "assistant",
		Content:   lastAssistantContent,
		Timestamp: lastTimestamp,
		SessionID: foundSessionID,
	}, nil
}

// parseClaudeLatestUserPrompt parses a Claude JSONL file to extract the last user message
func parseClaudeLatestUserPrompt(data []byte) (string, error) {
	// JSONL record structure
	type claudeMessage struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	type claudeRecord struct {
		Message json.RawMessage `json:"message"`
	}

	var latestPrompt string

	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Handle large lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var record claudeRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue // Skip malformed lines
		}

		// Only care about messages
		if len(record.Message) == 0 {
			continue
		}

		var msg claudeMessage
		if err := json.Unmarshal(record.Message, &msg); err != nil {
			continue
		}

		// Only care about user messages
		if msg.Role != "user" {
			continue
		}

		// Extract content (can be string or array of blocks)
		var contentStr string
		var extractedText string
		if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
			// Simple string content
			extractedText = contentStr
		} else {
			// Try as array of content blocks
			var blocks []map[string]interface{}
			if err := json.Unmarshal(msg.Content, &blocks); err == nil {
				var sb strings.Builder
				for _, block := range blocks {
					if blockType, ok := block["type"].(string); ok && blockType == "text" {
						if text, ok := block["text"].(string); ok {
							sb.WriteString(text)
							sb.WriteString(" ")
						}
					}
				}
				extractedText = strings.TrimSpace(sb.String())
			}
		}

		// Sanitize: strip newlines and extra spaces for single-line display
		if extractedText != "" {
			content := strings.ReplaceAll(extractedText, "\n", " ")
			latestPrompt = strings.Join(strings.Fields(content), " ")
		}
	}

	return latestPrompt, nil
}

// readJSONLTail reads the last user prompt from a JSONL file using tail-read with size caching.
// Instead of reading the entire file (can be 100-800MB), it:
// 1. Stats the file to get current size (cheap syscall)
// 2. Skips reading entirely if size hasn't changed since last check
// 3. Only reads the last 32KB when the file has grown
func (i *Instance) readJSONLTail(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	size := info.Size()

	// If same file and same size, return cached prompt
	if path == i.lastJSONLPath && size == i.lastJSONLSize {
		return i.cachedPrompt
	}

	// File changed or new file - read the tail
	const tailSize int64 = 32 * 1024 // 32KB
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	offset := size - tailSize
	if offset < 0 {
		offset = 0
	}
	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return ""
		}
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}

	// If we seeked into the middle of the file, skip to the first complete line
	if offset > 0 {
		if idx := bytes.IndexByte(data, '\n'); idx >= 0 {
			data = data[idx+1:]
		}
	}

	prompt, err := parseClaudeLatestUserPrompt(data)
	if err != nil || prompt == "" {
		// Update cache even on empty result to avoid re-reading
		i.lastJSONLPath = path
		i.lastJSONLSize = size
		return i.cachedPrompt // Return previous cached value
	}

	i.lastJSONLPath = path
	i.lastJSONLSize = size
	i.cachedPrompt = prompt
	return prompt
}

// parseGeminiLatestUserPrompt parses a Gemini JSON file to extract the last user message
func parseGeminiLatestUserPrompt(data []byte) (string, error) {
	var session struct {
		Messages []struct {
			Type    string `json:"type"` // "user" or "gemini"
			Content string `json:"content"`
		} `json:"messages"`
	}

	if err := json.Unmarshal(data, &session); err != nil {
		return "", fmt.Errorf("failed to parse Gemini session: %w", err)
	}

	var latestPrompt string
	// Find last "user" type message
	for i := len(session.Messages) - 1; i >= 0; i-- {
		msg := session.Messages[i]
		if msg.Type == "user" {
			// Sanitize: strip newlines and extra spaces for single-line display
			content := strings.ReplaceAll(msg.Content, "\n", " ")
			latestPrompt = strings.Join(strings.Fields(content), " ")
			break
		}
	}

	return latestPrompt, nil
}

// getGeminiLastResponse extracts the last assistant message from Gemini's JSON file
func (i *Instance) getGeminiLastResponse() (*ResponseOutput, error) {
	// Require stored session ID - no fallback to file scanning
	if i.GeminiSessionID == "" || len(i.GeminiSessionID) < 8 {
		return nil, fmt.Errorf("no Gemini session ID available for this instance")
	}

	sessionsDir := GetGeminiSessionsDir(i.ProjectPath)

	// Find file by session ID (first 8 chars in filename)
	// Filename format is session-YYYY-MM-DDTHH-MM-<uuid8>.json
	pattern := filepath.Join(sessionsDir, "session-*-"+i.GeminiSessionID[:8]+".json")
	files, _ := filepath.Glob(pattern)

	// Fallback: cross-project search if not found in expected location
	if len(files) == 0 {
		if fallbackPath := findGeminiSessionInAllProjects(i.GeminiSessionID); fallbackPath != "" {
			files = []string{fallbackPath}
		}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("session file not found for ID: %s", i.GeminiSessionID)
	}
	sessionFile := files[0]

	// Read and parse the JSON file
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	return parseGeminiLastAssistantMessage(data)
}

// parseGeminiLastAssistantMessage parses a Gemini JSON file to extract the last assistant message
// VERIFIED: Message type is "gemini" (NOT role: "assistant")
func parseGeminiLastAssistantMessage(data []byte) (*ResponseOutput, error) {
	var session struct {
		SessionID string `json:"sessionId"` // VERIFIED: camelCase
		Messages  []struct {
			ID        string            `json:"id"`
			Timestamp string            `json:"timestamp"`
			Type      string            `json:"type"` // VERIFIED: "user" or "gemini"
			Content   string            `json:"content"`
			ToolCalls []json.RawMessage `json:"toolCalls,omitempty"`
			Thoughts  []json.RawMessage `json:"thoughts,omitempty"`
			Model     string            `json:"model,omitempty"`
			Tokens    json.RawMessage   `json:"tokens,omitempty"`
		} `json:"messages"`
	}

	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse session file: %w", err)
	}

	// Find last "gemini" type message
	for i := len(session.Messages) - 1; i >= 0; i-- {
		msg := session.Messages[i]
		if msg.Type == "gemini" {
			return &ResponseOutput{
				Tool:      "gemini",
				Role:      "assistant",
				Content:   msg.Content,
				Timestamp: msg.Timestamp,
				SessionID: session.SessionID,
			}, nil
		}
	}

	return nil, fmt.Errorf("no assistant response found in session")
}

// getTerminalLastResponse extracts the last response from terminal output
// This is used for Gemini, Codex, and other tools without structured output
func (i *Instance) getTerminalLastResponse() (*ResponseOutput, error) {
	if i.tmuxSession == nil {
		return nil, fmt.Errorf("tmux session not initialized")
	}

	// Capture full history
	content, err := i.tmuxSession.CaptureFullHistory()
	if err != nil {
		return nil, fmt.Errorf("failed to capture terminal output: %w", err)
	}

	// Parse based on tool type
	switch {
	case i.Tool == "gemini":
		return parseGeminiOutput(content)
	case IsCodexCompatible(i.Tool):
		return parseCodexOutput(content)
	default:
		return parseGenericOutput(content, i.Tool)
	}
}

// parseGeminiOutput parses Gemini CLI output to extract the last response
func parseGeminiOutput(content string) (*ResponseOutput, error) {
	content = tmux.StripANSI(content)
	lines := strings.Split(content, "\n")

	// Gemini typically shows responses after "▸" prompt and before the next ">"
	// Look for response blocks in reverse order
	var responseLines []string
	inResponse := false

	for idx := len(lines) - 1; idx >= 0; idx-- {
		line := lines[idx]
		trimmed := strings.TrimSpace(line)

		// Skip empty lines at the end
		if trimmed == "" && !inResponse {
			continue
		}

		// Detect prompt line (end of response when reading backwards)
		// Common prompts: "> ", ">>> ", "$", "❯", "➜"
		isPrompt := geminiPromptRE.MatchString(trimmed)

		if isPrompt && inResponse {
			// We've found the start of the response block
			break
		}

		// Detect user input line (also marks start of assistant response when reading backwards)
		if strings.HasPrefix(trimmed, "> ") && len(trimmed) > 5 && inResponse {
			break
		}

		// We're in a response
		inResponse = true
		responseLines = append([]string{line}, responseLines...)
	}

	if len(responseLines) == 0 {
		return nil, fmt.Errorf("no response found in Gemini output")
	}

	// Clean up the response
	response := strings.TrimSpace(strings.Join(responseLines, "\n"))

	return &ResponseOutput{
		Tool:    "gemini",
		Role:    "assistant",
		Content: response,
	}, nil
}

// parseCodexOutput parses OpenAI Codex CLI output
func parseCodexOutput(content string) (*ResponseOutput, error) {
	// Codex has similar structure - adapt as needed
	return parseGenericOutput(content, "codex")
}

// parseGenericOutput is a fallback parser for unknown tools
func parseGenericOutput(content, tool string) (*ResponseOutput, error) {
	content = tmux.StripANSI(content)
	lines := strings.Split(content, "\n")

	// Look for the last substantial block of text (more than 2 lines)
	// before a prompt character
	var responseLines []string
	inResponse := false
	promptPattern := shellPromptRE

	for idx := len(lines) - 1; idx >= 0; idx-- {
		line := lines[idx]
		trimmed := strings.TrimSpace(line)

		// Skip empty lines at the end
		if trimmed == "" && !inResponse {
			continue
		}

		// Detect prompt line
		if promptPattern.MatchString(trimmed) {
			if inResponse {
				break
			}
			continue
		}

		inResponse = true
		responseLines = append([]string{line}, responseLines...)

		// Stop if we've collected enough lines (limit to prevent huge outputs)
		if len(responseLines) > 500 {
			break
		}
	}

	if len(responseLines) == 0 {
		return nil, fmt.Errorf("no response found in terminal output")
	}

	// Clean up
	response := strings.TrimSpace(strings.Join(responseLines, "\n"))

	return &ResponseOutput{
		Tool:    tool,
		Role:    "assistant",
		Content: response,
	}, nil
}

// ServiceUnitOwnership snapshots the evidence needed to decide whether
// this instance may retire its transient systemd-user service unit
// (issue #1721). MUST be called BEFORE teardown: it reads the pid of the
// tmux server generation this session belongs to, which is unobservable
// once the session is killed.
//
// Returns a zero value (which always skips the stop) when the instance has
// no tmux session.
func (i *Instance) ServiceUnitOwnership() tmux.ServiceUnitOwnership {
	if i.tmuxSession == nil {
		return tmux.ServiceUnitOwnership{}
	}
	return i.tmuxSession.ServiceUnitOwnership()
}

// RetireServiceUnit stops + resets-failed the transient systemd-user
// service unit associated with this instance's tmux server (if
// LaunchAs=service was used) — but ONLY when `own` proves this session was
// its exclusive owner. Intended for the remove/delete code path ONLY —
// NOT for restart, which needs the unit to persist so it can re-spawn tmux.
//
// `own` must come from ServiceUnitOwnership() called BEFORE teardown. The
// returned decision reports whether the unit was stopped and why (see the
// tmux.UnitStop* reasons); a skip is never an error — leaving a transient
// unit behind is recoverable, killing a sibling session is not.
func (i *Instance) RetireServiceUnit(own tmux.ServiceUnitOwnership) tmux.ServiceUnitDecision {
	return tmux.StopServiceUnitOwned(own)
}

// Kill terminates the tmux session and cleans up sandbox container if present.
func (i *Instance) Kill() error {
	return i.killInternal(false)
}

// KillAndWait is the synchronous companion to Kill. It performs the
// same teardown AND blocks until the pane process tree has been
// verified dead (SIGTERM → SIGKILL escalation inline, not in a
// background goroutine). Callers in short-lived CLI processes
// (`agent-deck remove`, `agent-deck session remove`) MUST use this
// variant — see issue #59 (v1.7.68). The TUI and web callers can
// keep using Kill for the non-blocking path.
func (i *Instance) KillAndWait() error {
	return i.killInternal(true)
}

func (i *Instance) killInternal(sync bool) error {
	// #1580/#1775: supersede any in-flight fast-death watcher BEFORE the tmux
	// kill (not after). watchForFastDeath only writes a SpawnFailureRecord /
	// lifecycle event on an iteration whose gen check passes; bumping here
	// first closes that window so a deliberate stop/kill can never be
	// observed by the watcher as a "fast death" mid-teardown — including the
	// up-to-3s KillAndWait escalation below, which used to run entirely
	// inside the stale-gen window.
	i.bumpSpawnGenAndBarrier()

	// Issue #965 wiring (PR #1000 follow-up): claude/codex/gemini spawn
	// stdio MCP children when they read .mcp.json — agent-deck never
	// has a direct exec.Command for them, so spawn-time PID
	// registration is impossible. Discover descendants from the pane
	// process tree while the shell+tool are still alive, then SIGTERM
	// them before tmux teardown. Without this, detached children
	// (e.g., npx-wrapped MCPs that setsid into their own session)
	// reparent to PID 1 and accumulate.
	i.discoverMCPChildrenFromPaneTree()

	// Reap tracked MCP child PIDs first (issue #965). Stdio MCP children
	// don't die with their parent claude process — they get reparented to
	// PID 1 and accumulate. SIGTERM with a short grace period, then
	// SIGKILL anything still alive.
	i.reapTrackedMCPChildren()

	// Issue #953: kill the tmux session AND publish StatusStopped
	// atomically under i.mu so concurrent UpdateStatus() callers (most
	// notably the TUI's backgroundStatusUpdate poller) cannot observe
	// the intermediate state where the tmux pane is gone but Status
	// still reflects the pre-kill running/idle value. The pre-existing
	// !tmuxSession.Exists() branch in UpdateStatus then short-circuits
	// on `Status == StatusStopped` (lines around 3221/3237) and leaves
	// the status alone. Setting Status only AFTER the tmux Kill (and
	// not before) also prevents the symmetric "Status is stopped, tmux
	// is alive — must be a user-initiated restart, flip to Running"
	// path at line 3245 from firing during the cleanup window.
	//
	// Holding the lock around the kill is safe: tmuxSession.Kill() is
	// a single tmux command (the process-tree reaping is deferred to a
	// goroutine via ensureProcessesDead). The KillAndWait variant can
	// take up to 3s when escalating to SIGKILL — only short-lived CLI
	// processes (session remove) take that path, and they have no
	// concurrent TUI render contending for the lock.
	var tmuxErr error
	i.mu.Lock()
	if i.tmuxSession != nil {
		if sync {
			tmuxErr = i.tmuxSession.KillAndWait()
		} else {
			tmuxErr = i.tmuxSession.Kill()
		}
	}
	i.Status = StatusStopped
	// A deliberate stop releases any auth hold: the session's whole runtime state
	// is being discarded, and the next start is by definition a user act — the
	// same intent the hold is waiting for. Without this, a session that showed a
	// 401 and was then stopped by hand would stay held, and the user's own
	// `session restart` would be refused with only --force as a way through.
	i.clearAuthHoldLocked()
	i.mu.Unlock()
	// (gen already bumped at the top of killInternal, before the tmux kill —
	// see the comment there for why it must happen first, not here.)
	if i.Tool == "hermes" {
		i.clearHermesHookArtifacts()
	}

	// Clean up sandbox container (only if name matches our prefix convention).
	// Runs regardless of tmux kill result to avoid orphaned containers.
	if i.SandboxContainer != "" && docker.IsManagedContainer(i.SandboxContainer) {
		dockerCfg := GetDockerSettings()
		if dockerCfg.GetAutoCleanup() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ctr := docker.FromName(i.SandboxContainer)
			_ = ctr.Remove(ctx, true) // Force remove, ignore errors.
			i.SandboxContainer = ""
		} else {
			sessionLog.Info("sandbox_container_kept", slog.String("container", i.SandboxContainer))
		}
	}

	// Remove plaintext keychain credential files extracted during sandbox sync.
	// Gated on IsSandboxed() (not SandboxContainer) so cleanup runs even if
	// container creation failed after credential extraction.
	if i.IsSandboxed() {
		if homeDir, err := os.UserHomeDir(); err == nil {
			docker.CleanupKeychainCredentials(homeDir)
		}

		// Tear down the per-instance scoped hook bridge dir (…/hooks/sandbox/<id>).
		// Each ended sandbox session otherwise leaks a directory AND (on Linux) an
		// fsnotify inotify watch held by the notify-daemon's StatusFileWatcher →
		// watch exhaustion on long-lived hosts. Removing the dir on disk also makes
		// the kernel auto-drop that watch (IN_IGNORED). Skip when the container is
		// intentionally kept (auto-cleanup off): it still has the dir bind-mounted.
		// Follow-up: an explicit watcher.Remove() from here would require threading
		// the daemon's StatusFileWatcher into the session-end lifecycle (it is not
		// reachable from this layer), so we rely on the on-delete auto-drop.
		if i.ID != "" && GetDockerSettings().GetAutoCleanup() {
			scopedDir := filepath.Join(GetHooksDir(), "sandbox", i.ID)
			if err := os.RemoveAll(scopedDir); err != nil {
				sessionLog.Debug("sandbox_hook_dir_cleanup_failed",
					slog.String("instance", i.ID),
					slog.String("dir", scopedDir),
					slog.String("error", err.Error()),
				)
			}
		}
	}

	// Remove the scratch CLAUDE_CONFIG_DIR prepared at spawn time for
	// this worker (issue #59, v1.7.68). Best-effort — leaking a scratch
	// dir on an unclean shutdown is harmless, just wasteful.
	i.CleanupWorkerScratchConfigDir()

	// Issue #953: StatusStopped was already written under i.mu at the top
	// of this function. Re-asserting it here without the lock would
	// reintroduce the write/write data race with concurrent UpdateStatus.

	if tmuxErr != nil {
		return fmt.Errorf("failed to kill tmux session: %w", tmuxErr)
	}
	return nil
}

// Restart restarts the Claude session
// For Claude sessions with known ID: sends Ctrl+C twice and resume command to existing session
// For dead sessions or unknown ID: recreates the tmux session
//
// Issue #1040: gated by acquireInstanceSpawnLock plus a "spawned-while-
// we-waited" stamp so concurrent callers (TUI poller + RC-exit handler
// in-process; multiple `agent-deck session start` CLI invocations
// cross-process) cannot each race to recreate a tmux session for the
// same instance. A legitimate manual restart still proceeds because the
// stamp from any prior spawn pre-dates the new caller's beforeLock.
func (i *Instance) Restart() error {
	return i.restart(nil)
}

// RestartWithEnv restarts the session with one-shot environment overrides.
// The values apply to the replacement process only and are not persisted in
// the Instance or tmux session environment for future restarts.
func (i *Instance) RestartWithEnv(env map[string]string) error {
	for key := range env {
		if !IsValidEnvKey(key) {
			return fmt.Errorf("invalid environment variable name %q", key)
		}
	}
	return i.restart(env)
}

func (i *Instance) restart(env map[string]string) error {
	beforeLock := nowFn()
	release, lockErr := acquireInstanceSpawnLock(i.ID)
	if lockErr != nil {
		return lockErr
	}
	defer release()
	// A one-shot environment request is explicit operator intent. If another
	// spawn won while this call waited for the lock, restart that fresh process
	// so the requested environment is not silently discarded.
	if spawnedSince(i.ID, beforeLock) && len(env) == 0 {
		return nil
	}
	defer recordInstanceSpawn(i.ID)

	// #1775: supersede the fast-death watcher from the PREVIOUS spawn here, at
	// the single entry point, rather than deeper down. restart() has several
	// early-returning fast paths (the claude respawn-pane branch and its
	// per-tool siblings) that never reach the fallback-recreate block, so a
	// bump placed there left a restart inside the 15s window watching the
	// REPLACEMENT pane with the old spawn's command and start time — and
	// recording a failure against it if that pane died.
	i.bumpSpawnGenAndBarrier()

	if len(env) > 0 {
		i.restartEnv = make(map[string]string, len(env))
		for key, value := range env {
			i.restartEnv[key] = value
		}
		defer func() { i.restartEnv = nil }()
	}
	// Generation publication is the restart invalidation boundary. Commit it
	// before any signal/kill reaches the old Hermes process, so its delayed
	// hooks cannot overwrite the replacement's starting seed.
	if i.Tool == "hermes" {
		if _, err := i.seedHermesHookGeneration("starting", false); err != nil {
			return fmt.Errorf("seed hermes restart generation: %w", err)
		}
	}

	mcpLog.Debug(
		"restart_called",
		slog.String("tool", i.Tool),
		slog.String("claude_session_id", i.ClaudeSessionID),
		slog.Bool("tmux_session", i.tmuxSession != nil),
		slog.Bool("tmux_exists", i.tmuxSession != nil && i.tmuxSession.Exists()),
	)

	// Regenerate .mcp.json before restart to use socket pool if available.
	// Skip if MCP dialog just wrote the config (avoids race condition).
	i.prepareRestartMCPConfig()

	// Regenerate worker-scratch CLAUDE_CONFIG_DIR before restart so
	// changes to Instance.Plugins (added/removed via TUI Plugin Manager
	// or `agent-deck plugin attach/detach`) propagate into the scratch
	// settings.json before claude re-reads it. Without this, the
	// respawn-pane fast path below uses the OLD scratch and claude
	// sees the plugin enablement state from session creation, not the
	// current state. Same call as Start()/recreate paths — idempotent
	// per (sourceProfileDir, plugins-set) and best-effort on failure.
	i.prepareWorkerScratchConfigDirForSpawn()

	// Issue #956: custom-command Claude sessions whose hooks never fired
	// (or whose wrapper script overrode CLAUDE_CONFIG_DIR) arrive at
	// Restart() with empty ClaudeSessionID even when the live conversation
	// wrote a JSONL to disk. Without this prelude the fallback recreate
	// path below dispatches through buildClaudeCommand(i.Command), re-runs
	// the wrapper fresh, and silently drops chat history. Discovery here
	// populates ClaudeSessionID so the respawn-pane fast path
	// (buildClaudeResumeCommand) engages and emits `claude --resume <uuid>`.
	// Mirrors Start()'s ensureClaudeSessionIDFromDisk but bypasses the
	// #608 brand-new-session gate — Restart() implies the instance ran.
	if IsClaudeCompatible(i.Tool) && i.ClaudeSessionID == "" {
		i.ensureClaudeSessionIDFromDiskForRestart()
	}

	// If Claude session with known ID AND tmux session exists, use respawn-pane.
	if IsClaudeCompatible(i.Tool) && i.ClaudeSessionID != "" && i.tmuxSession != nil && i.tmuxSession.Exists() {
		// A known-ID restart resumes straight to `claude --resume <uuid>`
		// without passing through the Start-path prelude. If the live
		// <id>.jsonl was lost to the #1533 account-switch bug but an
		// orphaned <id>.jsonl.bak-<epoch> remains, restore it here so the
		// resume finds its history instead of starting empty.
		if i.Tool == "claude" {
			if restored, rErr := RestoreOrphanedConversationBackup(i, GetClaudeConfigDirForInstance(i)); rErr == nil && restored != "" {
				sessionLog.Info("resume: restored orphaned conversation backup id="+i.ClaudeSessionID+" reason=orphan_bak_restore_restart",
					slog.String("instance_id", i.ID),
					slog.String("claude_session_id", i.ClaudeSessionID),
					slog.String("path", restored),
					slog.String("reason", "orphan_bak_restore_restart"))
			}
		}
		prepInput := i.buildClaudeResumeCommand()
		resumeCmd, containerName, err := i.prepareCommand(prepInput)
		if err != nil {
			// #1924: a restart that fails here left StatusError with no reason.
			i.recordPrepareFailure(prepInput, err)
			return err
		}
		if containerName != "" {
			i.SandboxContainer = containerName
		}
		mcpLog.Debug("respawn_pane_claude", slog.String("command", resumeCmd))

		// #1822 F2: assert AGENTDECK_PROFILE / CLAUDE_CONFIG_DIR host-side
		// BEFORE respawn-pane, not after. tmux applies the session environment
		// to a process at the moment it starts it, so setting it after
		// RespawnPane only takes effect for the *next* spawn — the process
		// RespawnPane is about to start still inherits whatever was set
		// previously. This branch also returns before the fallback recreate
		// path that would otherwise (re)assert it.
		i.ensureProfileEnv()
		i.ensureClaudeConfigDirEnv()

		// Use respawn-pane for atomic restart
		// This is more reliable than Ctrl+C + wait for shell + send command
		// respawn-pane -k kills the current process and starts the new command atomically
		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			mcpLog.Debug("respawn_pane_claude_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart Claude session: %w", err)
		}

		mcpLog.Debug("respawn_pane_claude_succeeded")

		// Persist .sid sidecar so hook events after restart can be correlated
		WriteHookSessionAnchor(i.ID, i.ClaudeSessionID)

		// Issue #666: kill OTHER agentdeck tmux sessions sharing this
		// Claude session id so two `claude --resume` processes don't
		// race the same conversation (and stack two telegram pollers).
		i.sweepDuplicateToolSessions()

		// Re-capture MCPs after restart (they may have changed since session started)
		i.CaptureLoadedMCPs()

		// Start as WAITING - will go GREEN on next tick if Claude shows busy indicator
		i.Status = StatusWaiting
		return nil
	}

	// For Gemini: ALWAYS update session to get the most recent one
	// Krudony fix: don't skip when we already have an ID - the user may have started a NEW session
	if i.Tool == "gemini" {
		i.UpdateGeminiSession(nil)
	}

	// If Gemini session with known ID AND tmux session exists, use respawn-pane.
	if i.Tool == "gemini" && i.GeminiSessionID != "" && i.tmuxSession != nil && i.tmuxSession.Exists() {
		prepInput := i.buildGeminiCommand("gemini")
		resumeCmd, containerName, err := i.prepareCommand(prepInput)
		if err != nil {
			// #1924: a restart that fails here left StatusError with no reason.
			i.recordPrepareFailure(prepInput, err)
			return err
		}
		if containerName != "" {
			i.SandboxContainer = containerName
		}
		sessionLog.Info("restart_gemini_respawn", slog.String("command", resumeCmd))

		// #1822 F2: gemini's rebuilt resume command carries no inline
		// AGENTDECK_PROFILE prefix, and this branch returns before the
		// fallback recreate path that would otherwise set it. Must run
		// BEFORE RespawnPane — tmux applies session env when it starts the
		// process, so asserting it after RespawnPane only affects the next
		// spawn, not the one just started.
		i.ensureProfileEnv()
		i.ensureClaudeConfigDirEnv()

		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			sessionLog.Info("restart_gemini_respawn_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart Gemini session: %w", err)
		}

		sessionLog.Info("restart_gemini_respawn_succeeded")

		// Persist .sid sidecar so hook events after restart can be correlated
		WriteHookSessionAnchor(i.ID, i.GeminiSessionID)

		// Issue #666: sweep cross-tmux duplicates on the respawn path too.
		i.sweepDuplicateToolSessions()

		i.Status = StatusWaiting
		return nil
	}

	// If OpenCode session AND tmux session exists, use respawn-pane
	if i.Tool == "opencode" && i.tmuxSession != nil && i.tmuxSession.Exists() {
		// Refresh from OpenCode state before deciding the resume target.
		i.updateOpenCodeSession(true)

		// Try to get session ID from tmux environment if not already set
		// (async detection stores it there but Instance might not have been saved)
		if i.OpenCodeSessionID == "" {
			if envID, err := i.tmuxSession.GetEnvironment("OPENCODE_SESSION_ID"); err == nil && envID != "" {
				i.OpenCodeSessionID = envID
				i.OpenCodeDetectedAt = time.Now()
				sessionLog.Info("restart_opencode_recovered_id", slog.String("session_id", envID))
			}
		}

		if i.OpenCodeSessionID == "" {
			i.OpenCodeStartedAt = time.Now().UnixMilli()
		}
		// Reuse the canonical builder so live-pane respawns retain the same
		// configured command, model, agent, and SSE flags as every other start.
		rawCmd := i.buildOpenCodeCommand("opencode")
		prepInput := rawCmd
		resumeCmd, containerName, err := i.prepareCommand(prepInput)
		if err != nil {
			// #1924: a restart that fails here left StatusError with no reason.
			i.recordPrepareFailure(prepInput, err)
			return err
		}
		if containerName != "" {
			i.SandboxContainer = containerName
		}
		sessionLog.Info("restart_opencode_respawn", slog.String("command", resumeCmd))

		// #1822 F2: opencode's rebuilt resume command carries no inline
		// AGENTDECK_PROFILE prefix, and this branch returns before the
		// fallback recreate path that would otherwise set it. Must run
		// BEFORE RespawnPane — see the Claude branch above for why.
		i.ensureProfileEnv()
		i.ensureClaudeConfigDirEnv()

		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			sessionLog.Info("restart_opencode_respawn_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart OpenCode session: %w", err)
		}

		// If no session ID, start async detection
		if i.OpenCodeSessionID == "" {
			go i.detectOpenCodeSessionAsync()
		}

		sessionLog.Info("restart_opencode_respawn_succeeded")

		// Persist .sid sidecar so hook events after restart can be correlated
		if i.OpenCodeSessionID != "" {
			WriteHookSessionAnchor(i.ID, i.OpenCodeSessionID)
		}

		// Issue #666: sweep cross-tmux duplicates on the respawn path too.
		i.sweepDuplicateToolSessions()

		i.Status = StatusWaiting
		return nil
	}

	// For Codex: try to update session ID, but only if we don't already have one.
	// When we already have a known session ID (from the database), trust it —
	// the disk scan can return a wrong ID when multiple instances share the same
	// project_path. The process probe is authoritative but only works when the
	// process is running, which it isn't during a restart.
	if IsCodexCompatible(i.Tool) && i.CodexSessionID == "" {
		i.mu.Lock()
		i.pendingCodexRestartWarning = ""
		i.mu.Unlock()
		if missingDep := i.updateCodexSession(i.collectOtherCodexSessionIDs(), true); missingDep != "" {
			i.mu.Lock()
			i.pendingCodexRestartWarning = codexProbeMissingWarning(missingDep)
			i.mu.Unlock()
			sessionLog.Warn("codex_probe_dep_missing_for_restart", slog.String("dependency", missingDep))
		}
	}

	// If Codex session AND tmux session exists, use respawn-pane
	if IsCodexCompatible(i.Tool) && i.tmuxSession != nil && i.tmuxSession.Exists() {
		// Try to get session ID from tmux environment if not already set
		if i.CodexSessionID == "" {
			if envID, err := i.tmuxSession.GetEnvironment("CODEX_SESSION_ID"); err == nil && envID != "" {
				i.CodexSessionID = envID
				i.CodexDetectedAt = time.Now()
				sessionLog.Info("restart_codex_recovered_id", slog.String("session_id", envID))
			}
		}

		if i.CodexSessionID == "" {
			i.CodexStartedAt = time.Now().UnixMilli()
		}
		prepInput := i.buildCodexCommand(i.Command)
		resumeCmd, containerName, err := i.prepareCommand(prepInput)
		if err != nil {
			// #1924: a restart that fails here left StatusError with no reason.
			i.recordPrepareFailure(prepInput, err)
			return err
		}
		if containerName != "" {
			i.SandboxContainer = containerName
		}
		sessionLog.Info("restart_codex_respawn", slog.String("command", resumeCmd))

		// #1822 F2: belt-and-suspenders to the inline prefix buildCodexCommand
		// already injects; this branch returns before the fallback recreate
		// path that would otherwise set it. Must run BEFORE RespawnPane —
		// see the Claude branch above for why.
		i.ensureProfileEnv()
		i.ensureClaudeConfigDirEnv()

		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			sessionLog.Info("restart_codex_respawn_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart Codex session: %w", err)
		}

		// If no session ID, start async detection
		if i.CodexSessionID == "" {
			go i.detectCodexSessionAsync()
		}

		sessionLog.Info("restart_codex_respawn_succeeded")

		// Persist .sid sidecar so hook events after restart can be correlated
		WriteHookSessionAnchor(i.ID, i.CodexSessionID)

		// Issue #666: sweep cross-tmux duplicates on the respawn path too.
		i.sweepDuplicateToolSessions()

		i.Status = StatusWaiting
		return nil
	}

	// If Cursor session AND tmux session exists, use respawn-pane.
	if i.Tool == "cursor" && i.tmuxSession != nil && i.tmuxSession.Exists() {
		prepInput := i.buildCursorCommand(i.Command, true)
		resumeCmd, containerName, err := i.prepareCommand(prepInput)
		if err != nil {
			// #1924: a restart that fails here left StatusError with no reason.
			i.recordPrepareFailure(prepInput, err)
			return err
		}
		if containerName != "" {
			i.SandboxContainer = containerName
		}
		sessionLog.Info("restart_cursor_respawn", slog.String("command", resumeCmd))

		// #1822 F2: must run BEFORE RespawnPane — see the Claude branch above
		// for why (tmux applies session env at process start, not after).
		i.ensureProfileEnv()
		i.ensureClaudeConfigDirEnv()

		if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
			sessionLog.Info("restart_cursor_respawn_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to restart Cursor session: %w", err)
		}

		sessionLog.Info("restart_cursor_respawn_succeeded")
		i.sweepDuplicateToolSessions()
		i.CaptureLoadedMCPs()
		i.Status = StatusWaiting
		return nil
	}

	// If custom tool with session resume support AND tmux session exists, use respawn-pane.
	// Resolve id and ToolDef once (avoid double GetGenericSessionID / env races
	// that could produce an empty --resume '' argv). Concurrent config reload
	// can drop the custom entry — never deref nil.
	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		toolDef := GetToolDef(i.Tool)
		sessionID := i.GetGenericSessionID()
		if toolDef != nil && toolDef.ResumeFlag != "" && sessionID != "" {
			// The session ID env var is propagated via host-side SetEnvironment after tmux start.
			// Same shape as buildGenericCommand (shared helper) so start/restart cannot drift.
			dangerous := ""
			if toolDef.DangerousMode && toolDef.DangerousFlag != "" {
				dangerous = toolDef.DangerousFlag
			}
			rawCmd := i.buildRestartEnvPrefix() + formatGenericResumeCommand(
				i.Command, toolDef.ResumeFlag, sessionID, dangerous)
			resumeCmd, containerName, err := i.prepareCommand(rawCmd)
			if err != nil {
				// #1924: a restart that fails here left StatusError with no reason.
				i.recordPrepareFailure(rawCmd, err)
				return err
			}
			if containerName != "" {
				i.SandboxContainer = containerName
			}

			// The resume command embeds the conversation id, so it is not
			// logged; the fingerprint identifies the binding without
			// disclosing it (see fingerprintSessionID).
			sessionLog.Info("restart_generic_respawn",
				slog.String("tool", logging.SanitizeValue(i.Tool)),
				slog.String("session_id_fingerprint", fingerprintSessionID(sessionID)))

			// #1822 F2: the generic resume command is a bare
			// `<cmd> <resumeFlag> <sid>` with no inline AGENTDECK_PROFILE prefix,
			// and this branch returns before the fallback recreate path. Must run
			// BEFORE RespawnPane — see the Claude branch above for why.
			i.ensureProfileEnv()
			i.ensureClaudeConfigDirEnv()

			if err := i.tmuxSession.RespawnPane(resumeCmd); err != nil {
				sessionLog.Info(
					"restart_generic_respawn_failed",
					slog.String("tool", i.Tool),
					slog.String("error", err.Error()),
				)
				return fmt.Errorf("failed to restart %s session: %w", i.Tool, err)
			}

			sessionLog.Info("restart_generic_respawn_succeeded", slog.String("tool", i.Tool))

			i.loadCustomPatternsFromConfig() // Reload custom patterns
			i.Status = StatusWaiting
			return nil
		}
		// toolDef vanished or id emptied between checks — fall through to recreate.
	}

	mcpLog.Debug("restart_fallback_recreate")

	// (the watcher was already superseded at the top of restart(), which covers
	// this path and every early-returning respawn fast path alike.)

	// Kill old tmux session to prevent orphans before recreating (#138)
	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		mcpLog.Debug("restart_killing_old_session", slog.String("session_name", i.tmuxSession.Name))
		if killErr := i.tmuxSession.Kill(); killErr != nil {
			mcpLog.Warn("restart_kill_old_session_failed", slog.String("error", killErr.Error()))
		}
	}

	// Fallback: recreate tmux session (for dead sessions or unknown ID)
	i.recreateTmuxSession()

	// Prepare scratch CLAUDE_CONFIG_DIR for non-conductor claude workers
	// on the restart path too (issue #59, v1.7.68).
	i.prepareWorkerScratchConfigDirForSpawn() // also runs plugin auto-install per fix C1

	var command string
	if IsClaudeCompatible(i.Tool) && i.ClaudeSessionID != "" {
		command = i.buildClaudeResumeCommand()
	} else if i.Tool == "gemini" && i.GeminiSessionID != "" {
		command = i.buildGeminiCommand("gemini")
	} else if i.Tool == "opencode" && i.OpenCodeSessionID != "" {
		command = i.buildOpenCodeCommand("opencode")
	} else if IsCodexCompatible(i.Tool) && i.CodexSessionID != "" {
		command = i.buildCodexCommand(i.Command)
	} else if i.Tool == "hermes" {
		// Re-capture the pane's hermes session ID on EVERY restart (hermes
		// doesn't export it). Overwriting rather than caching once self-heals a
		// stale ID: if the previously-resumed session was pruned, the query
		// returns the current most-recent session, or "" — in which case
		// buildHermesCommand starts fresh instead of resuming a dead ID forever.
		// Scoped to the working dir to avoid picking up an unrelated session.
		i.HermesSessionID = captureHermesSessionID(i.EffectiveWorkingDir())
		// Drop any stale hook state from the previous process, then seed a
		// synthetic "waiting" baseline. Hermes fires on_session_start only at
		// the FIRST TURN of a brand-new session (not at process launch), so
		// after a restart NOTHING writes a hook status until the user types:
		// without the clear a pre-restart on_session_finalize "dead" shows an
		// error ✕ for the whole freshness window, and without the seed the
		// session shows no state at all. The freshly respawned process sits at
		// its prompt, which is exactly "waiting".
		if _, err := i.seedHermesHookGeneration("waiting", false); err != nil {
			return fmt.Errorf("seed hermes restart baseline: %w", err)
		}
		command = i.buildHermesCommand(i.Command)
	} else if i.Tool == "deepseek" {
		// Re-discover the workspace's newest dsh session on EVERY restart, for
		// the same self-healing reason as hermes: a pruned session yields the
		// current newest, or "" — in which case the launch boots fresh instead
		// of resuming a dead ID forever. A no-op when [deepseek].resume_flag is
		// unset (the default), where restart is a plain re-boot and dsh's own
		// persistence under $DSH_HOME keeps the conversation reachable.
		i.refreshDeepSeekSessionID()
		// A headless restart replays the recorded task; every other profile
		// re-boots as before. deepSeekRestartCommand refuses rather than
		// rebuilding a taskless one-shot invocation.
		dsCommand, dsErr := i.deepSeekRestartCommand()
		if dsErr != nil {
			return dsErr
		}
		command = dsCommand
	} else {
		// Route to appropriate command builder based on tool
		switch {
		case IsClaudeCompatible(i.Tool):
			command = i.buildClaudeCommand(i.Command)
		case i.Tool == "gemini":
			command = i.buildGeminiCommand(i.Command)
		case i.Tool == "opencode":
			command = i.buildOpenCodeCommand(i.Command)
			// Record start time for async session ID detection
			i.OpenCodeStartedAt = time.Now().UnixMilli()
		case IsCodexCompatible(i.Tool):
			command = i.buildCodexCommand(i.Command)
			// Record start time for async session ID detection
			i.CodexStartedAt = time.Now().UnixMilli()
		case i.Tool == "pi":
			command = i.buildPiCommand(i.Command)
		case i.Tool == "copilot":
			command = i.buildCopilotCommand(i.Command)
		case i.Tool == "crush":
			command = i.buildCrushCommand(i.Command)
		case i.Tool == "cursor":
			command = i.buildCursorCommand(i.Command, true)
		default:
			// Check if this is a custom tool with session resume config
			if toolDef := GetToolDef(i.Tool); toolDef != nil {
				command = i.buildGenericCommand(i.Command)
			} else {
				command = i.buildRestartEnvPrefix() + i.Command
				if i.Command == "" && command != "" {
					shell := ""
					if !i.IsSandboxed() && i.SSHHost == "" {
						shell = os.Getenv("SHELL")
					}
					if shell == "" {
						shell = "/bin/sh"
					}
					command += "exec " + shellescape.Quote(shell)
				}
			}
		}
	}
	command, containerName, err := i.prepareCommand(command)
	if err != nil {
		i.recordPrepareFailure(command, err) // #1924, sister path
		return err
	}
	if containerName != "" {
		i.SandboxContainer = containerName
	}

	// Load custom patterns for status detection (for custom tools).
	i.loadCustomPatternsFromConfig()

	// Build tmux option overrides from config (e.g. allow-passthrough = "all").
	// Sandbox sessions also get remain-on-exit for dead-pane detection.
	i.tmuxSession.OptionOverrides = i.buildTmuxOptionOverrides()
	i.tmuxSession.RunCommandAsInitialProcess = i.IsSandboxed() || i.Tool != "shell"
	i.applyLaunchSettingsFromConfig()

	// Re-assert the declarative skill+mcp loadout before respawn — sister
	// call to Start(); config edits land on the next restart this way.
	ApplyConfiguredLoadout(i)

	mcpLog.Debug("restart_starting_new_session", slog.String("command", command))

	if err := i.tmuxSession.Start(command); err != nil {
		mcpLog.Debug("restart_start_failed", slog.String("error", err.Error()))
		// #1924: Start() and its sister path have recorded this since #1580,
		// but restart() never did — so the one case where the user has just
		// changed something and is most likely to ask "why?" was the one that
		// set StatusError with nothing to show for it.
		i.recordTmuxStartFailure(command, err)
		i.Status = StatusError
		return fmt.Errorf("failed to restart tmux session: %w", err)
	}

	mcpLog.Debug("restart_start_succeeded")

	// CFG-07: emit the config-resolution log on restart too — triage must not
	// go dark on the exact scenario most likely to need debugging.
	if IsClaudeCompatible(i.Tool) {
		i.logClaudeConfigResolution()
	}

	// Set AGENTDECK_INSTANCE_ID for Claude hooks to identify this session
	// This enables real-time status updates via Stop/SessionStart hooks
	if err := i.tmuxSession.SetEnvironment("AGENTDECK_INSTANCE_ID", i.ID); err != nil {
		sessionLog.Warn("set_instance_id_failed", slog.String("error", err.Error()))
	}

	// Set AGENTDECK_PROFILE (host-side, tool-agnostic) so a bare `agent-deck`
	// command run inside this session resolves the session's own profile rather
	// than falling back to "default". Covers shells/OpenCode/etc. that have no
	// inline env-prefix injection of their own.
	i.ensureProfileEnv()
	i.ensureClaudeConfigDirEnv()

	// Propagate all known tool session IDs to the tmux environment (host-side).
	// This covers Restart() which uses buildClaudeResumeCommand() and similar
	// builders that no longer embed "tmux set-environment" in the shell string.
	i.SyncSessionIDsToTmux()

	// Kill any other agentdeck tmux session that duplicates this instance.
	// Routed through sweepDuplicateToolSessions so the fallback restart path
	// gets the same tool-session-id guard (#596) AND instance-id guard (#678)
	// as the respawn-pane paths. The instance-id guard is what catches shell
	// / placeholder sessions that have no tool-level session id.
	i.sweepDuplicateToolSessions()

	// Re-capture MCPs after restart
	i.CaptureLoadedMCPs()

	// Start async session ID detection for OpenCode (if no ID yet)
	if i.Tool == "opencode" && i.OpenCodeSessionID == "" {
		go i.detectOpenCodeSessionAsync()
	}

	// Start async session ID detection for Codex (if no ID yet)
	if IsCodexCompatible(i.Tool) && i.CodexSessionID == "" {
		go i.detectCodexSessionAsync()
	}

	// Start as WAITING - will go GREEN on next tick if Claude shows busy indicator
	if command != "" {
		i.Status = StatusWaiting
	} else {
		i.Status = StatusIdle
	}

	// #1870: this is the only path that mints a new tmux session name, so it is
	// the only place that has to make it durable. Callers that never save --
	// mcp/skill/plugin attach|detach --restart -- used to leave the killed
	// session's name on disk, so agent-deck reported a live session as broken
	// and orphaned its tmux session. Status travels with it because the two are
	// one fact about the restart. See restart_tmux_persist.go for why this is a
	// targeted write and why a failure here is recorded rather than returned.
	i.recordRestartOutcome()

	return nil
}

// RestartFresh restarts the current tool without resuming the existing tool session.
// This recreates the tmux session and clears the stored tool session binding first,
// so the next start gets a brand-new tool session ID.
func (i *Instance) RestartFresh() error {
	i.prepareRestartMCPConfig()

	i.clearSessionBindingForFreshStart()

	// #1775: same reasoning as the restart-fallback-recreate path above, and
	// likewise unconditional — an old watcher whose session is already gone is
	// the one most likely to write a spurious failure.
	i.bumpSpawnGenAndBarrier()

	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		if killErr := i.tmuxSession.Kill(); killErr != nil {
			mcpLog.Warn("restart_fresh_kill_old_session_failed", slog.String("error", killErr.Error()))
		}
	}

	i.recreateTmuxSession()

	if err := i.Start(); err != nil {
		i.Status = StatusError
		return fmt.Errorf("failed to restart session fresh: %w", err)
	}

	return nil
}

// buildClaudeResumeCommand builds the claude resume command with proper config options
// Respects: CLAUDE_CONFIG_DIR, dangerous_mode, and [shell].env_files + init_script
// CLAUDE_SESSION_ID is set via host-side SetEnvironment (called by SyncSessionIDsToTmux after restart)
func (i *Instance) buildClaudeResumeCommand() string {
	// Source env files and init_script so resumed sessions have the same
	// shell environment as freshly started ones (fixes #409).
	envPrefix := i.buildEnvSourceCommand()

	// Get the configured Claude command (e.g., "claude", "cdw", "cdp"),
	// resolved per instance: conductor > group (ancestor-walk) > global.
	// A per-session custom command (i.Command, e.g. from `launch -c
	// claude-dbx`) takes precedence — mirroring buildClaudeCommandWithMessage,
	// where the per-session Command field is a distinct axis that
	// GetClaudeCommandForInstance never sees (see its doc comment). Without
	// this, resume falls back to the literal "claude" binary and bypasses
	// the wrapper script that sets up gateway env vars.
	//
	// The --fork-session / --session-id exclusions reuse the same predicate
	// adoptExplicitClaudeSessionID uses for the same discrimination: a fork
	// target's Command holds the whole compound fork command (cd '<path>' &&
	// ... exec claude --session-id "<uuid>" --resume <parent-id> --fork-session)
	// baked by buildClaudeForkCommandForTarget and persisted to disk. The
	// IsForkAwaitingStart sentinel that routes the first Start() around this
	// function is transient (json:"-"), so it is gone after the first start —
	// a subsequent Restart() reaches here with that compound string in Command.
	// Treating it as a custom command splices it in as the claude binary:
	// --fork-session and the parent's id come back (double-counting the parent
	// conversation) and the pane dies with `exec: cd: not found` (exit 127)
	// because the string starts with `exec cd`. Same shape as the #1830
	// finding, one path over.
	claudeCmd := GetClaudeCommandForInstance(i)
	customCommand := i.Command != "" && i.Command != "claude" &&
		!commandHasToken(i.Command, "--fork-session") &&
		!commandHasToken(i.Command, "--session-id")
	if customCommand {
		claudeCmd = i.Command
	}

	// #1791/#1822 F5: use the shared `export ...;` prefix, not a bare
	// `KEY=val cmd` prefix — see the identical comment in
	// buildClaudeCommandWithMessage for why (exit_to_shell's trailing
	// `exec "$SHELL" -i` only inherits exported vars, not a single-command
	// prefix scoped to the claude invocation alone). #1822 F3: skip the
	// CLAUDE_CONFIG_DIR export when claudeCmd resolves to a config-level
	// alias — the alias handles it itself. A per-session custom command is
	// the same axis as the fresh-start custom-command path, so the F3 gate
	// does not apply there (pass false), matching buildClaudeCommandWithMessage.
	bashExportPrefix := i.buildBashExportPrefix(!customCommand && claudeCmd != "claude")

	// Get per-session permission settings (falls back to config if not persisted)
	opts := i.GetClaudeOptions()
	if opts == nil {
		userConfig, _ := LoadUserConfig()
		opts = NewClaudeOptions(userConfig)
	}

	// Check if session has actual conversation data.
	// If not, use --session-id instead of --resume to avoid "No conversation found" error.
	//
	// Issue #662: a bounded retry-once at this call site covers the
	// SessionEnd-flush race — the helper is called synchronously with
	// restart, and Claude may still be flushing its jsonl for a few
	// hundred milliseconds after the SessionEnd hook fires. Waiting 200ms
	// and re-checking turns a shipped-fresh-session into a resume for the
	// common flush-race case without slowing the happy path (retry only
	// fires when the first check comes back negative AND we have a
	// non-empty ClaudeSessionID).
	// #1815 identity guard: refuse outright when the id about to be resumed
	// is not this session's own recorded conversation id (missing recorded id,
	// or a mismatch). The refused id is replaced by a freshly minted one so
	// the session starts clean instead of claiming another session's id.
	if decision := i.resumeIdentityAllowed(i.ClaudeSessionID); !decision.Allow {
		refused := i.ClaudeSessionID
		i.logResumeRefusal(refused, decision.Reason)
		fresh := i.replaceRefusedClaudeSessionID()
		extraFlags := i.buildClaudeExtraFlags(opts)
		// `fresh` is minted here so it is trusted, but `refused` and the path
		// are session-supplied; sanitize both (CodeQL go/log-injection).
		sessionLog.Info("resume: id="+fresh+" reason=identity_guard_fresh_session",
			slog.String("instance_id", logging.SanitizeValue(i.ID)),
			slog.String("claude_session_id", fresh),
			slog.String("refused_session_id", logging.SanitizeValue(refused)),
			slog.String("path", logging.SanitizeValue(i.ProjectPath)),
			slog.String("reason", "identity_guard_fresh_session"))
		return fmt.Sprintf("%s%s%s --session-id %s%s",
			envPrefix, bashExportPrefix, claudeCmd, fresh, extraFlags)
	}

	// #1815 / CodeQL go/log-injection: i.ClaudeSessionID, i.ID and
	// i.ProjectPath are session-supplied, so every log call in this function
	// — including the two Debug sibling calls below, not only the final Info
	// record — sanitizes them. Computed once, before first use.
	safeSID := logging.SanitizeValue(i.ClaudeSessionID)
	safeInstID := logging.SanitizeValue(i.ID)
	safePath := logging.SanitizeValue(i.ProjectPath)

	useResume := canResumeClaudeSession(i, i.ClaudeSessionID)
	if !useResume && i.ClaudeSessionID != "" && i.TranscriptIsResolvableLocally() {
		time.Sleep(resumeCheckRetryDelay)
		useResume = canResumeClaudeSession(i, i.ClaudeSessionID)
		sessionLog.Debug(
			"session_data_retry_after_wait",
			slog.String("session_id", safeSID),
			slog.Duration("wait", resumeCheckRetryDelay),
			slog.Bool("use_resume_after_retry", useResume),
		)
	}
	sessionLog.Debug(
		"session_data_build_resume",
		slog.String("session_id", safeSID),
		slog.String("path", safePath),
		slog.Bool("use_resume", useResume),
	)

	// OBS-02: per-call grep-stable Info record. One emission per
	// buildClaudeResumeCommand call — NOT sync.Once'd. See CONTEXT Decision 2.
	// Every Start / StartWithMessage / Restart dispatch that routes through
	// this helper produces exactly one "resume: id=<id> reason=<why>" line.
	if useResume {
		sessionLog.Info("resume: id="+safeSID+" reason=conversation_data_present",
			slog.String("instance_id", safeInstID),
			slog.String("claude_session_id", safeSID),
			slog.String("path", safePath),
			slog.String("reason", "conversation_data_present"))
	} else {
		sessionLog.Info("resume: id="+safeSID+" reason=session_id_flag_no_jsonl",
			slog.String("instance_id", safeInstID),
			slog.String("claude_session_id", safeSID),
			slog.String("path", safePath),
			slog.String("reason", "session_id_flag_no_jsonl"))
	}

	// Delegate flag assembly to buildClaudeExtraFlags so restart stays in
	// lockstep with the start path. Handles permission modes (dangerous /
	// auto / allow), --add-dir for parented sessions, and --channels for
	// plugin channel subscriptions. Without this, any flag added to
	// buildClaudeExtraFlags silently disappears on session restart — the
	// phase-5 loopback regression (TestResumeCommandAppendsChannels).
	extraFlags := i.buildClaudeExtraFlags(opts)

	// CLAUDE_SESSION_ID is propagated via host-side SetEnvironment (SyncSessionIDsToTmux)
	// after the tmux session is restarted. No inline tmux set-environment in the shell string
	// (which silently fails inside Docker sandbox containers).
	if useResume {
		return fmt.Sprintf("%s%sexec %s --resume %s%s",
			envPrefix, bashExportPrefix, claudeCmd, i.ClaudeSessionID, extraFlags)
	}
	// Session was never interacted with - use --session-id to create fresh session.
	return fmt.Sprintf("%s%sexec %s --session-id %s%s",
		envPrefix, bashExportPrefix, claudeCmd, i.ClaudeSessionID, extraFlags)
}

// SetGeminiModel sets the Gemini model for this session and triggers a restart if running.
func (i *Instance) SetGeminiModel(model string) error {
	i.GeminiModel = model
	sessionLog.Debug(
		"gemini_model_set",
		slog.String("model", model),
		slog.String("session_id", i.ID),
		slog.String("title", i.Title),
	)

	// Restart if the session is running so it picks up the new model
	if i.Exists() {
		return i.Restart()
	}
	return nil
}

// SupportsLaunchModel reports whether a newly-created session can receive an
// explicit model override through Agent Deck's generic session creation path.
func SupportsLaunchModel(tool string) bool {
	return IsClaudeCompatible(tool) || tool == "gemini" || tool == "opencode" || IsCodexCompatible(tool)
}

// ApplyLaunchModel stores a per-session model override in the tool-specific
// field that the relevant command builder already reads on start/restart.
func (i *Instance) ApplyLaunchModel(model string) error {
	model = strings.TrimSpace(model)
	if i == nil || model == "" {
		return nil
	}

	switch {
	case IsClaudeCompatible(i.Tool):
		opts := i.GetClaudeOptions()
		if opts == nil {
			userConfig, _ := LoadUserConfig()
			opts = NewClaudeOptions(userConfig)
		}
		opts.Model = model
		return i.SetClaudeOptions(opts)
	case i.Tool == "gemini":
		i.GeminiModel = model
		return nil
	case i.Tool == "opencode":
		opts := i.GetOpenCodeOptions()
		if opts == nil {
			userConfig, _ := LoadUserConfig()
			opts = NewOpenCodeOptions(userConfig)
		}
		opts.Model = model
		return i.SetOpenCodeOptions(opts)
	case IsCodexCompatible(i.Tool):
		opts := i.GetCodexOptions()
		if opts == nil {
			userConfig, _ := LoadUserConfig()
			opts = NewCodexOptions(userConfig)
		}
		opts.Model = model
		return i.SetCodexOptions(opts)
	default:
		return fmt.Errorf("model selection is not supported for tool %q", i.Tool)
	}
}

// ClearLaunchModel removes any per-session model override so the session falls
// back to the configured default ([claude].default_model, etc.) on the next
// start/restart (#1436). Mirrors ApplyLaunchModel across tools; a no-op when
// no override is set.
func (i *Instance) ClearLaunchModel() error {
	if i == nil {
		return nil
	}
	switch {
	case IsClaudeCompatible(i.Tool):
		opts := i.GetClaudeOptions()
		if opts == nil {
			return nil
		}
		opts.Model = ""
		return i.SetClaudeOptions(opts)
	case i.Tool == "gemini":
		i.GeminiModel = ""
		return nil
	case i.Tool == "opencode":
		opts := i.GetOpenCodeOptions()
		if opts == nil {
			return nil
		}
		opts.Model = ""
		return i.SetOpenCodeOptions(opts)
	case IsCodexCompatible(i.Tool):
		opts := i.GetCodexOptions()
		if opts == nil {
			return nil
		}
		opts.Model = ""
		return i.SetCodexOptions(opts)
	default:
		return nil
	}
}

// CanRestart returns true if the session can be restarted
// For Claude sessions with known ID: can always restart (interrupt and resume)
// For Gemini sessions with known ID: can always restart (interrupt and resume)
// For OpenCode sessions with known ID: can always restart (interrupt and resume)
// For Codex sessions with known ID: can always restart (interrupt and resume)
// For custom tools with session resume config: can restart if session ID available
// For other sessions: only if dead/error state
func (i *Instance) CanRestart() bool {
	// Gemini sessions with known session ID can always be restarted
	if i.Tool == "gemini" && i.GeminiSessionID != "" {
		return true
	}

	// Claude sessions with known session ID can always be restarted
	if IsClaudeCompatible(i.Tool) && i.ClaudeSessionID != "" {
		return true
	}

	// Claude sessions without ID can still restart (will start fresh or
	// resume the latest JSONL via ensureClaudeSessionIDFromDisk). REQ-7
	// reopen #911: custom-command Claude sessions (Tool=claude with a
	// wrapper Command) bypass happy-path session-id capture and have an
	// intentionally empty ClaudeSessionID. Without this branch they fall
	// to the dead-or-error fallback below and the registry refuses
	// restart even when the underlying tmux pane is alive — the false-
	// error class this issue tracks. Mirrors the opencode/codex policy.
	if IsClaudeCompatible(i.Tool) {
		return true
	}

	// OpenCode sessions with known session ID can always be restarted
	if i.Tool == "opencode" && i.OpenCodeSessionID != "" {
		return true
	}

	// OpenCode sessions without ID can still restart (will start fresh)
	// This allows restart even before session ID is detected
	if i.Tool == "opencode" {
		return true
	}

	// Codex sessions with known session ID can always be restarted
	if IsCodexCompatible(i.Tool) && i.CodexSessionID != "" {
		return true
	}

	// Codex sessions without ID can still restart (will start fresh)
	// This allows restart even before session ID is detected
	if IsCodexCompatible(i.Tool) {
		return true
	}

	// Pi sessions are scoped to an Agent Deck instance-specific session dir and
	// can always be relaunched with --continue.
	if i.Tool == "pi" {
		return true
	}

	// Cursor sessions resume via --continue on restart (workspace-scoped chat).
	if i.Tool == "cursor" {
		return true
	}

	// Hermes resumes via `--resume <id>`; the ID is captured from
	// `hermes sessions list` at restart time. Always restartable (resumes when a
	// session is found, else starts fresh) rather than only when dead/errored,
	// which left a live hermes session silently un-restartable.
	if i.Tool == "hermes" {
		return true
	}

	// DeepSeek Harness is restartable: a restart re-boots the same profile in
	// the same workspace against the same $DSH_HOME, so every conversation dsh
	// persisted stays reachable whether or not the configured profile's app
	// accepts a resume flag. Gating this on "dead or errored" would leave a live
	// dsh session silently un-restartable, the exact hermes bug above.
	//
	// The headless profile is the exception, and it is a REAL one rather than a
	// conservative guess: a one-shot's task is its whole invocation, so without
	// a recorded task there is nothing to restart INTO — the rebuild would be
	// `dsh --profile headless`, which dsh rejects. Claiming restartability there
	// promises an action that can only fail (PR #1942 review, P1c).
	if i.Tool == "deepseek" {
		if i.deepSeekPromptDelivery() == DeepSeekPromptCommandLine {
			return strings.TrimSpace(i.DeepSeekTask) != ""
		}
		return true
	}

	// Custom tools: check if they have session resume support
	if i.CanRestartGeneric() {
		return true
	}

	// Other sessions: only if dead or error
	return i.Status == StatusError || i.tmuxSession == nil || !i.tmuxSession.Exists()
}

// CanRestartFresh returns true when the session has a known tool session binding
// that can be intentionally discarded to start with a new session ID.
func (i *Instance) CanRestartFresh() bool {
	if IsClaudeCompatible(i.Tool) {
		return i.ClaudeSessionID != ""
	}
	if i.Tool == "gemini" {
		return i.GeminiSessionID != ""
	}
	if i.Tool == "opencode" {
		return i.OpenCodeSessionID != ""
	}
	if i.Tool == "codex" {
		return i.CodexSessionID != ""
	}
	// Hermes fresh-restart discards the resume binding and starts a new session.
	if i.Tool == "hermes" {
		return true
	}
	// DeepSeek fresh-restart is only meaningful when there is a resume binding
	// to discard. With [deepseek].resume_flag unset (the default) every restart
	// is already fresh, so offering "restart fresh" as a distinct action would
	// promise a difference that does not exist.
	if i.Tool == "deepseek" {
		return DeepSeekSupportsResume()
	}
	return i.CanRestartGeneric()
}

// CanFork returns true if this session can be forked
func (i *Instance) CanFork() bool {
	// Gemini CLI doesn't support forking
	if i.Tool == "gemini" {
		return false
	}

	// DeepSeek Harness 0.1.0-rc.6 has no fork/branch command: the launcher owns
	// --profile/--patch/the dumps, and neither shipped app exposes one. Falling
	// through to the Claude branch below would also return false, but only by
	// accident (empty ClaudeSessionID) — say it explicitly so a future Claude
	// change cannot silently start offering a fork dsh cannot perform.
	if i.Tool == "deepseek" {
		return false
	}

	// OpenCode sessions can fork if session ID is recent
	if i.Tool == "opencode" {
		return i.CanForkOpenCode()
	}

	// Pi sessions fork by source JSONL path under Agent Deck's per-instance
	// Pi session directory. The launch command validates that a JSONL exists.
	if i.Tool == "pi" {
		return i.CanForkPi()
	}

	// Codex-compatible sessions fork via `codex fork <sid>`, gated on a
	// flushed on-disk rollout (same invariant as `codex resume`).
	if IsCodexCompatible(i.Tool) {
		return i.CanForkCodex()
	}

	// Claude sessions can fork if session ID is recent
	if i.ClaudeSessionID == "" {
		return false
	}
	return time.Since(i.ClaudeDetectedAt) < 5*time.Minute
}

// CanForkOpenCode returns true if this OpenCode session can be forked
func (i *Instance) CanForkOpenCode() bool {
	sessionID, err := normalizeToolSessionID(FieldOpenCodeSessionID, i.OpenCodeSessionID)
	return i.Tool == "opencode" && err == nil && sessionID != "" && sessionID == strings.TrimSpace(i.OpenCodeSessionID) && time.Since(i.OpenCodeDetectedAt) < 5*time.Minute
}

// CanForkPi returns true if this Pi session can be forked by Agent Deck.
func (i *Instance) CanForkPi() bool {
	if i.Tool != "pi" || i.ID == "" {
		return false
	}
	// For local non-sandboxed Pi sessions, require an actual source JSONL so
	// CLI/TUI fork attempts fail before creating an immediately-dead child tmux
	// pane. Remote/sandboxed sessions use target-side $HOME, which this process
	// cannot inspect, so the launch command performs the runtime validation.
	if i.SSHHost == "" && !i.IsSandboxed() {
		return i.hasLocalPiSessionFile()
	}
	return true
}

func (i *Instance) hasLocalPiSessionFile() bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	sessionDir := filepath.Join(home, ".pi", "agent-deck", i.ID)
	found := false
	_ = filepath.WalkDir(sessionDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".jsonl") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// Fork returns the command to create a forked Claude session
// Uses capture-resume pattern: starts fork in print mode to get new session ID,
// stores in tmux environment, then resumes interactively
// Deprecated: Use ForkWithOptions instead
func (i *Instance) Fork(newTitle, newGroupPath string) (string, error) {
	return i.ForkWithOptions(newTitle, newGroupPath, nil)
}

// ForkWithOptions returns the command to create a forked Claude session with custom options
// Uses capture-resume pattern: starts fork in print mode to get new session ID,
// stores in tmux environment, then resumes interactively
func (i *Instance) ForkWithOptions(newTitle, newGroupPath string, opts *ClaudeOptions) (string, error) {
	projectPath := i.ProjectPath
	if opts != nil && opts.WorkDir != "" {
		projectPath = opts.WorkDir
	}
	target := NewInstance(newTitle, projectPath)
	if newGroupPath != "" {
		target.GroupPath = newGroupPath
	} else {
		target.GroupPath = i.GroupPath
	}
	target.Tool = "claude"

	return i.buildClaudeForkCommandForTarget(target, opts)
}

func (i *Instance) buildClaudeForkCommandForTarget(target *Instance, opts *ClaudeOptions) (string, error) {
	if target == nil {
		return "", fmt.Errorf("cannot build fork command: target instance is nil")
	}

	if !i.CanFork() {
		return "", fmt.Errorf("cannot fork: no active Claude session")
	}

	// #1815: a fork is a `--resume <source id> --fork-session`, so it needs
	// the same identity verification as any other resume — forking a
	// transcript this session does not own would clone another session's
	// context (and its authority) into a new pane.
	if decision := i.resumeIdentityAllowed(i.ClaudeSessionID); !decision.Allow {
		i.logResumeRefusal(i.ClaudeSessionID, "fork_"+decision.Reason)
		return "", fmt.Errorf("cannot fork: conversation id %q is not verified as this session's own (%s)",
			i.ClaudeSessionID, decision.Reason)
	}

	workDir := target.ProjectPath

	// IMPORTANT: For capture-resume commands (which contain $(...) syntax), we MUST use
	// "claude" binary + explicit env exports, NOT a custom command alias like "cdw".
	// Reason: Commands with $(...) get wrapped in `bash -c` for fish compatibility (#47),
	// and shell aliases are not available in non-interactive bash shells. This always
	// execs the literal "claude" binary below regardless of GetClaudeCommandForInstance,
	// so the #1822 F3 custom-command gate does not apply here: pass false.
	bashExportPrefix := target.buildBashExportPrefix(false)

	// If no options provided, use defaults from config
	if opts == nil {
		userConfig, _ := LoadUserConfig()
		opts = NewClaudeOptions(userConfig)
	}

	// Build extra flags from options (for fork, we use ToArgsForFork which excludes session mode)
	extraFlags := i.buildClaudeExtraFlags(opts)

	// Pre-generate UUID for forked session to avoid shell uuidgen dependency.
	// CLAUDE_SESSION_ID is propagated via host-side SetEnvironment after tmux start.
	// Use `exec` before claude so that when this compound command is wrapped
	// in `bash -c` (for fish compatibility), claude replaces the bash process,
	// enabling proper job control (Ctrl+Z suspend / fg resume).
	forkUUID := generateUUID()
	target.ClaudeSessionID = forkUUID
	// #1815: minted for the fork target — vouched ownership.
	target.markClaudeSessionIDVerified()
	cmd := fmt.Sprintf(
		`cd '%s' && `+
			`%sexec claude --session-id "%s" --resume %s --fork-session%s`,
		workDir,
		bashExportPrefix, forkUUID, i.ClaudeSessionID, extraFlags)
	cmd, err := i.applyWrapper(cmd)
	if err != nil {
		return "", err
	}

	return cmd, nil
}

// GetActualWorkDir returns the actual working directory from tmux, or falls back to ProjectPath
func (i *Instance) GetActualWorkDir() string {
	if i.tmuxSession != nil {
		if workDir := i.tmuxSession.GetWorkDir(); workDir != "" {
			return workDir
		}
	}
	return i.ProjectPath
}

// CreateForkedInstance creates a new Instance configured for forking
// Deprecated: Use CreateForkedInstanceWithOptions instead
func (i *Instance) CreateForkedInstance(newTitle, newGroupPath string) (*Instance, string, error) {
	return i.CreateForkedInstanceWithOptions(newTitle, newGroupPath, nil)
}

// CreateForkedInstanceWithOptions creates a new Instance configured for forking with custom options
func (i *Instance) CreateForkedInstanceWithOptions(
	newTitle, newGroupPath string,
	opts *ClaudeOptions,
) (*Instance, string, error) {
	// Create new instance - use worktree path if provided, otherwise parent's project path
	projectPath := i.ProjectPath
	if opts != nil && opts.WorkDir != "" {
		projectPath = opts.WorkDir
	}
	forked := NewInstance(newTitle, projectPath)
	if newGroupPath != "" {
		forked.GroupPath = newGroupPath
	} else {
		forked.GroupPath = i.GroupPath
	}
	forked.Tool = "claude"
	if IsClaudeCompatible(i.Tool) {
		forked.Tool = i.Tool
	}
	forked.Wrapper = i.Wrapper

	// #1407: persist the parent's ExtraArgs onto the fork record. The baked
	// one-shot fork command below inherits them implicitly via the builder
	// (which reads the PARENT's ExtraArgs), but without persisting them on
	// the fork they silently drop on the fork's first restart
	// (buildClaudeResumeCommand reads the fork's own ExtraArgs) and a
	// fork-of-a-fork never sees them at all. Mirrors how ClaudeOptions are
	// persisted via SetClaudeOptions further down. Copied, not aliased.
	if len(i.ExtraArgs) > 0 {
		forked.ExtraArgs = append([]string(nil), i.ExtraArgs...)
	}

	cmd, err := i.buildClaudeForkCommandForTarget(forked, opts)
	if err != nil {
		return nil, "", err
	}
	forked.Command = cmd
	// #745: flag Start() to run cmd verbatim. Without this, Start() rebuilds
	// the command through buildClaudeResumeCommand and silently drops
	// --resume <parent-id> / --fork-session because the brand-new fork UUID
	// has no JSONL on disk yet.
	forked.IsForkAwaitingStart = true

	// Store options in the new instance for persistence
	if opts != nil {
		if err := forked.SetClaudeOptions(opts); err != nil {
			// Log but don't fail - options are not critical for fork
			sessionLog.Warn("set_claude_options_failed", slog.String("error", err.Error()))
		}
		// Copy transient worktree fields to the forked instance
		if opts.WorktreePath != "" {
			forked.WorktreePath = opts.WorktreePath
			forked.WorktreeRepoRoot = opts.WorktreeRepoRoot
			forked.WorktreeBranch = opts.WorktreeBranch
		}
	}

	return forked, cmd, nil
}

// CreateForkedInstanceForTool creates a forked instance using the correct
// tool-specific fork implementation. opts is the shared fork carrier for
// worktree fields; non-Claude tool options continue to come from global config.
func (i *Instance) CreateForkedInstanceForTool(newTitle, newGroupPath string, opts *ClaudeOptions) (*Instance, string, error) {
	switch {
	case i.Tool == "opencode":
		workDir := i.ProjectPath
		repoRoot := ""
		branch := ""
		if opts != nil && opts.WorkDir != "" {
			workDir = opts.WorkDir
			repoRoot = opts.WorktreeRepoRoot
			branch = opts.WorktreeBranch
		}
		return i.CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(newTitle, newGroupPath, nil, workDir, repoRoot, branch)
	case i.Tool == "pi":
		return i.CreateForkedPiInstanceWithOptions(newTitle, newGroupPath, opts)
	case IsCodexCompatible(i.Tool):
		return i.CreateForkedCodexInstanceWithOptions(newTitle, newGroupPath, opts)
	default:
		return i.CreateForkedInstanceWithOptions(newTitle, newGroupPath, opts)
	}
}

// ForkOpenCode returns the command to create a forked OpenCode session.
// Uses OpenCode's native `--fork` flag to branch the parent session into a new
// session with its own id (discovered asynchronously after launch).
// Deprecated: Use ForkOpenCodeWithOptions instead.
func (i *Instance) ForkOpenCode(newTitle, newGroupPath string) (string, error) {
	return i.ForkOpenCodeWithOptions(newTitle, newGroupPath, nil)
}

// ForkOpenCodeWithOptions returns the command to create a forked OpenCode session
// with custom options. Uses OpenCode's native `opencode -s <parent-id> --fork`,
// which branches the parent transcript into a fresh session while leaving the
// parent intact, plus any model/agent flags.
func (i *Instance) ForkOpenCodeWithOptions(newTitle, newGroupPath string, opts *OpenCodeOptions) (string, error) {
	return i.forkOpenCodeWithOptionsInWorkDir(newTitle, newGroupPath, opts, i.ProjectPath)
}

// forkOpenCodeWithOptionsInWorkDir builds the one-time `cd <workDir> &&
// opencode -s <parent-id> --fork` launch command for a forked OpenCode
// instance. `--fork` is a newer OpenCode CLI flag that branches the session
// named by -s/--continue; if the installed binary predates it the launched
// command fails into a recoverable error state, mirroring how `codex fork` is
// handled (CanForkCodex below).
//
// The launch is explicitly anchored to workDir with a `cd`: the multi-repo fork
// path later repoints the tmux session WorkDir to the MultiRepoTempDir
// container (internal/ui/home.go), yet async OpenCode session detection matches
// by ProjectPath (DetectOpenCodeSession), so OpenCode must run in the requested
// repo/worktree dir — not tmux's WorkDir — for the child session to be
// discoverable. OpenCode mints the child session id, which that async detection
// picks up; the previous export/import clone relied on the same path (and the
// same `cd`), so no id is pre-assigned here. The env prefix is applied once by
// buildOpenCodeCommand at start time.
func (i *Instance) forkOpenCodeWithOptionsInWorkDir(newTitle, newGroupPath string, opts *OpenCodeOptions, workDir string) (string, error) {
	if !i.CanForkOpenCode() {
		return "", fmt.Errorf("cannot fork: no active OpenCode session")
	}
	if strings.TrimSpace(workDir) == "" {
		workDir = i.ProjectPath
	}

	// Build extra flags from options (for fork, exclude session mode flags).
	var extraFlags string
	if opts != nil {
		for _, arg := range opts.ToArgsForFork() {
			extraFlags += " " + shellescape.Quote(arg)
		}
	} else if config, err := LoadUserConfig(); err == nil && config != nil {
		defaultOpts := NewOpenCodeOptions(config)
		for _, arg := range defaultOpts.ToArgsForFork() {
			extraFlags += " " + shellescape.Quote(arg)
		}
	}

	// workDir and the session id are shell-quoted to keep the launch command
	// injection-safe (the id is also charset-validated upstream by CanForkOpenCode).
	return fmt.Sprintf("cd %s && opencode -s %s --fork%s",
		shellescape.Quote(workDir), shellescape.Quote(i.OpenCodeSessionID), extraFlags), nil
}

// CreateForkedOpenCodeInstance creates a new Instance configured for forking an OpenCode session
// Deprecated: Use CreateForkedOpenCodeInstanceWithOptions instead.
func (i *Instance) CreateForkedOpenCodeInstance(newTitle, newGroupPath string) (*Instance, string, error) {
	return i.CreateForkedOpenCodeInstanceWithOptions(newTitle, newGroupPath, nil)
}

// CreateForkedOpenCodeInstanceWithOptions creates a new Instance configured for forking with custom options
func (i *Instance) CreateForkedOpenCodeInstanceWithOptions(
	newTitle, newGroupPath string,
	opts *OpenCodeOptions,
) (*Instance, string, error) {
	return i.CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(newTitle, newGroupPath, opts, i.ProjectPath, "", "")
}

// CreateForkedOpenCodeInstanceWithOptionsAndWorkDir creates a forked OpenCode instance
// rooted at workDir and copies worktree metadata when worktreeRepoRoot is set.
func (i *Instance) CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(
	newTitle, newGroupPath string,
	opts *OpenCodeOptions,
	workDir, worktreeRepoRoot, worktreeBranch string,
) (*Instance, string, error) {
	if strings.TrimSpace(workDir) == "" {
		workDir = i.ProjectPath
	}
	cmd, err := i.forkOpenCodeWithOptionsInWorkDir(newTitle, newGroupPath, opts, workDir)
	if err != nil {
		return nil, "", err
	}

	forked := NewInstance(newTitle, workDir)
	if newGroupPath != "" {
		forked.GroupPath = newGroupPath
	} else {
		forked.GroupPath = i.GroupPath
	}
	// Defer the one-shot fork script via ForkStartCommand (Pi/Codex pattern): the
	// script self-deletes after first run, so storing it as the persistent Command
	// would make a later restart re-run a missing file. Command holds a stable base
	// ("opencode") that restart resumes from via OpenCodeSessionID.
	forked.Command = "opencode"
	forked.ForkStartCommand = cmd
	forked.IsForkAwaitingStart = true
	forked.Tool = "opencode"
	if worktreeRepoRoot != "" {
		forked.WorktreePath = workDir
		forked.WorktreeRepoRoot = worktreeRepoRoot
		forked.WorktreeBranch = worktreeBranch
	}

	// Store options in the new instance for persistence
	if opts != nil {
		if err := forked.SetOpenCodeOptions(opts); err != nil {
			sessionLog.Warn("set_opencode_options_failed", slog.String("error", err.Error()))
		}
	}

	return forked, cmd, nil
}

// CreateForkedPiInstance creates a new Instance configured for forking a Pi session.
// Deprecated: Use CreateForkedPiInstanceWithOptions instead.
func (i *Instance) CreateForkedPiInstance(newTitle, newGroupPath string) (*Instance, string, error) {
	return i.CreateForkedPiInstanceWithOptions(newTitle, newGroupPath, nil)
}

// CreateForkedPiInstanceWithOptions creates a new Instance configured for forking a Pi session.
// The opts parameter is accepted for the shared fork worktree flow; only WorkDir
// and Worktree* fields are consumed for Pi.
func (i *Instance) CreateForkedPiInstanceWithOptions(
	newTitle, newGroupPath string,
	opts *ClaudeOptions,
) (*Instance, string, error) {
	projectPath := i.ProjectPath
	if opts != nil && opts.WorkDir != "" {
		projectPath = opts.WorkDir
	}

	forked := NewInstance(newTitle, projectPath)
	if newGroupPath != "" {
		forked.GroupPath = newGroupPath
	} else {
		forked.GroupPath = i.GroupPath
	}
	forked.Tool = "pi"
	forked.Wrapper = i.Wrapper

	baseCommand := strings.TrimSpace(i.Command)
	if baseCommand == "" {
		baseCommand = "pi"
	}
	forked.Command = baseCommand

	cmd, err := i.buildPiForkCommandForTarget(forked, baseCommand)
	if err != nil {
		return nil, "", err
	}
	forked.ForkStartCommand = cmd
	forked.IsForkAwaitingStart = true

	if opts != nil && opts.WorktreePath != "" {
		forked.WorktreePath = opts.WorktreePath
		forked.WorktreeRepoRoot = opts.WorktreeRepoRoot
		forked.WorktreeBranch = opts.WorktreeBranch
	}

	return forked, cmd, nil
}

// CanForkCodex reports whether this Codex session can be forked. Forkability
// requires a flushed on-disk rollout for the captured session id — the same
// invariant buildCodexCommand uses to gate `codex resume` (#756). `codex fork`
// is a newer codex CLI subcommand; if the installed binary predates it the
// launched command fails into a recoverable error state.
func (i *Instance) CanForkCodex() bool {
	if !IsCodexCompatible(i.Tool) || i.CodexSessionID == "" {
		return false
	}
	sessionID, err := normalizeToolSessionID(FieldCodexSessionID, i.CodexSessionID)
	return err == nil && sessionID != "" && sessionID == strings.TrimSpace(i.CodexSessionID) && codexRolloutExistsInHome(sessionID, i.getCodexHomeDir())
}

// buildCodexForkCommandForTarget builds the one-time `codex fork <parent-sid>`
// launch command for a forked codex instance. Mirrors buildCodexCommand's resume
// path (instance.go:1374) but uses `fork`, which clones the parent transcript into
// a new thread with a fresh id while leaving the parent intact.
func (i *Instance) buildCodexForkCommandForTarget(target *Instance, baseCommand string) (string, error) {
	if !i.CanForkCodex() {
		return "", fmt.Errorf("cannot fork: no resumable Codex session")
	}
	envPrefix := target.buildEnvSourceCommand()
	// Shell-quote the injected env values: target.Title is user-editable and could
	// contain shell metacharacters (e.g. $(...) or backticks), and custom Codex-tool
	// identities are config-defined — keep the generated fork command injection-safe.
	envPrefix += fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s AGENTDECK_TITLE=%s AGENTDECK_TOOL=%s AGENTDECK_PROFILE=%s ",
		shellescape.Quote(target.ID), shellescape.Quote(target.Title), shellescape.Quote(target.Tool), shellescape.Quote(sessionProfileEnvValue()))
	yoloFlag := target.resolveCodexYoloFlag()
	modelFlag := target.resolveCodexModelFlag()
	reasoningFlag := target.resolveCodexReasoningEffortFlag()
	command := target.resolveCodexCommand(baseCommand)
	if codexHome := target.codexHomeToExport(); codexHome != "" {
		if err := os.MkdirAll(codexHome, 0o755); err != nil {
			sessionLog.Warn("codex_home_mkdir_failed",
				slog.String("path", codexHome),
				slog.String("error", err.Error()))
		}
		envPrefix += "CODEX_HOME=" + shellescape.Quote(codexHome) + " "
	}
	return envPrefix + fmt.Sprintf("%s%s%s%s fork %s", command, yoloFlag, modelFlag, reasoningFlag, shellescape.Quote(i.CodexSessionID)), nil
}

// CreateForkedCodexInstanceWithOptions creates a forked Codex instance. Mirrors
// CreateForkedPiInstanceWithOptions: opts is the shared worktree carrier (only
// WorkDir/Worktree* consumed); launch is deferred via ForkStartCommand.
func (i *Instance) CreateForkedCodexInstanceWithOptions(
	newTitle, newGroupPath string,
	opts *ClaudeOptions,
) (*Instance, string, error) {
	projectPath := i.ProjectPath
	if opts != nil && opts.WorkDir != "" {
		projectPath = opts.WorkDir
	}

	forked := NewInstance(newTitle, projectPath)
	if newGroupPath != "" {
		forked.GroupPath = newGroupPath
	} else {
		forked.GroupPath = i.GroupPath
	}
	forked.Tool = i.Tool
	forked.Wrapper = i.Wrapper
	// #1929: a fork runs against the parent's thread, so it must run under the
	// parent's account. Without this the fork resolves its own codex home
	// through the default chain and `codex fork <parent-sid>` cannot find the
	// rollout, which lives under the account's home — the fork starts empty and
	// the account selection is lost from the record too.
	forked.Account = i.Account

	baseCommand := strings.TrimSpace(i.Command)
	if baseCommand == "" {
		baseCommand = "codex"
	}
	forked.Command = baseCommand
	if parentOpts := i.GetCodexOptions(); parentOpts != nil && parentOpts.ReasoningEffort != "" {
		forkOpts := &CodexOptions{ReasoningEffort: parentOpts.ReasoningEffort}
		if err := forked.SetCodexOptions(forkOpts); err != nil {
			return nil, "", fmt.Errorf("persist fork reasoning effort: %w", err)
		}
	}

	cmd, err := i.buildCodexForkCommandForTarget(forked, baseCommand)
	if err != nil {
		return nil, "", err
	}
	forked.ForkStartCommand = cmd
	forked.IsForkAwaitingStart = true

	if opts != nil && opts.WorktreePath != "" {
		forked.WorktreePath = opts.WorktreePath
		forked.WorktreeRepoRoot = opts.WorktreeRepoRoot
		forked.WorktreeBranch = opts.WorktreeBranch
	}

	return forked, cmd, nil
}

// Exists checks if the tmux session still exists
func (i *Instance) Exists() bool {
	if i.tmuxSession == nil {
		return false
	}
	return i.tmuxSession.Exists()
}

// GetTmuxSession returns the tmux session object
func (i *Instance) GetTmuxSession() *tmux.Session {
	return i.tmuxSession
}

// Substate returns the additive Honest-Status-v2 refinement for this session
// (see Substate). It reads the live tmux pane and classifies it; SubstateNone
// when there is no tmux session, the pane is dead, or the tool has no substate
// heuristics. This is an enrichment of Status, not a replacement — it never
// changes the canonical status reported by GetStatus/UpdateStatus.
// An auth hold overrides both accessors: a session whose agent EXITED on a
// credential failure has no pane left to classify, so the tmux layer honestly
// reports SubstateNone — which is exactly the blindness that made a fleet-wide
// 401 look like anonymous mass death. The hold is the durable memory of the last
// live verdict, so surfacing it here gives every reporting layer (TUI glyph, CLI
// status --json, web, transition events) the auth-401 substate for a dead
// session without any of them changing.
func (i *Instance) Substate() Substate {
	if held, _ := i.IsAuthHeld(); held {
		return SubstateAuth401
	}
	// A RECENT quota rejection is checked before the pane verdict, including the
	// pane's auth banner: it is the latest assistant turn and a 429 proves the
	// request was authenticated, so a stale "Please run /login" line still in the
	// scrollback must not win on category order and mislabel a session whose
	// credentials demonstrably work.
	//
	// "Recent" is load-bearing rather than decorative. A 429 only proves
	// authentication AT ITS OWN TIMESTAMP, so an old one must not mask newer pane
	// evidence — a freshly rendered 401, or a dropped socket that never arms the
	// credential hold. usageLimited enforces that bound (usageLimitMaxAge), which
	// is what keeps this ordering honest (#1802).
	if i.usageLimited() {
		return SubstateUsageLimit
	}
	tmuxSess := i.GetTmuxSession()
	if tmuxSess == nil {
		return SubstateNone
	}
	return tmuxSess.GetSubstate()
}

// CachedSubstate returns the last substate computed by a prior status check
// WITHOUT capturing the pane. Use it on the TUI render hot path; the background
// status loop keeps the value fresh.
// Like Substate, an auth hold wins — read from the in-memory mirror so the
// render path stays filesystem-free.
func (i *Instance) CachedSubstate() Substate {
	if i.AuthHeldCached() {
		return SubstateAuth401
	}
	// Deliberately NOT wired for usage-limit: this path must stay
	// filesystem-free, and nothing in the background status pass populates a
	// usage-limit mirror, so a branch here would be dead on the TUI/Web/transition
	// surfaces while looking supported. usage-limit is a live-Substate signal in
	// this change (CLI/JSON/fleet); wiring the cached path belongs with whatever
	// populates it. See usagelimit.go (#1802).
	tmuxSess := i.GetTmuxSession()
	if tmuxSess == nil {
		return SubstateNone
	}
	return tmuxSess.CachedSubstate()
}

// SetAcknowledgedFromShared applies an acknowledgment from another TUI instance
// (read from SQLite). This transitions a YELLOW (waiting) session to GRAY (idle)
// without requiring the user to interact with this specific TUI instance.
func (i *Instance) SetAcknowledgedFromShared(ack bool) {
	if !ack || i.tmuxSession == nil {
		return
	}

	// Running/starting is authoritative: don't let stale shared ack force
	// an active session back to idle.
	status := i.GetStatusThreadSafe()
	if status == StatusRunning || status == StatusStarting {
		return
	}

	i.tmuxSession.Acknowledge()
}

// SyncTmuxDisplayName updates tmux-rendered UI that reflects the current title.
func (i *Instance) SyncTmuxDisplayName() {
	if tmuxSess := i.GetTmuxSession(); tmuxSess != nil && tmuxSess.Exists() {
		tmuxSess.DisplayName = i.Title
		tmuxSess.ConfigureStatusBar()
		tmuxSess.ConfigureTerminalTitle()
	}
}

// GetClaudeOptions returns Claude-specific options, or nil if not set
func (i *Instance) GetClaudeOptions() *ClaudeOptions {
	if len(i.ToolOptionsJSON) == 0 {
		return nil
	}
	opts, err := UnmarshalClaudeOptions(i.ToolOptionsJSON)
	if err != nil {
		return nil
	}
	return opts
}

// SetClaudeOptions stores Claude-specific options
func (i *Instance) SetClaudeOptions(opts *ClaudeOptions) error {
	if opts == nil {
		i.ToolOptionsJSON = nil
		return nil
	}
	data, err := MarshalToolOptions(opts)
	if err != nil {
		return err
	}
	i.ToolOptionsJSON = data
	return nil
}

// GetCodexOptions returns Codex-specific options, or nil if not set
func (i *Instance) GetCodexOptions() *CodexOptions {
	if len(i.ToolOptionsJSON) == 0 {
		return nil
	}
	opts, err := UnmarshalCodexOptions(i.ToolOptionsJSON)
	if err != nil {
		return nil
	}
	return opts
}

// SetCodexOptions stores Codex-specific options
func (i *Instance) SetCodexOptions(opts *CodexOptions) error {
	if opts == nil {
		i.ToolOptionsJSON = nil
		return nil
	}
	data, err := MarshalToolOptions(opts)
	if err != nil {
		return err
	}
	i.ToolOptionsJSON = data
	return nil
}

// GetHermesOptions returns Hermes-specific options from ToolOptionsJSON, or nil if not set.
func (i *Instance) GetHermesOptions() *HermesOptions {
	if len(i.ToolOptionsJSON) == 0 {
		return nil
	}
	opts, err := UnmarshalHermesOptions(i.ToolOptionsJSON)
	if err != nil {
		return nil
	}
	return opts
}

// SetHermesOptions stores Hermes-specific options into ToolOptionsJSON.
func (i *Instance) SetHermesOptions(opts *HermesOptions) error {
	if opts == nil {
		i.ToolOptionsJSON = nil
		return nil
	}
	data, err := MarshalToolOptions(opts)
	if err != nil {
		return err
	}
	i.ToolOptionsJSON = data
	return nil
}

// GetOpenCodeOptions returns OpenCode-specific options, or nil if not set
func (i *Instance) GetOpenCodeOptions() *OpenCodeOptions {
	if len(i.ToolOptionsJSON) == 0 {
		return nil
	}
	opts, err := UnmarshalOpenCodeOptions(i.ToolOptionsJSON)
	if err != nil {
		return nil
	}
	return opts
}

// SetOpenCodeOptions stores OpenCode-specific options
func (i *Instance) SetOpenCodeOptions(opts *OpenCodeOptions) error {
	if opts == nil {
		i.ToolOptionsJSON = nil
		return nil
	}
	data, err := MarshalToolOptions(opts)
	if err != nil {
		return err
	}
	i.ToolOptionsJSON = data
	return nil
}

// GetSessionIDFromTmux reads Claude session ID from tmux environment
// This is the primary method for sessions started with the capture-resume pattern
func (i *Instance) GetSessionIDFromTmux() string {
	if i.tmuxSession == nil {
		return ""
	}
	sessionID, err := i.tmuxSession.GetEnvironment("CLAUDE_SESSION_ID")
	if err != nil {
		return ""
	}
	return sessionID
}

// RefreshLiveSessionIDs re-reads tool-specific session identifiers from the
// live tmux environment and updates the instance's stored IDs when a newer
// non-empty value is found. Safe no-op when tmuxSession is nil or the tool
// has no live-env handle.
//
// Call this before reads that must reflect the CURRENT conversation (e.g.
// TUI cross-session send-output, issue #598). Reads that tolerate stale data
// (status polling) don't need it.
func (i *Instance) RefreshLiveSessionIDs() {
	if i.tmuxSession == nil {
		return
	}
	if IsClaudeCompatible(i.Tool) {
		if id := i.GetSessionIDFromTmux(); id != "" && id != i.ClaudeSessionID {
			i.ClaudeSessionID = id
			// #1815: own pane env — weak vouch.
			i.noteClaudeSessionIDFromOwnPane()
			i.ClaudeDetectedAt = time.Now()
		}
	}
	if i.Tool == "gemini" {
		i.syncGeminiSessionFromTmux()
	}
}

// GetMCPInfo returns MCP server information for this session.
// Returns nil if not a Claude-compatible, Gemini, or Cursor session.
// Hermes is intentionally excluded: it uses its own ~/.hermes/config.yaml
// `mcp_servers:` schema (user-scoped, YAML), not Claude's project-scoped
// .mcp.json — agent-deck does not manage it yet.
func (i *Instance) GetMCPInfo() *MCPInfo {
	switch {
	case IsCodexCompatible(i.Tool):
		if i.isRemoteSession() {
			return &MCPInfo{}
		}
		return GetCodexMCPInfo(i.getCodexHomeDir())
	case IsClaudeCompatible(i.Tool):
		return GetMCPInfo(i.ProjectPath)
	case i.Tool == "gemini":
		return GetGeminiMCPInfo(i.ProjectPath)
	case i.Tool == "cursor":
		return GetCursorMCPInfo(i.ProjectPath)
	default:
		return nil
	}
}

// CaptureLoadedMCPs captures the current MCP names as the "loaded" state
// This should be called when a session starts or restarts, so we can track
// which MCPs are actually loaded in the running Claude session vs just configured
func (i *Instance) CaptureLoadedMCPs() {
	if !IsClaudeCompatible(i.Tool) && !IsCodexCompatible(i.Tool) && i.Tool != "cursor" {
		i.LoadedMCPNames = nil
		return
	}

	mcpInfo := i.GetMCPInfo()
	if mcpInfo == nil {
		i.LoadedMCPNames = nil
		return
	}

	i.LoadedMCPNames = mcpInfo.AllNames()
}

// regenerateMCPConfig regenerates .mcp.json with current pool status
// If socket pool is running, MCPs will use socket configs (nc -U /tmp/...)
// Otherwise, MCPs will use stdio configs (npx ...)
// Returns error if .mcp.json write fails
func (i *Instance) regenerateMCPConfig() error {
	if IsCodexCompatible(i.Tool) {
		ClearCodexMCPCache(i.getCodexHomeDir())
		mcpInfo := i.GetMCPInfo()
		if mcpInfo == nil || len(mcpInfo.Global) == 0 {
			return nil
		}
		if err := i.WriteGlobalMCPConfig(mcpInfo.Global); err != nil {
			mcpLog.Debug("regen_codex_mcp_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to regenerate Codex MCP config: %w", err)
		}
		mcpLog.Debug("regen_codex_mcp_succeeded", slog.String("title", i.Title), slog.Int("mcp_count", len(mcpInfo.Global)))
		return nil
	}

	if i.Tool == "cursor" {
		ClearCursorMCPCache(i.ProjectPath)
		mcpInfo := i.GetMCPInfo()
		if mcpInfo == nil || !mcpInfo.HasAny() {
			return nil
		}

		switch GetMCPDefaultScope() {
		case "global", "user":
			globalMCPs := mcpInfo.Global
			if len(globalMCPs) == 0 {
				return nil
			}
			if err := i.WriteGlobalMCPConfig(globalMCPs); err != nil {
				mcpLog.Debug("regen_cursor_global_mcp_failed", slog.String("error", err.Error()))
				return fmt.Errorf("failed to regenerate Cursor global MCP config: %w", err)
			}
			mcpLog.Debug("regen_cursor_global_mcp_succeeded", slog.String("title", i.Title), slog.Int("mcp_count", len(globalMCPs)))
		default:
			localMCPs := mcpInfo.Local()
			if len(localMCPs) == 0 {
				return nil
			}
			if err := i.WriteLocalMCPConfig(localMCPs); err != nil {
				mcpLog.Debug("regen_cursor_project_mcp_failed", slog.String("error", err.Error()))
				return fmt.Errorf("failed to regenerate .cursor/mcp.json: %w", err)
			}
			mcpLog.Debug("regen_cursor_project_mcp_succeeded", slog.String("title", i.Title), slog.Int("mcp_count", len(localMCPs)))
		}
		return nil
	}

	ClearMCPCache(i.ProjectPath) // Force fresh read from disk (not stale 30s cache)
	mcpInfo := GetMCPInfo(i.ProjectPath)
	if mcpInfo == nil {
		return nil // No MCP info, nothing to regenerate
	}

	switch GetMCPDefaultScope() {
	case "global":
		globalMCPs := mcpInfo.Global
		if len(globalMCPs) == 0 {
			return nil
		}
		if err := WriteGlobalMCP(globalMCPs); err != nil {
			mcpLog.Debug("regen_global_mcp_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to regenerate global MCP config: %w", err)
		}
		mcpLog.Debug(
			"regen_global_mcp_succeeded",
			slog.String("title", i.Title),
			slog.Int("mcp_count", len(globalMCPs)),
		)
	case "user":
		userMCPs := GetUserMCPNames()
		if len(userMCPs) == 0 {
			return nil
		}
		if err := WriteUserMCP(userMCPs); err != nil {
			mcpLog.Debug("regen_user_mcp_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to regenerate user MCP config: %w", err)
		}
		mcpLog.Debug("regen_user_mcp_succeeded", slog.String("title", i.Title), slog.Int("mcp_count", len(userMCPs)))
	default:
		localMCPs := mcpInfo.Local()
		if len(localMCPs) == 0 {
			return nil
		}
		// WriteMCPJsonFromConfig checks pool status and writes socket configs if pool is running
		if err := WriteMCPJsonFromConfig(i.ProjectPath, localMCPs); err != nil {
			mcpLog.Debug("regen_local_mcp_failed", slog.String("error", err.Error()))
			return fmt.Errorf("failed to regenerate .mcp.json: %w", err)
		}
		mcpLog.Debug("regen_local_mcp_succeeded", slog.String("title", i.Title), slog.Int("mcp_count", len(localMCPs)))
	}
	return nil
}

// geminiSessionHasConversationData checks whether a Gemini session file exists
// and contains at least one message record.
//
// Returns true on read/parse errors as a safe fallback, matching Claude quality-gate
// behavior (avoid dropping potentially valid sessions due to transient I/O issues).
func geminiSessionHasConversationData(sessionID, projectPath string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) < 8 {
		return false
	}

	sessionsDir := GetGeminiSessionsDir(projectPath)
	pattern := filepath.Join(sessionsDir, "session-*-"+sessionID[:8]+".json")
	filePath, _ := findNewestFile(pattern)
	if filePath == "" {
		filePath = findGeminiSessionInAllProjects(sessionID)
	}
	if filePath == "" {
		return false
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return true
	}

	var payload struct {
		SessionID string            `json:"sessionId"`
		Messages  []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return true
	}

	// If full ID is present in file and mismatches, treat candidate as invalid.
	if payload.SessionID != "" && payload.SessionID != sessionID {
		return false
	}
	return len(payload.Messages) > 0
}

// sessionConversationByteSize returns the size in bytes of the Claude
// session's jsonl file (or 0 if it cannot be located). Used as a robust
// "how much history does this session hold" proxy when choosing between
// two non-empty candidates during hook rebind — a 974KB historic jsonl
// should always win over a fresh 1-record jsonl, regardless of whether
// both pass the binary `sessionHasConversationData` check.
//
// Uses the PER-INSTANCE Claude config dir (same lookup as
// sessionHasConversationData) so conductors with config_dir overrides
// resolve correctly. Errors return 0 — this is a tiebreaker, not the
// primary gate, so a missing file degrades gracefully to "candidate
// doesn't appear larger, reject" rather than false-accepting.
func sessionConversationByteSize(inst *Instance, sessionID string) int64 {
	// #1851: see sessionHasConversationData — a remote session's transcript is
	// not in this filesystem, so any size found here belongs to someone else.
	if inst != nil && !inst.TranscriptIsResolvableLocally() {
		return 0
	}
	var configDir string
	if inst != nil {
		configDir = GetClaudeConfigDirForInstance(inst)
	} else {
		configDir = GetClaudeConfigDir()
	}
	if configDir == "" {
		configDir = filepath.Join(os.Getenv("HOME"), ".claude")
	}
	// Issue #663: for multi-repo sessions ProjectPath is a symlink into
	// MultiRepoTempDir; EvalSymlinks would resolve away from the parent
	// dir Claude actually used as cwd. EffectiveWorkingDir() is the
	// authoritative cwd for JSONL encoding.
	projectPath := ""
	if inst != nil {
		projectPath = inst.EffectiveWorkingDir()
	}
	resolvedPath := projectPath
	if resolved, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedPath = resolved
	}
	encodedPath := ConvertToClaudeDirName(resolvedPath)
	if encodedPath == "" {
		encodedPath = "-"
	}
	sessionFile := filepath.Join(configDir, "projects", encodedPath, sessionID+".jsonl")
	if info, err := os.Stat(sessionFile); err == nil {
		return info.Size()
	}
	if fallback := findSessionFileInAllProjects(inst, sessionID); fallback != "" {
		if info, err := os.Stat(fallback); err == nil {
			return info.Size()
		}
	}
	return 0
}

// sessionConversationMtime returns the modification time of the Claude
// session's jsonl file (or the zero time if it cannot be located). Issue
// #856: when both current and candidate jsonls have data, mtime gap is the
// discriminator between a stale flap (rich session still being actively
// written, candidate is a momentary blip) and a user-initiated new session
// like /clear (rich session is dormant, candidate is the new active jsonl).
// Path resolution mirrors sessionConversationByteSize.
func sessionConversationMtime(inst *Instance, sessionID string) time.Time {
	// #1851: see sessionHasConversationData.
	if inst != nil && !inst.TranscriptIsResolvableLocally() {
		return time.Time{}
	}
	var configDir string
	if inst != nil {
		configDir = GetClaudeConfigDirForInstance(inst)
	} else {
		configDir = GetClaudeConfigDir()
	}
	if configDir == "" {
		configDir = filepath.Join(os.Getenv("HOME"), ".claude")
	}
	projectPath := ""
	if inst != nil {
		projectPath = inst.EffectiveWorkingDir()
	}
	resolvedPath := projectPath
	if resolved, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedPath = resolved
	}
	encodedPath := ConvertToClaudeDirName(resolvedPath)
	if encodedPath == "" {
		encodedPath = "-"
	}
	sessionFile := filepath.Join(configDir, "projects", encodedPath, sessionID+".jsonl")
	if info, err := os.Stat(sessionFile); err == nil {
		return info.ModTime()
	}
	if fallback := findSessionFileInAllProjects(inst, sessionID); fallback != "" {
		if info, err := os.Stat(fallback); err == nil {
			return info.ModTime()
		}
	}
	return time.Time{}
}

// bindClaudeSessionFromHook performs the common bookkeeping when
// UpdateHookStatus has decided a candidate session ID wins: log the
// lifecycle event, update the in-memory instance fields, and propagate
// the ID into the tmux environment so a future restart's
// capture-resume pattern picks it up. `action` is "bind" (cold start)
// or "rebind" (replacing an existing ID).
func (i *Instance) bindClaudeSessionFromHook(sessionID, hookSource, hookEvent, action string) {
	sessionLog.Debug("claude_session_update_from_hook",
		slog.String("old_id", i.ClaudeSessionID),
		slog.String("new_id", sessionID),
		slog.String("event", hookEvent),
	)
	_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
		InstanceID: i.ID, Tool: i.Tool, Action: action,
		Source: hookSource, OldID: i.ClaudeSessionID, NewID: sessionID,
		HookEvent: hookEvent,
	})
	i.ClaudeSessionID = sessionID
	// #1815: a hook payload correlated to this instance identifies THIS
	// session, so the id is verified ownership.
	i.markClaudeSessionIDVerified()
	i.ClaudeDetectedAt = time.Now()
	i.hookSessionID = sessionID

	if i.tmuxSession != nil && i.tmuxSession.Exists() {
		_ = i.tmuxSession.SetEnvironment("CLAUDE_SESSION_ID", sessionID)
	}

	// Persist the rebind to SQLite. The PERSIST-12 contract above assumed
	// an "external save cycle" would pick this up, but none of the three
	// UpdateHookStatus callers (TUI tick, web refresh, CLI status refresh)
	// actually save after rebind — leaving tool_data.claude_session_id
	// stuck at the pre-/clear UUID indefinitely for DB-direct consumers,
	// and producing a runaway loop of fresh "rebind" lifecycle entries
	// because peer processes keep reloading the stale row and clobbering
	// the in-memory mutation.
	//
	// What this UPDATE guarantees: the write is atomic at SQLite's row
	// lock against WriteStatus (different columns) and SaveInstance
	// (same row, serialized). What it does NOT prevent: a concurrent
	// SaveInstance from a peer process holding a stale Instance snapshot
	// can still clobber the value we just wrote, because
	// claude_session_id is a typed schema field — MergeToolDataExtras
	// only protects keys outside that typed set, so the peer's stale
	// typed value wins. The runaway-rebind loop terminates anyway
	// because the writer that decided to rebind also persists
	// synchronously here, not because clobbering is impossible — a
	// later peer reload that observes the new ID will short-circuit at
	// the `sessionID == i.ClaudeSessionID` check in UpdateHookStatus.
	if db := statedb.GetGlobal(); db != nil {
		if err := db.WriteClaudeSessionBinding(i.ID, sessionID, i.ClaudeDetectedAt); err != nil {
			sessionLog.Warn("claude_session_rebind_persist_failed",
				slog.String("instance_id", i.ID),
				slog.String("new_id", sessionID),
				slog.String("error", err.Error()))
		}
	}
}

// sessionHasConversationData checks if a Claude session file contains actual
// conversation data (has "sessionId" field in records).
//
// Uses the PER-INSTANCE Claude config dir (via GetClaudeConfigDirForInstance)
// so sessions with [conductors.<name>.claude].config_dir or
// [groups."<group>".claude].config_dir overrides detect their own JSONL
// history correctly. Prior versions (≤v1.7.6) consulted the process-wide
// GetClaudeConfigDir(), which silently ignored per-instance overrides and
// caused restart-with-history to mis-route to --session-id (Claude rejects
// that as "already in use") instead of --resume. Passing inst == nil
// degrades to the global lookup, preserving legacy call sites without an
// Instance.
//
// Returns true if:
// - File has any "sessionId" field (user interacted with session)
// - Error reading file (safe fallback - don't risk losing sessions)
//
// Returns false if:
// - File doesn't exist (nothing to resume, use --session-id)
// - File exists but has zero "sessionId" occurrences (never interacted)
// It answers a LOCAL-DISK question. Callers deciding "start fresh or resume?"
// must use conversationIsResumable instead — see its doc comment for why a hard
// false is the wrong answer for a remote session.
func sessionHasConversationData(inst *Instance, sessionID string) bool {
	// #1851: for an --ssh session every path below is a directory on THIS
	// machine, keyed on the local placeholder ProjectPath — so "yes, that id has
	// conversation data" would be a statement about a LOCAL session's transcript.
	// The honest answer for a conversation that lives on another host is "not
	// here"; the remote harness makes its own resume decision on its own disk.
	if inst != nil && !inst.TranscriptIsResolvableLocally() {
		return false
	}
	// Build the session file path
	// Format: {config_dir}/projects/{encoded_path}/{sessionID}.jsonl
	var configDir string
	if inst != nil {
		configDir = GetClaudeConfigDirForInstance(inst)
	} else {
		configDir = GetClaudeConfigDir()
	}
	if configDir == "" {
		configDir = filepath.Join(os.Getenv("HOME"), ".claude")
	}

	// Issue #663: see sessionConversationByteSize rationale above.
	// Multi-repo sessions must encode EffectiveWorkingDir(), not the
	// ProjectPath symlink.
	projectPath := ""
	if inst != nil {
		projectPath = inst.EffectiveWorkingDir()
	}

	// Resolve symlinks in project path (macOS: /tmp -> /private/tmp)
	resolvedPath := projectPath
	if resolved, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedPath = resolved
	}

	// Encode project path using Claude's directory format
	encodedPath := ConvertToClaudeDirName(resolvedPath)
	if encodedPath == "" {
		encodedPath = "-"
	}

	sessionFile := filepath.Join(configDir, "projects", encodedPath, sessionID+".jsonl")

	// Issue #662 diagnostic contract: emit a single structured "decision"
	// log line per call with every field needed to reconstruct the false
	// negatives in production logs (config_dir, resolved_project_path,
	// encoded_path, primary_path_tested, primary_path_stat_err,
	// fallback_lookup_tried, fallback_path_found, final_result).
	primaryStatErr := ""
	fallbackTried := false
	fallbackPathFound := ""

	emitDecision := func(result bool, reason string) {
		sessionLog.Debug(
			"session_data_decision",
			slog.String("session_id", sessionID),
			slog.String("config_dir", configDir),
			slog.String("resolved_project_path", resolvedPath),
			slog.String("encoded_path", encodedPath),
			slog.String("primary_path_tested", sessionFile),
			slog.String("primary_path_stat_err", primaryStatErr),
			slog.Bool("fallback_lookup_tried", fallbackTried),
			slog.String("fallback_path_found", fallbackPathFound),
			slog.Bool("final_result", result),
			slog.String("reason", reason),
		)
	}

	sessionLog.Debug("session_data_checking_file", slog.String("file", sessionFile))

	// Check if file exists
	if _, err := os.Stat(sessionFile); os.IsNotExist(err) {
		if err != nil {
			primaryStatErr = err.Error()
		}
		// File doesn't exist at expected location - try cross-project search.
		// This handles ENCODING mismatches that still denote the same real
		// directory (symlinked project paths: the primary lookup encodes the
		// resolved path, while Claude may have filed under the unresolved
		// spelling, or vice versa).
		//
		// It must NOT accept a jsonl belonging to a genuinely different
		// project. `claude --resume` only consults the project dir derived
		// from its own cwd, so a foreign-directory hit is not evidence that
		// --resume will succeed — it guarantees the opposite: Claude prints
		// "No conversation found with session ID: <id>" and exits within a
		// second, flapping the session to `error` on every restart forever.
		// Returning false instead routes to --session-id, which starts
		// cleanly in the correct project dir.
		fallbackTried = true
		fallbackPath := findSessionFileInAllProjects(inst, sessionID)
		foreignHit := fallbackPath != "" &&
			!encodesSameWorkingDir(filepath.Base(filepath.Dir(fallbackPath)), projectPath, resolvedPath)
		if fallbackPath != "" {
			fallbackPathFound = fallbackPath
		}
		switch {
		case foreignHit:
			sessionLog.Debug(
				"session_data_cross_project_rejected",
				slog.String("found_path", logging.SanitizeValue(fallbackPath)),
				slog.String("instance_working_dir", logging.SanitizeValue(projectPath)),
				slog.String("result", "use_session_id"),
			)
			emitDecision(false, "foreign_project_dir_resume_would_fail")
			return false
		case fallbackPath == "":
			// File doesn't exist anywhere - use --session-id to create fresh session
			// (there's nothing to resume if the file doesn't exist)
			sessionLog.Debug("session_data_file_not_found", slog.String("result", "use_session_id"))
			emitDecision(false, "file_not_found")
			return false
		default:
			sessionLog.Debug("session_data_cross_project_found", slog.String("path", logging.SanitizeValue(fallbackPath)))
			sessionFile = fallbackPath
		}
	}

	sessionLog.Debug("session_data_file_exists", slog.String("file", sessionFile))

	// Read file and search for "sessionId" field
	file, err := os.Open(sessionFile)
	if err != nil {
		// Error opening - safe fallback to --resume
		sessionLog.Debug(
			"session_data_open_error",
			slog.String("error", err.Error()),
			slog.String("fallback", "use_resume"),
		)
		emitDecision(true, "open_error_safe_fallback")
		return true
	}
	defer file.Close()

	// Use scanner to read line by line (memory efficient for large files)
	scanner := bufio.NewScanner(file)
	// Increase buffer size for long lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		// Simple string search - faster than JSON parsing
		if strings.Contains(line, `"sessionId"`) {
			sessionLog.Debug("session_data_found_session_id", slog.String("result", "use_resume"))
			emitDecision(true, "session_id_line_present")
			return true // Found conversation data
		}
	}

	if err := scanner.Err(); err != nil {
		// Error reading - safe fallback to --resume
		sessionLog.Debug(
			"session_data_scanner_error",
			slog.String("error", err.Error()),
			slog.String("fallback", "use_resume"),
		)
		emitDecision(true, "scanner_error_safe_fallback")
		return true
	}

	// No sessionId found - session was never interacted with
	sessionLog.Debug("session_data_no_session_id", slog.String("result", "use_session_id"))
	emitDecision(false, "no_session_id_line")
	return false
}

// encodesSameWorkingDir reports whether encodedDir — a directory name found
// under {config_dir}/projects — is a plausible Claude encoding of this
// instance's own working directory, given both its literal and
// symlink-resolved spellings.
//
// This gates the cross-project fallback in sessionHasConversationData. The
// fallback globs every project dir, so it happily returns a jsonl belonging to
// an unrelated session. That is worse than not finding one: `claude --resume`
// derives its project dir from its own cwd, so acting on a foreign hit makes
// Claude exit immediately with "No conversation found with session ID: <id>".
// Only an encoding that denotes the SAME real directory (the symlinked-path
// case the fallback exists for) is safe to accept.
func encodesSameWorkingDir(encodedDir, projectPath, resolvedPath string) bool {
	if encodedDir == "" {
		return false
	}
	for _, candidate := range []string{projectPath, resolvedPath} {
		if candidate == "" {
			continue
		}
		if encodedDir == ConvertToClaudeDirName(candidate) {
			return true
		}
	}
	return false
}

// findSessionFileInAllProjects searches all Claude project directories for a session file
// This handles path hash mismatches when agent-deck runs from a different directory
// than where the Claude session was originally created.
// Returns the full path to the session file, or empty string if not found.
//
// CAUTION: a hit here means "this jsonl exists somewhere", NOT "Claude can
// resume it". `claude --resume` only consults the project dir derived from its
// own cwd. Callers deciding between --resume and --session-id must therefore
// gate this result through encodesSameWorkingDir; callers that merely need the
// file's size or mtime (sessionConversationByteSize, sessionConversationMtime)
// can use it unguarded.
// Uses the PER-INSTANCE config dir (via GetClaudeConfigDirForInstance) when
// inst is non-nil so sessions with conductor/group config_dir overrides find
// their own JSONLs. Passing inst == nil degrades to the global lookup.
func findSessionFileInAllProjects(inst *Instance, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	// #1851: this scans EVERY local project dir for <sessionID>.jsonl, so for a
	// remote session it is the widest door of all — it does not even need the
	// placeholder path to collide.
	if inst != nil && !inst.TranscriptIsResolvableLocally() {
		return ""
	}

	var configDir string
	if inst != nil {
		configDir = GetClaudeConfigDirForInstance(inst)
	} else {
		configDir = GetClaudeConfigDir()
	}
	if configDir == "" {
		configDir = filepath.Join(os.Getenv("HOME"), ".claude")
	}

	projectsDir := filepath.Join(configDir, "projects")

	// List all project hash directories
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return ""
	}

	// Session filename format: {sessionID}.jsonl
	sessionFile := sessionID + ".jsonl"

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		filePath := filepath.Join(projectsDir, entry.Name(), sessionFile)
		if _, err := os.Stat(filePath); err == nil {
			return filePath
		}
	}

	return ""
}

// OpenContainerShell creates a tmux session running an interactive shell inside
// the sandbox container. Returns the tmux session name for attaching.
// Uses /bin/sh for portability (not all images have bash).
func (i *Instance) OpenContainerShell() (string, error) {
	if !i.IsSandboxed() {
		return "", fmt.Errorf("session %s is not sandboxed", i.ID)
	}
	if i.SandboxContainer == "" || !docker.IsManagedContainer(i.SandboxContainer) {
		return "", fmt.Errorf("no valid sandbox container for session %s", i.ID)
	}

	// Verify the container is still running before attempting docker exec.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ctr := docker.FromName(i.SandboxContainer)
	running, err := ctr.IsRunning(ctx)
	if err != nil {
		return "", fmt.Errorf("checking container %s: %w", i.SandboxContainer, err)
	}
	if !running {
		return "", fmt.Errorf("sandbox container %s is not running", i.SandboxContainer)
	}

	// Reuse the GenerateName prefix logic for consistency.
	tmuxName := "ad-term-" + docker.GenerateName(i.ID, i.Title)[len("agent-deck-"):]

	// Kill any existing terminal session to prevent orphans from repeated T presses.
	// Target the same socket the parent agent-deck instance lives on so the
	// terminal helper is visible to `tmux -L <socket> ls` and agent-deck's
	// own reap paths (issue #687).
	killCtx, killCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer killCancel()
	_ = tmux.ExecContext(killCtx, i.TmuxSocketName, "kill-session", "-t", tmuxName).Run()

	// Omit -w flag: the container's workdir was set during create (respects worktree path).
	// Pass the docker exec command as discrete tmux args to avoid shell interpolation of
	// the container name (defence-in-depth against state file tampering).
	//
	// #1694: -x/-y are mandatory on every detached new-session — without them
	// the pane is born at tmux's 80x24 default and the shell (plus anything the
	// user runs in it) paints its first frames clipped. See
	// tmux.InitialWindowSize.
	cols, rows := tmux.InitialWindowSize()
	newCtx, newCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer newCancel()
	newCmd := tmux.ExecContext(newCtx, i.TmuxSocketName,
		"new-session", "-d", "-s", tmuxName,
		"-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows),
		"docker", "exec", "-it", i.SandboxContainer, "/bin/sh",
	)
	// #1713: if this new-session is what starts the tmux server, the server
	// inherits this process's cwd — and agent-deck is frequently run from a
	// worktree that later gets deleted, after which tmux ignores every -c and
	// births all panes in that dead directory. Spawn from a directory that
	// cannot be unlinked. The pane's own cwd is irrelevant here: it runs
	// `docker exec`, whose working directory was fixed at container create.
	newCmd.Dir = tmux.SpawnBaseDir
	out, err := newCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("creating terminal session: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return tmuxName, nil
}

// wrapForSSH wraps the command in an SSH invocation if the instance targets a remote host.
// Uses ControlMaster for connection multiplexing to avoid repeated handshakes.
func (i *Instance) wrapForSSH(command string) string {
	if !i.IsSSH() {
		return command
	}

	// Ensure ControlMaster socket directory exists
	sshDir := "/tmp/agent-deck-ssh"
	_ = os.MkdirAll(sshDir, 0700)

	// remoteCmd is the shell program the REMOTE shell will run. It is quoted
	// exactly once, by shellQuote below, when it becomes ssh's final argument.
	//
	// The command is spliced in verbatim: it is already a shell program (the
	// export prefix plus the harness invocation), not a literal to embed. It
	// used to be escaped here as well and then again by shellQuote, so every
	// single quote in the payload reached the remote shell malformed. That broke
	// EVERY ungrouped --ssh claude session with a --remote-path, because the
	// spawn prefix always emits `export AGENTDECK_RESOLVED_GROUP='';` — the
	// remote shell answered `unexpected EOF while looking for matching '` and
	// the session died in ~250ms, which is the symptom #1858 reports.
	//
	// RemoteCDPrefix (not a hand-rolled `cd '%s'`) is what makes the directory
	// the session RUNS in the same directory its identity says it is in: it
	// leaves a ~ prefix unquoted so the remote shell expands it against the
	// REMOTE user's home, and emits nothing at all for the spellings that
	// CanonicalRemotePath folds together ("", "~", "~/").
	remoteCmd := RemoteCDPrefix(i.SSHRemotePath) + command

	return fmt.Sprintf(
		"ssh -t -o ControlMaster=auto -o ControlPath=/tmp/agent-deck-ssh/%%r@%%h:%%p -o ControlPersist=600 %s %s",
		shellQuote(i.SSHHost),
		shellQuote(remoteCmd),
	)
}

// shellQuote wraps a string in single quotes, escaping embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// wrapForSandbox wraps command in docker exec if the instance is sandboxed.
// Returns the wrapped command and the container name. The caller is responsible
// for persisting the container name to i.SandboxContainer.
func (i *Instance) wrapForSandbox(command string) (string, string, error) {
	if !i.IsSandboxed() {
		return command, "", nil
	}

	userCfg, cfgErr := LoadUserConfig()
	if cfgErr != nil {
		sessionLog.Warn("load_user_config_for_sandbox", slog.String("error", cfgErr.Error()))
	}

	wrappedCmd, containerName, err := ensureSandboxContainer(i, userCfg, command)
	if err != nil {
		return "", "", err
	}
	return wrappedCmd, containerName, nil
}

// builtinAgentTools are the first-party agent CLIs agent-deck launches as a
// pane's initial process and whose clean exit (e.g. `/exit`) can fall back to
// an interactive shell when exit_to_shell is enabled (issue #1161).
var builtinAgentTools = map[string]bool{
	"claude":   true,
	"gemini":   true,
	"opencode": true,
	"codex":    true,
	"copilot":  true,
	"cursor":   true,
	"hermes":   true,
	"crush":    true,
}

// isBuiltinAgentTool reports whether tool is a first-party agent (or a custom
// tool wrapping claude/codex). Custom non-agent commands and "shell" are not
// agents and must never be exit-to-shell wrapped.
func isBuiltinAgentTool(tool string) bool {
	if builtinAgentTools[tool] {
		return true
	}
	return IsClaudeCompatible(tool) || IsCodexCompatible(tool)
}

// exitToShellEnabled resolves the exit-to-shell toggle for this instance.
// Per-session override (Instance.ExitToShell) wins; otherwise the global
// [shell] exit_to_shell config flag applies. Default is OFF (opt-in). #1161.
func (i *Instance) exitToShellEnabled() bool {
	if i.ExitToShell != nil {
		return *i.ExitToShell
	}
	cfg, _ := LoadUserConfig()
	return cfg != nil && cfg.Shell.GetExitToShell()
}

// wrapExitToShell rewrites a built-in agent's spawn command so the pane falls
// back to an interactive shell at the same cwd when the agent exits, restoring
// the pre-#503 exit→shell→resume workflow (issue #1161, Option A).
//
// The transform is:
//
//	<agent cmd>; exec "$SHELL" -i
//
// with the agent's own `exec ` launcher neutralised — claude execs itself for
// job control, which would replace the wrapping bash and prevent the trailing
// shell exec from ever running. Only the first `exec ` (the launcher) is
// stripped; any later "exec " lives inside a shell-quoted startup-query suffix.
// Agents that do not exec (gemini, codex, …) are unaffected by the strip and
// simply get the suffix appended.
//
// No-op when the flag is off, the command is empty, the session is sandboxed
// (docker exec owns the in-container process), or the tool is not a built-in
// agent. Resume is unaffected: i.ClaudeSessionID is captured in Go before the
// command is built, so the `--session-id`/`--resume` id still targets the same
// session after the shell detour.
func (i *Instance) wrapExitToShell(command string) string {
	if command == "" || i.IsSandboxed() || !i.exitToShellEnabled() || !isBuiltinAgentTool(i.Tool) {
		return command
	}
	rewritten := strings.Replace(command, "exec ", "", 1)
	return rewritten + `; exec "$SHELL" -i`
}

// launchShellEnabled returns whether the session should wrap agent commands
// with a shell invocation that loads startup files before launching the agent.
// Checks per-session override first, then falls back to global [shell].launch_shell config.
func (i *Instance) launchShellEnabled() bool {
	if i.LaunchShell != nil {
		return *i.LaunchShell
	}
	cfg, _ := LoadUserConfig()
	return cfg != nil && cfg.Shell.GetLaunchShell()
}

// wrapLaunchShell wraps the command with an interactive shell invocation so
// that environment variables from ~/.zshrc, ~/.bashrc, etc. are available to
// the agent process (issue #1218).
//
// The transform is:
//
//	$SHELL -il -c '<command>'
//
// where $SHELL is the user's configured shell (e.g. /bin/zsh, /bin/bash).
// For bash, ~/.bashrc is sourced explicitly before the command because
// interactive login bash does not read it automatically.
//
// This solves the issue where OpenCode MCP configs with {env:VAR} references
// fail when launched from the TUI because agent-deck spawns the agent directly
// without going through the user's interactive shell environment.
//
// No-op when the flag is off, the command is empty, the session is sandboxed
// (container already handles environment), or for shell tools (to avoid
// double-wrapping). SSH remote sessions are also excluded because the remote
// SSH invocation should handle the login shell setup.
func (i *Instance) wrapLaunchShell(command string) string {
	if command == "" || i.IsSandboxed() || !i.launchShellEnabled() {
		return command
	}
	// Don't wrap shell sessions or SSH sessions
	if i.Tool == "shell" || i.SSHHost != "" {
		return command
	}
	// Get the shell from environment, default to bash
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	// Escape single quotes in the command for safe shell quoting
	escaped := strings.ReplaceAll(command, "'", "'\"'\"'")
	if filepath.Base(shell) == "bash" {
		return fmt.Sprintf("%s -il -c 'if [ -f ~/.bashrc ]; then source ~/.bashrc; fi; %s'", shell, escaped)
	}
	return fmt.Sprintf("%s -il -c '%s'", shell, escaped)
}

// prepareCommand applies the full command wrapping chain: user wrapper → sandbox → ignore-suspend.
// Returns the wrapped command, the sandbox container name (empty if not sandboxed), and an error.
// All code paths that launch or respawn a tmux pane should use this instead of calling
// applyWrapper/wrapForSandbox/wrapIgnoreSuspend individually.
func (i *Instance) prepareCommand(cmd string) (string, string, error) {
	// Exit-to-shell wrap FIRST, on the bare agent command, so the agent's own
	// `exec ` launcher is still visible to neutralise and the trailing shell
	// exec stays the outermost statement before any user-wrapper / bash -c /
	// SSH layering. No-op unless opt-in for a built-in agent (issue #1161).
	cmd = i.wrapExitToShell(cmd)

	// Launch-shell wrap SECOND, before user wrapper, so the interactive shell
	// loads its startup files and then executes the complete command (with
	// exit-to-shell suffix if enabled). This ensures env vars from ~/.zshrc,
	// ~/.bashrc, etc. are available to the agent and any trailing shell
	// (issue #1218). No-op unless opt-in.
	cmd = i.wrapLaunchShell(cmd)

	// Apply the user wrapper THIRD so that extra args folded into a
	// "{command} --flag1 --flag2" wrapper template become part of the string
	// that the bash -c wrap protects. Previously the order was reversed
	// (bash -c wrap then wrapper substitution), which produced
	// "bash -c '<cmd>' --flag1 --flag2" — bash treated --flag1/--flag2 as
	// positional parameters ($0, $1, …) and the child process never saw them.
	// See issue #601.
	wrapped, err := i.applyWrapper(cmd)
	if err != nil {
		return "", "", err
	}

	// Wrap the fully-substituted command under bash -c when a wrapper is
	// configured. This keeps shell metacharacters (&&, $(), inline env) in the
	// base command from leaking into the outer shell parse, and — critically —
	// keeps trailing wrapper-suffix flags INSIDE a single quoted argv so they
	// reach the child process intact.
	//
	// A SubcommandPassthrough Tool=="shell" instance needs the same wrap for
	// a different reason even though it has no configured wrapper:
	// buildShellPassthroughCommand injects bash/POSIX-only syntax ahead of
	// the subcommand — `export VAR='val' && `, `[ -f x ] && . x && `, inline
	// `VAR=val cmd` prefix assignments. Tool=="shell" instances are delivered
	// via tmux send-keys into the user's actual interactive login shell
	// (RunCommandAsInitialProcess is false for them), which is not
	// necessarily bash — a fish user's pane would receive a fish syntax
	// error ("Unsupported use of '='") instead of running the subcommand
	// (Claude review, PR #1821 HIGH #2). Explicitly invoking `bash -c '...'`
	// is itself valid syntax in any shell, so wrapping guarantees the
	// injected bash syntax is interpreted by bash regardless of the user's
	// default shell.
	if i.hasEffectiveWrapper() || (i.Tool == "shell" && i.SubcommandPassthrough) {
		escaped := strings.ReplaceAll(wrapped, "'", "'\"'\"'")
		wrapped = fmt.Sprintf("bash -c '%s'", escaped)
	}
	wrapped = i.wrapForSSH(wrapped)
	wrapped, containerName, err := i.wrapForSandbox(wrapped)
	if err != nil {
		return "", "", err
	}
	// Only disable Ctrl+Z suspend for sandboxed sessions where the command
	// runs as the pane's initial process (no interactive shell for job control).
	// Non-sandbox sessions use send-keys into an interactive shell, so Ctrl+Z
	// naturally suspends the process and the user can `fg` to resume.
	if wrapped != "" && i.IsSandboxed() {
		wrapped = wrapIgnoreSuspend(wrapped)
	}
	return wrapped, containerName, nil
}

// terminalEnvVars are always passed through to containers for proper UI/theming.
var terminalEnvVars = []string{"TERM", "COLORTERM", "FORCE_COLOR", "NO_COLOR", "COLORFGBG"}

// collectDockerEnvVars returns host environment variables to forward to containers.
// Each call reads fresh values from the host environment via os.LookupEnv so that
// changes between session starts (e.g. updated TERM) are picked up immediately.
// Terminal-related variables (TERM, COLORTERM, FORCE_COLOR, NO_COLOR, COLORFGBG) are always
// included when set. Additional names from DockerSettings.Environment are appended.
func collectDockerEnvVars(names []string) map[string]string {
	env := make(map[string]string, len(terminalEnvVars)+len(names))
	for _, name := range terminalEnvVars {
		if val, ok := os.LookupEnv(name); ok {
			env[name] = val
			continue
		}
		if name == "COLORFGBG" {
			env[name] = ThemeColorFGBG()
		}
	}
	for _, name := range names {
		if val, ok := os.LookupEnv(name); ok {
			env[name] = val
		}
	}
	return env
}

// ensureSandboxContainer creates and starts the Docker container if needed,
// then returns the tool command wrapped in "docker exec" and the container name.
// The userCfg parameter avoids a redundant LoadUserConfig call — the caller
// (wrapForSandbox) already loaded it.
func ensureSandboxContainer(inst *Instance, userCfg *UserConfig, toolCommand string) (string, string, error) {
	// Use a bounded context to prevent indefinite hangs if Docker is unresponsive.
	// Image pulls may take longer, but CheckAvailability/Exists/Create/Start should be fast.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := docker.CheckAvailability(ctx); err != nil {
		return "", "", fmt.Errorf("sandbox unavailable: %w", err)
	}

	if err := docker.EnsureImage(ctx, inst.Sandbox.Image); err != nil {
		return "", "", fmt.Errorf("ensuring sandbox image: %w", err)
	}

	containerName := docker.GenerateName(inst.ID, inst.Title)
	ctr := docker.NewContainer(containerName, inst.Sandbox.Image)

	homeDir, homeErr := os.UserHomeDir()
	if homeErr != nil {
		sessionLog.Warn("user_home_dir", slog.String("error", homeErr.Error()))
	}

	// Skip agent config sync when home directory is unavailable — RefreshAgentConfigs
	// would produce broken paths rooted at "/" with an empty homeDir.
	var bindMounts []docker.VolumeMount
	var homeMounts []docker.VolumeMount
	if homeDir != "" {
		bindMounts, homeMounts = docker.RefreshAgentConfigs(homeDir, "")
		if IsCodexCompatible(inst.Tool) {
			if err := PreAcceptCodexSandboxWorkspaceTrust(homeDir); err != nil {
				sessionLog.Warn("codex_sandbox_preaccept_trust_failed",
					slog.String("instance_id", inst.ID),
					slog.String("error", err.Error()))
			}
		}
	}

	if err := ensureContainerRunning(ctx, inst, ctr, userCfg, homeDir, bindMounts, homeMounts); err != nil {
		return "", "", err
	}

	return buildExecCommand(ctr, userCfg, toolCommand), containerName, nil
}

// ensureContainerRunning creates and starts the container if it doesn't exist or is stopped.
func ensureContainerRunning(
	ctx context.Context,
	inst *Instance,
	ctr *docker.Container,
	userCfg *UserConfig,
	homeDir string,
	bindMounts []docker.VolumeMount,
	homeMounts []docker.VolumeMount,
) error {
	exists, err := ctr.Exists(ctx)
	if err != nil {
		return fmt.Errorf("checking sandbox container: %w", err)
	}

	if !exists {
		cfg := buildSandboxConfig(inst, userCfg, homeDir, bindMounts, homeMounts)
		if _, createErr := ctr.Create(ctx, cfg); createErr != nil {
			return fmt.Errorf("creating sandbox container: %w", createErr)
		}
	}

	running, err := ctr.IsRunning(ctx)
	if err != nil {
		return fmt.Errorf("checking sandbox container status: %w", err)
	}
	if !running {
		if startErr := ctr.Start(ctx); startErr != nil {
			return fmt.Errorf("starting sandbox container: %w", startErr)
		}
	}

	// Migration guard: older containers were created with root-owned tmpfs mounts
	// for /root/.npm and /root/.cache. With --user uid:gid this causes plugin
	// bootstrap failures (EACCES mkdir '/root/.npm/_cacache'). Recreate the
	// container once if those paths are not writable.
	cacheWritable := sandboxCacheDirsWritable(ctx, ctr)
	tmpExecutable := sandboxTmpExecutable(ctx, ctr)
	if !cacheWritable || !tmpExecutable {
		sessionLog.Warn(
			"sandbox_recreating_for_runtime_compat",
			slog.Bool("cache_writable", cacheWritable),
			slog.Bool("tmp_executable", tmpExecutable),
		)
		if rmErr := ctr.Remove(ctx, true); rmErr != nil {
			return fmt.Errorf("removing incompatible sandbox container: %w", rmErr)
		}
		cfg := buildSandboxConfig(inst, userCfg, homeDir, bindMounts, homeMounts)
		if _, createErr := ctr.Create(ctx, cfg); createErr != nil {
			return fmt.Errorf("recreating sandbox container: %w", createErr)
		}
		if startErr := ctr.Start(ctx); startErr != nil {
			return fmt.Errorf("starting recreated sandbox container: %w", startErr)
		}
	}

	return nil
}

func sandboxCacheDirsWritable(ctx context.Context, ctr *docker.Container) bool {
	return sandboxExecProbe(ctx, ctr, "test -w /root/.npm && test -w /root/.cache")
}

func sandboxTmpExecutable(ctx context.Context, ctr *docker.Container) bool {
	probe := `f=/tmp/.agent_deck_exec_probe.sh; printf '#!/bin/sh\nexit 0\n' > "$f" && chmod +x "$f" && "$f" >/dev/null 2>&1 && rm -f "$f"`
	return sandboxExecProbe(ctx, ctr, probe)
}

func sandboxExecProbe(ctx context.Context, ctr *docker.Container, script string) bool {
	prefix := ctr.ExecPrefixNonInteractive()
	args := append(prefix[1:], "bash", "-lc", script)
	// #nosec G204 -- prefix comes from docker.Container.ExecPrefixNonInteractive
	// (returns ["docker", "exec", containerName]); script is a hardcoded probe
	// snippet from callers above. No external input.
	_, err := exec.CommandContext(ctx, prefix[0], args...).CombinedOutput()
	return err == nil
}

// buildSandboxConfig assembles the ContainerConfig from session and user settings.
func buildSandboxConfig(
	inst *Instance,
	userCfg *UserConfig,
	homeDir string,
	bindMounts []docker.VolumeMount,
	homeMounts []docker.VolumeMount,
) *docker.ContainerConfig {
	var cpuLimit, memLimit string
	if inst.Sandbox.CPULimit != nil {
		cpuLimit = *inst.Sandbox.CPULimit
	}
	if inst.Sandbox.MemoryLimit != nil {
		memLimit = *inst.Sandbox.MemoryLimit
	}
	if cpuLimit == "" && userCfg != nil {
		cpuLimit = userCfg.Docker.CPULimit
	}
	if memLimit == "" && userCfg != nil {
		memLimit = userCfg.Docker.MemoryLimit
	}

	configOpts := []docker.ContainerConfigOption{
		docker.WithCPULimit(cpuLimit),
		docker.WithMemoryLimit(memLimit),
		docker.WithAgentConfigs(bindMounts, homeMounts),
	}

	// Bridge in-container hook-handler status writes to a PER-INSTANCE host dir.
	// The container's own hooks path sits on the read-only rootfs, so without
	// this mount Stop/transition events from sandboxed sessions are lost. The
	// dir is scoped to this instance (…/hooks/sandbox/<id>) rather than the
	// global fleet-wide hooks dir. Three properties keep this safe: (1) only this
	// instance's subdir is mounted, so a compromised container can read/write
	// files ONLY inside its own subdir — it can never see siblings' or the
	// conductor's status; (2) the host StatusFileWatcher keys a scoped file
	// by its OWNING SUBDIR and ingests only <id>.json, so a container cannot
	// forge a sibling's transition (or inject a done_summary into the conductor)
	// by naming a file after a victim inside its own subdir; and (3) every host
	// read of a status file is no-follow (O_NOFOLLOW, plus Lstat on the scoped
	// path) and size-bounded, so the container cannot symlink its own <id>.json
	// at a sibling/host file or /dev/zero, nor write a huge <id>.json, to read
	// host files or DoS the shared notify-daemon. The host read path
	// (readHookStatusFile / hookStatusFilePath) also builds the path from the
	// requested <id>, so it cannot be cross-attributed either.
	if hooksDir := GetHooksDir(); hooksDir != "" {
		perInstanceDir := filepath.Join(hooksDir, "sandbox", inst.ID)
		if mkErr := os.MkdirAll(perInstanceDir, 0o700); mkErr == nil {
			configOpts = append(configOpts, docker.WithHooksDir(perInstanceDir))
		} else {
			// Don't fail the spawn, but surface it: without the scoped hooks dir
			// the bridge mount is silently skipped, leaving the host watcher blind
			// to this sandboxed session — the exact problem this bridge solves.
			sessionLog.Warn("scoped_hooks_dir_create_failed",
				slog.String("instance_id", inst.ID),
				slog.String("dir", perInstanceDir),
				slog.String("error", mkErr.Error()))
		}
	}

	// Note: Docker.Environment names (e.g. TERM) are NOT forwarded at create time.
	// They are forwarded at exec time via buildExecCommand with fresh host values.
	// Only Docker.EnvironmentValues (static key=value pairs) are baked into the container.

	if homeDir != "" {
		gitconfigPath := filepath.Join(homeDir, ".gitconfig")
		if _, statErr := os.Stat(gitconfigPath); statErr == nil {
			configOpts = append(configOpts, docker.WithGitConfig(gitconfigPath))
		}
	}

	if userCfg != nil && userCfg.Docker.MountSSH && homeDir != "" {
		sshPath := filepath.Join(homeDir, ".ssh")
		if _, statErr := os.Stat(sshPath); statErr == nil {
			configOpts = append(configOpts, docker.WithSSH(sshPath))
		}
	}

	if userCfg != nil && len(userCfg.Docker.VolumeIgnores) > 0 {
		configOpts = append(configOpts, docker.WithVolumeIgnores(userCfg.Docker.VolumeIgnores))
	}

	if userCfg != nil && len(userCfg.Docker.ExtraVolumes) > 0 {
		configOpts = append(configOpts, docker.WithExtraVolumes(userCfg.Docker.ExtraVolumes))
	}

	if userCfg != nil && len(userCfg.Docker.EnvironmentValues) > 0 {
		configOpts = append(configOpts, docker.WithEnvironment(userCfg.Docker.EnvironmentValues))
	}

	// Multi-repo: mount each path under /workspace/<dirname> instead of single project mount.
	if inst.MultiRepoEnabled {
		configOpts = append(configOpts, docker.WithMultiRepoPaths(inst.AllProjectPaths()))
	}

	return docker.NewContainerConfig(inst.ProjectPath, configOpts...)
}

// buildExecCommand returns a shell-safe "docker exec ... bash -c toolCommand" string.
// The two-layer bash -c architecture:
//  1. Inner: docker exec ... bash -c <toolCommand> — runs the agent command inside
//     the container, properly shell-quoted by ShellJoinArgs.
//  2. Outer: wrapIgnoreSuspend wraps the entire string in bash -c with stty susp undef,
//     which tmux then delivers via its implicit /bin/sh -c.
//
// This prevents shell injection: toolCommand (which may contain user-controlled text
// like session IDs) is passed as a single quoted argument to bash -c inside the container.
func buildExecCommand(ctr *docker.Container, userCfg *UserConfig, toolCommand string) string {
	// Always collect terminal env vars; append user-configured env var names.
	var userNames []string
	if userCfg != nil {
		userNames = userCfg.Docker.Environment
	}
	runtimeEnv := collectDockerEnvVars(userNames)

	var prefix []string
	if len(runtimeEnv) > 0 {
		prefix = ctr.ExecPrefixWithEnv(runtimeEnv)
	} else {
		prefix = ctr.ExecPrefix()
	}
	// Wrap toolCommand in bash -c inside the container so it is passed as a single
	// shell-quoted argument, preventing injection of shell metacharacters.
	return docker.ShellJoinArgs(append(prefix, "bash", "-c", toolCommand))
}

// generateUUID generates a cryptographically random UUID v4 as a lowercase string.
// Pre-generating in Go (instead of using shell $(uuidgen)) ensures the ID is immediately
// known to the Instance and avoids Docker-sandbox failures where uuidgen is unavailable.
func generateUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: timestamp-based placeholder; unique but not a valid UUID v4.
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits (RFC 4122)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// generateID generates a unique session ID
// GenerateID creates a unique session identifier.
func GenerateID() string {
	return fmt.Sprintf("%s-%d", randomString(8), time.Now().Unix())
}

// randomString generates a random hex string of specified length
func randomString(length int) string {
	bytes := make([]byte, length/2)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to timestamp-based ID
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}

// UpdateClaudeSessionsWithDedup clears duplicate Claude session IDs across instances.
// The oldest session (by CreatedAt) keeps its ID, newer duplicates are cleared.
// With tmux env being authoritative, duplicates shouldn't occur in normal use,
// but we handle them defensively for loaded/migrated sessions.
func UpdateClaudeSessionsWithDedup(instances []*Instance) {
	// Work on a copy so callers don't observe order mutation as a side effect.
	ordered := make([]*Instance, len(instances))
	copy(ordered, instances)

	// Sort instances by CreatedAt (older first get priority for keeping IDs).
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
	})

	// Find and clear duplicate IDs (keep only the oldest session's claim)
	idOwner := make(map[string]*Instance)
	for _, inst := range ordered {
		if !IsClaudeCompatible(inst.Tool) || inst.ClaudeSessionID == "" {
			continue
		}
		if owner, exists := idOwner[inst.ClaudeSessionID]; exists {
			// Duplicate found! The older session (owner) keeps the ID
			// Clear the newer session's ID (it will get a new one from tmux env)
			inst.ClaudeSessionID = ""
			inst.ClaudeDetectedAt = time.Time{}
			_ = owner // Older session keeps its ID
		} else {
			idOwner[inst.ClaudeSessionID] = inst
		}
	}
	// No re-detection step - tmux env is the authoritative source
	// Sessions will get their IDs from UpdateClaudeSession() during normal status updates
}

// wrapIgnoreSuspend wraps cmd in a bash -c invocation that disables CTRL+Z
// suspension before running the command. This is the sole bash -c layer
// in the command chain — ensureSandboxContainer returns a plain docker exec
// string, matching AoE's single-wrapper pattern.
//
// No `exec` is used: compound commands (&&, ||, pipes) must remain valid
// when passed through Restart() resume paths. The extra shell process is
// negligible overhead.
func wrapIgnoreSuspend(cmd string) string {
	// Escape single quotes for safe embedding inside a single-quoted string.
	// Pattern: end quote, add double-quoted literal quote, restart quote.
	// Example: it's -> it'"'"'s -> bash sees: it's.
	escaped := strings.ReplaceAll(cmd, `'`, `'"'"'`)
	return "bash -c 'stty susp undef; " + escaped + "'"
}
