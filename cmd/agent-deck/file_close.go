package main

import (
	"log/slog"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// closeChecked closes a writable file and records failures that would
// otherwise be silently lost from deferred Close calls.
func closeChecked(file *os.File) {
	if err := file.Close(); err != nil {
		hookHandlerLog.Warn("writable_file_close_failed",
			slog.String("error", logging.SanitizeValue(err.Error())))
	}
}
