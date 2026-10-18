// Conductor identity: which conductor a session is.
//
// Historically every caller derived the conductor name with
// strings.TrimPrefix(Title, "conductor-"). The title is a display string that
// other tools rewrite (the voice daemon prefixed "🎙 " to the mic session) and
// that users rename, and the moment it changed the conductor silently lost its
// [conductors.<name>] settings and meta.json env (env_file ->
// TELEGRAM_STATE_DIR -> the Telegram plugin exited).
//
// The stable key is Instance.ConductorName, stamped by `conductor setup` and
// persisted in the tool_data extras zone (no schema migration). Old records
// without it fall back to the title, tolerating leading decoration, and
// lazily adopt the parsed name once it is confirmed by a matching meta.json so
// a later rename keeps working. ResolveConductorName is the single helper;
// call it wherever the question is "which conductor is this".
package session

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

const toolDataConductorNameKey = "conductor_name"

// conductorTitleDriftWarned dedupes the title-drift warning per instance ID so
// a status poll that resolves the name every tick logs it once per process.
var conductorTitleDriftWarned sync.Map

// ConductorNameFromTitle parses a conductor name out of a session title of the
// form "conductor-<name>". Leading decoration is skipped (see
// isConductorTitleDecoration: whitespace and emoji, such as the voice
// daemon's "🎙 "); the "conductor-" prefix itself is case-sensitive. Returns ""
// when the title is not a conductor title. This is only the fallback for
// records without a stored name; prefer Instance.ResolveConductorName.
//
// The decoration set is deliberately narrow: a title that parses here also
// keeps the conductor's TELEGRAM_* env (telegramStateDirStripExpr), so
// "Conductor-review" or "* conductor-notes" must stay ordinary sessions.
func ConductorNameFromTitle(title string) string {
	t := strings.TrimLeftFunc(title, isConductorTitleDecoration)
	name, ok := strings.CutPrefix(t, ConductorSessionTitlePrefix)
	if !ok {
		return ""
	}
	return strings.TrimRightFunc(name, unicode.IsSpace)
}

// isConductorTitleDecoration reports whether r may precede "conductor-" in a
// decorated conductor title: whitespace, emoji and other symbols (unicode So),
// the emoji skin-tone modifiers, the emoji variation selectors and the
// zero-width joiner that glue multi-codepoint emoji together. The rest of
// unicode Sk is excluded on purpose: it holds keyboard characters like "^"
// and "`" that must not turn an ordinary title into a conductor.
func isConductorTitleDecoration(r rune) bool {
	switch {
	case r == '\uFE0E', r == '\uFE0F', r == '\u200D':
		return true
	case r >= '\U0001F3FB' && r <= '\U0001F3FF':
		return true
	}
	return unicode.IsSpace(r) || unicode.Is(unicode.So, r)
}

// legacyConductorNameFold is the pre-identity title parse kept for the two
// call sites that historically matched case-insensitively (the transition
// notifier's conductor check and the [conductors.<name>.inbox] lookup):
// trim surrounding whitespace, lowercase, cut "conductor-". ok reports a
// prefix match even when the name is empty, as those sites did.
func legacyConductorNameFold(title string) (name string, ok bool) {
	return strings.CutPrefix(strings.ToLower(strings.TrimSpace(title)), ConductorSessionTitlePrefix)
}

// storedConductorName reads the persisted stable name under its lock (the
// lazy fill below can write it from a status/spawn goroutine). A stored name
// counts only on a row flagged IsConductor: an unflagged row is a worker, and
// a name stamped on one (the pre-#2708 adoption of any row that briefly held
// the exact "conductor-<name>" title) must not keep it resolving as the
// conductor after it is renamed back, so it is treated as absent.
func (i *Instance) storedConductorName() string {
	if !i.IsConductor {
		return ""
	}
	i.conductorNameMu.Lock()
	defer i.conductorNameMu.Unlock()
	return i.ConductorName
}

// ResolveConductorName returns the conductor this session is, or "" for a
// non-conductor session. Order:
//
//  1. the stored ConductorName (stable across renames and decoration);
//  2. for a session flagged IsConductor, the conductor dir it runs in
//     (ConductorNameFromPath): setup creates the conductor in
//     conductor/<name>, so the dir names it even when the title does not
//     ("conductor-team-ops" for conductor "ops"). Adopted like 3. An
//     unflagged session is never resolved or adopted by its dir: a worker
//     launched inside the conductor dir is not the conductor;
//  3. the title fallback (ConductorNameFromTitle) for old records. When a
//     meta.json exists for the parsed name and the session is flagged
//     IsConductor, the name is adopted as the stable key in memory; the next
//     save persists it. An unflagged session (a legacy row written before
//     the flag existed, or a worker carrying a conductor-shaped title) still
//     resolves by its current title (env, settings) but is never adopted, so
//     renaming it away ends the match.
//
// A stored name whose title no longer parses to it is logged once.
//
// It reads Title without i.mu on purpose: it is reachable from inside i.mu
// critical sections (UpdateHookStatus -> sessionHasConversationData ->
// GetClaudeConfigDirForInstance), and sync.RWMutex is not reentrant, so
// taking the read lock here would deadlock the hook path. Callers on another
// goroutine than the title writers, not holding i.mu, use
// ResolveConductorNameThreadSafe.
func (i *Instance) ResolveConductorName() string {
	if i == nil {
		return ""
	}
	return i.resolveConductorName(i.Title)
}

// ResolveConductorNameThreadSafe is ResolveConductorName with the title read
// under its lock (GetTitleThreadSafe), for status pollers, the web snapshot
// and the TUI, which race title syncs (SetTitleThreadSafe). Never call it
// while holding i.mu.
func (i *Instance) ResolveConductorNameThreadSafe() string {
	if i == nil {
		return ""
	}
	return i.resolveConductorName(i.GetTitleThreadSafe())
}

func (i *Instance) resolveConductorName(title string) string {
	if name := i.storedConductorName(); name != "" {
		i.warnConductorTitleDrift(name, title)
		return name
	}
	if i.IsConductor {
		if name := ConductorNameFromPath(i.ProjectPath); name != "" {
			i.adoptConductorName(name)
			return name
		}
	}
	name := ConductorNameFromTitle(title)
	if name == "" {
		return ""
	}
	// Adopt only a confirmed conductor: the IsConductor flag guards against a
	// worker that merely carries a conductor-shaped title (even the exact one,
	// #2708), and the meta.json check against persisting a guess for a
	// conductor that was never set up.
	if i.IsConductor && IsConductorSetup(name) {
		i.adoptConductorName(name)
	}
	return name
}

// adoptConductorName stamps name as the stable key if none is stored yet.
// Callers adopt only on a row flagged IsConductor (storedConductorName
// ignores a name on an unflagged row anyway).
func (i *Instance) adoptConductorName(name string) {
	i.conductorNameMu.Lock()
	if i.ConductorName == "" {
		i.ConductorName = name
	}
	i.conductorNameMu.Unlock()
}

// ConductorNameFromPath returns the conductor whose dir path is
// (conductor/<name> with a meta.json), or "". The name is the one meta.json
// records, so an alias dir (conductor/<alias> -> <name>) still names the real
// conductor; it must lead back to path (os.SameFile, so symlinked bases
// compare equal). Callers gate it on IsConductor: a worker launched inside
// the conductor dir is not the conductor.
func ConductorNameFromPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	// The session's project path is stored data: only an absolute, clean
	// path with no ".." is compared (and statted) against the conductor dir.
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || strings.Contains(path, "..") {
		return ""
	}
	name := filepath.Base(path)
	meta, err := LoadConductorMeta(name)
	if err != nil {
		return ""
	}
	if meta.Name != "" {
		name = meta.Name
	}
	dir, err := ConductorNameDir(name)
	if err != nil {
		return ""
	}
	want, err := os.Stat(dir)
	if err != nil {
		return ""
	}
	got, err := os.Stat(path)
	if err != nil || !os.SameFile(want, got) || !IsConductorSetup(name) {
		return ""
	}
	return name
}

// ConductorTitleDrift reports the session's conductor name and whether its
// title no longer names it (renamed to something that does not parse as
// "conductor-<name>"). A decorated title for the same name is not drift.
// Used by `conductor status` to surface the mismatch.
func (i *Instance) ConductorTitleDrift() (name string, drifted bool) {
	if i == nil {
		return "", false
	}
	title := i.GetTitleThreadSafe()
	name = i.resolveConductorName(title)
	if name == "" {
		return "", false
	}
	return name, ConductorNameFromTitle(title) != name
}

func (i *Instance) warnConductorTitleDrift(name, title string) {
	if ConductorNameFromTitle(title) == name {
		return
	}
	if _, loaded := conductorTitleDriftWarned.LoadOrStore(i.ID, struct{}{}); loaded {
		return
	}
	sessionLog.Warn("conductor_title_drift",
		slog.String("session_id", logging.SanitizeValue(i.ID)),
		// The inline ReplaceAll calls are no-ops after SanitizeValue; they are
		// the newline barrier CodeQL go/log-injection recognises at the sink.
		slog.String("title", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(title), "\n", ""), "\r", "")),
		slog.String("conductor", logging.SanitizeValue(name)),
		slog.String("detail", "title no longer matches the conductor name; settings still resolve by the stored name"))
}

// FindConductorInstance returns the session of the named conductor in
// instances, or nil. A stored ConductorName (on a row flagged IsConductor,
// see storedConductorName) wins; then a session flagged IsConductor running
// in the conductor's dir; then the canonical title;
// then a decorated title that parses to name on a session flagged
// IsConductor (a worker that merely carries a decorated conductor-shaped
// title must not be taken over by setup or removed by teardown). A session
// whose stored name is a different conductor never matches by title.
func FindConductorInstance(instances []*Instance, name string) *Instance {
	if name == "" {
		return nil
	}
	for _, inst := range instances {
		if inst != nil && inst.storedConductorName() == name {
			return inst
		}
	}
	for _, inst := range instances {
		if inst != nil && inst.IsConductor && inst.storedConductorName() == "" && ConductorNameFromPath(inst.ProjectPath) == name {
			return inst
		}
	}
	title := ConductorSessionTitle(name)
	var decorated *Instance
	for _, inst := range instances {
		if inst == nil || inst.storedConductorName() != "" {
			continue
		}
		instTitle := inst.GetTitleThreadSafe()
		if instTitle == title {
			return inst
		}
		if decorated == nil && inst.IsConductor && ConductorNameFromTitle(instTitle) == name {
			decorated = inst
		}
	}
	return decorated
}

// ConductorSessionIDs returns the IDs of every session in instances that
// resolves as the named conductor. More than one is a duplicate (an old row
// plus one a setup re-run created): both would own the bot token, so a second
// poller races the first. `conductor status` warns about it.
func ConductorSessionIDs(instances []*Instance, name string) []string {
	var ids []string
	for _, inst := range instances {
		if inst != nil && name != "" && inst.ResolveConductorNameThreadSafe() == name {
			ids = append(ids, inst.ID)
		}
	}
	return ids
}

// IsConductorNamed reports whether this session is the named conductor, judged
// on title (which the caller reads under its own locking, e.g.
// GetTitleThreadSafe). A stored name decides alone; otherwise the conductor
// dir of a flagged session, or the title, must name it.
func (i *Instance) IsConductorNamed(name, title string) bool {
	if i == nil || name == "" {
		return false
	}
	if stored := i.storedConductorName(); stored != "" {
		return stored == name
	}
	if i.IsConductor && ConductorNameFromPath(i.ProjectPath) == name {
		return true
	}
	return ConductorNameFromTitle(title) == name
}

// WriteConductorNameToToolData merges conductor_name into the tool_data blob.
// An empty name leaves the blob untouched rather than deleting the key: the
// identity is only ever stamped (setup, lazy fill), never cleared, so a save
// from a copy that never resolved it must not erase it, and
// MergeToolDataExtras then carries the stored value forward.
func WriteConductorNameToToolData(td json.RawMessage, name string) json.RawMessage {
	if name == "" {
		return td
	}
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	raw, err := json.Marshal(name)
	if err != nil {
		return td
	}
	m[toolDataConductorNameKey] = raw
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

// ReadConductorNameFromToolData extracts conductor_name from the blob.
// Returns "" for missing, malformed, or legacy rows (title fallback applies).
func ReadConductorNameFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var blob struct {
		ConductorName string `json:"conductor_name"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.ConductorName
}
