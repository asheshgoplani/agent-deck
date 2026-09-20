// Package query is the read side of recall.db: the candidate-set
// intersection, the SQL ranking, snippet rendering, listing and status. It
// never writes.
package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/classify"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
)

// Defaults of the search plan.
const (
	DefaultLimit = 20
	// CandidateCeiling caps the msg_fts rowid set: msg_fts cannot rank, so
	// no LIMIT pushes into it; the ceiling is reported when hit.
	CandidateCeiling = 5000
	// DefaultPhraseScan is how many candidates --phrase verifies.
	DefaultPhraseScan = 2000
	snippetChars      = 160
)

// Filters are the structural filters shared by search and sessions.
type Filters struct {
	Harness string
	Profile string
	// Project matches session.project_key (the cwd) exactly.
	Project string
	Since   time.Time
	// Hints are key=value filters resolved live against state.db.
	Hints map[string]string
	Tags  []string
	// DeckID restricts to one agent-deck session.
	DeckID string
	// IncludeSidechains lists subagent transcripts too (default: hidden).
	IncludeSidechains bool
}

// Searcher runs queries against one open store and, optionally, the active
// profile's state.db (attached read-only as `reg` for hint and tag filters
// so an annotation typed a second ago is immediately searchable).
type Searcher struct {
	st          *store.Store
	stateDBPath string
}

// New returns a Searcher. stateDBPath may be "" (no hint filters then).
func New(st *store.Store, stateDBPath string) *Searcher {
	return &Searcher{st: st, stateDBPath: stateDBPath}
}

// SearchOptions configures one search.
type SearchOptions struct {
	Filters
	Query string
	// Role restricts body hits to user (1) or assistant (2) messages.
	Role int
	// Phrase verifies the literal phrase over the top PhraseScan candidate
	// bodies and reports how many were checked.
	Phrase     bool
	PhraseScan int
	Limit      int
	// Ceiling overrides CandidateCeiling (0: the default); tests use it to
	// reproduce the ceiling on a small corpus.
	Ceiling int
}

// Hit is one ranked session.
type Hit struct {
	SessID    int64  `json:"sess_id"`
	Harness   string `json:"harness"`
	Profile   string `json:"profile,omitempty"`
	NativeID  string `json:"native_id"`
	DeckID    string `json:"deck_id,omitempty"`
	Title     string `json:"title,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
	EndedAt   int64  `json:"ended_at,omitempty"`
	BodyHits  int    `json:"body_hits"`
	CardHit   bool   `json:"card_hit"`
	Snippet   string `json:"snippet,omitempty"`
	// Missing means the source file is gone: the hit is listed from the
	// stored card and bodies, and cannot be reopened from disk.
	Missing bool `json:"missing,omitempty"`
	// Verified is set by --phrase: the literal phrase occurs in a body
	// (true), or every matching body was read whole without it (false). It
	// stays nil when the scan limit ran out before this hit's bodies were
	// reached, or when a body that could refute the phrase is clipped.
	Verified *bool `json:"verified,omitempty"`
	// Clipped means a nil Verified is because a matching body is stored
	// clipped (text_tier=clipped, 8 KiB) or could not be decompressed: the
	// phrase may sit in the part that is not stored, so it is unknown, not
	// absent.
	Clipped bool `json:"clipped,omitempty"`
	// PhraseChecked is set on every hit of a --phrase search, so a nil
	// Verified reads as "not reached", not "not asked".
	PhraseChecked bool `json:"phrase_checked,omitempty"`
	// Sidechain marks a subagent transcript.
	Sidechain bool `json:"sidechain,omitempty"`
	// HostUID and DigestOnly mark a card pulled from another machine.
	HostUID    string `json:"host_uid,omitempty"`
	DigestOnly bool   `json:"digest_only,omitempty"`
	// Remote is the alias of the remote that answered a federated search
	// (set by the CLI, never by the index).
	Remote string `json:"remote,omitempty"`
}

// SearchResult is what `recall search` prints.
type SearchResult struct {
	Query      string `json:"query"`
	Match      string `json:"match"`
	Hits       []Hit  `json:"hits"`
	Candidates int    `json:"candidates"`
	// CeilingHit means more than CandidateCeiling messages matched the
	// query and the filters, and the ranking saw only the newest
	// CandidateCeiling of them.
	CeilingHit bool `json:"ceiling_hit,omitempty"`
	// Scanned and VerifiedCount describe the --phrase pass.
	Scanned       int   `json:"scanned,omitempty"`
	VerifiedCount int   `json:"verified,omitempty"`
	ElapsedMS     int64 `json:"elapsed_ms"`
}

// MatchExpr turns a user query into the msg_fts expression: every term
// quoted (tokenchars make SB-412 one token, but the query parser still
// reads a bare '-' as an operator), AND / OR / NOT kept as operators, a
// trailing '*' kept as a prefix query. Column filters (tags:foo) are
// dropped: msg_fts has one column.
func MatchExpr(q string) string { return matchExpr(q, false) }

// CardMatchExpr is MatchExpr for card_fts, keeping column filters
// (title:, hints:, tags:, summary:, preview:).
func CardMatchExpr(q string) string { return matchExpr(q, true) }

func matchExpr(q string, keepCols bool) string {
	var parts []string
	for _, tok := range strings.Fields(q) {
		if isOperator(tok) {
			parts = append(parts, tok)
			continue
		}
		col := ""
		if i := strings.IndexByte(tok, ':'); i > 0 && i < len(tok)-1 && isIdent(tok[:i]) {
			col, tok = tok[:i]+":", tok[i+1:]
		}
		prefix := strings.HasSuffix(tok, "*")
		tok = strings.TrimSuffix(tok, "*")
		tok = strings.Trim(tok, `"`)
		if tok == "" {
			continue
		}
		if !keepCols {
			col = ""
		}
		term := col + `"` + strings.ReplaceAll(tok, `"`, `""`) + `"`
		if prefix {
			term += "*"
		}
		parts = append(parts, term)
	}
	return strings.Join(parts, " ")
}

// isOperator reports an FTS5 boolean operator token.
func isOperator(tok string) bool {
	return tok == "AND" || tok == "OR" || tok == "NOT"
}

func isIdent(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_') {
			return false
		}
	}
	return s != ""
}

// Search runs the plan: card_fts (ranked, phrase-capable) for the spine,
// msg_fts membership intersected with the structural filters in SQL and
// then capped at the candidate ceiling (newest first), then a fixed
// order: card hit first, then body hit count, then recency. A hint or card
// hit always outranks an incidental body mention.
func (s *Searcher) Search(ctx context.Context, o SearchOptions) (SearchResult, error) {
	start := time.Now()
	res := SearchResult{Query: o.Query, Match: MatchExpr(o.Query)}
	if res.Match == "" {
		return res, errors.New("recall: empty query")
	}
	if o.Limit <= 0 {
		o.Limit = DefaultLimit
	}
	ceiling := o.Ceiling
	if ceiling <= 0 {
		ceiling = CandidateCeiling
	}
	conn, detach, err := s.conn(ctx, o.Filters)
	if err != nil {
		return res, err
	}
	defer detach()

	where, args := s.filterSQL(o.Filters)
	roleSQL := roleClause(o.Role)
	// The structural filters narrow the body candidates BEFORE the
	// ceiling (the design's query plan): msg_fts membership is joined to
	// msg and session and filtered in the same subquery, and the ceiling
	// takes the newest rowids, so what was appended after a backfill is
	// never beyond reach. A filter narrows the set; it is never applied to
	// an already truncated one.
	hits := `SELECT f.rowid AS msg_id, m.sess_id, m.ts FROM msg_fts f JOIN msg m ON m.msg_id=f.rowid JOIN session s ON s.sess_id=m.sess_id
		WHERE msg_fts MATCH ?` + roleSQL + where + ` ORDER BY f.rowid DESC LIMIT ?`
	// Candidate count first, so the ceiling is reported honestly.
	countArgs := append([]any{res.Match}, args...)
	countArgs = append(countArgs, ceiling+1)
	//nolint:gosec // hits is fixed SQL text plus roleClause/filterSQL output; all values are bound via ? args, never interpolated
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM (`+hits+`)`, countArgs...).Scan(&res.Candidates); err != nil {
		return res, fmt.Errorf("recall: match %q: %w", res.Match, err)
	}
	if res.Candidates > ceiling {
		res.Candidates = ceiling
		res.CeilingHit = true
	}
	//nolint:gosec // fragments are fixed SQL text and roleClause/filterSQL output; all values are bound via ? args, never interpolated
	q := `WITH hits AS (` + hits + `),
	per_sess AS (SELECT sess_id, count(*) AS n, max(ts) AS last, min(msg_id) AS first_msg FROM hits GROUP BY sess_id),
	cards AS (SELECT rowid AS sess_id, bm25(card_fts) AS score FROM card_fts WHERE card_fts MATCH ?),
	cand AS (SELECT sess_id FROM per_sess UNION SELECT sess_id FROM cards)
	SELECT s.sess_id, s.harness, s.profile, s.native_id, s.deck_id, COALESCE(s.title,''), COALESCE(s.cwd,''),
		COALESCE(s.started_at,0), COALESCE(s.ended_at,0), COALESCE(p.n,0), c.score IS NOT NULL, COALESCE(p.first_msg,0), s.is_sidechain,
		EXISTS(SELECT 1 FROM source src WHERE src.sess_id=s.sess_id AND src.state=` + strconv.Itoa(recall.SourceMissing) + `),
		s.host_uid, s.digest_only
	FROM cand JOIN session s ON s.sess_id=cand.sess_id
	LEFT JOIN per_sess p ON p.sess_id=s.sess_id LEFT JOIN cards c ON c.sess_id=s.sess_id
	WHERE 1=1` + where + `
	ORDER BY (c.score IS NOT NULL) DESC, COALESCE(p.n,0) DESC, COALESCE(p.last, s.ended_at, 0) DESC
	LIMIT ?`
	qargs := append([]any{res.Match}, args...)
	qargs = append(qargs, ceiling, CardMatchExpr(o.Query))
	qargs = append(qargs, args...)
	qargs = append(qargs, o.Limit)
	rows, err := conn.QueryContext(ctx, q, qargs...)
	if err != nil {
		return res, fmt.Errorf("recall: search: %w", err)
	}
	var firsts []int64
	for rows.Next() {
		var h Hit
		var firstMsg int64
		var cardHit, sidechain, missing, digest int
		if err := rows.Scan(&h.SessID, &h.Harness, &h.Profile, &h.NativeID, &h.DeckID, &h.Title, &h.CWD, &h.StartedAt, &h.EndedAt,
			&h.BodyHits, &cardHit, &firstMsg, &sidechain, &missing, &h.HostUID, &digest); err != nil {
			rows.Close()
			return res, err
		}
		h.CardHit, h.Sidechain, h.Missing, h.DigestOnly = cardHit == 1, sidechain == 1, missing == 1, digest == 1
		if h.HostUID == store.LocalHostUID {
			h.HostUID = ""
		}
		res.Hits = append(res.Hits, h)
		firsts = append(firsts, firstMsg)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	terms := queryTerms(o.Query)
	for i := range res.Hits {
		if firsts[i] == 0 {
			continue
		}
		body, err := s.body(ctx, conn, firsts[i])
		if err == nil {
			res.Hits[i].Snippet = Snippet(body, terms, snippetChars)
		}
	}
	if o.Phrase {
		if err := s.verifyPhrase(ctx, conn, &res, o); err != nil {
			return res, err
		}
	}
	res.ElapsedMS = time.Since(start).Milliseconds()
	return res, nil
}

// verifyPhrase checks the literal phrase in the bodies of the ranked hits'
// own matching messages, hit by hit in rank order and newest message
// first, decompressing at most PhraseScan bodies in all. The scan follows
// the hits (the same filtered candidate set the ranking used), never an
// unfiltered window of the oldest rowids. A hit is verified once one body
// carries the phrase, NOT found once every matching body was read whole
// without it, and left unverified (nil) when the budget ran out before its
// bodies were reached or when a matching body is stored clipped (the
// phrase may sit past the clip; msg_fts is detail=none and cannot say, and
// the source file is not reopened at query time): unknown is reported as
// unknown. Scanned and VerifiedCount say what was checked.
func (s *Searcher) verifyPhrase(ctx context.Context, conn *sql.Conn, res *SearchResult, o SearchOptions) error {
	scan := o.PhraseScan
	if scan <= 0 {
		scan = DefaultPhraseScan
	}
	phrase := strings.ToLower(strings.Join(queryTerms(o.Query), " "))
	match := strings.Join(mustTerms(o.Query), " AND ")
	roleSQL := roleClause(o.Role)
	for i := range res.Hits {
		res.Hits[i].PhraseChecked = true
	}
	for i := range res.Hits {
		if res.Scanned >= scan {
			break // out of budget: the rest stay unverified
		}
		r, err := s.phraseInSession(ctx, conn, match, phrase, roleSQL, res.Hits[i].SessID, scan-res.Scanned)
		res.Scanned += r.scanned
		if err != nil {
			return err
		}
		switch {
		case r.found:
			v := true
			res.Hits[i].Verified = &v
			res.VerifiedCount++
		case !r.complete:
			// budget ran out inside this session: stays unverified
		case r.clipped:
			res.Hits[i].Clipped = true
		default:
			v := false
			res.Hits[i].Verified = &v
		}
	}
	return nil
}

// phraseScan is what phraseInSession learned about one session: found
// once a body carries the phrase; complete once every matching body was
// reached (false when the budget ran out first); clipped when a body that
// did not carry the phrase is shorter than its message (nchars) or would
// not decompress, so absence is not proven; scanned counts the bodies
// decompressed.
type phraseScan struct {
	found, complete, clipped bool
	scanned                  int
}

// phraseInSession reads up to limit matching bodies of one session, newest
// first. A clipped body can confirm the phrase but never refute it.
func (s *Searcher) phraseInSession(ctx context.Context, conn *sql.Conn, match, phrase, roleSQL string, sessID int64, limit int) (r phraseScan, err error) {
	//nolint:gosec // roleSQL is fixed text from roleClause; all values are bound via ? args, never interpolated
	rows, err := conn.QueryContext(ctx, `SELECT m.body, m.nchars FROM msg_fts f JOIN msg m ON m.msg_id=f.rowid
		WHERE msg_fts MATCH ? AND m.sess_id=?`+roleSQL+` ORDER BY f.rowid DESC LIMIT ?`, match, sessID, limit+1)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		if r.scanned == limit {
			return r, rows.Err() // more bodies than budget: unknown
		}
		var body []byte
		var nchars int
		if err := rows.Scan(&body, &nchars); err != nil {
			return r, err
		}
		r.scanned++
		text, err := recall.DecompressBody(body)
		if err != nil {
			r.clipped = true
			continue
		}
		if strings.Contains(strings.ToLower(string(text)), phrase) {
			r.found, r.complete = true, true
			return r, rows.Close()
		}
		if len(text) < nchars {
			r.clipped = true
		}
	}
	r.complete = true
	return r, rows.Err()
}

// roleClause is the body-hit role restriction as a WHERE clause fragment on
// msg m, empty when no role is asked for.
func roleClause(role int) string {
	if role == 0 {
		return ""
	}
	return " AND m.role=" + strconv.Itoa(role)
}

// mustTerms is the query's words (no operators, no column prefixes),
// quoted, for the AND candidate query of --phrase.
func mustTerms(q string) []string {
	terms := queryTerms(q)
	out := make([]string, 0, len(terms))
	for _, tok := range terms {
		out = append(out, `"`+strings.ReplaceAll(tok, `"`, `""`)+`"`)
	}
	return out
}

func queryTerms(q string) []string {
	var out []string
	for _, tok := range strings.Fields(q) {
		if i := strings.IndexByte(tok, ':'); i > 0 && isIdent(tok[:i]) {
			tok = tok[i+1:]
		}
		tok = strings.Trim(tok, `"*`)
		if tok != "" && !isOperator(tok) {
			out = append(out, tok)
		}
	}
	return out
}

// Snippet returns about n characters of text around the first term that
// occurs (case-insensitive), whitespace collapsed.
func Snippet(text string, terms []string, n int) string {
	flat := strings.Join(strings.Fields(text), " ")
	lower := strings.ToLower(flat)
	at := -1
	for _, t := range terms {
		if i := strings.Index(lower, strings.ToLower(t)); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	if at < 0 {
		at = 0
	}
	start := at - n/3
	if start < 0 {
		start = 0
	}
	end := start + n
	if end > len(flat) {
		end = len(flat)
		if start = end - n; start < 0 {
			start = 0
		}
	}
	for start > 0 && flat[start]&0xC0 == 0x80 {
		start--
	}
	for end < len(flat) && flat[end]&0xC0 == 0x80 {
		end++
	}
	out := flat[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(flat) {
		out += "…"
	}
	return out
}

// conn takes one reader connection and attaches state.db as `reg` when a
// hint or tag filter needs it.
func (s *Searcher) conn(ctx context.Context, f Filters) (*sql.Conn, func(), error) {
	conn, err := s.st.R.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	attached := false
	if len(f.Hints) > 0 || len(f.Tags) > 0 {
		if s.stateDBPath == "" {
			conn.Close()
			return nil, nil, errors.New("recall: hint and tag filters need the profile's state.db")
		}
		if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS reg`, s.stateDBPath); err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("recall: attach state.db: %w", err)
		}
		attached = true
	}
	return conn, func() {
		if attached {
			_, _ = conn.ExecContext(context.Background(), `DETACH DATABASE reg`)
		}
		conn.Close()
	}, nil
}

// filterSQL renders Filters as " AND ..." clauses on alias s.
func (s *Searcher) filterSQL(f Filters) (string, []any) {
	var sb strings.Builder
	var args []any
	if f.Harness != "" {
		sb.WriteString(" AND s.harness=?")
		args = append(args, f.Harness)
	}
	if f.Profile != "" {
		sb.WriteString(" AND s.profile=?")
		args = append(args, f.Profile)
	}
	if f.Project != "" {
		sb.WriteString(" AND s.project_key=?")
		args = append(args, f.Project)
	}
	if !f.Since.IsZero() {
		sb.WriteString(" AND COALESCE(s.ended_at, s.started_at, 0) >= ?")
		args = append(args, f.Since.Unix())
	}
	if f.DeckID != "" {
		sb.WriteString(" AND s.deck_id=?")
		args = append(args, f.DeckID)
	}
	if !f.IncludeSidechains {
		sb.WriteString(" AND s.is_sidechain=0")
	}
	// Hint and tag filters resolve live: instance-scoped rows reach the
	// session through the authoritative link in state.db (not through the
	// deck_id column, which only moves on a sweep), harness-scoped rows
	// through the conversation id.
	for k, v := range f.Hints {
		sb.WriteString(` AND (s.native_id IN (SELECT l.native_id FROM reg.session_links l JOIN reg.session_hints h ON h.scope_kind='instance' AND h.scope_id=l.session_id
				WHERE l.authoritative=1 AND l.harness=s.harness AND h.key=? AND h.value=?)
			OR s.native_id IN (SELECT scope_id FROM reg.session_hints WHERE scope_kind='harness_session' AND key=? AND value=?))`)
		args = append(args, k, v, k, v)
	}
	for _, tag := range f.Tags {
		sb.WriteString(` AND (s.native_id IN (SELECT l.native_id FROM reg.session_links l JOIN reg.session_tags t ON t.scope_kind='instance' AND t.scope_id=l.session_id
				WHERE l.authoritative=1 AND l.harness=s.harness AND t.tag=? AND t.deleted_at=0)
			OR s.native_id IN (SELECT scope_id FROM reg.session_tags WHERE scope_kind='harness_session' AND tag=? AND deleted_at=0))`)
		args = append(args, tag, tag)
	}
	return sb.String(), args
}

func (s *Searcher) body(ctx context.Context, conn *sql.Conn, msgID int64) (string, error) {
	var body []byte
	if err := conn.QueryRowContext(ctx, `SELECT body FROM msg WHERE msg_id=?`, msgID).Scan(&body); err != nil {
		return "", err
	}
	text, err := recall.DecompressBody(body)
	return string(text), err
}

// ---- sessions ------------------------------------------------------------

// SessionRow is one listed session.
type SessionRow struct {
	SessID     int64   `json:"sess_id"`
	Harness    string  `json:"harness"`
	Profile    string  `json:"profile,omitempty"`
	NativeID   string  `json:"native_id"`
	DeckID     string  `json:"deck_id,omitempty"`
	Title      string  `json:"title,omitempty"`
	TitleSrc   string  `json:"title_src,omitempty"`
	CWD        string  `json:"cwd,omitempty"`
	Branch     string  `json:"branch,omitempty"`
	StartedAt  int64   `json:"started_at,omitempty"`
	EndedAt    int64   `json:"ended_at,omitempty"`
	Turns      int     `json:"turns"`
	ToolCalls  int     `json:"tool_calls"`
	Errors     int     `json:"errors"`
	Interrupts int     `json:"interrupts"`
	Compacts   int     `json:"compacts"`
	InTok      int64   `json:"in_tok"`
	OutTok     int64   `json:"out_tok"`
	CacheR     int64   `json:"cache_r"`
	CacheW     int64   `json:"cache_w"`
	Model      string  `json:"model,omitempty"`
	Sidechain  bool    `json:"sidechain,omitempty"`
	Preview    string  `json:"preview,omitempty"`
	Missing    bool    `json:"missing,omitempty"`
	Path       string  `json:"path,omitempty"`
	DerivedRev int64   `json:"derived_rev"`
	Hints      string  `json:"hints,omitempty"`
	Tags       string  `json:"tags,omitempty"`
	Messages   int     `json:"messages,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
	// HostUID and DigestOnly mark a card imported from another machine
	// (recall pull): no bodies are here, and every listing says so.
	HostUID    string `json:"host_uid,omitempty"`
	DigestOnly bool   `json:"digest_only,omitempty"`
}

// sessionCols selects a SessionRow from session s LEFT JOIN card c: the
// path of the healthiest non-quarantined source, and whether any source is
// missing.
var sessionCols = `s.sess_id, s.harness, s.profile, s.native_id, s.deck_id, COALESCE(s.title,''), COALESCE(s.title_src,''), COALESCE(s.cwd,''), COALESCE(s.branch,''),
	COALESCE(s.started_at,0), COALESCE(s.ended_at,0), s.turns, s.tool_calls, s.errors, s.interrupts, s.compacts, s.in_tok, s.out_tok, s.cache_r, s.cache_w,
	COALESCE(s.model,''), s.is_sidechain, COALESCE(c.preview,''), COALESCE(c.hints,''), COALESCE(c.tags,''), s.derived_rev,
	COALESCE((SELECT path FROM source src WHERE src.sess_id=s.sess_id AND src.state<>` + strconv.Itoa(recall.SourceQuarantined) + ` ORDER BY src.state LIMIT 1),''),
	EXISTS(SELECT 1 FROM source src WHERE src.sess_id=s.sess_id AND src.state=` + strconv.Itoa(recall.SourceMissing) + `),
	s.host_uid, s.digest_only`

func scanSession(sc interface{ Scan(...any) error }) (SessionRow, error) {
	var r SessionRow
	var sidechain, missing, digest int
	err := sc.Scan(&r.SessID, &r.Harness, &r.Profile, &r.NativeID, &r.DeckID, &r.Title, &r.TitleSrc, &r.CWD, &r.Branch,
		&r.StartedAt, &r.EndedAt, &r.Turns, &r.ToolCalls, &r.Errors, &r.Interrupts, &r.Compacts, &r.InTok, &r.OutTok, &r.CacheR, &r.CacheW,
		&r.Model, &sidechain, &r.Preview, &r.Hints, &r.Tags, &r.DerivedRev, &r.Path, &missing, &r.HostUID, &digest)
	r.Sidechain, r.Missing, r.DigestOnly = sidechain == 1, missing == 1, digest == 1
	if r.HostUID == store.LocalHostUID {
		r.HostUID = ""
	}
	return r, err
}

// Sessions lists sessions newest first under the filters.
func (s *Searcher) Sessions(ctx context.Context, f Filters, limit int) ([]SessionRow, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	conn, detach, err := s.conn(ctx, f)
	if err != nil {
		return nil, err
	}
	defer detach()
	where, args := s.filterSQL(f)
	args = append(args, limit)
	//nolint:gosec // sessionCols is a fixed const and where is fixed text from filterSQL; all values are bound via ? args, never interpolated
	rows, err := conn.QueryContext(ctx, `SELECT `+sessionCols+` FROM session s LEFT JOIN card c ON c.sess_id=s.sess_id WHERE 1=1`+where+
		` ORDER BY COALESCE(s.ended_at, s.started_at, 0) DESC, s.sess_id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		r, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- show -----------------------------------------------------------------

// Message is one decoded message for `recall show`.
type Message struct {
	Seq       int    `json:"seq"`
	Role      string `json:"role"`
	Class     string `json:"class"`
	TS        int64  `json:"ts,omitempty"`
	ToolNames string `json:"tool_names,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	NChars    int    `json:"nchars"`
	Text      string `json:"text"`
}

// ToolStat is one row of the tool timeline summary.
type ToolStat struct {
	Name       string `json:"name"`
	Calls      int    `json:"calls"`
	Errors     int    `json:"errors"`
	TotalMS    int64  `json:"total_ms"`
	LastDigest string `json:"last_digest,omitempty"`
}

// Detail is `recall show` for one session.
type Detail struct {
	Session SessionRow `json:"session"`
	Tools   []ToolStat `json:"tools,omitempty"`
	Files   []string   `json:"files,omitempty"`
	// Artifacts are the derived rows (enrich), each marked stale when the
	// session moved since it was produced; present in every tier.
	Artifacts []Artifact `json:"artifacts,omitempty"`
	Messages  []Message  `json:"messages,omitempty"`
	// Truncated reports messages left out by --turns.
	Truncated int `json:"truncated,omitempty"`
}

// ErrNotFound means no session matched the reference.
var ErrNotFound = errors.New("recall: no such session")

// Ref is the `#n` form of a session id: what every listing prints, what
// `recall show` accepts and what the TUI preview passes. Resolve is the
// one place that reads it back.
func Ref(sessID int64) string { return "#" + strconv.FormatInt(sessID, 10) }

// Resolve finds a session by numeric sess_id (bare or as `#n`), exact or
// prefix native id, or deck session id. Exact matches win over a prefix;
// a prefix shared by two sessions is ambiguous.
func (s *Searcher) Resolve(ctx context.Context, ref string) (SessionRow, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == "#" {
		return SessionRow{}, ErrNotFound
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(ref, "#"), 10, 64)
	if id <= 0 && strings.HasPrefix(ref, "#") {
		return SessionRow{}, ErrNotFound // `#` is only ever the numeric form
	}
	//nolint:gosec // sessionCols is a fixed const; all values are bound via ? args, never interpolated
	rows, err := s.st.R.QueryContext(ctx, `SELECT `+sessionCols+` FROM session s LEFT JOIN card c ON c.sess_id=s.sess_id
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
	exact := func(r SessionRow) bool { return r.SessID == id || r.DeckID == ref || r.NativeID == ref }
	switch {
	case len(found) == 0:
		return SessionRow{}, ErrNotFound
	case len(found) > 1 && !exact(found[0]):
		return SessionRow{}, fmt.Errorf("recall: %q is ambiguous (%s, %s, ...)", ref, found[0].NativeID, found[1].NativeID)
	}
	return found[0], nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Show returns the session with its tool summary, touched files and up to
// turns messages (0: all; negative: none).
func (s *Searcher) Show(ctx context.Context, ref string, turns int) (Detail, error) {
	sess, err := s.Resolve(ctx, ref)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Session: sess}
	if d.Tools, err = s.toolStats(ctx, sess.SessID); err != nil {
		return d, err
	}
	if d.Files, err = s.touchedFiles(ctx, sess.SessID); err != nil {
		return d, err
	}
	if d.Artifacts, err = s.Artifacts(ctx, sess.SessID); err != nil {
		return d, err
	}
	if err := s.st.R.QueryRowContext(ctx, `SELECT count(*) FROM msg WHERE sess_id=?`, sess.SessID).Scan(&d.Session.Messages); err != nil {
		return d, err
	}
	if turns < 0 {
		return d, nil
	}
	if d.Messages, err = s.messages(ctx, sess.SessID, turns); err != nil {
		return d, err
	}
	if turns > 0 && d.Session.Messages > turns {
		d.Truncated = d.Session.Messages - turns
	}
	return d, nil
}

// toolStats summarises the session's tool calls per tool name, busiest first.
func (s *Searcher) toolStats(ctx context.Context, sessID int64) ([]ToolStat, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT name, count(*), sum(is_error), sum(duration_ms), max(arg_digest) FROM tool_call WHERE sess_id=? AND name<>'' GROUP BY name ORDER BY 2 DESC`, sessID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolStat
	for rows.Next() {
		var t ToolStat
		if err := rows.Scan(&t.Name, &t.Calls, &t.Errors, &t.TotalMS, &t.LastDigest); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// touchedFiles lists the paths the session read, wrote or edited, most
// touched first, rendered as "path (op xN, ...)".
func (s *Searcher) touchedFiles(ctx context.Context, sessID int64) ([]string, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT path || ' (' || group_concat(op || ' x' || n, ', ') || ')' FROM file_touch WHERE sess_id=? GROUP BY path ORDER BY sum(n) DESC LIMIT 40`, sessID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// messages decodes the first turns messages in sequence order (0: all).
func (s *Searcher) messages(ctx context.Context, sessID int64, turns int) ([]Message, error) {
	q := `SELECT seq, role, class, ts, tool_name, is_error, nchars, body FROM msg WHERE sess_id=? ORDER BY seq`
	args := []any{sessID}
	if turns > 0 {
		q += ` LIMIT ?`
		args = append(args, turns)
	}
	rows, err := s.st.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var role, class, isErr int
		var body []byte
		if err := rows.Scan(&m.Seq, &role, &class, &m.TS, &m.ToolNames, &isErr, &m.NChars, &body); err != nil {
			return nil, err
		}
		m.Role = roleName(role)
		m.Class = classify.Class(class).String()
		m.IsError = isErr == 1
		text, err := recall.DecompressBody(body)
		if err != nil {
			return nil, err
		}
		m.Text = string(text)
		out = append(out, m)
	}
	return out, rows.Err()
}

func roleName(r int) string {
	switch r {
	case recall.RoleUser:
		return "user"
	case recall.RoleAssistant:
		return "assistant"
	}
	return strconv.Itoa(r)
}

// ---- status --------------------------------------------------------------

// Status is `recall status`.
type Status struct {
	DBPath        string         `json:"db_path"`
	DBBytes       int64          `json:"db_bytes"`
	SchemaVersion string         `json:"schema_version"`
	Sources       map[string]int `json:"sources"`
	SourceBytes   int64          `json:"source_bytes"`
	IndexedBytes  int64          `json:"indexed_bytes"`
	PendingBytes  int64          `json:"pending_bytes"`
	Sessions      int            `json:"sessions"`
	Messages      int            `json:"messages"`
	ToolCalls     int            `json:"tool_calls"`
	Cards         int            `json:"cards"`
	CardFTSRows   int            `json:"card_fts_rows"`
	ByHarness     map[string]int `json:"by_harness"`
	ByProfile     map[string]int `json:"by_profile"`
	ExpiringBytes int64          `json:"expiring_bytes_30d"`
	Tombstones    int            `json:"tombstones"`
	FreePages     int64          `json:"free_pages"`
	LastSweep     int64          `json:"last_sweep,omitempty"`
}

var sourceStateNames = map[int]string{recall.SourceOK: "ok", recall.SourcePartial: "partial", recall.SourceError: "error", recall.SourceMissing: "missing", recall.SourceQuarantined: "quarantined"}

// Status gathers counts and sizes. It is cheap: counts over small tables
// and one sum over source.
func (s *Searcher) Status(ctx context.Context) (Status, error) {
	st := Status{DBPath: s.st.Path, DBBytes: store.FileSize(s.st.Path), Sources: map[string]int{}, ByHarness: map[string]int{}, ByProfile: map[string]int{}}
	db := s.st.R
	_ = db.QueryRowContext(ctx, `SELECT v FROM meta WHERE k='schema_version'`).Scan(&st.SchemaVersion)
	rows, err := db.QueryContext(ctx, `SELECT state, count(*), sum(size), sum(min(parsed_to, size)), sum(max(size - parsed_to, 0)) FROM source GROUP BY state`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var state, n int
		var size, parsed, pending sql.NullInt64
		if err := rows.Scan(&state, &n, &size, &parsed, &pending); err != nil {
			rows.Close()
			return st, err
		}
		name := sourceStateNames[state]
		if name == "" {
			name = strconv.Itoa(state)
		}
		st.Sources[name] = n
		st.SourceBytes += size.Int64
		if state != recall.SourceMissing && state != recall.SourceQuarantined {
			st.IndexedBytes += parsed.Int64
			st.PendingBytes += pending.Int64
		}
	}
	rows.Close()
	counts := []struct {
		q   string
		dst *int
	}{
		{`SELECT count(*) FROM session`, &st.Sessions},
		{`SELECT count(*) FROM msg`, &st.Messages},
		{`SELECT count(*) FROM tool_call`, &st.ToolCalls},
		{`SELECT count(*) FROM card`, &st.Cards},
		{`SELECT count(*) FROM card_fts`, &st.CardFTSRows},
		{`SELECT count(*) FROM tombstone`, &st.Tombstones},
	}
	for _, c := range counts {
		if err := db.QueryRowContext(ctx, c.q).Scan(c.dst); err != nil {
			return st, err
		}
	}
	_ = db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&st.FreePages)
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(max(last_seen),0) FROM source`).Scan(&st.LastSweep)
	for _, g := range []struct {
		col string
		dst map[string]int
	}{{"harness", st.ByHarness}, {"profile", st.ByProfile}} {
		//nolint:gosec // g.col comes only from the fixed "harness"/"profile" literals above, never user input
		rows, err := db.QueryContext(ctx, `SELECT `+g.col+`, count(*) FROM session GROUP BY 1`)
		if err != nil {
			return st, err
		}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				rows.Close()
				return st, err
			}
			g.dst[k] = n
		}
		rows.Close()
	}
	// Bytes indexed from sources whose profile retention will delete the
	// file within 30 days: the index is what survives.
	cut := time.Now().Add(30 * 24 * time.Hour).UnixNano()
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(sum(size),0) FROM source WHERE retention_d>0 AND state IN (0,1) AND mtime_ns + retention_d*86400*1000000000 < ?`, cut).Scan(&st.ExpiringBytes)
	return st, nil
}
