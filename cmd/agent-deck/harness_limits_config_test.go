package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigGetSetSchema(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runAgentDeck(t, home, "config", "schema", "--json")
	if code != 0 {
		t.Fatalf("schema: %d %s %s", code, stdout, stderr)
	}
	var schema struct {
		Keys []struct {
			Key             string `json:"key"`
			Section         string `json:"section"`
			Label           string `json:"label"`
			Help            string `json:"help"`
			Type            string `json:"type"`
			Default         any    `json:"default"`
			RestartRequired bool   `json:"restart_required"`
		} `json:"keys"`
	}
	if err := json.Unmarshal([]byte(stdout), &schema); err != nil || len(schema.Keys) < 30 {
		t.Fatalf("schema: %v %d keys", err, len(schema.Keys))
	}
	seen := map[string]bool{}
	for _, k := range schema.Keys {
		if k.Help == "" || k.Label == "" || k.Type == "" {
			t.Errorf("key %s lacks label/help/type", k.Key)
		}
		seen[k.Key] = true
	}
	// Every TUI Settings row that edits config.toml has a key.
	for _, want := range []string{"theme", "default_tool", "claude.dangerous_mode", "claude.config_dir", "gemini.yolo_mode", "codex.yolo_mode",
		"hermes.yolo_mode", "updates.check_enabled", "updates.auto_update", "updates.auto_install", "updates.auto_restart",
		"logs.max_size_mb", "logs.max_lines", "logs.remove_orphans", "global_search.enabled", "global_search.tier",
		"global_search.recent_days", "preview.show_output", "preview.show_analytics", "preview.show_notes",
		"preview.notes_output_split", "sync_title", "maintenance.enabled", "system_stats.enabled",
		"system_stats.refresh_seconds", "system_stats.format", "system_stats.show", "display.show_session_timestamps",
		"display.show_pane_titles", "ui.show_only_installed_tools", "ui.embedded_terminal", "ui.sidebar_density",
		"recall.enabled", "macapp.plugins", "macapp.transcript_events", "core.daemon"} {
		if !seen[want] {
			t.Errorf("schema lacks %s", want)
		}
	}
	// The doc's example: ui.theme is an alias of theme.
	stdout, stderr, code = runAgentDeck(t, home, "config", "set", "ui.theme", "light", "--json")
	var set struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
		Path  string `json:"path"`
	}
	if code != 0 || json.Unmarshal([]byte(stdout), &set) != nil || set.Key != "theme" || set.Value != "light" {
		t.Fatalf("set: %d %s %s", code, stdout, stderr)
	}
	if stdout, _, _ := runAgentDeck(t, home, "config", "get", "theme"); strings.TrimSpace(stdout) != "light" {
		t.Fatalf("get after set: %q", stdout)
	}
	data, err := os.ReadFile(set.Path)
	if err != nil || !strings.Contains(string(data), `theme = "light"`) {
		t.Fatalf("config.toml: %v %s", err, data)
	}
	// Other sections survive a set; lists parse; bad values exit 2.
	if err := os.WriteFile(set.Path, append(data, []byte("\n[recall]\nenabled = true\n\n[worktree]\ndefault_location = \"sibling\"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runAgentDeck(t, home, "config", "set", "system_stats.show", "load,cpu"); code != 0 {
		t.Fatalf("list set: %d", code)
	}
	data, _ = os.ReadFile(set.Path)
	for _, want := range []string{`show = ["cpu", "load"]`, "[recall]", "default_location", `theme = "light"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("config.toml lost %q:\n%s", want, data)
		}
	}
	for _, bad := range [][]string{{"theme", "purple"}, {"logs.max_lines", "-3"}, {"no.such.key", "1"}, {"preview.notes_output_split", "2"}} {
		if _, _, code := runAgentDeck(t, home, "config", "set", bad[0], bad[1]); code != 2 {
			t.Errorf("config set %v: exit %d, want 2", bad, code)
		}
	}
	stdout, _, _ = runAgentDeck(t, home, "config", "get", "logs.max_lines", "--json")
	if !strings.Contains(stdout, `"value": 10000`) {
		t.Fatalf("default value: %s", stdout)
	}
}

func TestHarnessListAndStatus(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	_ = os.MkdirAll(bin, 0o755)
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\necho 'codex-cli 0.155.1'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"tokens":{}}`), 0o600)
	writeMacappConfig(t, home, "[harnesses.pi]\ninstall_command = \"example-installer pi\"\n")
	env := []string{"PATH=" + bin + ":/usr/bin:/bin", "CODEX_HOME=" + filepath.Join(home, ".codex"), "OPENAI_API_KEY=", "ANTHROPIC_API_KEY=", "GEMINI_API_KEY=", "GOOGLE_API_KEY="}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "harness", "list", "--json")
	if code != 0 {
		t.Fatalf("harness list: %d %s %s", code, stdout, stderr)
	}
	var list []struct {
		Name           string `json:"name"`
		Installed      bool   `json:"installed"`
		Version        string `json:"version"`
		State          string `json:"state"`
		InstallCommand string `json:"install_command"`
		LoginCommand   string `json:"login_command"`
		DocsURL        string `json:"docs_url"`
		LoggedIn       *bool  `json:"logged_in"`
	}
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatalf("JSON: %v %s", err, stdout)
	}
	byName := map[string]int{}
	for i, h := range list {
		byName[h.Name] = i
		if h.InstallCommand == "" || h.LoginCommand == "" || h.DocsURL == "" {
			t.Errorf("%s lacks install/login/docs", h.Name)
		}
	}
	for _, name := range []string{"claude", "codex", "gemini", "opencode", "pi", "hermes"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("harness list lacks %s", name)
		}
	}
	codex := list[byName["codex"]]
	if !codex.Installed || codex.Version != "0.155.1" || codex.State != "ready" || codex.LoggedIn == nil || !*codex.LoggedIn {
		t.Fatalf("codex: %+v", codex)
	}
	if list[byName["pi"]].InstallCommand != "example-installer pi" {
		t.Fatalf("[harnesses.pi] override ignored: %+v", list[byName["pi"]])
	}
	if h := list[byName["hermes"]]; h.Installed || h.State != "not_installed" {
		t.Fatalf("hermes should be not_installed: %+v", h)
	}
	stdout, _, code = runAgentDeckEnv(t, home, "", env, "harness", "status", "codex", "--json")
	if code != 0 || !strings.Contains(stdout, `"state": "ready"`) {
		t.Fatalf("harness status: %d %s", code, stdout)
	}
	if _, _, code := runAgentDeckEnv(t, home, "", env, "harness", "status", "nope"); code != 2 {
		t.Fatalf("unknown harness: exit %d", code)
	}
}

func TestLimitsClaudeAndCodex(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, ".claude-work")
	_ = os.MkdirAll(work, 0o755)
	if _, _, code := runAgentDeck(t, home, "limits", "--json"); code != 2 {
		t.Fatalf("limits without [macapp] plugins: exit %d", code)
	}
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n\n[profiles.work.claude]\nconfig_dir = \""+work+"\"\n")
	cache := filepath.Join(home, ".cache")
	quotaDir := filepath.Join(cache, "agent-deck", "quota", "work")
	_ = os.MkdirAll(quotaDir, 0o700)
	snap := `{"id":"claude","label":"Claude","windows":[{"kind":"five_hour","label":"5h","used_percentage":40,"resets_at":1790150400},{"kind":"seven_day","label":"7d","used_percentage":20,"resets_at":1790500000}],"updated_at":1790150000}`
	if err := os.WriteFile(filepath.Join(quotaDir, "claude.json"), []byte(snap), 0o644); err != nil {
		t.Fatal(err)
	}
	day := filepath.Join(home, ".codex", "sessions", "2026", "09", "23")
	_ = os.MkdirAll(day, 0o755)
	frame := `{"timestamp":"2026-09-23T07:59:45.472Z","type":"event_msg","payload":{"type":"token_count","info":{},"rate_limits":{"primary":{"used_percent":13.0,"window_minutes":10080,"resets_at":1790700000}}}}` + "\n"
	_ = os.WriteFile(filepath.Join(day, "rollout-2026-09-23T09-59-36-abc.jsonl"), []byte(frame), 0o644)
	env := []string{"XDG_CACHE_HOME=" + cache, "CODEX_HOME=" + filepath.Join(home, ".codex")}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "limits", "--json")
	if code != 0 {
		t.Fatalf("limits: %d %s %s", code, stdout, stderr)
	}
	var res struct {
		Accounts []struct {
			Harness string `json:"harness"`
			Name    string `json:"name"`
			Windows []struct {
				Window   string  `json:"window"`
				UsedPct  float64 `json:"used_pct"`
				ResetsAt string  `json:"resets_at"`
			} `json:"windows"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("JSON: %v %s", err, stdout)
	}
	got := map[string]string{}
	for _, a := range res.Accounts {
		for _, w := range a.Windows {
			b, _ := json.Marshal(w.UsedPct)
			got[a.Harness+"/"+a.Name+"/"+w.Window] = string(b) + "@" + w.ResetsAt
		}
	}
	want := map[string]string{
		"claude/work/5h":       "40@2026-09-23T08:00:00Z",
		"claude/work/7d":       "20@2026-09-27T09:06:40Z",
		"codex/default/weekly": "13@2026-09-29T16:40:00Z",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}
