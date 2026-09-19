// Package testcorpus generates Claude-shaped transcript trees for tests and
// benchmarks. The real corpus is not available in the CI sandbox, so the
// generator reproduces its record mix: user and assistant text are each
// about 1.6% of bytes, tool_use input 3.5%, tool_result 11.8%, noise records
// (attachment, file-history-snapshot, progress, queue-operation) about 41%,
// the rest JSON envelope (CONTEXT.md, measured 2026-09-18).
package testcorpus

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Shape selects the byte mix.
type Shape int

const (
	// Realistic reproduces the measured shares above.
	Realistic Shape = iota
	// Dense has far more indexable text per byte (the phase 1 benchmark
	// corpus); useful when a test wants many messages from few bytes.
	Dense
)

// Options configures Generate.
type Options struct {
	// TargetBytes stops generation once the tree is at least this large.
	TargetBytes int64
	// Files, when > 0, generates exactly this many session files instead.
	Files int
	Seed  int64
	Shape Shape
	// SubagentEvery gives every nth session a subagents/ dir with one
	// sidechain transcript (0: none).
	SubagentEvery int
	// Projects is the number of project slugs to spread sessions over.
	Projects int
}

// Stats describes what was written.
type Stats struct {
	Files      int
	Bytes      int64
	Sessions   []string // native ids, in creation order
	Paths      []string // top-level session files
	Prompts    int      // user prompts written (= expected countable turns)
	TextBytes  int64    // decoded user+assistant text
	NoiseLines int
	Lines      int
}

// Generate writes <dir>/projects/<slug>/<uuid>.jsonl files.
func Generate(dir string, o Options) (Stats, error) {
	var st Stats
	if o.Projects <= 0 {
		o.Projects = 23
	}
	r := rand.New(rand.NewSource(o.Seed))
	for f := 0; ; f++ {
		if o.Files > 0 && f >= o.Files {
			break
		}
		if o.Files == 0 && st.Bytes >= o.TargetBytes {
			break
		}
		sessionID := UUID(r)
		projDir := filepath.Join(dir, "projects", fmt.Sprintf("-Users-bench-proj-%d", f%o.Projects))
		if err := os.MkdirAll(projDir, 0o755); err != nil {
			return st, err
		}
		path := filepath.Join(projDir, sessionID+".jsonl")
		fs, err := WriteSession(path, sessionID, r, o.Shape, false)
		if err != nil {
			return st, err
		}
		st.add(fs)
		st.Sessions = append(st.Sessions, sessionID)
		st.Paths = append(st.Paths, path)
		if o.SubagentEvery > 0 && f%o.SubagentEvery == 0 {
			subDir := filepath.Join(projDir, sessionID, "subagents")
			if err := os.MkdirAll(subDir, 0o755); err != nil {
				return st, err
			}
			agent := "agent-" + UUID(r)[:17]
			fs, err := WriteSession(filepath.Join(subDir, agent+".jsonl"), sessionID, r, o.Shape, true)
			if err != nil {
				return st, err
			}
			st.add(fs)
		}
	}
	return st, nil
}

func (s *Stats) add(o Stats) {
	s.Files += o.Files
	s.Bytes += o.Bytes
	s.Prompts += o.Prompts
	s.TextBytes += o.TextBytes
	s.NoiseLines += o.NoiseLines
	s.Lines += o.Lines
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

// prose is real English about the work these transcripts describe. Real
// chat text compresses 3 to 4x under zstd because of English letter
// statistics and repeated identifiers; a stream of random words has
// neither, so the size test would measure the generator, not the index.
var prose = []string{
	"The flaky auth test failed again because the clock on the CI runner drifted by three seconds",
	"and the token validation rejected the request before the handler ran",
	"We should pin the clock source in the container and retry the test with a fresh token",
	"before concluding that the fix works on the main branch",
	"I read the session file and the transcript shows the hook fired twice for the same turn",
	"The status went from running to waiting while the tmux pane was still printing output",
	"Let me check the config profile and the claude config dir that this session resolved",
	"The build passes and go vet is clean but the golden frame for the search overlay changed",
	"Every message body is stored compressed so the phrase verifier reads decoded text",
	"The index is a disposable cache and the hints live in state.db where a rebuild cannot lose them",
	"Run the tests in the docker sandbox and paste every ok and FAIL line into the results",
	"The remote host runs its own agent-deck so the transcript never crosses the ssh boundary",
	"The sweep stats every file and compares size and mtime against the source ledger",
	"Only the appended tail is parsed and a torn trailing line is left for the next sweep",
	"The launch command derives the purpose hint from the first line of the message",
	"I will commit this with the session trailer and no attribution as the rules require",
	"The worktree branch is ahead of main by four commits and the rebase applied cleanly",
	"The error came from the sqlite driver returning busy while the writer held the lock",
	"Please annotate the session with the outcome and the decision so the next agent can find it",
	"The parser skips attachment and progress records before decoding any json at all",
	"The conductor session restarted the child and the inbox drained the completion event",
	"The cost events are written from the same pass that indexed the text so nothing is read twice",
	"The card carries the title the hints the tags and a short preview of the first prompt",
	"A copied transcript keeps its session id so it is quarantined rather than merged",
	"The load gate refuses to start a backfill while any managed session is busy",
	"The heap must not grow with the file because the reader holds one record at a time",
	"The search ranks card hits above body hits and applies a recency decay in sql",
	"The subagent transcript is its own source with an edge back to the owning session",
	"Check the file path in the tool call and record a read write or edit touch for it",
	"The interactive sweep stops after one hundred and fifty milliseconds and reports what it deferred",
}

// Text returns about words words: English sentences from the prose pool
// (Zipf-ish: the first ones carry most of the mass) with identifiers from
// the vocabulary sprinkled in so the FTS term distribution has a long tail.
func Text(r *rand.Rand, words int) string {
	var sb strings.Builder
	n := 0
	for n < words {
		if sb.Len() > 0 {
			if r.Intn(5) == 0 {
				sb.WriteString(".\n")
			} else {
				sb.WriteString(". ")
			}
		}
		var sent string
		if r.Intn(3) == 0 {
			sent = prose[r.Intn(len(prose))]
		} else {
			sent = prose[r.Intn(8)]
		}
		sb.WriteString(sent)
		n += strings.Count(sent, " ") + 1
		if r.Intn(10) == 0 {
			sb.WriteByte(' ')
			sb.WriteString(vocabulary[r.Intn(len(vocabulary))])
			n++
		}
	}
	return sb.String()
}

// UUID formats a v4-shaped id the way Claude names sessions and records.
func UUID(r *rand.Rand) string {
	return fmt.Sprintf("%08x-%04x-4%03x-8%03x-%012x", r.Uint32(), r.Intn(1<<16), r.Intn(1<<12), r.Intn(1<<12), r.Int63n(1<<48))
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

// WriteSession writes one transcript. sidechain marks a subagent file.
func WriteSession(path, sessionID string, r *rand.Rand, shape Shape, sidechain bool) (Stats, error) {
	var st Stats
	fh, err := os.Create(path)
	if err != nil {
		return st, err
	}
	defer fh.Close()
	w := bufio.NewWriterSize(fh, 1<<20)
	cw := &countWriter{w: w}
	enc := json.NewEncoder(cw)
	ts := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(r.Intn(1<<20)) * time.Second)
	cwd := "/Users/bench/proj"
	parent := ""
	turns := 20 + r.Intn(100)
	if shape == Dense {
		turns = 40 + r.Intn(400)
	}
	env := func(typ string) map[string]any {
		ts = ts.Add(time.Duration(1+r.Intn(90)) * time.Second)
		uuid := UUID(r)
		rec := map[string]any{
			"uuid": uuid, "parentUuid": parent, "sessionId": sessionID, "cwd": cwd, "type": typ,
			"timestamp": ts.Format(time.RFC3339Nano), "isSidechain": sidechain, "version": "2.1.0",
			"gitBranch": "main", "userType": "external", "entrypoint": "cli", "slug": "bench",
		}
		if sidechain {
			rec["agentId"] = strings.TrimPrefix(strings.TrimSuffix(filepath.Base(path), ".jsonl"), "agent-")
		}
		parent = uuid
		return rec
	}
	write := func(rec map[string]any) error {
		st.Lines++
		return enc.Encode(rec)
	}
	if !sidechain && r.Intn(3) == 0 {
		st.Lines++
		if err := enc.Encode(map[string]any{"type": "custom-title", "customTitle": "bench " + Text(r, 2), "sessionId": sessionID}); err != nil {
			return st, err
		}
	}
	for i := 0; i < turns; i++ {
		if shape == Dense {
			if err := denseTurn(r, env, write, &st); err != nil {
				return st, err
			}
			continue
		}
		if err := realisticTurn(r, env, write, &st, i); err != nil {
			return st, err
		}
	}
	if err := w.Flush(); err != nil {
		return st, err
	}
	st.Files = 1
	st.Bytes = cw.n
	return st, nil
}

// realisticTurn writes one user prompt, one assistant reply with a tool
// call, the tool result, and the noise records that surround them in a
// real file, at the measured byte shares.
func realisticTurn(r *rand.Rand, env func(string) map[string]any, write func(map[string]any) error, st *Stats, i int) error {
	prompt := Text(r, 30+r.Intn(180))
	rec := env("user")
	rec["message"] = map[string]any{"role": "user", "content": prompt}
	st.Prompts++
	st.TextBytes += int64(len(prompt))
	if err := write(rec); err != nil {
		return err
	}
	reply := Text(r, 50+r.Intn(250))
	uuid := rec["uuid"].(string)
	toolID := "toolu_" + uuid[:12]
	input := map[string]any{"command": "go test ./... " + Text(r, 20+r.Intn(40))}
	name := "Bash"
	switch r.Intn(4) {
	case 0:
		name, input = "Read", map[string]any{"file_path": "/Users/bench/proj/" + Text(r, 1) + ".go"}
	case 1:
		name, input = "Edit", map[string]any{"file_path": "/Users/bench/proj/" + Text(r, 1) + ".go", "old_string": Text(r, 15), "new_string": Text(r, 15)}
	}
	rec = env("assistant")
	rec["requestId"] = "req_" + uuid[:10]
	rec["message"] = map[string]any{"role": "assistant", "model": "claude-opus-5", "id": "msg_" + uuid[:12], "content": []any{
		map[string]any{"type": "text", "text": reply},
		map[string]any{"type": "tool_use", "id": toolID, "name": name, "input": input},
	}, "usage": map[string]any{"input_tokens": r.Intn(3000), "output_tokens": r.Intn(800), "cache_read_input_tokens": r.Intn(60000)}}
	st.TextBytes += int64(len(reply))
	if err := write(rec); err != nil {
		return err
	}
	isErr := r.Intn(12) == 0
	rec = env("user")
	rec["message"] = map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": toolID, "is_error": isErr, "content": Text(r, 1500+r.Intn(2500))},
	}}
	rec["toolUseResult"] = map[string]any{"stdout": Text(r, 20), "stderr": "", "interrupted": false}
	if err := write(rec); err != nil {
		return err
	}
	// Noise: about 41% of bytes, in the observed proportions.
	noise := 3 + r.Intn(4)
	for k := 0; k < noise; k++ {
		rec = env("progress")
		switch r.Intn(5) {
		case 0:
			rec["type"] = "file-history-snapshot"
			rec["snapshot"] = map[string]any{"trackedFileBackups": map[string]any{"a.go": map[string]any{"content": Text(r, 1200+r.Intn(2600))}}}
		case 1:
			rec["type"] = "attachment"
			rec["attachment"] = map[string]any{"type": "diagnostics", "files": []any{map[string]any{"uri": "file:///a.go", "diagnostics": Text(r, 600+r.Intn(1600))}}}
		case 2:
			rec["type"] = "queue-operation"
			rec["operation"] = "enqueue"
			rec["content"] = Text(r, 60+r.Intn(200))
		default:
			rec["data"] = map[string]any{"type": "hook_progress", "hookEvent": "PreToolUse", "output": Text(r, 400+r.Intn(1200))}
		}
		st.NoiseLines++
		if err := write(rec); err != nil {
			return err
		}
	}
	if i%25 == 24 {
		rec = env("system")
		rec["subtype"] = "compact_boundary"
		rec["content"] = "Conversation compacted"
		if err := write(rec); err != nil {
			return err
		}
	}
	return nil
}

// denseTurn mirrors the phase 1 benchmark corpus record mix.
func denseTurn(r *rand.Rand, env func(string) map[string]any, write func(map[string]any) error, st *Stats) error {
	rec := env("user")
	uuid := rec["uuid"].(string)
	switch r.Intn(12) {
	case 0, 1:
		text := Text(r, 8+r.Intn(60))
		rec["message"] = map[string]any{"role": "user", "content": text}
		st.Prompts++
		st.TextBytes += int64(len(text))
	case 2, 3, 4:
		rec["type"] = "assistant"
		text := Text(r, 20+r.Intn(120))
		rec["message"] = map[string]any{"role": "assistant", "model": "claude-opus-5", "content": []any{
			map[string]any{"type": "thinking", "thinking": Text(r, 30+r.Intn(200))},
			map[string]any{"type": "text", "text": text},
			map[string]any{"type": "tool_use", "id": "toolu_" + uuid[:12], "name": "Bash", "input": map[string]any{"command": "go test ./... " + Text(r, 3)}},
		}, "usage": map[string]any{"input_tokens": r.Intn(3000), "output_tokens": r.Intn(800), "cache_read_input_tokens": r.Intn(60000)}}
		st.TextBytes += int64(len(text))
	case 5, 6:
		rec["message"] = map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_" + uuid[:12], "is_error": r.Intn(9) == 0, "content": Text(r, 100+r.Intn(900))},
		}}
		rec["toolUseResult"] = map[string]any{"stdout": Text(r, 100+r.Intn(400)), "stderr": "", "interrupted": false}
	case 7, 8:
		rec["type"] = "progress"
		rec["data"] = map[string]any{"type": "hook_progress", "hookEvent": "PreToolUse", "output": Text(r, 20+r.Intn(200))}
		st.NoiseLines++
	case 9:
		rec["type"] = "file-history-snapshot"
		rec["snapshot"] = map[string]any{"trackedFileBackups": map[string]any{"a.go": map[string]any{"content": Text(r, 400+r.Intn(2500))}}}
		st.NoiseLines++
	case 10:
		rec["type"] = "attachment"
		rec["attachment"] = map[string]any{"type": "diagnostics", "files": []any{map[string]any{"uri": "file:///a.go", "diagnostics": Text(r, 100+r.Intn(1500))}}}
		st.NoiseLines++
	default:
		rec["type"] = "summary"
		rec["summary"] = Text(r, 6+r.Intn(12))
		rec["leafUuid"] = uuid
	}
	return write(rec)
}
