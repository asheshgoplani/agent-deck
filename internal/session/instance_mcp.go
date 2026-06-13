package session

import (
	"fmt"
	"path/filepath"
)

// ToolSupportsMCPManager reports whether the TUI/CLI MCP surfaces apply to this tool.
func ToolSupportsMCPManager(toolName string) bool {
	return IsClaudeCompatible(toolName) || toolName == "gemini" || toolName == "cursor" || toolName == "opencode"
}

// MCPLocalConfigPathForTool returns the project-local MCP config path for display and writes.
// Empty when the tool has no project-local MCP file (e.g. Gemini uses global settings only).
func MCPLocalConfigPathForTool(toolName, projectPath string) string {
	if projectPath == "" {
		return ""
	}
	switch {
	case IsClaudeCompatible(toolName):
		return filepath.Join(projectPath, ".mcp.json")
	case toolName == "cursor":
		return filepath.Join(projectPath, ".cursor", "mcp.json")
	case toolName == "opencode":
		return filepath.Join(projectPath, "opencode.json")
	default:
		return ""
	}
}

// MCPGlobalConfigPathForTool returns the global MCP config path for display and writes.
func MCPGlobalConfigPathForTool(toolName string) string {
	switch {
	case IsClaudeCompatible(toolName):
		return filepath.Join(GetClaudeConfigDir(), ".claude.json")
	case toolName == "gemini":
		return filepath.Join(GetGeminiConfigDir(), "settings.json")
	case toolName == "cursor":
		return filepath.Join(GetCursorConfigDir(), "mcp.json")
	case toolName == "opencode":
		return filepath.Join(GetOpenCodeConfigDir(), "opencode.json")
	default:
		return ""
	}
}

// MCPInfoForLocalAttach returns MCP info used for CLI/TUI local attach and detach.
// Gemini is special: global MCPs live in settings.json, but "local" CLI scope still
// targets the Claude-style .mcp.json walker in the project tree (legacy behavior).
func MCPInfoForLocalAttach(toolName, projectPath string) *MCPInfo {
	if toolName == "gemini" {
		return GetMCPInfo(projectPath)
	}
	switch {
	case toolName == "cursor":
		return GetCursorMCPInfo(projectPath)
	case toolName == "opencode":
		return GetOpenCodeMCPInfo(projectPath)
	case IsClaudeCompatible(toolName):
		return GetMCPInfo(projectPath)
	default:
		return &MCPInfo{}
	}
}

// WriteLocalMCPConfigForTool writes enabled catalog MCPs to the tool's project-local MCP file.
func WriteLocalMCPConfigForTool(toolName, projectPath string, names []string) error {
	switch {
	case toolName == "cursor":
		return WriteCursorProjectMCP(projectPath, names)
	case toolName == "opencode":
		return WriteOpenCodeProjectMCP(projectPath, names)
	case IsClaudeCompatible(toolName) || toolName == "gemini":
		return WriteMCPJsonFromConfig(projectPath, names)
	default:
		return fmt.Errorf("local MCP: unsupported tool %q", toolName)
	}
}

// WriteGlobalMCPConfigForTool writes enabled catalog MCPs to the tool's global MCP store.
func WriteGlobalMCPConfigForTool(toolName string, names []string) error {
	switch {
	case IsClaudeCompatible(toolName):
		return WriteGlobalMCP(names)
	case toolName == "gemini":
		return WriteGeminiMCPSettings(names)
	case toolName == "cursor":
		return WriteCursorGlobalMCP(names)
	case toolName == "opencode":
		return WriteOpenCodeGlobalMCP(names)
	default:
		return fmt.Errorf("global MCP: unsupported tool %q", toolName)
	}
}

// InvalidateProjectMCPIntegrationsCache clears session caches used by Claude-style, Cursor, and OpenCode MCP reads.
func InvalidateProjectMCPIntegrationsCache(projectPath string) {
	ClearMCPCache(projectPath)
	ClearCursorMCPCache(projectPath)
	ClearOpenCodeMCPCache(projectPath)
}

// MCPLocalConfigPath returns the project-local MCP file path for this instance's tool.
func (i *Instance) MCPLocalConfigPath() string {
	return MCPLocalConfigPathForTool(i.Tool, i.ProjectPath)
}

// MCPGlobalConfigPath returns the global MCP file path for this instance's tool.
func (i *Instance) MCPGlobalConfigPath() string {
	return MCPGlobalConfigPathForTool(i.Tool)
}

// MCPInfoForLocalAttach returns MCP info for local attach/detach for this instance.
func (i *Instance) MCPInfoForLocalAttach() *MCPInfo {
	return MCPInfoForLocalAttach(i.Tool, i.ProjectPath)
}

// WriteLocalMCPConfig writes catalog MCPs to this instance's project-local MCP file.
func (i *Instance) WriteLocalMCPConfig(names []string) error {
	return WriteLocalMCPConfigForTool(i.Tool, i.ProjectPath, names)
}

// WriteGlobalMCPConfig writes catalog MCPs to this instance's global MCP store.
func (i *Instance) WriteGlobalMCPConfig(names []string) error {
	return WriteGlobalMCPConfigForTool(i.Tool, names)
}

// InvalidateProjectMCPIntegrationsCache clears MCP read caches for this instance's project.
func (i *Instance) InvalidateProjectMCPIntegrationsCache() {
	InvalidateProjectMCPIntegrationsCache(i.ProjectPath)
}

// SupportsMCPAgentRestart is true when attach/detach --restart may reload the running agent.
func (i *Instance) SupportsMCPAgentRestart() bool {
	return ToolSupportsMCPManager(i.Tool)
}
