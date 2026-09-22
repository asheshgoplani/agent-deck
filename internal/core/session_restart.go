package core

import (
	"context"
	"fmt"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// SessionRestartIn is the input of session.restart.
type SessionRestartIn struct {
	Profile string            `json:"profile" doc:"Profile whose store holds the session"`
	Session string            `json:"session,omitempty" doc:"Session id, id prefix, title or path; empty with all"`
	All     bool              `json:"all,omitempty" doc:"Restart every active session"`
	Force   bool              `json:"force,omitempty" doc:"Restart even if the session is healthy and fresh"`
	Env     map[string]string `json:"env,omitempty" doc:"Environment variables for the restarted process"`
}

// SessionRestartOut is the output of session.restart. A single restart fills
// the top-level fields; restart --all fills All.
type SessionRestartOut struct {
	ID      string         `json:"id,omitempty"`
	Title   string         `json:"title,omitempty"`
	Skipped bool           `json:"skipped,omitempty" doc:"The freshness or auth guard skipped the restart"`
	Reason  string         `json:"reason,omitempty" doc:"Why the restart was skipped"`
	Warning string         `json:"warning,omitempty"`
	All     *RestartAllOut `json:"all,omitempty"`
}

// RestartAllOut summarises restart --all.
type RestartAllOut struct {
	Total       int          `json:"total"`
	Restarted   int          `json:"restarted"`
	Failed      int          `json:"failed"`
	SkippedAuth int          `json:"skipped_auth"`
	AuthDeaths  int          `json:"auth_deaths"`
	Abandoned   int          `json:"abandoned"`
	AuthTripped bool         `json:"auth_tripped"`
	TripMessage string       `json:"trip_message"`
	Sessions    []RestartRow `json:"sessions"`
}

// OK reports whether the sweep succeeded (no failures, circuit not tripped).
func (r *RestartAllOut) OK() bool { return r.Failed == 0 && !r.AuthTripped }

// RestartRow is one session of restart --all, in sweep order.
type RestartRow struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Success is nil for sessions the sweep never reached (abandoned).
	Success      *bool                       `json:"success,omitempty"`
	Error        string                      `json:"error,omitempty"`
	SpawnFailure *session.SpawnFailureRecord `json:"spawn_failure,omitempty"`
	Warning      string                      `json:"warning,omitempty"`
	Skipped      bool                        `json:"skipped,omitempty"`
	Reason       string                      `json:"reason,omitempty"`
	AuthDeath    bool                        `json:"auth_death,omitempty"`
}

func (deps Deps) sessionRestart(ctx context.Context, in SessionRestartIn) (SessionRestartOut, error) {
	d, err := loadSessionData(in.Profile)
	if err != nil {
		return SessionRestartOut{}, err
	}
	if in.All {
		all, err := d.restartAll(ctx, in)
		if err != nil {
			return SessionRestartOut{}, err
		}
		return SessionRestartOut{All: all}, nil
	}
	if in.Session == "" {
		return SessionRestartOut{}, Errorf(CodeMissingArg, "session identifier required (or use --all)")
	}
	inst, err := deps.resolve(in.Session, d.instances)
	if err != nil {
		return SessionRestartOut{}, err
	}
	out := SessionRestartOut{ID: inst.ID, Title: inst.Title}

	// Issue #30 freshness guard: keep a healthy, just-started scope intact.
	if skip, reason := session.ShouldSkipRestart(inst, time.Now(), in.Force || len(in.Env) > 0); skip {
		out.Skipped = true
		out.Reason = reason
		return out, nil
	}

	if err := inst.RestartWithEnv(in.Env); err != nil {
		return SessionRestartOut{}, &Error{Code: CodeInvalid, Message: fmt.Sprintf("failed to restart session: %v", err), Cause: err}
	}
	if err := inst.VerifySpawned(SpawnVerifyWait); err != nil {
		return SessionRestartOut{}, d.failSpawn("restart", inst, err)
	}
	inst.LastStartedAt = time.Now()
	if warning := inst.ConsumeCodexRestartWarning(); warning != "" {
		out.Warning = warning
		Warn(ctx, warning)
		Emit(ctx, Event{Kind: EventRestartWarning, ID: inst.ID, Title: inst.Title, Message: warning})
	}
	if session.IsClaudeCompatible(inst.Tool) && inst.ClaudeSessionID == "" {
		inst.PostStartSync(PostStartSyncWait)
	}
	if err := d.saveOr("failed to save session state"); err != nil {
		return SessionRestartOut{}, err
	}
	AfterFunc(ctx, func() {
		session.RecordSessionEvent(in.Profile, inst.ID, health.KindRestart, nil)
	})
	return out, nil
}

// restartAll restarts every active session through session.BootSweep, which
// paces boots, skips auth-held sessions and trips on repeated auth deaths.
func (d *sessionData) restartAll(ctx context.Context, in SessionRestartIn) (*RestartAllOut, error) {
	var active []*session.Instance
	for _, inst := range d.instances {
		if inst.Exists() {
			active = append(active, inst)
		}
	}
	if len(active) == 0 {
		return nil, Errorf(CodeNoActive, "no active sessions to restart")
	}

	rows := make(map[string]*RestartRow, len(active))
	restarted := make([]string, 0, len(active))
	sweep := session.NewBootSweep().Run(active, func(inst *session.Instance) error {
		row := &RestartRow{ID: inst.ID, Title: inst.Title}
		rows[inst.ID] = row
		fail := func(msg string, err error) error {
			row.Success = boolPtr(false)
			row.Error = msg
			Emit(ctx, Event{Kind: EventRestartFailed, ID: inst.ID, Title: inst.Title, Message: msg, Err: err})
			return err
		}

		Emit(ctx, Event{Kind: EventRestartBegin, ID: inst.ID, Title: inst.Title})
		if err := inst.RestartWithEnv(in.Env); err != nil {
			return fail(fmt.Sprintf("failed to restart session '%s': %v", inst.Title, err), err)
		}
		// #2099: a restart whose pane is already gone is a failure.
		if err := inst.VerifySpawned(SpawnVerifyWait); err != nil {
			row.SpawnFailure = newSpawnFailure("restart", inst, err).Record
			return fail(spawnFailureMessage("restart", err), err)
		}
		inst.LastStartedAt = time.Now()
		restarted = append(restarted, inst.ID)

		warning := inst.ConsumeCodexRestartWarning()
		if warning != "" {
			Warn(ctx, warning)
			Emit(ctx, Event{Kind: EventRestartWarning, ID: inst.ID, Title: inst.Title, Message: warning})
		}
		if session.IsClaudeCompatible(inst.Tool) && inst.ClaudeSessionID == "" {
			inst.PostStartSync(PostStartSyncWait)
		}
		row.Success = boolPtr(true)
		row.Warning = warning
		Emit(ctx, Event{Kind: EventRestartDone, ID: inst.ID, Title: inst.Title})
		return nil
	})

	out := &RestartAllOut{
		Total:       len(active),
		Restarted:   sweep.Booted,
		Failed:      sweep.Failed,
		SkippedAuth: sweep.SkippedHeld,
		AuthDeaths:  sweep.AuthDeaths,
		Abandoned:   sweep.Abandoned,
		AuthTripped: sweep.Tripped,
		TripMessage: sweep.TripMessage,
		Sessions:    make([]RestartRow, 0, len(sweep.Attempts)),
	}
	for _, attempt := range sweep.Attempts {
		row := rows[attempt.InstanceID]
		if row == nil {
			row = &RestartRow{ID: attempt.InstanceID, Title: attempt.Title}
		}
		if attempt.Skipped {
			row.Success = boolPtr(true)
			row.Skipped = true
			row.Reason = attempt.SkipReason
		}
		if attempt.AuthDeath {
			row.AuthDeath = true
		}
		out.Sessions = append(out.Sessions, *row)
	}
	for _, attempt := range sweep.Attempts {
		if attempt.Skipped {
			Emit(ctx, Event{Kind: EventRestartSkipped, ID: attempt.InstanceID, Title: attempt.Title, Message: attempt.SkipReason})
		}
	}

	if err := d.saveOr("failed to save session state"); err != nil {
		return nil, err
	}
	AfterFunc(ctx, func() {
		for _, id := range restarted {
			session.RecordSessionEvent(in.Profile, id, health.KindRestart, nil)
		}
	})
	return out, nil
}

func boolPtr(b bool) *bool { return &b }
