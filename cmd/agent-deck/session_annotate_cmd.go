package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Recall phase 1 (docs/recall.md): durable human intent about a session.
// `add` / `launch` accept the creation-time subset (--hint/--tag/--ticket/
// --why) and `session annotate` edits the full set afterwards. Every write
// lands in state.db's session_hints / session_tags, never in the transcript
// index, so a rebuild of the index cannot lose it.

// maxHintValueBytes bounds one hint value (a --note-stdin body included).
// Hints are a card, not a transcript: the same 8 KiB clip the recall index
// applies to a message body.
const maxHintValueBytes = 8 * 1024

// derivedPurposeLimit clips the purpose hint `launch` derives from the first
// line of its message.
const derivedPurposeLimit = 200

// Well-known hint keys. Any other identifier-shaped key is accepted too.
const (
	hintKeyPurpose  = "purpose"
	hintKeyTicket   = "ticket"
	hintKeyWhy      = "why"
	hintKeyDecision = "decision"
	hintKeyOutcome  = "outcome"
	hintKeyNote     = "note"
	hintKeyParent   = "parent"
)

// hintEdits is the parsed mutation set shared by `add`, `launch` and
// `session annotate`. Order inside a run does not matter: sets are applied
// before unsets so `--set-hint ticket=X --unset why` reads naturally.
type hintEdits struct {
	set        map[string]string
	setOrder   []string
	unset      []string
	addTags    []string
	removeTags []string
}

func newHintEdits() *hintEdits { return &hintEdits{set: map[string]string{}} }

func (h *hintEdits) empty() bool {
	return len(h.set) == 0 && len(h.unset) == 0 && len(h.addTags) == 0 && len(h.removeTags) == 0
}

// setHint records key=value; a later value for the same key wins, matching
// the UPSERT semantics of the table.
func (h *hintEdits) setHint(key, value string) error {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" {
		return fmt.Errorf("hint key is empty")
	}
	if strings.ContainsAny(key, " \t=") {
		return fmt.Errorf("hint key %q must be a single token without '='", key)
	}
	if value == "" {
		return fmt.Errorf("hint %q has an empty value (use --unset %s to remove it)", key, key)
	}
	if len(value) > maxHintValueBytes {
		return fmt.Errorf("hint %q value is %d bytes; the limit is %d", key, len(value), maxHintValueBytes)
	}
	if _, seen := h.set[key]; !seen {
		h.setOrder = append(h.setOrder, key)
	}
	h.set[key] = value
	return nil
}

func (h *hintEdits) setHintPair(kv string) error {
	key, value, ok := strings.Cut(kv, "=")
	if !ok {
		return fmt.Errorf("expected key=value, got %q", kv)
	}
	return h.setHint(key, value)
}

func validTag(tag string) (string, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" || strings.ContainsAny(tag, " \t=,") {
		return "", fmt.Errorf("tag %q must be a single token without '=' or ','", tag)
	}
	return tag, nil
}

// appendTagFlag is the flag.Func body shared by --tag and --remove-tag:
// validate the token, then append it to the given list.
func appendTagFlag(dst *[]string) func(string) error {
	return func(s string) error {
		tag, err := validTag(s)
		if err != nil {
			return err
		}
		*dst = append(*dst, tag)
		return nil
	}
}

// registerCreationHintFlags adds the creation-time subset to an `add` or
// `launch` flag set. All are repeatable Func flags so the remote creation
// catalog reports them as value-taking.
func registerCreationHintFlags(fs *flag.FlagSet) *hintEdits {
	h := newHintEdits()
	fs.Func("hint", "Durable hint key=value for recall (repeatable; e.g. --hint purpose=\"fix flaky auth test\")", h.setHintPair)
	fs.Func("tag", "Tag for recall (repeatable)", appendTagFlag(&h.addTags))
	fs.Func("ticket", "Ticket id hint (shorthand for --hint ticket=<id>)", func(s string) error { return h.setHint(hintKeyTicket, s) })
	fs.Func("why", "Why this session exists (shorthand for --hint why=<text>)", func(s string) error { return h.setHint(hintKeyWhy, s) })
	return h
}

// registerAnnotateFlags adds the full edit set for `session annotate`.
func registerAnnotateFlags(fs *flag.FlagSet) *hintEdits {
	h := registerCreationHintFlags(fs)
	fs.Func("set-hint", "Alias for --hint key=value (replaces the existing value)", h.setHintPair)
	fs.Func("unset", "Remove a hint key (repeatable)", func(s string) error {
		s = strings.TrimSpace(s)
		if s == "" {
			return fmt.Errorf("--unset needs a key")
		}
		h.unset = append(h.unset, s)
		return nil
	})
	fs.Func("remove-tag", "Remove a tag (repeatable)", appendTagFlag(&h.removeTags))
	fs.Func("decision", "Decision taken in this session (shorthand for --hint decision=<text>)", func(s string) error { return h.setHint(hintKeyDecision, s) })
	fs.Func("outcome", "Outcome of this session, e.g. worked|failed|abandoned (shorthand for --hint outcome=<text>)", func(s string) error { return h.setHint(hintKeyOutcome, s) })
	return h
}

// hintAuthor names who annotated: the agent's own instance id when run from
// inside a session, otherwise the OS user.
func hintAuthor() string {
	if id := strings.TrimSpace(os.Getenv("AGENTDECK_INSTANCE_ID")); id != "" {
		return "session:" + id
	}
	return strings.TrimSpace(os.Getenv("USER"))
}

// hintChange is one applied edit, echoed back in the output.
type hintChange struct {
	Op    string `json:"op"` // set|unset|tag|untag
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
	// Applied is false when the edit was a no-op (unsetting a missing key,
	// removing an absent tag).
	Applied bool `json:"applied"`
}

// apply writes the edits for one instance. It keeps going after a failed
// write and returns every error, so a partially applied run is reported
// rather than silently truncated.
func (h *hintEdits) apply(db *statedb.StateDB, instanceID, source string) ([]hintChange, []error) {
	var changes []hintChange
	var errs []error
	author := hintAuthor()
	for _, key := range h.setOrder {
		value := h.set[key]
		if err := db.SetSessionHint(statedb.HintScopeInstance, instanceID, key, value, source, author); err != nil {
			errs = append(errs, fmt.Errorf("set %s: %w", key, err))
			continue
		}
		changes = append(changes, hintChange{Op: "set", Key: key, Value: value, Applied: true})
	}
	for _, key := range h.unset {
		removed, err := db.UnsetSessionHint(statedb.HintScopeInstance, instanceID, key)
		if err != nil {
			errs = append(errs, fmt.Errorf("unset %s: %w", key, err))
			continue
		}
		changes = append(changes, hintChange{Op: "unset", Key: key, Applied: removed})
	}
	for _, tag := range h.addTags {
		if err := db.AddSessionTag(statedb.HintScopeInstance, instanceID, tag, source); err != nil {
			errs = append(errs, fmt.Errorf("tag %s: %w", tag, err))
			continue
		}
		changes = append(changes, hintChange{Op: "tag", Key: tag, Applied: true})
	}
	for _, tag := range h.removeTags {
		removed, err := db.RemoveSessionTag(statedb.HintScopeInstance, instanceID, tag)
		if err != nil {
			errs = append(errs, fmt.Errorf("remove-tag %s: %w", tag, err))
			continue
		}
		changes = append(changes, hintChange{Op: "untag", Key: tag, Applied: removed})
	}
	return changes, errs
}

// applyCreationHints is the `add` / `launch` hook: the session row exists,
// so hint failures are warnings on stderr rather than a failed creation.
// It returns the hints and tags now on the instance for the JSON output.
func applyCreationHints(storage *session.Storage, inst *session.Instance, explicit *hintEdits, auto map[string]string) (map[string]string, []string) {
	if storage == nil || inst == nil {
		return nil, nil
	}
	db := storage.GetDB()
	if db == nil {
		return nil, nil
	}
	// Auto-injected hints (parent, purpose derived from the launch message)
	// never override a value the caller typed.
	derived := newHintEdits()
	for key, value := range auto {
		if _, typed := explicit.set[key]; typed || value == "" {
			continue
		}
		_ = derived.setHint(key, value) // invalid derived values are simply skipped
	}
	_, errs := derived.apply(db, inst.ID, statedb.HintSourceDerived)
	warnHintErrors(errs)
	_, errs = explicit.apply(db, inst.ID, statedb.HintSourceCLICreate)
	warnHintErrors(errs)
	hints, tags, _ := readInstanceHints(db, inst.ID)
	return hints, tags
}

func warnHintErrors(errs []error) {
	for _, err := range errs {
		fmt.Fprintf(os.Stderr, "Warning: hint: %v\n", err)
	}
}

// firstLineClipped derives a purpose hint from a launch message: its first
// non-empty line, clipped, so every fan-out child carries what it was for.
func firstLineClipped(text string, limit int) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > limit {
			line = line[:limit]
		}
		return line
	}
	return ""
}

func readInstanceHints(db *statedb.StateDB, instanceID string) (map[string]string, []string, error) {
	rows, err := db.ListSessionHints(statedb.HintScopeInstance, instanceID)
	if err != nil {
		return nil, nil, err
	}
	hints := map[string]string{}
	for _, h := range rows {
		hints[h.Key] = h.Value
	}
	tagRows, err := db.ListSessionTags(statedb.HintScopeInstance, instanceID)
	if err != nil {
		return nil, nil, err
	}
	tags := []string{}
	for _, t := range tagRows {
		tags = append(tags, t.Tag)
	}
	return hints, tags, nil
}

// handleSessionAnnotate implements `agent-deck session annotate`.
func handleSessionAnnotate(profile string, args []string) {
	fs := flag.NewFlagSet("session annotate", flag.ExitOnError)
	edits := registerAnnotateFlags(fs)
	noteStdin := fs.Bool("note-stdin", false, "Read a free-text note from stdin and store it as the 'note' hint")
	self := fs.Bool("self", false, "Annotate the calling session (AGENTDECK_INSTANCE_ID or the current tmux session) instead of a named one")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session annotate <session> [options]")
		fmt.Println("       agent-deck session annotate --self [options]")
		fmt.Println()
		fmt.Println("Record durable intent about a session: what it is for, which ticket,")
		fmt.Println("why, what was decided and how it ended. Hints are single-valued (setting")
		fmt.Println("a key again replaces it); tags are a set. Everything is stored in the")
		fmt.Println("profile's state.db and survives any rebuild of the recall index.")
		fmt.Println("With no edit flags the current hints, tags and harness links are shown.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session annotate auth-fix --decision \"root cause was clock skew\" --outcome worked --tag clock-skew")
		fmt.Println("  agent-deck session annotate auth-fix --set-hint ticket=SB-413 --remove-tag flaky --unset why")
		fmt.Println("  agent-deck session annotate --self --note-stdin < summary.md")
		fmt.Println("  agent-deck session annotate auth-fix --json          # show hints, tags and links")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	identifier := strings.TrimSpace(fs.Arg(0))
	if *self {
		if identifier != "" {
			out.Error("--self cannot be combined with a session argument", ErrCodeInvalidOperation)
			os.Exit(1)
		}
		resolved, err := resolveSelfSessionID()
		if err != nil {
			out.Error(err.Error(), ErrCodeNotFound)
			os.Exit(2)
		}
		identifier = resolved
	}
	if identifier == "" {
		fs.Usage()
		os.Exit(1)
	}
	if fs.NArg() > 1 {
		out.Error(fmt.Sprintf("unexpected extra arguments: %s", strings.Join(fs.Args()[1:], " ")), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if *noteStdin {
		body, err := io.ReadAll(io.LimitReader(os.Stdin, maxHintValueBytes+1))
		if err != nil {
			out.Error(fmt.Sprintf("read note from stdin: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		if err := edits.setHint(hintKeyNote, string(body)); err != nil {
			out.Error(err.Error(), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
	}
	db := storage.GetDB()
	if db == nil {
		out.Error("session storage has no state database", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	changes, errs := edits.apply(db, inst.ID, statedb.HintSourceAnnotate)
	hints, tags, readErr := readInstanceHints(db, inst.ID)
	if readErr != nil {
		errs = append(errs, readErr)
	}
	links, _ := db.ListSessionLinks(inst.ID)
	if links == nil {
		links = []statedb.SessionLink{}
	}
	if changes == nil {
		changes = []hintChange{}
	}

	data := map[string]interface{}{
		"success":       len(errs) == 0,
		"session_id":    inst.ID,
		"session_title": inst.Title,
		"hints":         hints,
		"tags":          tags,
		"links":         links,
		"changes":       changes,
	}
	if len(errs) > 0 {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Error())
		}
		data["errors"] = msgs
		out.ErrorWithData(strings.Join(msgs, "; "), ErrCodeInvalidOperation, data)
		os.Exit(1)
	}

	var human strings.Builder
	if edits.empty() {
		fmt.Fprintf(&human, "Hints for '%s' (%s):\n", inst.Title, inst.ID)
	} else {
		fmt.Fprintf(&human, "Annotated '%s' (%s): %d change(s)\n", inst.Title, inst.ID, len(changes))
	}
	keys := make([]string, 0, len(hints))
	for k := range hints {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&human, "  %-10s %s\n", k+":", firstLineClipped(hints[k], 120))
	}
	if len(keys) == 0 {
		human.WriteString("  (no hints)\n")
	}
	if len(tags) > 0 {
		fmt.Fprintf(&human, "  tags:      %s\n", strings.Join(tags, ", "))
	}
	for _, l := range links {
		mark := ""
		if l.Authoritative {
			mark = " (authoritative)"
		}
		fmt.Fprintf(&human, "  link:      %s %s%s\n", l.Harness, l.NativeID, mark)
	}
	out.Print(human.String(), data)
}
