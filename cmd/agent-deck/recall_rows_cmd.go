package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// rowsTarget is a conversation resolved for the typed row model: an
// agent-deck session (read straight from its native transcript, no index
// and no [recall] gate) or an explicit --transcript file.
type rowsTarget struct {
	src     query.RowsSource
	session query.RowsSession
	inst    *session.Instance
	storage *session.Storage
}

type rowsFlags struct {
	rows       *bool
	transcript *string
	harness    *string
}

func registerRowsFlags(fs interface {
	Bool(string, bool, string) *bool
	String(string, string, string) *string
}) rowsFlags {
	return rowsFlags{
		rows:       fs.Bool("rows", false, "Typed row model v2 (user, assistant, thinking, bash, edit, read, subagent, todo, question, command, system, turn_end, ...) read directly from the native transcript; works without [recall] enabled"),
		transcript: fs.String("transcript", "", "With --rows: read this Claude Code JSONL or Codex rollout instead of a session's"),
		harness:    fs.String("harness", "", "With --transcript: claude or codex"),
	}
}

// rowsHarness maps a session tool to the native format the rows reader parses.
func rowsHarness(tool string) string {
	switch {
	case session.IsClaudeCompatible(tool):
		return "claude"
	case session.IsCodexCompatible(tool):
		return "codex"
	}
	return tool
}

// liveTranscriptPath resolves an instance's current native transcript.
func liveTranscriptPath(inst *session.Instance) (string, error) {
	switch rowsHarness(inst.Tool) {
	case "claude":
		if inst.ClaudeSessionID == "" {
			return "", fmt.Errorf("session %s has no Claude session id yet", inst.Title)
		}
		p := session.ClaudeTranscriptPathForInstance(inst)
		if p == "" {
			return "", fmt.Errorf("session %s: transcript is not on this machine", inst.Title)
		}
		return p, nil
	case "codex":
		p := session.CodexRolloutPathForInstance(inst)
		if p == "" {
			return "", fmt.Errorf("session %s has no Codex rollout yet", inst.Title)
		}
		return p, nil
	}
	return "", fmt.Errorf("%w: %s", query.ErrRowsUnsupported, inst.Tool)
}

func resolveRowsTarget(profile, ref string, f rowsFlags) (*rowsTarget, error) {
	if *f.transcript != "" {
		h := *f.harness
		if !query.SupportsDirectRows(h) {
			return nil, fmt.Errorf("--transcript needs --harness claude or codex")
		}
		return &rowsTarget{src: query.RowsSource{Harness: h, Path: *f.transcript}, session: query.RowsSession{Harness: h, Path: *f.transcript}}, nil
	}
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		return nil, err
	}
	inst, msg, _ := ResolveSession(ref, instances)
	if inst == nil {
		storage.Close()
		return nil, fmt.Errorf("%s", msg)
	}
	path, err := liveTranscriptPath(inst)
	if err != nil {
		storage.Close()
		return nil, err
	}
	h := rowsHarness(inst.Tool)
	t := &rowsTarget{inst: inst, storage: storage}
	t.src = query.RowsSource{Harness: h, Path: path, Resolve: func() (string, error) { return liveTranscriptPath(inst) }}
	t.session = query.RowsSession{ID: inst.ID, Title: inst.Title, Tool: inst.Tool, Harness: h, Path: path, NativeID: firstNonEmpty(inst.ClaudeSessionID, inst.CodexSessionID)}
	return t, nil
}

func (t *rowsTarget) close() {
	if t.storage != nil {
		t.storage.Close()
	}
}

// liveStatus samples the session's status row and, while it runs, parses
// a read-only pane capture into the synthetic status row.
func (t *rowsTarget) liveStatusFunc() func() *query.LiveStatus {
	if t.inst == nil || t.storage == nil {
		return nil
	}
	var last string
	var since time.Time
	return func() *query.LiveStatus {
		state := string(t.inst.Status)
		if db := t.storage.GetDB(); db != nil {
			if rows, err := db.ReadAllStatuses(); err == nil {
				if row, ok := rows[t.inst.ID]; ok && row.Status != "" {
					state = row.Status
				}
			}
		}
		if state != last {
			if last != "" {
				since = time.Now()
			}
			last = state
		}
		if state == "" {
			state = "unknown"
		}
		ls := &query.LiveStatus{State: state}
		if !since.IsZero() {
			ls.Since = since.UTC().Format(time.RFC3339)
		}
		if state == string(session.StatusRunning) {
			if ts := t.inst.GetTmuxSession(); ts != nil {
				if content, err := ts.CapturePane(); err == nil {
					ps := tmux.ParsePaneStatus(content)
					ls.Verb, ls.Elapsed, ls.Tokens, ls.CurrentTool = ps.Verb, ps.Elapsed, ps.Tokens, ps.CurrentTool
					ls.Queued, ls.Footer, ls.Mode, ls.Notice = ps.Queued, ps.Footer, ps.Mode, ps.Notice
				}
			}
		}
		return ls
	}
}

func handleRecallTimelineRows(profile string, ref string, f rowsFlags, tailBytes int64, agentID string) {
	out := NewCLIOutput(true, false)
	t, err := resolveRowsTarget(profile, ref, f)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}
	defer t.close()
	rows, cursor, err := query.ReadRows(context.Background(), t.src, query.RowsOptions{TailBytes: tailBytes, AgentID: agentID})
	if err != nil {
		code := ErrCodeInvalidOperation
		if errors.Is(err, os.ErrNotExist) {
			code = ErrCodeNotFound
		}
		out.Error(err.Error(), code)
		os.Exit(1)
	}
	result := query.RowsTimeline{Schema: query.RowsSchema, Session: t.session, Source: "native", Rows: rows, ThroughCursor: cursor}
	if fn := t.liveStatusFunc(); fn != nil {
		result.Status = fn()
	}
	out.printJSON(result)
}

func handleRecallFollowRows(profile, ref, after string, f rowsFlags) {
	t, err := resolveRowsTarget(profile, ref, f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "recall follow:", err)
		os.Exit(1)
	}
	defer t.close()
	ctx, cancel := interruptibleContext()
	defer cancel()
	if after == "end" {
		// Start at the current end: a client that paints from its own cache
		// only needs what lands from now on.
		_, cursor, err := query.ReadRows(ctx, t.src, query.RowsOptions{TailBytes: 1})
		if err != nil {
			fmt.Fprintln(os.Stderr, "recall follow:", err)
			os.Exit(1)
		}
		after = cursor
	}
	enc := json.NewEncoder(os.Stdout)
	err = query.FollowRows(ctx, t.src, after, 0, t.liveStatusFunc(), func(frame query.RowFrame) error {
		return enc.Encode(frame)
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "recall follow:", strings.TrimSpace(err.Error()))
		os.Exit(1)
	}
}
