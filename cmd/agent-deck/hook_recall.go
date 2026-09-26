package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/ingest"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// recallHookTrigger is the Claude hook side of Recall's triggers
// (docs/recall.md, phase 3). On Stop and SessionEnd it appends the
// session's transcript to the hook queue after the recall containment
// check (RecallContainedPath: every Claude config dir plus the
// worker-scratch root, symlink-resolved, fail-closed). Stop is installed
// synchronous and sits on Claude's turn-end latency, so that is ALL the
// Stop path does: one appended line, no database, no lock (FINAL-DESIGN
// §6 trigger 1). SessionEnd is asynchronous; there, when [recall]
// hook_sweep is on and the sweep lock is free, the hook also indexes ONLY
// that file within the interactive budget (150 ms / 32 MB). Everything is
// behind recover() and gated on [recall] enabled: a hook must never fail
// and must never block Claude on recall work.
func recallHookTrigger(instanceID, event string, payload []byte) {
	defer func() {
		if r := recover(); r != nil {
			hookHandlerLog.Warn("recall_hook_panic", slog.Any("panic", r))
		}
	}()
	key := normalizeHookEventKey(event)
	if key != "stop" && key != "sessionend" {
		return
	}
	var stop stopHookPayload
	if json.Unmarshal(payload, &stop) != nil || stop.TranscriptPath == "" {
		return
	}
	path, _, ok := session.RecallNotifyTranscript(stop.TranscriptPath, event, instanceID)
	if !ok || key != "sessionend" {
		return
	}
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil || !cfg.Recall.GetHookSweep() {
		return
	}
	res, err := recallSweepFile(cfg, path)
	if err != nil {
		logCostDebug("recall hook sweep skipped: %v", err)
		return
	}
	logCostDebug("recall hook sweep: %s parsed=%d deferred=%d msgs=%d %dms", path, res.Parsed, res.Deferred, res.Messages, res.ElapsedMS)
}

// recallHookDeadline is the hook sweep's time budget. A variable only so a
// test can remove timing from an assertion about what gets indexed.
var recallHookDeadline = ingest.InteractiveDeadline

// recallSweepFile indexes one transcript under the interactive budget. It
// returns store.ErrLocked without waiting when a sweep is running; the
// queued line is then picked up by that sweep or the next.
func recallSweepFile(cfg *session.UserConfig, path string) (ingest.Result, error) {
	dbPath, err := recall.DBPath()
	if err != nil {
		return ingest.Result{}, err
	}
	lockPath, err := recall.LockPath()
	if err != nil {
		return ingest.Result{}, err
	}
	release, err := store.Lock(lockPath)
	if err != nil {
		return ingest.Result{}, err
	}
	defer release()
	st, err := store.Open(dbPath)
	if err != nil {
		return ingest.Result{}, err
	}
	defer st.Close()
	opts := ingest.Options{
		Roots:          session.RecallRoots(),
		TextTier:       cfg.Recall.GetTextTier(),
		PerSourceBytes: int64(cfg.Recall.GetPerSourceMB()) << 20,
		Budget:         reader.NewBudget(recallHookDeadline, ingest.InteractiveBytes),
	}
	// The profile's state.db supplies the deck id, hints and tags the card
	// carries; without it the projection would blank them until the next
	// full sweep. No usage sink: the hook wrote this turn's cost event
	// itself a moment ago.
	if storage, err := session.NewStorageWithProfile(os.Getenv("AGENTDECK_PROFILE")); err == nil {
		defer storage.Close()
		reg := session.NewRecallRegistry(storage.Profile(), storage.GetDB())
		defer reg.Close()
		opts.Registry = reg
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*recallHookDeadline+time.Second)
	defer cancel()
	return ingest.New(st, opts).SweepFiles(ctx, []string{path})
}
