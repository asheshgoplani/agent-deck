package session

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// CodeQL go/path-injection: spawn-guard file names are built from a
// validated instance component and must stay directly under the locks dir.

func TestValidateSpawnGuardID_RefusesTraversal(t *testing.T) {
	for _, id := range []string{"", "..", "../x", "a/../b", "a/b", `a\b`, "a b", "x\n"} {
		if err := validateSpawnGuardID(id); err == nil {
			t.Errorf("validateSpawnGuardID(%q) = nil, want error", id)
		}
	}
	for _, id := range []string{"abc-123_X", "a.b", "unknown"} {
		if err := validateSpawnGuardID(id); err != nil {
			t.Errorf("validateSpawnGuardID(%q) = %v, want nil", id, err)
		}
	}
}

func TestSpawnGuardPaths_StayUnderLocksDir(t *testing.T) {
	tmp := t.TempDir()
	prev := agentDeckDirOverride
	agentDeckDirOverride = filepath.Join(tmp, ".agent-deck")
	t.Cleanup(func() { agentDeckDirOverride = prev })
	locks := filepath.Join(agentDeckDirOverride, "locks")

	for _, id := range []string{"../../etc/passwd", "..", "a/../../b", "/abs/path", "ok-id_1"} {
		for name, fn := range map[string]func(string) (string, error){
			"lock":  instanceSpawnLockPath,
			"stamp": instanceSpawnStampPath,
		} {
			p, err := fn(id)
			if err != nil {
				t.Fatalf("%s path for %q: %v", name, id, err)
			}
			if filepath.Dir(p) != locks {
				t.Errorf("%s path for %q = %q, want a file directly under %q", name, id, p, locks)
			}
			if strings.Contains(filepath.Base(p), "..") {
				t.Errorf("%s path for %q has '..' in its name: %q", name, id, p)
			}
		}
	}
}

// CodeQL go/log-injection: the clock anomaly warning must not carry raw
// line breaks from the instance id.

type captureAttrsHandler struct{ attrs map[string]string }

func (h *captureAttrsHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureAttrsHandler) Handle(_ context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		h.attrs[a.Key] = a.Value.String()
		return true
	})
	return nil
}
func (h *captureAttrsHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureAttrsHandler) WithGroup(string) slog.Handler      { return h }

func TestSpawnStampAnomalyLog_StripsLineBreaks(t *testing.T) {
	h := &captureAttrsHandler{attrs: map[string]string{}}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	now := time.Now()
	spawnStampClockAnomalyLogFn("id\nlevel=ERROR msg=forged\r", now.Add(time.Hour), now)

	got, ok := h.attrs["instance"]
	if !ok {
		t.Fatal("instance attr not logged")
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("instance attr %q contains a raw line break", got)
	}
}
