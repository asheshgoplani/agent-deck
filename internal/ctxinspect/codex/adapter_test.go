package codex

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

// inspect runs the adapter through a registry, so every assertion is made
// against a finalised report: reconciled, validated and carrying the declared
// capabilities, exactly as the CLI and the TUI receive it.
func inspect(t *testing.T, req ctxinspect.Request) *ctxinspect.Report {
	t.Helper()
	if req.Tool == "" {
		req.Tool = "codex"
	}
	if req.Host == nil {
		req.Host = &ctxinspect.StaticHost{}
	}
	reg := ctxinspect.NewRegistry(ctxinspect.NewGenericAdapter(), New())
	rep, err := reg.Inspect(context.Background(), req)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	return rep
}

// categoryNames returns the emitted category names in adapter order.
func categoryNames(rep *ctxinspect.Report) []string {
	out := make([]string, 0, len(rep.Categories))
	for _, c := range rep.Categories {
		out = append(out, c.Name)
	}
	return out
}

// caveatCodes returns the report's caveat codes.
func caveatCodes(rep *ctxinspect.Report) []string {
	out := make([]string, 0, len(rep.Caveats))
	for _, c := range rep.Caveats {
		out = append(out, c.Code)
	}
	return out
}

// hasCaveat reports whether a code is present.
func hasCaveat(rep *ctxinspect.Report, code string) bool {
	for _, c := range rep.Caveats {
		if c.Code == code {
			return true
		}
	}
	return false
}

// findItem returns an item by id from any category.
func findItem(rep *ctxinspect.Report, id string) (ctxinspect.Item, bool) {
	for _, c := range rep.Categories {
		for _, it := range c.Items {
			if it.ID == id {
				return it, true
			}
		}
	}
	return ctxinspect.Item{}, false
}

// realisticRollout builds a rollout shaped like the ones Codex 0.145 writes,
// including the world-state blobs that are embedded inside the prefix blocks.
// It returns the rollout path, the config dir and the project dir.
func realisticRollout(t *testing.T) (rolloutPath, configDir, projectDir string) {
	t.Helper()
	root := t.TempDir()
	configDir = filepath.Join(root, "codexhome")
	projectDir = filepath.Join(root, "work", "project")

	userAgents := "# Global rules\n\nAlways be careful.\n"
	projectAgents := "# Project rules\n\nRun the linter.\n"
	writeFile(t, configDir, "AGENTS.md", userAgents)
	writeFile(t, configDir, "config.toml", "approval_policy = \"never\"\n")
	writeFile(t, projectDir, "AGENTS.md", projectAgents)
	writeFile(t, configDir, filepath.Join("skills", "dataviz", "SKILL.md"), strings.Repeat("body ", 400))

	skillsBody := "## Skills\n" +
		"A skill is a set of local instructions.\n" +
		"### Skill roots\n" +
		"- `r0` = `" + filepath.Join(configDir, "skills") + "`\n" +
		"- dataviz: Charts and plots. (file: " + filepath.Join(configDir, "skills", "dataviz", "SKILL.md") + ")\n" +
		"- plugged: A plugin skill. (file: /opt/x/plugins/pack/skills/plugged/SKILL.md)\n"
	skillsBlock := "<skills_instructions>\n" + skillsBody
	agentsBlob := userAgents + "\n--- project-doc ---\n" + projectAgents
	agentsBlock := "# AGENTS.md instructions\n\n<INSTRUCTIONS>\n" + agentsBlob + "</INSTRUCTIONS>\n"
	typed := "please review the linter config"

	rolloutPath = newRollout(t).
		sessionMeta("sess-real", projectDir, strings.Repeat("base prompt. ", 200)).
		message(roleDeveloper,
			"<permissions instructions>\nFilesystem sandboxing is disabled.",
			"<collaboration_mode># Collaboration Mode: Default\n",
			"<apps_instructions>\n## Apps\n",
			"<plugins_instructions>\n## Plugins\n",
			skillsBlock,
		).
		message(roleDeveloper, "You are `/root`, the primary agent in a team of agents.").
		message(roleDeveloper, "<multi_agent_mode>Do not spawn sub-agents.").
		worldState(map[string]any{
			"agents_md":         map[string]any{"text": agentsBlob},
			"host_skills":       map[string]any{"body": skillsBody, "includeInstructions": true},
			"environments":      map[string]any{"current_date": "2026-07-28"},
			"permissions":       "32ca25d6d85edaeb6f1e19b2a5cc4e08d2a631cd",
			"apps_instructions": true,
		}).
		turnContext("gpt-5.6-sol", "default").
		message(roleUser, agentsBlock, "<environment_context>\n  <cwd>"+projectDir+"</cwd>\n").
		message(roleUser, typed).
		userMessageEvent(typed).
		assistant().
		tokenCount(21579, 21579, 258400).
		write()
	return rolloutPath, configDir, projectDir
}

func TestInspectDeclaresCodexOwnCategorySet(t *testing.T) {
	rolloutPath, configDir, projectDir := realisticRollout(t)
	rep := inspect(t, ctxinspect.Request{
		SessionRef:     "demo",
		ProjectPath:    projectDir,
		ConfigDir:      configDir,
		TranscriptPath: rolloutPath,
	})

	want := []string{
		CategoryBaseInstructions,
		CategoryAgentsMD,
		CategoryHostSkills,
		CategoryEnvironments,
		CategoryDeveloper,
	}
	got := categoryNames(rep)
	if len(got) != len(want) {
		t.Fatalf("categories = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("categories = %v, want %v (adapter order is part of the contract)", got, want)
		}
	}
	// The proof that the taxonomy is adapter-owned: Codex has no "skills"
	// category in the Claude Code sense and must not fabricate one.
	for _, name := range got {
		if name == "skills" || name == "memory-files" || name == "system-tools" || name == "agents" {
			t.Fatalf("category %q belongs to the Claude Code adapter and must not appear in a Codex report", name)
		}
	}
}

func TestInspectReconcilesAgainstTheMeasuredAnchor(t *testing.T) {
	rolloutPath, configDir, projectDir := realisticRollout(t)
	rep := inspect(t, ctxinspect.Request{
		ProjectPath:    projectDir,
		ConfigDir:      configDir,
		TranscriptPath: rolloutPath,
	})

	if len(rep.Violations) != 0 {
		t.Fatalf("report violates its own invariants: %v", rep.Violations)
	}
	if rep.Basis != ctxinspect.BasisObserved {
		t.Errorf("Basis = %v, want observed", rep.Basis)
	}
	if rep.Anchor == nil {
		t.Fatalf("no anchor; caveats: %v", caveatCodes(rep))
	}
	anchor, ok := rep.Anchor.Tokens.Value()
	if !ok || anchor != 21579 {
		t.Fatalf("anchor = %v, want 21579", rep.Anchor.Tokens)
	}
	if rep.Reconciliation.Status != ctxinspect.ReconOK {
		t.Fatalf("reconciliation = %v: %s", rep.Reconciliation.Status, rep.Reconciliation.Message)
	}
	fixed, complete := rep.FixedTotal()
	if !complete || fixed != anchor {
		t.Fatalf("FixedTotal = (%d, %v), want (%d, true): the residual must close against the anchor", fixed, complete, anchor)
	}
	residual, ok := rep.Unaccounted.Load.Actual.Value()
	if !ok || residual <= 0 {
		t.Fatalf("residual = %v, want a positive remainder for the unrecorded tool schemas", rep.Unaccounted.Load.Actual)
	}
	if rep.Unaccounted.Content.Prov != ctxinspect.TextAbsent {
		t.Error("the residual has no text and must say so")
	}
}

func TestInspectDoesNotDoubleCountWorldStateBlobs(t *testing.T) {
	// host_skills and agents_md live inside the injected blocks. Pricing both
	// the block and the snapshot would roughly double the attributed total, so
	// the attributed sum must stay under the measured anchor.
	rolloutPath, configDir, projectDir := realisticRollout(t)
	rep := inspect(t, ctxinspect.Request{
		ProjectPath:    projectDir,
		ConfigDir:      configDir,
		TranscriptPath: rolloutPath,
	})

	attributed, complete := rep.AttributedTotal()
	if !complete {
		t.Fatal("every attributed item must carry a number for this rollout")
	}
	anchor, _ := rep.Anchor.Tokens.Value()
	if attributed >= anchor {
		t.Fatalf("attributed %d >= measured %d: content is counted more than once", attributed, anchor)
	}
	if hasCaveat(rep, "agents-md-world-state-unlocated") || hasCaveat(rep, "host-skills-world-state-unlocated") {
		t.Errorf("both blobs are inside the prefix blocks and must be located, got %v", caveatCodes(rep))
	}
	if !hasCaveat(rep, "permissions-digest-only") {
		t.Error("a digest-only permissions field must be called out rather than priced as text")
	}
}

func TestInspectAttributesTheMergedAgentsBlockPerFile(t *testing.T) {
	rolloutPath, configDir, projectDir := realisticRollout(t)
	rep := inspect(t, ctxinspect.Request{
		ProjectPath:    projectDir,
		ConfigDir:      configDir,
		TranscriptPath: rolloutPath,
	})

	cat, ok := rep.Category(CategoryAgentsMD)
	if !ok {
		t.Fatal("the agents-md category is missing")
	}
	userFile := filepath.Join(configDir, "AGENTS.md")
	projectFile := filepath.Join(projectDir, "AGENTS.md")
	for _, path := range []string{userFile, projectFile} {
		it, found := findItem(rep, "agents-md:"+path)
		if !found {
			t.Fatalf("no item for %s; items: %v", path, itemIDs(cat))
		}
		if it.Content.Prov != ctxinspect.TextCaptured {
			t.Errorf("%s: text provenance = %v, want captured (the bytes came from the injected block)", path, it.Content.Prov)
		}
		if it.Lever.Kind != ctxinspect.LeverEditFile || it.Lever.Path != path {
			t.Errorf("%s: lever = %+v, want an edit-file lever on the file itself", path, it.Lever)
		}
		if v, ok := it.Load.Actual.Value(); !ok || v <= 0 {
			t.Errorf("%s: cost = %v, want a positive estimate", path, it.Load.Actual)
		}
	}
	if _, found := findItem(rep, "agents-md:unattributed"); !found {
		t.Fatal("the wrapper Codex adds around the merge must be its own row, not spread across the files")
	}

	// The category's total must equal the injected block, not the sum of the
	// files on disk: the block is what was actually sent.
	total, complete := cat.Total()
	if !complete || total <= 0 {
		t.Fatalf("category total = (%d, %v)", total, complete)
	}
	if !hasNote(cat, "attributed to a file on disk") {
		t.Errorf("the match rate must be published, notes: %v", cat.Notes)
	}
	if hasCaveat(rep, "agents-md-low-match-rate") {
		t.Error("both files are present verbatim, so the match rate must not be low")
	}
}

func TestInspectReportsALowMatchRateRatherThanGuessing(t *testing.T) {
	// A file edited since the session started no longer matches the injected
	// bytes. The report must say so instead of pretending the current file is
	// what was sent.
	root := t.TempDir()
	configDir := filepath.Join(root, "codexhome")
	projectDir := filepath.Join(root, "project")
	writeFile(t, projectDir, "AGENTS.md", "TOTALLY DIFFERENT CONTENT NOW\n")

	rolloutPath := newRollout(t).
		sessionMeta("sess-drift", projectDir, "BASE").
		message(roleUser, "# AGENTS.md instructions\n\nthe rules as they were when the session started\n").
		assistant().
		tokenCount(9000, 9000, 100000).
		write()

	rep := inspect(t, ctxinspect.Request{
		ProjectPath:    projectDir,
		ConfigDir:      configDir,
		TranscriptPath: rolloutPath,
	})
	// Per file, not per block. A 90%-of-the-whole-block threshold says nothing
	// about WHICH file the missing text belongs to, and stays silent at 91%
	// even when this particular file is the entire gap.
	if !hasCaveat(rep, "agents-md-file-unmatched") {
		t.Fatalf("a drifted file must produce a caveat naming that file, got %v", caveatCodes(rep))
	}
	it, found := findItem(rep, "agents-md:absent:"+filepath.Join(projectDir, "AGENTS.md"))
	if !found {
		t.Fatal("a file on the search path that is not in the block must still be listed")
	}
	if it.Load.State != ctxinspect.Available {
		t.Errorf("load state = %v, want available: nothing put it in this session's prefix", it.Load.State)
	}
	// The cardinal rule, at the one place it used to be broken: the block still
	// has unattributed text, so this file may well be in there under bytes that
	// have since changed. A measured, summable zero would be the report
	// asserting "this costs you nothing" about content it never matched.
	if v, ok := it.Load.Actual.Value(); ok {
		t.Errorf("actual cost = %d with certainty, want an explicit unknown: the injected block is not fully explained, so this file's absence from it was never established", v)
	}
	if it.Load.Actual.Reason() == "" {
		t.Error("the unknown carries no reason, which is the one thing an unknown must always carry")
	}
	if !it.Informational {
		t.Error("the item is counted in the category total: whatever of it is in the block is already priced in the unattributed row, so counting it here would count the same tokens twice")
	}
	if it.Load.Potential == nil {
		t.Error("its body must be reported as potential cost")
	}
	if len(rep.Violations) != 0 {
		t.Fatalf("violations: %v", rep.Violations)
	}
}

func TestInspectSplitsTheSkillCatalogue(t *testing.T) {
	rolloutPath, configDir, projectDir := realisticRollout(t)
	rep := inspect(t, ctxinspect.Request{
		ProjectPath:    projectDir,
		ConfigDir:      configDir,
		TranscriptPath: rolloutPath,
	})

	skill, ok := findItem(rep, "host-skill:dataviz")
	if !ok {
		t.Fatal("the dataviz skill must have its own row")
	}
	if skill.Origin != ctxinspect.OriginUserConfig {
		t.Errorf("origin = %v, want user-config", skill.Origin)
	}
	if skill.Lever.Kind != ctxinspect.LeverDeleteDir {
		t.Errorf("lever = %+v, want a delete-directory lever", skill.Lever)
	}
	if skill.Lever.Path != filepath.Join(configDir, "skills", "dataviz") {
		t.Errorf("lever path = %q", skill.Lever.Path)
	}
	if skill.Load.State != ctxinspect.LoadedNow {
		t.Errorf("load state = %v, want loaded: the catalogue entry is in the prefix", skill.Load.State)
	}
	if skill.Load.Potential == nil {
		t.Fatal("the SKILL.md body must be reported as potential cost")
	}
	potential, _ := skill.Load.Potential.Value()
	actual, _ := skill.Load.Actual.Value()
	if potential <= actual {
		t.Errorf("potential %d must exceed the catalogue line's %d", potential, actual)
	}

	plugged, ok := findItem(rep, "host-skill:plugged")
	if !ok {
		t.Fatal("the plugin skill must have its own row")
	}
	if plugged.Origin != ctxinspect.OriginPlugin {
		t.Errorf("origin = %v, want plugin: it lives under a plugin cache", plugged.Origin)
	}

	if _, ok := findItem(rep, "host-skills:protocol"); !ok {
		t.Error("the catalogue's protocol text is harness overhead and must be its own row")
	}
}

func TestInspectPricesTheBaseSystemPromptVerbatim(t *testing.T) {
	rolloutPath, configDir, projectDir := realisticRollout(t)
	rep := inspect(t, ctxinspect.Request{
		ProjectPath:    projectDir,
		ConfigDir:      configDir,
		TranscriptPath: rolloutPath,
	})

	it, ok := findItem(rep, "base-instructions")
	if !ok {
		t.Fatal("the base system prompt must be a first-class item")
	}
	if it.Content.Prov != ctxinspect.TextCaptured {
		t.Fatalf("provenance = %v, want captured: this is the Codex differentiator", it.Content.Prov)
	}
	if !strings.HasPrefix(it.Content.Text, "base prompt.") {
		t.Fatalf("the text must be readable in full, got %.40q", it.Content.Text)
	}
	if it.Origin != ctxinspect.OriginHarnessBuiltin || it.Lever.Kind != ctxinspect.LeverImmovable {
		t.Errorf("the base prompt is not something the user can change: %+v / %+v", it.Origin, it.Lever)
	}
	if !rep.Capabilities.CanVerbatimSystem {
		t.Error("Capabilities must declare that Codex exposes its system prompt")
	}
}

func TestInspectOmitsCategoriesWithNoSource(t *testing.T) {
	// A rollout with nothing but a base prompt must not grow empty rows: an
	// empty category claims the content costs nothing, which is a different
	// statement from "it was not recorded".
	rolloutPath := newRollout(t).
		sessionMeta("sess-bare", t.TempDir(), "BASE ONLY").
		assistant().
		tokenCount(1200, 1200, 100000).
		write()

	rep := inspect(t, ctxinspect.Request{TranscriptPath: rolloutPath})
	if got := categoryNames(rep); len(got) != 1 || got[0] != CategoryBaseInstructions {
		t.Fatalf("categories = %v, want only %q", got, CategoryBaseInstructions)
	}
	if len(rep.Violations) != 0 {
		t.Fatalf("violations: %v", rep.Violations)
	}
}

func TestInspectRefusesAnAnchorOnAResumedRollout(t *testing.T) {
	rolloutPath := newRollout(t).
		sessionMeta("sess-resumed", t.TempDir(), "BASE").
		message(roleDeveloper, "<permissions instructions>\npolicy").
		assistant().
		tokenCount(94172, 78697747, 258400).
		write()

	rep := inspect(t, ctxinspect.Request{TranscriptPath: rolloutPath})
	if rep.Anchor != nil {
		t.Fatal("a warm first turn must not become an anchor")
	}
	if !hasCaveat(rep, "anchor-unavailable-resumed") {
		t.Fatalf("caveats = %v, want anchor-unavailable-resumed", caveatCodes(rep))
	}
	if rep.Reconciliation.Status != ctxinspect.ReconNoAnchor {
		t.Errorf("reconciliation = %v, want no-anchor", rep.Reconciliation.Status)
	}
	if rep.Unaccounted.Load.Actual.Known() {
		t.Error("with no anchor the remainder is unknown, never zero")
	}
	// The window is still measured: it does not depend on the turn being cold.
	if rep.Window.Tokens != 258400 || rep.Window.Source != ctxinspect.WindowHarnessReported {
		t.Errorf("window = %+v, want the harness-reported 258400", rep.Window)
	}
}

func TestInspectWindowIsNeverInferredFromTheModelName(t *testing.T) {
	rolloutPath := newRollout(t).
		sessionMeta("sess-nowindow", t.TempDir(), "BASE").
		turnContext("gpt-5.6-sol", "default").
		assistant().
		tokenCount(100, 100, 0).
		write()

	rep := inspect(t, ctxinspect.Request{TranscriptPath: rolloutPath})
	if rep.Model != "gpt-5.6-sol" {
		t.Errorf("Model = %q, want the model that served the turn", rep.Model)
	}
	if rep.Window.Known() {
		t.Fatalf("window = %+v, want unknown: no size may be guessed from a model name", rep.Window)
	}
	if !hasCaveat(rep, "window-unknown") {
		t.Errorf("caveats = %v, want window-unknown", caveatCodes(rep))
	}
}

func TestInspectProjectsWhenThereIsNoRollout(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "codexhome")
	projectDir := filepath.Join(root, "project")
	writeFile(t, configDir, "AGENTS.md", "global rules\n")
	writeFile(t, projectDir, "AGENTS.md", "project rules\n")

	rep := inspect(t, ctxinspect.Request{ProjectPath: projectDir, ConfigDir: configDir})
	if rep.Basis != ctxinspect.BasisProjected {
		t.Errorf("Basis = %v, want projected", rep.Basis)
	}
	if rep.Anchor != nil {
		t.Error("a projection has no session to measure and must carry no anchor")
	}
	if !hasCaveat(rep, "no-rollout") {
		t.Fatalf("caveats = %v, want no-rollout", caveatCodes(rep))
	}
	cat, ok := rep.Category(CategoryAgentsMD)
	if !ok || len(cat.Items) != 2 {
		t.Fatalf("projected agents-md category = %+v, want both files", cat)
	}
	for _, it := range cat.Items {
		if it.Content.Prov != ctxinspect.TextReconstructed {
			t.Errorf("%s: provenance = %v, want reconstructed: nothing was observed", it.ID, it.Content.Prov)
		}
	}
	if len(rep.Violations) != 0 {
		t.Fatalf("violations: %v", rep.Violations)
	}
}

func TestInspectLegacyRolloutPricesInstructionsOnlyOnce(t *testing.T) {
	// Older releases wrote the same text both as session_meta.instructions and
	// as an injected block. Counting both would inflate the report.
	shared := "## Skills\nlegacy instructions text"
	rolloutPath := newRollout(t).
		add(recordSessionMeta, map[string]any{
			"session_id":   "legacy",
			"cli_version":  "0.77.0",
			"instructions": shared,
		}).
		message(roleUser, "# AGENTS.md instructions for /tmp/p\n\n"+shared).
		assistant().
		tokenCount(4624, 4624, 0).
		write()

	rep := inspect(t, ctxinspect.Request{TranscriptPath: rolloutPath})
	if _, ok := rep.Category(CategoryBaseInstructions); ok {
		t.Fatal("the duplicated instructions field must not be priced a second time")
	}
	if !hasCaveat(rep, "legacy-instructions-duplicated") {
		t.Fatalf("caveats = %v, want legacy-instructions-duplicated", caveatCodes(rep))
	}
	if len(rep.Violations) != 0 {
		t.Fatalf("violations: %v", rep.Violations)
	}
}

func TestInspectLegacyRolloutPricesAStandaloneInstructionsField(t *testing.T) {
	rolloutPath := newRollout(t).
		add(recordSessionMeta, map[string]any{
			"session_id":   "legacy2",
			"cli_version":  "0.77.0",
			"instructions": "instructions that appear nowhere else",
		}).
		assistant().
		tokenCount(500, 500, 0).
		write()

	rep := inspect(t, ctxinspect.Request{TranscriptPath: rolloutPath})
	it, ok := findItem(rep, "base-instructions:legacy")
	if !ok {
		t.Fatalf("categories = %v, want the legacy instructions priced", categoryNames(rep))
	}
	if it.Content.Prov != ctxinspect.TextCaptured {
		t.Errorf("provenance = %v, want captured", it.Content.Prov)
	}
}

func TestInspectReportsBaseInstructionsAbsentRatherThanZero(t *testing.T) {
	rolloutPath := newRollout(t).
		add(recordSessionMeta, map[string]any{"session_id": "no-base"}).
		message(roleDeveloper, "<permissions instructions>\npolicy").
		assistant().
		tokenCount(700, 700, 0).
		write()

	rep := inspect(t, ctxinspect.Request{TranscriptPath: rolloutPath})
	if _, ok := rep.Category(CategoryBaseInstructions); ok {
		t.Fatal("a base prompt that was not recorded must not appear as a zero row")
	}
	if !hasCaveat(rep, "base-instructions-absent") {
		t.Fatalf("caveats = %v, want base-instructions-absent", caveatCodes(rep))
	}
}

func TestInspectSurfacesUnreadableRecordsAsCaveats(t *testing.T) {
	rolloutPath := newRollout(t).
		sessionMeta("sess-bad", t.TempDir(), "BASE").
		addRaw("{broken").
		assistant().
		tokenCount(100, 100, 1000).
		write()

	rep := inspect(t, ctxinspect.Request{TranscriptPath: rolloutPath})
	if !hasCaveat(rep, "rollout-record-unreadable") {
		t.Fatalf("caveats = %v, want the unreadable record surfaced", caveatCodes(rep))
	}
}

func TestInspectMissingRolloutIsAnError(t *testing.T) {
	reg := ctxinspect.NewRegistry(ctxinspect.NewGenericAdapter(), New())
	_, err := reg.Inspect(context.Background(), ctxinspect.Request{
		Tool:           "codex",
		Host:           &ctxinspect.StaticHost{},
		TranscriptPath: "/nonexistent/rollout.jsonl",
	})
	if err == nil {
		t.Fatal("an unreadable rollout must be an error, never a silently empty report")
	}
}

func TestInspectHonoursContextCancellation(t *testing.T) {
	rolloutPath, configDir, projectDir := realisticRollout(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reg := ctxinspect.NewRegistry(ctxinspect.NewGenericAdapter(), New())
	if _, err := reg.Inspect(ctx, ctxinspect.Request{
		Tool:           "codex",
		Host:           &ctxinspect.StaticHost{},
		ProjectPath:    projectDir,
		ConfigDir:      configDir,
		TranscriptPath: rolloutPath,
	}); err == nil {
		t.Fatal("a cancelled context must abort the inspection")
	}
}

func TestSupportsRoutesThroughTheHostPredicate(t *testing.T) {
	a := New()
	host := &ctxinspect.StaticHost{CodexTools: []string{"my-codex-wrapper"}}
	tests := []struct {
		tool string
		want bool
	}{
		{tool: "codex", want: true},
		{tool: "my-codex-wrapper", want: true},
		{tool: "claude", want: false},
		{tool: "", want: false},
	}
	for _, tc := range tests {
		if got := a.Supports(tc.tool, host); got != tc.want {
			t.Errorf("Supports(%q) = %v, want %v", tc.tool, got, tc.want)
		}
	}
	if a.Supports("codex", nil) != true {
		t.Error("a nil host must fall back to exact-name matching, not panic")
	}
}

func TestCapabilitiesTouchNoDiskAndDeclareCodexCategories(t *testing.T) {
	caps := New().Capabilities()
	if caps.Adapter != "codex" || !caps.CanAnchor || !caps.CanVerbatimSystem {
		t.Fatalf("capabilities = %+v", caps)
	}
	want := []string{
		CategoryBaseInstructions, CategoryAgentsMD, CategoryHostSkills,
		CategoryEnvironments, CategoryDeveloper, CategoryInjected,
	}
	if len(caps.Categories) != len(want) {
		t.Fatalf("declared %d categories, want %d", len(caps.Categories), len(want))
	}
	for i, name := range want {
		cc := caps.Categories[i]
		if cc.Name != name {
			t.Fatalf("category %d = %q, want %q", i, cc.Name, name)
		}
		if cc.Title == "" || cc.Note == "" {
			t.Errorf("category %q must declare a title and a note", name)
		}
	}
}

func TestPotentialFromSizeRefusesAMeaninglessZero(t *testing.T) {
	est := ctxinspect.DefaultEstimator()
	if tc := potentialFromSize(est, 0); tc.Known() {
		t.Errorf("an empty file has no body: %v", tc)
	}
	if tc := potentialFromSize(est, 1); tc.Known() {
		t.Errorf("a file too small to register a token must be unknown, not zero: %v", tc)
	}
	if tc := potentialFromSize(est, 4096); !tc.Known() {
		t.Errorf("a real file must be priced: %v", tc)
	}
}

func TestCodexConfigPathOnlyPointsAtSomethingReal(t *testing.T) {
	dir := t.TempDir()
	if got := codexConfigPath(dir); got != "" {
		t.Errorf("codexConfigPath = %q, want empty: a lever must not point at a missing file", got)
	}
	if got := codexConfigPath(""); got != "" {
		t.Errorf("codexConfigPath(\"\") = %q, want empty", got)
	}
	path := writeFile(t, dir, "config.toml", "x = 1\n")
	if got := codexConfigPath(dir); got != path {
		t.Errorf("codexConfigPath = %q, want %q", got, path)
	}
}

func TestOriginForSkillPath(t *testing.T) {
	tests := []struct {
		path string
		want ctxinspect.Origin
	}{
		{path: "/home/u/.codex/skills/x/SKILL.md", want: ctxinspect.OriginUserConfig},
		{path: "/home/u/.codex/plugins/cache/p/skills/x/SKILL.md", want: ctxinspect.OriginPlugin},
	}
	for _, tc := range tests {
		if got := originForSkillPath(tc.path); got != tc.want {
			t.Errorf("originForSkillPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// itemIDs lists a category's item ids, for failure messages.
func itemIDs(cat ctxinspect.Category) []string {
	out := make([]string, 0, len(cat.Items))
	for _, it := range cat.Items {
		out = append(out, it.ID)
	}
	return out
}

// hasNote reports whether any of a category's notes contains a fragment.
func hasNote(cat ctxinspect.Category, fragment string) bool {
	for _, n := range cat.Notes {
		if strings.Contains(n, fragment) {
			return true
		}
	}
	return false
}
