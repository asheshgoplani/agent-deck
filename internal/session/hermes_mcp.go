package session

// Hermes MCP support
// Hermes primarily uses project-level .mcp.json (same as Claude).
// These functions provide the interface expected by mcp_dialog.go.

func GetHermesMCPInfo(projectPath string) *MCPInfo {
	// Hermes uses the same .mcp.json format as Claude
	return GetMCPInfo(projectPath)
}

func WriteHermesMCPSettings(enabledNames []string) error {
	// Delegate cleanly to the shared .mcp.json logic (same as Claude)
	return WriteMCPJsonFromConfig("", enabledNames)
}
