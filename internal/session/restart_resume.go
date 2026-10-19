package session

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A restart that only resumes the conversation (the idle build refresh, or a
// `session restart` of a session at its prompt) makes the child boot and
// settle at its prompt again: a running->waiting flip with no new turn.
// emitTurn reads an observed flip over an unchanged transcript as a real turn
// whose signal is stale (#2184) and wakes the parent with an urgent record, so
// every refresh restart of a child used to send its parent a false "waiting"
// completion (#2792).
//
// So a restart first notes where the child's transcript ends (or, with no
// transcript, its turn signal: the Codex completion) in
// runtime/restart-resume/<id>.json. Until a real turn shows up, every flip of
// the child is checked against it, on every path (the snapshot edge and the
// hook path both see the boot, and the hook path also sees the old process
// die as an error): a flip whose transcript gained no user or assistant
// record (a resume appends only metadata), or whose signal is unchanged, is
// the boot and reaches no inbox. An error counts as the restart's own only
// until shortly after the restart returned. An idle build refresh of a
// parented child gives one info-tier "refreshed onto <build>" line instead.
// The first flip that is not the boot (a new turn, a later error) spends the
// note and is delivered as before. The size, not the turn identity, is what
// a transcript whose tail has no finished turn (a compaction summary, say)
// is keyed on.

// restartResumeTTL bounds how long after a restart its boot flip may come.
const restartResumeTTL = 10 * time.Minute

// restartResumeErrorSlack is how long after a restart returned an error flip
// still counts as the restart's teardown (the daemon observes it a poll or a
// hook write later).
const restartResumeErrorSlack = 15 * time.Second

// restartResumeMaxScan caps the transcript bytes read past the note; a
// boot appends a few hundred, so more is treated as new activity.
const restartResumeMaxScan = 1 << 20

// RestartReasonIdleBuildRefresh is the restart reason of `session restart
// --if-idle`, the idle build refresh.
const RestartReasonIdleBuildRefresh = "idle build refresh"

type restartResumeNote struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
	Tool   string    `json:"tool,omitempty"`
	Build  string    `json:"build,omitempty"`
	// Transcript and Offset are the transcript file and its size at the
	// restart; Signal is the turn signal for a tool without one.
	Transcript string `json:"transcript,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
	Signal     string `json:"signal,omitempty"`
	// RestartedAt is when the restart returned (zero while it runs);
	// Announced marks the refresh info line as sent.
	RestartedAt time.Time `json:"restarted_at,omitempty"`
	Announced   bool      `json:"announced,omitempty"`
}

// SetRestartReason names why the next restart happens, for its resume note.
func (i *Instance) SetRestartReason(reason string) { i.restartReason = reason }

func restartResumePath(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	return runtimeDataPath(filepath.Join("restart-resume", id+".json"))
}

// noteRestartResume records where the child's turns stand before a restart.
// Only a child idle at its prompt gets a note: a restart that cuts a turn
// short (running, a tool call newer than the last text, a reply still
// flushing) must still tell the parent the child stopped, and so must one
// with nothing to compare against (no transcript or signal).
func noteRestartResume(i *Instance, now time.Time) {
	if i == nil {
		return
	}
	switch i.GetStatusThreadSafe() {
	case StatusWaiting, StatusIdle:
	default:
		return
	}
	if facts, ok := instanceTurnFacts(i); ok && (facts.Pending || facts.FinalTextPending) {
		return
	}
	note := restartResumeNote{At: now, Reason: i.restartReason, Tool: i.Tool}
	if path := i.GetJSONLPath(); path != "" {
		if info, err := os.Stat(path); err == nil {
			note.Transcript, note.Offset = path, info.Size()
		}
	}
	if note.Transcript == "" {
		if note.Signal = transitionEventOutputHash(i); note.Signal == "" {
			return
		}
	}
	writeRestartResumeNote(i.ID, note)
}

// annotateRestartResumeBuild adds the build an idle refresh restarted onto to
// the child's note, for the info line.
func annotateRestartResumeBuild(id, build string) {
	updateRestartResumeNote(id, func(n *restartResumeNote) { n.Build = build })
}

// markRestartResumed stamps the note with the time the restart returned.
func markRestartResumed(id string, at time.Time) {
	updateRestartResumeNote(id, func(n *restartResumeNote) { n.RestartedAt = at })
}

// updateRestartResumeNote rewrites the child's note, never bringing back
// one that was spent in the meantime.
func updateRestartResumeNote(id string, edit func(*restartResumeNote)) {
	note, ok := readRestartResumeNote(id)
	if !ok {
		return
	}
	edit(&note)
	writeRestartResumeNoteIf(id, note, true)
}

func writeRestartResumeNote(id string, note restartResumeNote) {
	writeRestartResumeNoteIf(id, note, false)
}

// writeRestartResumeNoteIf writes the note; with mustExist only over a note
// still on disk, so an annotation never brings back one the daemon spent.
func writeRestartResumeNoteIf(id string, note restartResumeNote, mustExist bool) {
	path, err := restartResumePath(id)
	if err != nil {
		return
	}
	data, err := json.Marshal(note)
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			tmp := path + ".tmp"
			if err = os.WriteFile(tmp, data, 0o600); err == nil {
				if _, serr := os.Stat(path); mustExist && serr != nil {
					_ = os.Remove(tmp)
					return
				}
				err = os.Rename(tmp, path)
			}
		}
	}
	if err != nil {
		sessionLog.Warn("restart_resume_note_failed", slog.String("instance_id", id), slog.String("error", err.Error()))
	}
}

func readRestartResumeNote(id string) (restartResumeNote, bool) {
	var note restartResumeNote
	path, err := restartResumePath(id)
	if err != nil {
		return note, false
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &note) != nil || (note.Signal == "" && note.Transcript == "") {
		return note, false
	}
	return note, true
}

func clearRestartResumeNote(id string) {
	if path, err := restartResumePath(id); err == nil {
		_ = os.Remove(path)
	}
}

// takeRestartResume reports whether a flip of inst to `to` at ts is part of
// its last restart (the old process dying, the new one booting). A boot
// keeps the note, since the snapshot and hook paths can each see it; any
// other flip after the restart spends it.
func takeRestartResume(inst *Instance, to string, ts time.Time) (restartResumeNote, bool) {
	note, ok := readRestartResumeNote(inst.ID)
	if !ok {
		return note, false
	}
	signal := ""
	if note.Transcript == "" {
		signal = transitionEventOutputHash(inst)
	}
	return note, settleRestartResume(inst.ID, note, inst.GetJSONLPath(), signal, to, ts)
}

// restartBootRecord is the notifier's own check (#2792), for any producer
// that reaches the inbox without emitTurn: a record of the child's restart
// boot is dropped. emitTurn's info line passes by its restartResumeLine mark.
func restartBootRecord(event TransitionNotificationEvent) bool {
	if event.restartResumeLine || event.ChildSessionID == "" {
		return false
	}
	note, ok := readRestartResumeNote(event.ChildSessionID)
	if !ok {
		return false
	}
	to := event.ToStatus
	if event.Kind == transitionKindFinished {
		to = string(StatusWaiting)
	}
	return settleRestartResume(event.ChildSessionID, note, note.Transcript, event.LastOutputHash, to, event.Timestamp)
}

// settleRestartResume decides a flip against the note (transcript is the
// child's current transcript path, signal its turn signal when the note has
// no transcript) and spends the note when the flip is not the boot.
func settleRestartResume(id string, note restartResumeNote, transcript, signal, to string, ts time.Time) bool {
	if ts.Before(note.At) {
		return false // from before the restart: delivered as before
	}
	if ts.Sub(note.At) > restartResumeTTL {
		clearRestartResumeNote(id)
		return false
	}
	unchanged := false
	if note.Transcript != "" {
		unchanged = note.Transcript == transcript && noTurnSince(note.Transcript, note.Offset)
	} else {
		unchanged = signal != "" && signal == note.Signal
	}
	boot := false
	switch normalizeStatusString(to) {
	case string(StatusWaiting), string(StatusIdle):
		boot = unchanged
	case string(StatusError):
		// The old process dying mid-restart; an error once the restart is
		// over (a resume that crashed) is news.
		boot = unchanged && (note.RestartedAt.IsZero() || !ts.After(note.RestartedAt.Add(restartResumeErrorSlack)))
	}
	if !boot {
		clearRestartResumeNote(id)
	}
	return boot
}

// noTurnSince reports whether the transcript at path gained no user or
// assistant record after offset.
func noTurnSince(path string, offset int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < offset || info.Size()-offset > restartResumeMaxScan {
		return false
	}
	buf := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return false
	}
	for _, line := range strings.Split(string(buf), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			return false // a partial or unknown line: not provably a boot
		}
		if rec.Type == "user" || rec.Type == "assistant" {
			return false
		}
	}
	return true
}

// emitRestartResumed is emitTurn's result for a restart's flips: one
// info-tier line per restart for a parented child once it is back at its
// prompt, never the old reply again, and nothing for the rest.
func (d *TransitionDaemon) emitRestartResumed(inst *Instance, event TransitionNotificationEvent, note restartResumeNote, selfConductor bool) TransitionNotificationEvent {
	event.DeliveryResult = transitionDeliveryDropped
	if note.Announced || selfConductor || strings.TrimSpace(inst.ParentSessionID) == "" ||
		normalizeStatusString(event.ToStatus) == string(StatusError) {
		return event
	}
	tool := note.Tool
	if tool == "" {
		tool = inst.Tool
	}
	event.Tier = TurnTierInfo
	event.Trigger = TurnTriggerSystem
	event.Text = "Restarted while idle; conversation resumed, no new turn."
	if note.Reason == RestartReasonIdleBuildRefresh {
		onto := "a newer " + tool + " build"
		if note.Build != "" {
			onto = tool + " " + filepath.Base(note.Build)
		}
		event.Text = "Refreshed onto " + onto + " while idle; conversation resumed, no new turn."
	}
	event.restartResumeLine = true
	// Its own identity: the old turn's hash would read as a duplicate of the
	// record that turn already sent.
	event.LastOutputHash = "restart:" + note.At.UTC().Format(time.RFC3339Nano)
	result := d.notifier.NotifyTransition(event)
	if result.DeliveryResult != transitionDeliveryFailed {
		updateRestartResumeNote(inst.ID, func(n *restartResumeNote) { n.Announced = true })
	}
	if result.DeliveryResult == transitionDeliveryCommitted {
		_ = BumpInboxStats(statsParentFor(inst), func(s *InboxStats) { s.RecordsInfo++ })
	}
	return result
}
