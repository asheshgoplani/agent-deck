package reader

import (
	"context"
	"database/sql"
	"encoding/json"
	"hash/fnv"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/recall"
)

// Hermes reads Hermes Agent's own store, <home>/state.db: sessions (id,
// parent_session_id, cwd, git_branch, title, model, token counters) and
// messages (autoincrement id, role, content, tool_calls, tool_name,
// timestamp). One source per Hermes session, addressed as
// "<state.db>#<session id>" with an opaque cursor: parsed_to is the last
// mirrored message id, so a pass reads only messages with a higher id.
// The database is opened mode=ro and never written. The design proposed
// federating body search into Hermes's own messages_fts at query time;
// mirroring the rows costs bytes proportional to Hermes's 225 KB store and
// keeps one query path, so that is what ships (docs/recall.md).
type Hermes struct{}

// HarnessHermes is the harness name stored on every Hermes row.
const HarnessHermes = "hermes"

// hermesStateDB is Hermes's SQLite store under its config dir.
const hermesStateDB = "state.db"

func (Hermes) Harness() string { return HarnessHermes }

// Cursor: a row id, not a byte offset.
func (Hermes) Cursor() CursorKind { return CursorOpaque }

// Discover lists Hermes sessions: one source each, sized by its highest
// message id and dated by its last activity, so the ledger's size/mtime
// comparison sees a session that gained messages.
func (Hermes) Discover(ctx context.Context, roots []Root, emit func(SourceRef) error) error {
	seenDB := map[string]bool{}
	for _, r := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		dbPath := filepath.Join(r.Dir, hermesStateDB)
		if real, err := fsEvalSymlinks(dbPath); err == nil {
			dbPath = real
		}
		if seenDB[dbPath] {
			continue
		}
		seenDB[dbPath] = true
		if _, err := fsStat(dbPath); err != nil {
			continue
		}
		db, err := openHermes(dbPath)
		if err != nil {
			continue
		}
		err = hermesSessions(ctx, db, func(id string, lastMsg int64, lastTS float64) error {
			path := dbPath + "#" + id
			// Not a file: the ledger's (dev, ino) identity is a hash of
			// the address (dev 0 never collides with a real device).
			return emit(SourceRef{
				Harness:  HarnessHermes,
				Profile:  r.Profile,
				Path:     path,
				Ino:      pathIdentity(path),
				Size:     lastMsg,
				MtimeNS:  int64(lastTS * 1e9),
				NativeID: id,
				Aux:      dbPath,
			})
		})
		db.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func openHermes(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(2000)&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func hermesSessions(ctx context.Context, db *sql.DB, fn func(id string, lastMsg int64, lastTS float64) error) error {
	rows, err := db.QueryContext(ctx, `SELECT s.id, COALESCE(MAX(m.id), 0), COALESCE(MAX(m.timestamp), s.started_at)
		FROM sessions s LEFT JOIN messages m ON m.session_id = s.id GROUP BY s.id ORDER BY s.started_at`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var lastMsg int64
		var lastTS float64
		if err := rows.Scan(&id, &lastMsg, &lastTS); err != nil {
			return err
		}
		if err := fn(id, lastMsg, lastTS); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Ingest mirrors the session row and every message with id > from.
func (Hermes) Ingest(ctx context.Context, src SourceRef, from int64, sink Sink, b *Budget) (int64, error) {
	dbPath, id, _ := strings.Cut(src.Path, "#")
	if src.Aux != "" {
		dbPath = src.Aux
	}
	if id == "" {
		id = src.NativeID
	}
	db, err := openHermes(dbPath)
	if err != nil {
		return from, err
	}
	defer db.Close()
	var s struct {
		parent, cwd, branch, title, titleSrc, model sql.NullString
	}
	var startedAt float64
	err = db.QueryRowContext(ctx, `SELECT parent_session_id, cwd, git_branch, title, title_source, model, started_at FROM sessions WHERE id=?`, id).
		Scan(&s.parent, &s.cwd, &s.branch, &s.title, &s.titleSrc, &s.model, &startedAt)
	if err != nil {
		return from, err
	}
	sink.Session(Session{NativeID: id, CWD: s.cwd.String, Branch: s.branch.String, Model: s.model.String, ForkOf: s.parent.String})
	if s.title.String != "" {
		sink.Session(Session{Title: s.title.String, TitleSrc: firstNonEmpty(s.titleSrc.String, "title")})
	}
	rows, err := db.QueryContext(ctx, `SELECT id, role, COALESCE(content,''), COALESCE(tool_calls,''), COALESCE(tool_name,''), COALESCE(tool_call_id,''), timestamp, COALESCE(compacted,0)
		FROM messages WHERE session_id=? AND id>? ORDER BY id`, id, from)
	if err != nil {
		return from, err
	}
	defer rows.Close()
	last := from
	pending := map[string]pendingCall{}
	for i := 0; rows.Next(); i++ {
		if i&63 == 0 {
			if err := ctx.Err(); err != nil {
				return last, err
			}
			if b.Expired() {
				return last, ErrBudget
			}
		}
		var (
			msgID, compacted                   int64
			role, content, calls, tool, callID string
			ts                                 float64
		)
		if err := rows.Scan(&msgID, &role, &content, &calls, &tool, &callID, &ts, &compacted); err != nil {
			return last, err
		}
		n := int64(len(content) + len(calls))
		switch role {
		case "user", "assistant":
			m := Msg{Role: recall.RoleUser, TS: int64(ts), Text: content, IsCompact: compacted == 1}
			if role == "assistant" {
				m.Role = recall.RoleAssistant
				for _, c := range hermesToolCalls(calls) {
					m.ToolNames = append(m.ToolNames, c.name)
					pending[c.id] = pendingCall{name: c.name, ts: unixFloat(ts), digest: c.digest}
				}
			}
			if strings.TrimSpace(m.Text) != "" {
				if err := sink.Msg(m); err != nil {
					return last, err
				}
			}
		case "tool":
			pc, ok := pending[callID]
			if !ok {
				pc = pendingCall{name: tool, ts: unixFloat(ts)}
			}
			delete(pending, callID)
			tc := ToolCall{Name: pc.name, TS: unixOrZero(pc.ts), ArgDigest: pc.digest, IsError: strings.HasPrefix(strings.TrimSpace(content), "Error")}
			if end := unixFloat(ts); end.After(pc.ts) && !pc.ts.IsZero() {
				tc.DurationMS = end.Sub(pc.ts).Milliseconds()
			}
			if err := sink.ToolCall(tc); err != nil {
				return last, err
			}
		}
		last = msgID
		if !b.Consume(n) {
			return last, ErrBudget
		}
	}
	if err := rows.Err(); err != nil {
		return last, err
	}
	for _, pc := range pending {
		if err := sink.ToolCall(ToolCall{Name: pc.name, TS: unixOrZero(pc.ts), ArgDigest: pc.digest}); err != nil {
			return last, err
		}
	}
	// Hermes keeps token totals per session, not per message.
	var in, out, cr, cw int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(cache_read_tokens,0), COALESCE(cache_write_tokens,0) FROM sessions WHERE id=?`, id).
		Scan(&in, &out, &cr, &cw); err == nil && from == 0 && in+out > 0 {
		_ = sink.Usage(Usage{UUID: "hermes-session-" + id, TS: unixFloat(startedAt), Model: s.model.String, In: in, Out: out, CacheR: cr, CacheW: cw})
	}
	return last, nil
}

// pathIdentity is a stable 64-bit hash of a source address.
func pathIdentity(path string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(path))
	return h.Sum64()
}

type hermesCall struct{ id, name, digest string }

// hermesToolCalls decodes the OpenAI-shaped tool_calls column.
func hermesToolCalls(raw string) []hermesCall {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var calls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if json.Unmarshal([]byte(raw), &calls) != nil {
		return nil
	}
	out := make([]hermesCall, 0, len(calls))
	for _, c := range calls {
		digest, _ := digestArgs(c.Function.Name, json.RawMessage(c.Function.Arguments))
		out = append(out, hermesCall{id: c.ID, name: c.Function.Name, digest: digest})
	}
	return out
}
