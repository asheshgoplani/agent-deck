package mcppool

// ServerStatus represents MCP server state
type ServerStatus int

const (
	StatusStopped ServerStatus = iota
	StatusStarting
	StatusRunning
	StatusFailed
)

func (s ServerStatus) String() string {
	switch s {
	case StatusStopped:
		return "stopped"
	case StatusStarting:
		return "starting"
	case StatusRunning:
		return "running"
	case StatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}
