package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// TestMenuSessionExposesAllEditableFields verifies that every session field
// editable in the TUI's EditSessionDialog (or otherwise persisted on
// *session.Instance) round-trips through /api/sessions JSON. Drives the
// promotion of MISSING rows in tests/web/PARITY_MATRIX.md.
func TestMenuSessionExposesAllEditableFields(t *testing.T) {
	yoloTrue := true
	cpuLimit := "2.0"
	sandbox := &session.SandboxConfig{
		Enabled:  true,
		Image:    "ghcr.io/example/sandbox:latest",
		CPULimit: &cpuLimit,
	}
	rawToolOpts := json.RawMessage(`{"tool":"claude","options":{"agent":"reviewer"}}`)
	worktrees := []session.MultiRepoWorktree{
		{OriginalPath: "/srv/a", WorktreePath: "/tmp/wt/a", RepoRoot: "/srv/a", Branch: "feature/a"},
	}

	full := &MenuSession{
		ID:                 "full-1",
		Title:              "fully populated",
		Tool:               "claude",
		CanFork:            true,
		Status:             session.StatusRunning,
		GroupPath:          "work",
		ProjectPath:        "/srv/app",
		Order:              0,
		IsConductor:        true,
		ClaudeSessionID:    "claude-abc",
		GeminiSessionID:    "gemini-xyz",
		GeminiModel:        "gemini-2.5-pro",
		GeminiYoloMode:     &yoloTrue,
		CodexSessionID:     "codex-123",
		OpenCodeSessionID:  "opencode-789",
		LatestPrompt:       "what is the meaning of life?",
		Notes:              "remember to test edge cases",
		Color:              "#ff8800",
		Command:            "claude --resume claude-abc",
		Wrapper:            "env FOO=bar {command}",
		Channels:           []string{"plugin:telegram@user/repo"},
		ExtraArgs:          []string{"--agent", "reviewer"},
		ToolOptionsJSON:    rawToolOpts,
		Sandbox:            sandbox,
		SandboxContainer:   "agent-deck-sbx-full-1",
		SSHHost:            "remote.example",
		SSHRemotePath:      "/srv/remote-app",
		MultiRepoEnabled:   true,
		AdditionalPaths:    []string{"/srv/lib", "/srv/api"},
		MultiRepoTempDir:   "/tmp/multi-repo-full-1",
		MultiRepoWorktrees: worktrees,
		WorktreePath:       "/tmp/worktrees/full-1",
		WorktreeRepoRoot:   "/srv/app",
		WorktreeBranch:     "feature/full-1",
		Archived:           true,
		ArchivedAt:         time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		AutoName:           true,
		TaskDescription:    "Add login endpoint",
		WorktreeType:       "git",
		TitleLocked:        true,
		NoTransitionNotify: true,
		LoadedMCPNames:     []string{"exa", "filesystem"},
		GeminiAnalytics: &session.GeminiSessionAnalytics{
			InputTokens:  100,
			OutputTokens: 200,
			Model:        "gemini-2.5-pro",
		},
	}

	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "test"})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "test",
			Items: []MenuItem{
				{Type: MenuItemTypeSession, Session: full},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(resp.Sessions))
	}
	got := resp.Sessions[0]

	cases := []struct {
		key  string
		want any
	}{
		{"canFork", true},
		{"isConductor", true},
		{"claudeSessionId", "claude-abc"},
		{"geminiSessionId", "gemini-xyz"},
		{"geminiModel", "gemini-2.5-pro"},
		{"geminiYoloMode", true},
		{"codexSessionId", "codex-123"},
		{"opencodeSessionId", "opencode-789"},
		{"latestPrompt", "what is the meaning of life?"},
		{"notes", "remember to test edge cases"},
		{"color", "#ff8800"},
		{"command", "claude --resume claude-abc"},
		{"wrapper", "env FOO=bar {command}"},
		{"sandboxContainer", "agent-deck-sbx-full-1"},
		{"sshHost", "remote.example"},
		{"sshRemotePath", "/srv/remote-app"},
		{"multiRepoEnabled", true},
		{"multiRepoTempDir", "/tmp/multi-repo-full-1"},
		{"worktreePath", "/tmp/worktrees/full-1"},
		{"worktreeRepoRoot", "/srv/app"},
		{"worktreeBranch", "feature/full-1"},
		{"archived", true},
		{"autoName", true},
		{"taskDescription", "Add login endpoint"},
		{"worktreeType", "git"},
		{"titleLocked", true},
		{"noTransitionNotify", true},
	}
	for _, c := range cases {
		if !reflect.DeepEqual(got[c.key], c.want) {
			t.Errorf("MenuSession.%s = %v (%T), want %v", c.key, got[c.key], got[c.key], c.want)
		}
	}

	wantSlices := map[string][]string{
		"channels":        {"plugin:telegram@user/repo"},
		"extraArgs":       {"--agent", "reviewer"},
		"additionalPaths": {"/srv/lib", "/srv/api"},
		"loadedMcpNames":  {"exa", "filesystem"},
	}
	for k, want := range wantSlices {
		raw, ok := got[k].([]any)
		if !ok {
			t.Errorf("MenuSession.%s missing or wrong type: %T", k, got[k])
			continue
		}
		gotStrs := make([]string, len(raw))
		for i, v := range raw {
			gotStrs[i], _ = v.(string)
		}
		if !reflect.DeepEqual(gotStrs, want) {
			t.Errorf("MenuSession.%s = %v, want %v", k, gotStrs, want)
		}
	}

	toolOpts, ok := got["toolOptions"].(map[string]any)
	if !ok {
		t.Errorf("MenuSession.toolOptions missing or wrong type: %T", got["toolOptions"])
	} else if toolOpts["tool"] != "claude" {
		t.Errorf("MenuSession.toolOptions.tool = %v, want claude", toolOpts["tool"])
	}

	if _, ok := got["sandbox"].(map[string]any); !ok {
		t.Errorf("MenuSession.sandbox missing or wrong type: %T", got["sandbox"])
	}

	if wts, ok := got["multiRepoWorktrees"].([]any); !ok || len(wts) != 1 {
		t.Errorf("MenuSession.multiRepoWorktrees missing or wrong shape: %v", got["multiRepoWorktrees"])
	}

	if _, ok := got["geminiAnalytics"].(map[string]any); !ok {
		t.Errorf("MenuSession.geminiAnalytics missing or wrong type: %T", got["geminiAnalytics"])
	}
}

// TestMenuSessionOmitsZeroValueFields covers the boundary case: a default-
// constructed MenuSession does NOT carry the new optional fields. omitempty
// keeps the wire compact for sessions that don't use the feature.
func TestMenuSessionOmitsZeroValueFields(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "test"})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "test",
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID:     "minimal",
						Title:  "minimal",
						Tool:   "shell",
						Status: session.StatusIdle,
					},
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	var resp struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	got := resp.Sessions[0]

	shouldBeOmitted := []string{
		"isConductor", "claudeSessionId", "geminiSessionId", "geminiModel",
		"geminiYoloMode", "codexSessionId", "opencodeSessionId", "latestPrompt",
		"notes", "color", "command", "wrapper", "channels", "extraArgs",
		"toolOptions", "sandbox", "sandboxContainer", "sshHost", "sshRemotePath",
		"multiRepoEnabled", "additionalPaths", "multiRepoTempDir",
		"multiRepoWorktrees", "worktreePath", "worktreeRepoRoot", "worktreeBranch",
		"archived", "autoName", "taskDescription", "worktreeType",
		"titleLocked", "noTransitionNotify", "loadedMcpNames", "geminiAnalytics",
	}
	for _, k := range shouldBeOmitted {
		if _, ok := got[k]; ok {
			t.Errorf("expected zero-value MenuSession to omit %q, but it was present: %v", k, got[k])
		}
	}
}

// TestMenuSessionGeminiYoloModePointerFalse covers the *bool boundary: a
// non-nil pointer to false MUST marshal as `false`, not be omitted. omitempty
// on a *bool only drops the field when the pointer itself is nil — YOLO=off
// is a deliberate user choice, distinct from "not set".
func TestMenuSessionGeminiYoloModePointerFalse(t *testing.T) {
	yoloFalse := false
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "test"})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID:             "yolo-off",
						Title:          "yolo-off",
						Tool:           "gemini",
						Status:         session.StatusIdle,
						GeminiYoloMode: &yoloFalse,
					},
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	var resp struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	v, ok := resp.Sessions[0]["geminiYoloMode"]
	if !ok {
		t.Fatalf("expected geminiYoloMode key present for &false pointer, got omitted")
	}
	if b, _ := v.(bool); b {
		t.Fatalf("expected geminiYoloMode=false, got %v", v)
	}
}

// TestToMenuSessionMapsInstanceFields verifies the Instance → MenuSession
// mapping copies each newly-exposed field. This is the persistence-side
// contract; the JSON marshal contract is exercised separately above.
func TestToMenuSessionMapsInstanceFields(t *testing.T) {
	yolo := true
	cpu := "1.5"
	inst := session.NewInstanceWithGroupAndTool("title", "/srv/proj", "work", "claude")
	inst.ID = "inst-1"
	inst.IsConductor = true
	inst.ClaudeSessionID = "claude-1"
	inst.ClaudeDetectedAt = time.Now()
	inst.GeminiSessionID = "gemini-1"
	inst.GeminiModel = "gemini-2.5-pro"
	inst.GeminiYoloMode = &yolo
	inst.CodexSessionID = "codex-1"
	inst.OpenCodeSessionID = "opencode-1"
	inst.LatestPrompt = "hi"
	inst.Notes = "n"
	inst.Color = "#abcdef"
	inst.Command = "claude resume"
	inst.Wrapper = "env X=1 {command}"
	inst.Channels = []string{"ch-1"}
	inst.ExtraArgs = []string{"--foo", "bar"}
	inst.ToolOptionsJSON = json.RawMessage(`{"tool":"claude"}`)
	inst.Sandbox = &session.SandboxConfig{Enabled: true, Image: "img", CPULimit: &cpu}
	inst.SandboxContainer = "sbx-1"
	inst.SSHHost = "host"
	inst.SSHRemotePath = "/r"
	inst.MultiRepoEnabled = true
	inst.AdditionalPaths = []string{"/a", "/b"}
	inst.MultiRepoTempDir = "/tmp/mr"
	inst.MultiRepoWorktrees = []session.MultiRepoWorktree{{OriginalPath: "/a", WorktreePath: "/w/a"}}
	inst.WorktreePath = "/w/inst-1"
	inst.WorktreeRepoRoot = "/srv/proj"
	inst.WorktreeBranch = "feat/x"
	archivedTime := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	inst.ArchivedAt = archivedTime
	inst.AutoName = true
	inst.WorktreeType = "jujutsu"
	inst.TitleLocked = true
	inst.NoTransitionNotify = true
	inst.LoadedMCPNames = []string{"m1"}
	inst.GeminiAnalytics = &session.GeminiSessionAnalytics{InputTokens: 1, OutputTokens: 2, Model: "gemini-2.5-pro"}

	ms := toMenuSession(inst)

	if !ms.CanFork {
		t.Fatal("MenuSession.CanFork should mirror Instance.CanFork for forkable sessions")
	}
	if !ms.IsConductor {
		t.Errorf("IsConductor not copied")
	}
	if ms.ClaudeSessionID != "claude-1" {
		t.Errorf("ClaudeSessionID = %q, want claude-1", ms.ClaudeSessionID)
	}
	if ms.GeminiSessionID != "gemini-1" {
		t.Errorf("GeminiSessionID not copied")
	}
	if ms.GeminiModel != "gemini-2.5-pro" {
		t.Errorf("GeminiModel not copied")
	}
	if ms.GeminiYoloMode == nil || !*ms.GeminiYoloMode {
		t.Errorf("GeminiYoloMode not copied")
	}
	if ms.CodexSessionID != "codex-1" {
		t.Errorf("CodexSessionID not copied")
	}
	if ms.OpenCodeSessionID != "opencode-1" {
		t.Errorf("OpenCodeSessionID not copied")
	}
	if ms.LatestPrompt != "hi" {
		t.Errorf("LatestPrompt not copied")
	}
	if ms.Notes != "n" {
		t.Errorf("Notes not copied")
	}
	if ms.Color != "#abcdef" {
		t.Errorf("Color not copied")
	}
	if ms.Command != "claude resume" {
		t.Errorf("Command not copied")
	}
	if ms.Wrapper != "env X=1 {command}" {
		t.Errorf("Wrapper not copied")
	}
	if !reflect.DeepEqual(ms.Channels, []string{"ch-1"}) {
		t.Errorf("Channels not copied: %v", ms.Channels)
	}
	if !reflect.DeepEqual(ms.ExtraArgs, []string{"--foo", "bar"}) {
		t.Errorf("ExtraArgs not copied: %v", ms.ExtraArgs)
	}
	if string(ms.ToolOptionsJSON) != `{"tool":"claude"}` {
		t.Errorf("ToolOptionsJSON not copied: %s", string(ms.ToolOptionsJSON))
	}
	if ms.Sandbox == nil || ms.Sandbox.Image != "img" {
		t.Errorf("Sandbox not copied")
	}
	if ms.SandboxContainer != "sbx-1" {
		t.Errorf("SandboxContainer not copied")
	}
	if ms.SSHHost != "host" || ms.SSHRemotePath != "/r" {
		t.Errorf("SSH fields not copied")
	}
	if !ms.MultiRepoEnabled {
		t.Errorf("MultiRepoEnabled not copied")
	}
	if !reflect.DeepEqual(ms.AdditionalPaths, []string{"/a", "/b"}) {
		t.Errorf("AdditionalPaths not copied")
	}
	if ms.MultiRepoTempDir != "/tmp/mr" {
		t.Errorf("MultiRepoTempDir not copied")
	}
	if len(ms.MultiRepoWorktrees) != 1 || ms.MultiRepoWorktrees[0].WorktreePath != "/w/a" {
		t.Errorf("MultiRepoWorktrees not copied")
	}
	if ms.WorktreePath != "/w/inst-1" || ms.WorktreeRepoRoot != "/srv/proj" || ms.WorktreeBranch != "feat/x" {
		t.Errorf("Worktree fields not copied")
	}
	if !ms.Archived {
		t.Errorf("Archived not set (IsArchived() should return true when ArchivedAt is non-zero)")
	}
	if !ms.ArchivedAt.Equal(archivedTime) {
		t.Errorf("ArchivedAt = %v, want %v", ms.ArchivedAt, archivedTime)
	}
	if !ms.AutoName {
		t.Errorf("AutoName not copied")
	}
	if ms.WorktreeType != "jujutsu" {
		t.Errorf("WorktreeType = %q, want jujutsu", ms.WorktreeType)
	}
	if !ms.TitleLocked || !ms.NoTransitionNotify {
		t.Errorf("Boolean flags not copied")
	}
	if !reflect.DeepEqual(ms.LoadedMCPNames, []string{"m1"}) {
		t.Errorf("LoadedMCPNames not copied")
	}
	if ms.GeminiAnalytics == nil || ms.GeminiAnalytics.InputTokens != 1 {
		t.Errorf("GeminiAnalytics not copied")
	}
}

// TestToMenuSessionArchivedFields verifies that archived sessions carry
// Archived=true and a non-zero ArchivedAt on the wire, while non-archived
// sessions omit both.
func TestToMenuSessionArchivedFields(t *testing.T) {
	t.Run("archived session marks wire fields", func(t *testing.T) {
		inst := session.NewInstanceWithGroupAndTool("archived-sess", "/srv/proj", "work", "claude")
		archivedAt := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
		inst.ArchivedAt = archivedAt

		ms := toMenuSession(inst)

		if !ms.Archived {
			t.Errorf("expected Archived=true for session with non-zero ArchivedAt")
		}
		if !ms.ArchivedAt.Equal(archivedAt) {
			t.Errorf("ArchivedAt = %v, want %v", ms.ArchivedAt, archivedAt)
		}
	})

	t.Run("non-archived session omits archived fields", func(t *testing.T) {
		inst := session.NewInstanceWithGroupAndTool("active-sess", "/srv/proj", "work", "claude")
		// ArchivedAt is zero-value

		ms := toMenuSession(inst)

		if ms.Archived {
			t.Errorf("expected Archived=false for non-archived session")
		}
		if !ms.ArchivedAt.IsZero() {
			t.Errorf("expected zero ArchivedAt for non-archived session, got %v", ms.ArchivedAt)
		}
	})
}

// TestToMenuSessionAutoNameFields verifies AutoName and TaskDescription
// population in toMenuSession.
func TestToMenuSessionAutoNameFields(t *testing.T) {
	t.Run("AutoName=true with warm pane cache populates TaskDescription", func(t *testing.T) {
		const tmuxSessName = "agentdeck_claude-autoname_test001"
		const rawTitle = "Add login endpoint"

		tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
			tmuxSessName: {Title: rawTitle},
		})

		inst := session.NewInstanceWithGroupAndTool("qk-adjective-noun", "/srv/proj", "work", "claude")
		inst.AutoName = true
		inst.SetTmuxSessionForTest(&tmux.Session{Name: tmuxSessName})

		ms := toMenuSession(inst)

		if !ms.AutoName {
			t.Errorf("expected AutoName=true")
		}
		want := tmux.CleanPaneTitle(rawTitle)
		if want == "" {
			t.Fatal("test title cleaned to empty — choose a non-generic title string")
		}
		if ms.TaskDescription != want {
			t.Errorf("TaskDescription = %q, want %q", ms.TaskDescription, want)
		}
	})

	t.Run("AutoName=true with no pane cache produces empty TaskDescription", func(t *testing.T) {
		// No SeedPaneInfoCacheForTest call — cache is cold for this session name.
		inst := session.NewInstanceWithGroupAndTool("qk-cold-cache", "/srv/proj", "work", "claude")
		inst.AutoName = true
		inst.SetTmuxSessionForTest(&tmux.Session{Name: "agentdeck_claude-nocache_cold0001"})

		ms := toMenuSession(inst)

		if !ms.AutoName {
			t.Errorf("expected AutoName=true")
		}
		if ms.TaskDescription != "" {
			t.Errorf("expected empty TaskDescription when cache cold, got %q", ms.TaskDescription)
		}
	})

	t.Run("AutoName=false skips TaskDescription even if pane cache is warm", func(t *testing.T) {
		const tmuxSessName = "agentdeck_claude-noauto_test002"
		tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
			tmuxSessName: {Title: "Some task"},
		})

		inst := session.NewInstanceWithGroupAndTool("manual-title", "/srv/proj", "work", "claude")
		// AutoName stays false (default)
		inst.SetTmuxSessionForTest(&tmux.Session{Name: tmuxSessName})

		ms := toMenuSession(inst)

		if ms.AutoName {
			t.Errorf("expected AutoName=false")
		}
		if ms.TaskDescription != "" {
			t.Errorf("expected empty TaskDescription for non-AutoName session, got %q", ms.TaskDescription)
		}
	})
}

// TestToMenuSessionWorktreeType verifies WorktreeType is copied from Instance.
func TestToMenuSessionWorktreeType(t *testing.T) {
	cases := []struct {
		name         string
		worktreeType string
	}{
		{"git worktree", "git"},
		{"jujutsu worktree", "jujutsu"},
		{"empty (legacy/default)", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := session.NewInstanceWithGroupAndTool("wt-sess", "/srv/proj", "work", "claude")
			inst.WorktreeType = tc.worktreeType

			ms := toMenuSession(inst)

			if ms.WorktreeType != tc.worktreeType {
				t.Errorf("WorktreeType = %q, want %q", ms.WorktreeType, tc.worktreeType)
			}
		})
	}
}
