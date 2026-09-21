package ui

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// dumpRowsEnv names a file the deck rewrites with the flat item list (one row
// per line, with identities) after every rebuild. It exists so a row that
// appears twice on screen can be told apart from a stale physical terminal
// row: if the dump has one row and the screen two, the model is clean.
const dumpRowsEnv = "AGENTDECK_DUMP_ROWS"

// dupWarnInterval is how long an unchanged set of duplicate rows stays quiet
// before it is logged again. Rebuilds follow status and poll traffic, so a
// persistent duplicate would otherwise log on every one.
const dupWarnInterval = 10 * time.Minute

// duplicateRowIdentities returns the sorted identities that appear more than
// once in items.
func duplicateRowIdentities(items []session.Item) []string {
	count := make(map[string]int, len(items))
	for _, it := range items {
		if id, ok := it.Identity(); ok {
			count[id]++
		}
	}
	var dups []string
	for id, n := range count {
		if n > 1 {
			dups = append(dups, strings.ReplaceAll(id, "\x00", "|"))
		}
	}
	sort.Strings(dups)
	return dups
}

// checkFlatItemsUnique logs a warning when the rebuilt list holds two rows
// with the same identity: once per distinct set of duplicates, again only when
// the set changes or dupWarnInterval passes. View-mode partitioning repeats
// headers by design, so it is skipped there.
func (h *Home) checkFlatItemsUnique() {
	if h.groupViewMode != session.GroupViewNormal {
		return
	}
	dups := duplicateRowIdentities(h.flatItems)
	if len(dups) == 0 {
		h.dupWarnKey = ""
		return
	}
	key := strings.Join(dups, "\n")
	now := time.Now()
	if key == h.dupWarnKey && now.Sub(h.dupWarnAt) < dupWarnInterval {
		return
	}
	h.dupWarnKey, h.dupWarnAt = key, now
	uiLog.Warn("duplicate_list_row", slog.Any("identities", dups))
}

// dumpFlatItems atomically rewrites $AGENTDECK_DUMP_ROWS with the current flat
// item list (temp file in the same directory, then rename), so a reader never
// sees a half-written dump. A write error is logged once.
func (h *Home) dumpFlatItems() {
	path := os.Getenv(dumpRowsEnv)
	if path == "" {
		return
	}
	if err := atomicfile.WriteFile(path, []byte(h.flatItemsDump()), 0o600); err != nil {
		if !h.dumpErrLogged {
			h.dumpErrLogged = true
			uiLog.Warn("dump_rows_write_failed", slog.String("path", path), slog.Any("error", err))
		}
		return
	}
	h.dumpErrLogged = false
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
