package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

func TestParseChannelsJSON_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "channels.json")
	content := `{
  "C0AABSF5GKD": {"name": "SI Bugs", "project_path": "/path/to/proj", "group": "bugs", "prefix": "si-bugs"},
  "C1BBCDF6HLE": {"name": "Feature Requests", "project_path": "/path/to/proj2", "group": "features", "prefix": "feat-req"}
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	channels, err := parseChannelsJSON(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}
	si, ok := channels["C0AABSF5GKD"]
	if !ok {
		t.Fatal("missing channel C0AABSF5GKD")
	}
	if si.Name != "SI Bugs" {
		t.Errorf("expected name 'SI Bugs', got %q", si.Name)
	}
	if si.Group != "bugs" {
		t.Errorf("expected group 'bugs', got %q", si.Group)
	}
	if si.Prefix != "si-bugs" {
		t.Errorf("expected prefix 'si-bugs', got %q", si.Prefix)
	}
}

func TestParseChannelsJSON_Invalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "channels.json")
	if err := os.WriteFile(path, []byte(`{invalid json`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := parseChannelsJSON(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestParseChannelsJSON_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "channels.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	channels, err := parseChannelsJSON(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if channels == nil {
		t.Fatal("expected non-nil map for empty JSON object")
	}
	if len(channels) != 0 {
		t.Fatalf("expected 0 channels, got %d", len(channels))
	}
}

func TestParseChannelsJSON_Nonexistent(t *testing.T) {
	_, err := parseChannelsJSON("/nonexistent/path/channels.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestGenerateWatcherToml(t *testing.T) {
	cfg := channelConfig{
		Name:        "SI Bugs",
		ProjectPath: "/path/to/project",
		Group:       "bugs",
		Prefix:      "si-bugs",
	}
	toml := generateWatcherToml("C0AABSF5GKD", cfg)

	checks := []string{
		`name = "si-bugs"`,
		`type = "slack"`,
		`conductor = "si-bugs"`,
		`group = "bugs"`,
		"C0AABSF5GKD",
	}
	for _, check := range checks {
		if !containsStr(toml, check) {
			t.Errorf("generated TOML missing %q\n\nFull output:\n%s", check, toml)
		}
	}
}

func TestMergeClientsJSON_NewFile(t *testing.T) {
	dir := t.TempDir()
	clientsPath := filepath.Join(dir, "clients.json")

	entries := map[string]watcher.ClientEntry{
		"slack:C0AABSF5GKD": {Conductor: "si-bugs", Group: "bugs", Name: "SI Bugs"},
	}
	if err := mergeClientsJSON(clientsPath, entries); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loaded, err := watcher.LoadClientsJSON(clientsPath)
	if err != nil {
		t.Fatalf("failed to load written clients.json: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(loaded))
	}
	entry, ok := loaded["slack:C0AABSF5GKD"]
	if !ok {
		t.Fatal("missing key slack:C0AABSF5GKD")
	}
	if entry.Conductor != "si-bugs" {
		t.Errorf("expected conductor 'si-bugs', got %q", entry.Conductor)
	}
}

func TestMergeClientsJSON_MergeExisting(t *testing.T) {
	dir := t.TempDir()
	clientsPath := filepath.Join(dir, "clients.json")

	// Write existing entry
	existing := map[string]watcher.ClientEntry{
		"user@example.com": {Conductor: "email-watcher", Group: "inbox", Name: "Email User"},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(clientsPath, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Merge new entry
	newEntries := map[string]watcher.ClientEntry{
		"slack:C123": {Conductor: "slack-bugs", Group: "bugs", Name: "Slack Bugs"},
	}
	if err := mergeClientsJSON(clientsPath, newEntries); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loaded, err := watcher.LoadClientsJSON(clientsPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(loaded))
	}
	if _, ok := loaded["user@example.com"]; !ok {
		t.Error("missing existing entry user@example.com")
	}
	if _, ok := loaded["slack:C123"]; !ok {
		t.Error("missing new entry slack:C123")
	}
}

func TestMergeClientsJSON_OverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	clientsPath := filepath.Join(dir, "clients.json")

	// Write existing entry
	existing := map[string]watcher.ClientEntry{
		"slack:C123": {Conductor: "old-conductor", Group: "old-group", Name: "Old Name"},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(clientsPath, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Merge with updated entry for the same key
	newEntries := map[string]watcher.ClientEntry{
		"slack:C123": {Conductor: "new-conductor", Group: "new-group", Name: "New Name"},
	}
	if err := mergeClientsJSON(clientsPath, newEntries); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loaded, err := watcher.LoadClientsJSON(clientsPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	entry := loaded["slack:C123"]
	if entry.Conductor != "new-conductor" {
		t.Errorf("expected conductor 'new-conductor', got %q", entry.Conductor)
	}
	if entry.Group != "new-group" {
		t.Errorf("expected group 'new-group', got %q", entry.Group)
	}
}

func TestImportChannels_EndToEnd(t *testing.T) {
	inputDir := t.TempDir()
	outputDir := t.TempDir()

	channelsJSON := `{
  "C0AABSF5GKD": {"name": "SI Bugs", "project_path": "/path/to/proj", "group": "bugs", "prefix": "si-bugs"},
  "C1BBCDF6HLE": {"name": "Feature Requests", "project_path": "/path/to/proj2", "group": "features", "prefix": "feat-req"}
}`
	inputPath := filepath.Join(inputDir, "channels.json")
	if err := os.WriteFile(inputPath, []byte(channelsJSON), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := importChannels(inputPath, outputDir); err != nil {
		t.Fatalf("importChannels: %v", err)
	}

	// Verify watcher.toml files created
	toml1 := filepath.Join(outputDir, "si-bugs", "watcher.toml")
	if _, err := os.Stat(toml1); os.IsNotExist(err) {
		t.Errorf("missing watcher.toml for si-bugs")
	}
	toml2 := filepath.Join(outputDir, "feat-req", "watcher.toml")
	if _, err := os.Stat(toml2); os.IsNotExist(err) {
		t.Errorf("missing watcher.toml for feat-req")
	}

	// Verify clients.json
	clientsPath := filepath.Join(outputDir, "clients.json")
	loaded, err := watcher.LoadClientsJSON(clientsPath)
	if err != nil {
		t.Fatalf("load clients.json: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 client entries, got %d", len(loaded))
	}
	entry, ok := loaded["slack:C0AABSF5GKD"]
	if !ok {
		t.Fatal("missing key slack:C0AABSF5GKD in clients.json")
	}
	if entry.Conductor != "si-bugs" {
		t.Errorf("expected conductor 'si-bugs', got %q", entry.Conductor)
	}
	if entry.Group != "bugs" {
		t.Errorf("expected group 'bugs', got %q", entry.Group)
	}
	if entry.Name != "SI Bugs" {
		t.Errorf("expected name 'SI Bugs', got %q", entry.Name)
	}
}

func TestImportChannels_Idempotent(t *testing.T) {
	inputDir := t.TempDir()
	outputDir := t.TempDir()

	channelsJSON := `{
  "C0AABSF5GKD": {"name": "SI Bugs", "project_path": "/path/to/proj", "group": "bugs", "prefix": "si-bugs"}
}`
	inputPath := filepath.Join(inputDir, "channels.json")
	if err := os.WriteFile(inputPath, []byte(channelsJSON), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Run twice
	if err := importChannels(inputPath, outputDir); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if err := importChannels(inputPath, outputDir); err != nil {
		t.Fatalf("second import: %v", err)
	}

	// Read the toml content
	tomlPath := filepath.Join(outputDir, "si-bugs", "watcher.toml")
	data1, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read toml: %v", err)
	}
	if len(data1) == 0 {
		t.Fatal("watcher.toml is empty after idempotent import")
	}

	// Verify clients.json still has exactly 1 entry (not duplicated)
	clientsPath := filepath.Join(outputDir, "clients.json")
	loaded, err := watcher.LoadClientsJSON(clientsPath)
	if err != nil {
		t.Fatalf("load clients.json: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 entry after idempotent import, got %d", len(loaded))
	}
}

func TestImportChannels_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()

	// Create a real channels.json
	realPath := filepath.Join(dir, "real-channels.json")
	if err := os.WriteFile(realPath, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Create a symlink to it
	linkPath := filepath.Join(dir, "link-channels.json")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	outputDir := t.TempDir()
	err := importChannels(linkPath, outputDir)
	if err == nil {
		t.Fatal("expected error for symlink input, got nil")
	}
}

func TestImportChannels_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	outputDir := t.TempDir()

	err := importChannels(dir, outputDir)
	if err == nil {
		t.Fatal("expected error for directory input, got nil")
	}
}

func TestImportChannels_EmptyChannels(t *testing.T) {
	inputDir := t.TempDir()
	outputDir := t.TempDir()

	inputPath := filepath.Join(inputDir, "channels.json")
	if err := os.WriteFile(inputPath, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := importChannels(inputPath, outputDir); err != nil {
		t.Fatalf("unexpected error for empty channels: %v", err)
	}

	// No watcher dirs should be created, but clients.json should exist (empty)
	clientsPath := filepath.Join(outputDir, "clients.json")
	if _, err := os.Stat(clientsPath); os.IsNotExist(err) {
		t.Error("expected clients.json to exist even with empty channels")
	}
}

// TestWatcherCreatorSkill_Parseable reads the embedded assets copy of SKILL.md
// (cmd/agent-deck/assets/skills/watcher-creator/SKILL.md) and verifies YAML
// frontmatter fields and body content. This is the committed source of truth;
// the docs/ copy is git-ignored and kept only for local reference.
// The test uses gopkg.in/yaml.v3 (present in go.mod as an indirect dep).
func TestWatcherCreatorSkill_Parseable(t *testing.T) {
	// Path is relative to the cmd/agent-deck package directory; Go tests run from
	// the package directory, so assets/skills/... resolves to the committed embed source.
	skillPath := filepath.Join("assets", "skills", "watcher-creator", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read SKILL.md: %v (run Task 1 to create the file)", err)
	}

	content := string(data)

	// Split on the second "---" delimiter to extract frontmatter.
	// The format is: ---\n<yaml>\n---\n<body>
	if !strings.HasPrefix(content, "---") {
		t.Fatal("SKILL.md does not start with '---' frontmatter delimiter")
	}
	// Find second ---
	rest := content[3:] // skip the opening ---
	idx := strings.Index(rest, "---")
	if idx < 0 {
		t.Fatal("SKILL.md missing closing frontmatter delimiter '---'")
	}
	yamlSection := strings.TrimSpace(rest[:idx])
	body := rest[idx+3:]

	// Parse YAML frontmatter.
	var fm map[string]any
	if err := yaml.Unmarshal([]byte(yamlSection), &fm); err != nil {
		t.Fatalf("parse frontmatter YAML: %v", err)
	}

	// Assert required fields.
	name, ok := fm["name"].(string)
	if !ok || name == "" {
		t.Fatalf("frontmatter missing 'name' field; got %v", fm["name"])
	}
	if name != "watcher-creator" {
		t.Errorf("frontmatter name = %q, want %q", name, "watcher-creator")
	}

	desc, ok := fm["description"].(string)
	if !ok || desc == "" {
		t.Fatal("frontmatter missing non-empty 'description' field")
	}
	if !strings.Contains(strings.ToLower(desc), "watcher") {
		t.Errorf("frontmatter description %q does not mention 'watcher'", desc)
	}

	// Assert body covers all 5 adapter types and the create command.
	for _, required := range []string{"webhook", "ntfy", "github", "slack", "gmail", "agent-deck watcher create"} {
		if !strings.Contains(body, required) {
			t.Errorf("SKILL.md body missing required string %q", required)
		}
	}
}

// TestWatcherCreatorSkill_DocsMatchesAssets byte-compares the docs/ copy of the
// skill against the embedded assets/ copy to detect drift (T-18-22).
// docs/ is git-ignored so this test skips when the docs/ copy is absent.
// Run locally after editing either copy to confirm parity.
func TestWatcherCreatorSkill_DocsMatchesAssets(t *testing.T) {
	files := []string{"SKILL.md", "README.md"}
	for _, f := range files {
		docsPath := filepath.Join("..", "..", "docs", "skills", "watcher-creator", f)
		assetsPath := filepath.Join("assets", "skills", "watcher-creator", f)

		docsData, err := os.ReadFile(docsPath)
		if os.IsNotExist(err) {
			t.Skipf("docs/skills/watcher-creator/%s not present (git-ignored); skipping drift check", f)
		}
		if err != nil {
			t.Fatalf("read docs copy of %s: %v", f, err)
		}
		assetsData, err := os.ReadFile(assetsPath)
		if err != nil {
			t.Fatalf("read assets copy of %s: %v", f, err)
		}
		if !bytes.Equal(docsData, assetsData) {
			t.Errorf("docs/skills/watcher-creator/%s differs from cmd/agent-deck/assets/skills/watcher-creator/%s — update both files to stay in sync", f, f)
		}
	}
}

// TestWatcherInstallSkill verifies that handleWatcherInstallSkill copies both
// SKILL.md and README.md to $HOME/.agent-deck/skills/pool/watcher-creator/.
func TestWatcherInstallSkill(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := handleWatcherInstallSkill("default", []string{"watcher-creator"}); err != nil {
		t.Fatalf("handleWatcherInstallSkill: %v", err)
	}

	poolDir := filepath.Join(tmp, ".agent-deck", "skills", "pool", "watcher-creator")

	// Both files should exist in pool dir.
	for _, f := range []string{"SKILL.md", "README.md"} {
		dest := filepath.Join(poolDir, f)
		if _, err := os.Stat(dest); err != nil {
			t.Errorf("expected %s to exist after install-skill: %v", dest, err)
			continue
		}
		// Content must match the embedded (assets/) source.
		srcPath := filepath.Join("assets", "skills", "watcher-creator", f)
		srcData, err := os.ReadFile(srcPath)
		if err != nil {
			t.Fatalf("read assets source %s: %v", srcPath, err)
		}
		destData, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("read installed %s: %v", dest, err)
		}
		if !bytes.Equal(srcData, destData) {
			t.Errorf("installed %s content does not match source", f)
		}
	}
}

// TestWatcherInstallSkill_DirMode verifies that install-skill creates the pool
// directory hierarchy with mode 0o700 (T-18-21).
func TestWatcherInstallSkill_DirMode(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := handleWatcherInstallSkill("default", []string{"watcher-creator"}); err != nil {
		t.Fatalf("handleWatcherInstallSkill: %v", err)
	}

	dirs := []string{
		filepath.Join(tmp, ".agent-deck", "skills"),
		filepath.Join(tmp, ".agent-deck", "skills", "pool"),
		filepath.Join(tmp, ".agent-deck", "skills", "pool", "watcher-creator"),
	}
	for _, d := range dirs {
		info, err := os.Stat(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		mode := info.Mode().Perm()
		if mode != 0o700 {
			t.Errorf("dir %s has mode %04o, want 0700", d, mode)
		}
	}

	// Installed files should be readable (0o644).
	poolDir := filepath.Join(tmp, ".agent-deck", "skills", "pool", "watcher-creator")
	for _, f := range []string{"SKILL.md", "README.md"} {
		info, err := os.Stat(filepath.Join(poolDir, f))
		if err != nil {
			t.Fatalf("stat %s: %v", f, err)
		}
		mode := info.Mode().Perm()
		if mode != 0o644 {
			t.Errorf("file %s has mode %04o, want 0644", f, mode)
		}
	}
}

// TestWatcherInstallSkill_RejectsUnknownSkill verifies that only "watcher-creator"
// is accepted (path traversal prevention, T-18-20).
func TestWatcherInstallSkill_RejectsUnknownSkill(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	err := handleWatcherInstallSkill("default", []string{"../etc/passwd"})
	if err == nil {
		t.Fatal("expected error for unknown/traversal skill name, got nil")
	}

	err = handleWatcherInstallSkill("default", []string{"unknown-skill"})
	if err == nil {
		t.Fatal("expected error for unknown skill name, got nil")
	}
}

// containsStr is a helper for string-contains checks in tests.
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestWatcherList_JSON_ExposesStateFields(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	seedState := func(name string, s *watcher.WatcherState) {
		t.Helper()
		if err := watcher.SaveState(name, s); err != nil {
			t.Fatalf("SaveState %s: %v", name, err)
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	seedState("healthy-w", &watcher.WatcherState{LastEventTS: now, ErrorCount: 0, AdapterHealthy: true})
	seedState("warning-w", &watcher.WatcherState{LastEventTS: now, ErrorCount: 5, AdapterHealthy: true})
	seedState("error-w", &watcher.WatcherState{LastEventTS: now, ErrorCount: 12, AdapterHealthy: true})
	// fresh-w intentionally has no state.json seeded.

	t.Run("healthy", func(t *testing.T) {
		e := watcherListEntry{Name: "healthy-w"}
		populateStateFields(&e)
		if e.LastEventTS == nil {
			t.Fatal("LastEventTS must not be nil for healthy-w")
		}
		if delta := e.LastEventTS.Sub(now); delta < -time.Second || delta > time.Second {
			t.Errorf("LastEventTS want within 1s of %v, got %v (delta %v)", now, *e.LastEventTS, delta)
		}
		if e.ErrorCount != 0 {
			t.Errorf("ErrorCount want 0, got %d", e.ErrorCount)
		}
		if e.HealthStatus != "healthy" {
			t.Errorf("HealthStatus want %q, got %q", "healthy", e.HealthStatus)
		}
	})

	t.Run("warning", func(t *testing.T) {
		e := watcherListEntry{Name: "warning-w"}
		populateStateFields(&e)
		if e.LastEventTS == nil {
			t.Fatal("LastEventTS must not be nil for warning-w")
		}
		if e.ErrorCount != 5 {
			t.Errorf("ErrorCount want 5, got %d", e.ErrorCount)
		}
		if e.HealthStatus != "warning" {
			t.Errorf("HealthStatus want %q, got %q (threshold per health.go:171 is >=3)", "warning", e.HealthStatus)
		}
	})

	t.Run("error", func(t *testing.T) {
		e := watcherListEntry{Name: "error-w"}
		populateStateFields(&e)
		if e.LastEventTS == nil {
			t.Fatal("LastEventTS must not be nil for error-w")
		}
		if e.ErrorCount != 12 {
			t.Errorf("ErrorCount want 12, got %d", e.ErrorCount)
		}
		if e.HealthStatus != "error" {
			t.Errorf("HealthStatus want %q, got %q (threshold per health.go:164 is >=10)", "error", e.HealthStatus)
		}
	})

	t.Run("fresh", func(t *testing.T) {
		e := watcherListEntry{Name: "fresh-w"}
		populateStateFields(&e)
		if e.LastEventTS != nil {
			t.Errorf("LastEventTS must be nil for fresh-w (no state.json), got %v", *e.LastEventTS)
		}
		if e.ErrorCount != 0 {
			t.Errorf("ErrorCount want 0 for fresh-w, got %d", e.ErrorCount)
		}
		// "unknown" is locked by CONTEXT.md line 59 ("emit `null` / `0` / `"unknown"` respectively").
		// It is NOT one of HealthTracker's Check() values (healthy/warning/error) — this is a
		// CLI-specific "no data yet" marker.
		if e.HealthStatus != "unknown" {
			t.Errorf("HealthStatus want %q (locked by CONTEXT.md for missing state.json), got %q", "unknown", e.HealthStatus)
		}
	})
}

// TestSkillDriftCheck_WatcherCreator reads the embedded SKILL.md and asserts
// that no occurrence of the old plural "watchers/" data-dir path remains.
// This prevents the embedded skill from silently drifting back to pre-v1.6.0
// paths after Phase 21 renamed the directory to singular "watcher/".
// Matches in explicitly marked "// legacy migration" comments are exempt.
func TestSkillDriftCheck_WatcherCreator(t *testing.T) {
	skillPath := filepath.Join("assets", "skills", "watcher-creator", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}

	lines := strings.Split(string(data), "\n")
	var violations []string
	for i, line := range lines {
		if strings.Contains(line, "watchers/") {
			// Allow lines that are explicitly about legacy migration
			if strings.Contains(line, "legacy") || strings.Contains(line, "migration") || strings.Contains(line, "renamed") || strings.Contains(line, "symlink") {
				continue
			}
			violations = append(violations, fmt.Sprintf("  line %d: %s", i+1, strings.TrimSpace(line)))
		}
	}
	if len(violations) > 0 {
		t.Errorf("SKILL.md contains stale 'watchers/' (plural) references that should be 'watcher/' (singular):\n%s",
			strings.Join(violations, "\n"))
	}
}

// TestWatcherHelp_MentionsAdapterExamples asserts the enriched help output
// (issue #628) includes a concrete usage example for every supported adapter
// type, a pointer to the watcher-creator conversational skill, and the README
// reference link. Guards against future help-text regressions that erase the
// documentation users need for first-time setup.
func TestWatcherHelp_MentionsAdapterExamples(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	printWatcherHelp()

	if err := w.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"watcher create webhook",
		"watcher create github",
		"watcher create ntfy",
		"watcher create slack",
		"--port",
		"--secret",
		"--topic",
		"watcher-creator",
		"install-skill",
		"agent-deck#watchers",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("watcher help missing %q\n\nFull output:\n%s", want, out)
		}
	}
}
