// Package cards is remote card sync (design 10, mode 2): the derived,
// body-free view of an index that one machine exports and another pulls.
// Export writes NDJSON of session, card, artifact and edge rows and never
// a message body, a byte offset, a span or a filesystem path; Import
// writes them under an explicit host and refuses anything that could fail
// open toward local disk: a stream without a host uid, an alias whose
// recorded uid disagrees, a uid already known under another alias, or
// this machine's own cards.
package cards

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
)

// FormatVersion is the export format; an import refuses a newer one.
const FormatVersion = 1

// PreviewChars is the card preview clip on export.
const PreviewChars = 200

// Line kinds.
const (
	KindHeader   = "header"
	KindSession  = "session"
	KindCard     = "card"
	KindArtifact = "artifact"
	KindEdge     = "edge"
	KindTrailer  = "trailer"
)

// ForbiddenKeys never appear in an export line: the boundary the
// TestExport_NeverEmitsOffsetsSpansBodiesOrPaths test asserts.
var ForbiddenKeys = []string{"path", "cwd", "project_key", "rec_off", "rec_len", "spanv", "body_zstd", "msg_id", "src_id"}

// Header opens a stream.
type Header struct {
	Kind       string `json:"kind"`
	Version    int    `json:"version"`
	HostUID    string `json:"host_uid"`
	ExportedAt int64  `json:"exported_at"`
	Since      int64  `json:"since,omitempty"`
}

// Session is one session row without paths or counters that need bodies.
type Session struct {
	Kind       string `json:"kind"`
	Harness    string `json:"harness"`
	Profile    string `json:"profile,omitempty"`
	NativeID   string `json:"native_id"`
	DeckID     string `json:"deck_id,omitempty"`
	Repo       string `json:"repo,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Title      string `json:"title,omitempty"`
	TitleSrc   string `json:"title_src,omitempty"`
	StartedAt  int64  `json:"started_at,omitempty"`
	EndedAt    int64  `json:"ended_at,omitempty"`
	Turns      int    `json:"turns"`
	ToolCalls  int    `json:"tool_calls"`
	Errors     int    `json:"errors"`
	Interrupts int    `json:"interrupts"`
	Compacts   int    `json:"compacts"`
	Model      string `json:"model,omitempty"`
	Sidechain  bool   `json:"sidechain,omitempty"`
	DerivedRev int64  `json:"derived_rev"`
}

// Card is the ranked surface of one session.
type Card struct {
	Kind     string `json:"kind"`
	NativeID string `json:"native_id"`
	Harness  string `json:"harness"`
	Profile  string `json:"profile,omitempty"`
	Title    string `json:"title,omitempty"`
	Hints    string `json:"hints,omitempty"`
	Tags     string `json:"tags,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Preview  string `json:"preview,omitempty"`
}

// Artifact is one derived row.
type Artifact struct {
	Kind        string  `json:"kind"`
	NativeID    string  `json:"native_id"`
	Harness     string  `json:"harness"`
	Profile     string  `json:"profile,omitempty"`
	ArtKind     string  `json:"art_kind"`
	Body        string  `json:"body"`
	BodyJSON    string  `json:"body_json,omitempty"`
	Producer    string  `json:"producer"`
	ProducerVer string  `json:"producer_ver"`
	Confidence  float64 `json:"confidence"`
	InputRev    int64   `json:"input_rev"`
	CreatedAt   int64   `json:"created_at"`
}

// Edge links two sessions by native id.
type Edge struct {
	Kind       string  `json:"kind"`
	FromNative string  `json:"from_native"`
	ToNative   string  `json:"to_native"`
	Harness    string  `json:"harness"`
	Profile    string  `json:"profile,omitempty"`
	EdgeKind   string  `json:"edge_kind"`
	Weight     float64 `json:"weight"`
}

// Trailer closes a stream with its counts.
type Trailer struct {
	Kind      string `json:"kind"`
	Sessions  int    `json:"sessions"`
	Cards     int    `json:"cards"`
	Artifacts int    `json:"artifacts"`
	Edges     int    `json:"edges"`
}

// Export writes this machine's local sessions (never re-exported remote
// cards) active at or after since as NDJSON.
func Export(w io.Writer, st *store.Store, since time.Time, now time.Time) (Trailer, error) {
	uid, err := st.HostUID()
	if err != nil {
		return Trailer{}, err
	}
	enc := json.NewEncoder(w)
	var sinceTS int64
	if !since.IsZero() {
		sinceTS = since.Unix()
	}
	if err := enc.Encode(Header{Kind: KindHeader, Version: FormatVersion, HostUID: uid, ExportedAt: now.Unix(), Since: sinceTS}); err != nil {
		return Trailer{}, err
	}
	tr := Trailer{Kind: KindTrailer}
	rows, err := st.R.Query(`SELECT s.sess_id, s.harness, s.profile, s.native_id, s.deck_id, COALESCE(s.repo,''), COALESCE(s.branch,''), COALESCE(s.title,''), COALESCE(s.title_src,''),
		COALESCE(s.started_at,0), COALESCE(s.ended_at,0), s.turns, s.tool_calls, s.errors, s.interrupts, s.compacts, COALESCE(s.model,''), s.is_sidechain, s.derived_rev,
		c.title, c.hints, c.tags, c.summary, c.preview
		FROM session s LEFT JOIN card c ON c.sess_id=s.sess_id
		WHERE s.host_uid=? AND s.digest_only=0 AND COALESCE(s.ended_at, s.started_at, 0) >= ? ORDER BY s.sess_id`, store.LocalHostUID, sinceTS)
	if err != nil {
		return tr, err
	}
	defer rows.Close()
	type sessRef struct {
		id                       int64
		harness, profile, native string
	}
	var refs []sessRef
	for rows.Next() {
		var s Session
		var id int64
		var sidechain int
		var cTitle, cHints, cTags, cSummary, cPreview *string
		if err := rows.Scan(&id, &s.Harness, &s.Profile, &s.NativeID, &s.DeckID, &s.Repo, &s.Branch, &s.Title, &s.TitleSrc,
			&s.StartedAt, &s.EndedAt, &s.Turns, &s.ToolCalls, &s.Errors, &s.Interrupts, &s.Compacts, &s.Model, &sidechain, &s.DerivedRev,
			&cTitle, &cHints, &cTags, &cSummary, &cPreview); err != nil {
			return tr, err
		}
		s.Kind, s.Sidechain = KindSession, sidechain == 1
		if err := enc.Encode(s); err != nil {
			return tr, err
		}
		tr.Sessions++
		if cTitle != nil || cHints != nil || cTags != nil || cSummary != nil || cPreview != nil {
			c := Card{Kind: KindCard, NativeID: s.NativeID, Harness: s.Harness, Profile: s.Profile,
				Title: deref(cTitle), Hints: deref(cHints), Tags: deref(cTags), Summary: deref(cSummary), Preview: recall.ClipBytes(deref(cPreview), PreviewChars)}
			if err := enc.Encode(c); err != nil {
				return tr, err
			}
			tr.Cards++
		}
		refs = append(refs, sessRef{id, s.Harness, s.Profile, s.NativeID})
	}
	if err := rows.Err(); err != nil {
		return tr, err
	}
	for _, r := range refs {
		arts, err := st.R.Query(`SELECT kind, body, body_json, producer, producer_ver, confidence, input_rev, created_at FROM artifact WHERE sess_id=? ORDER BY kind`, r.id)
		if err != nil {
			return tr, err
		}
		for arts.Next() {
			a := Artifact{Kind: KindArtifact, NativeID: r.native, Harness: r.harness, Profile: r.profile}
			if err := arts.Scan(&a.ArtKind, &a.Body, &a.BodyJSON, &a.Producer, &a.ProducerVer, &a.Confidence, &a.InputRev, &a.CreatedAt); err != nil {
				arts.Close()
				return tr, err
			}
			if err := enc.Encode(a); err != nil {
				arts.Close()
				return tr, err
			}
			tr.Artifacts++
		}
		arts.Close()
		edges, err := st.R.Query(`SELECT t.native_id, e.kind, e.weight FROM conv_edge e JOIN session t ON t.sess_id=e.to_sess WHERE e.from_sess=?`, r.id)
		if err != nil {
			return tr, err
		}
		for edges.Next() {
			e := Edge{Kind: KindEdge, FromNative: r.native, Harness: r.harness, Profile: r.profile}
			if err := edges.Scan(&e.ToNative, &e.EdgeKind, &e.Weight); err != nil {
				edges.Close()
				return tr, err
			}
			if err := enc.Encode(e); err != nil {
				edges.Close()
				return tr, err
			}
			tr.Edges++
		}
		edges.Close()
	}
	return tr, enc.Encode(tr)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Import errors, each a refusal that keeps the boundary closed.
var (
	ErrNoHost         = errors.New("recall import: an explicit --host alias is required; cards are never imported under an empty host")
	ErrNoHeader       = errors.New("recall import: the stream carries no header with a host_uid; refused")
	ErrOwnCards       = errors.New("recall import: the stream was exported by this machine; refused")
	ErrVersion        = errors.New("recall import: the stream is a newer format than this agent-deck reads")
	ErrHostUIDChanged = errors.New("recall import: host_uid mismatch")
	ErrAliasTaken     = errors.New("recall import: host_uid already known under another alias")
)

// ImportResult reports what Import wrote.
type ImportResult struct {
	HostUID   string `json:"host_uid"`
	Alias     string `json:"alias"`
	Sessions  int    `json:"sessions"`
	Cards     int    `json:"cards"`
	Artifacts int    `json:"artifacts"`
	Edges     int    `json:"edges"`
	Skipped   int    `json:"skipped"`
}

// maxLine bounds one NDJSON line.
const maxLine = 4 << 20

// Import reads an Export stream and writes it under alias. Rows land with
// digest_only=1 and the stream's host_uid; local rows are never touched.
func Import(r io.Reader, st *store.Store, alias string, now time.Time) (ImportResult, error) {
	alias = strings.TrimSpace(alias)
	res := ImportResult{Alias: alias}
	if alias == "" {
		return res, ErrNoHost
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return res, err
		}
		return res, ErrNoHeader
	}
	var h Header
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil || h.Kind != KindHeader || strings.TrimSpace(h.HostUID) == "" {
		return res, ErrNoHeader
	}
	if h.Version > FormatVersion {
		return res, ErrVersion
	}
	own, err := st.HostUID()
	if err != nil {
		return res, err
	}
	if h.HostUID == own || h.HostUID == store.LocalHostUID {
		return res, ErrOwnCards
	}
	res.HostUID = h.HostUID
	if err := checkAlias(st, alias, h.HostUID); err != nil {
		return res, err
	}
	tx, err := st.W.Begin()
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO host(host_uid, alias, is_local, last_seen) VALUES (?, ?, 0, ?)
		ON CONFLICT(host_uid) DO UPDATE SET alias=excluded.alias, last_seen=excluded.last_seen`, h.HostUID, alias, now.Unix()); err != nil {
		return res, err
	}
	sessID := func(harness, profile, native string) (int64, error) {
		var id int64
		err := tx.QueryRow(`SELECT sess_id FROM session WHERE host_uid=? AND harness=? AND profile=? AND native_id=?`, h.HostUID, harness, profile, native).Scan(&id)
		return id, err
	}
	for sc.Scan() {
		line := sc.Bytes()
		var probe struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			res.Skipped++
			continue
		}
		switch probe.Kind {
		case KindSession:
			var s Session
			if err := json.Unmarshal(line, &s); err != nil || s.NativeID == "" || s.Harness == "" {
				res.Skipped++
				continue
			}
			if _, err := tx.Exec(`INSERT INTO session(host_uid, harness, profile, native_id, deck_id, repo, branch, title, title_src, started_at, ended_at,
				turns, tool_calls, errors, interrupts, compacts, model, is_sidechain, text_tier, digest_only, derived_rev)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'none', 1, ?)
				ON CONFLICT(host_uid, harness, profile, native_id) DO UPDATE SET deck_id=excluded.deck_id, repo=excluded.repo, branch=excluded.branch,
				title=excluded.title, title_src=excluded.title_src, started_at=excluded.started_at, ended_at=excluded.ended_at, turns=excluded.turns,
				tool_calls=excluded.tool_calls, errors=excluded.errors, interrupts=excluded.interrupts, compacts=excluded.compacts, model=excluded.model,
				is_sidechain=excluded.is_sidechain, digest_only=1, derived_rev=excluded.derived_rev`,
				h.HostUID, s.Harness, s.Profile, s.NativeID, s.DeckID, s.Repo, s.Branch, s.Title, s.TitleSrc, s.StartedAt, s.EndedAt,
				s.Turns, s.ToolCalls, s.Errors, s.Interrupts, s.Compacts, s.Model, boolInt(s.Sidechain), s.DerivedRev); err != nil {
				return res, fmt.Errorf("recall import: session %s: %w", s.NativeID, err)
			}
			res.Sessions++
		case KindCard:
			var c Card
			if err := json.Unmarshal(line, &c); err != nil {
				res.Skipped++
				continue
			}
			id, err := sessID(c.Harness, c.Profile, c.NativeID)
			if err != nil {
				res.Skipped++
				continue
			}
			if _, err := tx.Exec(`INSERT INTO card(sess_id, title, hints, tags, summary, preview) VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT(sess_id) DO UPDATE SET title=excluded.title, hints=excluded.hints, tags=excluded.tags, summary=excluded.summary, preview=excluded.preview`,
				id, c.Title, c.Hints, c.Tags, c.Summary, recall.ClipBytes(c.Preview, PreviewChars)); err != nil {
				return res, err
			}
			res.Cards++
		case KindArtifact:
			var a Artifact
			if err := json.Unmarshal(line, &a); err != nil || a.ArtKind == "" || a.Producer == "" {
				res.Skipped++
				continue
			}
			id, err := sessID(a.Harness, a.Profile, a.NativeID)
			if err != nil {
				res.Skipped++
				continue
			}
			if _, err := tx.Exec(`INSERT INTO artifact(sess_id, kind, body, body_json, producer, producer_ver, confidence, input_rev, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(sess_id, kind, producer, producer_ver) DO UPDATE SET body=excluded.body, body_json=excluded.body_json,
				confidence=excluded.confidence, input_rev=excluded.input_rev, created_at=excluded.created_at`,
				id, a.ArtKind, a.Body, a.BodyJSON, a.Producer, a.ProducerVer, a.Confidence, a.InputRev, a.CreatedAt); err != nil {
				return res, err
			}
			res.Artifacts++
		case KindEdge:
			var e Edge
			if err := json.Unmarshal(line, &e); err != nil {
				res.Skipped++
				continue
			}
			from, err1 := sessID(e.Harness, e.Profile, e.FromNative)
			to, err2 := sessID(e.Harness, e.Profile, e.ToNative)
			if err1 != nil || err2 != nil {
				res.Skipped++
				continue
			}
			if _, err := tx.Exec(`INSERT INTO conv_edge(from_sess, to_sess, kind, weight, created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
				from, to, e.EdgeKind, e.Weight, now.Unix()); err != nil {
				return res, err
			}
			res.Edges++
		case KindTrailer, KindHeader:
		default:
			res.Skipped++
		}
	}
	if err := sc.Err(); err != nil {
		return res, err
	}
	if _, err := tx.Exec(`INSERT INTO remote_sync(host_uid, alias, last_pull, cursor, sessions, status, err) VALUES (?, ?, ?, ?, ?, 'ok', '')
		ON CONFLICT(host_uid) DO UPDATE SET alias=excluded.alias, last_pull=excluded.last_pull, cursor=excluded.cursor, sessions=excluded.sessions, status='ok', err=''`,
		h.HostUID, alias, now.Unix(), fmt.Sprint(h.ExportedAt), res.Sessions); err != nil {
		return res, err
	}
	return res, tx.Commit()
}

// checkAlias refuses an alias recorded under another uid and a uid
// recorded under another alias.
func checkAlias(st *store.Store, alias, uid string) error {
	var recorded string
	err := st.W.QueryRow(`SELECT host_uid FROM remote_sync WHERE alias=?`, alias).Scan(&recorded)
	if err == nil && recorded != uid {
		return fmt.Errorf("%w: alias %q is recorded as host %s but the stream was exported by host %s (a renamed or repointed remote; remove the old sync row or use another alias)", ErrHostUIDChanged, alias, recorded, uid)
	}
	var other string
	err = st.W.QueryRow(`SELECT alias FROM remote_sync WHERE host_uid=? AND alias<>?`, uid, alias).Scan(&other)
	if err == nil && other != "" {
		return fmt.Errorf("%w: host %s is already imported as %q; importing it again as %q would double every card", ErrAliasTaken, uid, other, alias)
	}
	return nil
}

// Cursor returns the exported_at of the last pull from alias (0: none).
func Cursor(st *store.Store, alias string) (int64, error) {
	var cursor string
	err := st.R.QueryRow(`SELECT COALESCE(cursor,'') FROM remote_sync WHERE alias=?`, alias).Scan(&cursor)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	var ts int64
	_, _ = fmt.Sscan(cursor, &ts)
	return ts, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
