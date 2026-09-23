package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

// Codex live rollout tracking (macapp-core-needs §3). Codex re-creates its
// rollout after the trust prompt, and a spawned sub-agent writes a rollout of
// its own beside the main one, so the stored codex_session_id can name a
// thread with no rollout (01a0cd46 in the Mac app receipt) while the live
// conversation sits in another file (01a0cd45; 01a0cd47 was the sub-agent).
// The live rollout is therefore resolved, read-only, on every call, and only
// ever to this session's own thread:
//
//  1. the exact rollout of the stored id, when it is not a sub-agent thread;
//  2. else the thread the pane's own Codex process holds open
//     (LiveCodexThreadID), when its rollout is a user thread;
//  3. else the user-thread rollouts (session_meta thread_source "user", no
//     parent thread, not `codex exec`) whose cwd is the session's working
//     directory, written since the session was created, and not bound to
//     another session: the one whose structured fields reference the stored
//     id, else the only one. More than one candidate is ambiguous (another
//     deck session, the user's own codex run) and resolves to "".
//
// This never rebinds codex_session_id: identity for accepted-turn receipts
// stays with the pane environment (hydrateLegacyCodexIdentity).

type codexRolloutHead struct {
	ID       string
	Cwd      string
	Subagent bool
	// UserThread: thread_source "user", no parent thread, not `codex exec`.
	UserThread bool
}

func readCodexRolloutHead(path string) (codexRolloutHead, bool) {
	f, err := os.Open(path)
	if err != nil {
		return codexRolloutHead{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	if !sc.Scan() {
		return codexRolloutHead{}, false
	}
	var head struct {
		Type    string `json:"type"`
		Payload struct {
			ID             string          `json:"id"`
			Cwd            string          `json:"cwd"`
			ThreadSource   string          `json:"thread_source"`
			ParentThreadID string          `json:"parent_thread_id"`
			Source         json.RawMessage `json:"source"`
		} `json:"payload"`
	}
	if json.Unmarshal(sc.Bytes(), &head) != nil || head.Type != "session_meta" {
		return codexRolloutHead{}, false
	}
	p := head.Payload
	sub := p.ThreadSource == "subagent" || p.ParentThreadID != "" || bytes.Contains(p.Source, []byte(`"subagent"`))
	var source string
	_ = json.Unmarshal(p.Source, &source)
	user := !sub && p.ThreadSource == "user" && source != "exec"
	return codexRolloutHead{ID: p.ID, Cwd: p.Cwd, Subagent: sub, UserThread: user}, true
}

func canonicalDir(p string) string {
	if p == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(p)
}

type codexRolloutCandidate struct {
	path, id string
	mtime    time.Time
}

// codexUserRolloutsForCwd lists user-thread rollouts in cwd written at or
// after since, newest first, skipping threads in owned. Only the day
// directories between since and now are globbed.
func codexUserRolloutsForCwd(codexHome, cwd string, since time.Time, owned map[string]bool) []codexRolloutCandidate {
	want := canonicalDir(cwd)
	if want == "" || codexHome == "" {
		return nil
	}
	now := time.Now()
	start := since
	if start.IsZero() || now.Sub(start) > 7*24*time.Hour {
		start = now.Add(-48 * time.Hour)
	}
	seenDay := map[string]bool{}
	var out []codexRolloutCandidate
	for d := start.Add(-24 * time.Hour); !d.After(now.Add(24 * time.Hour)); d = d.Add(24 * time.Hour) {
		dir := filepath.Join(codexHome, "sessions", d.Format("2006"), d.Format("01"), d.Format("02"))
		if seenDay[dir] {
			continue
		}
		seenDay[dir] = true
		matches, _ := filepath.Glob(filepath.Join(dir, "rollout-*.jsonl"))
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil || (!since.IsZero() && info.ModTime().Before(since.Add(-time.Minute))) {
				continue
			}
			head, ok := readCodexRolloutHead(m)
			if !ok || !head.UserThread || owned[head.ID] || canonicalDir(head.Cwd) != want {
				continue
			}
			out = append(out, codexRolloutCandidate{path: m, id: head.ID, mtime: info.ModTime()})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].mtime.After(out[j].mtime) })
	return out
}

// codexThreadRefFields are the rollout payload fields that name a thread or
// turn. Message text is never searched: a rollout that quotes an id in chat
// does not reference that thread.
var codexThreadRefFields = []string{"id", "session_id", "thread_id", "turn_id", "conversation_id", "forked_from_id", "previous_thread_id"}

// rolloutReferencesThread reports whether a line in the first 4 MiB of path
// carries threadID in one of its payload's thread/turn id fields.
func rolloutReferencesThread(path, threadID string) bool {
	if threadID == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 4<<20))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	needle := []byte(threadID)
	for sc.Scan() {
		if !bytes.Contains(sc.Bytes(), needle) {
			continue
		}
		var line struct {
			Payload map[string]json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		for _, k := range codexThreadRefFields {
			var v string
			if json.Unmarshal(line.Payload[k], &v) == nil && v == threadID {
				return true
			}
		}
	}
	return false
}

// codexThreadsOwnedByPeers lists the Codex threads other sessions are bound to.
func codexThreadsOwnedByPeers(inst *Instance, peers []*Instance) map[string]bool {
	owned := map[string]bool{}
	for _, p := range peers {
		if p != nil && p.ID != inst.ID && IsCodexCompatible(p.Tool) && p.CodexSessionID != "" && p.CodexSessionID != inst.CodexSessionID {
			owned[p.CodexSessionID] = true
		}
	}
	return owned
}

// CodexLiveRolloutPath is the rollout a Codex session writes now, or "".
// peers are the profile's other sessions: a thread bound to one of them is
// never this session's.
func CodexLiveRolloutPath(inst *Instance, peers []*Instance) string {
	if inst == nil || !IsCodexCompatible(inst.Tool) || !inst.CodexRolloutIsResolvableLocally() {
		return ""
	}
	home := CodexHomeDirForInstance(inst)
	if p := codexRolloutPathInHome(inst.CodexSessionID, home); p != "" {
		if head, ok := readCodexRolloutHead(p); !ok || !head.Subagent {
			return p
		}
	}
	owned := codexThreadsOwnedByPeers(inst, peers)
	if live := inst.LiveCodexThreadID(); live != "" && !owned[live] {
		if p := codexRolloutPathInHome(live, home); p != "" {
			if head, ok := readCodexRolloutHead(p); ok && head.UserThread {
				return p
			}
		}
	}
	cands := codexUserRolloutsForCwd(home, inst.EffectiveWorkingDir(), inst.CreatedAt, owned)
	var refs []codexRolloutCandidate
	for _, c := range cands {
		if rolloutReferencesThread(c.path, inst.CodexSessionID) {
			refs = append(refs, c)
		}
	}
	switch {
	case len(refs) == 1:
		return refs[0].path
	case len(refs) == 0 && len(cands) == 1:
		return cands[0].path
	}
	return ""
}

// TranscriptIDs lists the native conversation ids of the session, newest
// first: for Codex the live rollout's thread and the stored id; for Claude
// the stored session id. Rollouts of other threads in the same directory
// are never listed.
func TranscriptIDs(inst *Instance, peers []*Instance) []string {
	if inst == nil {
		return nil
	}
	var ids []string
	add := func(id string) {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	switch {
	case IsCodexCompatible(inst.Tool) && inst.CodexRolloutIsResolvableLocally():
		if p := CodexLiveRolloutPath(inst, peers); p != "" {
			if head, ok := readCodexRolloutHead(p); ok {
				add(head.ID)
			}
		}
		add(inst.CodexSessionID)
	case IsClaudeCompatible(inst.Tool):
		add(inst.ClaudeSessionID)
	}
	return ids
}
