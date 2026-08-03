package claude

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

// Category names this adapter declares, in the order it emits them. They are
// exported so the CLI and the TUI can address a category without a string
// literal.
const (
	// CategorySystemTools holds tools whose names are in the prefix while their
	// schemas load on demand.
	CategorySystemTools = "system-tools"
	// CategoryMCP holds the instruction blocks MCP servers contributed.
	CategoryMCP = ctxinspect.CategoryMCP
	// CategoryMemory holds the reconstructed CLAUDE.md hierarchy.
	CategoryMemory = "memory-files"
	// CategorySkills holds the startup skill catalogue and the skills on disk
	// that did not load.
	CategorySkills = "skills"
	// CategoryAgents holds the startup agent catalogue.
	CategoryAgents = "agents"
	// CategorySystemPrompt is declared in [Adapter.Capabilities] but never
	// emitted as a category: Claude Code writes neither the base system prompt
	// nor its built-in tool schemas to disk, so they are reported as the
	// reconciliation residual — one row, measured by subtraction from the
	// provider total, with its text honestly absent.
	CategorySystemPrompt = "system-prompt"
)

// maxReportedWarnings bounds how many parse warnings become caveats, so a
// systematically malformed transcript produces a readable footer rather than
// hundreds of rows.
const maxReportedWarnings = 8

// Adapter reports the context inventory of a Claude Code session.
//
// It reads only the head of the session transcript — up to and including the
// first assistant record — and reconstructs the memory hierarchy from disk. It
// performs no network call, launches no process, and never resolves a transcript
// itself: [ctxinspect.Request] carries the collision-checked transcript path and
// the per-instance config directory, because both need live session state this
// package must not import.
type Adapter struct {
	// Env resolves environment variables. Nil uses [os.Getenv].
	Env func(string) string
}

// New returns the Claude Code adapter.
func New() *Adapter { return &Adapter{} }

// Name implements [ctxinspect.Adapter].
func (a *Adapter) Name() string { return "claude" }

// Supports implements [ctxinspect.Adapter]. It routes through the host's
// compatibility predicate so a custom [tools.*] entry whose command is claude
// inherits this adapter instead of falling through to the generic floor.
func (a *Adapter) Supports(tool string, host ctxinspect.Host) bool {
	if strings.TrimSpace(tool) == "" {
		return false
	}
	if host == nil {
		host = ctxinspect.NoHost()
	}
	return host.IsClaudeCompatible(tool)
}

// Capabilities implements [ctxinspect.Adapter]. It touches no disk, so the UI
// can render a truthful "what this harness can tell you" screen before — or
// instead of — a successful inspection.
func (a *Adapter) Capabilities() ctxinspect.Capabilities {
	return ctxinspect.Capabilities{
		Adapter:   a.Name(),
		CanAnchor: true,
		// Claude Code sends its base system prompt but never writes it to disk.
		// Codex ships it in the rollout; Claude Code does not, and this build
		// does not intercept the request to get it.
		CanVerbatimSystem: false,
		Categories: []ctxinspect.CategoryCapability{
			{
				Name:  CategorySystemPrompt,
				Title: "system prompt + built-in tool schemas",
				Text:  ctxinspect.TextAbsent,
				Token: ctxinspect.TokenResidual,
				Note:  "not written to disk by Claude Code; reported as the reconciliation residual against the provider-measured first-turn total rather than as a category",
			},
			{
				Name:  CategorySystemTools,
				Title: "system tools",
				Text:  ctxinspect.TextCaptured,
				Token: ctxinspect.TokenEstimated,
				Note:  "the transcript records deferred tool names verbatim; the schemas themselves are not recorded, so only the names are priced",
			},
			{
				Name:  CategoryMCP,
				Title: "MCP instructions",
				Text:  ctxinspect.TextCaptured,
				Token: ctxinspect.TokenEstimated,
				Note:  "each server's instruction block is recorded verbatim; its tool schemas are not, and stay inside the residual",
			},
			{
				Name:  CategoryMemory,
				Title: "memory files",
				Text:  ctxinspect.TextReconstructed,
				Token: ctxinspect.TokenEstimated,
				Note:  "the CLAUDE.md chain goes into the system prompt and is absent from the transcript, so it is rebuilt by re-walking Claude Code's own discovery rules; a file Claude Code recorded as nested memory is upgraded to captured",
			},
			{
				Name:  CategorySkills,
				Title: "skills",
				Text:  ctxinspect.TextCaptured,
				Token: ctxinspect.TokenEstimated,
				Note:  "the startup catalogue is recorded verbatim and split per skill; each skill's body is reported as potential cost, since it loads only when invoked",
			},
			{
				Name:  CategoryAgents,
				Title: "agents",
				Text:  ctxinspect.TextCaptured,
				Token: ctxinspect.TokenEstimated,
				Note:  "the startup catalogue is recorded verbatim; an agent with a definition file on disk is yours and carries a lever, one without is built into the harness",
			},
		},
		Notes: []string{
			"Per-item token figures are estimates: there is no offline Anthropic tokenizer, and the provider's counting endpoint needs credentials.",
			"The first-turn total is provider-measured, so the unattributed remainder is obtained by subtraction rather than guessed.",
			"AGENTS.md is not listed: Claude Code does not read it.",
		},
	}
}

// Inspect implements [ctxinspect.Adapter].
func (a *Adapter) Inspect(ctx context.Context, req ctxinspect.Request) (*ctxinspect.Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	env := a.Env
	if env == nil {
		env = os.Getenv
	}
	host := req.HostOrNone()
	est := req.EstimatorOrDefault()

	var head *Head
	var timeline *ModelTimeline
	var timelineErr error
	if path := strings.TrimSpace(req.TranscriptPath); path != "" {
		parsed, err := ParseHead(path)
		if err != nil {
			return nil, err
		}
		head = parsed
		// A second, independent read: the head pins the anchor to the boot
		// turn, while the header's model is a present-tense claim only the
		// latest turn can support. A failure here degrades the model to the
		// boot turn's — with a caveat — rather than failing the report.
		timeline, timelineErr = ScanModelTimeline(path)
	}

	rep := &ctxinspect.Report{
		Harness:     req.Tool,
		SessionID:   sessionID(req, head),
		ProjectPath: req.ProjectPath,
		Model:       modelOf(req, head, timeline),
		Basis:       ctxinspect.BasisProjected,
		GeneratedAt: req.Timestamp(),
	}
	rep.Window = ResolveWindow(rep.Model, env)
	if head != nil {
		rep.Basis = ctxinspect.BasisObserved
	}

	a.applyAnchor(rep, head)
	a.applyModelIdentity(rep, head, timeline, timelineErr)

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cat := a.systemToolsCategory(req, host, est, head); cat != nil {
		rep.Categories = append(rep.Categories, *cat)
	}
	if cat := a.mcpCategory(req, host, est, head, rep); cat != nil {
		rep.Categories = append(rep.Categories, *cat)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	memCat, err := a.memoryCategory(req, est, head, env, rep)
	if err != nil {
		return nil, err
	}
	if memCat != nil {
		rep.Categories = append(rep.Categories, *memCat)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cat := a.skillsCategory(req, est, head, rep); cat != nil {
		rep.Categories = append(rep.Categories, *cat)
	}
	if cat := a.agentsCategory(req, est, head); cat != nil {
		rep.Categories = append(rep.Categories, *cat)
	}

	a.applyReportCaveats(rep, head)
	return rep, nil
}

// sessionID prefers the identifier the transcript recorded, which is the one the
// harness itself uses.
func sessionID(req ctxinspect.Request, head *Head) string {
	if head != nil && head.SessionID != "" {
		return head.SessionID
	}
	return req.SessionID
}

// modelOf resolves the model the header may print as a present-tense fact.
//
// Preference order: the model that served the transcript's most recent
// recorded turn (the model the session is on now), then the model that served
// the first turn, then a model named by a record that carried no accounting,
// and finally whatever the caller believed was configured.
//
// The first preference is the fix for a real failure: a session that switched
// models two minutes after boot spent the following hours captioned with the
// boot model — and resolving its context window from it — because the header
// was read from where the anchor is read. The anchor rightly stays pinned to
// the boot turn ([FirstTurn.Model] is untouched); the header is a different
// claim and follows the latest turn.
func modelOf(req ctxinspect.Request, head *Head, timeline *ModelTimeline) string {
	if timeline != nil && timeline.Current != "" {
		return timeline.Current
	}
	if head != nil && head.FirstTurn != nil && head.FirstTurn.Model != "" {
		return head.FirstTurn.Model
	}
	// A transcript can name the model on records that carry no accounting —
	// <synthetic> placeholders do exactly that — and the model is not an
	// accounting claim. Reading it from the session beats falling back to what
	// the caller believed was configured, which is the value that was wrong.
	if head != nil && head.ModelSeen != "" {
		return head.ModelSeen
	}
	return req.Model
}

// bootModelOf is the model that served the session's first turn, as the head
// parse saw it. It is what the header used to print, and it is still what the
// anchor's provenance is pinned to.
func bootModelOf(head *Head) string {
	if head == nil {
		return ""
	}
	if head.FirstTurn != nil && head.FirstTurn.Model != "" {
		return head.FirstTurn.Model
	}
	return head.ModelSeen
}

// applyModelIdentity reconciles the header's present-tense model with the
// session's history, and says so when the two are different facts.
//
// When the session switched models after boot, the header names the current
// model and this caveat records the switch — including the one thing a reader
// could otherwise mix up: the measured first-turn total (the anchor) was
// served by the boot model and keeps measuring that request, whatever the
// session runs now.
func (a *Adapter) applyModelIdentity(rep *ctxinspect.Report, head *Head, timeline *ModelTimeline, scanErr error) {
	if scanErr != nil {
		rep.AddCaveat("model-scan-failed",
			"the transcript could not be re-read for the model currently serving this session ("+scanErr.Error()+"), so the model above is the one that served the first turn and may be stale.",
			ctxinspect.SeverityWarn)
		return
	}
	if timeline == nil || timeline.Current == "" {
		return
	}
	boot := bootModelOf(head)
	if boot == "" || boot == timeline.Current {
		return
	}
	at := "a time its record did not carry"
	if sw := timeline.LastSwitch; sw != nil && !sw.At.IsZero() {
		at = sw.At.Format("2006-01-02 15:04:05 MST")
	}
	msg := fmt.Sprintf(
		"this session booted on %s and switched to %s at %s. The model and window above follow the most recent recorded turn — they describe the session now. The measured first-turn total (the anchor) is not moved by the switch: that request was served by %s, and it is still the request the anchor measures.",
		boot, timeline.Current, at, boot)
	if timeline.SwitchCount > 1 {
		msg += fmt.Sprintf(" The serving model changed %d times in this transcript; the switch above is the one that established the current model.", timeline.SwitchCount)
	}
	rep.AddCaveat("model-switched", msg, ctxinspect.SeverityInfo)
}

// absenceNote phrases an empty category so it claims only what was established,
// and reports whether the emptiness is a fact or a gap.
//
// "This session recorded no X" is a statement about the session; it is only true
// once the startup injection burst was actually observed. When the head parse
// never reached that burst, the same silence means the parser did not look far
// enough, and saying otherwise turns a gap in the read into a fact about the
// user's configuration.
//
// Witnessing the burst is necessary but not sufficient. The burst is recorded on
// sight — before the decode — precisely so an unreadable record cannot be
// mistaken for an absent one, and that distinction has to survive into the note:
// when an attachment of this kind was seen but could not be decoded, the honest
// sentence is "one was present and could not be read", never "this session
// recorded none". decodeFailed carries that third state.
func absenceNote(head *Head, unobserved, empty string) (note string, established bool) {
	switch {
	case head == nil:
		return "no session was observed, so " + unobserved, false
	case !head.StartupBlockObserved:
		return "this session's startup injections were not found within the parser's budget, so " + unobserved, false
	default:
		return empty, true
	}
}

// decodedAbsenceNote is [absenceNote] with the burst-witnessed / record-decoded
// split applied: decodeFailed says an attachment of this kind was present in the
// startup burst and could not be read.
func decodedAbsenceNote(head *Head, decodeFailed bool, unobserved, empty string) (note string, established bool) {
	note, established = absenceNote(head, unobserved, empty)
	if established && decodeFailed {
		return "an attachment of this kind was recorded at startup but could not be decoded, so " + unobserved, false
	}
	return note, established
}

// decodeFailed reports whether an attachment of this type was witnessed in the
// startup burst and could not be decoded.
func decodeFailed(head *Head, attachType string) bool {
	return head != nil && head.DecodeFailed[attachType]
}

// applyAnchor attaches the provider-measured first-turn total, or explains why
// there is none.
//
// The guard is against a first turn that measures something other than a cold
// fixed prefix — a compacted or replayed conversation. It deliberately does not
// treat a warm prompt cache as disqualifying: Anthropic's cache is shared
// between sessions, so a genuinely fresh session reports a large
// cache_read_input_tokens as a matter of course (measured at 20.4k–22.9k across
// eight independent cold starts), and vetoing on it would leave essentially
// every session without an anchor. The cache split is reported as information.
func (a *Adapter) applyAnchor(rep *ctxinspect.Report, head *Head) {
	if head == nil {
		rep.AddCaveat("no-transcript",
			"no transcript was resolved for this session, so nothing could be observed: the memory hierarchy below is what a fresh session would load, and no measured total exists to reconcile against.",
			ctxinspect.SeverityWarn)
		return
	}
	if head.FirstTurn == nil {
		// Say which of the two things happened. "This session has not completed
		// a model turn yet" is a claim about the session, and it was false on
		// every transcript that opens with <synthetic> placeholder records —
		// four live sessions carrying 187, 124, 114 and 86 completed turns each
		// were told they had never run one. An unknown that ships with a
		// fabricated reason is worse than a bare unknown, so the reason now
		// states what the parser actually saw.
		rep.AddCaveat("anchor-unavailable-no-turn",
			anchorMissingReason(head)+", so there is no provider-measured total to reconcile against and the unattributed remainder is unknown rather than zero.",
			ctxinspect.SeverityWarn)
		return
	}
	if len(head.ResumeSignals) > 0 {
		rep.AddCaveat("anchor-unavailable-resumed",
			"the first recorded turn does not measure a cold startup prefix ("+strings.Join(head.ResumeSignals, "; ")+"), so no measured total is claimed and the unattributed remainder is reported as unknown.",
			ctxinspect.SeverityWarn)
		return
	}

	turn := head.FirstTurn
	rep.Anchor = &ctxinspect.Anchor{
		Tokens: ctxinspect.Measured(turn.PromptTokens(), turn.Source),
		Source: fmt.Sprintf("first assistant turn: %s (input %d + cache creation %d + cache read %d)",
			turn.Source, turn.InputTokens, turn.CacheCreation, turn.CacheRead),
		At: turn.At,
	}
	if head.PreBlockTurns > 0 {
		// The figure is measured, and it measures more than the fixed prefix:
		// the boundary request also carried the earlier turn's output, its tool
		// results and whatever the user attached. Publishing that as an exact
		// prefix — which is what "provider-measured" alone reads as — overstated
		// the prefix by up to 28,948 tokens on a real corpus. Mark it on the
		// anchor itself so --json, both renderers and verify.Compare can act on
		// the qualifier instead of hoping someone reads the caveat, and raise
		// the caveat to Warn: an Info-level note is the wrong weight for the
		// one figure the whole report reconciles against.
		reason := fmt.Sprintf("the measured request also carried %d earlier model turn(s) recorded before this session's startup catalogues, so it covers more than the fixed prefix", head.PreBlockTurns)
		rep.Anchor.UpperBound = true
		rep.Anchor.UpperBoundReason = reason
		rep.AddCaveat("anchor-advanced-past-startup-block",
			fmt.Sprintf("Claude Code recorded this session's startup catalogues after %d earlier model turn(s), so the measured total is read from the first request that actually carried them rather than from the transcript's first turn. It therefore also covers those earlier turn(s) and their tool results, which makes it an UPPER BOUND on the fixed prefix rather than an exact measurement of it: the real prefix is at most this large, and the residual below absorbs the difference.", head.PreBlockTurns),
			ctxinspect.SeverityWarn)
	}
	if head.PostBlockUndecodable > 0 {
		// The parse stepped over an assistant record it could not decode to
		// reach one it could. A placeholder costs nothing to skip; a record that
		// simply would not parse might have been a real model turn, in which
		// case the request that was measured also carried its output. Say so on
		// the anchor rather than publishing a total that quietly covers more
		// than the prefix.
		reason := fmt.Sprintf("%d assistant record(s) between the startup injections and the measured turn could not be decoded, so the measured request may also cover a model turn agent-deck could not read", head.PostBlockUndecodable)
		rep.Anchor.UpperBound = true
		if rep.Anchor.UpperBoundReason == "" {
			rep.Anchor.UpperBoundReason = reason
		} else {
			rep.Anchor.UpperBoundReason += "; " + reason
		}
		rep.AddCaveat("anchor-skipped-undecodable-turn",
			fmt.Sprintf("%d assistant record(s) recorded after this session's startup injections could not be decoded (see the transcript warnings), so the measured total was read from the first record after them that could be. If any of those records was a real model turn, the measured request also carried its output, which makes the total an UPPER BOUND on the fixed prefix rather than an exact measurement of it.", head.PostBlockUndecodable),
			ctxinspect.SeverityWarn)
	}
	if turn.CacheRead > 0 {
		rep.AddCaveat("anchor-warm-cache",
			fmt.Sprintf("%d of the measured %d prompt tokens were served from Anthropic's prompt cache. The cache is shared between sessions, so this is normal for a fresh session and does not change the prompt's size.",
				turn.CacheRead, turn.PromptTokens()),
			ctxinspect.SeverityInfo)
	}
}

// anchorMissingReason states why no first turn could be read, in the words of
// what the parser saw rather than in a guess about the user's session.
func anchorMissingReason(head *Head) string {
	switch {
	case head.UnusableTurns > 0:
		return fmt.Sprintf("%d assistant record(s) were read and none of them carries provider accounting agent-deck can use (see the transcript warnings below); whether this session has completed a model turn is not knowable from them",
			head.UnusableTurns)
	case !head.ReachedAssistant:
		return "the parser reached the end of its record budget without finding an assistant turn, so whether this session has completed one is not knowable from the part of the transcript that was read"
	default:
		return "no assistant turn is on record in this session's transcript"
	}
}

// applyReportCaveats records everything about the report that a number alone
// cannot say.
func (a *Adapter) applyReportCaveats(rep *ctxinspect.Report, head *Head) {
	if !rep.Window.Known() {
		rep.AddCaveat("window-unknown",
			"the context-window size could not be established ("+rep.Window.Detail+"), so no percentage is shown. A wrong denominator would misrepresent an otherwise honest total.",
			ctxinspect.SeverityWarn)
	}
	if rep.Window.Assumed() {
		// The percentage IS shown in this case, which is exactly why it needs
		// saying: the one number a reader takes away rests on a denominator
		// nothing measured.
		rep.AddCaveat("window-assumed",
			"the context-window size was assumed, not established ("+rep.Window.Detail+"), so the percentage is an approximation. The assumption is a lower bound on the real window, so the percentage errs high rather than low.",
			ctxinspect.SeverityInfo)
	}
	if head == nil {
		return
	}

	if head.FirstUserChars > 0 || head.HookContextCount > 0 {
		var parts []string
		if head.FirstUserChars > 0 {
			parts = append(parts, fmt.Sprintf("this session's first typed message (%d characters)", head.FirstUserChars))
		}
		if head.HookContextCount > 0 {
			parts = append(parts, fmt.Sprintf("%d session-start hook injection(s) (%d characters)", head.HookContextCount, head.HookContextChars))
		}
		rep.AddCaveat("residual-includes-turn-inputs",
			"the measured total covers the whole first request, so the unattributed remainder also contains "+strings.Join(parts, " and ")+" alongside the base system prompt and the built-in tool schemas.",
			ctxinspect.SeverityInfo)
	}

	if !head.ReachedAssistant {
		detail := "the parser did not reach an assistant turn within its record budget"
		if head.StartupBlockObserved {
			detail = "the parser found this session's startup injections but no model turn after them within its record budget"
		} else if head.PreBlockTurns > 0 {
			detail = "the parser found no startup injections within its lookahead of the transcript's first model turn"
		}
		rep.AddCaveat("head-boundary-not-reached",
			detail+", so parts of the startup inventory may be missing rather than empty.",
			ctxinspect.SeverityWarn)
	}

	warnings := head.Warnings
	if len(warnings) > maxReportedWarnings {
		rep.AddCaveat("transcript-warnings-truncated",
			fmt.Sprintf("%d transcript records could not be read; the first %d are listed below.", len(warnings), maxReportedWarnings),
			ctxinspect.SeverityWarn)
		warnings = warnings[:maxReportedWarnings]
	}
	for _, w := range warnings {
		rep.AddCaveat("transcript-record-unreadable", w, ctxinspect.SeverityWarn)
	}
}

// systemToolsCategory reports tools whose names are in the prefix while their
// schemas load on demand.
//
// Tools are grouped by what put them there — one rollup for the harness's own
// deferred tools, one per MCP server — because that is the granularity at which
// a user can act: an MCP server can be detached, an individual tool cannot.
func (a *Adapter) systemToolsCategory(req ctxinspect.Request, host ctxinspect.Host, est ctxinspect.Estimator, head *Head) *ctxinspect.Category {
	cat := &ctxinspect.Category{
		Name:        CategorySystemTools,
		Title:       "system tools",
		Description: "tools whose names sit in the prefix while their schemas load only when the agent asks for one",
		Notes: []string{
			"only the names are injected up front, so only the names are priced here; the schemas load on invocation and are part of the unattributed remainder until then",
		},
	}
	if head == nil || head.Deferred == nil || len(head.Deferred.Names) == 0 {
		note, established := decodedAbsenceNote(head, decodeFailed(head, attachDeferredTools),
			"the deferred-tool set is not knowable from disk", "this session recorded no deferred tools")
		cat.Notes = append(cat.Notes, note)
		cat.Unobserved = !established
		return cat
	}

	catalog := catalogNames(host)
	var builtin []ctxinspect.Item
	byServer := make(map[string][]ctxinspect.Item)
	var serverOrder []string

	// A deferred tool's schema is never written to disk, so its potential cost
	// is genuinely unknown rather than zero. Saying so is the whole point of
	// the on-demand state.
	unknownSchema := func() ctxinspect.TokenCount {
		return ctxinspect.UnknownTokens("the tool's schema is negotiated at invocation and never written to disk, so its size is not knowable without launching it")
	}

	for _, name := range head.Deferred.Names {
		// A name plus its separator is what the injected list costs.
		cost := est.EstimateChars(ctxinspect.KindPath, len([]rune(name))+1)
		server, isMCP := mcpServerForTool(name)
		if !isMCP {
			builtin = append(builtin, ctxinspect.Item{
				ID:      "system-tools:builtin:" + name,
				Label:   name,
				Detail:  "harness built-in",
				Content: ctxinspect.CapturedContent(ctxinspect.KindPath, name),
				Load:    ctxinspect.OnDemandCost(cost, unknownSchema()),
				Origin:  ctxinspect.OriginHarnessBuiltin,
				Lever:   ctxinspect.ImmovableLever("shipped inside Claude Code; the name is injected whether or not the tool is used"),
			})
			continue
		}
		if _, ok := byServer[server]; !ok {
			serverOrder = append(serverOrder, server)
		}
		origin := ctxinspect.OriginMCPServer
		lever := ctxinspect.ImmovableLever("contributed by MCP server " + server + ", which agent-deck did not attach")
		if entry := catalog[catalogKey(server)]; entry != "" {
			origin = ctxinspect.OriginAgentDeck
			lever = ctxinspect.CommandLever(
				fmt.Sprintf("agent-deck mcp detach %s %s", sessionRefOf(req), entry),
				"attached by agent-deck from the [mcps.*] catalog",
			)
		}
		byServer[server] = append(byServer[server], ctxinspect.Item{
			ID:      "system-tools:mcp:" + server + ":" + name,
			Label:   name,
			Detail:  "from MCP server " + server,
			Content: ctxinspect.CapturedContent(ctxinspect.KindPath, name),
			Load:    ctxinspect.OnDemandCost(cost, unknownSchema()),
			Origin:  origin,
			Lever:   lever,
		})
	}

	if len(builtin) > 0 {
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:       "system-tools:builtin",
			Label:    "Claude Code built-in tools",
			Detail:   fmt.Sprintf("%d deferred names", len(builtin)),
			Content:  ctxinspect.CapturedContent(ctxinspect.KindPath, joinLabels(builtin)),
			Load:     ctxinspect.OnDemandCost(sumActual(builtin, "sum of the built-in deferred tool names"), unknownSchema()),
			Origin:   ctxinspect.OriginHarnessBuiltin,
			Lever:    ctxinspect.ImmovableLever("shipped inside Claude Code"),
			Children: builtin,
			Rollup:   true,
		})
	}
	for _, server := range serverOrder {
		items := byServer[server]
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:       "system-tools:mcp:" + server,
			Label:    server,
			Detail:   fmt.Sprintf("%d deferred names from this MCP server", len(items)),
			Content:  ctxinspect.CapturedContent(ctxinspect.KindPath, joinLabels(items)),
			Load:     ctxinspect.OnDemandCost(sumActual(items, "sum of this server's deferred tool names"), unknownSchema()),
			Origin:   items[0].Origin,
			Lever:    items[0].Lever,
			Children: items,
			Rollup:   true,
		})
	}
	return cat
}

// mcpCategory reports the instruction block each MCP server contributed.
func (a *Adapter) mcpCategory(req ctxinspect.Request, host ctxinspect.Host, est ctxinspect.Estimator, head *Head, rep *ctxinspect.Report) *ctxinspect.Category {
	cat := &ctxinspect.Category{
		Name:        CategoryMCP,
		Title:       "MCP instructions",
		Description: "the instruction block each MCP server contributed to the prefix, verbatim",
		Notes: []string{
			"tool schemas are negotiated at runtime and never written to disk; they stay inside the unattributed remainder rather than being guessed at here",
		},
	}
	if head == nil || head.MCP == nil || len(head.MCP.Names) == 0 {
		note, established := decodedAbsenceNote(head, decodeFailed(head, attachMCPInstruction),
			"no injected MCP instructions could be read", "this session recorded no MCP instruction blocks")
		cat.Notes = append(cat.Notes, note)
		cat.Unobserved = !established
		return cat
	}

	catalog := catalogNames(host)
	attached := attachedByName(host, req)

	for i, name := range head.MCP.Names {
		block := ""
		if i < len(head.MCP.Blocks) {
			block = head.MCP.Blocks[i]
		}
		if block == "" {
			rep.AddCaveat("mcp-block-missing",
				fmt.Sprintf("MCP server %q was named in the injected instructions but no block was recorded for it, so its cost is missing from this report rather than zero.", name),
				ctxinspect.SeverityWarn)
		}

		origin := ctxinspect.OriginMCPServer
		lever := ctxinspect.ImmovableLever("agent-deck has no record of configuring this server, so it offers no command for it")
		key := catalogKey(name)
		switch {
		case catalog[key] != "":
			origin = ctxinspect.OriginAgentDeck
			lever = ctxinspect.CommandLever(
				fmt.Sprintf("agent-deck mcp detach %s %s", sessionRefOf(req), catalog[key]),
				fmt.Sprintf("matched agent-deck's [mcps.*] catalog entry %q by name", catalog[key]),
			)
		case attached[key] != "":
			lever = ctxinspect.FileLever(attached[key], "configured in "+attached[key])
		}

		load := ctxinspect.LoadedNowCost(est.Estimate(ctxinspect.KindMarkdown, block))
		if block == "" {
			load = ctxinspect.LoadedNowCost(ctxinspect.UnknownTokens("the server was named but its instruction block was not recorded"))
		}
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:      "mcp:" + name,
			Label:   name,
			Detail:  fmt.Sprintf("%d characters of instructions", len([]rune(block))),
			Content: ctxinspect.CapturedContent(ctxinspect.KindMarkdown, block),
			Load:    load,
			Origin:  origin,
			Lever:   lever,
		})
	}
	return cat
}

// memoryCategory reconstructs the CLAUDE.md hierarchy.
func (a *Adapter) memoryCategory(req ctxinspect.Request, est ctxinspect.Estimator, head *Head, env func(string) string, rep *ctxinspect.Report) (*ctxinspect.Category, error) {
	chain, err := DiscoverMemory(MemoryOptions{
		ConfigDir:      req.ConfigDir,
		ProjectPath:    req.ProjectPath,
		TranscriptPath: req.TranscriptPath,
		Env:            env,
	})
	if err != nil {
		return nil, fmt.Errorf("reconstructing the CLAUDE.md hierarchy: %w", err)
	}

	cat := &ctxinspect.Category{
		Name:        CategoryMemory,
		Title:       "memory files",
		Description: "the CLAUDE.md hierarchy, in load order from managed policy down to this project's auto-memory",
		Notes: []string{
			"rebuilt by re-walking Claude Code's own discovery rules: the injected memory text goes into the system prompt and is not written to the transcript",
			"AGENTS.md is not listed: Claude Code does not read it",
			"each file is priced with the wrapper line Claude Code injects above it, because those bytes are in the prompt too",
		},
	}
	for _, w := range chain.Warnings {
		rep.AddCaveat("memory-discovery-warning", w, ctxinspect.SeverityWarn)
	}
	if len(chain.SettingsRead) > 0 {
		cat.Notes = append(cat.Notes, "settings honoured from: "+strings.Join(chain.SettingsRead, ", "))
	}
	if len(chain.Excludes) > 0 {
		cat.Notes = append(cat.Notes, "claudeMdExcludes applied: "+strings.Join(chain.Excludes, ", "))
	}

	if chain.Disabled {
		cat.Notes = append(cat.Notes, disableMemoryEnv+" is set, so no memory file is loaded at all")
		rep.AddCaveat("memory-disabled",
			disableMemoryEnv+" is set in this process's environment, so the memory hierarchy is reported as empty. agent-deck reads its own environment here, which matches the session's only when the session was launched from it.",
			ctxinspect.SeverityWarn)
		return cat, nil
	}

	captured := map[string]string{}
	if head != nil {
		captured = head.NestedMemory
	}
	usedCapture := make(map[string]bool)
	unverifiedWrapper := false

	a.noteAutoMemory(cat, rep, chain)

	for _, f := range chain.Files {
		item, used := a.memoryItem(f, est, captured, chain.AutoMem)
		if used {
			usedCapture[f.Path] = true
		}
		if !f.WrapperObserved {
			unverifiedWrapper = true
		}
		if f.ReadErr != "" {
			rep.AddCaveat("memory-file-unreadable",
				fmt.Sprintf("%s is in the chain but could not be read (%s); it is priced from its size on disk instead of its content.", f.Path, f.ReadErr),
				ctxinspect.SeverityWarn)
		}
		cat.Items = append(cat.Items, item)
	}

	// A file Claude Code recorded loading that the walk did not find is a gap in
	// the reconstruction. Adding it keeps the inventory complete and makes the
	// gap visible instead of silently dropping real context.
	for path, content := range captured {
		if usedCapture[path] {
			continue
		}
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:      "memory:" + path,
			Label:   shortPath(path, req.ProjectPath),
			Detail:  "recorded by the harness but not found by the discovery walk",
			Content: ctxinspect.CapturedContent(ctxinspect.KindMarkdown, content),
			Load:    ctxinspect.LoadedNowCost(est.Estimate(ctxinspect.KindMarkdown, content)),
			Origin:  ctxinspect.OriginUserConfig,
			Lever:   ctxinspect.FileLever(path, "loaded by Claude Code; agent-deck's reconstruction of the chain did not reach it"),
			Caveats: []ctxinspect.Caveat{{
				Code:     "memory-outside-reconstruction",
				Message:  fmt.Sprintf("Claude Code recorded loading %s, which agent-deck's rebuild of the discovery rules did not produce. The text is exact; the rebuild has a gap.", path),
				Severity: ctxinspect.SeverityWarn,
				Category: CategoryMemory,
			}},
		})
	}

	for _, ex := range chain.Excluded {
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:      "memory-excluded:" + ex.Path,
			Label:   shortPath(ex.Path, req.ProjectPath),
			Detail:  "excluded by claudeMdExcludes pattern " + ex.Pattern,
			Content: ctxinspect.AbsentContent("excluded from the chain, so its content was never read"),
			Load:    ctxinspect.AvailableCost(potentialFromSize(est, ex.Size)),
			Origin:  ctxinspect.OriginUserConfig,
			Lever:   ctxinspect.FileLever(ex.Path, "kept out of the prefix by the claudeMdExcludes pattern "+ex.Pattern),
		})
	}

	if unverifiedWrapper {
		rep.AddCaveat("memory-wrapper-unverified",
			"one or more files sit in a layer whose exact injected wrapper line agent-deck has not observed in a real prompt. Their content is priced correctly; the wrapper's few tokens use the closest observed form.",
			ctxinspect.SeverityInfo)
	}
	if len(cat.Items) == 0 {
		cat.Notes = append(cat.Notes, "no memory file was found for this session")
	} else {
		// The chain header is injected once, above every file.
		cat.Notes = append(cat.Notes, fmt.Sprintf("a %d-character header is injected once above the whole chain and is included in the first file's cost", len([]rune(memoryHeader))))
	}
	return cat, nil
}

// noteAutoMemory says out loud where the per-project auto-memory index was
// looked for and how sure the answer is.
//
// Three cases each need saying. An index that exists somewhere other than where
// the rule puts it means a user who goes looking will find the wrong file — that
// is exactly how a 130-byte stub came to be presented as this session's memory
// while the 17,506-byte index it actually loads went unmentioned. An inferred
// location must not read as settled. And auto-memory switched off is different
// from auto-memory empty.
func (a *Adapter) noteAutoMemory(cat *ctxinspect.Category, rep *ctxinspect.Report, chain *MemoryChain) {
	auto := chain.AutoMem
	switch {
	case auto.Disabled:
		cat.Notes = append(cat.Notes, "auto-memory is switched off for this session (autoMemoryEnabled is false), so no index is loaded however many exist on disk")
		return
	case auto.Basis == autoMemAbsent:
		if auto.Path != "" {
			cat.Notes = append(cat.Notes, "no auto-memory index exists yet; Claude Code would keep this session's at "+auto.Path)
		}
	case auto.Basis == autoMemFromGitRoot:
		cat.Notes = append(cat.Notes,
			"auto-memory is scoped to the git repository root ("+auto.ProjectRoot+"), not to the working directory, so every session in the repository shares this one index")
	}

	if !auto.Confident && auto.Basis == autoMemFromExistingIndex && len(auto.Candidates) > 0 {
		rep.AddCaveat("memory-automem-location-inferred",
			fmt.Sprintf("no auto-memory index exists where Claude Code's rule puts this session's (%s), so the row uses %s, the only index that does exist among the slugs this session could resolve to. Its bytes are real; that this session is the one loading them is inferred.",
				auto.Candidates[0], auto.Path),
			ctxinspect.SeverityWarn)
	}
	for _, other := range auto.Superseded {
		rep.AddCaveat("memory-automem-superseded",
			fmt.Sprintf("%s is also an auto-memory index on disk, but this session loads %s. Older Claude Code versions filed auto-memory under the working directory's slug rather than the repository root's, so an index left behind there is not read and editing it changes nothing.",
				other, auto.Path),
			ctxinspect.SeverityInfo)
	}
}

// memoryItem converts one discovered memory file into an item, upgrading it to
// captured text when the harness recorded the exact bytes it loaded.
//
// auto carries the auto-memory resolution so that row can name the rule that
// located it, and can decline to present a settled figure when it could not.
func (a *Adapter) memoryItem(f MemoryFile, est ctxinspect.Estimator, captured map[string]string, auto AutoMemory) (ctxinspect.Item, bool) {
	wrapperChars := len([]rune(f.Wrapper))
	if f.Order == 0 {
		wrapperChars += len([]rune(memoryHeader))
	}

	var content ctxinspect.Content
	var cost ctxinspect.TokenCount
	usedCapture := false

	switch {
	case captured[f.Path] != "":
		text := captured[f.Path]
		content = ctxinspect.CapturedContent(ctxinspect.KindMarkdown, text)
		cost = est.EstimateChars(ctxinspect.KindMarkdown, wrapperChars+len([]rune(text)))
		if wrapperChars > 0 {
			// The item is priced for the wrapper too, and the wrapper is not in
			// the bytes below. Without this sentence the detail screen shows
			// three different sizes for one item and explains none of them, so
			// the "verbatim text" promise silently covers only part of what was
			// charged for.
			content.Note = fmt.Sprintf("the cost above also covers the %d-character wrapper Claude Code injects around this file, which the harness does not record and is not shown below", wrapperChars)
		}
		usedCapture = true
	case f.ReadErr != "":
		content = ctxinspect.AbsentContent("found in the chain but unreadable: " + f.ReadErr)
		cost = est.EstimateChars(ctxinspect.KindMarkdown, wrapperChars+int(f.Size))
	default:
		note := "rebuilt by re-walking Claude Code's discovery rules; the harness does not record this text"
		if f.Truncated {
			note += fmt.Sprintf("; showing the first %d bytes, which is Claude Code's own per-file cap", maxMemoryFileBytes)
		}
		content = ctxinspect.ReconstructedContent(ctxinspect.KindMarkdown, f.Wrapper+"\n\n"+f.Content, note)
		content.Truncated = f.Truncated
		cost = est.EstimateChars(ctxinspect.KindMarkdown, wrapperChars+f.Chars)
	}

	detail := f.Layer.String()
	if f.Layer == LayerImport {
		detail = fmt.Sprintf("@-import from %s (depth %d)", f.ImportedBy, f.ImportDepth)
	}
	detail = fmt.Sprintf("%s · %s", detail, humanBytes(f.Size))
	// An aliased file is one file. Saying so on the row is what stops a reader
	// from going looking for the second copy they expected to see charged.
	if len(f.Aliases) > 0 {
		detail += fmt.Sprintf(" · also reached as %s — one file, counted once", strings.Join(f.Aliases, ", "))
	}

	origin := ctxinspect.OriginProject
	switch f.Layer {
	case LayerManaged:
		origin = ctxinspect.OriginHarnessBuiltin
	case LayerUser, LayerAutoMem:
		origin = ctxinspect.OriginUserConfig
	}

	why := fmt.Sprintf("%s layer, position %d in the load order", f.Layer, f.Order)
	// The auto-memory row is the one row whose *location* is derived rather than
	// walked to, so the lever says which rule put it there. Without that, a user
	// whose repository holds a second, stale index has no way to tell which of
	// the two the panel means.
	var itemCaveats []ctxinspect.Caveat
	if f.Layer == LayerAutoMem && f.Path == auto.Path {
		if auto.Reason != "" {
			why += "; " + auto.Reason
		}
		if !auto.Confident {
			detail += " · location inferred, not confirmed"
			itemCaveats = append(itemCaveats, ctxinspect.Caveat{
				Code:     "memory-automem-location-inferred",
				Message:  "this file's bytes are read from disk, but that this session is the one loading them is inferred: " + auto.Reason,
				Severity: ctxinspect.SeverityWarn,
				Category: CategoryMemory,
			})
		}
	}
	if len(f.Aliases) > 0 && f.ResolvedPath != "" {
		// The lever must point at the file that actually holds the bytes.
		// Editing an alias edits the same inode, but telling a user to edit a
		// symlink is telling them to edit something other than what they will
		// see when they open it.
		why += fmt.Sprintf("; %s and %s are the same file (%s)",
			f.Path, strings.Join(f.Aliases, ", "), f.ResolvedPath)
	}
	lever := ctxinspect.FileLever(f.Path, why)
	if f.Layer == LayerManaged {
		lever = ctxinspect.ImmovableLever("managed policy instructions; not editable from this session")
	}

	return ctxinspect.Item{
		ID: "memory:" + f.Path,
		// The absolute path is the label: it is what the user has to open, and
		// several files in one chain routinely share a base name.
		Label:   f.Path,
		Detail:  detail,
		Content: content,
		Load:    ctxinspect.LoadedNowCost(cost),
		Origin:  origin,
		Lever:   lever,
		Caveats: itemCaveats,
	}, usedCapture
}

// skillsCategory reports the startup skill catalogue and the skills on disk
// that did not load.
//
// The distinction is the point of the category: a listed skill costs its
// catalogue line today and its body only if invoked, while an unlisted skill on
// disk costs a certain zero. Deleting the wrong one buys nothing.
func (a *Adapter) skillsCategory(req ctxinspect.Request, est ctxinspect.Estimator, head *Head, rep *ctxinspect.Report) *ctxinspect.Category {
	disk := DiscoverSkills(req.ConfigDir, req.ProjectPath)
	byName := make(map[string]DiskSkill, len(disk))
	for _, s := range disk {
		byName[s.Name] = s
	}

	cat := &ctxinspect.Category{
		Name:        CategorySkills,
		Title:       "skills",
		Description: "the startup skill catalogue, plus the skills on disk that it did not list",
		Notes: []string{
			"a listed skill costs its catalogue line in the prefix; its body is the potential cost and loads only when the skill is invoked",
		},
	}

	// listed is only authoritative once the catalogue has been read *and* split
	// per skill. Until then a name's absence from it says nothing, so the two
	// facts are tracked apart.
	listed := make(map[string]bool)
	split := false

	// catalogueEstablished is the same predicate every other category uses for
	// its empty case: is the silence a finding about the session, or a gap in
	// the read. Skills was the only category that never asked, and with an
	// undecodable catalogue and no skills on disk it printed "skills (0 items)"
	// at 0 tokens — the exact phantom zero the honest-unknown floor exists to
	// prevent, on the category most likely to be large.
	catalogueEstablished := true

	switch {
	case head == nil || head.Skills == nil:
		note, established := decodedAbsenceNote(head, decodeFailed(head, attachSkillListing),
			"which of the skills below this session listed at startup is not knowable",
			"this session recorded no skill catalogue, so which of the skills below loaded is not knowable")
		cat.Notes = append(cat.Notes, note)
		catalogueEstablished = established
	default:
		names, derived, derivErr := skillNames(head.Skills)
		if derivErr != nil {
			rep.AddCaveat("skill-names-underivable", derivErr.Error(), ctxinspect.SeverityWarn)
		} else if derived {
			rep.AddCaveat("skill-names-derived",
				fmt.Sprintf("this Claude Code version records the skill catalogue without a name list, so the %d names below were recovered from the catalogue text itself and checked against the harness's own count.", len(names)),
				ctxinspect.SeverityInfo)
		}
		entries, err := SplitSkillListing(head.Skills.Content, names)
		if err != nil {
			rep.AddCaveat("skill-listing-unsplittable",
				fmt.Sprintf("the injected skill catalogue could not be split per skill (%v), so it is reported as one block and no per-skill cost is claimed.", err),
				ctxinspect.SeverityWarn)
			cat.Items = append(cat.Items, ctxinspect.Item{
				ID:      "skills:listing",
				Label:   fmt.Sprintf("skill catalogue (%d skills, unsplit)", head.Skills.Count),
				Detail:  "the harness's own listing, verbatim",
				Content: ctxinspect.CapturedContent(ctxinspect.KindListing, head.Skills.Content),
				Load:    ctxinspect.LoadedNowCost(est.Estimate(ctxinspect.KindListing, head.Skills.Content)),
				Origin:  ctxinspect.OriginUserConfig,
				Lever:   ctxinspect.ImmovableLever("the catalogue is generated; trim it by removing individual skills"),
			})
			break
		}
		split = true
		if head.Skills.Count != len(entries) {
			rep.AddCaveat("skill-count-mismatch",
				fmt.Sprintf("the harness reported %d skills but named %d; the report follows the names it gave.", head.Skills.Count, len(entries)),
				ctxinspect.SeverityWarn)
		}
		for _, e := range entries {
			listed[e.Name] = true
			cat.Items = append(cat.Items, a.listedSkillItem(e, byName[e.Name], est))
		}
	}

	if !split {
		a.unknownSkillItems(cat, disk, est, head, rep)
		// An empty category has to say which of the two it is. With items on it
		// the question is already answered — an unobserved category that lists
		// its contents is a contradiction [ctxinspect.Report.Validate] rejects —
		// so the flag is set only when there is nothing to show.
		if len(cat.Items) == 0 {
			cat.Unobserved = !catalogueEstablished
		}
		return cat
	}

	for _, s := range disk {
		if listed[s.Name] {
			continue
		}
		potential := potentialFromSize(est, s.Size)
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:      "skill:" + s.Name,
			Label:   s.Name,
			Detail:  s.Source + " · on disk, not listed at startup",
			Content: ctxinspect.AbsentContent("not listed in this session's catalogue; its body was not read"),
			Load:    ctxinspect.AvailableCost(potential),
			Origin:  originForSkillSource(s.Source),
			Lever:   ctxinspect.DirLever(s.Dir, "remove the skill directory to keep it out of every future session"),
		})
	}

	if len(disk) > 0 {
		cat.Notes = append(cat.Notes, fmt.Sprintf("%d skills found on disk, %d listed at startup", len(disk), len(listed)))
	}
	return cat
}

// unknownSkillItems lists the skills on disk at unknown cost, for the cases
// where no per-skill catalogue was read.
//
// A skill is only certainly absent from the prefix when the catalogue that would
// have listed it was read and split. Without that, silence is an unread
// catalogue as readily as an empty one — and reporting a certain, summable zero
// for content that is in fact loaded is the single error the provenance model
// exists to prevent. It cost 88 skills and 29,927 characters per session on a
// third of a real corpus before the head parse learned to find catalogues
// injected after the first model turn; this is the floor that keeps the failure
// honest if it ever recurs for another reason.
func (a *Adapter) unknownSkillItems(cat *ctxinspect.Category, disk []DiskSkill, est ctxinspect.Estimator, head *Head, rep *ctxinspect.Report) {
	// An undecodable catalogue is a gap in the read whether or not there are
	// skills on disk to hang rows off, and it was reported only when there were.
	// A session with no skills on disk and an unreadable catalogue got a silent
	// "skills (0 items)" — no row, no note, no caveat, and a zero that reads as
	// a finding.
	if head != nil && head.Skills == nil && decodeFailed(head, attachSkillListing) {
		scope := fmt.Sprintf("the %d skills on disk below are reported at unknown cost rather than at zero", len(disk))
		if len(disk) == 0 {
			scope = "this category is reported as not established rather than as empty"
		}
		rep.AddCaveat("skill-catalogue-undecodable",
			"this session recorded a skill catalogue and agent-deck could not decode it (see the transcript warnings), so "+scope+". This is a gap in the read, not a finding about the session.",
			ctxinspect.SeverityWarn)
	}
	if len(disk) == 0 {
		return
	}
	// countsAgainstTotal separates the two reasons a per-skill cost is unknown.
	// When the catalogue was READ but could not be split, its whole cost is
	// already on the report as the unsplit block: these rows are that block seen
	// per skill, so counting their unknowns would both double-count the tokens
	// and turn a total that is actually complete into "≥". They are marked
	// informational instead — visible, actionable, and excluded from the
	// arithmetic. When no catalogue was read at all, nothing accounts for these
	// skills, and their unknown must stay in the sum where it can be seen.
	informational := false
	reason := "no session was observed, so whether Claude Code lists this skill is not knowable"
	absent := "no session was observed, so this skill's catalogue line was not read"
	switch {
	case head == nil:
	case head.Skills != nil:
		informational = true
		// The catalogue is on record but could not be split, so its whole cost is
		// already reported as the unsplit block above. What is unknown here is
		// only how much of that block belongs to this skill.
		reason = "the catalogue could not be split per skill, so this skill's share of the block above is not knowable"
		absent = "the catalogue was recorded but could not be split, so this skill's own line was not isolated"
	case decodeFailed(head, attachSkillListing):
		// The catalogue is not missing from this session; it is missing from
		// this report. Saying "no skill catalogue was found in this session's
		// transcript" would blame the session for the reader's failure, and it
		// is the same sentence a session with genuinely no catalogue gets.
		reason = "this session's skill catalogue was recorded but could not be decoded, so whether this skill was listed at startup is not knowable"
		absent = "a skill catalogue was recorded for this session and could not be decoded, so this skill's listing was never read"
		// The caveat is raised above, before the empty-disk return, so a session
		// with no skills on disk still gets told the read failed.
	default:
		reason = "this session's skill catalogue was not read, so whether this skill was listed at startup is not knowable"
		absent = "no catalogue was read for this session, so this skill's listing was not observed"
		rep.AddCaveat("skill-catalogue-unobserved",
			fmt.Sprintf("no skill catalogue was found in this session's transcript, so the %d skills on disk below are reported at unknown cost rather than at zero. Their catalogue lines may well be in the prefix; this report cannot say they are not.", len(disk)),
			ctxinspect.SeverityWarn)
	}
	detail := s_onDisk
	if informational {
		detail = "on disk · its share of the unsplit catalogue block above"
	}
	for _, s := range disk {
		potential := potentialFromSize(est, s.Size)
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:            "skill:" + s.Name,
			Label:         s.Name,
			Detail:        s.Source + " · " + detail,
			Content:       ctxinspect.AbsentContent(absent),
			Load:          ctxinspect.OnDemandCost(ctxinspect.UnknownTokens(reason), potential),
			Origin:        originForSkillSource(s.Source),
			Lever:         ctxinspect.DirLever(s.Dir, "remove the skill directory to keep it out of every future session"),
			Informational: informational,
		})
	}
}

// s_onDisk is the detail line for a skill whose cost nothing else accounts for.
const s_onDisk = "on disk"

// skillNames returns the names to split the catalogue by, recovering them from
// the catalogue text when the harness did not record them.
//
// The recovery is only accepted when it reproduces the count the harness itself
// published. A derivation that disagrees with the harness is wrong by
// definition, and mis-assigning cost between skills is precisely the failure
// this feature exists to prevent — so it degrades to one unsplit block instead.
func skillNames(listing *SkillListing) (names []string, derived bool, err error) {
	if len(listing.Names) > 0 {
		return listing.Names, false, nil
	}
	candidates := DeriveSkillNames(listing.Content)
	if len(candidates) == 0 {
		return nil, false, fmt.Errorf("this Claude Code version records the skill catalogue without a name list, and no catalogue entries could be recovered from its text")
	}
	if listing.Count > 0 && len(candidates) != listing.Count {
		return nil, false, fmt.Errorf("this Claude Code version records the skill catalogue without a name list; %d entries were recovered from its text but the harness reported %d skills, so the per-skill split is not trustworthy and was discarded",
			len(candidates), listing.Count)
	}
	return candidates, true, nil
}

// listedSkillItem builds the item for a skill that is in the startup catalogue.
func (a *Adapter) listedSkillItem(e SkillEntry, disk DiskSkill, est ctxinspect.Estimator) ctxinspect.Item {
	load := ctxinspect.Load{
		State:  ctxinspect.LoadedNow,
		Actual: est.EstimateChars(ctxinspect.KindListing, e.Chars),
	}
	origin := ctxinspect.OriginHarnessBuiltin
	lever := ctxinspect.ImmovableLever("agent-deck could not locate this skill on disk, so it offers no way to remove it")
	detail := "listed at startup"

	if disk.Path != "" {
		// The body is only worth showing as potential cost when it is both
		// known and different from the catalogue line already counted; an
		// equal or unknown potential is noise the report model rejects.
		if potential := potentialFromSize(est, disk.Size); potential.Known() {
			pv, _ := potential.Value()
			av, actualKnown := load.Actual.Value()
			if !actualKnown || pv != av {
				p := potential
				load.Potential = &p
			}
		}
		origin = originForSkillSource(disk.Source)
		lever = ctxinspect.DirLever(disk.Dir, "remove the skill directory to drop it from the catalogue")
		detail = fmt.Sprintf("listed at startup · %s · body %s", disk.Source, humanBytes(disk.Size))
	}

	return ctxinspect.Item{
		ID:      "skill:" + e.Name,
		Label:   e.Name,
		Detail:  detail,
		Content: ctxinspect.CapturedContent(ctxinspect.KindListing, e.Listing),
		Load:    load,
		Origin:  origin,
		Lever:   lever,
	}
}

// agentsCategory reports the startup agent catalogue.
func (a *Adapter) agentsCategory(req ctxinspect.Request, est ctxinspect.Estimator, head *Head) *ctxinspect.Category {
	cat := &ctxinspect.Category{
		Name:        CategoryAgents,
		Title:       "agents",
		Description: "the agents made available at startup, one catalogue line each",
		Notes: []string{
			"an agent with a definition file on disk is yours and can be removed; one without is built into Claude Code",
		},
	}
	if head == nil || head.Agents == nil || len(head.Agents.Lines) == 0 {
		note, established := decodedAbsenceNote(head, decodeFailed(head, attachAgentListing),
			"the agent catalogue could not be read", "this session recorded no agent catalogue")
		cat.Notes = append(cat.Notes, note)
		cat.Unobserved = !established
		return cat
	}

	entries, err := SplitAgentListing(head.Agents.Types, head.Agents.Lines)
	if err != nil {
		// Fall back to pricing the lines without names rather than dropping a
		// real contributor from the report.
		joined := strings.Join(head.Agents.Lines, "\n")
		cat.Notes = append(cat.Notes, "the catalogue's identifiers and lines did not pair up ("+err.Error()+"), so it is reported as one block")
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:      "agents:listing",
			Label:   fmt.Sprintf("agent catalogue (%d lines, unpaired)", len(head.Agents.Lines)),
			Detail:  "the harness's own listing, verbatim",
			Content: ctxinspect.CapturedContent(ctxinspect.KindListing, joined),
			Load:    ctxinspect.LoadedNowCost(est.Estimate(ctxinspect.KindListing, joined)),
			Origin:  ctxinspect.OriginHarnessBuiltin,
			Lever:   ctxinspect.ImmovableLever("the catalogue is generated by the harness"),
		})
		return cat
	}

	disk := DiscoverAgents(req.ConfigDir, req.ProjectPath)
	byName := make(map[string]DiskAgent, len(disk))
	for _, d := range disk {
		byName[d.Name] = d
	}

	builtins := 0
	for _, e := range entries {
		origin := ctxinspect.OriginHarnessBuiltin
		lever := ctxinspect.ImmovableLever("no definition file on disk, so this agent is built into Claude Code")
		detail := "built-in"
		if d, ok := byName[e.Name]; ok {
			origin = originForSkillSource(d.Source)
			lever = ctxinspect.FileLever(d.Path, "defined in "+d.Path)
			detail = d.Source + " · " + d.Path
		} else {
			builtins++
		}
		cat.Items = append(cat.Items, ctxinspect.Item{
			ID:      "agent:" + e.Name,
			Label:   e.Name,
			Detail:  detail,
			Content: ctxinspect.CapturedContent(ctxinspect.KindListing, e.Listing),
			Load:    ctxinspect.LoadedNowCost(est.EstimateChars(ctxinspect.KindListing, e.Chars)),
			Origin:  origin,
			Lever:   lever,
		})
	}
	cat.Notes = append(cat.Notes, fmt.Sprintf("%d of %d agents are built into the harness and cannot be removed", builtins, len(entries)))
	return cat
}

// originForSkillSource maps a discovery source onto an origin.
func originForSkillSource(source string) ctxinspect.Origin {
	switch source {
	case "project":
		return ctxinspect.OriginProject
	case "plugin":
		return ctxinspect.OriginPlugin
	default:
		return ctxinspect.OriginUserConfig
	}
}

// catalogNames maps a lookup key to the agent-deck catalog entry name, so an
// item can carry the exact detach command. An unavailable host yields an empty
// map, which downgrades levers rather than failing the report.
func catalogNames(host ctxinspect.Host) map[string]string {
	out := map[string]string{}
	entries, err := host.CatalogMCPs()
	if err != nil {
		return out
	}
	for _, e := range entries {
		out[catalogKey(e.Name)] = e.Name
	}
	return out
}

// attachedByName maps a lookup key to the file that configured the server.
func attachedByName(host ctxinspect.Host, req ctxinspect.Request) map[string]string {
	out := map[string]string{}
	if !host.SupportsMCPManager(req.Tool) {
		return out
	}
	refs, err := host.AttachedMCPs(req.Tool, req.ProjectPath)
	if err != nil {
		return out
	}
	for _, r := range refs {
		if r.SourcePath != "" {
			out[catalogKey(r.Name)] = r.SourcePath
		}
	}
	return out
}

// catalogKey normalises an MCP server name for matching.
//
// Claude Code names a plugin-provided server "plugin:<plugin>:<server>" while
// agent-deck's catalog knows it as "<server>", so the final segment is the
// comparable part. Matching is case-insensitive because config.toml keys are
// hand-written.
func catalogKey(name string) string {
	name = strings.TrimSpace(name)
	if idx := strings.LastIndex(name, ":"); idx >= 0 && idx+1 < len(name) {
		name = name[idx+1:]
	}
	return strings.ToLower(name)
}

// sessionRefOf returns the reference to interpolate into a command lever,
// falling back to a visible placeholder so a copied command can never silently
// target the wrong session.
func sessionRefOf(req ctxinspect.Request) string {
	if s := strings.TrimSpace(req.SessionRef); s != "" {
		return s
	}
	return "<session>"
}

// potentialFromSize prices a file's body as the cost it would add if it were
// fully loaded.
//
// It returns an unknown rather than a zero for an empty or unmeasurably small
// file. A potential of zero is indistinguishable from "costs nothing", which is
// a claim, and [ctxinspect.Report.Validate] rightly rejects a potential equal to
// the actual cost as noise.
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

// sumActual sums a rollup's children for display. The value is never counted —
// [ctxinspect.Item.Rollup] makes the children the countable leaves — but a
// group header with no figure would read as free.
func sumActual(items []ctxinspect.Item, method string) ctxinspect.TokenCount {
	counts := make([]ctxinspect.TokenCount, 0, len(items))
	for _, it := range items {
		counts = append(counts, it.Load.Actual)
	}
	return ctxinspect.SumTokens(method, counts...)
}

// joinLabels renders a rollup's members as the text it stands for.
func joinLabels(items []ctxinspect.Item) string {
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Label)
	}
	return strings.Join(names, ", ")
}

// shortPath renders a path relative to the project when that is shorter and
// still unambiguous, so a list row is readable without losing the full path —
// which stays available on the item's lever.
func shortPath(path, projectPath string) string {
	if p := strings.TrimSpace(projectPath); p != "" && strings.HasPrefix(path, p+string(os.PathSeparator)) {
		return strings.TrimPrefix(path, p+string(os.PathSeparator))
	}
	return path
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
