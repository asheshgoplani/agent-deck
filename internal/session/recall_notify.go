package session

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
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
	if !RecallEnabled() {
		return "", "", false
	}
	return recallNotifyIn(RecallRoots(), path, event, instance)
}

// recallNotifyIn is RecallNotifyTranscript with the roots already
// resolved and [recall] enabled already checked.
func recallNotifyIn(roots []reader.Root, path, event, instance string) (resolved, harness string, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			resolved, harness, ok = "", "", false
		}
	}()
	resolved, harness, ok = recallContainedIn(path, roots)
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

// recallNotifyQueue is the bounded hand-off behind RecallNotifyInstanceAsync:
// one worker goroutine, started on first use, drains it in batches and
// resolves the recall roots once per batch. The transition daemon runs
// every profile's status detection on one goroutine, so the transcript
// resolution (a glob over Codex rollouts, a stat per worker-scratch
// generation, EvalSymlinks per root) must never run inline there.
type recallNotifyReq struct {
	inst  *Instance
	event string
}

var recallNotify struct {
	once sync.Once
	ch   chan recallNotifyReq
}

// recallNotifyQueueCap bounds the hand-off; a full queue drops the notify,
// which costs nothing but freshness: the next sweep walks the roots anyway.
const recallNotifyQueueCap = 64

// RecallNotifyInstanceAsync queues the notify of a lifecycle edge for the
// background worker and returns at once. It reports whether the request
// was accepted (false: recall off, no instance, or the queue is full).
func RecallNotifyInstanceAsync(inst *Instance, event string) bool {
	if inst == nil || !RecallEnabled() {
		return false
	}
	recallNotify.once.Do(func() {
		recallNotify.ch = make(chan recallNotifyReq, recallNotifyQueueCap)
		go recallNotifyWorker(recallNotify.ch)
	})
	select {
	case recallNotify.ch <- recallNotifyReq{inst: inst, event: event}:
		return true
	default:
		return false
	}
}

// recallNotifyWorker drains the queue: everything waiting is one batch
// served with one RecallRoots() walk.
func recallNotifyWorker(ch chan recallNotifyReq) {
	for req := range ch {
		batch := []recallNotifyReq{req}
	drain:
		for {
			select {
			case more := <-ch:
				batch = append(batch, more)
			default:
				break drain
			}
		}
		recallNotifyBatch(batch)
	}
}

// recallNotifyBatch resolves each instance's transcript and queues it,
// walking the roots once for the whole batch.
func recallNotifyBatch(batch []recallNotifyReq) {
	defer func() { _ = recover() }()
	if !RecallEnabled() {
		return
	}
	roots := RecallRoots()
	for _, req := range batch {
		if req.inst == nil || !req.inst.TranscriptIsResolvableLocally() {
			continue
		}
		if path := recallInstanceTranscript(req.inst); path != "" {
			recallNotifyIn(roots, path, req.event, req.inst.ID)
		}
	}
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
