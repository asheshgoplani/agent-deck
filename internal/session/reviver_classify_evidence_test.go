package session

import (
	"log/slog"
	"testing"
)

// Issue #1705: a live conductor was restarted as if it were dead, and the
// investigation could not get past "the restart fired" because the READINGS
// behind that verdict were never written down. Classify now states its evidence
// for every non-alive verdict.

// classifyAttrs returns the attributes of the last reviver_classify record.
func classifyAttrs(t *testing.T, h *recordingHandler) map[string]string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.recs) - 1; i >= 0; i-- {
		if h.recs[i].Message != "reviver_classify" {
			continue
		}
		attrs := map[string]string{}
		h.recs[i].Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.String()
			return true
		})
		attrs["__level"] = h.recs[i].Level.String()
		return attrs
	}
	t.Fatalf("no reviver_classify record emitted")
	return nil
}

func TestReviver_Classify_LogsEvidenceForErroredSession(t *testing.T) {
	inst := newReviverTestInstance("evidence-errored", StatusError)
	h := &recordingHandler{}
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    func(string) bool { return false },
		ReviveAction: func(*Instance) error { return nil },
		Log:          slog.New(h),
	}

	if got := r.Classify(inst); got != ClassErrored {
		t.Fatalf("expected ClassErrored, got %v", got)
	}

	attrs := classifyAttrs(t, h)
	for key, want := range map[string]string{
		"tmux_alive":    "true",
		"pipe_alive":    "false",
		"stored_status": string(StatusError),
		"class":         "errored",
		"title":         "evidence-errored",
	} {
		if attrs[key] != want {
			t.Errorf("attr %q = %q, want %q", key, attrs[key], want)
		}
	}
	if attrs["sampled_at"] == "" {
		t.Error("evidence must be timestamped (sampled_at missing)")
	}
	if attrs["__level"] != slog.LevelInfo.String() {
		t.Errorf("a non-alive verdict must be retrievable without debug logging; level=%s", attrs["__level"])
	}
}

// The pipe reading must be recorded even when the stored status alone already
// settles the verdict — it is the reading that says whether the session was
// reachable, which is the whole question #1705 could not answer afterwards.
func TestReviver_Classify_RecordsPipeReadingEvenWhenStatusDecides(t *testing.T) {
	inst := newReviverTestInstance("evidence-live-pipe", StatusError)
	h := &recordingHandler{}
	probed := 0
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    func(string) bool { probed++; return true },
		ReviveAction: func(*Instance) error { return nil },
		Log:          slog.New(h),
	}

	if got := r.Classify(inst); got != ClassErrored {
		t.Fatalf("a StatusError session on a live server stays ClassErrored, got %v", got)
	}
	if probed != 1 {
		t.Fatalf("expected the pipe to be probed once for the record, got %d probes", probed)
	}
	if attrs := classifyAttrs(t, h); attrs["pipe_alive"] != "true" {
		t.Errorf("pipe_alive = %q, want true (a live pipe under a StatusError reading is exactly the false-positive signal)", attrs["pipe_alive"])
	}
}

func TestReviver_Classify_DeadServerEvidence(t *testing.T) {
	inst := newReviverTestInstance("evidence-dead", StatusRunning)
	h := &recordingHandler{}
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return false },
		PipeAlive:    func(string) bool { t.Fatal("pipe must not be probed when the server is gone"); return false },
		ReviveAction: func(*Instance) error { return nil },
		Log:          slog.New(h),
	}

	if got := r.Classify(inst); got != ClassDead {
		t.Fatalf("expected ClassDead, got %v", got)
	}
	attrs := classifyAttrs(t, h)
	if attrs["tmux_alive"] != "false" || attrs["class"] != "dead" {
		t.Errorf("dead-server evidence wrong: tmux_alive=%q class=%q", attrs["tmux_alive"], attrs["class"])
	}
}

// An alive session is the overwhelming majority of every sweep: its verdict stays
// at debug level so the fleet does not drown the useful records.
func TestReviver_Classify_AliveVerdictStaysDebug(t *testing.T) {
	inst := newReviverTestInstance("evidence-alive", StatusRunning)
	h := &recordingHandler{}
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    func(string) bool { return true },
		ReviveAction: func(*Instance) error { return nil },
		Log:          slog.New(h),
	}

	if got := r.Classify(inst); got != ClassAlive {
		t.Fatalf("expected ClassAlive, got %v", got)
	}
	if attrs := classifyAttrs(t, h); attrs["__level"] != slog.LevelDebug.String() {
		t.Errorf("alive verdict level = %s, want DEBUG", attrs["__level"])
	}
}

// A Reviver built by hand without a PipeAlive func must not panic: Classify used
// to call it unconditionally on the non-error path only, and the evidence read
// widened when that call site moved.
func TestReviver_Classify_NilPipeAliveDoesNotPanic(t *testing.T) {
	inst := newReviverTestInstance("evidence-nilpipe", StatusRunning)
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		ReviveAction: func(*Instance) error { return nil },
	}
	if got := r.Classify(inst); got != ClassErrored {
		t.Fatalf("no pipe reading available → not provably alive; got %v", got)
	}
}
