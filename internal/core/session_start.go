package core

import (
	"context"
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// SessionStartIn is the input of session.start.
type SessionStartIn struct {
	Profile string `json:"profile" doc:"Profile whose store holds the session"`
	Session string `json:"session" doc:"Session id, id prefix, title or path"`
	Message string `json:"message,omitempty" doc:"Initial message to send once the agent is ready"`
	Yolo    bool   `json:"yolo,omitempty" doc:"Enable YOLO mode for Gemini, Codex or Hermes sessions"`
	NoWait  bool   `json:"no_wait,omitempty" doc:"Return once the process is spawned instead of waiting for the tool's session id"`
}

// SessionStartOut is the output of session.start.
type SessionStartOut struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Status is "started", or "queued" when the group is at its concurrency cap.
	Status          string `json:"status" doc:"started or queued"`
	Group           string `json:"group" doc:"Group path of the session"`
	MaxConcurrent   int    `json:"max_concurrent,omitempty" doc:"Group cap that queued the session"`
	Tmux            string `json:"tmux,omitempty" doc:"tmux session name"`
	ClaudeSessionID string `json:"claude_session_id,omitempty"`
	Message         string `json:"message,omitempty" doc:"Initial message that was sent"`
	// Instance is the in-process handle of the started session (for a
	// surface that attaches to it). Never serialized.
	Instance *session.Instance `json:"-"`
}

// Session start statuses.
const (
	StartStatusStarted = "started"
	StartStatusQueued  = "queued"
)

func (deps Deps) sessionStart(ctx context.Context, in SessionStartIn) (SessionStartOut, error) {
	d, err := loadSessionData(in.Profile)
	if err != nil {
		return SessionStartOut{}, err
	}
	inst, err := deps.resolve(in.Session, d.instances)
	if err != nil {
		return SessionStartOut{}, err
	}
	if inst.Exists() {
		return SessionStartOut{}, Errorf(CodeAlreadyRunning, "session '%s' is already running", inst.Title)
	}
	if deps.ApplyYolo != nil {
		if err := deps.ApplyYolo(inst, in.Yolo); err != nil {
			return SessionStartOut{}, &Error{Code: CodeInvalid, Message: err.Error(), Cause: err}
		}
	}

	out := SessionStartOut{ID: inst.ID, Title: inst.Title, Group: inst.GroupPath, Instance: inst}

	// v1.9.1 group concurrency cap: a group at max_concurrent queues the
	// session instead of starting it; session.stop drains the queue.
	tree := session.NewGroupTreeWithGroups(d.instances, d.groups)
	max := session.GroupMaxConcurrent(tree, inst.GroupPath)
	if session.ShouldQueue(d.instances, inst.GroupPath, max) {
		inst.Status = session.StatusQueued
		if err := d.saveOr("failed to save queued state"); err != nil {
			return SessionStartOut{}, err
		}
		out.Status = StartStatusQueued
		out.MaxConcurrent = max
		return out, nil
	}

	if in.Message != "" {
		err = inst.StartWithMessage(in.Message)
	} else {
		err = inst.Start()
	}
	if err != nil {
		return SessionStartOut{}, &Error{Code: CodeInvalid, Message: fmt.Sprintf("failed to start session: %v", err), Cause: err}
	}
	// #2099: nil from Start only means tmux accepted the spawn.
	if err := inst.VerifySpawned(SpawnVerifyWait); err != nil {
		return SessionStartOut{}, d.failSpawn("start", inst, err)
	}
	if !in.NoWait {
		inst.PostStartSync(PostStartSyncWait)
	}
	if err := d.saveOr("failed to save session state"); err != nil {
		return SessionStartOut{}, err
	}

	out.Status = StartStatusStarted
	if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
		out.Tmux = tmuxSess.Name
	}
	out.ClaudeSessionID = inst.ClaudeSessionID
	out.Message = in.Message
	return out, nil
}
