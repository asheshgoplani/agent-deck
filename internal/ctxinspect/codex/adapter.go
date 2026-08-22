package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

// Category names this adapter declares, in the order it emits them.
//
// They are deliberately not the Claude adapter's names. Codex has no "skills"
// category in the Claude sense — its catalogue is injected as one developer
// block, not as a listing record — and it has a verbatim base prompt, which
// Claude Code does not write to disk at all. The taxonomy belongs to the
// adapter; the renderer prints whatever it is given.
const (
	// CategoryBaseInstructions holds the base system prompt, verbatim.
	CategoryBaseInstructions = "base-instructions"
	// CategoryAgentsMD holds the merged AGENTS.md block, attributed per file.
	CategoryAgentsMD = "agents-md"
	// CategoryHostSkills holds the injected skills catalogue.
	CategoryHostSkills = "host-skills"
	// CategoryEnvironments holds the workspace and sandbox description.
	CategoryEnvironments = "environments"
	// CategoryDeveloper holds the remaining developer-role instruction blocks.
	CategoryDeveloper = "developer-instructions"
	// CategoryInjected holds recognised user-role blocks that are not AGENTS.md
	// and not the environment description.
	CategoryInjected = "injected-context"
)

// maxReportedWarnings bounds how many parse warnings become caveats, so a
// systematically malformed rollout produces a readable footer rather than
// hundreds of rows.
const maxReportedWarnings = 8

// maxSkillStats bounds how many SKILL.md files are stat'ed to price a skill's
// body. The panel runs from a keypress; a machine with a thousand skills must
// not pay a thousand stats for a column that is informational.
const maxSkillStats = 256

// Adapter reports the context inventory of a Codex CLI session.
//
// It reads only the head of the session rollout and performs no network call,
// launches no process, and never resolves a rollout path itself:
// [ctxinspect.Request] carries the resolved rollout path and the per-instance
// Codex home, because agent-deck's per-session scratch homes make the
// process-wide default frequently wrong.
type Adapter struct {
	// Stat resolves file metadata. Nil uses [os.Stat]. It exists so tests can
	// exercise the potential-cost column without a filesystem.
	Stat func(string) (os.FileInfo, error)
}

// New returns the Codex adapter.
func New() *Adapter { return &Adapter{} }

// Name implements [ctxinspect.Adapter].
func (a *Adapter) Name() string { return "codex" }

// Supports implements [ctxinspect.Adapter]. It routes through the host's
// compatibility predicate so a custom [tools.*] entry whose command is codex
// inherits this adapter instead of falling through to the generic floor.
func (a *Adapter) Supports(tool string, host ctxinspect.Host) bool {
	if strings.TrimSpace(tool) == "" {
		return false
	}
	if host == nil {
		host = ctxinspect.NoHost()
	}
	return host.IsCodexCompatible(tool)
}

// Capabilities implements [ctxinspect.Adapter]. It touches no disk.
//
// The declared category set is Codex's own. There is no "skills" category in
// the Claude sense and no empty one is fabricated, which is the point: the
// taxonomy is adapter-owned, and a renderer that hardcoded Claude's categories
// would have nothing to draw here.
func (a *Adapter) Capabilities() ctxinspect.Capabilities {
	return ctxinspect.Capabilities{
		Adapter:   a.Name(),
		CanAnchor: true,
		// Codex writes its base prompt into the rollout head, so unlike Claude
		// Code the text itself is readable.
		CanVerbatimSystem: true,
		Categories: []ctxinspect.CategoryCapability{
			{
				Name:  CategoryBaseInstructions,
				Title: "base instructions",
				Text:  ctxinspect.TextCaptured,
				Token: ctxinspect.TokenEstimated,
				Note:  "Codex records the base system prompt verbatim in the rollout's session_meta record, so it is readable in full; releases before roughly 0.10x recorded a session instructions string instead and are reported as such",
			},
			{
				Name:  CategoryAgentsMD,
				Title: "AGENTS.md",
				Text:  ctxinspect.TextCaptured,
				Token: ctxinspect.TokenEstimated,
				Note:  "the merged block is captured verbatim and attributed back to individual files on disk by locating each file's bytes inside it; the share that could not be attributed is reported as its own row and as a match rate",
			},
			{
				Name:  CategoryHostSkills,
				Title: "skills catalogue",
				Text:  ctxinspect.TextCaptured,
				Token: ctxinspect.TokenEstimated,
				Note:  "the injected catalogue is split per skill; each skill's SKILL.md body is reported as potential cost, since it loads only when the skill is opened",
			},
			{
				Name:  CategoryEnvironments,
				Title: "environment",
				Text:  ctxinspect.TextCaptured,
				Token: ctxinspect.TokenEstimated,
				Note:  "the workspace, sandbox and approval description Codex injects for the session",
			},
			{
				Name:  CategoryDeveloper,
				Title: "developer instructions",
				Text:  ctxinspect.TextCaptured,
				Token: ctxinspect.TokenEstimated,
				Note:  "permissions, collaboration mode, apps, plugins and multi-agent blocks, one row per block as Codex injected it",
			},
			{
				Name:  CategoryInjected,
				Title: "other injected blocks",
				Text:  ctxinspect.TextCaptured,
				Token: ctxinspect.TokenEstimated,
				Note:  "recognised blocks this build has no dedicated category for; they are still priced from their verbatim text",
			},
		},
		Notes: []string{
			"Tool schemas are negotiated over the wire and are never written to the rollout. They are the bulk of the unattributed remainder, which is measured by subtraction from the provider-reported total rather than guessed.",
			"MCP servers are not given their own category: their instruction text is injected inside the blocks above and their schemas are inside the remainder, so a separate row would either double count or be an unknown that destroys the remainder for every report.",
			"Per-item token figures are estimates; the first-turn total is provider-measured, so the remainder is obtained by subtraction.",
		},
	}
}

// Inspect implements [ctxinspect.Adapter].
func (a *Adapter) Inspect(ctx context.Context, req ctxinspect.Request) (*ctxinspect.Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	est := req.EstimatorOrDefault()

	var head *Head
	if path := strings.TrimSpace(req.TranscriptPath); path != "" {
		parsed, err := ParseHead(path)
		if err != nil {
			return nil, err
		}
		head = parsed
	}

	rep := &ctxinspect.Report{
		Harness:     req.Tool,
		SessionID:   sessionID(req, head),
		ProjectPath: req.ProjectPath,
		Model:       modelOf(req, head),
		Basis:       ctxinspect.BasisProjected,
		Window:      resolveWindow(head),
		GeneratedAt: req.Timestamp(),
	}
	if head != nil {
		rep.Basis = ctxinspect.BasisObserved
	}
	a.applyAnchor(rep, head)

	if head == nil {
		if err := a.appendProjected(ctx, rep, req, est); err != nil {
			return nil, err
		}
		a.applyReportCaveats(rep, head)
		return rep, nil
	}

	claimed := make([]bool, len(head.Prefix))
	appendCategory := func(cat *ctxinspect.Category, err error) error {
		if err != nil {
			return err
		}
		if cat != nil {
			rep.Categories = append(rep.Categories, *cat)
		}
		return ctx.Err()
	}

	if err := appendCategory(a.baseInstructionsCategory(head, est, rep), nil); err != nil {
		return nil, err
	}
	if err := appendCategory(a.agentsMDCategory(req, head, est, claimed, rep)); err != nil {
		return nil, err
	}
	if err := appendCategory(a.hostSkillsCategory(head, est, claimed, rep), nil); err != nil {
		return nil, err
	}
	if err := appendCategory(a.environmentsCategory(req, head, est, claimed), nil); err != nil {
		return nil, err
	}
	if err := appendCategory(a.developerCategory(head, est, claimed), nil); err != nil {
		return nil, err
	}
	if err := appendCategory(a.injectedCategory(head, est, claimed), nil); err != nil {
		return nil, err
	}

	a.applyReportCaveats(rep, head)
	return rep, nil
}

// sessionID prefers the identifier the rollout recorded, which is the one the
// harness itself uses.
func sessionID(req ctxinspect.Request, head *Head) string {
	if head != nil && head.SessionID != "" {
		return head.SessionID
	}
	return req.SessionID
}

// modelOf prefers the model that actually served the first turn over whatever
// the caller believed was configured.
func modelOf(req ctxinspect.Request, head *Head) string {
	if head != nil && head.Turn != nil && head.Turn.Model != "" {
		return head.Turn.Model
	}
	return req.Model
}

// resolveWindow reads the context window from the harness's own accounting.
//
// There is no model-name table here on purpose. Codex reports the window it
// actually used, so inferring one from an identifier would replace a measured
// fact with a guess; when the record carries none the window is unknown and no
// percentage is shown.
func resolveWindow(head *Head) ctxinspect.WindowInfo {
	if head != nil && head.FirstTurn != nil && head.FirstTurn.ContextWindow > 0 {
		return ctxinspect.WindowInfo{
			Tokens: head.FirstTurn.ContextWindow,
			Source: ctxinspect.WindowHarnessReported,
			Detail: "event_msg token_count: info.model_context_window",
		}
	}
	return ctxinspect.WindowInfo{
		Source: ctxinspect.WindowUnknown,
		Detail: "the rollout carries no model_context_window figure for this session",
	}
}

// applyAnchor attaches the provider-measured first-turn total, or explains why
// there is none.
func (a *Adapter) applyAnchor(rep *ctxinspect.Report, head *Head) {
	switch {
	case head == nil:
		rep.AddCaveat("no-rollout",
			"no Codex rollout was resolved for this session, so nothing could be observed: what follows is what a fresh session would load, and there is no measured total to reconcile against.",
			ctxinspect.SeverityWarn)
		return
	case head.FirstTurn == nil:
		rep.AddCaveat("anchor-unavailable-no-turn",
			"this session has no completed model turn with usage accounting yet, so there is no provider-measured total and the unattributed remainder is unknown rather than zero.",
			ctxinspect.SeverityWarn)
		return
	case len(head.ResumeSignals) > 0:
		rep.AddCaveat("anchor-unavailable-resumed",
			"the first recorded turn does not measure a cold startup prefix ("+strings.Join(head.ResumeSignals, "; ")+"), so no measured total is claimed and the unattributed remainder is reported as unknown.",
			ctxinspect.SeverityWarn)
		return
	}

	turn := head.FirstTurn
	rep.Anchor = &ctxinspect.Anchor{
		Tokens: ctxinspect.Measured(turn.InputTokens, turn.Source),
		Source: fmt.Sprintf("first accounted turn: %s (%d cached, %d written to cache)",
			turn.Source, turn.CachedInput, turn.CacheWrite),
		At: turn.At,
	}
	if turn.CachedInput > 0 {
		rep.AddCaveat("anchor-warm-cache",
			fmt.Sprintf("%d of the measured %d prompt tokens were served from the provider's cache. Caching is normal on a cold start and does not change the prompt's size.",
				turn.CachedInput, turn.InputTokens),
			ctxinspect.SeverityInfo)
	}
}

// baseInstructionsCategory reports the base system prompt.
//
// This is the differentiator against the Claude Code adapter: the text is here,
// verbatim, and can be read at the detail level rather than inferred from a
// residual.
func (a *Adapter) baseInstructionsCategory(head *Head, est ctxinspect.Estimator, rep *ctxinspect.Report) *ctxinspect.Category {
	cat := &ctxinspect.Category{
		Name:        CategoryBaseInstructions,
		Title:       "base instructions",
		Description: "the base system prompt Codex sends before anything else, exactly as it recorded it",
	}

	switch {
	case head.BaseInstructions != "":
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:      "base-instructions",
			Label:   "Codex base system prompt",
			Detail:  fmt.Sprintf("%d characters, recorded verbatim by the harness", len([]rune(head.BaseInstructions))),
			Content: ctxinspect.CapturedContent(ctxinspect.KindMarkdown, head.BaseInstructions),
			Load:    ctxinspect.LoadedNowCost(est.Estimate(ctxinspect.KindMarkdown, head.BaseInstructions)),
			Origin:  ctxinspect.OriginHarnessBuiltin,
			Lever:   ctxinspect.ImmovableLever("shipped inside the Codex binary; not configurable"),
		})
		return cat

	case head.LegacyInstructions != "":
		if containsAnyPart(head, head.LegacyInstructions) {
			// Older releases wrote the same text both as a session_meta field
			// and as a prefix block. Pricing both would double count it.
			rep.AddCaveat("legacy-instructions-duplicated",
				"this Codex release records its session instructions both in session_meta and as an injected block; the block is priced and the duplicate field is not, so the text is counted once.",
				ctxinspect.SeverityInfo)
			return nil
		}
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:      "base-instructions:legacy",
			Label:   "session instructions",
			Detail:  fmt.Sprintf("%d characters, from this release's session_meta.instructions field", len([]rune(head.LegacyInstructions))),
			Content: ctxinspect.CapturedContent(ctxinspect.KindMarkdown, head.LegacyInstructions),
			Load:    ctxinspect.LoadedNowCost(est.Estimate(ctxinspect.KindMarkdown, head.LegacyInstructions)),
			Origin:  ctxinspect.OriginHarnessBuiltin,
			Lever:   ctxinspect.ImmovableLever("assembled by this Codex release; not editable as one file"),
		})
		cat.Notes = append(cat.Notes,
			"this Codex release records a session instructions string rather than a base prompt, so the base prompt itself is not on disk and sits inside the unattributed remainder")
		return cat
	}

	rep.AddCaveat("base-instructions-absent",
		"this rollout carries no base system prompt, so it is not listed as a category; its cost is inside the unattributed remainder rather than reported as zero.",
		ctxinspect.SeverityWarn)
	return nil
}

// agentsMDCategory reports the merged AGENTS.md block, attributed per file.
//
// The merged block is what Codex actually injected, so it is the quantity that
// must add up. Individual files are located inside it by their own bytes; what
// no file explains is reported as its own row and as a match rate, because a
// silently absorbed remainder is how per-file attribution comes to lie.
func (a *Adapter) agentsMDCategory(req ctxinspect.Request, head *Head, est ctxinspect.Estimator, claimed []bool, rep *ctxinspect.Report) (*ctxinspect.Category, error) {
	parts := indicesWithTag(head, agentsMDTag)
	blocks := make([]string, 0, len(parts))
	for _, i := range parts {
		claimed[i] = true
		blocks = append(blocks, head.Prefix[i].Text)
	}
	fromWorldState := false
	if len(blocks) == 0 {
		if head.World == nil || head.World.AgentsMD == "" {
			return nil, nil
		}
		// No prefix block was recorded, so the world-state snapshot is the only
		// source and cannot be double counted against one.
		blocks = append(blocks, head.World.AgentsMD)
		fromWorldState = true
	}

	files, err := ctxinspect.DiscoverInstructionFiles(ctxinspect.InstructionWalkOptions{
		Spec:        ctxinspect.InstructionSpecFor(req.Tool, req.HostOrNone()),
		ProjectPath: req.ProjectPath,
		UserDirs:    userDirs(req.ConfigDir),
		ReadContent: true,
	})
	if err != nil {
		return nil, fmt.Errorf("discovering the AGENTS.md hierarchy: %w", err)
	}

	cat := &ctxinspect.Category{
		Name:        CategoryAgentsMD,
		Title:       "AGENTS.md",
		Description: "the merged instruction block Codex injected, attributed back to the files it was built from",
		Notes: []string{
			"each file is located inside the merged block by its own bytes, so a file that is on disk but not in the block is shown as costing nothing rather than assumed to be loaded",
		},
	}
	if fromWorldState {
		cat.Notes = append(cat.Notes,
			"this rollout records no injected AGENTS.md block, so the figures come from the session's world-state snapshot and exclude whatever wrapper the harness added around it")
	}

	matchedAny := make(map[string]bool, len(files))
	// blockHasResidue records that some of the injected block was explained by
	// no file on disk. While that is true, a file that did not match cannot be
	// declared absent — the unexplained text may be its older bytes.
	blockHasResidue := false
	for blockIdx, block := range blocks {
		spans := newSpanSet(block)
		suffix := ""
		if len(blocks) > 1 {
			suffix = fmt.Sprintf(":%d", blockIdx)
		}

		for _, f := range files {
			needle := strings.TrimSpace(f.Content)
			if needle == "" {
				continue
			}
			span, ok := spans.Claim(needle)
			if !ok {
				continue
			}
			matchedAny[f.Path] = true
			text := spans.Text(span)
			cat.Items = append(cat.Items, ctxinspect.Item{
				ID:      "agents-md:" + f.Path + suffix,
				Label:   f.Path,
				Detail:  fmt.Sprintf("%s · %s · merged into the injected block", f.Scope, humanBytes(f.Size)),
				Content: ctxinspect.CapturedContent(ctxinspect.KindMarkdown, text),
				Load:    ctxinspect.LoadedNowCost(est.EstimateChars(ctxinspect.KindMarkdown, span.Chars())),
				Origin:  originForScope(f.Scope),
				Lever:   ctxinspect.FileLever(f.Path, fmt.Sprintf("%s layer of the AGENTS.md chain", f.Scope)),
			})
			if f.Truncated {
				rep.AddCaveat("agents-md-file-truncated",
					fmt.Sprintf("%s is larger than the per-file read cap, so only its first bytes were matched against the merged block; the rest is inside the unattributed row.", f.Path),
					ctxinspect.SeverityWarn)
			}
		}

		residue := spans.Residue()
		if residue > 0 {
			cat.Items = append(cat.Items, ctxinspect.Item{
				ID:     "agents-md:unattributed" + suffix,
				Label:  "injection wrapper and unattributed text",
				Detail: fmt.Sprintf("%d characters of the merged block that no file on disk accounts for", residue),
				Content: ctxinspect.ReconstructedContent(ctxinspect.KindMarkdown,
					joinUnclaimed(spans),
					"the parts of the injected block that no discovered file explained, concatenated in injection order"),
				Load:   ctxinspect.LoadedNowCost(est.EstimateChars(ctxinspect.KindMarkdown, residue)),
				Origin: ctxinspect.OriginHarnessBuiltin,
				Lever:  ctxinspect.ImmovableLever("headers and separators Codex adds when it merges the chain"),
			})
		}
		if spans.Residue() > 0 {
			blockHasResidue = true
		}
		cat.Notes = append(cat.Notes, fmt.Sprintf(
			"%d of %d characters of the merged block were attributed to a file on disk (%s)",
			spans.Claimed(), spans.Chars(), pct(spans.MatchRate())))
	}

	for _, f := range files {
		if matchedAny[f.Path] {
			continue
		}
		// "Not found in the block" is evidence of absence only when the block is
		// fully explained by the files that did match. While unattributed text
		// remains, this file may well be in there under bytes that have since
		// changed on disk — and a file we could not even read was never given
		// the chance to match at all. Both cases used to price at a measured,
		// summable zero, which is the report asserting "this costs you nothing"
		// about content it never looked at.
		unreadable := f.ReadErr != ""
		certainlyAbsent := !blockHasResidue && !unreadable

		detail := "on disk, not present in the injected block"
		absence := "this file's bytes were not found in the block Codex injected"
		load := ctxinspect.AvailableCost(potentialFromSize(est, f.Size))
		var caveats []ctxinspect.Caveat

		switch {
		case unreadable:
			detail = "on disk but unreadable (" + f.ReadErr + "), so it could not be matched"
			absence = "this file could not be read, so whether its text is in the injected block was never tested"
			load = ctxinspect.AvailableUnknownCost(
				"the file could not be read, so it could not be matched against the injected block and its cost is not knowable",
				potentialFromSize(est, f.Size))
			caveats = append(caveats, ctxinspect.Caveat{
				Code:     "agents-md-file-unreadable",
				Message:  fmt.Sprintf("%s is on the AGENTS.md search path and could not be read (%s). It is priced as unknown rather than as zero: an unread file is not a file known to be absent.", f.Path, f.ReadErr),
				Severity: ctxinspect.SeverityWarn,
				Category: CategoryAgentsMD,
			})
		case !certainlyAbsent:
			detail = "on disk, not matched byte-for-byte in the injected block"
			absence = "this file's current bytes are not in the injected block; part of that block is still unattributed, so it may be in there in an older form"
			load = ctxinspect.AvailableUnknownCost(
				"part of the injected block is still unattributed, so whether this file contributed to it under bytes that have since changed is not knowable",
				potentialFromSize(est, f.Size))
			// Per file, not per block: a 90%-of-the-whole-block threshold says
			// nothing about which file the missing 10% belongs to, and stays
			// silent at 91% even when this particular file is the gap.
			caveats = append(caveats, ctxinspect.Caveat{
				Code:     "agents-md-file-unmatched",
				Message:  fmt.Sprintf("%s did not match the injected block byte-for-byte, and the block still has unattributed text. Attribution is by exact bytes, so a file edited since this session started no longer matches what was injected. Whatever of it is in the block is priced in the unattributed row.", f.Path),
				Severity: ctxinspect.SeverityWarn,
				Category: CategoryAgentsMD,
			})
			rep.AddCaveat("agents-md-file-unmatched",
				fmt.Sprintf("%s did not match the injected block byte-for-byte, and the block still has unattributed text. Attribution is by exact bytes, so the file may have changed since this session started.", f.Path),
				ctxinspect.SeverityWarn)
		}

		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:      "agents-md:absent:" + f.Path,
			Label:   f.Path,
			Detail:  fmt.Sprintf("%s · %s · %s", f.Scope, humanBytes(f.Size), detail),
			Content: ctxinspect.AbsentContent(absence),
			Load:    load,
			Origin:  originForScope(f.Scope),
			Lever:   ctxinspect.FileLever(f.Path, "on the AGENTS.md search path but not part of this session's injected block"),
			// Its cost, if it has one, is already inside the unattributed row.
			Informational: !certainlyAbsent && !unreadable,
			Caveats:       caveats,
		})
	}

	if head.World != nil && head.World.AgentsMD != "" && !fromWorldState {
		if !containsAnyPart(head, head.World.AgentsMD) {
			rep.AddCaveat("agents-md-world-state-unlocated",
				fmt.Sprintf("the session's world state records %d characters of merged AGENTS.md text that could not be located inside any injected block. Its cost is inside another row's unattributed share rather than counted twice.",
					len([]rune(head.World.AgentsMD))),
				ctxinspect.SeverityWarn)
		}
	}
	return cat, nil
}

// hostSkillsCategory reports the injected skills catalogue, split per skill.
//
// A catalogue entry is what a skill costs today; its SKILL.md body is what it
// would cost if the agent opened it. Reporting only the second would tell the
// user to delete the wrong things.
func (a *Adapter) hostSkillsCategory(head *Head, est ctxinspect.Estimator, claimed []bool, rep *ctxinspect.Report) *ctxinspect.Category {
	parts := indicesWithTag(head, skillsInstructionTag)
	block := ""
	fromWorldState := false
	switch {
	case len(parts) > 0:
		for _, i := range parts {
			claimed[i] = true
			block += head.Prefix[i].Text
		}
	case head.World != nil && head.World.HostSkills != "":
		block = head.World.HostSkills
		fromWorldState = true
	default:
		return nil
	}

	cat := &ctxinspect.Category{
		Name:        CategoryHostSkills,
		Title:       "skills catalogue",
		Description: "the skill catalogue Codex injects: one name, description and path per skill",
		Notes: []string{
			"a skill costs its catalogue entry in the prefix; its SKILL.md body is the potential cost and loads only when the agent opens it",
		},
	}
	if fromWorldState {
		cat.Notes = append(cat.Notes,
			"this rollout records no injected skills block, so the figures come from the session's world-state snapshot")
	}

	catalogue := ParseSkillCatalogue(block)
	if len(catalogue.Entries) == 0 {
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:      "host-skills:block",
			Label:   "skills catalogue (unsplit)",
			Detail:  fmt.Sprintf("%d characters; no catalogue entries could be recognised", len([]rune(block))),
			Content: ctxinspect.CapturedContent(ctxinspect.KindListing, block),
			Load:    ctxinspect.LoadedNowCost(est.Estimate(ctxinspect.KindListing, block)),
			Origin:  ctxinspect.OriginUserConfig,
			Lever:   ctxinspect.ImmovableLever("the catalogue is generated; trim it by removing individual skills"),
		})
		rep.AddCaveat("host-skills-unsplittable",
			"the injected skills catalogue is in a shape this build cannot split per skill, so it is reported as one block and no per-skill cost is claimed.",
			ctxinspect.SeverityWarn)
		return cat
	}

	stats := 0
	for _, e := range catalogue.Entries {
		load := ctxinspect.Load{
			State:  ctxinspect.LoadedNow,
			Actual: est.EstimateChars(ctxinspect.KindListing, e.Chars),
		}
		lever := ctxinspect.ImmovableLever("agent-deck could not resolve this skill's directory from the catalogue path")
		detail := "listed at startup"
		if strings.HasPrefix(e.Path, "/") {
			lever = ctxinspect.DirLever(e.Dir(), "remove the skill directory to drop it from every future catalogue")
			detail = "listed at startup · " + e.Dir()
			if stats < maxSkillStats {
				stats++
				if info, err := a.stat(e.Path); err == nil && !info.IsDir() {
					if p := potentialFromSize(est, info.Size()); p.Known() {
						pv, _ := p.Value()
						if av, ok := load.Actual.Value(); !ok || pv != av {
							potential := p
							load.Potential = &potential
						}
					}
					detail += " · body " + humanBytes(info.Size())
				}
			}
		}
		if e.Alias != "" {
			detail += " · path expanded from root " + e.Alias
		}
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:      "host-skill:" + e.Name,
			Label:   e.Name,
			Detail:  detail,
			Content: ctxinspect.CapturedContent(ctxinspect.KindListing, e.Line),
			Load:    load,
			Origin:  originForSkillPath(e.Path),
			Lever:   lever,
		})
	}

	overhead := len([]rune(block)) - catalogue.EntryChars()
	if overhead > 0 {
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:      "host-skills:protocol",
			Label:   "skills protocol and root table",
			Detail:  fmt.Sprintf("%d characters of explanation and path aliases, injected whether or not a skill is used", overhead),
			Content: ctxinspect.AbsentContent("the catalogue text minus its per-skill entries; the entries are listed individually above"),
			Load:    ctxinspect.LoadedNowCost(est.EstimateChars(ctxinspect.KindMarkdown, overhead)),
			Origin:  ctxinspect.OriginHarnessBuiltin,
			Lever:   ctxinspect.ImmovableLever("generated by Codex around the catalogue"),
		})
	}
	cat.Notes = append(cat.Notes, fmt.Sprintf("%d skills listed, from %d root directories", len(catalogue.Entries), len(catalogue.Roots)))
	return cat
}

// environmentsCategory reports the workspace and sandbox description.
func (a *Adapter) environmentsCategory(req ctxinspect.Request, head *Head, est ctxinspect.Estimator, claimed []bool) *ctxinspect.Category {
	parts := indicesWithTag(head, environmentTag)
	cat := &ctxinspect.Category{
		Name:        CategoryEnvironments,
		Title:       "environment",
		Description: "the workspace roots, sandbox policy and approval policy Codex describes to the model",
	}

	if len(parts) > 0 {
		// The block is generated by Codex, but what it describes — the sandbox
		// and approval policy — is the user's configuration. When that file is
		// on disk the item is user-config with a real lever; when it is not,
		// there is nothing to point at and the item says so instead of
		// offering a path that does not exist.
		lever := ctxinspect.ImmovableLever("derived from the session's working directory and the sandbox policy it was launched with")
		origin := ctxinspect.OriginHarnessBuiltin
		if path := codexConfigPath(req.ConfigDir); path != "" {
			lever = ctxinspect.FileLever(path, "approval and sandbox policy are configured here")
			origin = ctxinspect.OriginUserConfig
		}
		for _, i := range parts {
			claimed[i] = true
			p := head.Prefix[i]
			cat.Items = append(cat.Items, ctxinspect.Item{
				ID:      partID("environment", p),
				Label:   "environment context",
				Detail:  fmt.Sprintf("%d characters", p.Chars()),
				Content: ctxinspect.CapturedContent(ctxinspect.KindListing, p.Text),
				Load:    ctxinspect.LoadedNowCost(est.Estimate(ctxinspect.KindListing, p.Text)),
				Origin:  origin,
				Lever:   lever,
			})
		}
		return cat
	}

	if head.World == nil || head.World.Environments == "" || len(head.Prefix) > 0 {
		// Either nothing was recorded, or a prefix exists and the snapshot's
		// text is inside one of its blocks; pricing it here would double count.
		return nil
	}
	cat.Items = append(cat.Items, ctxinspect.Item{
		ID:      "environment:world-state",
		Label:   "environment snapshot",
		Detail:  "from the session's world state; no injected environment block was recorded",
		Content: ctxinspect.CapturedContent(ctxinspect.KindJSON, head.World.Environments),
		Load:    ctxinspect.LoadedNowCost(est.Estimate(ctxinspect.KindJSON, head.World.Environments)),
		Origin:  ctxinspect.OriginHarnessBuiltin,
		Lever:   ctxinspect.ImmovableLever("derived from the session's working directory and sandbox policy"),
	})
	return cat
}

// developerCategory reports the remaining developer-role blocks.
func (a *Adapter) developerCategory(head *Head, est ctxinspect.Estimator, claimed []bool) *ctxinspect.Category {
	cat := &ctxinspect.Category{
		Name:        CategoryDeveloper,
		Title:       "developer instructions",
		Description: "the instruction blocks Codex injects as developer messages, one row per block",
	}
	for i, p := range head.Prefix {
		if claimed[i] || p.Role != roleDeveloper {
			continue
		}
		claimed[i] = true
		cat.Items = append(cat.Items, a.blockItem(p, est, head))
	}
	if len(cat.Items) == 0 {
		return nil
	}
	return cat
}

// injectedCategory reports recognised user-role blocks with no dedicated home.
func (a *Adapter) injectedCategory(head *Head, est ctxinspect.Estimator, claimed []bool) *ctxinspect.Category {
	cat := &ctxinspect.Category{
		Name:        CategoryInjected,
		Title:       "other injected blocks",
		Description: "recognised blocks Codex injected that this build has no dedicated category for",
	}
	for i, p := range head.Prefix {
		if claimed[i] {
			continue
		}
		claimed[i] = true
		cat.Items = append(cat.Items, a.blockItem(p, est, head))
	}
	if len(cat.Items) == 0 {
		return nil
	}
	return cat
}

// blockItem prices one injected block from its verbatim text.
func (a *Adapter) blockItem(p ContentPart, est ctxinspect.Estimator, head *Head) ctxinspect.Item {
	label, origin, why := describeBlock(p, head)
	return ctxinspect.Item{
		ID:      partID("block", p),
		Label:   label,
		Detail:  fmt.Sprintf("%s message · %d characters", p.Role, p.Chars()),
		Content: ctxinspect.CapturedContent(ctxinspect.KindMarkdown, p.Text),
		Load:    ctxinspect.LoadedNowCost(est.Estimate(ctxinspect.KindMarkdown, p.Text)),
		Origin:  origin,
		Lever:   ctxinspect.ImmovableLever(why),
	}
}

// describeBlock names a block and says who put it there.
//
// Every block here is immovable in this version: Codex assembles them from
// runtime state rather than from a file agent-deck can point at, and offering a
// lever that does not exist would be worse than admitting there is none. The
// origin still distinguishes what the harness ships from what an enabled plugin
// or connector added, which is the part the user can act on elsewhere.
func describeBlock(p ContentPart, head *Head) (label string, origin ctxinspect.Origin, why string) {
	switch p.Tag {
	case "permissions":
		return "permissions and sandbox instructions", ctxinspect.OriginHarnessBuiltin,
			"generated from the approval and sandbox policy this session was launched with"
	case "collaboration_mode":
		mode := ""
		if head.Turn != nil {
			mode = head.Turn.CollaborationMode
		}
		if mode != "" {
			return "collaboration mode: " + mode, ctxinspect.OriginHarnessBuiltin,
				"injected by the active collaboration mode " + mode
		}
		return "collaboration mode", ctxinspect.OriginHarnessBuiltin, "injected by the active collaboration mode"
	case "apps_instructions":
		return "apps and connectors protocol", ctxinspect.OriginPlugin,
			"injected because apps or connectors are enabled for this Codex profile"
	case "plugins_instructions":
		return "plugins protocol", ctxinspect.OriginPlugin,
			"injected because plugins are installed for this Codex profile"
	case "recommended_plugins":
		return "recommended plugins", ctxinspect.OriginPlugin,
			"suggested by Codex from the installed plugin set"
	case "multi_agent_mode":
		return "multi-agent mode", ctxinspect.OriginHarnessBuiltin,
			"injected by the active multi-agent configuration"
	case "":
		return firstLineLabel(p.Text), ctxinspect.OriginHarnessBuiltin,
			"an untagged block Codex injected before the first turn"
	default:
		return strings.ReplaceAll(p.Tag, "_", " "), ctxinspect.OriginHarnessBuiltin,
			"injected by Codex as the " + p.Tag + " block"
	}
}

// appendProjected fills a report for a session with no rollout on disk.
//
// Only the AGENTS.md chain is projected. Everything else Codex injects —
// the base prompt, the skills catalogue, the permissions block — is assembled at
// launch from state that is not on disk, and inventing it would be a
// projection of something this build has never observed.
func (a *Adapter) appendProjected(ctx context.Context, rep *ctxinspect.Report, req ctxinspect.Request, est ctxinspect.Estimator) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	files, err := ctxinspect.DiscoverInstructionFiles(ctxinspect.InstructionWalkOptions{
		Spec:        ctxinspect.InstructionSpecFor(req.Tool, req.HostOrNone()),
		ProjectPath: req.ProjectPath,
		UserDirs:    userDirs(req.ConfigDir),
		ReadContent: true,
	})
	if err != nil {
		return fmt.Errorf("discovering the AGENTS.md hierarchy: %w", err)
	}
	if len(files) == 0 {
		return nil
	}

	cat := ctxinspect.Category{
		Name:        CategoryAgentsMD,
		Title:       "AGENTS.md",
		Description: "the AGENTS.md files a fresh session in this directory would merge",
		Notes: []string{
			"no session was observed, so these are the files on the search path rather than the block Codex actually injected; the merge wrapper Codex adds is not included",
		},
	}
	for _, f := range files {
		cost := est.EstimateChars(ctxinspect.KindMarkdown, f.Chars)
		if f.ReadErr != "" {
			cost = est.EstimateChars(ctxinspect.KindMarkdown, int(f.Size))
		}
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:      "agents-md:" + f.Path,
			Label:   f.Path,
			Detail:  fmt.Sprintf("%s · %s · projected", f.Scope, humanBytes(f.Size)),
			Content: ctxinspect.ReconstructedContent(ctxinspect.KindMarkdown, f.Content, "read from disk; no session recorded what Codex actually merged"),
			Load:    ctxinspect.LoadedNowCost(cost),
			Origin:  originForScope(f.Scope),
			Lever:   ctxinspect.FileLever(f.Path, fmt.Sprintf("%s layer of the AGENTS.md chain", f.Scope)),
		})
	}
	rep.Categories = append(rep.Categories, cat)
	return nil
}

// applyReportCaveats records everything about the report that a number alone
// cannot say.
func (a *Adapter) applyReportCaveats(rep *ctxinspect.Report, head *Head) {
	if !rep.Window.Known() {
		rep.AddCaveat("window-unknown",
			"the context-window size could not be established ("+rep.Window.Detail+"), so no percentage is shown. A wrong denominator would misrepresent an otherwise honest total.",
			ctxinspect.SeverityWarn)
	}
	if head == nil {
		return
	}

	if len(head.Excluded) > 0 {
		chars := 0
		for _, p := range head.Excluded {
			chars += p.Chars()
		}
		rep.AddCaveat("residual-includes-turn-inputs",
			fmt.Sprintf("the measured total covers the whole first request, so the unattributed remainder also contains this session's first typed message (%d characters across %d block(s)) alongside the tool schemas.",
				chars, len(head.Excluded)),
			ctxinspect.SeverityInfo)
	}
	if head.World != nil {
		rep.AddCaveat("world-state-keys",
			fmt.Sprintf("Codex %s recorded these world-state keys: %s. The record format moves between releases; a key this build does not read contributes to the unattributed remainder rather than to a category.",
				displayVersion(head.CLIVersion), describeKeys(head.World.Keys)),
			ctxinspect.SeverityInfo)
		if head.World.PermissionsDigest != "" {
			rep.AddCaveat("permissions-digest-only",
				"the world state records the permissions block as a digest rather than as text, so it is not priced from that record; the injected permissions block itself is listed under developer instructions.",
				ctxinspect.SeverityInfo)
		}
		if head.World.HostSkills != "" && !containsAnyPart(head, head.World.HostSkills) && len(head.Prefix) > 0 {
			rep.AddCaveat("host-skills-world-state-unlocated",
				fmt.Sprintf("the session's world state records %d characters of skills catalogue that could not be located inside any injected block. Its cost is inside another row rather than counted twice.",
					len([]rune(head.World.HostSkills))),
				ctxinspect.SeverityWarn)
		}
	}
	if !head.ReachedTurn {
		rep.AddCaveat("prefix-boundary-not-reached",
			"the parser did not reach a model turn within its record budget, so parts of the injected prefix may be missing rather than absent.",
			ctxinspect.SeverityWarn)
	}

	warnings := head.Warnings
	if len(warnings) > maxReportedWarnings {
		rep.AddCaveat("rollout-warnings-truncated",
			fmt.Sprintf("%d rollout records could not be read; the first %d are listed below.", len(warnings), maxReportedWarnings),
			ctxinspect.SeverityWarn)
		warnings = warnings[:maxReportedWarnings]
	}
	for _, w := range warnings {
		rep.AddCaveat("rollout-record-unreadable", w, ctxinspect.SeverityWarn)
	}
}

// stat resolves file metadata through the adapter's hook.
func (a *Adapter) stat(path string) (os.FileInfo, error) {
	if a.Stat != nil {
		return a.Stat(path)
	}
	return os.Stat(path)
}

// indicesWithTag returns the prefix positions carrying a tag.
func indicesWithTag(head *Head, tag string) []int {
	var out []int
	for i, p := range head.Prefix {
		if p.Tag == tag {
			out = append(out, i)
		}
	}
	return out
}

// containsAnyPart reports whether a world-state blob is present inside an
// injected block, which is how double counting is detected.
func containsAnyPart(head *Head, needle string) bool {
	if strings.TrimSpace(needle) == "" {
		return false
	}
	for _, p := range head.Prefix {
		if strings.Contains(p.Text, needle) {
			return true
		}
	}
	for _, p := range head.Excluded {
		if strings.Contains(p.Text, needle) {
			return true
		}
	}
	return false
}

// joinUnclaimed renders the parts of a block no source explained, in order.
func joinUnclaimed(spans *spanSet) string {
	var b strings.Builder
	cursor := 0
	claims := append([]Span(nil), spans.claims...)
	for i := 1; i < len(claims); i++ {
		for j := i; j > 0 && claims[j].Start < claims[j-1].Start; j-- {
			claims[j], claims[j-1] = claims[j-1], claims[j]
		}
	}
	for _, c := range claims {
		if c.Start > cursor {
			b.WriteString(string(spans.runes[cursor:c.Start]))
		}
		if c.End > cursor {
			cursor = c.End
		}
	}
	if cursor < len(spans.runes) {
		b.WriteString(string(spans.runes[cursor:]))
	}
	return b.String()
}

// partID builds a stable identifier for an injected block.
func partID(prefix string, p ContentPart) string {
	tag := p.Tag
	if tag == "" {
		tag = "untagged"
	}
	return fmt.Sprintf("%s:%s:%d.%d", prefix, tag, p.Message, p.Element)
}

// firstLineLabel names an untagged block by its opening line, bounded so a row
// stays readable.
func firstLineLabel(text string) string {
	line := strings.TrimSpace(text)
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	runes := []rune(strings.TrimSpace(line))
	if len(runes) > 64 {
		return string(runes[:61]) + "..."
	}
	if len(runes) == 0 {
		return "untagged block"
	}
	return string(runes)
}

// userDirs returns the config directories searched for a user-scope AGENTS.md.
func userDirs(configDir string) []string {
	if strings.TrimSpace(configDir) == "" {
		return nil
	}
	return []string{configDir}
}

// codexConfigPath returns the session's Codex config file when it exists. A
// lever must point at something real, so a missing file yields no lever.
func codexConfigPath(configDir string) string {
	if strings.TrimSpace(configDir) == "" {
		return ""
	}
	path := filepath.Join(configDir, "config.toml")
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return ""
	}
	return path
}

// originForScope maps an instruction file's layer onto an origin.
func originForScope(scope ctxinspect.InstructionScope) ctxinspect.Origin {
	if scope == ctxinspect.ScopeUser {
		return ctxinspect.OriginUserConfig
	}
	return ctxinspect.OriginProject
}

// originForSkillPath distinguishes a skill a plugin installed from one the user
// wrote, which is the difference between uninstalling and deleting.
func originForSkillPath(path string) ctxinspect.Origin {
	if strings.Contains(path, "/plugins/") {
		return ctxinspect.OriginPlugin
	}
	return ctxinspect.OriginUserConfig
}

// potentialFromSize prices a file's body as the cost it would add if it were
// fully loaded.
//
// It returns an unknown rather than a zero for an empty file: a potential of
// zero is indistinguishable from "costs nothing", which is a claim, and
// [ctxinspect.Report.Validate] rejects a potential equal to the actual cost.
func potentialFromSize(est ctxinspect.Estimator, size int64) ctxinspect.TokenCount {
	if size <= 0 {
		return ctxinspect.UnknownTokens("the file is empty, so there is no body to load")
	}
	if size > int64(^uint(0)>>1) {
		return ctxinspect.UnknownTokens("the file is larger than this platform can measure in one count")
	}
	tc := est.EstimateChars(ctxinspect.KindMarkdown, int(size))
	if v, ok := tc.Value(); ok && v == 0 {
		return ctxinspect.UnknownTokens("the file is too small to register as a whole token")
	}
	return tc
}

// displayVersion renders a CLI version for a caveat, naming the absence rather
// than printing an empty string.
func displayVersion(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(version not recorded)"
	}
	return v
}

// humanBytes renders a byte count compactly for a detail line.
func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// ensure the adapter satisfies the registry contract at compile time.
var _ ctxinspect.Adapter = (*Adapter)(nil)
