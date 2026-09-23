package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// rowsTarget is a conversation resolved for the row model. direct targets
// are read from their native file; index-only targets of other harnesses
// come back as v1 turns mapped onto rows.
type rowsTarget struct {
	src     query.RowsSource
	session query.RowsSession
	direct  bool
	inst    *session.Instance
	storage *session.Storage
	env     *recallEnv
	native  string
}

type rowsFlags struct {
	v1         *bool
	transcript *string
	harness    *string
}

func registerRowsFlags(fs interface {
	Bool(string, bool, string) *bool
	String(string, string, string) *string
}) rowsFlags {
	return rowsFlags{
		v1:         fs.Bool("v1", false, "Slice-6 output (turns with role/kind/text, turn frames) instead of rows"),
		transcript: fs.String("transcript", "", "Read this Claude Code JSONL or Codex rollout instead of a session's"),
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
	case "claude", "codex":
		if p := session.LiveTranscriptPath(inst); p != "" {
			return p, nil
		}
		return "", fmt.Errorf("session %s has no native transcript on this machine yet", inst.Title)
	}
	return "", fmt.Errorf("%w: %s", query.ErrRowsUnsupported, inst.Tool)
}

var nativeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{7,}$`)

// nativeTranscriptByID finds a Claude or Codex conversation by its native id
// under every root recall knows (all Claude config dirs, Codex homes).
func nativeTranscriptByID(id string) (harness, path string) {
	if !nativeIDPattern.MatchString(id) {
		return "", ""
	}
	for _, root := range session.RecallRoots() {
		var pattern string
		switch root.Harness {
		case reader.HarnessClaude:
			pattern = filepath.Join(root.Dir, "projects", "*", id+".jsonl")
		case reader.HarnessCodex:
			pattern = filepath.Join(root.Dir, "sessions", "*", "*", "*", "rollout-*-"+id+".jsonl")
		default:
			continue
		}
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			return root.Harness, matches[len(matches)-1]
		}
	}
	return "", ""
}

func instanceTarget(inst *session.Instance, storage *session.Storage) (*rowsTarget, error) {
	path, err := liveTranscriptPath(inst)
	if err != nil {
		return nil, err
	}
	h := rowsHarness(inst.Tool)
	t := &rowsTarget{inst: inst, storage: storage, direct: true}
	t.src = query.RowsSource{Harness: h, Path: path, Resolve: func() (string, error) { return liveTranscriptPath(inst) }}
	t.session = query.RowsSession{ID: inst.ID, Harness: h, NativeID: firstNonEmpty(inst.ClaudeSessionID, inst.CodexSessionID), Path: path, Title: inst.Title, Cwd: inst.EffectiveWorkingDir()}
	return t, nil
}

// resolveRowsTarget resolves <session> without the index first: deck
// session id, then a conversation id bound to a deck session, then a deck
// title or id prefix, then a native conversation file on disk, and only
// then the index (#n, card ids, other harnesses).
func resolveRowsTarget(profile, ref string, f rowsFlags) (*rowsTarget, error) {
	if *f.transcript != "" {
		h := *f.harness
		if !query.SupportsDirectRows(h) {
			return nil, fmt.Errorf("--transcript needs --harness claude or codex")
		}
		return &rowsTarget{direct: true, src: query.RowsSource{Harness: h, Path: *f.transcript}, session: query.RowsSession{Harness: h, Path: *f.transcript}}, nil
	}
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		return nil, err
	}
	var inst *session.Instance
	for _, match := range []func(*session.Instance) bool{
		func(i *session.Instance) bool { return i.ID == ref },
		func(i *session.Instance) bool { return ref != "" && (i.ClaudeSessionID == ref || i.CodexSessionID == ref) },
	} {
		for _, i := range instances {
			if match(i) {
				inst = i
				break
			}
		}
		if inst != nil {
			break
		}
	}
	if inst == nil && !strings.HasPrefix(ref, "#") {
		inst, _, _ = ResolveSession(ref, instances)
	}
	if inst != nil {
		t, err := instanceTarget(inst, storage)
		if err == nil || !errors.Is(err, query.ErrRowsUnsupported) {
			if err != nil {
				storage.Close()
			}
			return t, err
		}
		// Another harness: the index knows its native source.
		ref = inst.ID
	}
	if h, path := nativeTranscriptByID(ref); path != "" {
		return &rowsTarget{direct: true, storage: storage, src: query.RowsSource{Harness: h, Path: path}, session: query.RowsSession{Harness: h, NativeID: ref, Path: path}}, nil
	}
	env := openRecallEnv(profile, NewCLIOutput(true, false))
	h, path, sess, err := query.New(env.st, env.stateDB).NativeSource(context.Background(), ref)
	if err != nil {
		env.close()
		storage.Close()
		if errors.Is(err, query.ErrNotFound) {
			return nil, fmt.Errorf("no session, conversation or indexed transcript matches %q", ref)
		}
		return nil, err
	}
	t := &rowsTarget{storage: storage, env: env, native: sess.NativeID, direct: query.SupportsDirectRows(h)}
	t.src = query.RowsSource{Harness: h, Path: path}
	t.session = query.RowsSession{ID: sess.DeckID, Harness: h, NativeID: sess.NativeID, Path: path, Title: sess.Title, Cwd: sess.CWD}
	if inst != nil {
		t.inst = inst
	}
	return t, nil
}

func (t *rowsTarget) close() {
	if t.storage != nil {
		t.storage.Close()
	}
	if t.env != nil {
		t.env.close()
	}
}

// liveStatusFunc samples the session's status row and, while it runs,
// parses a read-only pane capture into the status frame.
func (t *rowsTarget) liveStatusFunc() func() *query.LiveStatus {
	if t.inst == nil || t.storage == nil {
		return nil
	}
	return func() *query.LiveStatus {
		state := string(t.inst.Status)
		if db := t.storage.GetDB(); db != nil {
			if rows, err := db.ReadAllStatuses(); err == nil {
				if row, ok := rows[t.inst.ID]; ok && row.Status != "" {
					state = row.Status
				}
			}
		}
		ls := &query.LiveStatus{SessionID: t.inst.ID, SessionStatus: state, Running: state == string(session.StatusRunning)}
		if !ls.Running {
			return ls
		}
		ts := t.inst.GetTmuxSession()
		if ts == nil {
			return ls
		}
		content, err := ts.CapturePane()
		if err != nil {
			return ls
		}
		ps := tmux.ParsePaneStatus(content)
		ls.Verb, ls.ElapsedS, ls.Tokens, ls.CurrentTool = ps.Verb, tmux.ElapsedSeconds(ps.Elapsed), ps.Tokens, ps.CurrentTool
		ls.Permission, ls.AutoCompactPct, ls.Queued, ls.Notice = ps.Mode, ps.AutoCompactPct, ps.QueuedText, ps.Notice
		if facts := tmux.FooterFacts(ps.Footer); len(facts) > 0 {
			ls.Facts = facts
		}
		if ls.Queued == nil && ps.Queued > 0 {
			ls.Queued = make([]string, ps.Queued)
		}
		return ls
	}
}

func handleRecallTimelineRows(profile string, ref string, f rowsFlags, opts query.RowsOptions) {
	out := NewCLIOutput(true, false)
	t, err := resolveRowsTarget(profile, ref, f)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}
	defer t.close()
	var result query.RowsTimeline
	if t.direct {
		result, err = query.ReadRows(context.Background(), t.src, opts)
	} else {
		result.Turns, err = query.TimelineTurnsForSource(context.Background(), t.src.Harness, t.src.Path, t.native)
		if err == nil && opts.Tail > 0 && len(result.Turns) > opts.Tail {
			result.Turns = result.Turns[len(result.Turns)-opts.Tail:]
		}
	}
	if err != nil {
		code := ErrCodeInvalidOperation
		var re *query.ResyncError
		switch {
		case errors.Is(err, os.ErrNotExist):
			code = ErrCodeNotFound
		case errors.As(err, &re):
			code = "RESYNC_REQUIRED"
		}
		out.Error(err.Error(), code)
		os.Exit(1)
	}
	result.Schema, result.Session = query.RowsSchema, t.session
	result.Source = "native"
	if !t.direct {
		result.Source = "index"
	}
	if result.Turns == nil {
		result.Turns = []query.Row{}
	}
	if fn := t.liveStatusFunc(); fn != nil {
		result.Status = fn()
	}
	out.printJSON(result)
}

func handleRecallFollowRows(profile, ref, after string, f rowsFlags, withStatus bool) {
	t, err := resolveRowsTarget(profile, ref, f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "recall follow:", err)
		os.Exit(1)
	}
	defer t.close()
	if !t.direct {
		fmt.Fprintf(os.Stderr, "recall follow: %s transcripts are streamed with --v1\n", t.src.Harness)
		os.Exit(2)
	}
	ctx, cancel := interruptibleContext()
	defer cancel()
	if after == "end" {
		// Start at the current end: a client that painted from its own
		// cache or a --tail timeline only needs what lands from now on.
		tl, err := query.ReadRows(ctx, t.src, query.RowsOptions{Tail: 1})
		if err != nil {
			fmt.Fprintln(os.Stderr, "recall follow:", err)
			os.Exit(1)
		}
		after = tl.ThroughCursor
	}
	var status func() *query.LiveStatus
	if withStatus {
		status = t.liveStatusFunc()
	}
	enc := json.NewEncoder(os.Stdout)
	err = query.FollowRows(ctx, t.src, after, 0, status, func(frame query.RowFrame) error {
		return enc.Encode(frame)
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "recall follow:", strings.TrimSpace(err.Error()))
		os.Exit(1)
	}
}
