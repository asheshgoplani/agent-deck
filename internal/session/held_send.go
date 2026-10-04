package session

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Held send origin (issue #2473 with the comms redesign PR5 reply routing).
//
// A tagged `session send` whose turn ends by handing off to background work
// (a Workflow, background agents) has its Stop held: the turn is not
// finished, so nothing is recorded for it. That held turn is the only one
// that carries trigger send and the sender's id. The turn that settles the
// session later is the task notification turn, which carries no sender, so
// without this record the session that asked would never get its answer.
//
// The daemon remembers the held send's sender per child here and stamps it
// on the turn that settles the work, so the sender receives the actual
// result. The record lives on disk so a notify-daemon restart in the middle
// of a long workflow does not lose it.
//
// Layout: <data>/runtime/held-send/<child>.json, one small JSON object.

// heldSendMaxAge bounds how long a remembered sender waits for the work to
// end. A child that never reports back (killed, removed) must not stamp a
// sender on an unrelated turn days later.
const heldSendMaxAge = 24 * time.Hour

// heldSendOrigin is the sender of a held tagged send.
type heldSendOrigin struct {
	FromID string    `json:"from_id"`
	UUID   string    `json:"uuid,omitempty"`
	HeldAt time.Time `json:"held_at"`
}

func heldSendPath(childID string) string {
	return filepath.Join(runtimeDirOrTemp("held-send"), sanitizeInboxName(childID)+".json")
}

// rememberHeldSend records the sender of a held turn when that turn is a
// tagged send that has not been recorded yet. Any other held turn (a human
// prompt, an intermediate task notification) leaves an existing record
// alone: the work it started is still in flight and the sender is still
// owed the result.
func rememberHeldSend(childID string, facts TurnFacts) {
	from := strings.TrimSpace(facts.FromID)
	if facts.Pending || facts.Trigger != TurnTriggerSend || from == "" || strings.TrimSpace(childID) == "" {
		return
	}
	if prev := loadHeldSend(childID); prev != nil && prev.FromID == from && prev.UUID == facts.UUID {
		return // already remembered; the hold re-fires every poll
	}
	// A send turn that was already recorded has replied to its sender (and
	// emitTurn cleared its record). The session can go back to running on the
	// same work with that turn still last, after a permission menu or a lapsed
	// hold; remembering it again would answer the sender a second time.
	if last := LastTurnJournalEntry(childID); last != nil && last.UUID != "" && last.UUID == facts.UUID {
		return
	}
	writeHeldSendOrigin(heldSendPath(childID), childID, heldSendOrigin{FromID: from, UUID: facts.UUID, HeldAt: time.Now()})
}

// loadHeldSend returns the remembered sender for childID, or nil when none
// is remembered or the record is older than heldSendMaxAge.
func loadHeldSend(childID string) *heldSendOrigin {
	return readHeldSendOrigin(heldSendPath(childID), childID)
}

func clearHeldSend(childID string) {
	removeHeldSendOrigin(heldSendPath(childID), childID)
}

// The Comms Ledger keeps its own owed sender per child, in a separate
// directory, so the inbox's gating and clearing never decide what the ledger
// answers (see commsHeldSendReplyTo). Same record, same max age.
//
// Layout: <data>/runtime/held-send-ledger/<child>.json.
func ledgerOwedSenderPath(childID string) string {
	return filepath.Join(runtimeDirOrTemp("held-send-ledger"), sanitizeInboxName(childID)+".json")
}

func rememberLedgerOwedSender(childID, from string) {
	if strings.TrimSpace(childID) == "" || strings.TrimSpace(from) == "" {
		return
	}
	writeHeldSendOrigin(ledgerOwedSenderPath(childID), childID, heldSendOrigin{FromID: from, HeldAt: time.Now()})
}

// loadLedgerOwedSender returns the sender the ledger owes childID a held
// send's result, or "" when none.
func loadLedgerOwedSender(childID string) string {
	if origin := readHeldSendOrigin(ledgerOwedSenderPath(childID), childID); origin != nil {
		return origin.FromID
	}
	return ""
}

func clearLedgerOwedSender(childID string) {
	removeHeldSendOrigin(ledgerOwedSenderPath(childID), childID)
}

func writeHeldSendOrigin(path, childID string, origin heldSendOrigin) {
	data, err := json.Marshal(origin)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	if err == nil {
		err = atomicWriteFile(path, data, 0o600)
	}
	if err != nil {
		commsLog.Warn("held_send_write_failed", slog.String("child", childID), slog.String("path", path), slog.String("error", err.Error()))
	}
}

func readHeldSendOrigin(path, childID string) *heldSendOrigin {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var origin heldSendOrigin
	if err := json.Unmarshal(data, &origin); err != nil || strings.TrimSpace(origin.FromID) == "" {
		return nil
	}
	if !origin.HeldAt.IsZero() && time.Since(origin.HeldAt) > heldSendMaxAge {
		removeHeldSendOrigin(path, childID)
		return nil
	}
	return &origin
}

func removeHeldSendOrigin(path, childID string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		commsLog.Warn("held_send_clear_failed", slog.String("child", childID), slog.String("path", path), slog.String("error", err.Error()))
	}
}

// eventAnswersSend reports whether event answers a tagged send: the send turn
// itself, or the task turn that settled background work a held send started
// (FromID carried over from the held send).
func eventAnswersSend(event TransitionNotificationEvent) bool {
	if strings.TrimSpace(event.FromID) == "" {
		return false
	}
	return event.Trigger == TurnTriggerSend || event.Trigger == TurnTriggerTask
}
