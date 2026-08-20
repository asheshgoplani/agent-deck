package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestMain(m *testing.M) {
	restore := testutil.IsolateHome()
	code := m.Run()
	restore()
	os.Exit(code)
}

func TestParseCronNext(t *testing.T) {
	utc := time.UTC
	base := time.Date(2026, 8, 20, 14, 3, 0, 0, utc)

	cases := []struct {
		spec string
		want time.Time
	}{
		{"*/5 * * * *", time.Date(2026, 8, 20, 14, 5, 0, 0, utc)},
		{"0 2 * * *", time.Date(2026, 8, 21, 2, 0, 0, 0, utc)},
		{"0 * * * *", time.Date(2026, 8, 20, 15, 0, 0, 0, utc)},
		{"30 14 * * *", time.Date(2026, 8, 20, 14, 30, 0, 0, utc)},
	}
	for _, tc := range cases {
		schedule, err := ParseCron(tc.spec, utc)
		if err != nil {
			t.Fatalf("ParseCron(%q): %v", tc.spec, err)
		}
		got, ok := schedule.Next(base)
		if !ok {
			t.Fatalf("ParseCron(%q).Next: no occurrence found", tc.spec)
		}
		if !got.Equal(tc.want) {
			t.Errorf("ParseCron(%q).Next = %s, want %s", tc.spec, got, tc.want)
		}
	}
}

// A schedule that cannot recur must report that, not return a plausible time.
func TestParseCronImpossibleScheduleReportsNoOccurrence(t *testing.T) {
	schedule, err := ParseCron("0 0 30 2 *", time.UTC)
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	if _, ok := schedule.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); ok {
		t.Fatal("February 30th reported an occurrence; it must report none")
	}
}

func TestParseCronRejectsMalformed(t *testing.T) {
	for _, spec := range []string{"* * * *", "bad * * * *", "*/0 * * * *", "60 * * * *", "* 25 * * *"} {
		if _, err := ParseCron(spec, time.UTC); err == nil {
			t.Errorf("ParseCron(%q) accepted a malformed expression", spec)
		}
	}
}

// Cron's day-of-month / day-of-week OR rule.
func TestCronDayWeekdayOrRule(t *testing.T) {
	schedule, err := ParseCron("0 0 1 * 5", time.UTC)
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	// 2026-08-21 is a Friday and not the 1st; it must still match.
	friday := time.Date(2026, 8, 20, 23, 59, 0, 0, time.UTC)
	next, ok := schedule.Next(friday)
	if !ok {
		t.Fatal("no occurrence")
	}
	if next.Weekday() != time.Friday {
		t.Errorf("next = %s, want a Friday via the day/weekday OR rule", next)
	}
}

func TestParseSystemdUnitReadsPairedTimerCadence(t *testing.T) {
	dir := t.TempDir()
	service := filepath.Join(dir, "demo.service")
	writeTestFile(t, service, `[Unit]
Description=demo tick
[Service]
Type=oneshot
ExecStart=/home/someone/projects/agent-deck-g14/overnight/manager.sh
Environment="API_TOKEN=shhh"
WorkingDirectory=/tmp
`)
	writeTestFile(t, filepath.Join(dir, "demo.timer"), `[Unit]
Description=tick
[Timer]
OnBootSec=2min
OnUnitInactiveSec=5min
`)

	src, err := ParseSystemdUnit(service)
	if err != nil {
		t.Fatalf("ParseSystemdUnit: %v", err)
	}
	// OnUnitInactiveSec is the repeating cadence and must win over OnBootSec,
	// which only says when the first run happens.
	if src.IntervalSeconds != 300 {
		t.Errorf("IntervalSeconds = %d, want 300 from OnUnitInactiveSec", src.IntervalSeconds)
	}
	if src.ScheduleKey != "OnUnitInactiveSec" {
		t.Errorf("ScheduleKey = %q, want OnUnitInactiveSec", src.ScheduleKey)
	}
	// Environment VALUES must never be captured.
	if len(src.EnvKeys) != 1 || src.EnvKeys[0] != "API_TOKEN" {
		t.Errorf("EnvKeys = %v, want [API_TOKEN]", src.EnvKeys)
	}
	for _, key := range src.EnvKeys {
		if strings.Contains(key, "shhh") {
			t.Fatal("an environment value leaked into the parsed unit")
		}
	}
}

// A unit whose script merely lives under a path containing "agent-deck" is not
// an agent. This is the false positive that made the notify daemon and a shell
// tick both classify as agents.
func TestClassifyIgnoresAgentDeckInPath(t *testing.T) {
	src := &LaunchSource{
		Kind:      "systemd",
		Label:     "agentdeck-overnight",
		Program:   "/bin/sh",
		Arguments: []string{"/home/someone/projects/agent-deck-g14/overnight/overnight-manager.sh"},
	}
	got := ClassifyLaunchSource(src)
	if got.Class == ClassAgent {
		t.Errorf("classified as agent from a path substring; got %+v", got)
	}
}

func TestClassifyDaemonSubcommandIsService(t *testing.T) {
	// The program must actually exist: a missing program is debris, and that
	// verdict deliberately outranks every other reading, because a unit whose
	// binary is gone cannot be doing any of them.
	binary := filepath.Join(t.TempDir(), "agent-deck")
	writeTestFile(t, binary, "#!/bin/sh\n")

	src := &LaunchSource{
		Kind:      "systemd",
		Label:     "agent-deck-transition-notifier",
		Program:   binary,
		Arguments: []string{binary, "notify-daemon"},
	}
	got := ClassifyLaunchSource(src)
	if got.Class != ClassService {
		t.Errorf("Class = %q, want %q for a daemon subcommand", got.Class, ClassService)
	}
}

// A missing program outranks a daemon reading.
func TestClassifyMissingProgramOutranksDaemonReading(t *testing.T) {
	src := &LaunchSource{
		Kind:      "systemd",
		Label:     "gone-notifier",
		Program:   "/definitely/not/here/agent-deck",
		Arguments: []string{"/definitely/not/here/agent-deck", "notify-daemon"},
	}
	if got := ClassifyLaunchSource(src); got.Class != ClassDebris {
		t.Errorf("Class = %q, want %q", got.Class, ClassDebris)
	}
}

func TestClassifyMissingProgramIsDebris(t *testing.T) {
	src := &LaunchSource{Kind: "launchd", Label: "gone", Program: "/definitely/not/here/binary"}
	if got := ClassifyLaunchSource(src); got.Class != ClassDebris {
		t.Errorf("Class = %q, want %q", got.Class, ClassDebris)
	}
}

// The reference org chart: conductors are managers, watchers are triage,
// maintainers are builders, and a builder implies an unfilled reviewer seat.
func TestClassifyRoleFollowsOrgChart(t *testing.T) {
	cases := []struct {
		name     string
		conduct  bool
		wantRole string
		wantPair string
	}{
		{"conductor-agent-deck", false, RoleManager, ""},
		{"anything", true, RoleManager, ""},
		{"gmail-watcher", false, RoleTriage, ""},
		{"repo-maintainer", false, RoleBuilder, RoleReviewer},
		{"wat", false, RoleUnresolved, ""},
	}
	for _, tc := range cases {
		got := ClassifyRole(tc.name, tc.conduct)
		if got.Role != tc.wantRole {
			t.Errorf("ClassifyRole(%q).Role = %q, want %q", tc.name, got.Role, tc.wantRole)
		}
		if PairFor(got.Role) != tc.wantPair {
			t.Errorf("PairFor(%q) = %q, want %q", got.Role, PairFor(got.Role), tc.wantPair)
		}
	}
}

func TestReportsToChain(t *testing.T) {
	if got := ReportsToFor(RoleManager, "boss"); got != PrincipalHuman {
		t.Errorf("manager reports to %q, want the human principal", got)
	}
	if got := ReportsToFor(RoleBuilder, "boss"); got != "boss" {
		t.Errorf("builder reports to %q, want boss", got)
	}
	if got := ReportsToFor(RoleBuilder, ""); got != PrincipalHuman {
		t.Errorf("builder with no manager reports to %q, want the human principal", got)
	}
}

func TestValidateReportsToDetectsCycle(t *testing.T) {
	a := NewPost("a", "post-a")
	b := NewPost("b", "post-b")
	a.Spec.Placement.ReportsTo = "b"
	b.Spec.Placement.ReportsTo = "a"

	findings := ValidateReportsTo([]*Post{a, b})
	if !findings.HasErrors() {
		t.Fatal("a reports_to cycle was not reported")
	}
}

// The injection invariant, as a test rather than a comment.
func TestValidatePostRejectsInterpolatedDelivery(t *testing.T) {
	post := validTestPost()
	post.Spec.Triggers = []Trigger{{
		Name: "mail", Type: TriggerMailDoorbell, External: true,
		ExternalSource: "/x.plist",
		Deliver:        "New mail from {{sender}}: {{subject}}",
	}}
	findings := ValidatePost(post)
	if !findings.HasErrors() {
		t.Fatal("a templated delivery string was accepted; it is a prompt-injection path")
	}
}

func TestValidatePostRejectsEnabledPhase1(t *testing.T) {
	post := validTestPost()
	post.Spec.Enabled = true
	if !ValidatePost(post).HasErrors() {
		t.Fatal("an enabled post was accepted in phase 1")
	}

	post = validTestPost()
	post.Spec.Triggers = []Trigger{{
		Name: "t", Type: TriggerCron, Schedule: "*/5 * * * *", Timezone: "UTC",
		Enabled: true, External: true, ExternalSource: "/x.timer",
	}}
	if !ValidatePost(post).HasErrors() {
		t.Fatal("an enabled trigger was accepted in phase 1")
	}
}

func TestValidatePostRequiresExternalSource(t *testing.T) {
	post := validTestPost()
	post.Spec.Triggers = []Trigger{{
		Name: "t", Type: TriggerCron, Schedule: "*/5 * * * *", Timezone: "UTC",
		External: true, // no ExternalSource
	}}
	if !ValidatePost(post).HasErrors() {
		t.Fatal("an external trigger with no owning file was accepted")
	}
}

func TestValidateRoleRejectsEscapingReference(t *testing.T) {
	role := NewRole("manager", "0.1.0")
	role.Spec.Entrypoint = "../../etc/passwd"
	if !ValidateRole(role).HasErrors() {
		t.Fatal("a role reference escaping the role directory was accepted")
	}

	role = NewRole("manager", "0.1.0")
	role.Spec.Entrypoint = "/etc/passwd"
	if !ValidateRole(role).HasErrors() {
		t.Fatal("an absolute role reference was accepted")
	}
}

func TestValidateRoleContentFlagsPortabilityRot(t *testing.T) {
	body := "Work out of /home/someone/x\nBox at build.local\nexport GITHUB_TOKEN=ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"
	findings := ValidateRoleContent("role/INSTRUCTIONS.md", body)
	if len(findings) < 3 {
		t.Errorf("got %d findings, want at least 3 (path, hostname, credential): %v", len(findings), findings)
	}
}

func TestCheckHealthDistinguishesUnknownFromDown(t *testing.T) {
	now := time.Now()

	unknown := CheckHealth("c", "mail", "", DefaultStaleAfter, now)
	if unknown.State != HealthUnknown {
		t.Errorf("no evidence path gave %q, want %q", unknown.State, HealthUnknown)
	}

	missing := CheckHealth("c", "mail", filepath.Join(t.TempDir(), "nope"), DefaultStaleAfter, now)
	if missing.State != HealthUnknown {
		t.Errorf("missing evidence path gave %q, want %q — absence is not proof of death", missing.State, HealthUnknown)
	}

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "pid"), "999999\n")
	dead := CheckHealth("c", "mail", dir, DefaultStaleAfter, now)
	if dead.State != HealthDown {
		t.Errorf("a pid file naming a dead process gave %q, want %q", dead.State, HealthDown)
	}
}

func TestCheckHealthFreshAndStale(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen.db")
	writeTestFile(t, seen, "x")

	fresh := CheckHealth("gmail", "mail", dir, 30*time.Minute, now)
	if fresh.State != HealthOK {
		t.Errorf("a just-written seen.db gave %q, want %q (%s)", fresh.State, HealthOK, fresh.Detail)
	}

	old := now.Add(-3 * time.Hour)
	if err := os.Chtimes(seen, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	stale := CheckHealth("gmail", "mail", dir, 30*time.Minute, now)
	if stale.State != HealthStale {
		t.Errorf("a 3h-old seen.db gave %q, want %q", stale.State, HealthStale)
	}
	if stale.FreshnessFile != seen {
		t.Errorf("FreshnessFile = %q, want %q", stale.FreshnessFile, seen)
	}
}

func TestAdoptConductorDirIsReadOnlyAndDisabled(t *testing.T) {
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "CLAUDE.md"), "You are the conductor.\n")
	writeTestFile(t, filepath.Join(source, "POLICY.md"), "Never merge unreviewed.\n")
	writeTestFile(t, filepath.Join(source, "LEARNINGS.md"), "Commit state before notifying.\n")
	if err := os.MkdirAll(filepath.Join(source, "workflows"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, filepath.Join(source, "workflows", "cut-a-release.md"), "1. tag\n2. push\n")

	before := snapshotDir(t, source)

	plan, err := Adopt(Options{Target: source, Machine: "testbox", Now: time.Unix(1755000000, 0)})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if len(plan.Definitions) != 1 {
		t.Fatalf("got %d definitions, want 1", len(plan.Definitions))
	}
	def := plan.Definitions[0]

	if def.Post.Spec.Role.Name != RoleManager {
		t.Errorf("role = %q, want %q for a conductor directory", def.Post.Spec.Role.Name, RoleManager)
	}
	if def.Post.Spec.Enabled {
		t.Error("adoption emitted an enabled post")
	}
	if def.Post.Spec.Placement.ReportsTo != PrincipalHuman {
		t.Errorf("reportsTo = %q, want the human principal", def.Post.Spec.Placement.ReportsTo)
	}
	// CLAUDE.md becomes the portable entry point.
	if _, ok := def.RoleFiles["INSTRUCTIONS.md"]; !ok {
		t.Error("CLAUDE.md was not mapped to INSTRUCTIONS.md")
	}
	if _, ok := def.RoleFiles[filepath.Join("workflows", "cut-a-release.md")]; !ok {
		t.Error("workflow file was not carried into the role")
	}
	if len(def.Post.Spec.Provenance) == 0 {
		t.Error("no provenance was recorded")
	}
	if findings := ValidateDefinition(def.Post, def.Role); findings.HasErrors() {
		t.Errorf("emitted definition does not validate: %v", findings)
	}

	// The source must be byte-identical afterwards.
	if after := snapshotDir(t, source); after != before {
		t.Error("adoption modified the source directory")
	}
}

func TestAdoptPlanWriteRefusesToClobber(t *testing.T) {
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "CLAUDE.md"), "conductor\n")

	plan, err := Adopt(Options{Target: source, Machine: "testbox"})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	root := t.TempDir()
	if _, err := plan.WriteTo(root); err != nil {
		t.Fatalf("first WriteTo: %v", err)
	}
	if _, err := plan.WriteTo(root); err == nil {
		t.Fatal("a second write silently overwrote an existing definition")
	}
}

func TestAdoptSessionUsesOrgChartAndKeepsSessionIntact(t *testing.T) {
	sessions := []SessionInfo{{
		ID: "abc-123", Title: "repo-maintainer", Tool: "claude",
		Account: "work", GroupPath: "maintainers", ProjectPath: "/tmp/x", Status: "running",
	}}
	plan, err := Adopt(Options{Target: "repo-maintainer", Sessions: sessions, ManagerPost: "conductor-x"})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	post := plan.Definitions[0].Post
	if post.Spec.Role.Name != RoleBuilder {
		t.Errorf("role = %q, want %q", post.Spec.Role.Name, RoleBuilder)
	}
	if post.Spec.Placement.ReportsTo != "conductor-x" {
		t.Errorf("reportsTo = %q, want conductor-x", post.Spec.Placement.ReportsTo)
	}
	if post.Spec.Runtime.AdoptedSessionID != "abc-123" {
		t.Errorf("adoptedSessionId = %q, want abc-123", post.Spec.Runtime.AdoptedSessionID)
	}
	// The pair rule must surface the empty reviewer seat.
	joined := strings.Join(plan.Notes, " ")
	if !strings.Contains(joined, RoleReviewer) {
		t.Errorf("notes = %v, want a note about the unfilled reviewer seat", plan.Notes)
	}
}

func TestAdoptUnknownTargetIsAnError(t *testing.T) {
	if _, err := Adopt(Options{Target: "no-such-thing"}); err == nil {
		t.Fatal("an unknown target was accepted")
	}
}

// A round trip through the registry must preserve the definition.
func TestWriteAndLoadRoundTrip(t *testing.T) {
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "CLAUDE.md"), "conductor\n")
	writeTestFile(t, filepath.Join(source, "POLICY.md"), "policy\n")

	plan, err := Adopt(Options{Target: source, Machine: "testbox"})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	root := t.TempDir()
	written, err := plan.WriteTo(root)
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	def, err := Load(written[0])
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if def.Post.Metadata.PostID != plan.Definitions[0].Post.Metadata.PostID {
		t.Error("post id did not survive the round trip")
	}
	if def.Role == nil || def.Role.Spec.Entrypoint != "INSTRUCTIONS.md" {
		t.Error("role did not survive the round trip")
	}
	// Definitions are private by default.
	info, err := os.Stat(filepath.Join(written[0], PostFileName))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("agent.yaml mode = %o, want 600", perm)
	}
}

// A malformed definition must be reported, not silently dropped: the fleet
// must never look smaller than it is.
func TestLoadAllReportsMalformedDefinition(t *testing.T) {
	root := t.TempDir()
	bad := filepath.Join(root, "broken")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, filepath.Join(bad, PostFileName), "this: is: not: a: post\n")

	def, err := Load(bad)
	if err == nil {
		t.Fatal("a malformed post loaded without error")
	}
	if def != nil {
		t.Error("a malformed post returned a definition")
	}
}

func TestBuildViewCountsAndGrouping(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	working := NewPost("builder-one", "post-1")
	working.Spec.Classification = ClassAgent
	working.Spec.Role = RoleRef{Name: RoleBuilder, Version: "0.1.0"}
	working.Spec.Runtime.AdoptedSessionID = "sess-1"
	working.Spec.Placement.Machine = "g14"

	orphan := NewPost("triage-one", "post-2")
	orphan.Spec.Classification = ClassAgent
	orphan.Spec.Role = RoleRef{Name: RoleTriage}
	orphan.Spec.Placement.Machine = "g14"

	view := BuildView(BuildOptions{
		Definitions: []*Definition{
			{Name: "builder-one", Post: working},
			{Name: "triage-one", Post: orphan},
		},
		SessionStates: map[string]SessionState{"sess-1": {Status: "running", Present: true}},
		LocalMachine:  "g14",
		Now:           now,
		SkipHealth:    true,
	})

	if view.TotalAgents != 2 {
		t.Errorf("TotalAgents = %d, want 2", view.TotalAgents)
	}
	if len(view.Machines) != 1 || view.Machines[0].Name != "g14" {
		t.Fatalf("machines = %+v, want a single g14 group", view.Machines)
	}
	rows := view.Machines[0].Agents
	if rows[0].State != RunWorking {
		t.Errorf("row state = %q, want %q", rows[0].State, RunWorking)
	}
	// A post whose session is gone says so; it does not borrow a state.
	if rows[1].State != RunNoRuntime {
		t.Errorf("orphan state = %q, want %q", rows[1].State, RunNoRuntime)
	}
}

// An unreachable remote must be reported as unconfirmed, and its rows must
// carry that through, so nothing reads as current when it is not.
func TestBuildViewMarksUnconfirmedRemote(t *testing.T) {
	view := BuildView(BuildOptions{
		LocalMachine: "g14",
		Now:          time.Now(),
		SkipHealth:   true,
		Remotes: []RemoteMachineData{{
			Name: "mac", Link: LinkUnconfirmed, Detail: "ssh timeout",
			Agents: []AgentRow{{Name: "gmail-watcher", Role: RoleTriage, State: RunIdle}},
		}},
	})
	if len(view.Machines) != 1 {
		t.Fatalf("machines = %d, want 1", len(view.Machines))
	}
	machine := view.Machines[0]
	if machine.Link != LinkUnconfirmed {
		t.Errorf("link = %q, want %q", machine.Link, LinkUnconfirmed)
	}
	row := machine.Agents[0]
	if row.LinkState != LinkUnconfirmed {
		t.Errorf("row link = %q, want %q", row.LinkState, LinkUnconfirmed)
	}
	if row.Attention == "" {
		t.Error("an unconfirmed row carries no attention note")
	}
	if view.NeedAttention != 1 {
		t.Errorf("NeedAttention = %d, want 1", view.NeedAttention)
	}
}

func TestBuildTriggerRowRendersDeclaredNextDue(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	cron := buildTriggerRow(Trigger{
		Name: "hygiene", Type: TriggerCron, Schedule: "0 2 * * *", Timezone: "UTC",
		External: true, ExternalSource: "/x.timer",
	}, now)
	if cron.NextDue == nil {
		t.Fatal("a declared cron trigger produced no next-due time")
	}
	if !cron.NextDue.Equal(time.Date(2026, 8, 21, 2, 0, 0, 0, time.UTC)) {
		t.Errorf("next due = %s, want 2026-08-21 02:00 UTC", cron.NextDue)
	}

	// An interval owned by a launcher has no visible phase, so it must show
	// the cadence and say why that is all it can show.
	interval := buildTriggerRow(Trigger{
		Name: "tick", Type: TriggerCron, IntervalSeconds: 300,
		External: true, ExternalSource: "/x.timer",
	}, now)
	if interval.NextDue != nil {
		t.Error("an externally phased interval claimed an exact next-due time")
	}
	if interval.NextDueText != "every 5m" {
		t.Errorf("NextDueText = %q, want %q", interval.NextDueText, "every 5m")
	}
	if interval.Note == "" {
		t.Error("no note explaining why there is no exact time")
	}
}

func TestFingerprintOfNothingIsEmpty(t *testing.T) {
	if got := FingerprintPaths(nil); got != "" {
		t.Errorf("FingerprintPaths(nil) = %q, want an empty string", got)
	}
}

// --- helpers ------------------------------------------------------------

func validTestPost() *Post {
	post := NewPost("x", "post-x")
	post.Spec.Classification = ClassAgent
	post.Spec.Role = RoleRef{Name: RoleBuilder}
	return post
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// snapshotDir renders a directory's contents as a comparable string.
func snapshotDir(t *testing.T, root string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(root, path)
		sb.WriteString(rel)
		sb.WriteString("\x00")
		sb.Write(body)
		sb.WriteString("\x00")
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return sb.String()
}

func TestSystemdCalendarToCron(t *testing.T) {
	ok := map[string]string{
		"daily":          "0 0 * * *",
		"hourly":         "0 * * * *",
		"*-*-* 02:00:00": "0 2 * * *",
		"*-*-* 06:30":    "30 6 * * *",
		"*-*-* 23:59:00": "59 23 * * *",
	}
	for spec, want := range ok {
		got, converted := SystemdCalendarToCron(spec)
		if !converted {
			t.Errorf("SystemdCalendarToCron(%q) declined to convert", spec)
			continue
		}
		if got != want {
			t.Errorf("SystemdCalendarToCron(%q) = %q, want %q", spec, got, want)
		}
		if _, err := ParseCron(got, time.UTC); err != nil {
			t.Errorf("conversion of %q produced unparseable cron %q: %v", spec, got, err)
		}
	}

	// Forms with no exact cron equivalent must be declined, not approximated.
	for _, spec := range []string{"Mon..Fri *-*-* 09:00:00", "*-*-* 00/15:00:00", "*-*-* 02:00:30", "weird"} {
		if _, converted := SystemdCalendarToCron(spec); converted {
			t.Errorf("SystemdCalendarToCron(%q) claimed a conversion it cannot make exactly", spec)
		}
	}
}

// A systemd OnCalendar that cannot become cron must reach the view as an
// opaque trigger showing the real text, never as a cron that fails to parse.
func TestAdoptSystemdUnconvertibleCalendarStaysOpaque(t *testing.T) {
	dir := t.TempDir()
	service := filepath.Join(dir, "weekday.service")
	writeTestFile(t, service, "[Service]\nExecStart=/bin/true\n")
	writeTestFile(t, filepath.Join(dir, "weekday.timer"),
		"[Timer]\nOnCalendar=Mon..Fri *-*-* 09:00:00\n")

	plan, err := Adopt(Options{Target: service, Machine: "testbox"})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	triggers := plan.Definitions[0].Post.Spec.Triggers
	if len(triggers) != 1 {
		t.Fatalf("got %d triggers, want 1", len(triggers))
	}
	if triggers[0].Type != TriggerOpaque {
		t.Errorf("type = %q, want %q for an unconvertible OnCalendar", triggers[0].Type, TriggerOpaque)
	}
	if triggers[0].Schedule != "Mon..Fri *-*-* 09:00:00" {
		t.Errorf("schedule = %q, want the verbatim OnCalendar text", triggers[0].Schedule)
	}
}

// A unit belonging to a different agent must not be adopted as this one's,
// even when one name is a substring of the other.
func TestFindRelatedUnitsMatchesOnTokenBoundary(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "gmail-watcher.plist"), minimalPlist("gmail-watcher"))
	writeTestFile(t, filepath.Join(dir, "com.ashesh.repo-maintainer.plist"), minimalPlist("repo-maintainer"))

	// "mail-watcher" is a substring of "gmail-watcher" but not a token of it.
	if got := findRelatedUnits("mail-watcher", []string{dir}); len(got) != 0 {
		t.Errorf("mail-watcher matched %d units, want 0 — substring is not a reference", len(got))
	}
	if got := findRelatedUnits("gmail-watcher", []string{dir}); len(got) != 1 {
		t.Errorf("gmail-watcher matched %d units, want 1", len(got))
	}
	// A reverse-DNS label still matches on the dot boundary.
	if got := findRelatedUnits("repo-maintainer", []string{dir}); len(got) != 1 {
		t.Errorf("repo-maintainer matched %d units, want 1", len(got))
	}
}

// A .service and its paired .timer describe one firing, not two.
func TestAdoptDoesNotDoubleCountPairedTimer(t *testing.T) {
	unitDir := t.TempDir()
	writeTestFile(t, filepath.Join(unitDir, "repo-maintainer.service"),
		"[Service]\nExecStart=/bin/true\n")
	writeTestFile(t, filepath.Join(unitDir, "repo-maintainer.timer"),
		"[Timer]\nOnCalendar=*-*-* 02:00:00\n")

	plan, err := Adopt(Options{
		Target:   "repo-maintainer",
		Sessions: []SessionInfo{{ID: "s1", Title: "repo-maintainer", Tool: "claude"}},
		UnitDirs: []string{unitDir},
	})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	triggers := plan.Definitions[0].Post.Spec.Triggers
	if len(triggers) != 1 {
		t.Fatalf("got %d triggers, want 1 for one service+timer pair: %+v", len(triggers), triggers)
	}
	if findings := ValidatePost(plan.Definitions[0].Post); findings.HasErrors() {
		t.Errorf("duplicate trigger names slipped through: %v", findings)
	}
}

func minimalPlist(label string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>Label</key><string>` + label + `</string>
  <key>ProgramArguments</key><array><string>/bin/true</string></array>
  <key>StartInterval</key><integer>300</integer>
</dict>
</plist>
`
}
