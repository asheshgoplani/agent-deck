package model

import "time"

type Session struct {
	ID         string
	Tool       string
	CWD        string
	Title      string
	LastPrompt string
	GitBranch  string
	FilePath   string
	ModTime    time.Time
	MsgCount   int
	Status     SessionStatus
	PID        int
	Name       string
	WaitingFor string // reason a waiting session needs input (e.g. "permission prompt")
}

func (s Session) Label() string {
	if s.Title == "" {
		return "(untitled)"
	}
	return s.Title
}

type SessionStatus int

const (
	StatusClosed SessionStatus = iota
	StatusRecent
	StatusRunningIdle
	StatusRunningBusy
	StatusWaiting
)

func (s SessionStatus) Glyph() string {
	switch s {
	case StatusWaiting:
		return "◆"
	case StatusRunningBusy:
		return "●"
	case StatusRunningIdle:
		return "◐"
	case StatusRecent:
		return "○"
	default:
		return "·"
	}
}

func (s SessionStatus) Label() string {
	switch s {
	case StatusWaiting:
		return "waiting"
	case StatusRunningBusy, StatusRunningIdle:
		return "running"
	case StatusRecent:
		return "recent"
	default:
		return "idle"
	}
}
