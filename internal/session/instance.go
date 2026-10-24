package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/asheshgoplani/agent-deck/internal/docker"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/termtext"
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
	SubstateInteractiveMenu   = tmux.SubstateInteractiveMenu
	SubstateBackgroundWork    = tmux.SubstateBackgroundWork
	SubstateWatching          = tmux.SubstateWatching
	SubstateModelUnavailable  = tmux.SubstateModelUnavailable
	SubstateAuth401           = tmux.SubstateAuth401
	SubstateUsageLimit        = tmux.SubstateUsageLimit
	SubstateModelLimit        = tmux.SubstateModelLimit
	SubstateAccountBlocked    = tmux.SubstateAccountBlocked
	SubstateUnknownExit       = tmux.SubstateUnknownExit
	SubstateProcessExited     = tmux.SubstateProcessExited
	SubstateHookLag           = tmux.SubstateHookLag
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
	codexHookWaitingFastPathWindow = 5 * time.Second
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
	opencodeSSEFreshnessWindow   = 30 * time.Second
	codexProbeMissingSentinel    = "__AGENT_DECK_MISSING_TOOL__"
	codexProbeIncompleteSentinel = "__AGENT_DECK_INCOMPLETE_PROBE__"
	// codexLsofProbeTimeout hard-caps a single lsof invocation so a slow or
	// hung child can never stall the shared status pass. lsof is also run with
	// -n -P (no host/port name resolution) to avoid reverse-DNS PTR lookups on
	// the codex process's open sockets, which block ~30s each on resolvers that
	// silently drop PTR queries (issue #1581).
	codexLsofProbeTimeout = 2 * time.Second
)

// Instance represents a single agent/shell session
type Instance struct {
	storageSnapshot *instanceStorageSnapshot

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

	// ConductorName is the stable conductor key ([conductors.<name>],
	// meta.json), stamped by `conductor setup` and persisted in the tool_data
	// extras zone. The title is display-only and may be decorated or renamed.
	// Read it via ResolveConductorName (conductor_identity.go), which also
	// covers old records through the title fallback.
	ConductorName   string     `json:"conductor_name,omitempty"`
	conductorNameMu sync.Mutex // guards ConductorName against the lazy fill

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
	// TrackCommandExit records an explicit shell command created by the CLI.
	// Older interactive shell sessions keep their shell after a typed command.
	TrackCommandExit bool `json:"track_command_exit,omitempty"`

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

	Command string `json:"command"`
	Wrapper string `json:"wrapper,omitempty"` // Optional wrapper command with {command} placeholder
	Tool    string `json:"tool"`
	// observedTool is the runtime DetectTool last reported (a foreground CLI
	// or shell). It is not persisted and is
	// not consulted by Start/Restart: detection must not change what restarts.
	// Empty until the first status sample that reaches DetectTool.
	observedTool string

	Status         Status    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	LastAccessedAt time.Time `json:"last_accessed_at,omitempty"` // When user last attached
	// ArchivedAt is set when the user archives the session (non-zero = archived).
	ArchivedAt time.Time `json:"archived_at,omitempty"`
	// SupersededBy/Supersedes form a reversible cross-harness handoff lineage.
	// The original row and native transcript remain intact; only its default
	// visibility changes after the destination is proven ready and committed.
	SupersededBy string `json:"superseded_by,omitempty"`
	Supersedes   string `json:"supersedes,omitempty"`

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
	// usageLimitedCached memoises the verdict for that window (usageLimitVerdict
	// carries its kind and detail: usage-limit, model-limit or account-blocked),
	// and usageLimitSessionID records the session id it was formed for so a
	// rebind cannot inherit it. usageLimitScanGen versions in-flight scans so one
	// slower than the interval cannot publish over a newer result.
	// shownTranscriptLimit is the transcript verdict the last Substate() call
	// returned, so SubstateDetail describes the substate actually reported.
	lastUsageLimitScanAt time.Time
	usageLimitedCached   bool
	usageLimitVerdict    limitVerdict
	shownTranscriptLimit limitVerdict
	usageLimitSessionID  string
	usageLimitScanGen    uint64
	lastCodexProbeAt     time.Time // Rate-limits expensive Codex process-file probes
	// pendingCodexRestartWarning is consumed by UI/CLI after Restart() succeeds.
	// It is intentionally transient and never persisted.
	pendingCodexRestartWarning string `json:"-"`

	// Pi CLI integration. Pi's native JSONL filename is generated by Pi, so both
	// the native identity and its exact instance-scoped path are persisted once
	// observed. They prevent later exports from selecting a neighbouring Pi file.
	PiSessionID   string `json:"pi_session_id,omitempty"`
	PiSessionPath string `json:"pi_session_path,omitempty"`

	// Hermes CLI integration. HermesSessionID is the native session id this
	// entry's OWN hook events reported (issue #2582); restart resumes exactly
	// that id with --resume. Persisted in the tool_data extras zone
	// (hermes_session_persist.go), not through this struct's JSON.
	// Only parent-only hook edges bind or rotate it (hermesBindsOnEvent);
	// hermesBoundAt orders rebinds against stale samples, and
	// hermesLastRejected rate-limits the reject lifecycle line.
	HermesSessionID      string `json:"-"`
	HermesHookGeneration string `json:"-"`
	// hermesStartFresh makes the next Start begin a new Hermes conversation
	// (RestartFresh) instead of resuming the recorded one.
	hermesStartFresh   bool
	hermesBoundAt      time.Time
	hermesLastRejected string

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
	// crossHarnessLaunch is an executor-only immutable launch binding. It is
	// never persisted and bypasses config re-resolution for one fresh target.
	crossHarnessLaunch *FreshTargetLaunchPlan

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

	// IdentityInjectionDisabled is the per-session opt-out of the harness
	// identity injection (see identity_injection.go): when true, the spawn
	// neither writes the identity file nor passes the harness-specific
	// instruction flag, regardless of [launch].inject_identity. Persisted in
	// the tool_data extras zone so a restart honours the choice. CLI flag:
	// `--no-identity` on `agent-deck add` / `agent-deck launch`.
	IdentityInjectionDisabled bool `json:"identity_injection_disabled,omitempty"`

	// ContextLevel is the per-session override of the harness context-level
	// (issue #2260): "none", "primer", or "full". Empty means unset — falls
	// through to the group then global config (global < group < session
	// precedence; see EffectiveContextLevel). Persisted in the tool_data
	// extras zone (context_level_persist.go). CLI:
	// `session set <id> context-level <none|primer|full>` (empty clears).
	ContextLevel string `json:"context_level,omitempty"`

	// QueuedMessage is the -m prompt of a session queued at its group's
	// max_concurrent cap. Every path off the queue (Storage.StartQueued, a
	// direct or TUI start, a restart) takes it off the row in the same write
	// as the status change (StateDB.TakeQueued) and only then hands it to the
	// send queue, so it is delivered once and never lingers here.
	// Persisted in the tool_data extras zone (queued_start.go).
	QueuedMessage string `json:"queued_message,omitempty"`

	// PromptSuggestions is the per-session override for Claude Code prompt
	// suggestions: "children-off", "on" or "off"; empty means unset (profile,
	// then [launch] config, then children-off). See prompt_suggestions.go.
	// CLI: `session set <id> prompt-suggestions <children-off|on|off>` (empty
	// clears).
	PromptSuggestions string `json:"prompt_suggestions,omitempty"`

	// IntendedSwitch marks the current account/model as switched on purpose,
	// until its ExpiresAt (limit-guard reads it from list/show --json). Nil
	// when unset. Persisted in the tool_data extras zone (intended_switch.go).
	// CLI: `session set <id> account|model <v> --intended "<reason>"`, or
	// `session set <id> intended-switch "<reason>"` ("" clears). Any other
	// account or model change clears it.
	IntendedSwitch *IntendedSwitch `json:"intended_switch,omitempty"`

	// LimitVerdictResetAt is when the session's account or model was last
	// changed. A transcript limit/block rejection stamped at or before it is
	// about the old account/model and is ignored (usagelimit.go). Zero when
	// never set. Persisted in the tool_data extras zone
	// (limit_verdict_reset.go).
	LimitVerdictResetAt time.Time `json:"limit_verdict_reset_at,omitempty"`

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

	// WorkerScratchConfigDir is the active immutable CLAUDE_CONFIG_DIR
	// generation prepared for a non-conductor claude worker (issue #59,
	// v1.7.68). The scratch dir copies the ambient profile's settings.json
	// with the telegram plugin explicitly disabled and symlinks the rest of
	// the profile. A restart always receives a fresh generation; prior
	// generations are retained rather than repointed or asynchronously
	// removed. Empty for conductor sessions, explicit telegram channel
	// owners, and non-claude tools — they use the ambient profile as-is.
	WorkerScratchConfigDir string `json:"worker_scratch_config_dir,omitempty"`

	// IdleTimeoutSecs is the auto-stop threshold (#1143). When > 0, a central
	// watcher poll triggers Kill() if the tmux pane content stays unchanged
	// for this many seconds. 0 = disabled (current behavior). Default is 0
	// so existing sessions are unaffected on upgrade.
	IdleTimeoutSecs int64 `json:"idle_timeout_secs,omitempty"`

	// Favorite marks the session as a favourite: `session set <id> favorite
	// true|false`, shown in list/show JSON. Persisted in the tool_data extras
	// zone (favorite_persist.go).
	Favorite bool `json:"favorite,omitempty"`
	// favoriteCleared is set when `session set favorite false` clears a
	// favourite, so the save writes an explicit false (favorite_persist.go).
	favoriteCleared bool

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

	tmuxSession              *tmux.Session // Internal tmux session
	conductorRecoveryWarning string        // protected by mu; latest spawn only
	// Database that last loaded or saved this instance. Restart bookkeeping must
	// return to that profile instead of whichever database is process-global.
	restartDB atomic.Pointer[statedb.StateDB]

	paneDeadExitStatusForTest func() (int, bool) // nil uses tmuxSession.PaneDeadExitStatus

	// terminatedPaneSubstate is set alongside i.Status by classifyTerminatedPane
	// (via terminatedPaneStatus/applyTerminatedPaneStatus). It is SubstateNone
	// except for SubstateUnknownExit (#2091): a terminated-pane StatusError
	// verdict reached with no exit code AND no hook record, i.e. a guess, not a
	// fact. Recomputed on every terminated-pane classification and cleared on
	// restart (see Start / the "session exists again" branch in UpdateStatus),
	// so it never persists across a lost process (a cold reload recomputes it
	// from the same exit-code/hook-file evidence, no disk state needed).
	terminatedPaneSubstate Substate
	terminatedPaneExitCode *int

	// Hook-based status detection (set by StatusFileWatcher from Claude Code hooks)
	hookStatus    string // running, idle, waiting, dead (empty = no hook data)
	hookEvent     string // Hook event name that caused the last status (e.g. "PermissionRequest")
	hookSessionID string // Session ID from hook payload
	// claudeSupersededIDs holds Claude session ids this instance moved away
	// from by following a continued-in handoff (#2683). A late hook still
	// carrying one must not revert ClaudeSessionID to the stalled copy.
	claudeSupersededIDs         map[string]struct{}
	hookLastUpdate              time.Time // When hook status was last received
	linkedClaudeSessionID       string    // Last Claude id confirmClaudeSessionLink wrote a session_links row for
	skipRecoveryTurn            bool      // SkipConductorRecoveryTurn: the next spawn sends no recovery turn
	restartReason               string    // SetRestartReason: why the next restart happens, for its resume note
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

	// archiveMu guards ArchivedAt alone (IsArchived, SetArchivedAtThreadSafe).
	// It is not mu: UpdateStatus holds mu across tool detection and metadata
	// I/O, and the TUI checks the archive state of every row it renders.
	archiveMu sync.RWMutex

	// spawnGen supersedes stale fast-death watchers; spawnWatchers lets teardown
	// and tests join them before restoring process-global filesystem environment.
	spawnGen      atomic.Uint64
	spawnWatchers sync.WaitGroup

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

	// stopRevision is guarded by mu and invalidates unlocked liveness probes.
	stopRevision uint64

	// tmuxFlipFromRunningPending debounces a purely tmux-inferred flip AWAY from
	// running (→ waiting/error). A long single tool-call (past the hook freshness
	// window) or transient subprocess churn can momentarily present the pane as a
	// shell prompt or a capture error, then recover; without a confirming second
	// sample that single transient read fires a false completion/error to the
	// conductor. Set when we hold the first such sample at running; cleared on any
	// settled (running/idle) outcome or once the flip is confirmed. Not serialized.
	tmuxFlipFromRunningPending bool

	// statusSampledLive records that THIS process has settled at least one
	// status verdict for the instance (hook fast path, SSE, or a tmux sample).
	// Until then i.Status is only the persisted row — what the notify daemon's
	// db.WriteStatus or `session start` left behind — and must not seed the
	// running debounce above: a one-pass process (`list --json`, `session
	// show`, the remote-agent probe, each daemon pass, which re-hydrates its
	// instances) never takes the confirming second sample, so a row loaded as
	// "running" beside an idle pane stayed green forever (the "remotes always
	// green" codex-without-hooks case). Not serialized, by design.
	statusSampledLive bool

	// Hook-lag evidence (see hook_lag.go): the samples of a completed turn at
	// an idle prompt taken while the hook file still said running, keyed by
	// the hook event they were taken under. Persisted in tool_data.hook_lag
	// so every process (one-pass CLI, daemon, TUI) reasons from the same
	// samples; hookLagPersisted is what this process last wrote.
	hookLag          hookLagRecord
	hookLagPersisted hookLagRecord
	// hookLagFlipped records that the CURRENT status pass set waiting from
	// the hook-lag record alone (no capture of its own), so a busy frame the
	// same pass captures afterwards can revert it (review round 3 P2-3).
	hookLagFlipped bool

	// Background work (issue #2473, background_work.go). bgWork is the last
	// merged pane+transcript verdict; bgWorkActive marks a running status that
	// exists BECAUSE of it (the foreground turn ended); bgWorkPaneSeenAt is
	// the last time the pane itself showed the work (the transcript-only hold
	// counts from it). bgTranscript* cache the transcript path per Claude
	// session id.
	bgWork           tmux.BackgroundWork
	bgWorkActive     bool
	bgWorkPaneSeenAt time.Time
	bgTranscriptSID  string
	bgTranscriptPath string
	bgTranscriptAt   time.Time
	// hookLagDB is the profile database this instance was loaded from, so a
	// CLI process (which registers no global StateDB) can persist the record
	// to the row it read. Nil for instances not loaded from storage.
	hookLagDB *statedb.StateDB

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

// GetToolThreadSafe returns the configured launch tool with read-lock protection.
// Restart and resume read this, not the observed runtime.
func (inst *Instance) GetToolThreadSafe() string {
	inst.mu.RLock()
	t := inst.Tool
	inst.mu.RUnlock()
	return t
}

// ObservedTool returns the runtime identity last reported by DetectTool.
// It is empty until a status sample runs, and it is not the launch identity.
func (inst *Instance) ObservedTool() string {
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	return inst.observedTool
}

// DisplayToolThreadSafe is the tool to show. A foreground agent
// wins over the configured shell so the list matches what is actually running;
// a shell observation does not hide a configured agent, and neither value is
// what Restart launches. OpenCode observations preserve the configured
// opencode2 launcher label without changing the canonical dispatch identity.
func (inst *Instance) DisplayToolThreadSafe() string {
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	tool := inst.Tool
	if inst.observedTool != "" && inst.observedTool != "shell" {
		tool = inst.observedTool
	}
	if tool == "opencode" && inst.Tool == "opencode" && openCodeExecutableBase(inst.Command) == "opencode2" {
		return "opencode2"
	}
	return tool
}

// GetAccountThreadSafe returns the stored slot, not a resolved login identity.
func (inst *Instance) GetAccountThreadSafe() string {
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	return inst.Account
}

// setAccountThreadSafe exchanges the stored slot under the snapshot reader's lock.
func (inst *Instance) setAccountThreadSafe(account string) string {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	old := inst.Account
	inst.Account = account
	return old
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
	t = termtext.Clean(t)
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
	if err := inst.withOwningStateDB(func(db *statedb.StateDB) error {
		return db.WriteLastAccessed(inst.ID, inst.LastAccessedAt)
	}); err != nil && !errors.Is(err, errNoStateDB) {
		sessionLog.Debug("last_accessed_persist_failed",
			slog.String("instance", inst.ID),
			slog.String("error", err.Error()),
		)
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
	title = termtext.Clean(title) // see NewInstanceWithTool
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
	tmuxSess.SetIndicZeroWidthMarks(GetTmuxSettings().IndicZeroWidthMarks)
	tmuxSess.SetClearOnRestart(GetTmuxSettings().ClearOnRestart)
	tmuxSess.SetTerminalChromeEnabled(GetTerminalSettings().GetITermBadge())

	inst := &Instance{
		ID:               id,
		Account:          strings.TrimSpace(os.Getenv("AGENTDECK_ACCOUNT")),
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
	tmuxSess.GroupPath = inst.GroupPath
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
	inst.tmuxSession.GroupPath = groupPath
	return inst
}

// NewInstanceWithTool creates a new session with tool-specific initialization
func NewInstanceWithTool(title, projectPath, tool string) *Instance {
	// The title is printed by `list` and rendered in the TUI and the tmux
	// status line; a control character in it could drive the terminal.
	title = termtext.Clean(title)
	id := GenerateID()
	socket := GetTmuxSettings().GetSocketName()
	tmuxSess := tmux.NewSession(title, projectPath)
	tmuxSess.SocketName = socket
	tmuxSess.InstanceID = id // Pass instance ID for activity hooks
	tmuxSess.SetInjectStatusLine(GetTmuxSettings().GetInjectStatusLine())
	tmuxSess.SetMouse(GetTmuxSettings().GetMouse())
	tmuxSess.SetIndicZeroWidthMarks(GetTmuxSettings().IndicZeroWidthMarks)
	tmuxSess.SetClearOnRestart(GetTmuxSettings().ClearOnRestart)
	tmuxSess.SetTerminalChromeEnabled(GetTerminalSettings().GetITermBadge())

	inst := &Instance{
		ID:               id,
		Account:          strings.TrimSpace(os.Getenv("AGENTDECK_ACCOUNT")),
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
	tmuxSess.GroupPath = inst.GroupPath

	if tool == "opencode2" {
		inst.Tool = "opencode"
		inst.Command = "opencode2"
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
	if inst.tmuxSession != nil {
		inst.tmuxSession.GroupPath = groupPath
	}
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

// Start starts the session in tmux.
//
// Issue #1040: gated by acquireInstanceSpawnLock plus a "spawned-while-
// we-waited" stamp so concurrent `agent-deck session start <id>`
// invocations after a Claude exit don't each fall through the "tmux
// session does not exist" gate and spawn parallel sessions. The lock
// and gate are inlined here (rather than wrapping the whole body in a
// SpawnAttempt helper) to preserve the structural-grep contract that
// checks Start()'s body for the #745 IsForkAwaitingStart guard.
func (i *Instance) Start() (startErr error) {
	defer func() { i.recordTelemetryStartError(startErr) }()
	if i.leftQueue() {
		defer func() {
			if startErr == nil {
				i.takeOffQueueAfterStart()
			}
		}()
	}
	if err := i.ValidateAccount(); err != nil {
		return err
	}
	beforeLock := spawnGenerationSnapshot(i.ID)
	release, lockErr := acquireInstanceSpawnLock(i.ID)
	if lockErr != nil {
		return lockErr
	}
	defer release()
	if spawnedSince(i.ID, beforeLock) {
		return nil
	}
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}
	var validatedDeepSeekCommand string
	if i.Tool == "deepseek" {
		var err error
		validatedDeepSeekCommand, err = i.deepSeekStartCommand()
		if err != nil {
			return err
		}
	}

	// #1873: refuse to spawn while this instance still owns a process tree from
	// an earlier spawn. Runs before anything is mutated or launched, so a
	// refusal leaves the session exactly as it was and signals nothing — and
	// before recordInstanceSpawn is deferred, so a refusal does not stamp a
	// spawn that never happened. That stamp is what a concurrent caller reads
	// to decide it can return "already started"; a refusal must not hand it
	// that answer.
	if err := i.guardOwnedProcessesBeforeSpawn("start"); err != nil {
		return err
	}
	defer recordInstanceSpawn(i.ID)

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
	case i.Tool == "omp":
		if i.IsForkAwaitingStart {
			command = i.consumeForkStartCommand()
			sessionLog.Info("resume: none reason=fork_awaiting_start",
				slog.String("instance_id", i.ID),
				slog.String("path", i.ProjectPath),
				slog.String("reason", "fork_awaiting_start"))
			break
		}
		command = i.buildOMPCommand(i.Command)
	case i.Tool == "copilot":
		command = i.buildCopilotCommand(i.Command)
	case i.Tool == "muse":
		command = i.buildMuseCommand(i.Command)
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
		i.preferHermesOwnedSessionForStart()
		command = i.buildHermesCommand(i.Command)
	case i.Tool == "deepseek":
		// The headless profile answers one task and exits, so a plain Start()
		// must replay a recorded task or refuse — `dsh --profile headless` with
		// no positional is a usage error dsh rejects before anything could be
		// delivered (PR #1942 review, P1b/P1c).
		command = validatedDeepSeekCommand
	default:
		// Check if this is a custom tool with session resume config
		if toolDef := GetToolDef(i.Tool); toolDef != nil {
			command = i.buildGenericCommand(i.Command)
		} else {
			command = i.buildShellPassthroughCommand(i.Command)
		}
	}

	// The tool the pane's probe checks and the marker it leaves when the
	// tool is missing, read off the bare command before any wrapper hides
	// it: the fast-death watcher tells "tool not on PATH" from a generic
	// early exit by that marker alone (spawn_path.go).
	probeTool, probeMarker := i.spawnToolLookup(command)

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
	i.tmuxSession.RunCommandAsInitialProcess = i.IsSandboxed() || i.Tool != "shell" || i.tracksCommandExit()
	i.applyLaunchSettingsFromConfig()

	// Re-assert the declarative per-group/per-conductor skill+mcp loadout
	// BEFORE the tool process starts so project-scope discovery sees it.
	// Idempotent: a healthy floor is a no-op; failures warn, never block.
	ApplyConfiguredLoadout(i)

	i.preAcceptCursorWorkspaceTrust()

	// Start the tmux session
	command, err = i.wrapTrackedCommandExit(command)
	if err != nil {
		return err
	}
	i.retireFastDeathWatchers() // #2805
	i.setInteractiveShellLaunchEnv()
	if err := i.tmuxSession.Start(command); err != nil {
		// #1580: persist the tmux-level failure so the preview / session show /
		// lifecycle log can surface it instead of a bare "error".
		i.recordTmuxStartFailure(command, err)
		return fmt.Errorf("failed to start tmux session: %w", err)
	}

	// gen AND the wake channel are both produced here, in the caller, so no
	// bump can slip into the gap before the watcher subscribes (see
	// newSpawnGenWatch). The ownership claim below shares the same pair: it
	// belongs to this spawn, and a second bump would supersede the very watcher
	// this call starts.
	gen, wake := i.newSpawnGenWatch()

	// #1873: record the durable ownership receipt — the pane process's pid
	// bound to its start identity — before anything can die. This is the only
	// moment at which the claim is provable.
	i.claimOwnershipAtSpawn(command, gen, wake)

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
		// Resolve both write targets HERE, synchronously in the caller, not
		// inside the goroutine: GetSessionIDLifecycleLogPath()/spawnFailureDir()
		// read the live $HOME, and watchForFastDeath's goroutine is never
		// joined — it can still be sleeping on its ticker when a later test
		// changes $HOME out from under it. Capturing the resolved paths as
		// plain values at spawn time (Go evaluates `go` call arguments in the
		// calling goroutine) makes the watcher's writes land in the HOME that
		// was live when this session started, never whichever HOME happens to
		// be live when the ticker next fires.
		i.startFastDeathWatcher(command, gen, wake, i.tmuxSession, i.ID, i.Tool, sessionLog, probeTool, probeMarker)
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
	i.ensureInteractiveShellEnv()
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
	i.mu.Lock()
	i.lastStartTime = time.Now()
	i.markStarted() // persisted stamp (issue #30 — cross-process freshness guard)
	i.lastErrorCheck = time.Time{}

	// A successful start supersedes the previous stop, including an
	// interactive shell with no command to initialize.
	if command != "" {
		i.Status = StatusStarting
	} else if i.Status == StatusStopped {
		i.Status = StatusIdle
	}
	i.mu.Unlock()

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

	i.recoverConductorAfterSpawn()
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
func (i *Instance) StartWithMessage(message string) (startErr error) {
	// Conductor with no explicit message: Start() records its own telemetry
	// start error, so return before installing the defer below or a failed
	// start would be recorded twice.
	if message == "" && i.IsConductor {
		return i.Start()
	}
	defer func() { i.recordTelemetryStartError(startErr) }()
	if i.leftQueue() {
		defer func() {
			if startErr == nil {
				i.takeOffQueueAfterStart()
			}
		}()
	}
	if err := i.ValidateAccount(); err != nil {
		return err
	}
	beforeLock := spawnGenerationSnapshot(i.ID)
	release, lockErr := acquireInstanceSpawnLock(i.ID)
	if lockErr != nil {
		return lockErr
	}
	defer release()
	if spawnedSince(i.ID, beforeLock) {
		return nil
	}
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
	var validatedDeepSeekCommand string
	if i.Tool == "deepseek" && message == "" {
		var err error
		validatedDeepSeekCommand, err = i.deepSeekStartCommand()
		if err != nil {
			return err
		}
	}

	// #1873: same fail-closed ownership gate as Start(), in the same position
	// relative to the spawn stamp. A second spawn path is still a second tree.
	if err := i.guardOwnedProcessesBeforeSpawn("start"); err != nil {
		return err
	}
	defer recordInstanceSpawn(i.ID)

	// #1580 diagnosability: clear any stale spawn-failure sidecar and drop a
	// spawn_attempt trace (same as Start()).
	i.recordSpawnAttempt()

	// Prepare scratch CLAUDE_CONFIG_DIR for ordinary non-conductor Claude
	// workers. An immutable cross-harness plan pins its own target home and
	// must never be replaced by a mutable scratch/config resolution.
	if i.crossHarnessLaunch == nil {
		i.prepareWorkerScratchConfigDirForSpawn() // also runs plugin auto-install per fix C1
	}

	// Start session normally (no embedded message logic)
	// Priority: built-in tools (claude, gemini, opencode, codex) → custom tools from config.toml → raw command
	var command string
	// Codex takes its initial prompt as a positional argument instead of having it
	// typed into the TUI; when that happens there is nothing left to send. The
	// DeepSeek headless profile is the same shape — `dsh --profile headless
	// "<task>"` IS the whole invocation — so the flag is tool-neutral.
	promptEmbeddedInCommand := false
	switch {
	case i.crossHarnessLaunch != nil:
		var launchErr error
		command, promptEmbeddedInCommand, launchErr = crossHarnessPlanCommand(i, i.crossHarnessLaunch, message)
		if launchErr != nil {
			return launchErr
		}
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
	case i.Tool == "omp":
		if i.IsForkAwaitingStart {
			command = i.consumeForkStartCommand()
			sessionLog.Info("resume: none reason=fork_awaiting_start",
				slog.String("instance_id", i.ID),
				slog.String("path", i.ProjectPath),
				slog.String("reason", "fork_awaiting_start"))
			break
		}
		command = i.buildOMPCommand(i.Command)
	case i.Tool == "copilot":
		command = i.buildCopilotCommand(i.Command)
	case i.Tool == "crush":
		command = i.buildCrushCommand(i.Command)
	case i.Tool == "muse":
		command = i.buildMuseCommand(i.Command)
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
		i.preferHermesOwnedSessionForStart()
		command = i.buildHermesCommand(i.Command)
	case i.Tool == "deepseek":
		// Only the headless profile takes a task positionally; an installed
		// interactive profile gets the prompt typed into its pane by the
		// post-start send path below. The web profile has no prompt at all and
		// was rejected above, before any spawn.
		if message == "" {
			// StartWithMessage with no message is an ordinary start, and
			// headless still needs its recorded task.
			command = validatedDeepSeekCommand
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

	diagnosticCommand := command
	if promptEmbeddedInCommand {
		diagnosticCommand = redactEmbeddedSpawnPrompt(command, message)
	}
	probeTool, probeMarker := i.spawnToolLookup(command)
	var containerName string
	var err error
	command, containerName, err = i.prepareCommand(command)
	if err != nil {
		// #1924: leave a reason behind. Without this the session sits on
		// StatusError with no tmux session and nothing to diagnose from.
		i.recordPrepareFailure(diagnosticCommand, err)
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
	i.tmuxSession.RunCommandAsInitialProcess = i.IsSandboxed() || i.Tool != "shell" || i.tracksCommandExit()
	i.applyLaunchSettingsFromConfig()

	// Re-assert the declarative skill+mcp loadout before spawn — sister
	// call to Start(); see ApplyConfiguredLoadout.
	ApplyConfiguredLoadout(i)

	i.preAcceptCursorWorkspaceTrust()

	// Start the tmux session
	command, err = i.wrapTrackedCommandExit(command)
	if err != nil {
		return err
	}
	i.retireFastDeathWatchers() // #2805
	i.setInteractiveShellLaunchEnv()
	if err := i.tmuxSession.Start(command); err != nil {
		// #1580: persist the tmux-level failure (sister path to Start()).
		i.recordTmuxStartFailure(diagnosticCommand, err)
		return fmt.Errorf("failed to start tmux session: %w", err)
	}

	// One generation for this spawn, shared by the ownership claim and the
	// fast-death watcher (sister path to Start()).
	gen, wake := i.newSpawnGenWatch()

	// #1873: claim the ownership receipt at spawn (sister path to Start()).
	// The receipt is an on-disk diagnostic like the spawn-failure sidecar, so
	// it records the prompt-redacted command (#2232), never the raw prompt.
	i.claimOwnershipAtSpawn(diagnosticCommand, gen, wake)

	// #1580: fast-death watcher (sister path to Start()).
	if command != "" && !i.expectsFastExit() {
		// See the matching comment in Start(): resolve the write targets — and
		// subscribe to the wake — here, not inside the never-joined goroutine.
		i.startFastDeathWatcher(diagnosticCommand, gen, wake, i.tmuxSession, i.ID, i.Tool, sessionLog, probeTool, probeMarker)
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
	i.ensureInteractiveShellEnv()
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

// updateStatus refreshes i.Status from tmux, hooks and the pane. A session
// still queued for group capacity keeps StatusQueued until its tmux pane
// exists; a missing pane is not an error for it (issue #2526).
func (i *Instance) updateStatus(pass *StatusUpdatePass, syncMetadata bool) error {
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
	var exists bool
	var checkedExists bool
	if i.Status == StatusQueued {
		// Waiting for group capacity is not a missing-pane error. Start may
		// leave an interactive shell queued until this pass sees its pane.
		if i.tmuxSession == nil {
			return nil
		}
		var current bool
		exists, _, current = i.probeTmuxExists(nil)
		if !current || !exists {
			return nil
		}
		checkedExists = true
	}
	if time.Since(graceTime) < 1500*time.Millisecond {
		// Only skip if tmux session doesn't exist yet
		if i.tmuxSession == nil {
			if i.Status != StatusRunning && i.Status != StatusIdle && i.Status != StatusStopped {
				i.Status = StatusStarting
			}
			return nil
		}
		var current bool
		exists, _, current = i.probeTmuxExists(nil)
		if !current {
			return nil
		}
		checkedExists = true
		if !exists {
			if i.Status != StatusRunning && i.Status != StatusIdle && i.Status != StatusStopped {
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
	// An error/stopped row whose session this tick's listing shows absent is
	// answered without forking has-session + list-panes. With ~130 ghosts those
	// two forks per row per 30s pushed whole status passes past the 250 ms
	// health budget. A recheck of a confirmed ghost stays dead as is; a first
	// sighting still runs the terminated-pane classification below.
	if (i.Status == StatusError || i.Status == StatusStopped) && i.tmuxSession.AbsentFromCache() {
		if !i.lastErrorCheck.IsZero() {
			i.lastErrorCheck = time.Now()
			return nil
		}
		exists, checkedExists = false, true
	}

	// Check if tmux session exists
	var listedAbsent bool
	if !checkedExists {
		var current bool
		exists, listedAbsent, current = i.probeTmuxExists(pass)
		if !current {
			return nil
		}
		if listedAbsent && !i.listingAloneProvesDead() {
			// A row last seen live: confirm with its own has-session before
			// a terminated status (the listing may predate a restart).
			exists, _, current = i.probeTmuxExists(nil)
			if !current {
				return nil
			}
			listedAbsent = !exists
		}
	}
	if !exists {
		if i.tmuxSession.AbsenceIsForeignServer() {
			// This process runs inside another tmux server and its socket-less
			// probe followed $TMUX there; the session is alive on the default
			// server. No verdict: keep the last-known status rather than publish
			// error for a session this process cannot see (rc 2026-09-23).
			return nil
		}
		if i.neverStarted() {
			// Added but never started: no tmux session was ever created, so an
			// absent tmux is expected — classify as idle, not error (✕ → ○).
			i.Status = StatusIdle
		} else {
			// tmux session is non-nil here, so the exit-status probe can block;
			// applyTerminatedPaneStatus drops i.mu for the query and keeps the
			// stopped-state guard on write. A session the pass listing proved
			// gone has no pane to ask.
			if listedAbsent {
				i.applyTerminatedPaneStatusOf(nil)
			} else {
				i.applyTerminatedPaneStatus()
			}
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

	// A cached positive tmux hit may outlive Kill(). Confirm a stopped
	// session directly before treating it as started again.
	if i.Status == StatusStopped {
		s := i.tmuxSession
		stopRevision := i.stopRevision
		i.mu.Unlock()
		live, err := s.ProbeExists()
		i.mu.Lock()
		if i.tmuxSession != s || i.Status != StatusStopped || i.stopRevision != stopRevision {
			return nil
		}
		if err != nil || !live {
			i.lastErrorCheck = time.Now()
			return nil
		}
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
	if i.hookStatus == "" && HookStatusTool(i.Tool) {
		if hs := readHookStatusFile(i.ID); hs != nil && !i.codexHookFromForeignThread(hs) {
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

	// Recheck cached evidence too: a rollout may acquire its subagent metadata
	// after the hook was first read, or an older reader may have cached a
	// foreign turn-end from a thread without a rollout.
	if IsCodexCompatible(i.Tool) && i.hookSessionID != "" &&
		(i.shouldRejectCodexSubagentRebind(i.hookSessionID) ||
			i.shouldRejectCodexUnbackedTurnEnd(i.hookSessionID, i.hookEvent)) {
		i.hookStatus, i.hookEvent, i.hookSessionID = "", "", ""
		i.hookLastUpdate = time.Time{}
	}

	// Issue #2853: the pane exists here, so a "dead" hook (SessionEnd) from
	// before the newest spawn began is the previous process's exit, not this
	// one's (HookDeadFromEarlierSpawn).
	// Read as fresh, it turned every start after a stop into error.
	if HookDeadFromEarlierSpawn(i.ID, i.hookStatus, i.hookLastUpdate) {
		i.hookStatus, i.hookEvent, i.hookSessionID = "", "", ""
		i.hookLastUpdate = time.Time{}
	}

	// HOOK FAST PATH: hook-based status for tools that emit lifecycle events.
	// Freshness is tool- and state-specific (e.g. Codex running vs waiting).
	// When this path is stale/missing, control naturally falls through to tmux
	// polling and tool-specific session sync (tmux env/process-files/disk).
	if HookStatusTool(i.Tool) && i.hookStatus != "" &&
		time.Since(i.hookLastUpdate) < hookFastPathFreshnessForTool(i.Tool, i.hookStatus) {
		i.hookLagFlipped = false
		i.bgWorkActive = false
		i.bgWork = tmux.BackgroundWork{}
		if i.hookStatus != "running" {
			// The hook moved on (Stop landed, or a new lifecycle event): any
			// lag observed under the old running event is over.
			i.resetHookLagLocked()
		}
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
			// Hook lag: the hook file can keep saying "running" while the
			// pane shows the turn finished (see hook_lag.go; cause unknown).
			// The pane is the newer evidence, but one frame never overrules a
			// fresh hook — two independent samples of a completed turn at an
			// idle prompt do. No capture here: the samples are the ones
			// GetStatus/GetSubstate already recorded (this process or, via
			// the persisted record, another one). A capture this same pass
			// makes afterwards (Instance.Substate) wins over the record: if
			// the pane is busy again, absorbCompletedTurnSample reverts the
			// flip in the same pass.
			//
			// A background shell is decided by the same transcript-based
			// hold as on the Stop hook path (shellHoldsTurn): a lost Stop
			// hook must not read waiting where a delivered one reads
			// running / background-work. The scan reads disk, so i.mu is
			// released around it, then re-checked for a concurrent Kill().
			if IsClaudeCompatible(i.Tool) && i.noteHookLagSampleLocked() {
				i.mu.Unlock()
				work, holds := i.shellHoldsTurn()
				i.mu.Lock()
				if i.Status == StatusStopped {
					return nil
				}
				if holds {
					i.bgWork = work
					i.bgWorkActive = true
				} else {
					i.Status = StatusWaiting
					i.hookLagFlipped = true
				}
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
				// ends, including the turn that launched a Workflow, background
				// agents or a run_in_background shell. Issue #2473: a running
				// workflow means a running session, so this "waiting" never
				// overrides a pane or transcript that proves background work in
				// flight (background_work.go has the merge rule; a shell counter
				// counts only while the transcript shows a fresh launch, A5); the
				// session stays running with substate background-work until the
				// work reports back. BackgroundWorkSince captures the pane (the
				// fast path has no captured content; a probe older than this
				// hook event is not reused) and the transcript scan reads disk,
				// so release i.mu around both like the GetStatus call below,
				// then re-check for a concurrent Kill().
				//
				// A menu or an error outranks the work: a frame showing an open
				// menu, an error banner or the model-unavailable no-op stays
				// waiting (the turn is blocked on the operator, or cannot
				// progress) while a workflow runs. A PermissionRequest /
				// Notification(permission_prompt|elicitation_dialog) hook stays
				// waiting unprobed only for blockingHookGrace, the moment before
				// the dialog is drawn; after that the frame decides, because a
				// dialog dismissed with Esc fires no further hook and the stale
				// event must not hold a running workflow at waiting.
				var work tmux.BackgroundWork
				var foregroundBusy bool
				if i.tmuxSession != nil && IsClaudeCompatible(i.Tool) &&
					!blockingHookInGrace(i.hookEvent, i.hookLastUpdate, time.Now()) {
					hookAt := i.hookLastUpdate
					i.mu.Unlock()
					if pane, blocked, busy := i.tmuxSession.BackgroundWorkSince(hookAt); !blocked {
						foregroundBusy = busy
						work = i.probeBackgroundWork(pane)
					}
					i.mu.Lock()
					if i.Status == StatusStopped {
						return nil
					}
				}
				switch {
				case foregroundBusy:
					// Stop can arrive before Claude removes its live spinner.
					// A fresh waiting hook cannot override that frame (#2502).
					i.Status = StatusRunning
					i.tmuxSession.ResetAcknowledged()
				case work.Running():
					i.Status = StatusRunning
					i.bgWorkActive = true
					// Output produced while the work ran is unseen: when it
					// ends the session is waiting, not idle.
					i.tmuxSession.ResetAcknowledged()
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
		i.applyHookSessionIDLocked()
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
		i.statusSampledLive = true
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
			i.statusSampledLive = true
			return nil
		case "waiting":
			if i.tmuxSession != nil && i.tmuxSession.IsAcknowledged() {
				i.Status = StatusIdle
			} else {
				i.Status = StatusWaiting
			}
			i.releaseAuthHoldIfHealthyLocked()
			i.statusSampledLive = true
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
	// For the running→waiting/error debounce only a status this process
	// settled itself counts as prior: a row loaded as "running" is a persisted
	// guess, not an observation, and holding on it is what kept hook-less codex
	// sessions green forever (statusSampledLive). A transient capture failure
	// below still keeps the persisted status for one sample: that hold cannot
	// stick, the next readable capture settles it.
	livePrevStatus := prevStatus
	if !i.statusSampledLive {
		livePrevStatus = ""
	}
	i.statusSampledLive = true

	// The pane itself decided this status; there is no hook verdict to lag
	// behind it. Drop any hook-lag evidence so the substate cannot keep
	// reporting a disagreement that no longer exists.
	i.resetHookLagLocked()

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
	if status != "inactive" {
		i.terminatedPaneSubstate = SubstateNone
		i.terminatedPaneExitCode = nil
	}
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

	// Issue #2473: merge the transcript into the pane verdict for background
	// work (background_work.go). A pane at the prompt with work in flight is
	// already "active" from tmux; the transcript can veto a stale workflow row
	// there, and can hold a waiting/idle pane running while the footer is
	// briefly not visible (redraw, resize). A shell counter never makes the
	// pane "active" (A5): the frame reads waiting/idle and only a fresh
	// background Bash launch in the transcript promotes it here.
	// A frame that shows an open menu or an error is never promoted: the menu
	// blocks the turn on the operator (#2185) and the error means no progress.
	i.bgWorkActive = false
	i.bgWork = tmux.BackgroundWork{}
	if IsClaudeCompatible(i.Tool) && (status == "active" || status == "waiting" || status == "idle") &&
		!backgroundWorkOutrankedBySubstate(i.tmuxSession) {
		pane := i.tmuxSession.CachedBackgroundWork()
		fromBackground := status != "active" ||
			(pane.InFlight() && (i.tmuxSession.CachedSubstate() == tmux.SubstateBackgroundWork || i.tmuxSession.CachedSubstate() == tmux.SubstateWatching))
		if fromBackground {
			i.mu.Unlock()
			work := i.probeBackgroundWork(pane)
			i.mu.Lock()
			if i.Status == StatusStopped {
				return nil
			}
			switch {
			case work.Running():
				if status != "active" {
					i.tmuxSession.ResetAcknowledged()
				}
				i.Status = StatusRunning
				i.bgWorkActive = true
			case status == "active":
				// The only thing keeping the pane green was a workflow row the
				// transcript proves finished.
				if i.tmuxSession.IsAcknowledged() {
					i.Status = StatusIdle
				} else {
					i.Status = StatusWaiting
				}
			}
		}
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
	// Skip debounce for shell and pi: shell has no hooks at all, and while pi
	// now has hooks (#2222) it is still CLI-single-sample in practice (no
	// long-lived watcher process keeps polling it between invocations), so
	// its tmux status is the ground truth with no hook fast-path to race
	// against. For every tool the hold also needs a running verdict THIS
	// process made (livePrevStatus is "" on a fresh load, see
	// statusSampledLive): a one-pass process (e.g. `agent-deck list --json`)
	// cannot take the confirming sample, so holding there is a guess it never
	// checks.
	bypassWaitingDebounce := i.shouldBypassCodexWaitingDebounce(i.Status)
	if shouldDebounceTmuxFlipForTool(i.Tool) && !bypassWaitingDebounce {
		if apply, nextPending, held := debounceFlipFromRunning(livePrevStatus, i.Status, status, i.hookStatus, i.tmuxFlipFromRunningPending); held {
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

	// Observed runtime is separate from the configured launch identity. A shell
	// session that later runs OpenCode in the foreground is observed as opencode
	// for status patterns and the list icon, but Restart still launches the
	// configured command. Writing the observation back onto Tool is what made
	// a screen-text guess sticky and then resumed the wrong agent.
	if detectedTool := i.tmuxSession.DetectTool(); detectedTool != "" {
		i.noteObservedRuntimeLocked(detectedTool)
	}

	// Update session metadata tracking only for active/waiting sessions.
	// This path can perform filesystem and tmux env reads while i.mu is held, so
	// rate-limit it to reduce intermittent render/key handling stalls under load.
	if syncMetadata && (i.Status == StatusRunning || i.Status == StatusWaiting) {
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
				i.updateCodexSessionForPass(nil, false, pass, true)
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

// PreviewPrimaryFull returns the managed agent window's scrollback rather than
// whichever auxiliary tmux window the operator most recently focused.
func (i *Instance) PreviewPrimaryFull() (string, error) {
	if i.tmuxSession == nil {
		return "", fmt.Errorf("tmux session not initialized")
	}
	return i.tmuxSession.CapturePrimaryFullHistory()
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

// Codex generation discovery expands backward by bytes rather than limiting
// the number of records emitted after a turn starts. Most starts resolve from
// the first small read; the maximum prevents an unresolved submission from
// causing an unbounded historical read.
const (
	codexTurnGenerationInitialScanBytes = int64(64 << 10)
	codexTurnGenerationScanMaxBytes     = int64(8 << 20)
)

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

// SyncTmuxDisplayName updates tmux-rendered UI that reflects the current title.
func (i *Instance) SyncTmuxDisplayName() {
	if tmuxSess := i.GetTmuxSession(); tmuxSess != nil && tmuxSess.Exists() {
		tmuxSess.DisplayName = i.Title
		tmuxSess.SetGroupPath(i.GroupPath)
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

func (i *Instance) GetOMPOptions() *OMPOptions {
	opts, err := UnmarshalOMPOptions(i.ToolOptionsJSON)
	if err != nil {
		return nil
	}
	return opts
}

func (i *Instance) SetOMPOptions(opts *OMPOptions) error {
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
