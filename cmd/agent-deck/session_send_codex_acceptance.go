package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func shouldAcquireCodexAcceptanceGuard(inst *session.Instance, jsonOutput, wait, draft bool) bool {
	if inst == nil || !session.IsCodexCompatible(inst.Tool) || draft {
		return false
	}
	structuredWait := jsonOutput && wait
	return structuredWait || inst.CodexRolloutIsResolvableLocally()
}

func codexAcceptanceLockWait(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		timeout = sessionSendDefaultTimeout
	}
	return min(timeout, codexAcceptanceLockTimeout)
}

func retainCodexAcceptanceGuardForCompletion(wait bool, receipt *codexAcceptedTurnReceipt) bool {
	return wait && receipt == nil
}

// codexAcceptanceFence snapshots the last durable rollout turn before a send.
type codexAcceptanceFence struct {
	codexSessionID      string
	priorTurnGeneration string
	available           bool
}

// codexAcceptedTurnReceipt is returned only after Codex submission is
// positively verified. It intentionally contains no prompt or response text.
type codexAcceptedTurnReceipt struct {
	ReceiptID      string `json:"receipt_id"`
	InstanceID     string `json:"instance_id"`
	CodexSessionID string `json:"codex_session_id"`
	TurnGeneration string `json:"turn_generation"`
	AcceptedAt     string `json:"accepted_at"`
}

type codexAcceptanceGuard struct {
	lock   *session.CodexAcceptanceLock
	fence  codexAcceptanceFence
	marker *session.CodexSubmissionMarker
}

func (g *codexAcceptanceGuard) Release() {
	if g != nil && g.lock != nil {
		g.lock.Release()
	}
}

func (g *codexAcceptanceGuard) Prepare(instanceID string, now time.Time) error {
	if g == nil || g.lock == nil || !g.fence.available || g.marker != nil {
		return fmt.Errorf("Codex acceptance guard is not ready to prepare a submission")
	}
	marker, err := session.PrepareCodexSubmissionMarker(
		instanceID, g.fence.codexSessionID, g.fence.priorTurnGeneration, now,
	)
	if err != nil {
		return err
	}
	g.marker = marker
	return nil
}

func (g *codexAcceptanceGuard) RecordTransportOutcome(delivery string, now time.Time) error {
	if g == nil || g.marker == nil {
		return fmt.Errorf("Codex submission marker is unavailable")
	}
	switch delivery {
	case deliveryLineTooLong, deliveryComposerBlocked, deliveryTargetBusy:
		return session.ClearCodexSubmissionMarker(g.marker)
	case deliverySubmitted:
		return g.marker.MarkSubmitted(now)
	default:
		return g.marker.MarkTransportAmbiguous(now)
	}
}

func (g *codexAcceptanceGuard) ResolveAccepted() error {
	if g == nil || g.marker == nil {
		return fmt.Errorf("Codex submission marker is unavailable")
	}
	return session.ClearCodexSubmissionMarker(g.marker)
}

// hydrateLegacyCodexIdentity binds a live, local Codex pane to the thread its
// live Codex process owns before a send establishes acceptance. It repairs two
// cases:
//
//   - an unbound row (it predates durable Codex identity tracking, or a fresh
//     composer owns its thread before any rollout exists);
//   - a stale binding (#2549): Codex restarted or re-authed inside the pane and
//     moved to a new thread, while the row kept the old id, so every send was
//     refused for want of a current rollout generation.
//
// The one user thread the pane's live Codex process holds open is the
// authority; ambiguous evidence (more than one open thread), subagent and
// Guardian review threads (#2529) and a thread another live session owns are
// never adopted. Only an unbound row falls back to the pane environment. Disk
// scans and terminal text are deliberately not identity sources here.
func hydrateLegacyCodexIdentity(
	inst *session.Instance,
	peers []*session.Instance,
	storage *session.Storage,
) error {
	if inst == nil || !session.IsCodexCompatible(inst.Tool) || !inst.CodexRolloutIsResolvableLocally() {
		return nil
	}
	stored := strings.TrimSpace(inst.CodexSessionID)
	live := inst.LiveCodexUserThreadID()
	if stored != "" && (live == "" || live == stored) {
		return nil
	}

	previousID, previousDetectedAt := inst.CodexSessionID, inst.CodexDetectedAt
	restore := func() {
		inst.CodexSessionID = previousID
		inst.CodexDetectedAt = previousDetectedAt
	}

	candidate, processOwned := live, live != ""
	if !processOwned {
		// Panes from earlier builds can carry a disk-scan guess (#2394), so
		// the pane environment only ever fills an empty identity.
		candidate = liveCodexSessionID(inst)
	}
	if candidate == "" {
		return errCodexIdentityUnavailable
	}
	if _, _, err := session.SetField(inst, session.FieldCodexSessionID, candidate, nil); err != nil {
		restore()
		return fmt.Errorf("invalid live Codex session identity: %w", err)
	}

	for _, peer := range peers {
		if peer == nil || peer.ID == inst.ID || !session.IsCodexCompatible(peer.Tool) ||
			!peer.CodexRolloutIsResolvableLocally() || !peer.Exists() {
			continue
		}
		if liveCodexSessionID(peer) == inst.CodexSessionID {
			restore()
			return fmt.Errorf("live Codex session identity is already owned by another session")
		}
	}

	generation, err := inst.LatestCodexTurnGeneration()
	if err != nil {
		restore()
		return fmt.Errorf("live Codex session identity has no unique current rollout: %w", err)
	}
	if strings.TrimSpace(generation) == "" && !processOwned {
		restore()
		return fmt.Errorf("live Codex session identity current turn generation is unavailable")
	}
	if storage == nil || storage.GetDB() == nil {
		restore()
		return fmt.Errorf("cannot persist live Codex session identity")
	}
	if err := storage.GetDB().WriteCodexSessionBinding(inst.ID, inst.CodexSessionID, inst.CodexDetectedAt); err != nil {
		restore()
		return fmt.Errorf("persist live Codex session identity: %w", err)
	}
	if processOwned {
		// The pane environment is what status passes read first; leaving the
		// old id there would bind the session straight back to it.
		if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil && tmuxSess.Exists() {
			_ = tmuxSess.SetEnvironment("CODEX_SESSION_ID", inst.CodexSessionID)
		}
	}
	if stored != "" {
		_ = session.WriteSessionIDLifecycleEvent(session.SessionIDLifecycleEvent{
			InstanceID: inst.ID, Tool: inst.Tool, Action: "rebind", Source: "send_live_process",
			OldID: stored, NewID: inst.CodexSessionID, Reason: "live_process_owns_new_thread",
		})
	}
	return nil
}

// codexComposerFallbackAllowed reports whether a send whose acceptance
// guard failed with err may go through the composer instead of refusing:
// only with --codex-composer-fallback, never for a structured --json --wait,
// and only when the identity is provably unavailable. Contested identity
// (an unresolved earlier submission, another session's thread, the
// acceptance lock held by another send, a remote rollout) always refuses.
func codexComposerFallbackAllowed(err error, flag, structuredWait bool) bool {
	if err == nil || !flag || structuredWait {
		return false
	}
	return codexIdentityProvablyUnavailable(err)
}

// codexIdentityProvablyUnavailable reports whether an acceptance error is
// one of the two that mean the Codex identity is provably unavailable.
func codexIdentityProvablyUnavailable(err error) bool {
	return errors.Is(err, errCodexIdentityUnavailable) || errors.Is(err, errCodexGenerationUnavailable)
}

// codexAcceptanceRefusalData is the JSON data of a refused Codex send.
// acceptance_unavailable marks the provably unavailable identity (the class
// --codex-composer-fallback accepts), which waiting does not cure the way it
// cures contention; the send queue fails fast on it once it persists (#2549).
func codexAcceptanceRefusalData(err error) map[string]interface{} {
	return map[string]interface{}{
		"delivery":               deliveryAcceptanceRefused,
		"acceptance_unavailable": codexIdentityProvablyUnavailable(err),
	}
}

// liveCodexSessionID reads only the authoritative Codex identity from a live
// pane. Unlike broad session-ID synchronization, it does not mutate metadata
// for Codex or any unrelated tool.
func liveCodexSessionID(inst *session.Instance) string {
	if inst == nil {
		return ""
	}
	tmuxSession := inst.GetTmuxSession()
	if tmuxSession == nil || !tmuxSession.Exists() {
		return ""
	}
	identity, err := tmuxSession.GetEnvironment("CODEX_SESSION_ID")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(identity)
}

func acquireCodexAcceptanceGuard(inst *session.Instance, timeout time.Duration) (*codexAcceptanceGuard, error) {
	if inst == nil || !session.IsCodexCompatible(inst.Tool) {
		return nil, fmt.Errorf("target is not Codex-compatible")
	}
	if !inst.CodexRolloutIsResolvableLocally() {
		return nil, fmt.Errorf("exact rollout is unavailable for remote or sandboxed sessions")
	}
	if strings.TrimSpace(inst.CodexSessionID) == "" {
		return nil, errCodexIdentityUnavailable
	}
	lock, err := session.AcquireCodexAcceptanceLock(inst.CodexSessionID, timeout)
	if err != nil {
		return nil, err
	}
	fence := captureCodexAcceptanceFence(inst)
	if !fence.available {
		lock.Release()
		return nil, errCodexGenerationUnavailable
	}
	if _, err := session.ReconcileCodexSubmissionMarker(
		inst.ID, inst.CodexSessionID, fence.priorTurnGeneration, inst.CodexTurnTookInputSince,
	); err != nil {
		lock.Release()
		return nil, err
	}
	return &codexAcceptanceGuard{lock: lock, fence: fence}, nil
}

func validateCodexAcceptanceFence(inst *session.Instance, fence codexAcceptanceFence) error {
	if inst == nil || !fence.available || inst.CodexSessionID != fence.codexSessionID {
		return fmt.Errorf("acceptance fence is unavailable")
	}
	current, err := inst.LatestCodexTurnGeneration()
	if err != nil {
		return err
	}
	if current != fence.priorTurnGeneration {
		return fmt.Errorf("another turn started before submission")
	}
	return nil
}

func requireStructuredCodexAcceptedTurn(
	inst *session.Instance,
	jsonOutput, wait bool,
	receipt *codexAcceptedTurnReceipt,
) error {
	if !jsonOutput || !wait || inst == nil || !session.IsCodexCompatible(inst.Tool) {
		return nil
	}
	if receipt == nil {
		return fmt.Errorf("submitted Codex turn has no exact accepted generation; refusing uncorrelated output")
	}
	return nil
}

func retryAndRequireStructuredCodexAcceptedTurn(
	inst *session.Instance,
	jsonOutput, wait bool,
	receipt *codexAcceptedTurnReceipt,
	delivery string,
	acceptedAt time.Time,
	fence codexAcceptanceFence,
) (*codexAcceptedTurnReceipt, error) {
	if receipt == nil {
		receipt = waitForAcceptedCodexTurn(inst, delivery, acceptedAt, fence)
	}
	return receipt, requireStructuredCodexAcceptedTurn(inst, jsonOutput, wait, receipt)
}

// codexTurnAdvancedPastFence reports whether the exact rollout has started a
// turn after the acceptance fence was captured.
func codexTurnAdvancedPastFence(inst *session.Instance, fence codexAcceptanceFence) bool {
	if inst == nil || !fence.available || inst.CodexSessionID != fence.codexSessionID {
		return false
	}
	generation, err := inst.LatestCodexTurnGeneration()
	return err == nil && generation != "" && generation != fence.priorTurnGeneration
}

func captureCodexAcceptanceFence(inst *session.Instance) codexAcceptanceFence {
	if inst == nil || !session.IsCodexCompatible(inst.Tool) {
		return codexAcceptanceFence{}
	}
	if inst.CodexSessionID == "" {
		return codexAcceptanceFence{}
	}
	generation, err := inst.LatestCodexTurnGeneration()
	if err != nil {
		return codexAcceptanceFence{}
	}
	return codexAcceptanceFence{
		codexSessionID:      inst.CodexSessionID,
		priorTurnGeneration: generation,
		available:           true,
	}
}

func newCodexAcceptedTurnReceipt(
	inst *session.Instance,
	delivery string,
	acceptedAt time.Time,
	fence codexAcceptanceFence,
	turnGeneration string,
) *codexAcceptedTurnReceipt {
	if inst == nil || !session.IsCodexCompatible(inst.Tool) ||
		delivery != deliverySubmitted || !fence.available ||
		inst.CodexSessionID == "" || inst.CodexSessionID != fence.codexSessionID ||
		turnGeneration == "" || turnGeneration == fence.priorTurnGeneration ||
		!strings.HasPrefix(turnGeneration, inst.CodexSessionID+":") {
		return nil
	}
	accepted := acceptedAt.UTC().Format(time.RFC3339Nano)
	return &codexAcceptedTurnReceipt{
		ReceiptID:      uuid.NewString(),
		InstanceID:     inst.ID,
		CodexSessionID: inst.CodexSessionID,
		TurnGeneration: turnGeneration,
		AcceptedAt:     accepted,
	}
}

// observeAcceptedCodexTurn returns the accepted-turn receipt for a `session
// send`, but only when the caller can actually consume it. A `--wait` send
// reads accepted_turn immediately (for the wait&&json eager-ack skip) and
// again at the completion boundary, so it pays the exact-generation poll. A
// non-wait send (including --no-wait heartbeats and plain human sends)
// never reads accepted_turn, and the acceptance guard's own release logic
// (retainCodexAcceptanceGuardForCompletion) already treats every non-wait
// send identically whether or not this observation ran — so skipping it
// changes no correctness guarantee, only removes a blocking wait.
//
// Before this gate, every local Codex send blocked here for up to
// codexAcceptedTurnPollTimeout regardless of --wait/--no-wait, adding
// latency to --no-wait heartbeats and nudges (issue #2279 review).
func observeAcceptedCodexTurn(
	wait bool,
	inst *session.Instance,
	delivery string,
	acceptedAt time.Time,
	fence codexAcceptanceFence,
) *codexAcceptedTurnReceipt {
	if !wait {
		return nil
	}
	return waitForAcceptedCodexTurn(inst, delivery, acceptedAt, fence)
}

func waitForAcceptedCodexTurn(
	inst *session.Instance,
	delivery string,
	acceptedAt time.Time,
	fence codexAcceptanceFence,
) *codexAcceptedTurnReceipt {
	if delivery != deliverySubmitted || !fence.available {
		return nil
	}
	deadline := time.Now().Add(codexAcceptedTurnPollTimeout)
	for {
		generation, err := inst.LatestCodexTurnGeneration()
		if err == nil {
			if receipt := newCodexAcceptedTurnReceipt(inst, delivery, acceptedAt, fence, generation); receipt != nil {
				return receipt
			}
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		time.Sleep(codexAcceptedTurnPollInterval)
	}
}
