package session

import (
	"log/slog"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// logFolderTrustGranted records that agent-deck pre-seeded folder trust for a
// tool. Callers log only a new grant (dir was not trusted before), so the log
// is an audit trail of every trust decision agent-deck made on the user's
// behalf. whereKey names the location: "config" for a local file, "host" or
// "container" for a remote seed. dir and where come from session paths and
// config, so both are sanitized (CodeQL go/log-injection).
func logFolderTrustGranted(tool, dir, whereKey, where string, extra ...slog.Attr) {
	args := []any{
		slog.String("tool", tool),
		slog.String("dir", logging.SanitizeValue(dir)),
		slog.String(whereKey, logging.SanitizeValue(where)),
	}
	for _, a := range extra {
		args = append(args, a)
	}
	sessionLog.Info("folder_trust_granted", args...)
}
