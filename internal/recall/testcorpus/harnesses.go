package testcorpus

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Fixtures shaped like the other harnesses' real files on the design
// machine (plans: RESEARCH.md, harness-formats), trimmed to every record
// kind a reader must handle. The reader tests and the ingest tests share
// them.

// CodexThread is the thread id of CodexShapes.
const CodexThread = "01a0b956-e4be-7301-8f3c-3f1e39ab75b0"

// CodexShapes is one rollout: session_meta, event_msg, developer/user/
// assistant messages, turn_context, reasoning, function_call and
// custom_tool_call with outputs, token_usage_record, turn_aborted,
// world_state, a compaction whose replacement_history repeats indexed
// messages, and an unknown record type.
const CodexShapes = `{"timestamp":"2026-09-19T11:04:34.883Z","ordinal":0,"type":"session_meta","payload":{"session_id":"01a0b956-e4be-7301-8f3c-3f1e39ab75b0","id":"01a0b956-e4be-7301-8f3c-3f1e39ab75b0","timestamp":"2026-09-19T11:04:34.750Z","cwd":"/Users/x/proj","originator":"codex_exec","cli_version":"0.154.0","source":"exec","git":{"commit_hash":"c87af298","branch":"perf/refresh-tick-v2","repository_url":"git@github.com:x/proj.git"},"model_provider":"openai","base_instructions":{"text":"You are Codex"}}}
{"timestamp":"2026-09-19T11:04:34.883Z","ordinal":1,"type":"event_msg","payload":{"type":"task_started","turn_id":"t1","started_at":1789815874,"model_context_window":258400}}
{"timestamp":"2026-09-19T11:04:37.386Z","ordinal":2,"type":"response_item","payload":{"type":"message","id":"msg_dev","role":"developer","content":[{"type":"input_text","text":"## Memory\n\nzebra developer instructions must not be indexed"}]}}
{"timestamp":"2026-09-19T11:04:37.386Z","ordinal":3,"type":"response_item","payload":{"type":"message","id":"msg_env","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/Users/x/proj</cwd>\n</environment_context>"}]}}
{"timestamp":"2026-09-19T11:04:37.387Z","ordinal":4,"type":"turn_context","payload":{"turn_id":"t1","cwd":"/Users/x/proj","model":"gpt-5.6-sol","approval_policy":"never","sandbox_policy":{"type":"read-only"}}}
{"timestamp":"2026-09-19T11:04:37.400Z","ordinal":5,"type":"response_item","payload":{"type":"message","id":"msg_u1","role":"user","content":[{"type":"input_text","text":"Fix the flaky auth test, the clock skew one"}]}}
{"timestamp":"2026-09-19T11:04:38.000Z","ordinal":6,"type":"response_item","payload":{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"gAAAA-zebra-encrypted"}}
{"timestamp":"2026-09-19T11:04:39.000Z","ordinal":7,"type":"response_item","payload":{"type":"function_call","id":"fc_1","name":"shell","namespace":"","arguments":"{\"command\":[\"go\",\"test\",\"./internal/auth/\"]}","call_id":"call_1"}}
{"timestamp":"2026-09-19T11:04:41.500Z","ordinal":8,"type":"response_item","payload":{"type":"function_call_output","id":"fco_1","call_id":"call_1","output":"FAIL\nExit code: 1"}}
{"timestamp":"2026-09-19T11:04:42.000Z","ordinal":9,"type":"response_item","payload":{"type":"custom_tool_call","id":"ctc_1","status":"completed","call_id":"call_2","name":"exec","input":"const r = await tools.exec_command({\"cmd\":\"sed -n 1,20p auth.go\"})"}}
{"timestamp":"2026-09-19T11:04:42.250Z","ordinal":10,"type":"response_item","payload":{"type":"custom_tool_call_output","id":"ctco_1","call_id":"call_2","output":[{"type":"input_text","text":"package auth"}]}}
{"timestamp":"2026-09-19T11:04:43.000Z","ordinal":11,"type":"response_item","payload":{"type":"message","id":"msg_a1","role":"assistant","content":[{"type":"output_text","text":"The root cause was clock skew."}],"phase":"final_answer"}}
{"timestamp":"2026-09-19T11:04:43.394Z","ordinal":12,"type":"token_usage_record","payload":{"thread_id":"01a0b956-e4be-7301-8f3c-3f1e39ab75b0","turn_id":"t1","usage":{"input_tokens":25088,"cached_input_tokens":12928,"cache_write_input_tokens":0,"output_tokens":6,"reasoning_output_tokens":0,"total_tokens":25094},"thread_token_usage":{"input_tokens":99999}}}
{"timestamp":"2026-09-19T11:04:44.000Z","ordinal":13,"type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t1","reason":"interrupted"}}
{"timestamp":"2026-09-19T11:04:44.000Z","ordinal":14,"type":"world_state","payload":{"files":[]}}
{"timestamp":"2026-09-19T11:04:45.000Z","ordinal":15,"type":"compacted","payload":{"message":"Summary so far: the auth test flakes on clock skew.","replacement_history":[{"type":"message","id":"msg_u1","role":"user","content":[{"type":"input_text","text":"Fix the flaky auth test, the clock skew one"}]},{"type":"message","id":"msg_a1","role":"assistant","content":[{"type":"output_text","text":"The root cause was clock skew."}]}],"window_number":1,"compaction_response_id":"resp_x"}}
{"timestamp":"2026-09-19T11:04:46.000Z","ordinal":16,"type":"response_item","payload":{"type":"message","id":"msg_u2","role":"user","content":[{"type":"input_text","text":"Now add a regression test"}]}}
{"timestamp":"2026-09-19T11:04:47.000Z","ordinal":17,"type":"brand_new_record","payload":{}}
`

// CodexHome lays out a Codex home under dir with one rollout, a
// session_index.jsonl naming the thread, and (cursor >= 0) a projection
// database whose cursor for the thread is cursor. Returns the rollout path.
func CodexHome(dir string, cursor int64) (string, error) {
	sessDir := filepath.Join(dir, "sessions", "2026", "09", "19")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		return "", err
	}
	rollout := filepath.Join(sessDir, "rollout-2026-09-19T13-04-34-"+CodexThread+".jsonl")
	if err := os.WriteFile(rollout, []byte(CodexShapes), 0o644); err != nil {
		return "", err
	}
	idx := `{"id":"` + CodexThread + `","thread_name":"Fix flaky auth","updated_at":"2026-09-19T11:05:00Z"}` + "\n" + `{"id":"other","thread_name":"Other"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "session_index.jsonl"), []byte(idx), 0o644); err != nil {
		return "", err
	}
	if cursor < 0 {
		return rollout, nil
	}
	return rollout, execSQL(filepath.Join(dir, "thread_history_1.sqlite"),
		`CREATE TABLE thread_history_projection_state (thread_id TEXT PRIMARY KEY, next_rollout_byte_offset INTEGER NOT NULL, next_rollout_ordinal INTEGER NOT NULL)`,
		fmt.Sprintf(`INSERT INTO thread_history_projection_state VALUES ('%s', %d, 0)`, CodexThread, cursor))
}

// PiID is the session id of PiShapes.
const PiID = "01a030d2-a0d3-7790-a9af-3c633c68f25a"

// PiShapes is one version-3 session: header with parentSession (a fork),
// model change, prompt, assistant thinking + toolCall, toolResult (one
// errored), session_info, custom_message, compaction, a string prompt
// and an unknown type.
const PiShapes = `{"type":"session","version":3,"id":"01a030d2-a0d3-7790-a9af-3c633c68f25a","timestamp":"2026-08-23T22:51:45.235Z","cwd":"/private/tmp/phase2-exp","parentSession":"/Users/x/.pi/agent/sessions/--private-tmp-phase2-exp--/2026-08-23T22-51-30-693Z_01a030d2-6805-77ca-86fb-7d759f7258ac.jsonl"}
{"type":"model_change","id":"b1d92e01","parentId":null,"timestamp":"2026-08-23T22:51:30.977Z","provider":"openai-codex","modelId":"gpt-5.6-sol"}
{"type":"thinking_level_change","id":"7a87b875","parentId":"b1d92e01","timestamp":"2026-08-23T22:51:30.977Z","thinkingLevel":"xhigh"}
{"type":"message","id":"m1","parentId":"7a87b875","timestamp":"2026-08-23T22:51:45.300Z","message":{"role":"user","content":[{"type":"text","text":"Evaluate Hermes as a harness"}],"timestamp":1787005905300}}
{"type":"message","id":"m2","parentId":"m1","timestamp":"2026-08-23T22:51:50.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"zebra thoughts"},{"type":"text","text":"Reading the README first."},{"type":"toolCall","id":"call_1","name":"read","arguments":{"path":"/Users/x/README.md"}}],"api":"openai-responses","provider":"openai-codex","model":"gpt-5.6-sol","usage":{"input":5800,"output":186,"cacheRead":0,"cacheWrite":0,"reasoning":166,"totalTokens":5986},"stopReason":"toolUse"}}
{"type":"message","id":"m3","parentId":"m2","timestamp":"2026-08-23T22:51:51.250Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"read","content":[{"type":"text","text":"# README zebra result text"}],"isError":false,"timestamp":1787005911250}}
{"type":"message","id":"m4","parentId":"m3","timestamp":"2026-08-23T22:51:52.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"call_2","name":"bash","arguments":{"command":"ls notes"}}],"model":"gpt-5.6-sol","usage":{"input":10,"output":2}}}
{"type":"message","id":"m5","parentId":"m4","timestamp":"2026-08-23T22:51:52.500Z","message":{"role":"toolResult","toolCallId":"call_2","toolName":"bash","content":[{"type":"text","text":"ls: notes: No such file"}],"isError":true}}
{"type":"session_info","id":"db851eca","parentId":"m5","timestamp":"2026-08-23T22:51:45.972Z","name":"hermes-eval"}
{"type":"custom_message","customType":"subagent_supervisor_request","content":"zebra supervisor notice","id":"c1","parentId":"m5","timestamp":"2026-08-23T22:51:53.000Z"}
{"type":"compaction","id":"fe32007a","parentId":"c1","timestamp":"2026-08-23T22:52:00.000Z","summary":"## Goal\nEvaluate Hermes","firstKeptEntryId":"m4","tokensBefore":783927}
{"type":"message","id":"m6","parentId":"fe32007a","timestamp":"2026-08-23T22:52:10.000Z","message":{"role":"user","content":"plain string prompt"}}
{"type":"future_record","id":"f1","parentId":"m6"}
`

// PiHome writes PiShapes under dir/agent/sessions/<enc-cwd>/ and returns
// the file path.
func PiHome(dir string) (string, error) {
	sessDir := filepath.Join(dir, "agent", "sessions", "--private-tmp-phase2-exp--")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(sessDir, "2026-08-23T22-51-45-235Z_"+PiID+".jsonl")
	return path, os.WriteFile(path, []byte(PiShapes), 0o644)
}

// GeminiShapes is one chat document: string and part-list content, a
// gemini turn with thoughts, tokens and tool calls (args as the
// Python-repr string real files carry), info and error messages, and the
// trailing summary.
const GeminiShapes = `{"sessionId":"196a60d9-6cfb-4069-8443-a78a64cedff5","projectHash":"1ac57b4f1f0ff6cf05bbdbc6f27b479d592a7ad58f1664af6d45aa20c949f9c0","startTime":"2026-01-19T12:18:54.601Z","lastUpdated":"2026-01-19T12:22:06.343Z","kind":"main","messages":[
{"id":"u1","timestamp":"2026-01-19T12:18:54.601Z","type":"user","content":"Fix the flaky auth test, the clock skew one"},
{"id":"g1","timestamp":"2026-01-19T12:18:58.887Z","type":"gemini","content":"I will read the issue context first.","thoughts":[{"subject":"zebra thought","description":"zebra thinking text","timestamp":"2026-01-19T12:18:57.014Z"}],"tokens":{"input":10387,"output":30,"cached":5,"thoughts":155,"tool":0,"total":10572},"model":"gemini-3-flash-preview","toolCalls":[{"id":"read_file-1","name":"read_file","args":"{'file_path': '.github-issue-context.json'}","result":[{"functionResponse":{"id":"read_file-1","name":"read_file","response":{"output":"zebra tool output"}}}],"status":"success","timestamp":"2026-01-19T12:18:59.387Z","resultDisplay":""}]},
{"id":"i1","timestamp":"2026-01-19T12:19:00.000Z","type":"info","content":"Request cancelled."},
{"id":"u2","timestamp":"2026-01-19T12:19:10.000Z","type":"user","content":[{"text":"Your task is to answer the following question"},{"text":"about clock skew"}]},
{"id":"g2","timestamp":"2026-01-19T12:19:12.000Z","type":"gemini","content":"The root cause was clock skew.","tokens":{"input":100,"output":20,"cached":0,"thoughts":0,"tool":0,"total":120},"model":"gemini-3-flash-preview","toolCalls":[{"id":"run_shell_command-1","name":"run_shell_command","args":{"command":"go test ./internal/auth/"},"status":"error","timestamp":"2026-01-19T12:19:13.000Z"}]},
{"id":"e1","timestamp":"2026-01-19T12:19:20.000Z","type":"error","content":"[API Error: 400]"},
{"id":"x1","timestamp":"2026-01-19T12:19:21.000Z","type":"future_kind","content":"zebra"}
],"summary":"Analyze the flaky auth test."}`

// GeminiHome writes GeminiShapes under dir/tmp/<hash>/chats/ and returns
// the file path.
func GeminiHome(dir string) (string, error) {
	chats := filepath.Join(dir, "tmp", "1ac57b4f", "chats")
	if err := os.MkdirAll(chats, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(chats, "session-2026-01-19T12-18-196a60d9.json")
	return path, os.WriteFile(path, []byte(GeminiShapes), 0o644)
}

// OpenCodeSession is the session id of the OpenCode tree.
const OpenCodeSession = "ses_41edf7088ffezWqCkbEE66oiGT"

// OpenCodeTree writes a storage/ tree (one session, a user and an
// assistant message, text / tool / reasoning / step parts, and a
// snapshot/ object that must never be opened) and returns the storage dir.
func OpenCodeTree(dir string) (string, error) {
	storage := filepath.Join(dir, "opencode", "storage")
	files := map[string]string{
		"session/60e0658e/ses_41edf7088ffezWqCkbEE66oiGT.json": `{"id":"ses_41edf7088ffezWqCkbEE66oiGT","slug":"kind-forest","version":"1.1.28","projectID":"60e0658e","directory":"/Users/x/claude-deck","title":"Greeting and quick check-in","time":{"created":1769008566135,"updated":1770052345394},"summary":{"additions":0,"deletions":0,"files":0}}`,
		"message/ses_41edf7088ffezWqCkbEE66oiGT/msg_a.json":    `{"id":"msg_a","sessionID":"ses_41edf7088ffezWqCkbEE66oiGT","role":"user","time":{"created":1769133410652},"agent":"build","model":{"providerID":"opencode","modelID":"big-pickle"}}`,
		"message/ses_41edf7088ffezWqCkbEE66oiGT/msg_b.json":    `{"id":"msg_b","sessionID":"ses_41edf7088ffezWqCkbEE66oiGT","role":"assistant","time":{"created":1769133412000,"completed":1769133415000},"parentID":"msg_a","modelID":"big-pickle","providerID":"opencode","path":{"cwd":"/Users/x/claude-deck","root":"/Users/x/claude-deck"},"cost":0,"tokens":{"input":20935,"output":13,"reasoning":0,"cache":{"read":32,"write":0}},"finish":"stop"}`,
		"part/msg_a/prt_1.json":                                `{"id":"prt_1","sessionID":"ses_41edf7088ffezWqCkbEE66oiGT","messageID":"msg_a","type":"text","text":"Fix the flaky auth test, the clock skew one"}`,
		"part/msg_b/prt_1.json":                                `{"id":"prt_1","sessionID":"ses_41edf7088ffezWqCkbEE66oiGT","messageID":"msg_b","type":"step-start"}`,
		"part/msg_b/prt_2.json":                                `{"id":"prt_2","sessionID":"ses_41edf7088ffezWqCkbEE66oiGT","messageID":"msg_b","type":"reasoning","text":"zebra reasoning"}`,
		"part/msg_b/prt_3.json":                                `{"id":"prt_3","sessionID":"ses_41edf7088ffezWqCkbEE66oiGT","messageID":"msg_b","type":"tool","callID":"call-1","tool":"codesearch","state":{"status":"error","input":{"query":"clock skew retries"},"output":"zebra output","time":{"start":1769133412500,"end":1769133413000}}}`,
		"part/msg_b/prt_4.json":                                `{"id":"prt_4","sessionID":"ses_41edf7088ffezWqCkbEE66oiGT","messageID":"msg_b","type":"text","text":"The root cause was clock skew."}`,
		"part/msg_b/prt_5.json":                                `{"id":"prt_5","sessionID":"ses_41edf7088ffezWqCkbEE66oiGT","messageID":"msg_b","type":"step-finish"}`,
		"snapshot/60e0658e/objects/ab/zebra":                   `must never be opened`,
	}
	for rel, body := range files {
		p := filepath.Join(storage, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	return storage, nil
}

// HermesSession and HermesChild are the two sessions of HermesHome.
const (
	HermesSession = "20260814_101329_7730e5"
	HermesChild   = "20260814_101333_f22eb1"
)

// HermesHome writes a state.db (the columns the reader reads, two
// sessions, six messages including a tool round trip) under dir.
func HermesHome(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "state.db")
	return path, execSQL(path,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT NOT NULL, parent_session_id TEXT, started_at REAL NOT NULL, ended_at REAL,
			model TEXT, cwd TEXT, git_branch TEXT, title TEXT, title_source TEXT, input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0,
			cache_read_tokens INTEGER DEFAULT 0, cache_write_tokens INTEGER DEFAULT 0, handoff_state TEXT, archived INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, role TEXT NOT NULL, content TEXT, tool_call_id TEXT,
			tool_calls TEXT, tool_name TEXT, timestamp REAL NOT NULL, token_count INTEGER, reasoning TEXT, compacted INTEGER NOT NULL DEFAULT 0, active INTEGER NOT NULL DEFAULT 1)`,
		`INSERT INTO sessions (id, source, started_at, ended_at, model, cwd, git_branch, title, title_source, input_tokens, output_tokens) VALUES
			('20260814_101329_7730e5', 'cli', 1786695212.2, 1787234837.5, 'gpt-5.6-sol', '/Users/x', 'main', 'Friendly greeting #2', 'llm', 5985, 11)`,
		`INSERT INTO sessions (id, source, parent_session_id, started_at, model, cwd, title, title_source) VALUES
			('20260814_101333_f22eb1', 'cli', '20260814_101329_7730e5', 1786695213.4, 'gpt-5.6-sol', '/Users/x', 'Reply with exactly: CODEX-OK', 'derived')`,
		`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('20260814_101329_7730e5', 'user', 'hi, the clock skew again', 1786695212.2)`,
		`INSERT INTO messages (session_id, role, content, tool_calls, timestamp) VALUES ('20260814_101329_7730e5', 'assistant', 'Let me look.',
			'[{"id":"call_1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"ls\"}"}}]', 1786695213.0)`,
		`INSERT INTO messages (session_id, role, content, tool_call_id, tool_name, timestamp) VALUES ('20260814_101329_7730e5', 'tool', 'Error: no such dir', 'call_1', 'terminal', 1786695213.5)`,
		`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('20260814_101329_7730e5', 'assistant', 'Hi! How can I help?', 1786695216.0)`,
		`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('20260814_101333_f22eb1', 'user', 'Reply with exactly: CODEX-OK', 1786695213.4)`,
		`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('20260814_101333_f22eb1', 'assistant', 'CODEX-OK gpt-5.6-sol', 1786695215.6)`,
	)
}

func execSQL(path string, stmts ...string) error {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("%s: %w", strings.SplitN(q, "\n", 2)[0], err)
		}
	}
	return nil
}
