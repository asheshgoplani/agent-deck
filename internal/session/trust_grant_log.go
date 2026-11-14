package session

import (
	"log/slog"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// logFolderTrustGranted records that agent-deck pre-seeded folder trust for a
// tool. Callers log only a new grant (dir was not trusted before), so the log
// is an audit trail of every trust decision agent-deck made on the user's
// behalf. whereKey names the location: "config" for a local file, "host" or
// "container" for a remote seed. dir and where come from session paths and
// config, so every string value is sanitized (CodeQL go/log-injection).
func logFolderTrustGranted(tool, dir, whereKey, where string, extra ...slog.Attr) {
	args := []any{
		slog.String("tool", trustLogValue(tool)),
		slog.String("dir", trustLogValue(dir)),
		slog.String(whereKey, trustLogValue(where)),
	}
	for _, a := range extra {
		if a.Value.Kind() == slog.KindString {
			a = slog.String(a.Key, trustLogValue(a.Value.String()))
		}
		args = append(args, a)
	}
	sessionLog.Info("folder_trust_granted", args...)
}

// trustLogValue escapes control characters with logging.SanitizeValue; the
// outer ReplaceAll calls are no-ops after it but are the form CodeQL
// recognizes as a log-injection sanitizer (same pattern as update_cli.go).
func trustLogValue(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(s), "\n", ""), "\r", "")
}
