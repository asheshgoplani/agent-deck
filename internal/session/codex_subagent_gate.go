package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Codex subagent-thread rebind poisoning (incident 2026-07-15, ares fleet).
//
// Codex spawns subagent threads (collab tool) whose rollouts live beside user
// threads under codexHome/sessions and whose notify hooks fire the same
// agent-turn-complete events. bindCodexSessionFromHook had no quality gate
// (unlike the Claude clear-rebind heuristics), so a completing subagent's
// hook payload rebound tool_data.codex_session_id to the child thread id.
// While the main process lives the lsof probe flips the binding back within
// its poll interval, but a `session restart` reads whichever id won the last
// race and respawns `codex resume <child>`. A subagent thread that already
// delivered its final_answer refuses new turns: codex exits status 1 with
// "Error: turn/start failed in TUI" on the first input, the tmux session
// dies, and the instance error-loops until the binding is repaired by hand.
//
// Empirical refinement (same incident, later the same day): finalization is
// irrelevant. Codex refuses USER-initiated turns on any thread whose
// session_meta says thread_source=subagent — `codex resume` loads such a
// thread and even auto-continues goal-mode work, but the first typed message
// dies identically. A session bound to a subagent thread therefore can never
// accept operator input, no matter how the binding got there. `codex fork
// <sid>` is the escape: it mints a fresh thread_source=user thread carrying
// the full context, which accepts turns indefinitely.
//
// Three defenses, all keyed off the rollout's session_meta head line:
//  1. Rebind gate — never rebind to a thread whose rollout says
//     thread_source=subagent (shouldRejectCodexSubagentRebind). This guards
//     every id-rotation path that can pick a subagent rollout: the notify
//     hook (a completing subagent fires agent-turn-complete), the
//     live-process FD probe (a codex TUI holds its spawned subagents'
//     rollouts open alongside the main thread), and the cold-start disk scan.
//  2. Restart safety net — when the bound thread is subagent-sourced,
//     buildCodexCommand emits `codex fork <sid>` instead of `codex resume
//     <sid>`. The forked thread keeps the bound thread's entire context (a
//     session legitimately living on an adopted subagent thread loses
//     nothing), and the session-id probe rebinds to the fork's fresh
//     thread_source=user id as soon as the process is up.
//
// The rebind gate stops the poisoning at the source; the safety net is the
// backstop for any binding poisoned before the gate existed or by a path the
// gate does not cover.

// codexThreadMeta is the subset of a rollout's session_meta payload the
// gate/safety-net decisions need.
type codexThreadMeta struct {
	ThreadSource   string
	ParentThreadID string
}

// codexThreadMetaCache memoizes session_meta head reads. A thread's origin
// metadata is immutable for the life of the rollout file, and hook events are
// redelivered every few seconds per session, so positive lookups are cached
// forever. Keyed by session id only: ids are UUIDv7, collision across codex
// homes is not a practical concern. Absence is NOT cached — a rollout may
// flush after the first hook event referencing it.
var codexThreadMetaCache sync.Map // sessionID string -> codexThreadMeta

// codexRolloutPathInHome returns the flushed rollout JSONL path for a session
// ID under codexHome/sessions, or "" when none exists.
//
// Codex layout: codexHome/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl
func codexRolloutPathInHome(sessionID, codexHome string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	pattern := filepath.Join(codexHome, "sessions", "*", "*", "*",
		"rollout-*-"+sessionID+".jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// readCodexRolloutThreadMeta parses the session_meta head line of a rollout.
// Returns the zero value on any read/parse failure (fail-open: an unreadable
// head is treated as a user thread).
func readCodexRolloutThreadMeta(path string) codexThreadMeta {
	f, err := os.Open(path)
	if err != nil {
		return codexThreadMeta{}
	}
	defer f.Close()

	// session_meta lines embed full base_instructions and can exceed
	// bufio.Scanner's 64KB default token size by a wide margin.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	if !scanner.Scan() {
		return codexThreadMeta{}
	}

	var head struct {
		Type    string `json:"type"`
		Payload struct {
			ThreadSource   string          `json:"thread_source"`
			ParentThreadID string          `json:"parent_thread_id"`
			Source         json.RawMessage `json:"source"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &head); err != nil || head.Type != "session_meta" {
		return codexThreadMeta{}
	}

	meta := codexThreadMeta{
		ThreadSource:   head.Payload.ThreadSource,
		ParentThreadID: head.Payload.ParentThreadID,
	}
	// Older payloads carry parenthood only inside source.subagent.thread_spawn.
	if meta.ParentThreadID == "" && len(head.Payload.Source) > 0 {
		var src struct {
			Subagent struct {
				ThreadSpawn struct {
					ParentThreadID string `json:"parent_thread_id"`
				} `json:"thread_spawn"`
			} `json:"subagent"`
		}
		// source is the string "cli" for user threads; ignore unmarshal errors.
		if err := json.Unmarshal(head.Payload.Source, &src); err == nil {
			meta.ParentThreadID = src.Subagent.ThreadSpawn.ParentThreadID
		}
	}
	return meta
}

// codexThreadMetaForSession resolves (with caching) the thread metadata for a
// session id. ok is false when no rollout is flushed for the id yet.
func codexThreadMetaForSession(sessionID, codexHome string) (codexThreadMeta, bool) {
	if v, ok := codexThreadMetaCache.Load(sessionID); ok {
		return v.(codexThreadMeta), true
	}
	path := codexRolloutPathInHome(sessionID, codexHome)
	if path == "" {
		return codexThreadMeta{}, false
	}
	meta := readCodexRolloutThreadMeta(path)
	codexThreadMetaCache.Store(sessionID, meta)
	return meta, true
}

// shouldRejectCodexSubagentRebind reports whether a candidate session id from
// any rotation source (notify hook, live-process FD probe, disk scan) must be
// rejected because it names a subagent-spawned thread. Candidates without a
// flushed rollout are allowed through (fail-open, matching the pre-gate
// behavior for freshly created sessions).
func (i *Instance) shouldRejectCodexSubagentRebind(candidateID string) bool {
	meta, ok := codexThreadMetaForSession(candidateID, i.getCodexHomeDir())
	return ok && meta.ThreadSource == "subagent"
}

// codexSessionNeedsFork reports whether the bound session id names a
// subagent-sourced thread, which `codex resume` would load but never accept
// operator input on. buildCodexCommand launches such bindings with `codex
// fork <sid>` instead: the fork carries the thread's full context into a
// fresh thread_source=user thread, and the live-process probe rebinds the
// instance to the fork's new id once the process is up. Bindings without a
// flushed rollout return false (the #756 existence gate already handled
// them).
func codexSessionNeedsFork(sessionID, codexHome string) bool {
	path := codexRolloutPathInHome(sessionID, codexHome)
	if path == "" {
		return false
	}
	return readCodexRolloutThreadMeta(path).ThreadSource == "subagent"
}
