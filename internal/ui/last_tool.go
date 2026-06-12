package ui

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// lastUsedToolMetaKey is the StateDB metadata key under which the last
// successfully-submitted new-session tool is remembered (UX top-3 change #2).
// Stored in the profile state.db metadata table — never in config.toml — so
// remembering a tool choice does not churn the user's hand-edited config.
const lastUsedToolMetaKey = "last_used_tool"

// resolveInitialTool decides which tool the new-session dialog should preselect
// on open. An explicit [default_tool] config value always wins; with nothing
// configured the remembered last-used tool is used; when neither is set it
// returns "" so the dialog preselects shell exactly as on first run.
func resolveInitialTool(configDefault, remembered string) string {
	if strings.TrimSpace(configDefault) != "" {
		return configDefault
	}
	return remembered
}

// rememberedTool returns the last successfully-submitted tool persisted in the
// profile StateDB, or "" when nothing is remembered or the store is
// unavailable.
func rememberedTool(db *statedb.StateDB) string {
	if db == nil {
		return ""
	}
	v, _ := db.GetMeta(lastUsedToolMetaKey)
	return v
}

// rememberTool persists tool as the last successfully-submitted choice. A nil
// store or a write error is silently ignored — remembering is a best-effort
// convenience, never load-bearing for session creation.
func rememberTool(db *statedb.StateDB, tool string) {
	if db == nil {
		return
	}
	_ = db.SetMeta(lastUsedToolMetaKey, tool)
}

// stateDB returns the profile StateDB handle for metadata reads/writes, or nil
// when storage is unavailable (so callers stay nil-safe).
func (h *Home) stateDB() *statedb.StateDB {
	if h == nil || h.storage == nil {
		return nil
	}
	return h.storage.GetDB()
}
