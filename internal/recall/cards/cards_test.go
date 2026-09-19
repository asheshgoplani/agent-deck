package cards

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/enrich"
	"github.com/asheshgoplani/agent-deck/internal/recall/ingest"
	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
)

const secret = "the migration secret nobody must export"

// indexed builds a store over a small Claude corpus whose first prompt
// carries a known secret, sweeps it (which drains the classifiers) and
// returns the store and the transcript path.
func indexed(t *testing.T) (*store.Store, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "claude")
	if _, err := testcorpus.Generate(root, testcorpus.Options{Files: 2, Seed: 5}); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(root, "projects", "-Users-x-app")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "dddddddd-0000-4000-8000-00000000000d"
	path := filepath.Join(proj, id+".jsonl")
	lines := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q},"uuid":"d-1","timestamp":"2026-09-19T10:00:00Z","sessionId":%q,"cwd":"/Users/x/app"}`+"\n"+
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Noted."}],"usage":{"input_tokens":1,"output_tokens":1}},"uuid":"d-2","timestamp":"2026-09-19T10:00:01Z","sessionId":%q,"cwd":"/Users/x/app"}`+"\n",
		secret, id, id)
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(base, "data", "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	roots := []reader.Root{{Harness: reader.HarnessClaude, Profile: "personal", Dir: root}}
	if _, err := ingest.New(st, ingest.Options{Roots: roots}).Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st, path
}

// TestExport_NeverEmitsOffsetsSpansBodiesOrPaths is the boundary
// assertion of design 10: no line of the stream carries an offset, a span,
// a body, a path, or the text of a message beyond the 200-character
// preview of the first prompt.
func TestExport_NeverEmitsOffsetsSpansBodiesOrPaths(t *testing.T) {
	st, path := indexed(t)
	var buf bytes.Buffer
	tr, err := Export(&buf, st, time.Time{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if tr.Sessions != 3 || tr.Cards != 3 || tr.Artifacts != 3*len(enrich.CheapKinds) {
		t.Fatalf("trailer: %+v", tr)
	}
	home := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	for i, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		for _, k := range ForbiddenKeys {
			if _, ok := obj[k]; ok {
				t.Errorf("line %d (%s) carries forbidden key %q", i, obj["kind"], k)
			}
		}
		if strings.Contains(line, home) || strings.Contains(line, "/projects/") || strings.Contains(line, ".jsonl") {
			t.Errorf("line %d carries a path: %s", i, line)
		}
		if strings.Contains(line, "Noted.") {
			t.Errorf("line %d carries an assistant body: %s", i, line)
		}
	}
	// The one place conversation text appears is the preview, clipped.
	if !strings.Contains(buf.String(), `"preview":"the migration secret`) {
		t.Fatalf("the card preview is the documented 200-char exception; got:\n%s", buf.String())
	}
	var hdr Header
	if err := json.Unmarshal([]byte(strings.SplitN(buf.String(), "\n", 2)[0]), &hdr); err != nil || hdr.Kind != KindHeader || len(hdr.HostUID) != 32 {
		t.Fatalf("header: %+v %v", hdr, err)
	}
	uid, _ := st.HostUID()
	if hdr.HostUID != uid {
		t.Fatal("header host_uid must be the store's own")
	}
}

func TestImport_RefusalsAndDigestOnlyRows(t *testing.T) {
	src, _ := indexed(t)
	var stream bytes.Buffer
	if _, err := Export(&stream, src, time.Time{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	dst, err := store.Open(filepath.Join(t.TempDir(), "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dst.Close)
	now := time.Now()

	// No explicit host: refused.
	if _, err := Import(bytes.NewReader(stream.Bytes()), dst, "  ", now); !errors.Is(err, ErrNoHost) {
		t.Fatalf("no host: %v", err)
	}
	// No header (a truncated stream starting mid-way): refused.
	body := strings.SplitN(stream.String(), "\n", 2)[1]
	if _, err := Import(strings.NewReader(body), dst, "lab", now); !errors.Is(err, ErrNoHeader) {
		t.Fatalf("no header: %v", err)
	}
	// A header with an empty host_uid: refused (an empty uid must never
	// read as local).
	if _, err := Import(strings.NewReader(`{"kind":"header","version":1,"host_uid":""}`+"\n"+body), dst, "lab", now); !errors.Is(err, ErrNoHeader) {
		t.Fatalf("empty uid: %v", err)
	}
	if _, err := Import(strings.NewReader(`{"kind":"header","version":1,"host_uid":"local"}`+"\n"+body), dst, "lab", now); !errors.Is(err, ErrOwnCards) {
		t.Fatalf("uid 'local': %v", err)
	}
	// Our own export back into ourselves: refused.
	var own bytes.Buffer
	if _, err := Export(&own, dst, time.Time{}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(bytes.NewReader(own.Bytes()), dst, "me", now); !errors.Is(err, ErrOwnCards) {
		t.Fatalf("own cards: %v", err)
	}
	// A newer format: refused.
	if _, err := Import(strings.NewReader(`{"kind":"header","version":99,"host_uid":"abcd"}`+"\n"), dst, "lab", now); !errors.Is(err, ErrVersion) {
		t.Fatalf("version: %v", err)
	}

	// The real import lands as digest-only rows under the alias.
	res, err := Import(bytes.NewReader(stream.Bytes()), dst, "lab", now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sessions != 3 || res.Cards != 3 || res.Artifacts != 3*len(enrich.CheapKinds) || res.Skipped != 0 {
		t.Fatalf("import: %+v", res)
	}
	var n, digest, local int
	_ = dst.R.QueryRow(`SELECT count(*), sum(digest_only) FROM session WHERE host_uid=?`, res.HostUID).Scan(&n, &digest)
	_ = dst.R.QueryRow(`SELECT count(*) FROM session WHERE host_uid='local'`).Scan(&local)
	if n != 3 || digest != 3 || local != 0 {
		t.Fatalf("rows: %d sessions, %d digest_only, %d local", n, digest, local)
	}
	var isLocal int
	if err := dst.R.QueryRow(`SELECT is_local FROM host WHERE host_uid=?`, res.HostUID).Scan(&isLocal); err != nil || isLocal != 0 {
		t.Fatalf("host row: is_local=%d %v", isLocal, err)
	}
	// Listed and labelled; searchable by card; the excerpt tier refuses.
	q := query.New(dst, "")
	rows, err := q.Sessions(context.Background(), query.Filters{}, 10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("sessions: %d %v", len(rows), err)
	}
	for _, r := range rows {
		if !r.DigestOnly || r.HostUID != res.HostUID || r.Path != "" {
			t.Fatalf("pulled row not labelled: %+v", r)
		}
	}
	hits, err := q.Search(context.Background(), query.SearchOptions{Query: "migration secret"})
	if err != nil || len(hits.Hits) != 1 || !hits.Hits[0].CardHit || hits.Hits[0].BodyHits != 0 {
		t.Fatalf("card search over pulled rows: %+v %v", hits, err)
	}
	brief, err := q.Context(context.Background(), "dddddddd", query.TierBrief, 0)
	if err != nil || !strings.Contains(brief.Text, "from another machine (card only)") || len(brief.Artifacts) != len(enrich.CheapKinds) {
		t.Fatalf("brief over a pulled card: %v\n%s", err, brief.Text)
	}
	if _, err := q.Context(context.Background(), "dddddddd", query.TierExcerpt, 0); !errors.Is(err, query.ErrDigestOnly) {
		t.Fatalf("excerpt over a pulled card must stop: %v", err)
	}

	// A second import under the same alias is idempotent.
	if res2, err := Import(bytes.NewReader(stream.Bytes()), dst, "lab", now.Add(time.Minute)); err != nil || res2.Sessions != 3 {
		t.Fatalf("re-import: %+v %v", res2, err)
	}
	_ = dst.R.QueryRow(`SELECT count(*) FROM session`).Scan(&n)
	if n != 3 {
		t.Fatalf("re-import duplicated rows: %d", n)
	}
	if cursor, err := Cursor(dst, "lab"); err != nil || cursor == 0 {
		t.Fatalf("cursor: %d %v", cursor, err)
	}

	// The same alias later carrying another machine's uid: refused.
	other := strings.Replace(stream.String(), res.HostUID, strings.Repeat("f", 32), 1)
	if _, err := Import(strings.NewReader(other), dst, "lab", now); !errors.Is(err, ErrHostUIDChanged) {
		t.Fatalf("host_uid mismatch: %v", err)
	}
	// The same machine under a second alias: refused (would double-import).
	if _, err := Import(bytes.NewReader(stream.Bytes()), dst, "lab2", now); !errors.Is(err, ErrAliasTaken) {
		t.Fatalf("alias taken: %v", err)
	}
	// And nothing local was ever written.
	_ = dst.R.QueryRow(`SELECT count(*) FROM msg`).Scan(&n)
	if n != 0 {
		t.Fatalf("an import wrote %d message rows", n)
	}
}
