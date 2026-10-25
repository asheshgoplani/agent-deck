// Package menudata holds the data contract shared by the web server and the
// TUI: the flattened menu snapshot DTOs (MenuSnapshot and friends, with their
// JSON wire tags), BuildMenuSnapshot/ToMenuSession that build them from
// session state, the in-memory MemoryMenuData store with its MenuDataLoader
// fallback, and the SessionMutator contract (with WorktreeFinishOptions,
// WorktreeFinishResult, DefaultUndoWindow and the Err* sentinels) that the
// TUI implements for the web handlers.
//
// It is a leaf: it imports only the standard library and internal/session,
// never internal/web or internal/ui. internal/web re-exports every name here
// as an alias (internal/web/menudata_alias.go), so web.MenuSnapshot and
// menudata.MenuSnapshot are the same type and web.ErrX is the same value as
// menudata.ErrX.
package menudata
