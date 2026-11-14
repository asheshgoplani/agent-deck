package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ASK-14: idle sessions move onto newer Claude/Codex builds by themselves.

const (
	idleClaudeComposer = "some prior output\n" +
		"\x1b[38;5;244m────────────────────────────────────────\x1b[39m\n" +
		"\x1b[39m❯ \n" +
		"\x1b[38;5;244m────────────────────────────────────────\x1b[39m\n" +
		"  ? for shortcuts\n"
	draftClaudeComposer = "some prior output\n" +
		"\x1b[38;5;244m────────────────────────────────────────\x1b[39m\n" +
		"\x1b[39m❯ half-typed operator text\n" +
		"\x1b[38;5;244m────────────────────────────────────────\x1b[39m\n" +
		"  ? for shortcuts\n"
	idleCodexComposer  = "• Ran make lint\n\n› \x1b[2mAsk Codex to do anything\x1b[0m\n\n  gpt-6 · ~/work · Context 70% left\n"
	draftCodexComposer = "• Ran make lint\n\n› operator draft\n\n  gpt-6 · ~/work · Context 70% left\n"
)

// idleRefreshHarness stubs every seam of the refresh pass with a fake clock.
type idleRefreshHarness struct {
	t        *testing.T
	now      time.Time
	build    toolBuild
	panes    map[string]idleRefreshPaneView
	mu       sync.Mutex
	restarts []string
	skip     bool
	fail     bool
	timers   map[string]string
	unprobed map[string]bool
	d        *TransitionDaemon
}

func newIdleRefreshHarness(t *testing.T, toml string) *idleRefreshHarness {
	t.Helper()
	withTempAgentDeckHome(t, toml)
	start := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	h := &idleRefreshHarness{
		t:        t,
		now:      start,
		build:    toolBuild{Path: "/opt/claude/versions/2.9.1", InstalledAt: start.Add(-time.Hour)},
		panes:    map[string]idleRefreshPaneView{},
		timers:   map[string]string{},
		unprobed: map[string]bool{},
		d:        &TransitionDaemon{},
	}
	origResolve, origPane, origRestart, origSupp, origNow, origTimers := resolveToolBuild, idleRefreshPane, restartForIdleRefresh, idleRefreshSuppressed, idleRefreshNow, idleRefreshTimers
	t.Cleanup(func() {
		resolveToolBuild, idleRefreshPane, restartForIdleRefresh, idleRefreshSuppressed, idleRefreshNow, idleRefreshTimers = origResolve, origPane, origRestart, origSupp, origNow, origTimers
	})
	idleRefreshTimers = func(inst *Instance, _ time.Time) string { return h.timers[inst.ID] }
	resolveToolBuild = func(string) (toolBuild, bool) { return h.build, true }
	idleRefreshPane = func(inst *Instance) (idleRefreshPaneView, error) { return h.panes[inst.ID], nil }
	restartForIdleRefresh = func(profile, id string) (bool, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.restarts = append(h.restarts, profile+"/"+id)
		if h.fail {
			return false, errors.New("exit status 1: pane died on boot")
		}
		return h.skip, nil
	}
	idleRefreshSuppressed = func() string { return "" }
	idleRefreshNow = func() time.Time { return h.now }
	return h
}

// run polls every 30 s for d and returns the restarts seen.
func (h *idleRefreshHarness) run(profile string, d time.Duration, insts []*Instance, statuses map[string]string) []string {
	h.t.Helper()
	return h.runEvery(idleRefreshRecheck, profile, d, insts, statuses)
}

func (h *idleRefreshHarness) runEvery(step time.Duration, profile string, d time.Duration, insts []*Instance, statuses map[string]string) []string {
	h.t.Helper()
	for end := h.now.Add(d); !h.now.After(end); h.now = h.now.Add(step) {
		probed := map[string]string{}
		for id := range statuses {
			if !h.unprobed[id] {
				probed[id] = ""
			}
		}
		h.d.runIdleBuildRefreshPass(profile, insts, statuses, probed)
		if h.d.idleRefresh != nil {
			h.d.idleRefresh.wg.Wait()
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.restarts...)
}

func idleClaude(id string, startedAt time.Time) *Instance {
	return &Instance{ID: id, Title: id, Tool: "claude", ClaudeSessionID: "conv-" + id, LastStartedAt: startedAt}
}

func idlePane(raw string) idleRefreshPaneView {
	return idleRefreshPaneView{Substate: SubstateIdleAtEmptyPrompt, Raw: raw}
}

func TestIdleBuildRefresh_RestartsIdleSessionOnceAfterDwell(t *testing.T) {
	h := newIdleRefreshHarness(t, "")
	inst := idleClaude("s1", h.now.Add(-48*time.Hour))
	h.panes["s1"] = idlePane(idleClaudeComposer)
	insts := []*Instance{inst}
	statuses := map[string]string{"s1": "waiting"}

	if got := h.run("p", idleRefreshDwell-time.Minute, insts, statuses); len(got) != 0 {
		t.Fatalf("restarted before the %s dwell: %v", idleRefreshDwell, got)
	}
	if got := h.run("p", 2*time.Minute, insts, statuses); len(got) != 1 || got[0] != "p/s1" {
		t.Fatalf("want one restart of p/s1 after the dwell, got %v", got)
	}
	// The restart failed to move the session (LastStartedAt unchanged): the
	// same build is never tried again, by this daemon or a recycled one.
	if got := h.run("p", time.Hour, insts, statuses); len(got) != 1 {
		t.Fatalf("same build retried: %v", got)
	}
	h.d = &TransitionDaemon{}
	if got := h.run("p", time.Hour, insts, statuses); len(got) != 1 {
		t.Fatalf("recycled daemon retried the same build: %v", got)
	}

	path, err := logDataPath("idle-build-refresh.log")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no restart log line: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("want one log line per restart, got %d:\n%s", len(lines), data)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["session"] != "s1" || entry["result"] != "restarted" || entry["build"] != h.build.Path {
		t.Fatalf("log line = %v", entry)
	}

	// A newer build is a new chance.
	h.build = toolBuild{Path: "/opt/claude/versions/2.9.2", InstalledAt: h.now.Add(-time.Minute)}
	h.skip = true // the CLI's freshness guard kept it: logged as skipped
	if got := h.run("p", idleRefreshDwell+time.Minute, insts, statuses); len(got) != 2 {
		t.Fatalf("next build not refreshed: %v", got)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), `"result":"skipped"`) {
		t.Fatalf("a skipped restart must be logged as skipped:\n%s", data)
	}
}

func TestIdleBuildRefresh_NeverRestartsBusyOrHeldSessions(t *testing.T) {
	cases := []struct {
		name   string
		status string
		mutate func(*Instance)
		pane   idleRefreshPaneView
	}{
		{"running", "running", nil, idlePane(idleClaudeComposer)},
		{"error", "error", nil, idlePane(idleClaudeComposer)},
		{"interactive menu", "waiting", nil, idleRefreshPaneView{Substate: SubstateInteractiveMenu, Raw: idleClaudeComposer}},
		{"background work substate", "waiting", nil, idleRefreshPaneView{Substate: SubstateBackgroundWork, Raw: idleClaudeComposer}},
		{"background work in flight", "waiting", nil, idleRefreshPaneView{Substate: SubstateIdleAtEmptyPrompt, BackgroundInFlight: true, Raw: idleClaudeComposer}},
		{"composer draft", "waiting", nil, idlePane(draftClaudeComposer)},
		{"queued input", "waiting", nil, idlePane(strings.Replace(draftClaudeComposer, "half-typed operator text", "Press up to edit queued messages", 1))},
		{"dialog open", "waiting", nil, idlePane(strings.Replace(idleClaudeComposer, "  ? for shortcuts", "  Enter to confirm · Esc to cancel", 1))},
		{"background shells in footer", "waiting", nil, idlePane(strings.Replace(idleClaudeComposer, "  ? for shortcuts", "  ⏵⏵ bypass permissions on · 2 shells · ← for agents", 1))},
		{"no composer", "waiting", nil, idlePane("some output\n")},
		{"client attached", "idle", nil, idleRefreshPaneView{Substate: SubstateIdleAtEmptyPrompt, Raw: idleClaudeComposer, Attached: true}},
		{"no conversation id", "waiting", func(i *Instance) { i.ClaudeSessionID = "" }, idlePane(idleClaudeComposer)},
		{"start time unknown", "waiting", func(i *Instance) { i.LastStartedAt = time.Time{} }, idlePane(idleClaudeComposer)},
		{"started after the install", "waiting", func(i *Instance) { i.LastStartedAt = time.Date(2026, 10, 19, 7, 30, 0, 0, time.UTC) }, idlePane(idleClaudeComposer)},
		{"other tool", "waiting", func(i *Instance) { i.Tool = "gemini" }, idlePane(idleClaudeComposer)},
		{"auth hold", "waiting", func(i *Instance) {
			if err := writeAuthHoldRecord(AuthHoldRecord{InstanceID: i.ID, Reason: AuthHoldReasonDeath}); err != nil {
				t.Fatal(err)
			}
		}, idlePane(idleClaudeComposer)},
		{"ssh session", "waiting", func(i *Instance) { i.SSHHost = "box" }, idlePane(idleClaudeComposer)},
		{"sandbox session", "waiting", func(i *Instance) { i.Sandbox = &SandboxConfig{Enabled: true, Image: "img"} }, idlePane(idleClaudeComposer)},
		{"wrapper session", "waiting", func(i *Instance) { i.Wrapper = "nice {command}" }, idlePane(idleClaudeComposer)},
		{"conductor not opted in", "waiting", func(i *Instance) { i.IsConductor, i.ConductorName = true, "cond" }, idlePane(idleClaudeComposer)},
		{"in-Claude timer", "waiting", func(i *Instance) { i.ID = "timer" }, idlePane(idleClaudeComposer)},
		{"codex background terminal", "waiting", func(i *Instance) { i.Tool, i.CodexSessionID = "codex", "thread-1" },
			idleRefreshPaneView{Raw: strings.Replace(idleCodexComposer, "Context 70% left", "Context 70% left · 1 background terminal running", 1)}},
		{"codex unprobed", "waiting", func(i *Instance) { i.ID, i.Tool, i.CodexSessionID = "unprobed", "codex", "thread-1" }, idleRefreshPaneView{Raw: idleCodexComposer}},
		{"codex draft", "waiting", func(i *Instance) { i.Tool, i.CodexSessionID = "codex", "thread-1" }, idleRefreshPaneView{Raw: draftCodexComposer}},
		{"codex usage limit", "waiting", func(i *Instance) { i.Tool, i.CodexSessionID = "codex", "thread-1" }, idleRefreshPaneView{Substate: SubstateUsageLimit, Raw: idleCodexComposer}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newIdleRefreshHarness(t, "")
			h.timers["timer"] = "scheduled wake-up"
			h.unprobed["unprobed"] = true
			inst := idleClaude("s1", h.now.Add(-48*time.Hour))
			if tc.mutate != nil {
				tc.mutate(inst)
			}
			h.panes[inst.ID] = tc.pane
			if got := h.run("p", time.Hour, []*Instance{inst}, map[string]string{inst.ID: tc.status}); len(got) != 0 {
				t.Fatalf("restarted a %s session: %v", tc.name, got)
			}
		})
	}
}

func TestIdleBuildRefresh_RestartsIdleCodex(t *testing.T) {
	h := newIdleRefreshHarness(t, "")
	inst := &Instance{ID: "c1", Tool: "codex", CodexSessionID: "thread-1", LastStartedAt: h.now.Add(-24 * time.Hour)}
	h.panes["c1"] = idleRefreshPaneView{Raw: idleCodexComposer}
	if got := h.run("p", idleRefreshDwell+time.Minute, []*Instance{inst}, map[string]string{"c1": "idle"}); len(got) != 1 {
		t.Fatalf("idle codex not refreshed: %v", got)
	}
}

func TestIdleBuildRefresh_OptedInConductorWaitsTenMinutes(t *testing.T) {
	h := newIdleRefreshHarness(t, "[conductors.cond.sessions]\nauto_refresh_idle_builds = true\n")
	inst := idleClaude("cond", h.now.Add(-72*time.Hour))
	inst.IsConductor, inst.ConductorName = true, "cond"
	h.panes["cond"] = idlePane(idleClaudeComposer)
	insts, statuses := []*Instance{inst}, map[string]string{"cond": "waiting"}
	if got := h.run("p", idleRefreshConductorDwell-time.Minute, insts, statuses); len(got) != 0 {
		t.Fatalf("conductor restarted before 10 min idle: %v", got)
	}
	if got := h.run("p", 2*time.Minute, insts, statuses); len(got) != 1 {
		t.Fatalf("idle conductor not refreshed after 10 min: %v", got)
	}
}

func TestIdleBuildRefresh_ActivityResetsDwell(t *testing.T) {
	h := newIdleRefreshHarness(t, "")
	inst := idleClaude("s1", h.now.Add(-48*time.Hour))
	h.panes["s1"] = idlePane(idleClaudeComposer)
	insts, statuses := []*Instance{inst}, map[string]string{"s1": "waiting"}
	h.run("p", 4*time.Minute, insts, statuses)
	statuses["s1"] = "running" // a new turn
	h.run("p", time.Minute, insts, statuses)
	statuses["s1"] = "waiting"
	if got := h.run("p", 4*time.Minute, insts, statuses); len(got) != 0 {
		t.Fatalf("dwell survived a running turn: %v", got)
	}
	if got := h.run("p", 2*time.Minute, insts, statuses); len(got) != 1 {
		t.Fatalf("not refreshed after a full idle dwell: %v", got)
	}
}

func TestIdleBuildRefresh_OneSessionAtATime(t *testing.T) {
	h := newIdleRefreshHarness(t, "")
	var insts []*Instance
	statuses := map[string]string{}
	for _, id := range []string{"a", "b", "c"} {
		insts = append(insts, idleClaude(id, h.now.Add(-48*time.Hour)))
		h.panes[id] = idlePane(idleClaudeComposer)
		statuses[id] = "waiting"
	}
	got := h.runEvery(10*time.Second, "p", idleRefreshDwell+idleRefreshSpacing-time.Second, insts, statuses)
	if len(got) != 1 {
		t.Fatalf("want a single restart in the first spacing window, got %v", got)
	}
	if got = h.runEvery(10*time.Second, "p", 2*idleRefreshSpacing, insts, statuses); len(got) != 3 {
		t.Fatalf("want the rest refreshed one per spacing window, got %v", got)
	}
}

// Review M1: activity while another session's restart holds the profile
// still resets the dwell.
func TestIdleBuildRefresh_ActivityDuringSpacingResetsDwell(t *testing.T) {
	h := newIdleRefreshHarness(t, "")
	a, b := idleClaude("a", h.now.Add(-48*time.Hour)), idleClaude("b", h.now.Add(-48*time.Hour))
	h.panes["a"], h.panes["b"] = idlePane(idleClaudeComposer), idlePane(idleClaudeComposer)
	insts, statuses := []*Instance{a, b}, map[string]string{"a": "waiting", "b": "waiting"}
	// a restarts at the dwell; b is held by the spacing window and works.
	if got := h.runEvery(10*time.Second, "p", idleRefreshDwell, insts, statuses); len(got) != 1 {
		t.Fatalf("want a refreshed first, got %v", got)
	}
	statuses["b"] = "running" // 10 s after a's restart, inside the spacing window
	h.run("p", 0, insts, statuses)
	statuses["b"] = "waiting"
	if got := h.run("p", idleRefreshDwell-time.Minute, insts, statuses); len(got) != 1 {
		t.Fatalf("b restarted right after its turn: %v", got)
	}
	if got := h.run("p", 2*time.Minute, insts, statuses); len(got) != 2 {
		t.Fatalf("b not refreshed after a full dwell: %v", got)
	}
}

func TestIdleBuildRefresh_OptOut(t *testing.T) {
	for _, tc := range []struct{ name, toml, profile string }{
		{"global", "[sessions]\nauto_refresh_idle_builds = false\n", "p"},
		{"profile", "[profiles.p.sessions]\nauto_refresh_idle_builds = false\n", "p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newIdleRefreshHarness(t, tc.toml)
			inst := idleClaude("s1", h.now.Add(-48*time.Hour))
			h.panes["s1"] = idlePane(idleClaudeComposer)
			if got := h.run(tc.profile, time.Hour, []*Instance{inst}, map[string]string{"s1": "waiting"}); len(got) != 0 {
				t.Fatalf("opted-out profile restarted: %v", got)
			}
		})
	}
}

func TestAutoRefreshIdleBuildsEnabled(t *testing.T) {
	no, yes := false, true
	var nilCfg *UserConfig
	if !nilCfg.AutoRefreshIdleBuildsEnabled("p") {
		t.Fatal("default must be on")
	}
	cfg := &UserConfig{
		Sessions: SessionsSettings{AutoRefreshIdleBuilds: &no},
		Profiles: map[string]ProfileSettings{"work": {Sessions: SessionsSettings{AutoRefreshIdleBuilds: &yes}}},
	}
	if cfg.AutoRefreshIdleBuildsEnabled("p") {
		t.Fatal("global false not honoured")
	}
	if !cfg.AutoRefreshIdleBuildsEnabled("work") {
		t.Fatal("profile value must win over the global one")
	}
}

func TestResolveToolBuild_FollowsSymlinkToInstalledFile(t *testing.T) {
	dir := t.TempDir()
	versions := filepath.Join(dir, "versions")
	if err := os.MkdirAll(versions, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(versions, "2.9.0")
	if err := os.WriteFile(old, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Installers back-date mtime (npm pins it to 1985); ctime still moves.
	ancient := time.Date(1985, 10, 26, 8, 15, 0, 0, time.UTC)
	if err := os.Chtimes(old, ancient, ancient); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "claude")
	if err := os.Symlink(old, link); err != nil {
		t.Fatal(err)
	}
	b, ok := defaultResolveToolBuild(link + " --flag")
	if !ok || b.Path != old {
		t.Fatalf("resolve = %+v, %v; want %s", b, ok, old)
	}
	// A chmod bumps ctime but is the same build.
	if err := os.Chmod(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if b2, _ := defaultResolveToolBuild(link); b2.key() != b.key() {
		t.Fatalf("chmod changed the build key: %s -> %s", b.key(), b2.key())
	}
	// A reinstall (new file at the same path) is a new build.
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("#!/bin/sh\n# 2.9.1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if b3, _ := defaultResolveToolBuild(link); b3.key() == b.key() {
		t.Fatalf("reinstall kept the build key %s", b.key())
	}
	if time.Since(b.InstalledAt) > time.Hour {
		t.Fatalf("InstalledAt %v follows the back-dated mtime, not the install", b.InstalledAt)
	}
	for _, cmd := range []string{"", "FOO=1 claude", filepath.Join(dir, "missing")} {
		if _, ok := defaultResolveToolBuild(cmd); ok {
			t.Fatalf("%q must resolve to an unknown build", cmd)
		}
	}
}

// Conductor ruling (4): two failed restarts in a row stop the profile for the
// day, across a daemon recycle; the next day it resumes.
func TestIdleBuildRefresh_BreakerStopsAfterTwoFailures(t *testing.T) {
	h := newIdleRefreshHarness(t, "")
	h.fail = true
	var insts []*Instance
	statuses := map[string]string{}
	for _, id := range []string{"a", "b", "c", "d"} {
		insts = append(insts, idleClaude(id, h.now.Add(-48*time.Hour)))
		h.panes[id] = idlePane(idleClaudeComposer)
		statuses[id] = "waiting"
	}
	if got := h.run("p", time.Hour, insts, statuses); len(got) != 2 {
		t.Fatalf("want the breaker to stop after 2 failed restarts, got %v", got)
	}
	h.d = &TransitionDaemon{}
	if got := h.run("p", time.Hour, insts, statuses); len(got) != 2 {
		t.Fatalf("a recycled daemon ignored today's breaker: %v", got)
	}
	data, _ := os.ReadFile(mustLogPath(t))
	if !strings.Contains(string(data), "breaker") {
		t.Fatalf("breaker trip not logged:\n%s", data)
	}
	h.fail = false
	h.now = h.now.Add(24 * time.Hour)
	if got := h.run("p", time.Hour, insts, statuses); len(got) != 4 {
		t.Fatalf("the next day must resume with the untried sessions, got %v", got)
	}
}

// A restart --if-idle refused (the session woke up) is not the build's one
// attempt: a later dwell tries again.
func TestIdleBuildRefresh_RefusedRestartIsRetried(t *testing.T) {
	h := newIdleRefreshHarness(t, "")
	h.skip = true
	inst := idleClaude("s1", h.now.Add(-48*time.Hour))
	h.panes["s1"] = idlePane(idleClaudeComposer)
	insts, statuses := []*Instance{inst}, map[string]string{"s1": "waiting"}
	if got := h.run("p", idleRefreshDwell+time.Minute, insts, statuses); len(got) != 1 {
		t.Fatalf("want one attempt, got %v", got)
	}
	h.skip = false
	if got := h.run("p", idleRefreshDwell+time.Minute, insts, statuses); len(got) != 2 {
		t.Fatalf("a refused restart was not retried: %v", got)
	}
}

func mustLogPath(t *testing.T) string {
	t.Helper()
	path, err := logDataPath("idle-build-refresh.log")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTranscriptTimers(t *testing.T) {
	now := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	use := func(at time.Time, name, input string) string {
		return `{"type":"assistant","timestamp":"` + at.Format(time.RFC3339) + `","message":{"content":[{"type":"tool_use","id":"t","name":"` + name + `","input":` + input + `}]}}`
	}
	text := `{"type":"user","timestamp":"` + now.Add(-time.Hour).Format(time.RFC3339) + `","message":{"content":"please call ScheduleWakeup and CronCreate"}}`
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"none", []string{text}, ""},
		{"cron armed", []string{use(now.Add(-48*time.Hour), "CronCreate", `{"cron":"*/5 * * * *"}`)}, "cron"},
		{"cron deleted", []string{use(now.Add(-2*time.Hour), "CronCreate", `{}`), use(now.Add(-time.Hour), "CronDelete", `{"id":"x"}`)}, ""},
		{"wake-up pending", []string{use(now.Add(-10*time.Minute), "ScheduleWakeup", `{"delaySeconds":1800}`)}, "scheduled wake-up"},
		{"wake-up fired", []string{use(now.Add(-40*time.Minute), "ScheduleWakeup", `{"delaySeconds":1800}`)}, ""},
		{"monitor live", []string{use(now.Add(-time.Minute), "Monitor", `{"timeout_ms":300000}`)}, "monitor"},
		{"monitor expired", []string{use(now.Add(-time.Hour), "Monitor", `{"timeout_ms":300000}`)}, ""},
		{"monitor persistent", []string{use(now.Add(-48*time.Hour), "Monitor", `{"persistent":true}`)}, "monitor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := transcriptTimers(strings.NewReader(strings.Join(tc.lines, "\n")+"\n"), now); got != tc.want {
				t.Fatalf("transcriptTimers = %q, want %q", got, tc.want)
			}
		})
	}
}

// The --if-idle recheck refuses anything but an idle pane it can see.
func TestIdleRefreshRecheck_RefusesBusyOrMissingPane(t *testing.T) {
	inst := &Instance{ID: "s1", Tool: "claude", Status: StatusRunning}
	if got := inst.IdleRefreshRecheck(); got != "status running" {
		t.Fatalf("running session: recheck = %q", got)
	}
	inst.Status = StatusWaiting
	if got := inst.IdleRefreshRecheck(); got == "" {
		t.Fatal("a session without a pane passed the recheck")
	}
}

// An --if-idle restart of a conductor sends no recovery turn.
func TestSkipConductorRecoveryTurn(t *testing.T) {
	inst := &Instance{ID: "cond", Tool: "claude", IsConductor: true}
	if err := inst.wakeConductorAfterSpawn(); err == nil {
		t.Fatal("control: a conductor without a pane must report the recovery as not sent")
	}
	inst.SkipConductorRecoveryTurn()
	if err := inst.wakeConductorAfterSpawn(); err != nil {
		t.Fatalf("skipped recovery turn still attempted: %v", err)
	}
}
