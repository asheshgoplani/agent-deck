package update

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// AuditLogFileName is the append-only record of every unattended update
// run, next to update.lock in the cache dir. The shared debug.log is
// rotated by rename by whichever agent-deck process fills it first (TUIs,
// the web daemon, the notifier), and the 2026-09-19 install left no line
// of the run that replaced the binary in any surviving file. This file is
// opened O_APPEND by each run and only ever moved aside at open when it is
// over auditLogCapBytes, so a run's lines are never lost to another
// process's rotation.
const AuditLogFileName = "update.log"

// auditLogCapBytes is the size past which the file is moved to
// update.log.1 at the next open.
const auditLogCapBytes = 4 * 1024 * 1024

// AuditIdentity is what every audit line says about the process writing it.
type AuditIdentity struct {
	Trigger string
	PID     int
	PPID    int
	// Service is the launchd service the process runs inside
	// (XPC_SERVICE_NAME), "" outside launchd.
	Service string
	Version string
}

func (id AuditIdentity) attrs() []slog.Attr {
	attrs := []slog.Attr{
		slog.String("trigger", id.Trigger),
		slog.String("mode", "unattended"),
		slog.Int("pid", id.PID),
		slog.Int("ppid", id.PPID),
		slog.String("version", id.Version),
	}
	if id.Service != "" {
		attrs = append(attrs, slog.String("service", id.Service))
	}
	return attrs
}

// auditIdentity builds the identity of this process for trigger and
// version; getenv is os.Getenv outside tests.
func auditIdentity(trigger, version string, getenv func(string) string) AuditIdentity {
	return AuditIdentity{
		Trigger: trigger,
		PID:     os.Getpid(),
		PPID:    os.Getppid(),
		Service: getenv(launchdServiceEnv),
		Version: version,
	}
}

// NewAuditIdentity is auditIdentity over the real environment.
func NewAuditIdentity(trigger, version string) AuditIdentity {
	return auditIdentity(trigger, version, os.Getenv)
}

// OpenAuditLog returns a logger that writes every record to <dir>/update.log
// and to base (the shared debug log), both tagged with id and
// component=update. The returned close flushes and closes the file. When
// the file cannot be opened the logger still works through base and the
// error says why; the caller logs it once and carries on.
func OpenAuditLog(dir string, base *slog.Logger, id AuditIdentity) (*slog.Logger, func(), error) {
	attrs := append([]slog.Attr{slog.String("component", logging.CompUpdate)}, id.attrs()...)
	handlers := []slog.Handler{base.Handler().WithAttrs(attrs)}
	closeFn := func() {}
	f, err := openAuditFile(dir)
	if err == nil {
		handlers = append(handlers, slog.NewJSONHandler(f, nil).WithAttrs(attrs))
		closeFn = func() { _ = f.Sync(); _ = f.Close() }
	}
	return slog.New(teeHandler(handlers)), closeFn, err
}

func openAuditFile(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	path := filepath.Join(dir, AuditLogFileName)
	if info, err := os.Stat(path); err == nil && info.Size() > auditLogCapBytes {
		if err := os.Rename(path, path+".1"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("move aside %s: %w", path, err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- fixed name under the cache dir
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, nil
}

// teeHandler fans one record out to every handler.
type teeHandler []slog.Handler

func (t teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range t {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (t teeHandler) Handle(ctx context.Context, r slog.Record) error {
	var first error
	for _, h := range t {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (t teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(teeHandler, len(t))
	for i, h := range t {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (t teeHandler) WithGroup(name string) slog.Handler {
	out := make(teeHandler, len(t))
	for i, h := range t {
		out[i] = h.WithGroup(name)
	}
	return out
}
