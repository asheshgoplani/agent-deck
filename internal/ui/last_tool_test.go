package ui

// UX top-3 change #2: remember the last successfully-submitted tool and
// preselect it on the next new-session dialog open. An explicit
// [default_tool] config value always WINS; with nothing configured the
// remembered tool is used; first-run (nothing remembered) is unchanged.
//
// The remembered value lives in the profile StateDB metadata table (the same
// key/value store used for last_modified), NOT config.toml — so persisting a
// tool choice never churns the user's hand-edited config file.

import (
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// resolveInitialTool: explicit config default wins over the remembered value.
func TestResolveInitialTool_ConfigWins(t *testing.T) {
	if got := resolveInitialTool("claude", "gemini"); got != "claude" {
		t.Fatalf("resolveInitialTool(config=claude, remembered=gemini) = %q, want claude (config wins)", got)
	}
}

// resolveInitialTool: no config default → use the remembered tool.
func TestResolveInitialTool_RememberedUsedWhenNoConfig(t *testing.T) {
	if got := resolveInitialTool("", "gemini"); got != "gemini" {
		t.Fatalf("resolveInitialTool(config=\"\", remembered=gemini) = %q, want gemini", got)
	}
}

// resolveInitialTool: first run — nothing configured, nothing remembered → "".
// SetDefaultTool("") preselects shell, i.e. behavior is unchanged.
func TestResolveInitialTool_FirstRunEmpty(t *testing.T) {
	if got := resolveInitialTool("", ""); got != "" {
		t.Fatalf("resolveInitialTool(\"\", \"\") = %q, want \"\" (first-run unchanged)", got)
	}
}

// Persistence round-trip through the StateDB metadata store.
func TestRememberTool_RoundTrip(t *testing.T) {
	db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open statedb: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate statedb: %v", err)
	}
	defer db.Close()

	// Nothing remembered yet.
	if got := rememberedTool(db); got != "" {
		t.Fatalf("rememberedTool on fresh db = %q, want \"\"", got)
	}
	// Persist a submitted tool, then read it back.
	rememberTool(db, "codex")
	if got := rememberedTool(db); got != "codex" {
		t.Fatalf("rememberedTool after rememberTool(codex) = %q, want codex", got)
	}
	// Overwrite with a newer choice.
	rememberTool(db, "claude")
	if got := rememberedTool(db); got != "claude" {
		t.Fatalf("rememberedTool after rememberTool(claude) = %q, want claude", got)
	}
}

// nil-safe: helpers must not panic when the StateDB is unavailable.
func TestRememberTool_NilDBSafe(t *testing.T) {
	if got := rememberedTool(nil); got != "" {
		t.Fatalf("rememberedTool(nil) = %q, want \"\"", got)
	}
	rememberTool(nil, "claude") // must not panic
}
