package session

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/recall"
)

// Recall triggers (docs/recall.md, phase 3). Nothing here opens recall.db
// or takes the sweep lock: a trigger appends one line to the hook queue
// and returns. Every function is safe to call from a hook, a CLI verb or
// the transition daemon, is a no-op while [recall] enabled = false, and
// recovers from any panic so recall can never take down the caller.

// RecallNotifyTranscript queues one transcript path for the next sweep
// after the recall containment check. It returns the resolved path and
// the harness that owns it, or ok=false when the path was refused or
// recall is off. event and instance are recorded on the queue line.
func RecallNotifyTranscript(path, event, instance string) (resolved, harness string, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			resolved, harness, ok = "", "", false
		}
	}()
	if !RecallEnabled() {
		return "", "", false
	}
	resolved, harness, ok = RecallContainedPath(path)
	if !ok {
		return "", "", false
	}
	qp, err := recall.QueuePath()
	if err != nil {
		return "", "", false
	}
	if err := recall.Enqueue(qp, recall.QueueEntry{Harness: harness, Path: resolved, Event: event, Instance: instance}); err != nil {
		return "", "", false
	}
	return resolved, harness, true
}

// RecallNotifyInstance queues the transcript of a managed session on a
// lifecycle edge (`session stop`, restart, worker_done). Remote sessions
// never resolve a local transcript (TranscriptIsResolvableLocally), so
// they queue nothing.
func RecallNotifyInstance(inst *Instance, event string) bool {
	defer func() { _ = recover() }()
	if inst == nil || !RecallEnabled() || !inst.TranscriptIsResolvableLocally() {
		return false
	}
	path := recallInstanceTranscript(inst)
	if path == "" {
		return false
	}
	_, _, ok := RecallNotifyTranscript(path, event, inst.ID)
	return ok
}

// RecallEnabled reads [recall] enabled from the user config.
func RecallEnabled() bool {
	cfg, err := LoadUserConfig()
	return err == nil && cfg != nil && cfg.Recall.GetEnabled()
}

// recallInstanceTranscript resolves the transcript file of a local
// instance per harness: the Claude jsonl under the instance's config dir,
// the Codex rollout, or the newest pi session file in the instance's
// agent-deck directory. "" when nothing is on disk yet.
func recallInstanceTranscript(inst *Instance) string {
	switch {
	case IsClaudeCompatible(inst.Tool) && inst.ClaudeSessionID != "":
		return ResolveClaudeTranscriptPath(GetClaudeConfigDirForInstance(inst), inst.ProjectPath, inst.ClaudeSessionID)
	case IsCodexCompatible(inst.Tool):
		return CodexRolloutPathForInstance(inst)
	case inst.Tool == "pi":
		dir, err := piInstanceSessionDir(inst.ID)
		if err != nil {
			return ""
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return ""
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
				names = append(names, e.Name())
			}
		}
		if len(names) == 0 {
			return ""
		}
		sort.Strings(names) // timestamp-prefixed: the last is the newest
		return filepath.Join(dir, names[len(names)-1])
	}
	return ""
}
