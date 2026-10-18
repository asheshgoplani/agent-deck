package session

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupConductorIdentityFixture isolates HOME, writes a config.toml with a
// [conductors.personal.claude] block (env_file + config_dir), and creates the
// "personal" conductor with a meta.json env_file. Returns the tmp home.
//
// This is the shape of the owner-reported failure: a voice daemon decorated
// the conductor title ("🎙 conductor-personal") and every lookup keyed on
// strings.TrimPrefix(Title, "conductor-") stopped finding the conductor, so
// env_file (TELEGRAM_STATE_DIR) and config_dir silently dropped.
func setupConductorIdentityFixture(t *testing.T) string {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	cfgPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	cfg := `
[conductors.personal.claude]
config_dir = "~/.claude-personal"
env_file = "~/personal-claude.env"
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	if err := SetupConductor("personal", "default", true, true, "", "", "", "", nil, "~/personal-meta.env"); err != nil {
		t.Fatalf("SetupConductor: %v", err)
	}
	return tmpHome
}

func assertConductorSettingsKept(t *testing.T, inst *Instance, tmpHome string) {
	t.Helper()

	metaEnv := filepath.Join(tmpHome, "personal-meta.env")
	if got := inst.getConductorEnv(true); !strings.Contains(got, metaEnv) {
		t.Errorf("getConductorEnv for title %q lost meta.json env_file %s; got %q", inst.Title, metaEnv, got)
	}

	envCmd := inst.buildEnvSourceCommand()
	if !strings.Contains(envCmd, metaEnv) {
		t.Errorf("buildEnvSourceCommand for title %q lost meta.json env_file %s; got %q", inst.Title, metaEnv, envCmd)
	}
	claudeEnv := filepath.Join(tmpHome, "personal-claude.env")
	if !strings.Contains(envCmd, claudeEnv) {
		t.Errorf("buildEnvSourceCommand for title %q lost [conductors.personal.claude].env_file %s; got %q", inst.Title, claudeEnv, envCmd)
	}

	if strings.Contains(envCmd, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("buildEnvSourceCommand for title %q strips TELEGRAM_* from the conductor (the bot owner); got %q", inst.Title, envCmd)
	}

	wantDir := filepath.Join(tmpHome, ".claude-personal")
	if got := GetClaudeConfigDirForInstance(inst); got != wantDir {
		t.Errorf("GetClaudeConfigDirForInstance for title %q = %q, want %q", inst.Title, got, wantDir)
	}
	if !inst.ConductorClearOnCompact() {
		t.Errorf("ConductorClearOnCompact for title %q = false, want true (meta default)", inst.Title)
	}
}

// TestConductorIdentity_DecoratedTitleKeepsSettings: an emoji/symbol-decorated
// title on an old record (no stable conductor name stored) still resolves to
// the conductor through the tolerant title fallback.
func TestConductorIdentity_DecoratedTitleKeepsSettings(t *testing.T) {
	for _, title := range []string{"🎙 conductor-personal", "🎙️conductor-personal", "👩\u200d💻 conductor-personal", "  conductor-personal"} {
		t.Run(title, func(t *testing.T) {
			tmpHome := setupConductorIdentityFixture(t)
			inst := &Instance{ID: "decorated-" + title, Title: title, Tool: "claude", ProjectPath: tmpHome}
			assertConductorSettingsKept(t, inst, tmpHome)
		})
	}
}

// TestConductorIdentity_OldRecordResolvesViaTitle: a legacy record with the
// plain "conductor-<name>" title and no stored name keeps working, and the
// lookup lazily fills the stable name because a matching meta.json exists.
func TestConductorIdentity_OldRecordResolvesViaTitle(t *testing.T) {
	tmpHome := setupConductorIdentityFixture(t)
	inst := &Instance{ID: "old-record", Title: "conductor-personal", Tool: "claude", ProjectPath: tmpHome, IsConductor: true}
	assertConductorSettingsKept(t, inst, tmpHome)
	if got := inst.ResolveConductorName(); got != "personal" {
		t.Fatalf("ResolveConductorName = %q, want personal", got)
	}
	if inst.ConductorName != "personal" {
		t.Fatalf("stable conductor name not lazily filled: %q", inst.ConductorName)
	}

	// No meta.json for the parsed name: resolve (legacy behavior) but do not
	// persist a guess.
	stray := &Instance{ID: "stray", Title: "conductor-ghost", Tool: "claude", IsConductor: true}
	if got := stray.ResolveConductorName(); got != "ghost" {
		t.Fatalf("ResolveConductorName(stray) = %q, want ghost", got)
	}
	if stray.ConductorName != "" {
		t.Fatalf("stable name filled without a matching meta.json: %q", stray.ConductorName)
	}

	// An unflagged row with exactly the canonical title (a legacy row from
	// before the IsConductor flag, or a worker renamed to it) resolves by
	// that title but is not adopted (#2708): only the flag confirms a
	// conductor.
	legacy := &Instance{ID: "legacy-unflagged", Title: "conductor-personal", Tool: "claude"}
	if got := legacy.ResolveConductorName(); got != "personal" {
		t.Fatalf("ResolveConductorName(legacy) = %q, want personal", got)
	}
	if legacy.ConductorName != "" {
		t.Fatalf("unflagged exact-title row adopted a conductor identity: %q", legacy.ConductorName)
	}

	// A decorated title on an unflagged session resolves (env, settings) but
	// is never persisted as an identity: only the exact title or the flag
	// confirm a conductor.
	worker := &Instance{ID: "worker", Title: "🎙 conductor-personal", Tool: "claude"}
	if got := worker.ResolveConductorName(); got != "personal" {
		t.Fatalf("ResolveConductorName(worker) = %q, want personal", got)
	}
	if worker.ConductorName != "" {
		t.Fatalf("unflagged decorated session adopted a conductor identity: %q", worker.ConductorName)
	}
}

// TestConductorIdentity_RenamedConductorKeepsSettings: once the stable name is
// stored, the title can change to anything and the conductor keeps its
// settings; the drift is reported.
func TestConductorIdentity_RenamedConductorKeepsSettings(t *testing.T) {
	tmpHome := setupConductorIdentityFixture(t)
	inst := &Instance{ID: "renamed", Title: "my assistant", Tool: "claude", ProjectPath: tmpHome, IsConductor: true, ConductorName: "personal"}
	assertConductorSettingsKept(t, inst, tmpHome)

	name, drifted := inst.ConductorTitleDrift()
	if name != "personal" || !drifted {
		t.Fatalf("ConductorTitleDrift = (%q, %v), want (personal, true)", name, drifted)
	}
	// A decorated title for the same name is not drift.
	inst.Title = "🎙 conductor-personal"
	if _, drifted := inst.ConductorTitleDrift(); drifted {
		t.Fatal("decorated title for the stored name reported as drift")
	}
}

func TestConductorIdentity_NonConductorTitles(t *testing.T) {
	// Only whitespace and emoji count as decoration and the prefix is
	// case-sensitive: these stay ordinary sessions (and keep the TELEGRAM_*
	// strip a non-conductor gets).
	for _, title := range []string{"", "main", "conductor-", "my conductor-x", "xconductor-foo", "🎙 conductor-",
		"* conductor-x", "[*] conductor-ops-two", "Conductor-ops", "Conductor-review", "* conductor-notes", "CONDUCTOR-x"} {
		if got := ConductorNameFromTitle(title); got != "" {
			t.Errorf("ConductorNameFromTitle(%q) = %q, want empty", title, got)
		}
	}
	for title, want := range map[string]string{
		"conductor-personal":       "personal",
		"🎙 conductor-personal":     "personal",
		"conductor-a.b_c-d":        "a.b_c-d",
		"👩\u200d💻 conductor-ops":   "ops",
		"\u2b50\ufe0f conductor-x": "x",
	} {
		if got := ConductorNameFromTitle(title); got != want {
			t.Errorf("ConductorNameFromTitle(%q) = %q, want %q", title, got, want)
		}
	}
}

// TestConductorIdentity_SQLiteRoundTrip: the stable name survives a
// SaveWithGroups -> LoadWithGroups cycle, and a row without it still loads.
func TestConductorIdentity_SQLiteRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("renamed title", "/tmp")
	inst.Tool = "shell"
	inst.IsConductor = true
	inst.ConductorName = "personal"
	legacy := NewInstance("conductor-legacy", "/tmp")
	legacy.Tool = "shell"

	all := []*Instance{inst, legacy}
	if err := storage.SaveWithGroups(all, NewGroupTreeWithGroups(all, nil)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	byID := map[string]*Instance{}
	for _, l := range loaded {
		byID[l.ID] = l
	}
	if got := byID[inst.ID]; got == nil || got.ConductorName != "personal" {
		t.Fatalf("ConductorName not preserved across SQLite round-trip (found=%v)", got != nil)
	}
	if got := byID[legacy.ID]; got == nil || got.ConductorName != "" {
		t.Fatalf("legacy row gained a conductor name (found=%v)", got != nil)
	}

	// A save that does not carry the name (an older binary, or an in-memory
	// copy that never resolved it) must not erase the stored identity.
	reloaded := byID[inst.ID]
	reloaded.ConductorName = ""
	if err := storage.SaveWithGroups(loaded, NewGroupTreeWithGroups(loaded, nil)); err != nil {
		t.Fatalf("SaveWithGroups (no name): %v", err)
	}
	loaded2, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups (2): %v", err)
	}
	for _, l := range loaded2 {
		if l.ID == inst.ID && l.ConductorName != "personal" {
			t.Fatalf("stored conductor identity erased by a save without it: %q", l.ConductorName)
		}
	}
}

// TestFindConductorInstance prefers the stable name, then the exact title,
// then a decorated title for the same name.
func TestFindConductorInstance(t *testing.T) {
	other := &Instance{ID: "o", Title: "conductor-other"}
	decorated := &Instance{ID: "d", Title: "🎙 conductor-personal", IsConductor: true}
	if got := FindConductorInstance([]*Instance{other, decorated}, "personal"); got != decorated {
		t.Fatalf("decorated title not found: %+v", got)
	}
	// An unflagged session with a decorated conductor-shaped title is a
	// worker, not the conductor: setup must not take it over and teardown
	// --remove must not delete it.
	worker := &Instance{ID: "w", Title: "🎙 conductor-personal"}
	if got := FindConductorInstance([]*Instance{other, worker}, "personal"); got != nil {
		t.Fatalf("unflagged decorated worker matched as the conductor: %+v", got)
	}
	if got := FindConductorInstance([]*Instance{worker, decorated}, "personal"); got != decorated {
		t.Fatalf("flagged decorated conductor should win over an unflagged worker: %+v", got)
	}
	// A legacy unflagged row with the exact canonical title still matches.
	legacy := &Instance{ID: "l", Title: "conductor-personal"}
	if got := FindConductorInstance([]*Instance{worker, legacy}, "personal"); got != legacy {
		t.Fatalf("legacy canonical-title row not found: %+v", got)
	}
	renamed := &Instance{ID: "r", Title: "something else", IsConductor: true, ConductorName: "personal"}
	plain := &Instance{ID: "p", Title: "conductor-personal"}
	if got := FindConductorInstance([]*Instance{plain, renamed}, "personal"); got != renamed {
		t.Fatalf("stable name should win over title: %+v", got)
	}
	if got := FindConductorInstance([]*Instance{other}, "personal"); got != nil {
		t.Fatalf("unexpected match: %+v", got)
	}
}

// TestConductorIdentity_InboxConfigFollowsConductor: the
// [conductors.<name>.inbox] override applies to a decorated title and, via
// the instance form, to a renamed conductor.
func TestConductorIdentity_InboxConfigFollowsConductor(t *testing.T) {
	setupConductorIdentityFixture(t)
	cfgPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[conductors.personal.inbox]\nmax_text_bytes = 1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()

	if got := ResolveInboxConfig("🎙 conductor-personal").MaxTextBytes; got != 1234 {
		t.Errorf("ResolveInboxConfig(decorated).MaxTextBytes = %d, want 1234", got)
	}
	renamed := &Instance{ID: "inbox-renamed", Title: "my assistant", IsConductor: true, ConductorName: "personal"}
	if got := ResolveInboxConfigForInstance(renamed).MaxTextBytes; got != 1234 {
		t.Errorf("ResolveInboxConfigForInstance(renamed).MaxTextBytes = %d, want 1234", got)
	}
	if got := ResolveInboxConfigForInstance(nil).MaxTextBytes; got != 0 {
		t.Errorf("ResolveInboxConfigForInstance(nil).MaxTextBytes = %d, want 0", got)
	}
}

// TestConductorIdentity_TelegramStripNotWidened: a title that only looks
// conductor-ish (wrong case, a leading "*" bullet or an ASCII-ish modifier
// symbol such as "^" or "`") is an ordinary session and
// keeps the TELEGRAM_* strip; the decoration fallback must not hand it the
// bot owner's exemption.
func TestConductorIdentity_TelegramStripNotWidened(t *testing.T) {
	setupConductorIdentityFixture(t)
	for _, title := range []string{"Conductor-review", "* conductor-notes", "Conductor-personal", "[x] conductor-personal", "`conductor-notes`", "^conductor-personal", "´conductor-personal"} {
		inst := &Instance{ID: "strip-" + title, Title: title, Tool: "claude"}
		if got := telegramStateDirStripExpr(inst); got == "" {
			t.Errorf("telegramStateDirStripExpr(%q) = \"\", want the TELEGRAM_* strip for a non-conductor", title)
		}
	}
	for _, title := range []string{"🎙 conductor-personal", "👋🏽 conductor-personal"} {
		conductor := &Instance{ID: "strip-conductor-" + title, Title: title, Tool: "claude"}
		if got := telegramStateDirStripExpr(conductor); got != "" {
			t.Errorf("telegramStateDirStripExpr(%q) = %q, want no strip", title, got)
		}
	}
}

// TestConductorIdentity_CaseInsensitiveCallSitesPreserved: the two call sites
// that matched conductor titles case-insensitively before the stable key
// (transition notifier conductor check, [conductors.<name>.inbox] lookup)
// keep doing so even though the generic resolver is now case-sensitive.
func TestConductorIdentity_CaseInsensitiveCallSitesPreserved(t *testing.T) {
	setupConductorIdentityFixture(t)
	cfgPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[conductors.personal.inbox]\nmax_text_bytes = 1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()

	for _, title := range []string{"Conductor-Ops", "  CONDUCTOR-personal ", "conductor-"} {
		if !isConductorSession(&Instance{ID: "ci-" + title, Title: title}) {
			t.Errorf("isConductorSession(%q) = false, want true (historical case-insensitive match)", title)
		}
	}
	if isConductorSession(&Instance{ID: "ci-plain", Title: "my worker"}) {
		t.Error("isConductorSession(my worker) = true")
	}
	if got := ResolveInboxConfig("Conductor-Personal").MaxTextBytes; got != 1234 {
		t.Errorf("ResolveInboxConfig(Conductor-Personal).MaxTextBytes = %d, want 1234", got)
	}
	upper := &Instance{ID: "ci-inbox", Title: "CONDUCTOR-personal"}
	if got := ResolveInboxConfigForInstance(upper).MaxTextBytes; got != 1234 {
		t.Errorf("ResolveInboxConfigForInstance(CONDUCTOR-personal).MaxTextBytes = %d, want 1234", got)
	}
}

// TestConductorIdentity_ResolveRacesTitleWrites: status pollers, the web
// snapshot and the TUI resolve the conductor while a title sync rewrites the
// title on another goroutine (SetTitleThreadSafe), so those entry points must
// read the title under its lock. Run with -race.
func TestConductorIdentity_ResolveRacesTitleWrites(t *testing.T) {
	setupConductorIdentityFixture(t)
	stored := &Instance{ID: "race-stored", Title: "conductor-personal", IsConductor: true, ConductorName: "personal"}
	legacy := &Instance{ID: "race-legacy", Title: "conductor-personal", IsConductor: true}
	all := []*Instance{stored, legacy}
	t.Cleanup(func() { conductorTitleDriftWarned.Delete(stored.ID) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for n := 0; n < 200; n++ {
			title := "conductor-personal"
			if n%2 == 0 {
				title = "renamed by sync"
			}
			stored.SetTitleThreadSafe(title)
			legacy.SetTitleThreadSafe(title)
		}
	}()
	for n := 0; n < 200; n++ {
		_ = stored.ResolveConductorNameThreadSafe()
		_, _ = stored.ConductorTitleDrift()
		_ = legacy.ResolveConductorNameThreadSafe()
		_ = FindConductorInstance(all, "personal")
	}
	<-done
}

// TestConductorIdentity_ResolveUnderInstanceLock: ResolveConductorName is
// reached with i.mu already held (UpdateHookStatus ->
// sessionHasConversationData -> GetClaudeConfigDirForInstance). RWMutex is
// not reentrant, so the resolver must not take i.mu or the hook path
// deadlocks.
func TestConductorIdentity_ResolveUnderInstanceLock(t *testing.T) {
	tmpHome := setupConductorIdentityFixture(t)
	inst := &Instance{ID: "under-lock", Title: "🎙 conductor-personal", Tool: "claude", ProjectPath: tmpHome}

	inst.mu.Lock()
	done := make(chan string, 1)
	go func() { done <- GetClaudeConfigDirForInstance(inst) }()
	select {
	case got := <-done:
		inst.mu.Unlock()
		if want := filepath.Join(tmpHome, ".claude-personal"); got != want {
			t.Fatalf("GetClaudeConfigDirForInstance under i.mu = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		inst.mu.Unlock()
		<-done
		t.Fatal("conductor resolution blocked on i.mu held by the caller (hook path deadlock)")
	}
}

// TestConductorIdentity_DriftWarnedOnce: a renamed conductor resolved on every
// status tick logs conductor_title_drift once per process, not per call.
func TestConductorIdentity_DriftWarnedOnce(t *testing.T) {
	var buf bytes.Buffer
	origLog := sessionLog
	sessionLog = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	t.Cleanup(func() { sessionLog = origLog })

	inst := &Instance{ID: "drift-once-" + t.Name(), Title: "my assistant", IsConductor: true, ConductorName: "personal"}
	t.Cleanup(func() { conductorTitleDriftWarned.Delete(inst.ID) })
	for n := 0; n < 5; n++ {
		if got := inst.ResolveConductorName(); got != "personal" {
			t.Fatalf("ResolveConductorName = %q, want personal", got)
		}
	}
	if got := strings.Count(buf.String(), `"conductor_title_drift"`); got != 1 {
		t.Fatalf("conductor_title_drift logged %d times, want 1; log:\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), `"title":"my assistant"`) {
		t.Errorf("drift warning missing the current title; log:\n%s", buf.String())
	}

	// A decorated title for the same name is not drift and logs nothing.
	buf.Reset()
	decorated := &Instance{ID: "drift-none-" + t.Name(), Title: "🎙 conductor-personal", IsConductor: true, ConductorName: "personal"}
	t.Cleanup(func() { conductorTitleDriftWarned.Delete(decorated.ID) })
	_ = decorated.ResolveConductorName()
	if strings.Contains(buf.String(), "conductor_title_drift") {
		t.Errorf("decorated title for the stored name logged drift:\n%s", buf.String())
	}
}

// TestConductorIdentity_LazyFillSurvivesSaveLoad: a legacy row (no stored
// name) lazily adopts its name on resolve; the next real save persists it, so
// after a rename and another save/load cycle the conductor still resolves.
// Covers both the flagged row and a pre-flag row with the canonical title.
func TestConductorIdentity_LazyFillSurvivesSaveLoad(t *testing.T) {
	for _, flagged := range []bool{true, false} {
		t.Run(map[bool]string{true: "is_conductor", false: "legacy_unflagged"}[flagged], func(t *testing.T) {
			setupConductorIdentityFixture(t)
			storage := newTestStorage(t)

			row := NewInstance("conductor-personal", "/tmp")
			row.Tool = "shell"
			row.IsConductor = flagged
			all := []*Instance{row}
			if err := storage.SaveWithGroups(all, NewGroupTreeWithGroups(all, nil)); err != nil {
				t.Fatalf("SaveWithGroups (legacy): %v", err)
			}

			load := func() []*Instance {
				t.Helper()
				loaded, _, err := storage.LoadWithGroups()
				if err != nil {
					t.Fatalf("LoadWithGroups: %v", err)
				}
				return loaded
			}
			loaded := load()
			got := FindConductorInstance(loaded, "personal")
			if got == nil || got.ConductorName != "" {
				t.Fatalf("legacy row should load without a stored name (found=%v)", got != nil)
			}
			if name := got.ResolveConductorName(); name != "personal" {
				t.Fatalf("ResolveConductorName = %q, want personal", name)
			}

			// Save the lazily filled copy, then rename and save again.
			if err := storage.SaveWithGroups(loaded, NewGroupTreeWithGroups(loaded, nil)); err != nil {
				t.Fatalf("SaveWithGroups (filled): %v", err)
			}
			loaded = load()
			if !flagged {
				// #2708: an unflagged row resolves by its title only; the
				// name is not persisted, so a rename ends the match.
				if loaded[0].ConductorName != "" {
					t.Fatalf("unflagged row persisted a conductor name: %q", loaded[0].ConductorName)
				}
				loaded[0].Title = "my assistant"
				if name := loaded[0].ResolveConductorName(); name != "" {
					t.Fatalf("renamed unflagged row still resolves to %q", name)
				}
				return
			}
			if loaded[0].ConductorName != "personal" {
				t.Fatalf("lazily filled name not persisted: %q", loaded[0].ConductorName)
			}
			loaded[0].Title = "my assistant"
			if err := storage.SaveWithGroups(loaded, NewGroupTreeWithGroups(loaded, nil)); err != nil {
				t.Fatalf("SaveWithGroups (renamed): %v", err)
			}
			loaded = load()
			t.Cleanup(func() { conductorTitleDriftWarned.Delete(loaded[0].ID) })
			if name := loaded[0].ResolveConductorName(); name != "personal" {
				t.Fatalf("renamed conductor resolves to %q after save/load, want personal", name)
			}
			if FindConductorInstance(loaded, "personal") != loaded[0] {
				t.Fatal("renamed conductor not found by name after save/load")
			}
		})
	}
}

// TestGetConductorLastActivity_DecoratedAndRenamed: the heartbeat idle gate
// and `conductor status` last-activity look the conductor up by identity, not
// by the exact canonical title, so a decorated or renamed conductor is found.
func TestGetConductorLastActivity_DecoratedAndRenamed(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	storage, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatalf("setup storage: %v", err)
	}
	decorated := NewInstance("🎙 conductor-eps", "/tmp")
	decorated.IsConductor = true
	renamed := NewInstance("my assistant", "/tmp")
	renamed.IsConductor = true
	renamed.ConductorName = "zeta"
	worker := NewInstance("worker", "/tmp/work")
	if err := storage.Save([]*Instance{decorated, renamed, worker}); err != nil {
		t.Fatalf("save: %v", err)
	}
	t.Cleanup(func() { conductorTitleDriftWarned.Delete(renamed.ID) })

	for _, name := range []string{"eps", "zeta"} {
		if _, err := GetConductorLastActivity(name, "default"); err != nil {
			t.Errorf("GetConductorLastActivity(%q): %v (decorated/renamed conductor not found)", name, err)
		}
	}
}

// TestConductorIdentity_RenameBackDoesNotStick (#2708): a worker (not flagged
// IsConductor) that briefly holds the exact title conductor-<name> must not
// keep the conductor identity after it is renamed back, and a worker already
// stamped with the name by an older build heals: it gets no conductor env,
// keeps the TELEGRAM_* strip, and never beats the real conductor in
// FindConductorInstance even when it comes first.
func TestConductorIdentity_RenameBackDoesNotStick(t *testing.T) {
	tmpHome := setupConductorIdentityFixture(t)
	metaEnv := filepath.Join(tmpHome, "personal-meta.env")

	worker := &Instance{ID: "worker-2708", Title: "conductor-personal", Tool: "claude", ProjectPath: tmpHome}
	if got := worker.ResolveConductorName(); got != "personal" {
		t.Fatalf("worker with the exact title: ResolveConductorName = %q, want personal", got)
	}
	worker.Title = "worker"
	stamped := &Instance{ID: "stamped-2708", Title: "worker", Tool: "claude", ProjectPath: tmpHome, ConductorName: "personal"}
	t.Cleanup(func() { conductorTitleDriftWarned.Delete(stamped.ID) })

	for _, inst := range []*Instance{worker, stamped} {
		if got := inst.ResolveConductorName(); got != "" {
			t.Errorf("%s renamed back still resolves to conductor %q", inst.ID, got)
		}
		if got := inst.getConductorEnv(true); strings.Contains(got, metaEnv) {
			t.Errorf("%s renamed back still gets the conductor env: %q", inst.ID, got)
		}
		if got := telegramStateDirStripExpr(inst); got == "" {
			t.Errorf("%s renamed back keeps TELEGRAM_* (no strip)", inst.ID)
		}
		if inst.IsConductorNamed("personal", inst.Title) {
			t.Errorf("%s renamed back still IsConductorNamed(personal)", inst.ID)
		}
	}

	cond := &Instance{ID: "real-2708", Title: "my assistant", Tool: "claude", IsConductor: true, ConductorName: "personal"}
	t.Cleanup(func() { conductorTitleDriftWarned.Delete(cond.ID) })
	if got := FindConductorInstance([]*Instance{worker, stamped, cond}, "personal"); got != cond {
		gotID := "<nil>"
		if got != nil {
			gotID = got.ID
		}
		t.Fatalf("FindConductorInstance = %s, want the flagged conductor %s", gotID, cond.ID)
	}
}
