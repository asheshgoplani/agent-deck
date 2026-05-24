package session

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// HermesOptions holds launch options for Hermes Agent CLI sessions.
// Binary: `hermes` from github.com/NousResearch/hermes-agent (MIT, v0.13.0+).
// Status detection: process-alive/dead only (content-sniffing deferred).
// NOTE: CLI --yolo override (via applyCLIYoloOverride) is deferred until
// HermesOptions is wired into the launch command builder.
type HermesOptions struct {
	// YoloMode enables --yolo flag (auto-approve all tool calls).
	// nil = inherit from config, true/false = explicit override.
	YoloMode *bool `json:"yolo_mode,omitempty"`
}

// ToolName returns "hermes"
func (o *HermesOptions) ToolName() string {
	return "hermes"
}

// ToArgs returns command-line arguments based on options.
func (o *HermesOptions) ToArgs() []string {
	var args []string
	if o.YoloMode != nil && *o.YoloMode {
		args = append(args, "--yolo")
	}
	return args
}

// NewHermesOptions creates HermesOptions with defaults from config.
func NewHermesOptions(config *UserConfig) *HermesOptions {
	opts := &HermesOptions{}
	if config != nil && config.Hermes.YoloMode {
		yolo := true
		opts.YoloMode = &yolo
	}
	return opts
}

// UnmarshalHermesOptions deserializes HermesOptions from JSON wrapper.
func UnmarshalHermesOptions(data json.RawMessage) (*HermesOptions, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var wrapper ToolOptionsWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}

	if wrapper.Tool != "hermes" {
		return nil, nil
	}

	var opts HermesOptions
	if err := json.Unmarshal(wrapper.Options, &opts); err != nil {
		return nil, err
	}

	return &opts, nil
}

// buildHermesCommand builds the launch command for Hermes Agent CLI.
// Applies env sourcing, command override, and --yolo flag.
// If baseCommand differs from the bare tool name "hermes", it is treated as a
// user-supplied passthrough command and returned without flag injection.
func (i *Instance) buildHermesCommand(baseCommand string) string {
	if i.Tool != "hermes" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	// Passthrough: custom command from CLI (not the bare name)
	if baseCommand != "hermes" && baseCommand != "" {
		return envPrefix + baseCommand
	}

	cmd := GetToolCommand("hermes")

	// Apply flags from ToolOptionsJSON (includes --yolo if set at session creation)
	if len(i.ToolOptionsJSON) > 0 {
		opts, err := UnmarshalHermesOptions(i.ToolOptionsJSON)
		if err == nil && opts != nil {
			args := opts.ToArgs()
			if len(args) > 0 {
				cmd += " " + strings.Join(args, " ")
			}
		}
	} else {
		// No per-session options — fall back to global config for --yolo
		config, _ := LoadUserConfig()
		if config != nil && config.Hermes.YoloMode {
			cmd += " --yolo"
		}
	}

	// Inject HERMES_KANBAN_BOARD so the spawned session gets kanban_* tools
	// automatically. Only injected when the DB exists to avoid polluting the
	// env for users who haven't set up Kanban.
	kanbanDB := filepath.Join(GetHermesConfigDir(), "kanban.db")
	if _, err := os.Stat(kanbanDB); err == nil {
		cmd = "HERMES_KANBAN_BOARD=default " + cmd
	}

	// Inject HERMES_KANBAN_TASK when this session is linked to a specific task.
	// This causes Hermes to inject kanban_show/complete/block/heartbeat as tools.
	if i.KanbanTaskID != "" {
		cmd = "HERMES_KANBAN_TASK=" + shellQuote(i.KanbanTaskID) + " " + cmd
	}

	return envPrefix + cmd
}

// IsHermesGatewayReachable performs a basic reachable check against the
// configured GatewayURL from HermesSettings. Returns true if a simple
// HTTP request succeeds within timeout. Keeps existing process-alive logic
// untouched; this augments status detection when gateway URL is available.
func IsHermesGatewayReachable(gatewayURL string) bool {
	if gatewayURL == "" {
		return false
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(gatewayURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
}

// HermesSharedWorkspaceDir returns the base directory Hermes uses for
// shared workspace sessions enabling multi-agent handoff visibility.
// If the user config specifies a WorkspaceDir, that is used; otherwise
// it falls back to a platform-appropriate temp directory.
func HermesSharedWorkspaceDir() string {
	if config, _ := LoadUserConfig(); config != nil && config.Hermes.WorkspaceDir != "" {
		return config.Hermes.WorkspaceDir
	}
	return filepath.Join(os.TempDir(), "hermes-workspaces")
}

// globalKanbanWatcher is the singleton KanbanWatcher started when a Hermes gateway is detected.
var (
	globalKanbanWatcher *KanbanWatcher
	kanbanWatcherMu     sync.Mutex
)

// StartKanbanWatcher starts the WebSocket-based Kanban watcher if not already running.
// Subsequent calls return the existing watcher. Safe for concurrent use.
func StartKanbanWatcher(gatewayURL string) *KanbanWatcher {
	kanbanWatcherMu.Lock()
	defer kanbanWatcherMu.Unlock()
	if globalKanbanWatcher != nil {
		return globalKanbanWatcher
	}
	w := NewKanbanWatcher(gatewayURL)
	w.Start()
	globalKanbanWatcher = w
	return w
}

// hermesDefaultGatewayPort is the port hermes gateway always listens on.
// See gateway/platforms/api_server.py: DEFAULT_PORT = 8642.
const hermesDefaultGatewayPort = 8642

// hermesGatewayStateFile is the JSON file hermes writes while its gateway is running.
const hermesGatewayStateFile = "gateway_state.json"

// hermesGatewayState is a minimal subset of gateway_state.json.
type hermesGatewayState struct {
	GatewayState string `json:"gateway_state"`
}

// isHermesGatewayRunning checks ~/.hermes/gateway_state.json to see if
// the hermes gateway process believes it is running. This is a lightweight
// signal that avoids a network round-trip; callers should still probe the
// URL before trusting the result.
func isHermesGatewayRunning() bool {
	p := filepath.Join(GetHermesConfigDir(), hermesGatewayStateFile)
	data, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	var state hermesGatewayState
	if err := json.Unmarshal(data, &state); err != nil {
		return false
	}
	return state.GatewayState == "running"
}

// DiscoverHermesGatewayURL auto-detects the hermes gateway URL.
// It checks gateway_state.json first (cheap), then probes the well-known
// local address. Returns "" if the gateway does not appear to be reachable.
func DiscoverHermesGatewayURL() string {
	if !isHermesGatewayRunning() {
		return ""
	}
	candidate := fmt.Sprintf("http://127.0.0.1:%d", hermesDefaultGatewayPort)
	if IsHermesGatewayReachable(candidate) {
		return candidate
	}
	return ""
}

// GetHermesGatewayURL returns the hermes gateway URL. It first checks the
// explicit gateway_url in agent-deck's config; if unset, it attempts
// auto-discovery via DiscoverHermesGatewayURL so users who run the hermes
// gateway get real-time kanban updates without any manual configuration.
func GetHermesGatewayURL() string {
	config, err := LoadUserConfig()
	if err == nil && config != nil {
		if url := strings.TrimSpace(config.Hermes.GatewayURL); url != "" {
			return url
		}
	}
	return DiscoverHermesGatewayURL()
}

// kanbanCache holds the last-fetched Kanban task counts with stale-while-revalidate
// semantics: callers always get the cached value instantly; a background goroutine
// refreshes when the cache is older than kanbanCacheTTL.
// Used as fallback when no gateway URL is configured.
var kanbanCache struct {
	mu           sync.Mutex
	running      int
	blocked      int
	taskStatuses map[string]string
	fetchedAt    time.Time
	refreshing   bool
}

const kanbanCacheTTL = 15 * time.Second

// GetHermesKanbanCounts returns the current running and blocked task counts.
// Prefers the WebSocket KanbanWatcher (real-time) when available and healthy;
// falls back to stale-while-revalidate CLI polling otherwise.
// Returns (0, 0) if hermes is not in PATH or the CLI call fails.
func GetHermesKanbanCounts() (running, blocked int) {
	kanbanWatcherMu.Lock()
	w := globalKanbanWatcher
	kanbanWatcherMu.Unlock()
	if w != nil && w.IsHealthy() {
		return w.Counts()
	}

	// Fallback: stale-while-revalidate polling via hermes CLI
	kanbanCache.mu.Lock()
	if time.Since(kanbanCache.fetchedAt) < kanbanCacheTTL {
		r, b := kanbanCache.running, kanbanCache.blocked
		kanbanCache.mu.Unlock()
		return r, b
	}
	// Stale: return current cached values and kick off a background refresh.
	r, b := kanbanCache.running, kanbanCache.blocked
	if !kanbanCache.refreshing {
		kanbanCache.refreshing = true
		go func() {
			refreshHermesKanbanCache()
			kanbanCache.mu.Lock()
			kanbanCache.refreshing = false
			kanbanCache.mu.Unlock()
		}()
	}
	kanbanCache.mu.Unlock()
	return r, b
}

func refreshHermesKanbanCache() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// hermes --status only accepts a single value, so fetch all and filter.
	out, err := exec.CommandContext(ctx, "hermes", "kanban", "list", "--json").Output()
	if err != nil {
		return
	}
	var tasks []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &tasks); err != nil {
		return
	}
	var r, b int
	statuses := make(map[string]string)
	for _, t := range tasks {
		switch t.Status {
		case "running", "claimed":
			r++
			if t.ID != "" {
				statuses[t.ID] = "running"
			}
		case "blocked":
			b++
			if t.ID != "" {
				statuses[t.ID] = "blocked"
			}
		}
	}
	kanbanCache.mu.Lock()
	kanbanCache.running = r
	kanbanCache.blocked = b
	kanbanCache.taskStatuses = statuses
	kanbanCache.fetchedAt = time.Now()
	kanbanCache.mu.Unlock()
}

// ForceRefreshHermesKanbanCache refreshes the CLI-based kanban cache immediately,
// ignoring the TTL. Safe to call from a Bubble Tea Cmd goroutine.
func ForceRefreshHermesKanbanCache() {
	refreshHermesKanbanCache()
}

// GetHermesKanbanTaskStatus returns the current kanban status for a specific task ID.
// Returns "running", "blocked", or "" (task not active / not found).
// Prefers the WebSocket KanbanWatcher when healthy; falls back to the
// stale-while-revalidate CLI cache otherwise (including when the watcher is
// running but the kanban WebSocket endpoint is unavailable in this gateway version).
func GetHermesKanbanTaskStatus(taskID string) string {
	if taskID == "" {
		return ""
	}
	kanbanWatcherMu.Lock()
	w := globalKanbanWatcher
	kanbanWatcherMu.Unlock()
	if w != nil && w.IsHealthy() {
		return w.TaskStatus(taskID)
	}
	kanbanCache.mu.Lock()
	defer kanbanCache.mu.Unlock()
	// Kick off a background refresh if the cache is stale — same pattern as
	// GetHermesKanbanCounts. Without this the per-task badge never populates
	// when GetHermesKanbanCounts isn't called elsewhere.
	if time.Since(kanbanCache.fetchedAt) >= kanbanCacheTTL && !kanbanCache.refreshing {
		kanbanCache.refreshing = true
		go func() {
			refreshHermesKanbanCache()
			kanbanCache.mu.Lock()
			kanbanCache.refreshing = false
			kanbanCache.mu.Unlock()
		}()
	}
	if kanbanCache.taskStatuses == nil {
		return ""
	}
	return kanbanCache.taskStatuses[taskID]
}
