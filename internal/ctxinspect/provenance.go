package ctxinspect

// TextProv records how good the *bytes* of an item are: whether we are showing
// what the harness actually injected, something we rebuilt from the same
// sources the harness reads, or nothing at all.
//
// It is deliberately independent of [TokenProv]. Claude Code writes its skill
// listing into the transcript verbatim (TextCaptured) but reports no per-item
// token cost, while the first assistant turn reports a provider-measured token
// total (TokenProviderMeasured) with no text whatsoever. One axis cannot say
// both things.
type TextProv uint8

const (
	// TextCaptured means the bytes are exactly what the harness injected, read
	// back from a record the harness itself wrote.
	TextCaptured TextProv = iota
	// TextReconstructed means we rebuilt the bytes by re-running the harness's
	// own discovery rules over the same files. It should match, but a harness
	// change or an undocumented filter can make it differ.
	TextReconstructed
	// TextAbsent means we know the content is in the context window and we
	// cannot see it. The item still exists and may still carry a cost.
	TextAbsent
)

var (
	textProvNames = map[TextProv]string{
		TextCaptured:      "captured",
		TextReconstructed: "reconstructed",
		TextAbsent:        "absent",
	}
	textProvByName = invertEnum(textProvNames)
)

// String returns the wire/display name.
func (p TextProv) String() string { return enumName(p, textProvNames, "absent") }

// Short returns the compact badge form rendered in dense list rows.
func (p TextProv) Short() string {
	switch p {
	case TextCaptured:
		return "CAPT"
	case TextReconstructed:
		return "RECON"
	default:
		return "ABSENT"
	}
}

// MarshalJSON encodes the value as its wire name.
func (p TextProv) MarshalJSON() ([]byte, error) { return marshalEnum(p, textProvNames) }

// UnmarshalJSON decodes the value from its wire name.
func (p *TextProv) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, textProvByName, p, "text provenance")
}

// textProvRank orders text provenance strongest (0) to weakest:
//
//	captured < reconstructed < absent
//
// It is spelled out rather than left to the constant order so that adding a
// value to the enum cannot silently reorder trust.
func textProvRank(p TextProv) int {
	switch p {
	case TextCaptured:
		return 0
	case TextReconstructed:
		return 1
	default:
		return 2
	}
}

// weakestTextProv returns the less trustworthy of two text provenances.
func weakestTextProv(a, b TextProv) TextProv {
	if textProvRank(a) >= textProvRank(b) {
		return a
	}
	return b
}

// TokenProv records how good a *number* is.
type TokenProv uint8

const (
	// TokenProviderMeasured means the model provider or the harness reported
	// the number and we only read it back. Nothing was inferred.
	TokenProviderMeasured TokenProv = iota
	// TokenComputed means we tokenized real bytes with the model's own
	// tokenizer. No adapter may claim this until a tokenizer is vendored; the
	// constant exists so an adapter is never tempted to call an estimate
	// "computed".
	TokenComputed
	// TokenEstimated means a heuristic produced the number. An estimator note
	// naming the heuristic is mandatory; [Report.Validate] rejects reports
	// where it is missing.
	TokenEstimated
	// TokenResidual means the number was obtained by subtracting attributed
	// costs from a measured anchor. It inherits the error of every estimate it
	// subtracted, so it is never treated as stronger than an estimate.
	TokenResidual
	// TokenUnknown means no number exists. It renders as an em dash and must
	// never be summed as zero.
	TokenUnknown
)

var (
	tokenProvNames = map[TokenProv]string{
		TokenProviderMeasured: "provider-measured",
		TokenComputed:         "computed",
		TokenEstimated:        "estimated",
		TokenResidual:         "residual",
		TokenUnknown:          "unknown",
	}
	tokenProvByName = invertEnum(tokenProvNames)
)

// String returns the wire/display name.
func (p TokenProv) String() string { return enumName(p, tokenProvNames, "unknown") }

// Short returns the compact badge form rendered in dense list rows.
func (p TokenProv) Short() string {
	switch p {
	case TokenProviderMeasured:
		return "measured"
	case TokenComputed:
		return "computed"
	case TokenEstimated:
		return "~est"
	case TokenResidual:
		return "residual"
	default:
		return "—"
	}
}

// MarshalJSON encodes the value as its wire name.
func (p TokenProv) MarshalJSON() ([]byte, error) { return marshalEnum(p, tokenProvNames) }

// UnmarshalJSON decodes the value from its wire name.
func (p *TokenProv) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, tokenProvByName, p, "token provenance")
}

// tokenProvRank orders token provenance strongest (0) to weakest. The ordering
// is the one claim the package makes about relative trust:
//
//	provider-measured < computed < estimated < residual < unknown
//
// A residual sits below an estimate on purpose. It is defined as
// anchor − Σ(attributed), so its error is at least the combined error of every
// estimate subtracted from the anchor; it can never be more trustworthy than
// its worst input.
func tokenProvRank(p TokenProv) int {
	switch p {
	case TokenProviderMeasured:
		return 0
	case TokenComputed:
		return 1
	case TokenEstimated:
		return 2
	case TokenResidual:
		return 3
	default:
		return 4
	}
}

// weakestTokenProv returns the less trustworthy of two token provenances.
func weakestTokenProv(a, b TokenProv) TokenProv {
	if tokenProvRank(a) >= tokenProvRank(b) {
		return a
	}
	return b
}

// Confidence is the single display grade a renderer colours by. It is derived,
// never stored: [Badge.Confidence] collapses the two provenance axes onto it so
// that exactly one place in the codebase decides how the axes combine.
type Confidence uint8

const (
	// ConfidenceHigh: bytes are verbatim and the number came from the provider.
	ConfidenceHigh Confidence = iota
	// ConfidenceMedium: one axis is derived (reconstructed bytes, or an
	// estimated/residual number) but both axes exist.
	ConfidenceMedium
	// ConfidenceLow: an axis is missing entirely (absent text or unknown
	// tokens).
	ConfidenceLow
)

var (
	confidenceNames = map[Confidence]string{
		ConfidenceHigh:   "high",
		ConfidenceMedium: "medium",
		ConfidenceLow:    "low",
	}
	confidenceByName = invertEnum(confidenceNames)
)

// String returns the wire/display name.
func (c Confidence) String() string { return enumName(c, confidenceNames, "low") }

// MarshalJSON encodes the value as its wire name.
func (c Confidence) MarshalJSON() ([]byte, error) { return marshalEnum(c, confidenceNames) }

// UnmarshalJSON decodes the value from its wire name.
func (c *Confidence) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, confidenceByName, c, "confidence")
}

// Badge is the rendered form of the two provenance axes. It is the single
// collapse point in the package: L2 and L3 views print both axes, dense rows
// print [Badge.Short], and colour comes from [Badge.Confidence]. No renderer
// may re-derive any of this.
type Badge struct {
	Text  TextProv  `json:"text"`
	Token TokenProv `json:"token"`
}

// Short renders the compact "CAPT/~est" form used in dense rows.
func (b Badge) Short() string { return b.Text.Short() + "/" + b.Token.Short() }

// Confidence collapses both axes to one display grade. The weaker axis wins:
// verbatim bytes do not rescue an unknown number, and a measured number does
// not rescue text we cannot show.
func (b Badge) Confidence() Confidence {
	textLow := b.Text == TextAbsent
	tokenLow := b.Token == TokenUnknown
	if textLow || tokenLow {
		return ConfidenceLow
	}
	if b.Text == TextCaptured && b.Token == TokenProviderMeasured {
		return ConfidenceHigh
	}
	return ConfidenceMedium
}

// Origin records who put an item into the context window. It drives both the
// default sort (things the user controls first) and whether a [Lever] exists
// at all.
type Origin uint8

const (
	// OriginHarnessBuiltin: shipped inside the agent binary. Immovable.
	OriginHarnessBuiltin Origin = iota
	// OriginUserConfig: the user's own global configuration or home directory.
	OriginUserConfig
	// OriginProject: a file inside the project tree (or an ancestor of it).
	OriginProject
	// OriginPlugin: contributed by a harness plugin or extension.
	OriginPlugin
	// OriginMCPServer: contributed by an MCP server's instructions or schemas.
	OriginMCPServer
	// OriginAgentDeck: wired in by agent-deck itself — the [mcps.*] catalog,
	// `agent-deck mcp attach`, group settings. These are the items agent-deck
	// can hand the user an exact command for, which no other tool can do.
	OriginAgentDeck
)

var (
	originNames = map[Origin]string{
		OriginHarnessBuiltin: "harness-builtin",
		OriginUserConfig:     "user-config",
		OriginProject:        "project",
		OriginPlugin:         "plugin",
		OriginMCPServer:      "mcp-server",
		OriginAgentDeck:      "agent-deck",
	}
	originByName = invertEnum(originNames)
)

// String returns the wire/display name.
func (o Origin) String() string { return enumName(o, originNames, "harness-builtin") }

// MarshalJSON encodes the value as its wire name.
func (o Origin) MarshalJSON() ([]byte, error) { return marshalEnum(o, originNames) }

// UnmarshalJSON decodes the value from its wire name.
func (o *Origin) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, originByName, o, "origin")
}

// LeverKind says what the user can actually do about an item.
type LeverKind uint8

const (
	// LeverImmovable: nothing the user can do. Rendered dimmed with a lock.
	LeverImmovable LeverKind = iota
	// LeverEditFile: edit (or trim a section of) a file on disk.
	LeverEditFile
	// LeverDeleteDir: remove a directory, e.g. an unused skill.
	LeverDeleteDir
	// LeverRunCommand: run an exact agent-deck command.
	LeverRunCommand
)

var (
	leverKindNames = map[LeverKind]string{
		LeverImmovable:  "immovable",
		LeverEditFile:   "edit-file",
		LeverDeleteDir:  "delete-dir",
		LeverRunCommand: "run-command",
	}
	leverKindByName = invertEnum(leverKindNames)
)

// String returns the wire/display name.
func (k LeverKind) String() string { return enumName(k, leverKindNames, "immovable") }

// MarshalJSON encodes the value as its wire name.
func (k LeverKind) MarshalJSON() ([]byte, error) { return marshalEnum(k, leverKindNames) }

// UnmarshalJSON decodes the value from its wire name.
func (k *LeverKind) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, leverKindByName, k, "lever kind")
}

// Actionable reports whether this lever gives the user something to do. The
// default item sort puts actionable items first, then orders by cost, so the
// top of the list answers "what can I remove that buys me the most".
func (k LeverKind) Actionable() bool { return k != LeverImmovable }

// Lever is the concrete action available for an item: a file to edit, a
// directory to delete, or a command to run. It is copyable from the UI with a
// single keystroke; v1 never mutates anything itself.
type Lever struct {
	Kind LeverKind `json:"kind"`
	// Path is absolute when set.
	Path string `json:"path,omitempty"`
	// LineRange optionally narrows the lever to a section of Path. Both bounds
	// are 1-indexed and inclusive; the zero value means "the whole file".
	LineRange [2]int `json:"line_range,omitempty"`
	// Command is the exact shell command to run, for LeverRunCommand.
	Command string `json:"command,omitempty"`
	// Why explains, in one clause, what put this item here — e.g. "enabled by
	// enabledPlugins.telegram in ~/.claude/settings.json".
	Why string `json:"why,omitempty"`
}

// Payload returns the text the UI copies for this lever: the command for
// LeverRunCommand, otherwise the path.
func (l Lever) Payload() string {
	if l.Kind == LeverRunCommand {
		return l.Command
	}
	return l.Path
}

// ImmovableLever builds the lever for content the user cannot influence.
func ImmovableLever(why string) Lever { return Lever{Kind: LeverImmovable, Why: why} }

// FileLever builds an edit-this-file lever.
func FileLever(path, why string) Lever {
	return Lever{Kind: LeverEditFile, Path: path, Why: why}
}

// DirLever builds a delete-this-directory lever.
func DirLever(path, why string) Lever {
	return Lever{Kind: LeverDeleteDir, Path: path, Why: why}
}

// CommandLever builds a run-this-command lever.
func CommandLever(command, why string) Lever {
	return Lever{Kind: LeverRunCommand, Command: command, Why: why}
}

// LoadState distinguishes what an item costs right now from what it could cost.
// It is the distinction that stops a user deleting the wrong thing: a listed
// skill costs its catalogue line today, not the body nobody invoked.
type LoadState uint8

const (
	// LoadedNow: the content is in the fixed prefix right now.
	LoadedNow LoadState = iota
	// OnDemand: only a name and description are in the prefix; the body loads
	// when the agent invokes it.
	OnDemand
	// Available: it exists on disk and nothing put it in the prefix. Its cost
	// today is a certain zero when that absence was established, and an
	// explicit unknown when it could not be — an unread file and a file known
	// to be unloaded are the same state on disk and different facts on screen.
	Available
)

var (
	loadStateNames = map[LoadState]string{
		LoadedNow: "loaded",
		OnDemand:  "on-demand",
		Available: "available",
	}
	loadStateByName = invertEnum(loadStateNames)
)

// String returns the wire/display name.
func (s LoadState) String() string { return enumName(s, loadStateNames, "available") }

// MarshalJSON encodes the value as its wire name.
func (s LoadState) MarshalJSON() ([]byte, error) { return marshalEnum(s, loadStateNames) }

// UnmarshalJSON decodes the value from its wire name.
func (s *LoadState) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, loadStateByName, s, "load state")
}

// Load is an item's cost, split into what it costs now and what it would cost
// if fully loaded.
//
// Only Actual is ever summed. Potential is rendered in a dimmed second column
// and is excluded from [Report.FixedTotal] and every [Category.Total];
// [Report.Validate] enforces that.
type Load struct {
	State LoadState `json:"state"`
	// Actual is what this item costs in the fixed prefix right now.
	Actual TokenCount `json:"actual"`
	// Potential is what it would cost if fully loaded. It is nil when unknown,
	// and nil when it would equal Actual — a redundant Potential is noise, and
	// [Report.Validate] rejects one.
	Potential *TokenCount `json:"potential,omitempty"`
}

// LoadedNowCost builds the Load of content that is in the prefix right now and
// has no larger form.
func LoadedNowCost(actual TokenCount) Load {
	return Load{State: LoadedNow, Actual: actual}
}

// OnDemandCost builds the Load of content whose name and description are in the
// prefix while its body loads only on invocation.
func OnDemandCost(actual TokenCount, potential TokenCount) Load {
	l := Load{State: OnDemand, Actual: actual}
	if p, ok := potential.Value(); ok {
		if a, aok := actual.Value(); !aok || a != p {
			l.Potential = &potential
		}
	}
	return l
}

// AvailableCost builds the Load of content that exists on disk and is not
// loaded. Its actual cost is a measured, certain zero — nothing was sent — so
// it is safe to sum, unlike an unknown.
func AvailableCost(potential TokenCount) Load {
	l := Load{State: Available, Actual: Measured(0, "not loaded into the prefix")}
	if _, ok := potential.Value(); ok {
		l.Potential = &potential
	}
	return l
}

// AvailableUnknownCost builds the Load of content that exists on disk and could
// not be shown to be absent from the prefix.
//
// It is the honest sibling of [AvailableCost]. That constructor's certain zero
// is only earned when the absence was established — the catalogue was read, the
// injected block was fully explained. When it was not, the zero is a claim about
// content nobody looked at, and a summable zero is the most dangerous shape an
// unknown can take: it reads as a measurement and disappears into a total.
// reason must say why the cost could not be established.
func AvailableUnknownCost(reason string, potential TokenCount) Load {
	l := Load{State: Available, Actual: UnknownTokens(reason)}
	if _, ok := potential.Value(); ok {
		l.Potential = &potential
	}
	return l
}
