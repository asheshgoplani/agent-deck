package session

// Tests for v1.16.0 session context injection (SESSION-CONTEXT-PLAN.md).
//
// Red-path contract pinned here:
//   - primer present in the spawn command env spine on FRESH launch
//   - primer env spine STILL present on the RESUME command
//   - level "none" injects nothing (no primer text, no context env exports)
//   - a fact that cannot be determined renders as "unknown", never guessed
//   - the primer stays inside its hard size budget (dumping-ground guard)

import (
	"os"
	"strings"
	"testing"
	"time"
)

// primerTestEnv mirrors channelsTestEnv: isolate HOME/CLAUDE_CONFIG_DIR so
// config resolution is deterministic.
func primerTestEnv(t *testing.T) {
	t.Helper()
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	origHome := os.Getenv("HOME")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	os.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(func() {
		if origConfigDir != "" {
			os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		} else {
			os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	})
}

func TestNormalizeContextLevel(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"none", ContextLevelNone},
		{"primer", ContextLevelPrimer},
		{"full", ContextLevelFull},
		{"  Full  ", ContextLevelFull},
		{"PRIMER", ContextLevelPrimer},
		{"", ""},
		{"medium", ""},
		{"nope", ""},
	}
	for _, c := range cases {
		if got := NormalizeContextLevel(c.in); got != c.want {
			t.Errorf("NormalizeContextLevel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveContextLevel_Precedence(t *testing.T) {
	cfg := &UserConfig{
		ContextLevel: "none",
		Groups: map[string]GroupSettings{
			"projects":         {ContextLevel: "full"},
			"projects/backend": {ContextLevel: "primer"},
		},
	}

	cases := []struct {
		name       string
		cfg        *UserConfig
		inst       *Instance
		wantLevel  string
		wantSource string
	}{
		{"session wins over everything",
			cfg, &Instance{ContextLevel: "none", GroupPath: "projects/backend"},
			ContextLevelNone, "session"},
		{"exact group",
			cfg, &Instance{GroupPath: "projects/backend"},
			ContextLevelPrimer, "group"},
		{"ancestor group walk",
			cfg, &Instance{GroupPath: "projects/frontend"},
			ContextLevelFull, "group"},
		{"global when no group matches",
			cfg, &Instance{GroupPath: "elsewhere"},
			ContextLevelNone, "global"},
		{"conductor default is full",
			nil, &Instance{IsConductor: true},
			ContextLevelFull, "default-conductor"},
		{"worker default is primer",
			nil, &Instance{},
			ContextLevelPrimer, "default"},
		{"conductor default loses to explicit global",
			&UserConfig{ContextLevel: "primer"}, &Instance{IsConductor: true},
			ContextLevelPrimer, "global"},
		{"invalid session value falls through",
			nil, &Instance{ContextLevel: "bogus"},
			ContextLevelPrimer, "default"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			level, source := ResolveContextLevel(c.cfg, c.inst)
			if level != c.wantLevel || source != c.wantSource {
				t.Errorf("ResolveContextLevel = (%q, %q), want (%q, %q)",
					level, source, c.wantLevel, c.wantSource)
			}
		})
	}
}

func TestValidMutableFields_IncludesContextLevel(t *testing.T) {
	for _, f := range ValidMutableFields {
		if f == FieldContextLevel {
			if RestartPolicyFor(FieldContextLevel) != FieldRestartRequired {
				t.Fatalf("FieldContextLevel must be restart-required (injection happens at spawn command build)")
			}
			return
		}
	}
	t.Fatalf("FieldContextLevel (%q) not in ValidMutableFields; CLI will reject `session set <id> context-level`", FieldContextLevel)
}

func TestSetField_ContextLevel(t *testing.T) {
	inst := &Instance{Title: "x"}
	if _, _, err := SetField(inst, FieldContextLevel, "full", nil); err != nil {
		t.Fatalf("SetField(context-level, full) errored: %v", err)
	}
	if inst.ContextLevel != ContextLevelFull {
		t.Fatalf("ContextLevel = %q, want full", inst.ContextLevel)
	}
	if _, _, err := SetField(inst, FieldContextLevel, "", nil); err != nil {
		t.Fatalf("clearing context-level errored: %v", err)
	}
	if inst.ContextLevel != "" {
		t.Fatalf("ContextLevel = %q after clear, want empty (inherit)", inst.ContextLevel)
	}
	if _, _, err := SetField(inst, FieldContextLevel, "medium", nil); err == nil {
		t.Fatalf("SetField(context-level, medium) should reject an invalid level")
	}
}

func TestContextLevelToolDataRoundTrip(t *testing.T) {
	td := WriteContextLevelToToolData(nil, "full")
	if got := ReadContextLevelFromToolData(td); got != ContextLevelFull {
		t.Fatalf("round-trip = %q, want full", got)
	}
	// Empty level removes the key: pre-1.16 blob shape, clean downgrades.
	td = WriteContextLevelToToolData(td, "")
	if strings.Contains(string(td), toolDataContextLevelKey) {
		t.Fatalf("clearing the level must remove the key, blob = %s", td)
	}
	if got := ReadContextLevelFromToolData(td); got != "" {
		t.Fatalf("cleared blob reads %q, want empty", got)
	}
	if got := ReadContextLevelFromToolData(nil); got != "" {
		t.Fatalf("legacy nil blob reads %q, want empty", got)
	}
}

func TestLifecycleAtLaunch(t *testing.T) {
	cases := []struct {
		name string
		inst *Instance
		want string
	}{
		{"claude fresh", &Instance{Tool: "claude"}, LifecycleCreated},
		{"claude bound id", &Instance{Tool: "claude", ClaudeSessionID: "abc"}, LifecycleResumed},
		{"gemini bound id", &Instance{Tool: "gemini", GeminiSessionID: "g1"}, LifecycleResumed},
		{"opencode fresh", &Instance{Tool: "opencode"}, LifecycleCreated},
		{"codex bound id", &Instance{Tool: "codex", CodexSessionID: "c1"}, LifecycleResumed},
		{"pi previously started", &Instance{Tool: "pi", LastStartedAt: time.Now()}, LifecycleResumed},
		{"pi never started", &Instance{Tool: "pi"}, LifecycleCreated},
		{"deepseek prior start, no id", &Instance{Tool: "deepseek", LastStartedAt: time.Now()}, LifecycleUnknown},
		{"nil instance", nil, LifecycleUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.inst.LifecycleAtLaunch(); got != c.want {
				t.Errorf("LifecycleAtLaunch = %q, want %q", got, c.want)
			}
		})
	}
}

func workerFacts() PrimerFacts {
	return PrimerFacts{
		SessionID: "1f9b45f2", Title: "fix-auth-timeout", Group: "projects/backend",
		Dir: "/home/u/w/backend-wt-auth", Host: "local",
		IsWorktree: true, Branch: "fix/auth-timeout", RepoRoot: "/home/u/w/backend",
		Harness: "claude", Model: "claude-sonnet-5", Account: "seminno", Profile: "default",
		ParentID: "9c02d1a7", ParentTitle: "nightly-conductor",
		Lifecycle: LifecycleCreated, Level: ContextLevelPrimer, LevelSource: "default",
	}
}

func TestRenderPrimer_FreshWorker(t *testing.T) {
	out := RenderPrimer(workerFacts(), ContextLevelPrimer)
	for _, want := range []string{
		"<agent-deck-context>",
		"</agent-deck-context>",
		`Session: 1f9b45f2 "fix-auth-timeout" | group: projects/backend | lifecycle: created`,
		"git worktree of /home/u/w/backend, branch fix/auth-timeout",
		"host: local",
		"Harness: claude | model: claude-sonnet-5 | account: seminno | profile: default",
		`Parent: 9c02d1a7 "nightly-conductor" — report results with: agent-deck session send 9c02d1a7`,
		"agent-deck status --json",
		"agent-deck session search",
		"agent-deck session children --follow --until-done",
		"agent-deck session output <id>",
		"agent-deck session primer --json",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("primer missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "Orchestrator extras") {
		t.Errorf("primer level must not include orchestrator extras")
	}
	if strings.Contains(out, "existed before this launch") {
		t.Errorf("fresh primer must not carry the resumed warning")
	}
}

func TestRenderPrimer_Resumed(t *testing.T) {
	f := workerFacts()
	f.Lifecycle = LifecycleResumed
	out := RenderPrimer(f, ContextLevelPrimer)
	for _, want := range []string{
		"lifecycle: resumed",
		"This conversation existed before this launch",
		"agent-deck session output 1f9b45f2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("resumed primer missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderPrimer_Full(t *testing.T) {
	f := workerFacts()
	f.ParentID, f.ParentTitle = "", ""
	f.Level = ContextLevelFull
	out := RenderPrimer(f, ContextLevelFull)
	for _, want := range []string{
		"Orchestrator extras:",
		"agent-deck launch <path> -c <tool> -m",
		"===AGENTDECK_DONE=== status=<ok|fail> summary=",
		"Parent: none (top-level session)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("full primer missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderPrimer_NoneInjectsNothing(t *testing.T) {
	if out := RenderPrimer(workerFacts(), ContextLevelNone); out != "" {
		t.Fatalf("level none must render nothing, got %q", out)
	}
	if out := RenderPrimer(workerFacts(), "garbage"); out != "" {
		t.Fatalf("unrecognized level must render nothing (degrade, never guess), got %q", out)
	}
}

func TestRenderPrimer_UnknownFields(t *testing.T) {
	f := PrimerFacts{Lifecycle: "", Level: ContextLevelPrimer}
	out := RenderPrimer(f, ContextLevelPrimer)
	for _, want := range []string{
		`Session: unknown "unknown" | group: unknown | lifecycle: unknown`,
		"Dir: unknown | host: unknown",
		"Harness: unknown | model: harness default | account: default | profile: unknown",
		"Parent: none (top-level session)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unknown rendering missing %q\n---\n%s", want, out)
		}
	}
}

// TestPrimerBudget is the hard dumping-ground guard. Rendered with realistic
// worst-case facts; growing past the budget must be a conscious decision that
// edits these constants in the same diff as the new content.
func TestPrimerBudget(t *testing.T) {
	f := workerFacts()
	f.Lifecycle = LifecycleResumed // resumed adds the extra guidance line

	primer := RenderPrimer(f, ContextLevelPrimer)
	if lines := strings.Count(primer, "\n") + 1; lines > PrimerMaxLines {
		t.Errorf("primer is %d lines, budget %d — trim it, don't grow it:\n%s", lines, PrimerMaxLines, primer)
	}
	if len(primer) > PrimerMaxChars {
		t.Errorf("primer is %d chars, budget %d:\n%s", len(primer), PrimerMaxChars, primer)
	}

	f.Level = ContextLevelFull
	full := RenderPrimer(f, ContextLevelFull)
	if lines := strings.Count(full, "\n") + 1; lines > FullMaxLines {
		t.Errorf("full primer is %d lines, budget %d:\n%s", lines, FullMaxLines, full)
	}
	if len(full) > FullMaxChars {
		t.Errorf("full primer is %d chars, budget %d:\n%s", len(full), FullMaxChars, full)
	}
}

func TestCollectPrimerFacts_UnknownStaysUnknown(t *testing.T) {
	primerTestEnv(t)
	// A worktree-marked session whose dir is not actually a git repo (e.g.
	// the worktree was deleted, or the dir is remote-only): branch must be
	// the literal "unknown", never the stale creation-time snapshot.
	inst := &Instance{
		ID: "id1", Title: "t", GroupPath: "g", Tool: "claude",
		ProjectPath:    t.TempDir(),
		WorktreePath:   "/gone",
		WorktreeBranch: "stale-branch-snapshot",
	}
	f := CollectPrimerFacts(nil, inst, "", LifecycleCreated)
	if f.Branch != "unknown" {
		t.Errorf("Branch = %q, want the literal unknown (never the persisted snapshot)", f.Branch)
	}
	if f.Host != "local" {
		t.Errorf("Host = %q, want local", f.Host)
	}
}

func TestCollectPrimerFacts_SSHUsesLocationNotProjectPath(t *testing.T) {
	primerTestEnv(t)
	inst := &Instance{
		ID: "id1", Title: "t", GroupPath: "g", Tool: "claude",
		ProjectPath:   "/tmp/local-placeholder", // the #1850-#1853 trap
		SSHHost:       "buildbox",
		SSHRemotePath: "/srv/app",
	}
	f := CollectPrimerFacts(nil, inst, "", LifecycleCreated)
	if f.Host != "buildbox" || f.Dir != "/srv/app" {
		t.Errorf("SSH facts = host %q dir %q, want buildbox /srv/app (ProjectPath is a local placeholder)", f.Host, f.Dir)
	}
}

func TestBuildContextEnvExports(t *testing.T) {
	primerTestEnv(t)
	inst := &Instance{
		ID: "abc123", Title: "worker one", GroupPath: "projects/x",
		Tool: "codex", ParentSessionID: "parent9",
	}

	out := inst.buildContextEnvExports(nil)
	for _, want := range []string{
		"export AGENTDECK_SESSION_ID=abc123",
		"export AGENTDECK_SESSION_TITLE='worker one'",
		"export AGENTDECK_TOOL=codex",
		"export AGENTDECK_GROUP=projects/x",
		"export AGENTDECK_LIFECYCLE=created",
		"export AGENTDECK_CONTEXT_LEVEL=primer",
		"export AGENTDECK_PARENT_ID=parent9",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("env exports missing %q\n---\n%s", want, out)
		}
	}

	// Resume: same spine, lifecycle flips.
	inst.CodexSessionID = "0badc0de-0000-4000-8000-000000000000"
	if out := inst.buildContextEnvExports(nil); !strings.Contains(out, "export AGENTDECK_LIFECYCLE=resumed") {
		t.Errorf("resume exports missing lifecycle=resumed:\n%s", out)
	}

	// Red path: level none emits NOTHING.
	inst.ContextLevel = ContextLevelNone
	if out := inst.buildContextEnvExports(nil); out != "" {
		t.Errorf("level none must emit no context exports, got %q", out)
	}
}

// TestClaudeCommands_CarryContextEnvSpine pins the red-path contract at the
// command-assembly layer: the exports are present on the FRESH claude spawn
// command AND on the RESUME command (an initial message alone would not
// survive a resume — the env spine and the SessionStart hook do).
func TestClaudeCommands_CarryContextEnvSpine(t *testing.T) {
	primerTestEnv(t)

	fresh := &Instance{
		ID: "fresh1", Title: "w", GroupPath: "g", Tool: "claude",
		ProjectPath: t.TempDir(),
	}
	cmd := fresh.buildClaudeCommand("claude")
	if !strings.Contains(cmd, "export AGENTDECK_LIFECYCLE=created") ||
		!strings.Contains(cmd, "export AGENTDECK_SESSION_ID=fresh1") {
		t.Errorf("fresh claude command missing context env spine:\n%s", cmd)
	}

	resumed := &Instance{
		ID: "res1", Title: "w", GroupPath: "g", Tool: "claude",
		ProjectPath:     t.TempDir(),
		ClaudeSessionID: "11111111-2222-4333-8444-555555555555",
	}
	resumed.markClaudeSessionIDVerified()
	rcmd := resumed.buildClaudeResumeCommand()
	if !strings.Contains(rcmd, "export AGENTDECK_LIFECYCLE=resumed") ||
		!strings.Contains(rcmd, "export AGENTDECK_SESSION_ID=res1") {
		t.Errorf("claude RESUME command missing context env spine:\n%s", rcmd)
	}

	// Red path: none injects nothing, fresh or resumed.
	fresh.ContextLevel = ContextLevelNone
	if cmd := fresh.buildClaudeCommand("claude"); strings.Contains(cmd, "AGENTDECK_LIFECYCLE") {
		t.Errorf("level none fresh command must carry no context spine:\n%s", cmd)
	}
	resumed.ContextLevel = ContextLevelNone
	if rcmd := resumed.buildClaudeResumeCommand(); strings.Contains(rcmd, "AGENTDECK_LIFECYCLE") {
		t.Errorf("level none resume command must carry no context spine:\n%s", rcmd)
	}
}

func TestCodexCommand_CarriesContextEnvSpine(t *testing.T) {
	primerTestEnv(t)
	inst := &Instance{
		ID: "cdx1", Title: "w", GroupPath: "g", Tool: "codex",
		ProjectPath: t.TempDir(),
	}
	cmd := inst.buildCodexCommand("codex")
	if !strings.Contains(cmd, "export AGENTDECK_SESSION_ID=cdx1") ||
		!strings.Contains(cmd, "export AGENTDECK_CONTEXT_LEVEL=primer") {
		t.Errorf("codex command missing context env spine:\n%s", cmd)
	}
}

func TestPrimerMessagePrefix(t *testing.T) {
	primerTestEnv(t)

	claude := &Instance{ID: "c1", Title: "w", GroupPath: "g", Tool: "claude", ProjectPath: t.TempDir()}
	if p := claude.PrimerMessagePrefix(); p != "" {
		t.Errorf("claude-compatible tools get the primer via the SessionStart hook; prefix must be empty, got %q", p)
	}

	codex := &Instance{ID: "x1", Title: "w", GroupPath: "g", Tool: "codex", ProjectPath: t.TempDir()}
	p := codex.PrimerMessagePrefix()
	if !strings.Contains(p, "<agent-deck-context>") || !strings.Contains(p, "Harness: codex") {
		t.Errorf("codex prefix must carry the primer, got:\n%s", p)
	}

	codex.ContextLevel = ContextLevelNone
	if p := codex.PrimerMessagePrefix(); p != "" {
		t.Errorf("level none must prepend nothing, got %q", p)
	}
}

func TestPrimerMessagePrefix_DeepSeekExcluded(t *testing.T) {
	primerTestEnv(t)
	// dsh headless replays its one-shot task verbatim on restart; an embedded
	// primer would replay stale lifecycle facts. Env spine only.
	dsh := &Instance{ID: "d1", Title: "w", GroupPath: "g", Tool: "deepseek", ProjectPath: t.TempDir()}
	if p := dsh.PrimerMessagePrefix(); p != "" {
		t.Errorf("deepseek must not get a message-prepended primer, got %q", p)
	}
}

func TestContextEnvTool_PassthroughReportsRealTool(t *testing.T) {
	// #1800/#1821 contract: a Tool=="shell" subcommand-passthrough instance
	// must never export AGENTDECK_TOOL=shell — the pane runs codex/claude.
	inst := &Instance{
		ID: "p1", Title: "codex-sub", GroupPath: "g",
		Tool: "shell", Command: "codex mcp list", SubcommandPassthrough: true,
	}
	if got := inst.contextEnvTool(); got != "codex" {
		t.Fatalf("contextEnvTool = %q, want codex", got)
	}
	plain := &Instance{Tool: "shell", Command: "bash"}
	if got := plain.contextEnvTool(); got != "shell" {
		t.Fatalf("plain shell contextEnvTool = %q, want shell", got)
	}
}
