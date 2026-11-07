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
	owner   *rowsLookup
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
func liveTranscriptPath(inst *session.Instance, peers []*session.Instance) (string, error) {
	switch rowsHarness(inst.Tool) {
	case "claude", "codex":
		if p := session.LiveTranscriptPath(inst, peers); p != "" {
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

func instanceTarget(inst *session.Instance, peers []*session.Instance, storage *session.Storage) (*rowsTarget, error) {
	path, err := liveTranscriptPath(inst, peers)
	if err != nil {
		return nil, err
	}
	h := rowsHarness(inst.Tool)
	t := &rowsTarget{inst: inst, storage: storage, direct: true}
	t.src = query.RowsSource{Harness: h, Path: path, Resolve: func() (string, error) { return liveTranscriptPath(inst, peers) }}
	t.session = query.RowsSession{ID: inst.ID, Harness: h, NativeID: firstNonEmpty(inst.ClaudeSessionID, inst.CodexSessionID), Path: path, Title: inst.Title, Cwd: inst.EffectiveWorkingDir()}
	return t, nil
}

// resolveRowsTarget resolves <session> without the index first: deck
// session id, then a conversation id bound to a deck session, then a deck
// title or id prefix, then a native conversation file on disk, and only
// then the index (#n, card ids, other harnesses). The target owns what it
// opened; close releases it.
func resolveRowsTarget(profile, ref string, f rowsFlags) (*rowsTarget, error) {
	if *f.transcript != "" {
		h := *f.harness
		if !query.SupportsDirectRows(h) {
			return nil, fmt.Errorf("--transcript needs --harness claude or codex")
		}
		return &rowsTarget{direct: true, src: query.RowsSource{Harness: h, Path: *f.transcript}, session: query.RowsSession{Harness: h, Path: *f.transcript}}, nil
	}
	lk, err := newRowsLookup(profile)
	if err != nil {
		return nil, err
	}
	t, err := lk.resolve(ref)
	if err != nil {
		lk.close()
		return nil, err
	}
	t.owner = lk
	return t, nil
}

// rowsLookup holds what resolving a <session> needs: the deck's sessions,
// loaded once, and the recall index, opened at most once and only when a
// ref falls through to it. A batch resolves every id through one lookup.
type rowsLookup struct {
	profile   string
	storage   *session.Storage
	instances []*session.Instance
	env       *recallEnv
	// envErr is the index open failure a batch lookup reports on every id
	// that falls through to the index; a single-id lookup exits instead.
	envErr  error
	inBatch bool
}

// errRecallIndexUnavailable marks a batch entry whose id needed the recall
// index when the index could not be opened.
var errRecallIndexUnavailable = errors.New("recall index unavailable")

func newRowsLookup(profile string) (*rowsLookup, error) {
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		return nil, err
	}
	return &rowsLookup{profile: profile, storage: storage, instances: instances}, nil
}

func (lk *rowsLookup) close() {
	if lk.storage != nil {
		lk.storage.Close()
	}
	if lk.env != nil {
		lk.env.close()
	}
}

func (lk *rowsLookup) resolve(ref string) (*rowsTarget, error) {
	instances, storage := lk.instances, lk.storage
	var inst *session.Instance
	for _, match := range []func(*session.Instance) bool{
		func(i *session.Instance) bool { return i.ID == ref },
		func(i *session.Instance) bool {
			return ref != "" && (i.ClaudeSessionID == ref || i.CodexSessionID == ref)
		},
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
		t, err := instanceTarget(inst, instances, storage)
		if err == nil || !errors.Is(err, query.ErrRowsUnsupported) {
			return t, err
		}
		// Another harness: the index knows its native source.
		ref = inst.ID
	}
	if h, path := nativeTranscriptByID(ref); path != "" {
		return &rowsTarget{direct: true, storage: storage, src: query.RowsSource{Harness: h, Path: path}, session: query.RowsSession{Harness: h, NativeID: ref, Path: path}}, nil
	}
	if lk.env == nil && lk.envErr == nil {
		if !lk.inBatch {
			lk.env = openRecallEnv(lk.profile, NewCLIOutput(true, false))
		} else if env, err := openRecallEnvErr(lk.profile, NewCLIOutput(true, false)); err != nil {
			lk.envErr = fmt.Errorf("%w: %v", errRecallIndexUnavailable, err)
		} else {
			lk.env = env
		}
	}
	if lk.envErr != nil {
		return nil, lk.envErr
	}
	h, path, sess, err := query.New(lk.env.st, lk.env.stateDB).NativeSource(context.Background(), ref)
	if err != nil {
		if errors.Is(err, query.ErrNotFound) {
			return nil, fmt.Errorf("no session, conversation or indexed transcript matches %q", ref)
		}
		return nil, err
	}
	t := &rowsTarget{storage: storage, native: sess.NativeID, direct: query.SupportsDirectRows(h)}
	t.src = query.RowsSource{Harness: h, Path: path}
	t.session = query.RowsSession{ID: sess.DeckID, Harness: h, NativeID: sess.NativeID, Path: path, Title: sess.Title, Cwd: sess.CWD}
	t.inst = inst
	return t, nil
}

// close releases what resolveRowsTarget opened for this target; targets
// resolved inside a batch share their lookup and own nothing.
func (t *rowsTarget) close() {
	if t.owner != nil {
		t.owner.close()
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
		exitCLI(1)
	}
	defer t.close()
	result, code, err := readRowsTimeline(t, opts)
	if err != nil {
		out.Error(err.Error(), code)
		exitCLI(1)
	}
	out.printJSON(result)
}

// readRowsTimeline is the object `recall timeline <session> --json` prints
// for a resolved target, or the error and its CLI code. The single-id and
// the --sessions batch paths both answer through it.
func readRowsTimeline(t *rowsTarget, opts query.RowsOptions) (query.RowsTimeline, string, error) {
	var result query.RowsTimeline
	var err error
	if t.direct {
		result, err = query.ReadRows(context.Background(), t.src, opts)
	} else if opts.Before != "" {
		err = fmt.Errorf("recall timeline: --before is unsupported for harness %s", t.src.Harness)
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
		return query.RowsTimeline{}, code, err
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
	return result, "", nil
}

func handleRecallFollowRows(profile, ref, after string, f rowsFlags, withStatus bool) {
	t, err := resolveRowsTarget(profile, ref, f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "recall follow:", err)
		exitCLI(1)
	}
	defer t.close()
	if !t.direct {
		fmt.Fprintf(os.Stderr, "recall follow: %s transcripts are streamed with --v1\n", t.src.Harness)
		exitCLI(2)
	}
	ctx, cancel := interruptibleContext()
	defer cancel()
	if after == "end" {
		// Start at the current end: a client that painted from its own
		// cache or a --tail timeline only needs what lands from now on.
		tl, err := query.ReadRows(ctx, t.src, query.RowsOptions{Tail: 1})
		if err != nil {
			fmt.Fprintln(os.Stderr, "recall follow:", err)
			exitCLI(1)
		}
		after = tl.ThroughCursor
	}
	var status func() *query.LiveStatus
	if withStatus {
		status = t.liveStatusFunc()
	}
	enc := json.NewEncoder(os.Stdout)
	var delivery func() []query.RowFrame
	if t.inst != nil {
		delivery = deliveryFrames(profile, t.storage, t.inst.ID)
	}
	err = query.FollowRows(ctx, t.src, after, 0, status, delivery, func(frame query.RowFrame) error {
		return enc.Encode(frame)
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "recall follow:", strings.TrimSpace(err.Error()))
		exitCLI(1)
	}
}
