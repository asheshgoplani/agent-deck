package ui

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// dumpRowsEnv names a file the deck rewrites with the flat item list (one row
// per line, with identities) after every rebuild. It exists so a row that
// appears twice on screen can be told apart from a stale physical terminal
// row: if the dump has one row and the screen two, the model is clean.
const dumpRowsEnv = "AGENTDECK_DUMP_ROWS"

// checkFlatItemsUnique logs a warning when the rebuilt list holds two rows
// with the same identity. View-mode partitioning repeats headers by design,
// so it is skipped there.
func (h *Home) checkFlatItemsUnique() {
	if h.groupViewMode != session.GroupViewNormal {
		return
	}
	if i, id, dup := session.FirstDuplicateRow(h.flatItems); dup {
		uiLog.Warn("duplicate_list_row", slog.Int("row", i), slog.String("identity", strings.ReplaceAll(id, "\x00", "|")))
	}
}

// dumpFlatItems writes the current flat item list to $AGENTDECK_DUMP_ROWS.
func (h *Home) dumpFlatItems() {
	path := os.Getenv(dumpRowsEnv)
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(h.flatItemsDump()), 0o600)
}

func (h *Home) flatItemsDump() string {
	var b strings.Builder
	fmt.Fprintf(&b, "size=%dx%d cursor=%d viewOffset=%d viewMode=%v rows=%d\n",
		h.width, h.height, h.cursor, h.viewOffset, h.groupViewMode, len(h.flatItems))
	seen := make(map[string]int, len(h.flatItems))
	for i, it := range h.flatItems {
		id, ok := it.Identity()
		if !ok {
			id = fmt.Sprintf("type=%d", it.Type)
		}
		note := ""
		if ok {
			if first, dup := seen[id]; dup {
				note = fmt.Sprintf("  DUPLICATE of row %d", first)
			} else {
				seen[id] = i
			}
		}
		fmt.Fprintf(&b, "%3d level=%d num=%d path=%q id=%s%s\n", i, it.Level, it.RootGroupNum, it.Path, strings.ReplaceAll(id, "\x00", "|"), note)
	}
	return b.String()
}
