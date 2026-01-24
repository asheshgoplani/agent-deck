package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// LaunchConfigInfo represents launch config data for the frontend.
type LaunchConfigInfo struct {
	Key           string   `json:"key"`
	Name          string   `json:"name"`
	Tool          string   `json:"tool"`
	Description   string   `json:"description"`
	DangerousMode bool     `json:"dangerousMode"`
	MCPConfigPath string   `json:"mcpConfigPath"`
	MCPNames      []string `json:"mcpNames,omitempty"`
	ExtraArgs     []string `json:"extraArgs"`
	IsDefault     bool     `json:"isDefault"`
}

// LaunchConfigManager handles launch configuration operations.
type LaunchConfigManager struct{}

// NewLaunchConfigManager creates a new LaunchConfigManager.
func NewLaunchConfigManager() *LaunchConfigManager {
	return &LaunchConfigManager{}
}

// GetLaunchConfigs returns all launch configurations.
func (m *LaunchConfigManager) GetLaunchConfigs() ([]LaunchConfigInfo, error) {
	configs := session.GetLaunchConfigs()
	result := make([]LaunchConfigInfo, 0, len(configs))

	for key, cfg := range configs {
		info := LaunchConfigInfo{
			Key:           key,
			Name:          cfg.Name,
			Tool:          cfg.Tool,
			Description:   cfg.Description,
			DangerousMode: cfg.DangerousMode,
			MCPConfigPath: cfg.MCPConfigPath,
			ExtraArgs:     cfg.ExtraArgs,
			IsDefault:     cfg.IsDefault,
		}

		// Try to parse MCP names from config file
		if cfg.MCPConfigPath != "" {
			names, err := cfg.ParseMCPNames()
			if err == nil {
				info.MCPNames = names
			}
		}

		result = append(result, info)
	}

	// Sort by tool, then by name for consistent ordering
	sort.Slice(result, func(i, j int) bool {
		if result[i].Tool != result[j].Tool {
			return result[i].Tool < result[j].Tool
		}
		return result[i].Name < result[j].Name
	})

	return result, nil
}

// GetLaunchConfigsForTool returns launch configs for a specific tool.
func (m *LaunchConfigManager) GetLaunchConfigsForTool(tool string) ([]LaunchConfigInfo, error) {
	all, err := m.GetLaunchConfigs()
	if err != nil {
		return nil, err
	}

	result := make([]LaunchConfigInfo, 0)
	for _, cfg := range all {
		if cfg.Tool == tool {
			result = append(result, cfg)
		}
	}

	return result, nil
}

// GetLaunchConfig returns a single launch config by key.
func (m *LaunchConfigManager) GetLaunchConfig(key string) (*LaunchConfigInfo, error) {
	cfg := session.GetLaunchConfigByKey(key)
	if cfg == nil {
		return nil, fmt.Errorf("launch config not found: %s", key)
	}

	info := &LaunchConfigInfo{
		Key:           key,
		Name:          cfg.Name,
		Tool:          cfg.Tool,
		Description:   cfg.Description,
		DangerousMode: cfg.DangerousMode,
		MCPConfigPath: cfg.MCPConfigPath,
		ExtraArgs:     cfg.ExtraArgs,
		IsDefault:     cfg.IsDefault,
	}

	// Try to parse MCP names
	if cfg.MCPConfigPath != "" {
		names, err := cfg.ParseMCPNames()
		if err == nil {
			info.MCPNames = names
		}
	}

	return info, nil
}

// SaveLaunchConfig creates or updates a launch configuration.
func (m *LaunchConfigManager) SaveLaunchConfig(
	key, name, tool, description string,
	dangerousMode bool,
	mcpConfigPath string,
	extraArgs []string,
	isDefault bool,
) error {
	// Validate required fields
	if key == "" {
		return fmt.Errorf("key is required")
	}
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if tool == "" {
		return fmt.Errorf("tool is required")
	}

	// Validate tool is known
	validTools := map[string]bool{"claude": true, "gemini": true, "opencode": true}
	if !validTools[tool] {
		return fmt.Errorf("invalid tool: %s (must be claude, gemini, or opencode)", tool)
	}

	// Validate extra args don't contain shell metacharacters (prevent command injection)
	for _, arg := range extraArgs {
		if strings.ContainsAny(arg, ";|&$`") {
			return fmt.Errorf("invalid character in extra argument: %q (shell metacharacters not allowed)", arg)
		}
	}

	// Load current config
	config, err := session.LoadUserConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Initialize map if nil
	if config.LaunchConfigs == nil {
		config.LaunchConfigs = make(map[string]session.LaunchConfig)
	}

	// If setting as default, clear other defaults for this tool
	if isDefault {
		for k, cfg := range config.LaunchConfigs {
			if cfg.Tool == tool && cfg.IsDefault {
				cfg.IsDefault = false
				config.LaunchConfigs[k] = cfg
			}
		}
	}

	// Create or update the config
	config.LaunchConfigs[key] = session.LaunchConfig{
		Name:          name,
		Tool:          tool,
		Description:   description,
		DangerousMode: dangerousMode,
		MCPConfigPath: mcpConfigPath,
		ExtraArgs:     extraArgs,
		IsDefault:     isDefault,
	}

	// Save config
	if err := session.SaveUserConfig(config); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	return nil
}

// DeleteLaunchConfig removes a launch configuration.
func (m *LaunchConfigManager) DeleteLaunchConfig(key string) error {
	// Load current config
	config, err := session.LoadUserConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if config.LaunchConfigs == nil {
		return fmt.Errorf("config not found: %s", key)
	}

	if _, exists := config.LaunchConfigs[key]; !exists {
		return fmt.Errorf("config not found: %s", key)
	}

	delete(config.LaunchConfigs, key)

	// Save config
	if err := session.SaveUserConfig(config); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	return nil
}

// ValidateMCPConfigPath validates an MCP config path and returns the MCP names.
func (m *LaunchConfigManager) ValidateMCPConfigPath(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}

	// Create a temporary config to use the parsing logic
	cfg := session.LaunchConfig{
		MCPConfigPath: path,
	}

	names, err := cfg.ParseMCPNames()
	if err != nil {
		return nil, err
	}

	return names, nil
}

// GenerateConfigKey generates a unique config key from tool and name.
func (m *LaunchConfigManager) GenerateConfigKey(tool, name string) string {
	// Normalize: lowercase, replace spaces with dashes
	safeName := strings.ToLower(name)
	safeName = strings.ReplaceAll(safeName, " ", "-")
	safeName = strings.ReplaceAll(safeName, "_", "-")

	return fmt.Sprintf("%s:%s", tool, safeName)
}

// GetDefaultLaunchConfig returns the default config for a tool, or nil if none set.
func (m *LaunchConfigManager) GetDefaultLaunchConfig(tool string) (*LaunchConfigInfo, error) {
	// Single iteration to find both key and config
	configs := session.GetLaunchConfigs()
	for key, cfg := range configs {
		if cfg.Tool == tool && cfg.IsDefault {
			info := &LaunchConfigInfo{
				Key:           key,
				Name:          cfg.Name,
				Tool:          cfg.Tool,
				Description:   cfg.Description,
				DangerousMode: cfg.DangerousMode,
				MCPConfigPath: cfg.MCPConfigPath,
				ExtraArgs:     cfg.ExtraArgs,
				IsDefault:     cfg.IsDefault,
			}

			// Try to parse MCP names
			if cfg.MCPConfigPath != "" {
				names, err := cfg.ParseMCPNames()
				if err == nil {
					info.MCPNames = names
				}
			}

			return info, nil
		}
	}

	return nil, nil
}
