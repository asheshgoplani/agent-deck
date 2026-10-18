package main

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// shadowedBy is the PATH scan against version (the version of this
// executable on disk), never nil so JSON shows an empty list.
func shadowedBy(version string) []update.ShadowCopy {
	copies := update.FindShadowingCopies(version)
	if copies == nil {
		return []update.ShadowCopy{}
	}
	return copies
}

// printPathShadowing prints one warning per shadowing copy (nothing when
// PATH is clean) and logs it. It never touches the copies.
func printPathShadowing(w io.Writer, copies []update.ShadowCopy, log *slog.Logger) {
	if len(copies) == 0 {
		return
	}
	fmt.Fprint(w, "\n"+update.ShadowWarnings(copies))
	update.LogShadowCopies(log, copies)
}
