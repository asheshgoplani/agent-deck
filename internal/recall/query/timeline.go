package query

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
)

// Turn is one native conversation event in source order. Raw retains the
// source payload for kinds the client does not yet understand.
type Turn struct {
	Seq       int64           `json:"seq"`
	Role      string          `json:"role"`
	Kind      string          `json:"kind"`
	Timestamp string          `json:"timestamp,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	Text      string          `json:"text,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

// Timeline is a snapshot of one indexed conversation. ThroughCursor is
// computed while the Recall read transaction remains open.
type Timeline struct {
	Session       SessionRow `json:"session"`
	Turns         []Turn     `json:"turns"`
	ThroughCursor string     `json:"through_cursor"`
}

// Frame is one newline-delimited follow event.
type Frame struct {
	Type   string `json:"type"`
	Turn   *Turn  `json:"turn,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// timelineSource is the indexed native source address captured by a read
// transaction. Parsers read the source directly because the search index
// intentionally omits tool results and non-text records.
type timelineSource struct {
	Harness string
	Path    string
}

type timelineCursor struct {
	Version int    `json:"v"`
	Session int64  `json:"session"`
	Count   int    `json:"count"`
	Hash    string `json:"hash"`
}

var errTimelineSourceChanged = errors.New("recall: native source changed during timeline read; retry")
var errTimelineSourceMissing = errors.New("recall: no readable native transcript")

func cursorFor(sessID int64, turns []Turn) string {
	b, _ := json.Marshal(turns)
	h := sha256.Sum256(b)
	c, _ := json.Marshal(timelineCursor{Version: 1, Session: sessID, Count: len(turns), Hash: fmt.Sprintf("%x", h)})
	return base64.RawURLEncoding.EncodeToString(c)
}

func decodeTimelineCursor(encoded string) (timelineCursor, error) {
	var c timelineCursor
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || json.Unmarshal(b, &c) != nil || c.Version != 1 || c.Session <= 0 || c.Count < 0 || c.Hash == "" {
		return c, errors.New("recall: invalid timeline cursor")
	}
	return c, nil
}

// Timeline reads the session identity and source ledger in one read-only
// transaction, then captures the native records before making its cursor.
func (s *Searcher) Timeline(ctx context.Context, ref string) (Timeline, error) {
	tx, err := s.st.ReadSnapshot(ctx)
	if err != nil {
		return Timeline{}, err
	}
	defer tx.Rollback()
	sess, err := resolveTimelineSession(ctx, tx, ref)
	if err != nil {
		return Timeline{}, err
	}
	if sess.DigestOnly {
		return Timeline{}, fmt.Errorf("recall: %s is a remote card without a local transcript", ref)
	}
	rows, err := tx.QueryContext(ctx, `SELECT harness, path FROM source WHERE sess_id=? AND state NOT IN (?,?) ORDER BY src_id`, sess.SessID, recall.SourceMissing, recall.SourceQuarantined)
	if err != nil {
		return Timeline{}, err
	}
	var sources []timelineSource
	for rows.Next() {
		var src timelineSource
		if err := rows.Scan(&src.Harness, &src.Path); err != nil {
			rows.Close()
			return Timeline{}, err
		}
		sources = append(sources, src)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Timeline{}, err
	}
	if len(sources) == 0 {
		return Timeline{}, fmt.Errorf("%w for %s", errTimelineSourceMissing, ref)
	}
	result := Timeline{Session: sess, Turns: make([]Turn, 0)}
	for _, src := range sources {
		turns, err := parseStableTimelineSource(ctx, src, sess.NativeID)
		if err != nil {
			return Timeline{}, err
		}
		for _, turn := range turns {
			turn.Seq = int64(len(result.Turns) + 1)
			result.Turns = append(result.Turns, turn)
		}
	}
	result.ThroughCursor = cursorFor(sess.SessID, result.Turns)
	if err := tx.Commit(); err != nil {
		return Timeline{}, err
	}
	return result, nil
}

// parseStableTimelineSource checks that a mutable native source was not
// replaced or rewritten while it was read. A pure append may happen after
// the bounded read: its existing prefix stays valid and follow picks up the
// appended records. OpenCode's message and part files are a tree, so a
// second parse checks that tree even when its session file is unchanged.
func parseStableTimelineSource(ctx context.Context, src timelineSource, nativeID string) ([]Turn, error) {
	path := src.Path
	if src.Harness == "hermes" {
		path, _, _ = strings.Cut(path, "#")
	}
	for attempt := 0; attempt < 3; attempt++ {
		before, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		turns, err := parseTimelineSource(ctx, src, nativeID)
		if err != nil {
			return nil, err
		}
		after, err := os.Stat(path)
		if err != nil {
			continue // rotated while reading
		}
		if src.Harness != "opencode" && os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime() == after.ModTime() {
			return turns, nil
		}
		// This also checks a growing file: an unchanged prefix is safe as
		// the cursor anchor even when another complete line arrived later.
		again, err := parseTimelineSource(ctx, src, nativeID)
		if err == nil && os.SameFile(before, after) && len(again) >= len(turns) && reflect.DeepEqual(turns, again[:len(turns)]) {
			return turns, nil
		}
	}
	return nil, errTimelineSourceChanged
}

func resolveTimelineSession(ctx context.Context, tx *sql.Tx, ref string) (SessionRow, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == "#" {
		return SessionRow{}, ErrNotFound
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(ref, "#"), 10, 64)
	if id <= 0 && strings.HasPrefix(ref, "#") {
		return SessionRow{}, ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+sessionCols+` FROM session s LEFT JOIN card c ON c.sess_id=s.sess_id
		WHERE s.sess_id=? OR s.deck_id=? OR s.native_id=? OR s.native_id LIKE ? ESCAPE '\'
		ORDER BY (s.sess_id=?) DESC, (s.deck_id=?) DESC, (s.native_id=?) DESC LIMIT 2`,
		id, ref, ref, escapeLike(ref)+"%", id, ref, ref)
	if err != nil {
		return SessionRow{}, err
	}
	defer rows.Close()
	var found []SessionRow
	for rows.Next() {
		r, err := scanSession(rows)
		if err != nil {
			return SessionRow{}, err
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return SessionRow{}, err
	}
	if len(found) == 0 {
		return SessionRow{}, ErrNotFound
	}
	if len(found) > 1 && found[0].SessID != id && found[0].DeckID != ref && found[0].NativeID != ref {
		return SessionRow{}, fmt.Errorf("recall: %q is ambiguous", ref)
	}
	return found[0], nil
}

// Follow polls the native source every 250 ms. This avoids waiting for an
// index sweep, which could discover every root and exceed the live bound.
// Every emitted cursor includes that exact prefix, so reconnects neither
// lose nor duplicate turns.
func (s *Searcher) Follow(ctx context.Context, ref, after string, emit func(Frame) error) error {
	c, err := decodeTimelineCursor(after)
	if err != nil {
		current, lookupErr := s.Timeline(ctx, ref)
		if lookupErr != nil {
			return lookupErr
		}
		return emit(Frame{Type: "resync_required", Cursor: current.ThroughCursor})
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := s.Timeline(ctx, ref)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, errTimelineSourceChanged) || errors.Is(err, errTimelineSourceMissing) {
				return emit(Frame{Type: "resync_required"})
			}
			return err
		}
		if current.Session.SessID != c.Session || c.Count > len(current.Turns) || cursorFor(c.Session, current.Turns[:c.Count]) != after {
			return emit(Frame{Type: "resync_required", Cursor: current.ThroughCursor})
		}
		for i := c.Count; i < len(current.Turns); i++ {
			turn := current.Turns[i]
			cursor := cursorFor(c.Session, current.Turns[:i+1])
			if err := emit(Frame{Type: "turn", Turn: &turn, Cursor: cursor}); err != nil {
				return err
			}
			after = cursor
		}
		c.Count = len(current.Turns)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
