package session

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeContextLevel(t *testing.T) {
	cases := map[string]string{
		"none":   ContextLevelNone,
		"NONE":   ContextLevelNone,
		" full ": ContextLevelFull,
		"Primer": ContextLevelPrimer,
	}
	for in, want := range cases {
		got, err := NormalizeContextLevel(in)
		if err != nil {
			t.Errorf("NormalizeContextLevel(%q) unexpected error: %v", in, err)
		}
		if got != want {
			t.Errorf("NormalizeContextLevel(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "verbose", "full-text"} {
		if _, err := NormalizeContextLevel(bad); err == nil {
			t.Errorf("NormalizeContextLevel(%q) should have errored", bad)
		}
	}
}

// TestEffectiveContextLevel_Precedence locks global < group < session
// (issue #2260): the nearest, most specific layer wins.
func TestEffectiveContextLevel_Precedence(t *testing.T) {
	identityTestEnv(t)

	t.Run("default is full", func(t *testing.T) {
		inst := identityTestInstance("claude")
		level, source := inst.EffectiveContextLevel()
		if level != ContextLevelFull || source != "default" {
			t.Errorf("got (%q, %q), want (full, default)", level, source)
		}
	})

	t.Run("global sets the floor", func(t *testing.T) {
		restore := resetUserConfigCache(t, &UserConfig{Launch: LaunchSettings{ContextLevel: ContextLevelPrimer}})
		defer restore()
		inst := identityTestInstance("claude")
		level, source := inst.EffectiveContextLevel()
		if level != ContextLevelPrimer || source != "global" {
			t.Errorf("got (%q, %q), want (primer, global)", level, source)
		}
	})

	t.Run("group overrides global", func(t *testing.T) {
		restore := resetUserConfigCache(t, &UserConfig{
			Launch: LaunchSettings{ContextLevel: ContextLevelPrimer},
			Groups: map[string]GroupSettings{
				"conductor/workers": {ContextLevel: ContextLevelFull},
			},
		})
		defer restore()
		inst := identityTestInstance("claude") // GroupPath: conductor/workers
		level, source := inst.EffectiveContextLevel()
		if level != ContextLevelFull || source != "group:conductor/workers" {
			t.Errorf("got (%q, %q), want (full, group:conductor/workers)", level, source)
		}
	})

	t.Run("group inherits from ancestor when the exact path is unset", func(t *testing.T) {
		restore := resetUserConfigCache(t, &UserConfig{
			Groups: map[string]GroupSettings{
				"conductor": {ContextLevel: ContextLevelNone},
			},
		})
		defer restore()
		inst := identityTestInstance("claude") // GroupPath: conductor/workers
		level, source := inst.EffectiveContextLevel()
		if level != ContextLevelNone || source != "group:conductor" {
			t.Errorf("got (%q, %q), want (none, group:conductor)", level, source)
		}
	})

	t.Run("session overrides group and global", func(t *testing.T) {
		restore := resetUserConfigCache(t, &UserConfig{
			Launch: LaunchSettings{ContextLevel: ContextLevelNone},
			Groups: map[string]GroupSettings{
				"conductor/workers": {ContextLevel: ContextLevelNone},
			},
		})
		defer restore()
		inst := identityTestInstance("claude")
		inst.ContextLevel = ContextLevelFull
		level, source := inst.EffectiveContextLevel()
		if level != ContextLevelFull || source != "session" {
			t.Errorf("got (%q, %q), want (full, session)", level, source)
		}
	})

	t.Run("legacy inject_identity=false still means none", func(t *testing.T) {
		off := false
		restore := resetUserConfigCache(t, &UserConfig{Launch: LaunchSettings{InjectIdentity: &off}})
		defer restore()
		inst := identityTestInstance("claude")
		level, source := inst.EffectiveContextLevel()
		if level != ContextLevelNone || source != "global (inject_identity=false)" {
			t.Errorf("got (%q, %q), want (none, global (inject_identity=false))", level, source)
		}
	})

	t.Run("legacy --no-identity wins over a positive group/global override", func(t *testing.T) {
		restore := resetUserConfigCache(t, &UserConfig{
			Launch: LaunchSettings{ContextLevel: ContextLevelFull},
			Groups: map[string]GroupSettings{
				"conductor/workers": {ContextLevel: ContextLevelFull},
			},
		})
		defer restore()
		inst := identityTestInstance("claude")
		inst.IdentityInjectionDisabled = true
		level, source := inst.EffectiveContextLevel()
		if level != ContextLevelNone || source != "session (--no-identity)" {
			t.Errorf("got (%q, %q), want (none, session (--no-identity))", level, source)
		}
	})

	t.Run("malformed config value falls through instead of failing", func(t *testing.T) {
		restore := resetUserConfigCache(t, &UserConfig{Launch: LaunchSettings{ContextLevel: "bogus"}})
		defer restore()
		inst := identityTestInstance("claude")
		level, source := inst.EffectiveContextLevel()
		if level != ContextLevelFull || source != "default" {
			t.Errorf("got (%q, %q), want (full, default)", level, source)
		}
	})
}

func TestContextInjectionActive(t *testing.T) {
	identityTestEnv(t)

	t.Run("none disables", func(t *testing.T) {
		restore := resetUserConfigCache(t, &UserConfig{Launch: LaunchSettings{ContextLevel: ContextLevelNone}})
		defer restore()
		if identityTestInstance("claude").ContextInjectionActive() {
			t.Error("context level none must disable injection")
		}
	})

	t.Run("primer enables", func(t *testing.T) {
		restore := resetUserConfigCache(t, &UserConfig{Launch: LaunchSettings{ContextLevel: ContextLevelPrimer}})
		defer restore()
		if !identityTestInstance("claude").ContextInjectionActive() {
			t.Error("context level primer must enable injection")
		}
	})

	t.Run("ssh and sandbox still excluded regardless of level", func(t *testing.T) {
		restore := resetUserConfigCache(t, &UserConfig{Launch: LaunchSettings{ContextLevel: ContextLevelFull}})
		defer restore()
		ssh := identityTestInstance("claude")
		ssh.SSHHost = "box"
		if ssh.ContextInjectionActive() {
			t.Error("ssh session must stay excluded")
		}
	})
}

// TestBuildPrimerPrompt_ShorterThanFullAndHasRecord locks the primer level's
// rendered text: session identity present, no CLI reference/skills section,
// and unavailable facts render as the same "(none)" placeholder as the full
// block — never invented.
func TestBuildPrimerPrompt_ShorterThanFullAndHasRecord(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("codex")
	primer := inst.BuildPrimerPrompt()
	full := inst.BuildIdentityPrompt()

	for _, want := range []string{
		"- session id: abc12345-1700000000",
		"- title: worker one",
		"- tool: codex",
		"- parent session id: parent01-1600000000",
	} {
		if !strings.Contains(primer, want) {
			t.Errorf("primer missing %q:\n%s", want, primer)
		}
	}
	for _, absent := range []string{
		"agent-deck session send",
		"agent-deck launch <path>",
		"## Skills",
		"===AGENTDECK_DONE===",
	} {
		if strings.Contains(primer, absent) {
			t.Errorf("primer must not contain the CLI reference (%q):\n%s", absent, primer)
		}
	}
	if len(primer) >= len(full) {
		t.Errorf("primer (%d bytes) must be shorter than full (%d bytes)", len(primer), len(full))
	}

	root := &Instance{ID: "root0001-1700000000", Title: "root", Tool: "claude", ProjectPath: "/tmp/p"}
	rootPrimer := root.BuildPrimerPrompt()
	if !strings.Contains(rootPrimer, "- parent session id: (none: this is a root session)") {
		t.Errorf("primer must not invent a parent for a root session:\n%s", rootPrimer)
	}
}

func TestBuildContextPromptForLevel(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")
	if got, want := inst.BuildContextPromptForLevel(ContextLevelPrimer), inst.BuildPrimerPrompt(); got != want {
		t.Errorf("primer level did not dispatch to BuildPrimerPrompt")
	}
	if got, want := inst.BuildContextPromptForLevel(ContextLevelFull), inst.BuildIdentityPrompt(); got != want {
		t.Errorf("full level did not dispatch to BuildIdentityPrompt")
	}
	if got, want := inst.BuildContextPromptForLevel("bogus"), inst.BuildIdentityPrompt(); got != want {
		t.Errorf("unrecognized level must fail safe to the full block")
	}
}

// TestEnsureIdentityFile_WritesPrimerContentAtPrimerLevel is a failing-first
// test for the behaviour that ensureIdentityFile writes the shorter primer
// block, not the full one, when the resolved level is "primer".
func TestEnsureIdentityFile_WritesPrimerContentAtPrimerLevel(t *testing.T) {
	identityTestEnv(t)
	restore := resetUserConfigCache(t, &UserConfig{Launch: LaunchSettings{ContextLevel: ContextLevelPrimer}})
	defer restore()
	inst := identityTestInstance("claude")
	_, file, ok := inst.ensureIdentityFile()
	if !ok {
		t.Fatal("ensureIdentityFile returned !ok at primer level")
	}
	data := readFileContent(t, file)
	if data != inst.BuildPrimerPrompt() {
		t.Errorf("file content is not the primer block:\n%s", data)
	}
	if strings.Contains(data, "## Skills") {
		t.Errorf("primer level must not write the full block:\n%s", data)
	}
}

func readFileContent(t *testing.T, path string) string {
	t.Helper()
	data, err := readFileOrEmpty(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestContextLevel_ToolDataRoundTrip mirrors
// TestIdentityInjectionDisabled_ToolDataRoundTrip for the new key.
func TestContextLevel_ToolDataRoundTrip(t *testing.T) {
	td := WriteContextLevelToToolData(json.RawMessage(`{"idle_timeout_secs":5}`), ContextLevelPrimer)
	if got := ReadContextLevelFromToolData(td); got != ContextLevelPrimer {
		t.Fatalf("level lost: got %q from %s", got, td)
	}
	if !strings.Contains(string(td), `"idle_timeout_secs":5`) {
		t.Errorf("sibling key lost: %s", td)
	}
	td = WriteContextLevelToToolData(td, "")
	if got := ReadContextLevelFromToolData(td); got != "" {
		t.Errorf("empty must clear the override: got %q", got)
	}
	if strings.Contains(string(td), toolDataContextLevelKey) {
		t.Errorf("empty must delete the key: %s", td)
	}
	if got := ReadContextLevelFromToolData(nil); got != "" {
		t.Errorf("missing blob must read as unset, got %q", got)
	}
	if got := ReadContextLevelFromToolData(json.RawMessage("not json")); got != "" {
		t.Errorf("malformed blob must read as unset, got %q", got)
	}
}

// TestSetField_ContextLevel exercises the `session set <id> context-level`
// CLI surface end to end: validation, persistence and clearing.
func TestSetField_ContextLevel(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")

	if _, _, err := SetField(inst, FieldContextLevel, "bogus", nil); err == nil {
		t.Fatal("invalid context-level value must be rejected")
	}

	old, _, err := SetField(inst, FieldContextLevel, "Primer", nil)
	if err != nil {
		t.Fatalf("SetField(context-level, Primer): %v", err)
	}
	if old != "" {
		t.Errorf("old value = %q, want empty (was unset)", old)
	}
	if inst.ContextLevel != ContextLevelPrimer {
		t.Errorf("ContextLevel = %q, want %q (case-insensitive normalization)", inst.ContextLevel, ContextLevelPrimer)
	}

	old, _, err = SetField(inst, FieldContextLevel, "", nil)
	if err != nil {
		t.Fatalf("SetField(context-level, \"\"): %v", err)
	}
	if old != ContextLevelPrimer {
		t.Errorf("old value = %q, want %q", old, ContextLevelPrimer)
	}
	if inst.ContextLevel != "" {
		t.Errorf("clearing must leave ContextLevel empty, got %q", inst.ContextLevel)
	}
}

// TestContextLevel_SQLiteRoundTrip mirrors
// TestIssue1143_IdleTimeout_SQLiteRoundTrip, plus the clear path: a session
// that already has a persisted context-level and is then set back to ""
// must actually lose it on the next load, not have MergeToolDataExtras
// resurrect the stored value because "context_level" looked like an
// extras-zone key an old binary wouldn't know about (the bug this test
// pins: the key must be registered in statedb's typed toolDataBlob so its
// omission is read as an intentional clear).
func TestContextLevel_SQLiteRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("context-level-roundtrip", "/tmp")
	inst.Tool = "shell"
	inst.ContextLevel = ContextLevelPrimer

	groupTree := NewGroupTreeWithGroups([]*Instance{inst}, nil)
	if err := storage.SaveWithGroups([]*Instance{inst}, groupTree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ContextLevel != ContextLevelPrimer {
		t.Fatalf("ContextLevel not preserved across SQLite round-trip: got %+v", loaded)
	}

	// Clear it (as `session set <id> context-level ""` does) and save again.
	loaded[0].ContextLevel = ""
	groupTree2 := NewGroupTreeWithGroups(loaded, nil)
	if err := storage.SaveWithGroups(loaded, groupTree2); err != nil {
		t.Fatalf("SaveWithGroups after clear: %v", err)
	}

	reloaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups after clear: %v", err)
	}
	if len(reloaded) != 1 || reloaded[0].ContextLevel != "" {
		t.Fatalf("clearing ContextLevel did not survive save/reload: got %+v", reloaded)
	}
}

func TestGetGroupContextLevel(t *testing.T) {
	cfg := &UserConfig{
		Groups: map[string]GroupSettings{
			"a":     {ContextLevel: ContextLevelPrimer},
			"a/b/c": {ContextLevel: ContextLevelFull},
		},
	}
	if v, m := cfg.GetGroupContextLevel("a/b/c"); v != ContextLevelFull || m != "a/b/c" {
		t.Errorf("exact match: got (%q, %q)", v, m)
	}
	if v, m := cfg.GetGroupContextLevel("a/b/c/d"); v != ContextLevelFull || m != "a/b/c" {
		t.Errorf("nearest ancestor: got (%q, %q)", v, m)
	}
	if v, m := cfg.GetGroupContextLevel("a/x"); v != ContextLevelPrimer || m != "a" {
		t.Errorf("root ancestor: got (%q, %q)", v, m)
	}
	if v, _ := cfg.GetGroupContextLevel("z"); v != "" {
		t.Errorf("no match must return empty, got %q", v)
	}
	if v, _ := (*UserConfig)(nil).GetGroupContextLevel("a"); v != "" {
		t.Errorf("nil config must return empty, got %q", v)
	}
}
