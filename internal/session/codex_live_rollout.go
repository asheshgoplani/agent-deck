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
// The live rollout is therefore resolved, read-only, on every call:
//
//  1. the exact rollout of the stored id, when it is a user thread;
//  2. else the user-thread rollouts (session_meta thread_source != subagent,
//     no parent thread) whose cwd is the session's working directory and
//     that were written since the session was created: the one that
//     mentions the stored id wins, else the most recently written one.
//
// This never rebinds codex_session_id: identity for accepted-turn receipts
// stays with the pane environment (hydrateLegacyCodexIdentity).

type codexRolloutHead struct {
	ID       string
	Cwd      string
	Subagent bool
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
	return codexRolloutHead{ID: p.ID, Cwd: p.Cwd, Subagent: sub}, true
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
// after since, newest first. Only the day directories between since and now
// are globbed.
func codexUserRolloutsForCwd(codexHome, cwd string, since time.Time) []codexRolloutCandidate {
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
			if !ok || head.Subagent || canonicalDir(head.Cwd) != want {
				continue
			}
			out = append(out, codexRolloutCandidate{path: m, id: head.ID, mtime: info.ModTime()})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].mtime.After(out[j].mtime) })
	return out
}

// fileMentions reports whether the first 4 MiB of path contain needle.
func fileMentions(path, needle string) bool {
	if needle == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 4<<20))
	return bytes.Contains(b, []byte(needle))
}

// CodexLiveRolloutPath is the rollout a Codex session writes now, or "".
func CodexLiveRolloutPath(inst *Instance) string {
	if inst == nil || !IsCodexCompatible(inst.Tool) || !inst.CodexRolloutIsResolvableLocally() {
		return ""
	}
	home := CodexHomeDirForInstance(inst)
	if p := codexRolloutPathInHome(inst.CodexSessionID, home); p != "" {
		if head, ok := readCodexRolloutHead(p); !ok || !head.Subagent {
			return p
		}
	}
	cands := codexUserRolloutsForCwd(home, inst.EffectiveWorkingDir(), inst.CreatedAt)
	if len(cands) == 0 {
		return ""
	}
	for _, c := range cands {
		if fileMentions(c.path, inst.CodexSessionID) {
			return c.path
		}
	}
	return cands[0].path
}

// TranscriptIDs lists every native conversation id seen for the session,
// newest first: for Codex the live and earlier user-thread rollouts in its
// directory since it was created plus the stored id; for Claude the stored
// session id.
func TranscriptIDs(inst *Instance) []string {
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
		if p := CodexLiveRolloutPath(inst); p != "" {
			if head, ok := readCodexRolloutHead(p); ok {
				add(head.ID)
			}
		}
		for _, c := range codexUserRolloutsForCwd(CodexHomeDirForInstance(inst), inst.EffectiveWorkingDir(), inst.CreatedAt) {
			add(c.id)
		}
		add(inst.CodexSessionID)
	case IsClaudeCompatible(inst.Tool):
		add(inst.ClaudeSessionID)
	}
	return ids
}
