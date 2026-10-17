package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const promptSuggestionOffExport = "export CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false;"

func promptSuggestionInstance(parent string) *Instance {
	inst := NewInstanceWithTool("ghost-worker", "/tmp/ghost-worker", "claude")
	inst.ParentSessionID = parent
	return inst
}

// A session agent-deck launches under a parent starts Claude with prompt
// suggestions off, on a fresh start and on the resume command restart uses.
// A root session's launch env does not mention the variable at all.
func TestPromptSuggestions_ParentedOffRootUnchanged(t *testing.T) {
	identityTestEnv(t)
	restore := resetUserConfigCache(t, &UserConfig{})
	defer restore()

	cases := []struct {
		name    string
		parent  string
		wantOff bool
	}{
		{"parented worker", "parent01-1600000000", true},
		{"root session", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fresh := promptSuggestionInstance(tc.parent).buildClaudeCommand("claude")

			resumed := promptSuggestionInstance(tc.parent)
			resumed.ClaudeSessionID = "abc-123-def"
			restart := resumed.buildClaudeResumeCommand()

			for label, cmd := range map[string]string{"start": fresh, "restart/resume": restart} {
				if got := strings.Contains(cmd, promptSuggestionOffExport); got != tc.wantOff {
					t.Errorf("%s: export present=%v, want %v: %s", label, got, tc.wantOff, cmd)
				}
				if !tc.wantOff && strings.Contains(cmd, "CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION") {
					t.Errorf("%s: a root session must not touch the variable: %s", label, cmd)
				}
			}
		})
	}
}

// Session > profile > [launch] > children-off, in both directions, and a
// malformed value at any layer (including the retired "auto") falls through
// to the next.
func TestPromptSuggestions_OverridePrecedence(t *testing.T) {
	identityTestEnv(t)
	withProfileEnv(t, "work")

	cases := []struct {
		name    string
		cfg     *UserConfig
		session string
		parent  string
		wantOff bool
	}{
		{"default parented", &UserConfig{}, "", "p1", true},
		{"default root", &UserConfig{}, "", "", false},
		{"launch children-off parented", &UserConfig{Launch: LaunchSettings{PromptSuggestions: "children-off"}}, "", "p1", true},
		{"global on keeps parented on", &UserConfig{Launch: LaunchSettings{PromptSuggestions: "on"}}, "", "p1", false},
		{"global off turns root off", &UserConfig{Launch: LaunchSettings{PromptSuggestions: "off"}}, "", "", true},
		{"profile beats global", &UserConfig{
			Launch:   LaunchSettings{PromptSuggestions: "off"},
			Profiles: map[string]ProfileSettings{"work": {Claude: ProfileClaudeSettings{PromptSuggestions: "on"}}},
		}, "", "", false},
		{"other profile ignored", &UserConfig{
			Profiles: map[string]ProfileSettings{"personal": {Claude: ProfileClaudeSettings{PromptSuggestions: "off"}}},
		}, "", "", false},
		{"session on beats profile off", &UserConfig{
			Profiles: map[string]ProfileSettings{"work": {Claude: ProfileClaudeSettings{PromptSuggestions: "off"}}},
		}, "on", "p1", false},
		{"session off on a root session", &UserConfig{}, "off", "", true},
		{"session children-off beats launch off", &UserConfig{Launch: LaunchSettings{PromptSuggestions: "off"}}, "children-off", "", false},
		{"profile children-off beats launch on", &UserConfig{
			Launch:   LaunchSettings{PromptSuggestions: "on"},
			Profiles: map[string]ProfileSettings{"work": {Claude: ProfileClaudeSettings{PromptSuggestions: "children-off"}}},
		}, "", "p1", true},
		{"malformed launch falls through to children-off", &UserConfig{Launch: LaunchSettings{PromptSuggestions: "sometimes"}}, "", "p1", true},
		{"retired auto falls through to children-off", &UserConfig{Launch: LaunchSettings{PromptSuggestions: "auto"}}, "", "p1", true},
		{"malformed session falls through to launch", &UserConfig{Launch: LaunchSettings{PromptSuggestions: "off"}}, "auto", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := resetUserConfigCache(t, tc.cfg)
			defer restore()
			inst := promptSuggestionInstance(tc.parent)
			inst.PromptSuggestions = tc.session
			if got := inst.PromptSuggestionsDisabled(); got != tc.wantOff {
				t.Fatalf("PromptSuggestionsDisabled() = %v, want %v", got, tc.wantOff)
			}
			if got := strings.Contains(inst.buildBashExportPrefix(false), promptSuggestionOffExport); got != tc.wantOff {
				t.Errorf("export prefix carries the off switch = %v, want %v", got, tc.wantOff)
			}
		})
	}
}

// The variable is a Claude Code switch: other tools never get it, even with
// suggestions forced off.
func TestPromptSuggestions_NonClaudeUntouched(t *testing.T) {
	identityTestEnv(t)
	restore := resetUserConfigCache(t, &UserConfig{Launch: LaunchSettings{PromptSuggestions: "off"}})
	defer restore()
	inst := NewInstanceWithTool("codex-worker", "/tmp/codex-worker", "codex")
	inst.ParentSessionID = "p1"
	if inst.PromptSuggestionsDisabled() {
		t.Fatal("a codex session must not be treated as having Claude prompt suggestions")
	}
}

func TestNormalizePromptSuggestions(t *testing.T) {
	for in, want := range map[string]string{
		"children-off": "children-off", "Children-Off": "children-off", "ON": "on", " off ": "off", "true": "on", "false": "off", "yes": "on", "0": "off",
	} {
		if got, err := NormalizePromptSuggestions(in); err != nil || got != want {
			t.Errorf("NormalizePromptSuggestions(%q) = (%q, %v), want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "maybe", "offf", "auto"} {
		if _, err := NormalizePromptSuggestions(bad); err == nil {
			t.Errorf("NormalizePromptSuggestions(%q) should fail", bad)
		}
	}
}

// `session set <id> prompt-suggestions` validates, normalizes, clears and is
// restart-required (the value is baked into the launch env).
func TestSetField_PromptSuggestions(t *testing.T) {
	identityTestEnv(t)
	inst := promptSuggestionInstance("p1")

	if _, _, err := SetField(inst, FieldPromptSuggestions, "sometimes", nil); err == nil {
		t.Fatal("an invalid value must be rejected")
	}
	if _, _, err := SetField(inst, FieldPromptSuggestions, "auto", nil); err == nil {
		t.Fatal("the retired value auto must be rejected")
	}
	if old, _, err := SetField(inst, FieldPromptSuggestions, "Children-Off", nil); err != nil || old != "" || inst.PromptSuggestions != "children-off" {
		t.Fatalf("set Children-Off: old=%q err=%v stored=%q", old, err, inst.PromptSuggestions)
	}
	if old, _, err := SetField(inst, FieldPromptSuggestions, "On", nil); err != nil || old != "children-off" || inst.PromptSuggestions != "on" {
		t.Fatalf("set On: old=%q err=%v stored=%q", old, err, inst.PromptSuggestions)
	}
	if old, _, err := SetField(inst, FieldPromptSuggestions, "", nil); err != nil || old != "on" || inst.PromptSuggestions != "" {
		t.Fatalf("clear: old=%q err=%v stored=%q", old, err, inst.PromptSuggestions)
	}
	if RestartPolicyFor(FieldPromptSuggestions) != FieldRestartRequired {
		t.Error("prompt-suggestions must be restart-required")
	}
}

// `config get/set/schema` expose the global switch as launch.prompt_suggestions
// with default children-off; the [claude] key and the auto value are gone.
func TestPromptSuggestions_ConfigKey(t *testing.T) {
	if _, ok := LookupConfigKey("claude.prompt_suggestions"); ok {
		t.Error("claude.prompt_suggestions must no longer be a config key")
	}
	var key ConfigKey
	for _, k := range ConfigKeys() {
		if k.Key == "launch.prompt_suggestions" {
			key = k
		}
	}
	if key.Key == "" {
		t.Fatal("launch.prompt_suggestions missing from the schema")
	}
	if key.Default != PromptSuggestionsChildrenOff || key.Type != "enum" ||
		strings.Join(key.Values, ",") != "children-off,on,off" {
		t.Errorf("schema = default %v, type %q, values %v", key.Default, key.Type, key.Values)
	}
	if _, err := key.Parse("auto"); err == nil {
		t.Error("config set must reject auto")
	}
	cfg := &UserConfig{}
	v, err := key.Parse("off")
	if err != nil {
		t.Fatalf("Parse(off): %v", err)
	}
	key.Set(cfg, v)
	if cfg.Launch.PromptSuggestions != "off" || key.Get(cfg) != "off" {
		t.Errorf("set off: stored %q, get %v", cfg.Launch.PromptSuggestions, key.Get(cfg))
	}
}

func TestPromptSuggestions_ToolDataRoundTrip(t *testing.T) {
	td := WritePromptSuggestionsToToolData(json.RawMessage(`{"context_level":"primer"}`), "off")
	if got := ReadPromptSuggestionsFromToolData(td); got != "off" {
		t.Fatalf("override lost: %q from %s", got, td)
	}
	if !strings.Contains(string(td), `"context_level":"primer"`) {
		t.Errorf("sibling key lost: %s", td)
	}
	td = WritePromptSuggestionsToToolData(td, "")
	if strings.Contains(string(td), toolDataPromptSuggestionsKey) {
		t.Errorf("empty must delete the key: %s", td)
	}
	if got := ReadPromptSuggestionsFromToolData(json.RawMessage("not json")); got != "" {
		t.Errorf("malformed blob must read as unset, got %q", got)
	}
}

// The override survives a save/load and a clear is not resurrected by
// MergeToolDataExtras (the key is registered in statedb's typed blob).
func TestPromptSuggestions_SQLiteRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("prompt-suggestions-roundtrip", "/tmp")
	inst.Tool = "shell"
	inst.PromptSuggestions = "off"
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil || len(loaded) != 1 || loaded[0].PromptSuggestions != "off" {
		t.Fatalf("override not preserved: %+v err=%v", loaded, err)
	}

	loaded[0].PromptSuggestions = ""
	if err := storage.SaveWithGroups(loaded, NewGroupTreeWithGroups(loaded, nil)); err != nil {
		t.Fatalf("SaveWithGroups after clear: %v", err)
	}
	reloaded, _, err := storage.LoadWithGroups()
	if err != nil || len(reloaded) != 1 || reloaded[0].PromptSuggestions != "" {
		t.Fatalf("clear did not survive save/reload: %+v err=%v", reloaded, err)
	}
}

// A fork's first start runs the command baked at construction, so a fork
// nested under a parent must carry the parented launch env in that command
// (review finding B1). Root forks (CLI `session fork`, web fork) stay as they
// were. Non-Claude forks get the parent link and never the Claude variable.
func TestPromptSuggestions_ForkUnderParent(t *testing.T) {
	home := identityTestEnv(t)
	restore := resetUserConfigCache(t, &UserConfig{})
	defer restore()

	for _, tc := range []struct {
		name    string
		parent  ForkParent
		wantOff bool
	}{
		{"parented fork", ForkParent{SessionID: "parent01-1600000000", ProjectPath: "/tmp/parent"}, true},
		{"root fork", ForkParent{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := NewInstanceWithTool("fork-source", "/tmp/fork-source", "claude")
			source.ClaudeSessionID = "11111111-1111-1111-1111-111111111111"
			source.ClaudeDetectedAt = time.Now()

			forked, cmd, err := source.CreateForkedInstanceForToolUnder("fork", "", nil, tc.parent)
			if err != nil {
				t.Fatalf("CreateForkedInstanceForToolUnder: %v", err)
			}
			if forked.ParentSessionID != tc.parent.SessionID || forked.ParentProjectPath != tc.parent.ProjectPath {
				t.Fatalf("parent = (%q, %q), want %+v", forked.ParentSessionID, forked.ParentProjectPath, tc.parent)
			}
			if !forked.IsForkAwaitingStart || forked.Command != cmd {
				t.Fatalf("first start must run the baked command")
			}
			if got := strings.Contains(cmd, promptSuggestionOffExport); got != tc.wantOff {
				t.Errorf("baked fork command carries the export = %v, want %v: %s", got, tc.wantOff, cmd)
			}
		})
	}

	t.Run("pi fork gets the parent, not the variable", func(t *testing.T) {
		source := NewInstanceWithTool("pi-source", home, "pi")
		source.Command = "pi"
		dir := filepath.Join(home, ".pi", "agent-deck", source.ID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		forked, _, err := source.CreateForkedInstanceForToolUnder("pi-fork", "", nil, ForkParent{SessionID: "p1", ProjectPath: "/tmp/p1"})
		if err != nil {
			t.Fatalf("CreateForkedInstanceForToolUnder: %v", err)
		}
		if forked.ParentSessionID != "p1" || forked.ParentProjectPath != "/tmp/p1" {
			t.Fatalf("pi fork parent = (%q, %q)", forked.ParentSessionID, forked.ParentProjectPath)
		}
		if strings.Contains(forked.ForkStartCommand+forked.Command, promptSuggestionEnvVar) {
			t.Errorf("a pi fork must not touch the Claude variable")
		}
	})
}

// A cross-harness switch into Claude keeps the source's parent, and its first
// launch is built by crossHarnessPlanCommand, not buildBashExportPrefix: a
// parented target must still start with suggestions off (review finding N1).
func TestPromptSuggestions_CrossHarnessClaudeTarget(t *testing.T) {
	home := identityTestEnv(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	restore := resetUserConfigCache(t, &UserConfig{})
	defer restore()

	for _, tc := range []struct {
		name    string
		parent  string
		wantOff bool
	}{
		{"parented target", "parent01-1600000000", true},
		{"root target", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := BuildFreshTargetLaunchPlan(syntheticTransferExport("codex"), FreshTargetLaunchOptions{TargetHarness: "claude"})
			if err != nil {
				t.Fatal(err)
			}
			if err := stageCrossHarnessPayload(plan); err != nil {
				t.Fatal(err)
			}
			target := &Instance{ID: plan.Target.InstanceID, Tool: plan.Target.Tool, Account: plan.Target.Account,
				ProjectPath: plan.Target.ProjectPath, Title: plan.Target.Title, GroupPath: plan.Target.GroupPath,
				ParentSessionID: tc.parent}
			command, _, err := crossHarnessPlanCommand(target, plan, "")
			if err != nil {
				t.Fatalf("crossHarnessPlanCommand: %v", err)
			}
			if got := strings.Contains(command, promptSuggestionEnvVar+"=false "); got != tc.wantOff {
				t.Errorf("first Claude launch carries the off switch = %v, want %v: %s", got, tc.wantOff, command)
			}
		})
	}
}
