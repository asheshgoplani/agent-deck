package session

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
// gateway get session health detection without any manual configuration.
func GetHermesGatewayURL() string {
	config, err := LoadUserConfig()
	if err == nil && config != nil {
		if url := strings.TrimSpace(config.Hermes.GatewayURL); url != "" {
			return url
		}
	}
	return DiscoverHermesGatewayURL()
}

