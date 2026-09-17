package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

// fixture builds a realistic session on disk and returns the request that
// inspects it.
type fixture struct {
	Config     string
	Project    string
	Transcript string
	Host       *ctxinspect.StaticHost
}

func newFixture(t *testing.T, transcriptLines ...string) fixture {
	t.Helper()
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "repo")

	writeFile(t, filepath.Join(config, "CLAUDE.md"), "global instructions")
	writeFile(t, filepath.Join(project, "CLAUDE.md"), "project instructions")
	writeFile(t, filepath.Join(config, "skills", "alpha", "SKILL.md"), strings.Repeat("a", 4000))
	writeFile(t, filepath.Join(config, "skills", "unused", "SKILL.md"), strings.Repeat("u", 8000))
	writeFile(t, filepath.Join(config, "agents", "mine.md"), "my agent")

	f := fixture{
		Config:  config,
		Project: project,
		Host: &ctxinspect.StaticHost{
			MCPTools: []string{"claude"},
			Catalog:  []ctxinspect.CatalogMCP{{Name: "telegram", Description: "chat", SourcePath: "/cfg/config.toml"}},
		},
	}
	if len(transcriptLines) > 0 {
		f.Transcript = writeTranscript(t, transcriptLines...)
	}
	return f
}

func (f fixture) request() ctxinspect.Request {
	return ctxinspect.Request{
		Tool:           "claude",
		SessionRef:     "my-session",
		ProjectPath:    f.Project,
		ConfigDir:      f.Config,
		TranscriptPath: f.Transcript,
		Host:           f.Host,
	}
}

// inspect runs the adapter through the registry, so the report goes through the
// same central reconciliation and validation production uses.
func inspect(t *testing.T, req ctxinspect.Request) *ctxinspect.Report {
	t.Helper()
	adapter := &Adapter{Env: emptyEnv}
	rep, err := ctxinspect.NewRegistry(nil, adapter).Inspect(context.Background(), req)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	return rep
}

func TestAdapterSupports(t *testing.T) {
	a := New()
	host := &ctxinspect.StaticHost{ClaudeTools: []string{"my-claude-wrapper"}}
	tests := []struct {
		tool string
		want bool
	}{
		{"claude", true},
		{"my-claude-wrapper", true}, // a custom [tools.*] entry wrapping claude
		{"codex", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := a.Supports(tc.tool, host); got != tc.want {
			t.Errorf("Supports(%q) = %v, want %v", tc.tool, got, tc.want)
		}
	}
	if !a.Supports("claude", nil) {
		t.Error("a nil host must fall back to exact-name matching, not panic")
	}
}

func TestCapabilitiesAreAnswerableWithoutDisk(t *testing.T) {
	caps := New().Capabilities()
	if caps.Adapter != "claude" || !caps.CanAnchor {
		t.Fatalf("capabilities = %+v", caps)
	}
	// Claude Code does not write its base system prompt anywhere, and this
	// build does not intercept the request. Claiming otherwise would be the
	// exact over-promise the design refuses.
	if caps.CanVerbatimSystem {
		t.Error("CanVerbatimSystem must be false: the Claude base system prompt is not obtainable in this version")
	}
	sys, ok := caps.Category(CategorySystemPrompt)
	if !ok {
		t.Fatal("want the system prompt declared, even though it is reported as the residual")
	}
	if sys.Text != ctxinspect.TextAbsent || sys.Token != ctxinspect.TokenResidual {
		t.Errorf("system prompt capability = %+v, want absent text and a residual number", sys)
	}
	for _, c := range caps.Categories {
		if c.Note == "" {
			t.Errorf("category %q declares no mechanism note", c.Name)
		}
	}
}

func TestInspectObservedSessionReconciles(t *testing.T) {
	f := newFixture(t, recDeferredTools, recAgentListing, recSkillListing, recMCPInstructions, recUserTyped, recAssistant)
	rep := inspect(t, f.request())

	if len(rep.Violations) != 0 {
		t.Fatalf("report violates its own invariants: %v", rep.Violations)
	}
	if rep.Basis != ctxinspect.BasisObserved {
		t.Errorf("Basis = %s, want observed", rep.Basis)
	}
	if rep.Anchor == nil {
		t.Fatal("want a provider-measured anchor for a cold-start session")
	}
	if got := rep.Anchor.Tokens.Prov(); got != ctxinspect.TokenProviderMeasured {
		t.Errorf("anchor provenance = %s, want provider-measured", got)
	}
	anchorTokens, _ := rep.Anchor.Tokens.Value()
	if anchorTokens != 60006 {
		t.Errorf("anchor = %d, want 60006", anchorTokens)
	}
	if !rep.Reconciliation.OK() {
		t.Fatalf("reconciliation = %s: %s", rep.Reconciliation.Status, rep.Reconciliation.Message)
	}

	// The gauge must equal the measured anchor exactly: attributed costs plus
	// the residual, with nothing clamped and nothing invented.
	total, complete := rep.FixedTotal()
	if !complete || total != anchorTokens {
		t.Fatalf("FixedTotal = (%d, %v), want (%d, true)", total, complete, anchorTokens)
	}
	if rep.Unaccounted == nil {
		t.Fatal("want an explicit residual row")
	}
	if rep.Unaccounted.Content.Prov != ctxinspect.TextAbsent {
		t.Errorf("residual text provenance = %s, want absent", rep.Unaccounted.Content.Prov)
	}
	residual, ok := rep.Unaccounted.Load.Actual.Value()
	if !ok || residual < 0 {
		t.Fatalf("residual = (%d, %v), want a non-negative number", residual, ok)
	}
	// The model comes from the turn that actually ran, not from the caller.
	if rep.Model != "claude-opus-4-7" {
		t.Errorf("Model = %q, want the model that served the first turn", rep.Model)
	}
	if rep.Window.Tokens != 1000000 || rep.Window.Source != ctxinspect.WindowModelDefault {
		t.Errorf("Window = %+v, want the model-table figure", rep.Window)
	}
}

func TestInspectDeclaresCategoriesInOrder(t *testing.T) {
	f := newFixture(t, recDeferredTools, recAgentListing, recSkillListing, recMCPInstructions, recAssistant)
	rep := inspect(t, f.request())

	want := []string{CategorySystemTools, CategoryMCP, CategoryMemory, CategorySkills, CategoryAgents}
	if len(rep.Categories) != len(want) {
		t.Fatalf("categories = %d, want %d", len(rep.Categories), len(want))
	}
	for i, name := range want {
		if rep.Categories[i].Name != name {
			t.Fatalf("category %d = %q, want %q", i, rep.Categories[i].Name, name)
		}
	}
	// The system prompt is never a category: it is the residual row, so a
	// category would double-count it.
	if _, ok := rep.Category(CategorySystemPrompt); ok {
		t.Error("the system prompt must not be emitted as a category")
	}
}

func TestInspectSkillsSeparatePotentialFromActual(t *testing.T) {
	f := newFixture(t, recSkillListing, recAssistant)
	rep := inspect(t, f.request())

	skills, ok := rep.Category(CategorySkills)
	if !ok {
		t.Fatal("want a skills category")
	}
	byLabel := map[string]ctxinspect.Item{}
	for _, it := range skills.Items {
		byLabel[it.Label] = it
	}

	// alpha is listed at startup: it costs its catalogue line now, and its body
	// only if invoked.
	alpha, ok := byLabel["alpha"]
	if !ok {
		t.Fatalf("want the listed skill alpha; got %v", skills.Items)
	}
	if alpha.Load.State != ctxinspect.LoadedNow {
		t.Errorf("alpha state = %s, want loaded", alpha.Load.State)
	}
	if alpha.Content.Prov != ctxinspect.TextCaptured {
		t.Errorf("alpha text provenance = %s, want captured", alpha.Content.Prov)
	}
	potential, hasPotential := alpha.PotentialTokens()
	if !hasPotential {
		t.Fatal("want alpha's body reported as potential cost")
	}
	actual, _ := alpha.Load.Actual.Value()
	if potential <= actual {
		t.Errorf("potential %d must exceed the catalogue line's %d", potential, actual)
	}

	// unused is on disk and was not listed: it costs a certain zero today.
	unused, ok := byLabel["unused"]
	if !ok {
		t.Fatalf("want the unlisted skill reported; got %v", skills.Items)
	}
	if unused.Load.State != ctxinspect.Available {
		t.Errorf("unused state = %s, want available", unused.Load.State)
	}
	if v, ok := unused.Load.Actual.Value(); !ok || v != 0 {
		t.Errorf("unused actual = (%d, %v), want a certain zero", v, ok)
	}
	if unused.Lever.Kind != ctxinspect.LeverDeleteDir || unused.Lever.Path == "" {
		t.Errorf("unused lever = %+v, want a delete-directory lever with a path", unused.Lever)
	}

	// Potential must never reach the gauge. The fixed total is exactly the
	// attributed costs plus the residual; adding potential would inflate it by
	// the size of every skill body nobody invoked.
	pot, any := rep.PotentialTotal()
	if !any || pot == 0 {
		t.Fatal("want a non-zero potential total to make the check meaningful")
	}
	attributed, complete := rep.AttributedTotal()
	if !complete {
		t.Fatal("want a complete attributed total for this fixture")
	}
	residual, ok := rep.Unaccounted.Load.Actual.Value()
	if !ok {
		t.Fatal("want a known residual for this fixture")
	}
	fixed, fixedComplete := rep.FixedTotal()
	if !fixedComplete || fixed != attributed+residual {
		t.Fatalf("FixedTotal = (%d, %v), want %d = attributed %d + residual %d, with no potential in it",
			fixed, fixedComplete, attributed+residual, attributed, residual)
	}
}

func TestInspectMemoryIsReconstructedAndLevered(t *testing.T) {
	f := newFixture(t, recSkillListing, recAssistant)
	rep := inspect(t, f.request())

	mem, ok := rep.Category(CategoryMemory)
	if !ok || len(mem.Items) == 0 {
		t.Fatal("want a populated memory category")
	}
	for _, it := range mem.Items {
		if it.Content.Prov != ctxinspect.TextReconstructed {
			t.Errorf("%s text provenance = %s, want reconstructed: the memory chain is not in the transcript", it.Label, it.Content.Prov)
		}
		if it.Lever.Kind != ctxinspect.LeverEditFile || !filepath.IsAbs(it.Lever.Path) {
			t.Errorf("%s lever = %+v, want an edit-file lever with an absolute path", it.Label, it.Lever)
		}
		if it.Badge().Token != ctxinspect.TokenEstimated {
			t.Errorf("%s token provenance = %s, want estimated", it.Label, it.Badge().Token)
		}
		if it.Load.Actual.Method() == "" {
			t.Errorf("%s carries an estimate with no estimator note", it.Label)
		}
	}
	// The injected wrapper is part of the prompt, so it must be part of the
	// reported text.
	if !strings.Contains(mem.Items[0].Content.Text, "Contents of ") {
		t.Errorf("first memory item does not reproduce its injected wrapper: %q", firstLine(mem.Items[0].Content.Text))
	}
}

func TestInspectUpgradesMemoryToCapturedFromNestedRecord(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "repo")
	userMD := writeFile(t, filepath.Join(config, "CLAUDE.md"), "on-disk text")

	rec := `{"type":"attachment","attachment":{"type":"nested_memory","path":"` + userMD +
		`","content":{"path":"` + userMD + `","content":"exact injected text"}}}`
	transcript := writeTranscript(t, rec, recAssistant)

	rep := inspect(t, ctxinspect.Request{
		Tool: "claude", ProjectPath: project, ConfigDir: config, TranscriptPath: transcript,
	})
	mem, _ := rep.Category(CategoryMemory)
	var found bool
	for _, it := range mem.Items {
		if it.Lever.Path != userMD {
			continue
		}
		found = true
		if it.Content.Prov != ctxinspect.TextCaptured {
			t.Errorf("provenance = %s, want captured when the harness recorded the exact bytes", it.Content.Prov)
		}
		if it.Content.Text != "exact injected text" {
			t.Errorf("text = %q, want the recorded bytes rather than the file on disk", it.Content.Text)
		}
	}
	if !found {
		t.Fatalf("want the user memory file in the chain; got %d items", len(mem.Items))
	}
}

func TestInspectMCPCarriesAgentDeckCommandLever(t *testing.T) {
	f := newFixture(t, recMCPInstructions, recDeferredTools, recAssistant)
	rep := inspect(t, f.request())

	mcp, ok := rep.Category(CategoryMCP)
	if !ok || len(mcp.Items) != 1 {
		t.Fatalf("mcp category = %+v, want one item", mcp)
	}
	it := mcp.Items[0]
	// The plugin-qualified name must still match agent-deck's catalog entry,
	// which is what lets the item carry an exact detach command.
	if it.Origin != ctxinspect.OriginAgentDeck {
		t.Errorf("origin = %s, want agent-deck", it.Origin)
	}
	if it.Lever.Kind != ctxinspect.LeverRunCommand {
		t.Fatalf("lever = %+v, want a run-command lever", it.Lever)
	}
	if want := "agent-deck mcp detach my-session telegram"; it.Lever.Command != want {
		t.Errorf("command = %q, want %q", it.Lever.Command, want)
	}
	if it.Content.Prov != ctxinspect.TextCaptured || it.Content.Text == "" {
		t.Errorf("content = %+v, want the verbatim instruction block", it.Content)
	}
}

func TestInspectDeferredToolsAreRolledUpAndNotDoubleCounted(t *testing.T) {
	f := newFixture(t, recDeferredTools, recAssistant)
	rep := inspect(t, f.request())

	tools, ok := rep.Category(CategorySystemTools)
	if !ok {
		t.Fatal("want a system-tools category")
	}
	var leafSum int
	for _, it := range tools.Items {
		if !it.Rollup {
			t.Errorf("%s is not a rollup; a group header counted alongside its children would double-count", it.Label)
			continue
		}
		if len(it.Children) == 0 {
			t.Errorf("%s is a rollup with no children", it.Label)
		}
		for _, c := range it.Children {
			if c.Load.State != ctxinspect.OnDemand && c.Load.State != ctxinspect.LoadedNow {
				t.Errorf("%s has state %s", c.Label, c.Load.State)
			}
			v, ok := c.Load.Actual.Value()
			if !ok {
				t.Errorf("%s has no cost", c.Label)
			}
			leafSum += v
		}
	}
	total, complete := tools.Total()
	if !complete || total != leafSum {
		t.Fatalf("category total = (%d, %v), want the leaf sum %d: the rollup header must not be counted", total, complete, leafSum)
	}
	// The MCP-derived tool must carry the detach command; the built-in must not
	// pretend to be removable.
	for _, it := range tools.Items {
		if it.Label == "telegram" && it.Lever.Kind != ctxinspect.LeverRunCommand {
			t.Errorf("MCP tool group lever = %+v, want a command", it.Lever)
		}
		if strings.Contains(it.Label, "built-in") && it.Lever.Kind != ctxinspect.LeverImmovable {
			t.Errorf("built-in tool group lever = %+v, want immovable", it.Lever)
		}
	}
}

func TestInspectAgentsDistinguishBuiltinFromUserDefined(t *testing.T) {
	f := newFixture(t, recAgentListing, recAssistant)
	rep := inspect(t, f.request())

	agents, ok := rep.Category(CategoryAgents)
	if !ok || len(agents.Items) != 2 {
		t.Fatalf("agents = %+v, want two", agents)
	}
	byLabel := map[string]ctxinspect.Item{}
	for _, it := range agents.Items {
		byLabel[it.Label] = it
	}
	// "Explore" has no definition file, so it is built into the harness.
	if got := byLabel["Explore"]; got.Origin != ctxinspect.OriginHarnessBuiltin || got.Lever.Kind != ctxinspect.LeverImmovable {
		t.Errorf("Explore = origin %s lever %s, want a harness built-in with no lever", got.Origin, got.Lever.Kind)
	}
	// "mine" has one, so it is the user's and carries a lever.
	if got := byLabel["mine"]; got.Origin != ctxinspect.OriginUserConfig || got.Lever.Kind != ctxinspect.LeverEditFile {
		t.Errorf("mine = origin %s lever %s, want a user file with an edit lever", got.Origin, got.Lever.Kind)
	}
}

func TestInspectResumedSessionRefusesAnAnchor(t *testing.T) {
	f := newFixture(t, recCompactBoundary, recSkillListing, recAssistant)
	rep := inspect(t, f.request())

	if rep.Anchor != nil {
		t.Fatal("a compacted session's first turn does not measure a cold prefix; no anchor may be claimed")
	}
	if rep.Reconciliation.Status != ctxinspect.ReconNoAnchor {
		t.Errorf("status = %s, want no-anchor", rep.Reconciliation.Status)
	}
	if rep.Unaccounted == nil || rep.Unaccounted.Load.Actual.Known() {
		t.Error("the residual must be unknown, not zero, when there is nothing to subtract from")
	}
	if !hasCaveat(rep, "anchor-unavailable-resumed") {
		t.Errorf("want an explanatory caveat; got %v", caveatCodes(rep))
	}
	if len(rep.Violations) != 0 {
		t.Fatalf("violations: %v", rep.Violations)
	}
}

func TestInspectWarmCacheDoesNotVetoTheAnchor(t *testing.T) {
	// Anthropic's prompt cache is shared between sessions, so a fresh session
	// routinely reports a large cache read. Treating that as a resume would
	// leave essentially every session without an anchor.
	f := newFixture(t, recSkillListing, recAssistant)
	rep := inspect(t, f.request())

	if rep.Anchor == nil {
		t.Fatal("a warm cache must not disqualify the anchor")
	}
	if !hasCaveat(rep, "anchor-warm-cache") {
		t.Errorf("want the cache split reported as information; got %v", caveatCodes(rep))
	}
}

func TestInspectProjectedWithoutTranscript(t *testing.T) {
	f := newFixture(t)
	rep := inspect(t, f.request())

	if rep.Basis != ctxinspect.BasisProjected {
		t.Errorf("Basis = %s, want projected", rep.Basis)
	}
	if rep.Anchor != nil {
		t.Error("a projection has no session to measure, so it must carry no anchor")
	}
	if rep.Reconciliation.Status != ctxinspect.ReconNoAnchor {
		t.Errorf("status = %s, want no-anchor", rep.Reconciliation.Status)
	}
	if len(rep.Violations) != 0 {
		t.Fatalf("violations: %v", rep.Violations)
	}
	// Skills on disk cannot be claimed to cost a certain zero without a session
	// that shows they did not load.
	skills, _ := rep.Category(CategorySkills)
	for _, it := range skills.Items {
		if it.Load.Actual.Known() {
			t.Errorf("%s claims a known cost with no session observed", it.Label)
		}
		if it.Load.Actual.Reason() == "" {
			t.Errorf("%s has an unknown cost with no reason", it.Label)
		}
	}
	// The memory chain is still useful without a session, and it is what a
	// fresh session would load.
	if mem, ok := rep.Category(CategoryMemory); !ok || len(mem.Items) == 0 {
		t.Error("want the memory hierarchy reported even in a projection")
	}
}

func TestInspectUnreadableTranscriptIsAnError(t *testing.T) {
	f := newFixture(t)
	req := f.request()
	req.TranscriptPath = filepath.Join(t.TempDir(), "gone.jsonl")

	adapter := &Adapter{Env: emptyEnv}
	if _, err := ctxinspect.NewRegistry(nil, adapter).Inspect(context.Background(), req); err == nil {
		t.Fatal("want an error, never a silently empty report")
	}
}

func TestInspectHonoursContextCancellation(t *testing.T) {
	f := newFixture(t, recSkillListing, recAssistant)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := ctxinspect.NewRegistry(nil, &Adapter{Env: emptyEnv}).Inspect(ctx, f.request()); err == nil {
		t.Fatal("want the cancelled context reported")
	}
}

func TestInspectQuantifiesWhatElseLandsInTheResidual(t *testing.T) {
	// The measured total covers the whole first request, so the first typed
	// message and any session-start hook output sit in the residual alongside
	// the system prompt. Saying so is what keeps the residual's label honest.
	f := newFixture(t, recHookContext, recUserTyped, recAssistant)
	rep := inspect(t, f.request())

	if !hasCaveat(rep, "residual-includes-turn-inputs") {
		t.Fatalf("want the extra residual contents quantified; got %v", caveatCodes(rep))
	}
	for _, c := range rep.Caveats {
		if c.Code == "residual-includes-turn-inputs" {
			if !strings.Contains(c.Message, "characters") {
				t.Errorf("caveat does not quantify what it names: %q", c.Message)
			}
		}
	}
}

func TestInspectUnsplittableSkillListingDegradesHonestly(t *testing.T) {
	bad := `{"type":"attachment","attachment":{"type":"skill_listing","content":"- alpha: a.",` +
		`"skillCount":2,"isInitial":true,"names":["alpha","beta"]}}`
	f := newFixture(t, bad, recAssistant)
	rep := inspect(t, f.request())

	if !hasCaveat(rep, "skill-listing-unsplittable") {
		t.Fatalf("want the failed split reported; got %v", caveatCodes(rep))
	}
	skills, _ := rep.Category(CategorySkills)
	var unsplit bool
	for _, it := range skills.Items {
		if it.ID == "skills:listing" {
			unsplit = true
			if it.Content.Prov != ctxinspect.TextCaptured {
				t.Errorf("the unsplit block is still verbatim; provenance = %s", it.Content.Prov)
			}
		}
	}
	if !unsplit {
		t.Error("want the listing reported as one block rather than dropped")
	}
	if len(rep.Violations) != 0 {
		t.Fatalf("violations: %v", rep.Violations)
	}
}

func TestInspectUnknownModelYieldsNoWindow(t *testing.T) {
	rec := `{"type":"assistant","message":{"role":"assistant","model":"claude-unreleased-9",` +
		`"usage":{"input_tokens":1,"cache_creation_input_tokens":10,"cache_read_input_tokens":0}}}`
	f := newFixture(t, rec)
	rep := inspect(t, f.request())

	if rep.Window.Known() {
		t.Fatalf("Window = %+v, want unknown for an unrecognised model", rep.Window)
	}
	if !hasCaveat(rep, "window-unknown") {
		t.Errorf("want the missing denominator explained; got %v", caveatCodes(rep))
	}
	if _, ok := rep.Window.Percent(1000); ok {
		t.Error("a percentage must not be offered against an unknown window")
	}
}

func hasCaveat(rep *ctxinspect.Report, code string) bool {
	for _, c := range rep.Caveats {
		if c.Code == code {
			return true
		}
	}
	return false
}

func caveatCodes(rep *ctxinspect.Report) []string {
	out := make([]string, 0, len(rep.Caveats))
	for _, c := range rep.Caveats {
		out = append(out, c.Code)
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func TestInspectSkillsAreUnknownNotZeroWithoutACatalogue(t *testing.T) {
	// The failure this guards: a head parse that never reached the catalogue
	// reported every skill on disk as a certain, summable zero — the strongest
	// provenance the model has — for content that was in fact in the prefix. It
	// did so on 81 of 230 real transcripts before the head parse was fixed.
	f := newFixture(t, recAssistantPreBlock)
	rep := inspect(t, f.request())

	skills, ok := rep.Category(CategorySkills)
	if !ok {
		t.Fatal("want a skills category")
	}
	if len(skills.Items) == 0 {
		t.Fatal("want the skills on disk listed")
	}
	for _, it := range skills.Items {
		if v, known := it.Load.Actual.Value(); known {
			t.Errorf("%s reports a known cost of %d; with no catalogue read it must be unknown", it.Label, v)
		}
		if it.Load.Actual.Prov() != ctxinspect.TokenUnknown {
			t.Errorf("%s provenance = %s, want unknown", it.Label, it.Load.Actual.Prov())
		}
	}

	var caveat bool
	for _, c := range rep.Caveats {
		if c.Code == "skill-catalogue-unobserved" {
			caveat = true
		}
	}
	if !caveat {
		t.Error("want a caveat saying no catalogue was found, so the unknowns are explained rather than mysterious")
	}
}

func TestInspectAnchorAdvancesPastALateStartupBlock(t *testing.T) {
	// The catalogues arrive after the first turn, so the anchor must come from
	// the turn that carried them and say so.
	f := newFixture(t, recAssistantPreBlock, recSkillListing, recAssistant)
	rep := inspect(t, f.request())

	if rep.Anchor == nil {
		t.Fatal("want an anchor from the turn that carried the startup block")
	}
	if v, _ := rep.Anchor.Tokens.Value(); v != 60006 {
		t.Fatalf("anchor = %d, want 60006; 50222 would be the turn that predates the catalogue", v)
	}
	var advanced bool
	for _, c := range rep.Caveats {
		if c.Code == "anchor-advanced-past-startup-block" {
			advanced = true
		}
	}
	if !advanced {
		t.Error("want the advance disclosed: the measured total also covers the earlier turn")
	}
	skills, _ := rep.Category(CategorySkills)
	var alphaListed bool
	for _, it := range skills.Items {
		if it.Label == "alpha" && it.Load.State == ctxinspect.LoadedNow {
			alphaListed = true
		}
	}
	if !alphaListed {
		t.Error("want the late-injected catalogue attributed per skill")
	}
}

// TestInspectReportsAnAliasedMemoryFileOnce is the report-level half of the
// symlink fix: one row, both names on it, and a lever that names the file the
// bytes actually live in.
//
// The chain-level assertion lives in memory_test.go. This one exists because a
// collapse that never reaches the screen is indistinguishable, to a reader, from
// a file that was silently dropped.
func TestInspectReportsAnAliasedMemoryFileOnce(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	config := filepath.Join(home, "scratch")
	project := filepath.Join(home, "repo")

	real := writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "shared global instructions")
	if err := os.MkdirAll(config, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	alias := filepath.Join(config, "CLAUDE.md")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("symlinks unavailable on this filesystem: %v", err)
	}
	writeFile(t, filepath.Join(project, "CLAUDE.md"), "project instructions")

	rep := inspect(t, ctxinspect.Request{Tool: "claude", ProjectPath: project, ConfigDir: config})
	mem, ok := rep.Category(CategoryMemory)
	if !ok {
		t.Fatal("want a memory category")
	}

	rows := 0
	var aliased ctxinspect.Item
	for _, it := range mem.Items {
		if it.Label == alias || it.Label == real {
			rows++
			aliased = it
		}
	}
	if rows != 1 {
		t.Fatalf("the shared global file produced %d rows, want 1: %s and %s are one file", rows, alias, real)
	}
	if !strings.Contains(aliased.Detail, real) {
		t.Errorf("row detail %q must name the other path; a reader who came looking for %s has to find it here", aliased.Detail, real)
	}
	if !strings.Contains(aliased.Detail, "counted once") {
		t.Errorf("row detail %q must say the file was counted once, not leave the reader to infer it", aliased.Detail)
	}
	if !strings.Contains(aliased.Lever.Why, real) {
		t.Errorf("lever rationale %q must name the file the bytes live in", aliased.Lever.Why)
	}
}
