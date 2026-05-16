package logassert_test

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil/logassert"
)

func TestCapture_RecordsLevelAndMessage(t *testing.T) {
	cap := logassert.NewCapture()
	logger := slog.New(cap)

	logger.Info("hook_status_updated", slog.String("session", "s1"))

	rec := cap.MustOne(t, "hook_status_updated")
	if rec.Level != slog.LevelInfo {
		t.Fatalf("level=%v want Info", rec.Level)
	}
	if got := rec.String("session"); got != "s1" {
		t.Fatalf("session=%q want s1", got)
	}
}

func TestRecords_FilterByMessage(t *testing.T) {
	cap := logassert.NewCapture()
	logger := slog.New(cap)

	logger.Info("a")
	logger.Info("b")
	logger.Info("a")

	if n := len(cap.WithMessage("a")); n != 2 {
		t.Fatalf("WithMessage(a) len=%d want 2", n)
	}
	if n := len(cap.WithMessage("missing")); n != 0 {
		t.Fatalf("WithMessage(missing) len=%d want 0", n)
	}
}

func TestAssertContains_PassesAndFails(t *testing.T) {
	cap := logassert.NewCapture()
	logger := slog.New(cap)
	logger.Warn("watcher_overflow", slog.Int("dropped", 42))

	cap.AssertContains(t, "watcher_overflow")

	// Use a stub T to verify failure path.
	stub := &stubT{}
	cap.AssertContains(stub, "never_logged")
	if !stub.failed {
		t.Fatal("expected AssertContains to fail when message absent")
	}
}

func TestAssertNotContains(t *testing.T) {
	cap := logassert.NewCapture()
	logger := slog.New(cap)
	logger.Info("ok")

	cap.AssertNotContains(t, "panic")

	stub := &stubT{}
	cap.AssertNotContains(stub, "ok")
	if !stub.failed {
		t.Fatal("expected AssertNotContains to fail when message present")
	}
}

func TestRecord_TypedAccessors(t *testing.T) {
	cap := logassert.NewCapture()
	logger := slog.New(cap)

	logger.Info("evt",
		slog.String("name", "click"),
		slog.Int("count", 7),
		slog.Bool("retry", true),
		slog.Any("err", errors.New("boom")),
	)

	rec := cap.MustOne(t, "evt")
	if rec.String("name") != "click" {
		t.Errorf("String(name)=%q", rec.String("name"))
	}
	if rec.Int("count") != 7 {
		t.Errorf("Int(count)=%d", rec.Int("count"))
	}
	if !rec.Bool("retry") {
		t.Errorf("Bool(retry)=false")
	}
	if rec.String("err") == "" {
		t.Errorf("String(err) empty; want error text")
	}
}

func TestWithGroup_FlattensNestedAttrs(t *testing.T) {
	cap := logassert.NewCapture()
	logger := slog.New(cap).WithGroup("watcher")
	logger.Info("event", slog.Int("dropped", 3))

	rec := cap.MustOne(t, "event")
	if got := rec.Int("watcher.dropped"); got != 3 {
		t.Fatalf("Int(watcher.dropped)=%d want 3", got)
	}
}

func TestReset_ClearsRecords(t *testing.T) {
	cap := logassert.NewCapture()
	logger := slog.New(cap)
	logger.Info("a")
	cap.Reset()
	if n := len(cap.Records()); n != 0 {
		t.Fatalf("after Reset len=%d want 0", n)
	}
}

// stubT records whether the assertion failed.
type stubT struct{ failed bool }

func (s *stubT) Errorf(format string, args ...any) { s.failed = true }
func (s *stubT) Helper()                           {}
