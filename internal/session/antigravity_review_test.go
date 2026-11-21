package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestAntigravityConversationOptionParsing(t *testing.T) {
	for _, tt := range []struct {
		name, command, wrapper, want string
	}{
		{"paired value", "agy --conversation conv-a", "", "conv-a"},
		{"attached value", "agy --conversation=conv-b", "", "conv-b"},
		{"prompt value is data", "agy --prompt-interactive '--conversation=conv-other'", "", ""},
		{"short prompt value is data", "agy -i '--conversation=conv-other'", "", ""},
		{"value after another option is data", "agy --add-dir /tmp/x --prompt-interactive '--conversation=conv-other'", "", ""},
		{"wrapper selector", "agy", "{command} --conversation conv-wrap", "conv-wrap"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractAntigravityConversationID(tt.command, tt.wrapper); got != tt.want {
				t.Fatalf("extract = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAntigravityForkRewritePreservesLauncherSyntax(t *testing.T) {
	for _, tt := range []struct {
		name, command, want string
		ok                  bool
	}{
		{"assignment keeps its syntax", "AGY_PROFILE='work profile' agy --conversation conv-a", "AGY_PROFILE='work profile' agy", true},
		{"compound command is rejected", "source ./env.sh && agy --conversation conv-a", "", false},
		{"expansion is rejected", `agy --add-dir "$HOME/lib" --conversation conv-a`, "", false},
		{"prompt is removed whole", "agy --conversation conv-a --prompt-interactive 'review --conversation=b'", "agy", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := stripAntigravitySessionFlags(tt.command)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("stripAntigravitySessionFlags(%q) = (%q, %v), want (%q, %v)", tt.command, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestAntigravityWrapperRewrite(t *testing.T) {
	for _, tt := range []struct {
		name, wrapper, want string
		ok                  bool
	}{
		{"selector removed anywhere", "nice {command} --conversation conv-a", "nice {command}", true},
		{"only the placeholder remains", "{command} --continue", "", true},
		{"opaque wrapper is rejected", "my-launcher --conversation conv-a", "", false},
		{"quoted placeholder is rejected", `bash -lc "{command}"`, "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := stripAntigravityConversationFromWrapper(tt.wrapper)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("stripAntigravityConversationFromWrapper(%q) = (%q, %v), want (%q, %v)", tt.wrapper, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestAntigravityResumeCommandUsesWrappers(t *testing.T) {
	for _, tt := range []struct {
		name, command, wrapper, bound, want string
	}{
		{"no binding", "agy", "", "", "agy"},
		{"bound", "agy", "", "conv-a", "agy --conversation conv-a"},
		{"explicit command selector wins", "agy --conversation explicit", "", "conv-a", "agy --conversation explicit"},
		{"argument-preserving wrapper", "agy", "nice {command}", "conv-a", "agy --conversation conv-a"},
		{"wrapper selector wins", "agy", "{command} --conversation explicit", "conv-a", "agy"},
		{"opaque wrapper cannot resume", "agy", "my-launcher", "conv-a", "agy"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inst := &Instance{Tool: "antigravity", Command: tt.command, Wrapper: tt.wrapper, AntigravitySessionID: tt.bound}
			if got := inst.buildAntigravityResumeCommand(); got != tt.want {
				t.Fatalf("buildAntigravityResumeCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

// An initial message must continue the bound conversation, not open a fresh
// one beside it.
func TestAntigravityPromptResumesBoundConversation(t *testing.T) {
	inst := &Instance{Tool: "antigravity", Command: "agy", AntigravitySessionID: "conv-a"}
	got, embedded := inst.buildAntigravityCommandWithPrompt("continue the review")
	want := "agy --conversation conv-a --prompt-interactive " + shellescape.Quote("continue the review")
	if !embedded || got != want {
		t.Fatalf("buildAntigravityCommandWithPrompt() = (%q, %t), want (%q, true)", got, embedded, want)
	}
}

func writeAntigravityMetadata(t *testing.T, dataDir string, conversations map[string]any) {
	t.Helper()
	cacheDir := filepath.Join(dataDir, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"conversations": conversations})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "conversation_metadata.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func antigravityTestConversationEntry(id, workspace string, updated time.Time, internal bool) map[string]any {
	summary := map[string]any{
		"ID":            id,
		"WorkspaceURIs": []string{"file://" + workspace},
		"Internal":      internal,
	}
	if !updated.IsZero() {
		summary["UpdatedAt"] = updated.UTC().Format(time.RFC3339Nano)
	}
	return map[string]any{"summary": summary, "is_internal": internal}
}

func TestAntigravityDiscoveryKeepsBindingMissingFromMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".gemini", "antigravity-cli")
	convDir := filepath.Join(dataDir, "conversations")
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The bound conversation still exists on disk but the metadata cache has
	// not caught up; a newer sibling in the same workspace must not steal it.
	if err := os.WriteFile(filepath.Join(convDir, "conv-bound.db"), []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAntigravityMetadata(t, dataDir, map[string]any{
		"conv-sibling": antigravityTestConversationEntry("conv-sibling", workDir, time.Now(), false),
	})
	if got := discoverAntigravityConversationFromDisk(dataDir, workDir, workDir, "conv-bound", "", 0); got != "conv-bound" {
		t.Fatalf("discovery moved a live binding to a sibling: %q", got)
	}
}

func TestAntigravityDiscoveryTreatsUndatedEntriesAsOld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".gemini", "antigravity-cli")
	workDir := filepath.Join(home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAntigravityMetadata(t, dataDir, map[string]any{
		"conv-undated": antigravityTestConversationEntry("conv-undated", workDir, time.Time{}, false),
	})
	if got := discoverAntigravityConversationFromDisk(dataDir, workDir, workDir, "", "", time.Now().UnixMilli()); got != "" {
		t.Fatalf("an entry with no timestamp was adopted: %q", got)
	}
}

func TestAntigravityDiscoveryFallbackSkipsInternalIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".gemini", "antigravity-cli")
	convDir := filepath.Join(dataDir, "conversations")
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAntigravityMetadata(t, dataDir, map[string]any{
		"conv-int": antigravityTestConversationEntry("conv-int", workDir, time.Now(), true),
	})
	if err := os.WriteFile(filepath.Join(convDir, "conv-int.db"), []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	last := map[string]string{workDir: "conv-int"}
	raw, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "cache", "last_conversations.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := discoverAntigravityConversationFromDisk(dataDir, workDir, workDir, "", "", 0); got != "" {
		t.Fatalf("fallback re-admitted an internal conversation: %q", got)
	}
}

// A binding that did not reach SQLite is retried on the next scan, even after
// the session looks bound in memory.
func TestAntigravityBindingRetriesAFailedWrite(t *testing.T) {
	storage := newTestStorage(t)
	db := storage.GetDB()
	if db == nil {
		t.Fatal("test storage has no state database")
	}
	prev := statedb.GetGlobal()
	statedb.SetGlobal(db)
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	inst := NewInstance("agy-retry", t.TempDir())
	inst.Tool = "antigravity"
	inst.Command = "agy"
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	path := db.Path()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Drop the owning handles: with them present the write layer would reopen
	// the state database instead of failing.
	inst.restartDB.Store(nil)
	inst.storageSnapshot = nil
	inst.bindAntigravityConversation("conv-retry")
	if !inst.antigravityBindingPending {
		t.Fatal("a failed SQLite write was not recorded as pending")
	}

	reopened, err := statedb.Open(path)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	statedb.SetGlobal(reopened)

	if got := inst.SyncAntigravitySessionID(); got != "conv-retry" {
		t.Fatalf("SyncAntigravitySessionID() = %q, want conv-retry", got)
	}
	if inst.antigravityBindingPending {
		t.Fatal("the retry did not clear the pending flag")
	}
	rows, err := reopened.LoadInstances()
	if err != nil {
		t.Fatal(err)
	}
	var stored bool
	for _, row := range rows {
		if row.ID == inst.ID && ReadAntigravitySessionIDFromToolData(row.ToolData) == "conv-retry" {
			stored = true
		}
	}
	if !stored {
		t.Fatal("the retried binding never reached SQLite")
	}
}

// TestAntigravitySchemaStaysOutOfLegacyJSON keeps the new tool_data key from
// leaking into the human-readable session file.
func TestAntigravityForkParentStaysInToolData(t *testing.T) {
	td := WriteAntigravityForkParentToToolData(json.RawMessage(`{"other":1}`), "conv-parent")
	if got := ReadAntigravityForkParentFromToolData(td); got != "conv-parent" {
		t.Fatalf("fork parent = %q, want conv-parent", got)
	}
	if got := ReadAntigravityForkParentFromToolData(WriteAntigravityForkParentToToolData(td, "")); got != "conv-parent" {
		t.Fatalf("empty write should leave the exclusion alone, got %q", got)
	}
	if strings.Contains(string(td), "antigravity_session_id") {
		t.Fatalf("fork parent write touched the binding keys: %s", td)
	}
}

// A fork child that reloads before its own conversation appears must keep
// excluding the parent, or the next scan binds the parent and restarts write
// into it.
func TestAntigravityForkParentSurvivesReload(t *testing.T) {
	storage := newTestStorage(t)
	child := NewInstance("agy-fork-reload", t.TempDir())
	child.Tool = "antigravity"
	child.Command = "agy"
	child.antigravityForkParentID = "conv-parent"
	if err := storage.SaveWithGroups([]*Instance{child}, NewGroupTreeWithGroups([]*Instance{child}, nil)); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var got *Instance
	for _, inst := range loaded {
		if inst.ID == child.ID {
			got = inst
		}
	}
	if got == nil {
		t.Fatal("reloaded instance missing")
	}
	if got.antigravityForkParentID != "conv-parent" {
		t.Fatalf("fork parent after reload = %q, want conv-parent", got.antigravityForkParentID)
	}

	// Once the child binds its own conversation the exclusion is stale and is
	// not applied on the next load.
	got.AntigravitySessionID = "conv-child"
	got.antigravityForkParentID = ""
	if err := storage.SaveWithGroups([]*Instance{got}, NewGroupTreeWithGroups([]*Instance{got}, nil)); err != nil {
		t.Fatalf("save bound child: %v", err)
	}
	bound, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range bound {
		if inst.ID == child.ID && inst.antigravityForkParentID != "" {
			t.Fatalf("bound child still excludes %q", inst.antigravityForkParentID)
		}
	}
}

func TestAntigravityFreshRestartClearsBinding(t *testing.T) {
	inst := &Instance{
		Tool:                    "antigravity",
		Command:                 "agy",
		AntigravitySessionID:    "conv-a",
		AntigravityDetectedAt:   time.Now(),
		antigravityForkParentID: "conv-parent",
	}
	inst.clearSessionBindingForFreshStart()
	if inst.AntigravitySessionID != "" || !inst.AntigravityDetectedAt.IsZero() ||
		inst.antigravityForkParentID != "" || !inst.antigravitySessionCleared {
		t.Fatalf("fresh restart kept the Antigravity binding: id=%q at=%v parent=%q cleared=%v",
			inst.AntigravitySessionID, inst.AntigravityDetectedAt, inst.antigravityForkParentID, inst.antigravitySessionCleared)
	}
}

// Another session's durable binding is not available to this pane, even when
// shared workspace metadata makes it the newest candidate.
func TestAntigravityWorkspaceCandidateOwnedElsewhereStaysUnbound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := filepath.Join(home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAntigravityMetadata(t, filepath.Join(home, ".gemini", "antigravity-cli"), map[string]any{
		"conv-shared": antigravityTestConversationEntry("conv-shared", workDir, time.Now(), false),
	})

	storage := newTestStorage(t)
	db := storage.GetDB()
	prev := statedb.GetGlobal()
	statedb.SetGlobal(db)
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	owner := NewInstance("agy-owner", workDir)
	owner.Tool, owner.Command = "antigravity", "agy"
	owner.AntigravitySessionID = "conv-shared"
	owner.AntigravityDetectedAt = time.Now()
	other := NewInstance("agy-other", workDir)
	other.Tool, other.Command = "antigravity", "agy"
	other.AntigravityStartedAt = time.Now().Add(-time.Minute).UnixMilli()
	if err := storage.SaveWithGroups([]*Instance{owner, other}, NewGroupTreeWithGroups([]*Instance{owner, other}, nil)); err != nil {
		t.Fatalf("save: %v", err)
	}

	if got := other.SyncAntigravitySessionID(); got != "" {
		t.Fatalf("first scan bound %q", got)
	}
	other.mu.Lock()
	other.antigravityPendingAt = time.Now().Add(-antigravityScanMinInterval)
	other.mu.Unlock()
	if got := other.SyncAntigravitySessionID(); got != "" {
		t.Fatalf("adopted an ID another session already bound: %q", got)
	}
}

// A fork must bind the conversation /fork creates, not a sibling that was
// already present (or newer) when the slash command was sent.
func TestAntigravityForkWaitIgnoresKnownSiblings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := filepath.Join(home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(home, ".gemini", "antigravity-cli")
	writeAntigravityMetadata(t, dataDir, map[string]any{
		"conv-parent":  antigravityTestConversationEntry("conv-parent", workDir, time.Now().Add(-time.Minute), false),
		"conv-sibling": antigravityTestConversationEntry("conv-sibling", workDir, time.Now(), false),
	})

	inst := NewInstance("agy-fork-wait", workDir)
	inst.Tool, inst.Command = "antigravity", "agy"
	inst.antigravityForkParentID = "conv-parent"
	known := inst.antigravityKnownConversations()
	if !known["conv-parent"] || !known["conv-sibling"] {
		t.Fatalf("pre-fork snapshot missed a conversation: %v", known)
	}

	if inst.waitForAntigravityForkChild(time.Now(), known, 400*time.Millisecond) {
		t.Fatal("fork wait adopted a conversation that existed before /fork")
	}
	if inst.AntigravitySessionID != "" {
		t.Fatalf("known sibling was bound: %q", inst.AntigravitySessionID)
	}

	writeAntigravityMetadata(t, dataDir, map[string]any{
		"conv-parent":  antigravityTestConversationEntry("conv-parent", workDir, time.Now().Add(-time.Minute), false),
		"conv-sibling": antigravityTestConversationEntry("conv-sibling", workDir, time.Now().Add(-time.Second), false),
		"conv-child":   antigravityTestConversationEntry("conv-child", workDir, time.Now(), false),
	})
	if !inst.waitForAntigravityForkChild(time.Now(), known, 2*time.Second) {
		t.Fatal("fork wait did not bind the new child conversation")
	}
	if inst.AntigravitySessionID != "conv-child" {
		t.Fatalf("bound %q, want conv-child", inst.AntigravitySessionID)
	}
}
