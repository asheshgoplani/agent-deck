package recall

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Phase 1 measurements (FINAL-DESIGN section 6, "Time and memory"): the
// design's backfill estimate rests on two numbers it could only extrapolate,
// a single-threaded Python parse rate (212 MB/s) and an FTS5 bulk-insert
// timing taken through C sqlite3 (0.54 s for 33,458 messages). These
// benchmarks measure both in Go against the driver the binary ships with.
//
// Run:
//
//	go test ./internal/recall/ -run '^$' -bench . -benchtime 1x
//
// The 400 real transcript files the design measured are not available in
// the CI sandbox, so the default corpus is generated with the same record
// shape (user/assistant content arrays, tool_use/tool_result blocks, and the
// attachment / file-history-snapshot / progress noise that is ~41% of bytes).
// Point RECALL_BENCH_CORPUS at a directory of real Claude *.jsonl files to
// measure those instead; RECALL_BENCH_CORPUS_MB sizes the generated corpus
// (default 24) and RECALL_BENCH_ROWS the FTS insert count (default 33458).

const (
	defaultCorpusMB  = 24
	defaultFTSRows   = 33458
	maxJSONLLineSize = 4 << 20
)

// ---- Corpus --------------------------------------------------------------

var (
	corpusOnce  sync.Once
	corpusFiles []string
	corpusBytes int64
	corpusErr   error
)

// benchCorpus returns the transcript files to parse and their total size.
func benchCorpus(b *testing.B) ([]string, int64) {
	b.Helper()
	corpusOnce.Do(func() {
		if dir := os.Getenv("RECALL_BENCH_CORPUS"); dir != "" {
			corpusFiles, corpusBytes, corpusErr = listJSONL(dir)
			return
		}
		mb := defaultCorpusMB
		if v, err := strconv.Atoi(os.Getenv("RECALL_BENCH_CORPUS_MB")); err == nil && v > 0 {
			mb = v
		}
		dir, err := os.MkdirTemp("", "recall-bench-corpus-*")
		if err != nil {
			corpusErr = err
			return
		}
		corpusErr = generateCorpus(dir, int64(mb)<<20, 20260919)
		if corpusErr == nil {
			corpusFiles, corpusBytes, corpusErr = listJSONL(dir)
		}
	})
	if corpusErr != nil {
		b.Fatalf("corpus: %v", corpusErr)
	}
	if len(corpusFiles) == 0 {
		b.Fatal("corpus: no *.jsonl files")
	}
	return corpusFiles, corpusBytes
}

func listJSONL(dir string) ([]string, int64, error) {
	var files []string
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Mirror the reader's exclusions: tool results and workflows are
			// never opened.
			if name := d.Name(); name == "tool-results" || name == "workflows" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".jsonl" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, path)
		total += info.Size()
		return nil
	})
	return files, total, err
}

// vocabulary is a fixed pseudo-English + identifier vocabulary so FTS5 sees
// a realistic term distribution rather than random bytes.
var vocabulary = func() []string {
	base := strings.Fields(`the a to of and in is that it for on with as this by from at be or are was
	not have has had but if then else when which while return error session agent deck
	tmux hook status running waiting idle test build fail pass commit branch file path
	config profile claude codex gemini json sqlite index search recall hint tag ticket
	parse token bytes line record message user assistant tool result output input
	restart stop start launch fork handoff remote host ssh local worktree group`)
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 4000; i++ {
		n := 4 + r.Intn(9)
		var sb strings.Builder
		for j := 0; j < n; j++ {
			sb.WriteByte(byte('a' + r.Intn(26)))
		}
		w := sb.String()
		switch r.Intn(10) {
		case 0:
			w = "handle_" + w
		case 1:
			w = w + ".go"
		case 2:
			w = "SB-" + strconv.Itoa(r.Intn(900)+100)
		}
		base = append(base, w)
	}
	return base
}()

func randomText(r *rand.Rand, words int) string {
	var sb strings.Builder
	for i := 0; i < words; i++ {
		if i > 0 {
			if r.Intn(14) == 0 {
				sb.WriteString(".\n")
			} else {
				sb.WriteByte(' ')
			}
		}
		// Zipf-ish: the first 80 words carry most of the mass.
		var w string
		if r.Intn(3) > 0 {
			w = vocabulary[r.Intn(80)]
		} else {
			w = vocabulary[r.Intn(len(vocabulary))]
		}
		sb.WriteString(w)
	}
	return sb.String()
}

// randomUUID formats a v4-shaped id the way Claude names sessions and records.
func randomUUID(r *rand.Rand) string {
	return fmt.Sprintf("%08x-%04x-4%03x-8%03x-%012x", r.Uint32(), r.Intn(1<<16), r.Intn(1<<12), r.Intn(1<<12), r.Int63n(1<<48))
}

// generateCorpus writes Claude-shaped session files until targetBytes.
func generateCorpus(dir string, targetBytes int64, seed int64) error {
	r := rand.New(rand.NewSource(seed))
	var written int64
	for f := 0; written < targetBytes; f++ {
		sessionID := randomUUID(r)
		projDir := filepath.Join(dir, "projects", fmt.Sprintf("-Users-bench-proj-%d", f%23))
		if err := os.MkdirAll(projDir, 0o755); err != nil {
			return err
		}
		path := filepath.Join(projDir, sessionID+".jsonl")
		n, err := writeSessionFile(path, sessionID, r)
		if err != nil {
			return err
		}
		written += n
	}
	return nil
}

func writeSessionFile(path, sessionID string, r *rand.Rand) (int64, error) {
	fh, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer fh.Close()
	w := bufio.NewWriterSize(fh, 1<<20)
	cw := &countWriter{w: w}
	enc := json.NewEncoder(cw)
	ts := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(r.Intn(1<<20)) * time.Second)
	cwd := "/Users/bench/proj"
	parent := ""
	turns := 40 + r.Intn(400)
	for i := 0; i < turns; i++ {
		ts = ts.Add(time.Duration(1+r.Intn(90)) * time.Second)
		uuid := randomUUID(r)
		rec := map[string]any{
			"uuid": uuid, "parentUuid": parent, "sessionId": sessionID, "cwd": cwd,
			"timestamp": ts.Format(time.RFC3339Nano), "isSidechain": false, "version": "2.1.0",
		}
		parent = uuid
		switch r.Intn(12) {
		case 0, 1: // user text
			rec["type"] = "user"
			rec["message"] = map[string]any{"role": "user", "content": randomText(r, 8+r.Intn(60))}
		case 2, 3, 4: // assistant with thinking + text + tool_use
			rec["type"] = "assistant"
			rec["message"] = map[string]any{"role": "assistant", "model": "claude-opus-5", "content": []any{
				map[string]any{"type": "thinking", "thinking": randomText(r, 30+r.Intn(200))},
				map[string]any{"type": "text", "text": randomText(r, 20+r.Intn(120))},
				map[string]any{"type": "tool_use", "id": "toolu_" + uuid[:12], "name": "Bash", "input": map[string]any{"command": "go test ./... " + randomText(r, 3)}},
			}, "usage": map[string]any{"input_tokens": r.Intn(3000), "output_tokens": r.Intn(800), "cache_read_input_tokens": r.Intn(60000)}}
		case 5, 6: // user tool_result
			rec["type"] = "user"
			rec["message"] = map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_" + uuid[:12], "is_error": r.Intn(9) == 0, "content": randomText(r, 100+r.Intn(900))},
			}}
			rec["toolUseResult"] = map[string]any{"stdout": randomText(r, 100+r.Intn(400)), "stderr": "", "interrupted": false}
		case 7, 8: // progress noise
			rec["type"] = "progress"
			rec["data"] = map[string]any{"type": "hook_progress", "hookEvent": "PreToolUse", "output": randomText(r, 20+r.Intn(200))}
		case 9: // file-history-snapshot noise
			rec["type"] = "file-history-snapshot"
			rec["snapshot"] = map[string]any{"trackedFileBackups": map[string]any{"a.go": map[string]any{"content": randomText(r, 400+r.Intn(2500))}}}
		case 10: // attachment noise
			rec["type"] = "attachment"
			rec["attachment"] = map[string]any{"type": "diagnostics", "files": []any{map[string]any{"uri": "file:///a.go", "diagnostics": randomText(r, 100+r.Intn(1500))}}}
		default: // summary / metadata
			rec["type"] = "summary"
			rec["summary"] = randomText(r, 6+r.Intn(12))
			rec["leafUuid"] = uuid
		}
		if err := enc.Encode(rec); err != nil {
			return cw.n, err
		}
	}
	if err := w.Flush(); err != nil {
		return cw.n, err
	}
	return cw.n, nil
}

type countWriter struct {
	w *bufio.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// ---- Parse -------------------------------------------------------------

// parseStats is what a Claude reader would hand to the ingest sink, reduced
// to counts so the benchmark measures decode cost and nothing else.
type parseStats struct {
	lines, skipped, messages, unknown int
	textBytes                         int64
	texts                             []string // populated only when keepTexts
}

// noiseTypes are skipped by a byte prefilter before any JSON decode: they are
// roughly 41% of Claude bytes and never carry conversation text.
var noiseTypes = [][]byte{
	[]byte(`"type":"attachment"`),
	[]byte(`"type":"file-history-snapshot"`),
	[]byte(`"type":"progress"`),
}

type claudeRecord struct {
	Type    string `json:"type"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Timestamp string `json:"timestamp"`
}

type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"is_error"`
}

// parseClaudeJSONL streams one file with a 4 MiB line cap, prefilters noise
// by bytes, decodes the rest and extracts the text blocks the same way the
// phase 2 reader will (text and tool_result text; thinking and tool_use args
// are not indexed).
func parseClaudeJSONL(path string, st *parseStats, keepTexts bool) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	sc := bufio.NewScanner(bufio.NewReaderSize(fh, 1<<20))
	sc.Buffer(make([]byte, 0, 64<<10), maxJSONLLineSize)
	for sc.Scan() {
		line := sc.Bytes()
		st.lines++
		if len(line) == 0 {
			continue
		}
		skip := false
		for _, nt := range noiseTypes {
			if bytes.Contains(line, nt) {
				skip = true
				break
			}
		}
		if skip {
			st.skipped++
			continue
		}
		var rec claudeRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			st.unknown++
			continue
		}
		if rec.Type != "user" && rec.Type != "assistant" {
			st.unknown++
			continue
		}
		text := extractText(rec.Message.Content)
		if text == "" {
			continue
		}
		if len(text) > TextClipBytes {
			text = text[:TextClipBytes]
		}
		st.messages++
		st.textBytes += int64(len(text))
		if keepTexts {
			st.texts = append(st.texts, text)
		}
	}
	return sc.Err()
}

func extractText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	if content[0] == '"' {
		var s string
		if json.Unmarshal(content, &s) == nil {
			return s
		}
		return ""
	}
	var blocks []contentBlock
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var sb strings.Builder
	for _, blk := range blocks {
		switch blk.Type {
		case "text":
			sb.WriteString(blk.Text)
			sb.WriteByte('\n')
		case "tool_result":
			if len(blk.Content) > 0 && blk.Content[0] == '"' {
				var s string
				if json.Unmarshal(blk.Content, &s) == nil {
					sb.WriteString(s)
					sb.WriteByte('\n')
				}
			}
		}
	}
	return sb.String()
}

// BenchmarkParseClaudeJSONL measures the Go decode throughput over the
// corpus; MB/s is the number the backfill estimate needs.
func BenchmarkParseClaudeJSONL(b *testing.B) {
	files, total := benchCorpus(b)
	b.SetBytes(total)
	b.ResetTimer()
	var st parseStats
	for i := 0; i < b.N; i++ {
		st = parseStats{}
		for _, f := range files {
			if err := parseClaudeJSONL(f, &st, false); err != nil {
				b.Fatalf("%s: %v", f, err)
			}
		}
	}
	b.ReportMetric(float64(len(files)), "files")
	b.ReportMetric(float64(st.lines), "lines")
	b.ReportMetric(float64(st.messages), "msgs")
	b.ReportMetric(float64(st.skipped)/float64(max(st.lines, 1))*100, "%noise-lines")
	b.ReportMetric(float64(st.textBytes)/float64(max(total, 1))*100, "%text-of-raw")
}

// ---- FTS5 bulk insert ----------------------------------------------------

// benchTexts returns message bodies for the insert benchmarks: real ones
// from the corpus when there are enough, synthetic otherwise.
func benchTexts(b *testing.B, rows int) []string {
	b.Helper()
	files, _ := benchCorpus(b)
	st := parseStats{}
	for _, f := range files {
		if err := parseClaudeJSONL(f, &st, true); err != nil {
			b.Fatal(err)
		}
		if len(st.texts) >= rows {
			break
		}
	}
	texts := st.texts
	r := rand.New(rand.NewSource(7))
	for len(texts) < rows {
		texts = append(texts, randomText(r, 60+r.Intn(400)))
	}
	return texts[:rows]
}

func benchRows() int {
	if v, err := strconv.Atoi(os.Getenv("RECALL_BENCH_ROWS")); err == nil && v > 0 {
		return v
	}
	return defaultFTSRows
}

// BenchmarkFTS5BulkInsert times inserting msg rows plus their msg_fts
// postings through modernc.org/sqlite, in byte-bounded transactions (8 MB of
// text per commit) the way the ingest path will.
func BenchmarkFTS5BulkInsert(b *testing.B) {
	rows := benchRows()
	texts := benchTexts(b, rows)
	var totalBytes int64
	for _, t := range texts {
		totalBytes += int64(len(t))
	}
	b.SetBytes(totalBytes)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		db := openTestDB(b)
		mustExec(b, db, `CREATE TABLE msg(msg_id INTEGER PRIMARY KEY, sess_id INTEGER NOT NULL, seq INTEGER NOT NULL, body BLOB)`, MsgFTSDDL)
		b.StartTimer()
		insertMessages(b, db, texts)
		b.StopTimer()
		if n := count(b, db, `SELECT count(*) FROM msg_fts WHERE msg_fts MATCH 'session'`); n == 0 {
			b.Fatal("no postings after bulk insert")
		}
		_ = db.Close()
		b.StartTimer()
	}
	b.ReportMetric(float64(rows), "rows")
	b.ReportMetric(float64(rows)*float64(b.N)/b.Elapsed().Seconds(), "rows/s")
}

// insertBatch is one open transaction with its two prepared inserts.
type insertBatch struct {
	tx      *sql.Tx
	msgStmt *sql.Stmt
	ftsStmt *sql.Stmt
}

func beginInsertBatch(b *testing.B, db *sql.DB) insertBatch {
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	msgStmt, err := tx.Prepare(`INSERT INTO msg(msg_id, sess_id, seq, body) VALUES (?, ?, ?, ?)`)
	if err != nil {
		b.Fatal(err)
	}
	ftsStmt, err := tx.Prepare(`INSERT INTO msg_fts(rowid, body) VALUES (?, ?)`)
	if err != nil {
		b.Fatal(err)
	}
	return insertBatch{tx: tx, msgStmt: msgStmt, ftsStmt: ftsStmt}
}

func (ib insertBatch) commit(b *testing.B) {
	_ = ib.msgStmt.Close()
	_ = ib.ftsStmt.Close()
	if err := ib.tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

func insertMessages(b *testing.B, db *sql.DB, texts []string) {
	const batchBytes = 8 << 20
	batch := beginInsertBatch(b, db)
	pending := 0
	for i, text := range texts {
		if _, err := batch.msgStmt.Exec(i+1, i/40+1, i%40, []byte(text)); err != nil {
			b.Fatal(err)
		}
		if _, err := batch.ftsStmt.Exec(i+1, text); err != nil {
			b.Fatal(err)
		}
		pending += len(text)
		if pending >= batchBytes {
			batch.commit(b)
			batch = beginInsertBatch(b, db)
			pending = 0
		}
	}
	batch.commit(b)
}

// BenchmarkCardFTSInsertViaTriggers times the ranked surface: card rows
// inserted with the three triggers maintaining card_fts (detail=full).
func BenchmarkCardFTSInsertViaTriggers(b *testing.B) {
	const cards = 12000
	r := rand.New(rand.NewSource(11))
	titles := make([]string, cards)
	summaries := make([]string, cards)
	for i := range titles {
		titles[i] = randomText(r, 3+r.Intn(6))
		summaries[i] = randomText(r, 20+r.Intn(60))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		db := openTestDB(b)
		mustExec(b, db, CardDDL, CardFTSDDL)
		mustExec(b, db, CardTriggerDDL...)
		b.StartTimer()
		tx, err := db.Begin()
		if err != nil {
			b.Fatal(err)
		}
		stmt, err := tx.Prepare(`INSERT INTO card(sess_id,title,hints,tags,summary,preview) VALUES (?,?,?,?,?,?)`)
		if err != nil {
			b.Fatal(err)
		}
		for j := 0; j < cards; j++ {
			if _, err := stmt.Exec(j+1, titles[j], "purpose=bench ticket=SB-"+strconv.Itoa(j%900), "bench auth", summaries[j], summaries[j][:min(len(summaries[j]), 200)]); err != nil {
				b.Fatal(err)
			}
		}
		_ = stmt.Close()
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if a, c := count(b, db, `SELECT count(*) FROM card`), count(b, db, `SELECT count(*) FROM card_fts`); a != cards || c != cards {
			b.Fatalf("card=%d card_fts=%d; want %d", a, c, cards)
		}
		_ = db.Close()
		b.StartTimer()
	}
	b.ReportMetric(float64(cards), "cards")
}

// The benchmark's own parser and generator agree on the record shape: noise
// lines are prefiltered, user and assistant text is extracted and clipped.
func TestParseClaudeJSONL_GeneratedCorpus(t *testing.T) {
	dir := t.TempDir()
	if err := generateCorpus(dir, 1<<20, 3); err != nil {
		t.Fatal(err)
	}
	files, total, err := listJSONL(dir)
	if err != nil || len(files) == 0 || total < 1<<20 {
		t.Fatalf("listJSONL: %d files, %d bytes, err %v", len(files), total, err)
	}
	var st parseStats
	for _, f := range files {
		if err := parseClaudeJSONL(f, &st, true); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	if st.messages == 0 || st.skipped == 0 || st.textBytes == 0 || len(st.texts) != st.messages {
		t.Fatalf("stats = %+v", st)
	}
	for _, text := range st.texts {
		if len(text) > TextClipBytes {
			t.Fatalf("text of %d bytes exceeds the %d clip", len(text), TextClipBytes)
		}
	}
	if extractText(json.RawMessage(`"plain"`)) != "plain" || extractText(json.RawMessage(`[{"type":"thinking","thinking":"x"},{"type":"text","text":"t"}]`)) != "t\n" {
		t.Fatal("extractText shape mismatch")
	}
}
