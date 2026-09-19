package recall

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// openTestDB opens a fresh SQLite file with the pragmas recall.db will use.
func openTestDB(t testing.TB) *sql.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "recall.db") + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA cache_size=-8000", "PRAGMA mmap_size=0"} {
		if _, err := db.Exec(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	return db
}

func mustExec(t testing.TB, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", firstLine(s), err)
		}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func count(t testing.TB, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// The exact DDL of FINAL-DESIGN section 4 must create against the pure-Go
// driver the binary ships with, and the FTS5 facts the design relies on must
// hold there too. A failure here is a build failure, not a runtime surprise.
func TestSchemaDDL_CreatesAgainstModerncSQLite(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, MsgFTSDDL, CardDDL, CardFTSDDL)
	mustExec(t, db, CardTriggerDDL...)

	var version string
	if err := db.QueryRow("SELECT sqlite_version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("sqlite_version() = %s", version)

	// Fact 1: contentless_delete=1 and columnsize=0 are mutually exclusive;
	// the design dropped columnsize=0 for that reason and must stay dropped.
	bad := strings.Replace(MsgFTSDDL, "contentless_delete=1,", "contentless_delete=1, columnsize=0,", 1)
	bad = strings.Replace(bad, "msg_fts", "msg_fts_bad", 1)
	if _, err := db.Exec(bad); err == nil {
		t.Fatal("columnsize=0 together with contentless_delete=1 was accepted; the design's size budget assumes the %_docsize table exists")
	} else if !strings.Contains(err.Error(), "columnsize=0") {
		t.Fatalf("unexpected error for columnsize=0 variant: %v", err)
	}
}

// msg_fts is a membership filter only: it cannot rank, snippet, run phrase
// queries or be rebuilt. Each limitation is asserted so that a driver upgrade
// that lifts one is noticed, and so that no query path assumes otherwise.
func TestMsgFTS_IsMembershipOnly(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, MsgFTSDDL)
	mustExec(t, db,
		`INSERT INTO msg_fts(rowid, body) VALUES (1, 'root cause was clock skew in the auth test')`,
		`INSERT INTO msg_fts(rowid, body) VALUES (2, 'unrelated telegram poller restart')`,
	)
	if n := count(t, db, `SELECT count(*) FROM msg_fts WHERE msg_fts MATCH 'clock AND skew'`); n != 1 {
		t.Fatalf("AND membership = %d rows; want 1", n)
	}
	if n := count(t, db, `SELECT count(*) FROM msg_fts WHERE msg_fts MATCH 'auth'`); n != 1 {
		t.Fatalf("term membership = %d; want 1", n)
	}
	if _, err := db.Query(`SELECT rowid FROM msg_fts WHERE msg_fts MATCH '"clock skew"'`); err == nil {
		t.Fatal("phrase query on detail=none succeeded; the phrase verifier must decompress msg.body instead")
	} else if !strings.Contains(err.Error(), "phrase queries are not supported") {
		t.Fatalf("unexpected phrase error: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO msg_fts(msg_fts) VALUES('rebuild')`); err == nil {
		t.Fatal("'rebuild' on a contentless table succeeded; the design assumes per-rowid delete is the only mechanism")
	}
	// Per-rowid delete is the mechanism contentless_delete=1 buys.
	mustExec(t, db, `DELETE FROM msg_fts WHERE rowid = 1`)
	if n := count(t, db, `SELECT count(*) FROM msg_fts WHERE msg_fts MATCH 'auth'`); n != 0 {
		t.Fatalf("after DELETE, membership = %d; want 0", n)
	}
}

// The three card_fts triggers: a match on insert, the stale posting dropped
// on update, and phrase + snippet() working because detail is full.
func TestCardFTS_TriggersKeepIndexInStep(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, CardDDL, CardFTSDDL)
	mustExec(t, db, CardTriggerDDL...)

	mustExec(t, db, `INSERT INTO card(sess_id,title,hints,tags,summary,preview)
		VALUES (7,'auth-fix','purpose=fix flaky auth test ticket=SB-412','auth flaky','root cause was clock skew','')`)

	// Insert trigger: the row is searchable immediately.
	if n := count(t, db, `SELECT count(*) FROM card_fts WHERE card_fts MATCH 'flaky'`); n != 1 {
		t.Fatalf("match on insert = %d; want 1 (card_ai trigger missing?)", n)
	}
	// tokenchars keeps the ticket id as one term. The FTS5 query parser
	// still reads a bare '-' as an operator, so a term containing tokenchars
	// must be quoted; the query layer has to do that for every user term.
	if _, err := db.Query(`SELECT rowid FROM card_fts WHERE card_fts MATCH 'SB-412'`); err == nil {
		t.Fatal("bare SB-412 parsed as a query; the query layer must quote terms with tokenchars")
	}
	if n := count(t, db, `SELECT count(*) FROM card_fts WHERE card_fts MATCH '"SB-412"'`); n != 1 {
		t.Fatalf("ticket term match = %d; want 1", n)
	}

	// Update trigger: the old posting is gone, the new one present, and the
	// rowid still maps to the card.
	mustExec(t, db, `UPDATE card SET hints='purpose=fix flaky auth test ticket=SB-413', tags='auth clock-skew' WHERE sess_id=7`)
	if n := count(t, db, `SELECT count(*) FROM card_fts WHERE card_fts MATCH '"SB-412"'`); n != 0 {
		t.Fatalf("stale posting after update = %d; want 0 (card_au trigger missing?)", n)
	}
	if n := count(t, db, `SELECT count(*) FROM card_fts WHERE card_fts MATCH '"SB-413"'`); n != 1 {
		t.Fatalf("new posting after update = %d; want 1", n)
	}
	var sessID int
	if err := db.QueryRow(`SELECT rowid FROM card_fts WHERE card_fts MATCH '"clock-skew"'`).Scan(&sessID); err != nil || sessID != 7 {
		t.Fatalf("rowid after update = %d, err %v; want 7", sessID, err)
	}

	// Phrase and snippet() work on the ranked surface, and bm25 is a real
	// (negative) score rather than msg_fts's -0.
	var snippet string
	var score float64
	err := db.QueryRow(`SELECT snippet(card_fts, 3, '[', ']', '…', 8), bm25(card_fts)
		FROM card_fts WHERE card_fts MATCH '"clock skew"'`).Scan(&snippet, &score)
	if err != nil {
		t.Fatalf("phrase + snippet on card_fts: %v", err)
	}
	if !strings.Contains(snippet, "[clock skew]") || score >= 0 {
		t.Fatalf("snippet = %q, bm25 = %v; want highlighted phrase and a negative rank", snippet, score)
	}

	// Delete trigger: the posting goes with the row.
	mustExec(t, db, `DELETE FROM card WHERE sess_id=7`)
	if n := count(t, db, `SELECT count(*) FROM card_fts WHERE card_fts MATCH 'auth'`); n != 0 {
		t.Fatalf("posting after delete = %d; want 0 (card_ad trigger missing?)", n)
	}
	// `recall status` compares these two counts; they must agree.
	if a, b := count(t, db, `SELECT count(*) FROM card`), count(t, db, `SELECT count(*) FROM card_fts`); a != b {
		t.Fatalf("card=%d card_fts=%d; want equal", a, b)
	}
}
