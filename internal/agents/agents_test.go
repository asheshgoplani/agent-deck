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
	if src.ScheduleSource != filepath.Join(dir, "demo.timer") {
		t.Errorf("ScheduleSource = %q, want sibling timer", src.ScheduleSource)
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

func TestParseBareSystemdTimerReadsSiblingServiceRuntime(t *testing.T) {
	dir := t.TempDir()
	timer := filepath.Join(dir, "truth.timer")
	service := filepath.Join(dir, "truth.service")
	writeTestFile(t, timer, "[Timer]\nOnCalendar=*-*-* 03:15:00\n")
	writeTestFile(t, service, "[Service]\nExecStart=/bin/echo hello\nEnvironment=TOKEN_NAME=discard-me\nWorkingDirectory=/srv/truth\nRestart=on-failure\n")

	src, err := ParseSystemdUnit(timer)
	if err != nil {
		t.Fatalf("ParseSystemdUnit(timer): %v", err)
	}
	if src.Program != "/bin/echo" || src.WorkingDirectory != "/srv/truth" || src.RestartMode != "on-failure" {
		t.Fatalf("bare timer lost sibling service runtime: %+v", src)
	}
	if len(src.EnvKeys) != 1 || src.EnvKeys[0] != "TOKEN_NAME" {
		t.Fatalf("EnvKeys = %v, want names from sibling service", src.EnvKeys)
	}
	if len(src.SourcePaths) != 2 || src.SourcePaths[0] != timer || src.SourcePaths[1] != service {
		t.Fatalf("SourcePaths = %v, want timer and sibling service", src.SourcePaths)
	}
}

func TestAdoptSystemdPreservesEveryOnCalendar(t *testing.T) {
	dir := t.TempDir()
	timer := filepath.Join(dir, "many.timer")
	writeTestFile(t, timer, "[Timer]\nOnCalendar=Mon *-*-* 09:00:00\nOnCalendar=Fri *-*-* 17:00:00\n")

	plan, err := Adopt(Options{Target: timer, Machine: "testbox"})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	triggers := plan.Definitions[0].Post.Spec.Triggers
	if len(triggers) != 2 {
		t.Fatalf("got %d triggers, want both OnCalendar directives: %+v", len(triggers), triggers)
	}
	if triggers[0].Schedule == triggers[1].Schedule {
		t.Fatalf("distinct OnCalendar directives collapsed: %+v", triggers)
	}
}

func TestBareTimerFingerprintTracksTimerEdits(t *testing.T) {
	dir := t.TempDir()
	timer := filepath.Join(dir, "fingerprint.timer")
	service := filepath.Join(dir, "fingerprint.service")
	writeTestFile(t, timer, "[Timer]\nOnCalendar=*-*-* 01:00:00\n")
	writeTestFile(t, service, "[Service]\nExecStart=/bin/true\n")
	first, err := Adopt(Options{Target: timer, Machine: "testbox"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	writeTestFile(t, timer, "[Timer]\nOnCalendar=*-*-* 02:00:00\n")
	second, err := Adopt(Options{Target: timer, Machine: "testbox"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Definitions[0].Post.Spec.SourceFingerprint == second.Definitions[0].Post.Spec.SourceFingerprint {
		t.Fatal("timer-only edit did not change source fingerprint")
	}
}

func TestConfirmedAdoptionTreeDiffIsConfinedToDefinitionsStore(t *testing.T) {
	parent := t.TempDir()
	registry := filepath.Join(parent, "agents")
	sentinel := filepath.Join(parent, "outside.txt")
	writeTestFile(t, sentinel, "unchanged")
	source := filepath.Join(parent, "source.service")
	writeTestFile(t, source, "[Service]\nExecStart=/bin/true\n")
	plan, err := Adopt(Options{Target: source, Machine: "testbox"})
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, parent)
	if _, err := plan.WriteTo(registry); err != nil {
		t.Fatal(err)
	}
	after := snapshotTree(t, parent)
	for path, digest := range after {
		if before[path] == digest {
			continue
		}
		if path != "agents" && !strings.HasPrefix(path, "agents"+string(filepath.Separator)) {
			t.Errorf("confirmed adoption changed path outside definitions store: %s", path)
		}
	}
	for path := range before {
		if _, exists := after[path]; !exists && path != "agents" && !strings.HasPrefix(path, "agents"+string(filepath.Separator)) {
			t.Errorf("confirmed adoption removed path outside definitions store: %s", path)
		}
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			out[rel] = info.Mode().String()
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
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

// --- regressions from the phase-1 adversarial review ------------------------

// A running unit whose ExecStart uses %h must not be called debris. Three of
// the six real user units on g14 do this, and debris is the one label that
// invites a human to delete something.
func TestSystemdSpecifierPathIsNotDebris(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, filepath.Join(binDir, "ios"), "#!/bin/sh\n")

	dir := t.TempDir()
	service := filepath.Join(dir, "go-ios-tunnel.service")
	writeTestFile(t, service, "[Service]\nExecStart=%h/bin/ios tunnel start --userspace\n")

	src, err := ParseSystemdUnit(service)
	if err != nil {
		t.Fatalf("ParseSystemdUnit: %v", err)
	}
	if src.Program != filepath.Join(home, "bin", "ios") {
		t.Errorf("Program = %q, want %%h expanded to the home dir", src.Program)
	}
	if got := src.ProgramStatus(); got != ProgramPresent {
		t.Errorf("ProgramStatus = %q, want %q", got, ProgramPresent)
	}
	if got := ClassifyLaunchSource(src); got.Class == ClassDebris {
		t.Errorf("a running unit classified as debris: %+v", got)
	}
}

// A specifier this reader does NOT expand leaves the path unresolved, and an
// unresolved path is unknown — never missing.
func TestUnexpandedSpecifierIsUnknownNotMissing(t *testing.T) {
	dir := t.TempDir()
	service := filepath.Join(dir, "tmpl@.service")
	writeTestFile(t, service, "[Service]\nExecStart=%t/run/%i/helper\n")

	src, err := ParseSystemdUnit(service)
	if err != nil {
		t.Fatalf("ParseSystemdUnit: %v", err)
	}
	if got := src.ProgramStatus(); got != ProgramUnknown {
		t.Errorf("ProgramStatus = %q, want %q for an unexpanded specifier", got, ProgramUnknown)
	}
	if got := ClassifyLaunchSource(src); got.Class == ClassDebris {
		t.Errorf("an unresolvable path classified as debris: %+v", got)
	}
}

// Credentials in an adopted role body must never be copied into the registry,
// including the shapes a prefix-only detector misses.
func TestAdoptDoesNotCopyCredentialBearingRoleBody(t *testing.T) {
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "CLAUDE.md"), strings.Join([]string{
		"You are the release conductor.",
		"Login to imap.gmail.com with the app password abcd efgh ijkl mnop",
		"The API key for the bridge is 7f3a-91b2-cc40",
		"GMAIL_APP_PASSWORD=abcd efgh ijkl mnop",
		"export SLACK_TOKEN=xoxb-4242424242-abcdefgh",
	}, "\n"))
	writeTestFile(t, filepath.Join(source, "POLICY.md"),
		"Never put a token or password in a role directory. Escalate on ambiguity.\n")

	plan, err := Adopt(Options{Target: source, Machine: "testbox"})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	def := plan.Definitions[0]

	if _, copied := def.RoleFiles["INSTRUCTIONS.md"]; copied {
		t.Error("a credential-bearing body was copied into the role")
	}
	// The policy file only TALKS about tokens; it must still be adopted.
	if _, copied := def.RoleFiles["POLICY.md"]; !copied {
		t.Error("prose about credentials was mistaken for a credential")
	}
	// Write it out and prove no secret material reaches disk.
	root := t.TempDir()
	if _, err := plan.WriteTo(root); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	var found []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, secret := range []string{"xoxb-", "abcd efgh", "7f3a-91b2"} {
			if strings.Contains(string(body), secret) {
				found = append(found, path+" contains "+secret)
			}
		}
		return nil
	})
	if len(found) > 0 {
		t.Errorf("secret material reached the registry: %v", found)
	}
	// And the definition says why the file is absent.
	if len(def.Post.Spec.Unresolved) == 0 ||
		!strings.Contains(strings.Join(def.Post.Spec.Unresolved, " "), "credential") {
		t.Errorf("no unresolved entry explains the missing file: %v", def.Post.Spec.Unresolved)
	}
}

func TestScanForCredentialsShapes(t *testing.T) {
	catches := []string{
		"Login with the app password abcd efgh ijkl mnop",
		"The API key for the bridge is 7f3a-91b2-cc40",
		"GMAIL_APP_PASSWORD=abcd efgh ijkl mnop",
		"export SLACK_TOKEN=xoxb-4242424242-abcdefgh",
		"token: ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"-----BEGIN RSA PRIVATE KEY-----",
	}
	for _, line := range catches {
		if len(ScanForCredentials(line)) == 0 {
			t.Errorf("ScanForCredentials missed: %q", line)
		}
	}

	// Policy prose must not be mistaken for a leak.
	clean := []string{
		"Never put a token or password in a role directory.",
		"Ask the user for the API key rather than storing it.",
		"Escalate to the human on ambiguity.",
		"Run the pr-review workflow before merging.",
	}
	for _, line := range clean {
		if lines := ScanForCredentials(line); len(lines) != 0 {
			t.Errorf("ScanForCredentials false-positived on: %q", line)
		}
	}
}

// launchd ANDs Day and Weekday; cron ORs them. No cron string is correct, so
// none is emitted.
func TestLaunchdDayAndWeekdayProducesNoSchedule(t *testing.T) {
	dir := t.TempDir()
	plist := filepath.Join(dir, "monthly-monday.plist")
	writeTestFile(t, plist, `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>Label</key><string>monthly-monday</string>
  <key>ProgramArguments</key><array><string>/bin/true</string></array>
  <key>StartCalendarInterval</key>
  <dict>
    <key>Day</key><integer>1</integer>
    <key>Weekday</key><integer>1</integer>
    <key>Hour</key><integer>3</integer>
    <key>Minute</key><integer>0</integer>
  </dict>
</dict>
</plist>
`)
	src, err := ParseLaunchdPlist(plist)
	if err != nil {
		t.Fatalf("ParseLaunchdPlist: %v", err)
	}
	if src.CalendarSpec != "" {
		t.Errorf("CalendarSpec = %q, want empty: launchd ANDs Day and Weekday, cron ORs them", src.CalendarSpec)
	}
	if len(src.Warnings) == 0 {
		t.Error("no warning explains why no schedule was produced")
	}
}

// An uncontacted machine must not be reported as a healthy link, and a post
// placed elsewhere must not be resolved against the LOCAL session index.
func TestBuildViewNeverClaimsLinkOKForUncontactedMachine(t *testing.T) {
	post := NewPost("mac-thing", "post-m")
	post.Spec.Classification = ClassAgent
	post.Spec.Role = RoleRef{Name: RoleTriage}
	post.Spec.Placement.Machine = "mac-studio"
	post.Spec.Runtime.AdoptedSessionID = "sess-elsewhere"

	view := BuildView(BuildOptions{
		Definitions:   []*Definition{{Name: "mac-thing", Post: post}},
		SessionStates: map[string]SessionState{},
		LocalMachine:  "g14",
		Now:           time.Now(),
		SkipHealth:    true,
	})
	if len(view.Machines) != 1 {
		t.Fatalf("machines = %d, want 1", len(view.Machines))
	}
	if view.Machines[0].Link != LinkNotContacted {
		t.Errorf("link = %q, want %q — nothing was contacted", view.Machines[0].Link, LinkNotContacted)
	}
	if got := view.Machines[0].Agents[0].State; got != RunUnknown {
		t.Errorf("state = %q, want %q: a remote post cannot be resolved against the local session index", got, RunUnknown)
	}
}

// The renderer must be told when a registry record breaks a phase-1 invariant.
func TestBuildViewFlagsArmedRegistryRecord(t *testing.T) {
	post := NewPost("armed", "post-a")
	post.Spec.Classification = ClassAgent
	post.Spec.Role = RoleRef{Name: RoleBuilder}
	post.Spec.Triggers = []Trigger{{
		Name: "cron", Type: TriggerCron, Schedule: "*/5 * * * *",
		Enabled: true, External: false,
	}}

	view := BuildView(BuildOptions{
		Definitions:  []*Definition{{Name: "armed", Post: post}},
		LocalMachine: "g14",
		Now:          time.Now(),
		SkipHealth:   true,
	})
	row := view.Machines[0].Agents[0]
	if len(row.Violations) == 0 {
		t.Error("an armed registry record produced no violation")
	}
	if !row.Armed() {
		t.Error("Armed() did not report an armed trigger")
	}
	if row.Attention == "" {
		t.Error("an armed record is not called out for attention")
	}
}

func TestValidateReportsToFlagsOrphanChain(t *testing.T) {
	orphan := NewPost("a", "post-a")
	orphan.Spec.Placement.ReportsTo = "b"
	parent := NewPost("b", "post-b")
	parent.Spec.Placement.ReportsTo = "c" // c does not exist

	findings := ValidateReportsTo([]*Post{orphan, parent})
	if len(findings) == 0 {
		t.Fatal("a chain that never reaches a human principal produced no finding")
	}
}

func TestPlaceholderPatternCatchesShellShapes(t *testing.T) {
	for _, deliver := range []string{
		"New mail from {{sender}}",
		"File ${PATH} changed",
		"Subject $SUBJECT arrived",
		"Run $(whoami)",
		"Run `id`",
		"Path %(path)s changed",
		"Got %s",
	} {
		post := validTestPost()
		post.Spec.Triggers = []Trigger{{
			Name: "t", Type: TriggerMailDoorbell, External: true,
			ExternalSource: "/x.plist", Deliver: deliver,
		}}
		if !ValidatePost(post).HasErrors() {
			t.Errorf("interpolated delivery accepted: %q", deliver)
		}
	}
}

func TestDescribeIntervalDoesNotFloorAwaySeconds(t *testing.T) {
	if got := DescribeInterval(90); got == "every 1m" {
		t.Errorf("DescribeInterval(90) = %q, which is a wrong cadence, not a coarse one", got)
	}
	if got := DescribeInterval(300); got != "every 5m" {
		t.Errorf("DescribeInterval(300) = %q, want %q", got, "every 5m")
	}
	if got := DescribeInterval(7200); got != "every 2h" {
		t.Errorf("DescribeInterval(7200) = %q, want %q", got, "every 2h")
	}
}

func TestCheckHealthFutureMtimeIsNotFresh(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen.db")
	writeTestFile(t, seen, "x")
	future := now.Add(48 * time.Hour)
	if err := os.Chtimes(seen, future, future); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	got := CheckHealth("c", "mail", dir, 30*time.Minute, now)
	if got.State == HealthOK {
		t.Errorf("a file dated in the future reported %q; that is a clock problem, not proof of work", got.State)
	}
}

func TestWriteToRefusesNonEmptyDirectory(t *testing.T) {
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "CLAUDE.md"), "conductor\n")
	plan, err := Adopt(Options{Target: source, Machine: "testbox"})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}

	root := t.TempDir()
	// A directory holding role content but NO agent.yaml must still be safe.
	occupied := filepath.Join(root, plan.Definitions[0].Name, RoleDirName)
	if err := os.MkdirAll(occupied, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, filepath.Join(occupied, "INSTRUCTIONS.md"), "someone's edit\n")

	if _, err := plan.WriteTo(root); err == nil {
		t.Fatal("WriteTo overwrote a non-empty directory that had no agent.yaml")
	}
	body, readErr := os.ReadFile(filepath.Join(occupied, "INSTRUCTIONS.md"))
	if readErr != nil || string(body) != "someone's edit\n" {
		t.Error("the existing file was modified")
	}
}

func TestSanitizeForDisplayStripsControlCharacters(t *testing.T) {
	got := SanitizeForDisplay("link\x1b[2K\rok\ttab\x07")
	if strings.ContainsAny(got, "\x1b\r\x07") {
		t.Errorf("SanitizeForDisplay left control characters: %q", got)
	}
	if !strings.Contains(got, "tab") {
		t.Errorf("SanitizeForDisplay dropped ordinary text: %q", got)
	}
}

// --- regressions from review round 2 ---------------------------------------

// A launchd schedule with no cron equivalent must still produce a trigger.
// Declining the schedule is right; dropping the trigger understates the fleet.
func TestLaunchdUnrepresentableScheduleStillEmitsOpaqueTrigger(t *testing.T) {
	dir := t.TempDir()
	plist := filepath.Join(dir, "com.ashesh.daywk.plist")
	writeTestFile(t, plist, `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>Label</key><string>com.ashesh.daywk</string>
  <key>ProgramArguments</key><array><string>/bin/true</string></array>
  <key>StartCalendarInterval</key>
  <dict>
    <key>Day</key><integer>1</integer>
    <key>Weekday</key><integer>1</integer>
    <key>Hour</key><integer>3</integer>
  </dict>
</dict>
</plist>
`)
	plan, err := Adopt(Options{Target: plist, Machine: "testbox"})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	triggers := plan.Definitions[0].Post.Spec.Triggers
	if len(triggers) != 1 {
		t.Fatalf("got %d triggers, want 1: a plist that demonstrably fires must not show as firing nothing", len(triggers))
	}
	if triggers[0].Type != TriggerOpaque {
		t.Errorf("type = %q, want %q", triggers[0].Type, TriggerOpaque)
	}
	if !strings.Contains(triggers[0].Schedule, "Day=1") {
		t.Errorf("schedule = %q, want the source's own terms", triggers[0].Schedule)
	}
	// And the row renders that, rather than an empty next-due.
	view := BuildView(BuildOptions{
		Definitions:  []*Definition{{Name: "daywk", Post: plan.Definitions[0].Post}},
		LocalMachine: "testbox", Now: time.Now(), SkipHealth: true,
	})
	if got := FormatNextDue(view.Machines[0].Agents[0]); got == "" {
		t.Error("the row shows no schedule at all for a post that is demonstrably fired")
	}
}

// A system-scope unit's %h belongs to its User=, not to the adopting process.
func TestSystemScopeUnitDoesNotExpandOurIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := t.TempDir()
	userUnit := filepath.Join(dir, "mine.service")
	writeTestFile(t, userUnit, "[Service]\nExecStart=%h/bin/tool\n")
	src, err := ParseSystemdUnit(userUnit)
	if err != nil {
		t.Fatalf("ParseSystemdUnit: %v", err)
	}
	if !strings.HasPrefix(src.Program, home) {
		t.Errorf("a user unit did not expand %%h: %q", src.Program)
	}

	// A unit that sets User= runs as someone else.
	otherUser := filepath.Join(dir, "theirs.service")
	writeTestFile(t, otherUser, "[Service]\nUser=svc\nExecStart=%h/bin/tool\n")
	src, err = ParseSystemdUnit(otherUser)
	if err != nil {
		t.Fatalf("ParseSystemdUnit: %v", err)
	}
	if strings.HasPrefix(src.Program, home) {
		t.Errorf("expanded %%h with OUR home for a unit that runs as another user: %q", src.Program)
	}
	if got := src.ProgramStatus(); got != ProgramUnknown {
		t.Errorf("ProgramStatus = %q, want %q — and it must never be debris", got, ProgramUnknown)
	}
	if got := ClassifyLaunchSource(src); got.Class == ClassDebris {
		t.Error("a unit running as another user was classified debris")
	}
}

// A broken escalation chain must actually reach the user.
func TestBuildViewSurfacesBrokenReportsToChain(t *testing.T) {
	orphan := NewPost("worker", "post-w")
	orphan.Spec.Classification = ClassAgent
	orphan.Spec.Role = RoleRef{Name: RoleBuilder}
	orphan.Spec.Placement.ReportsTo = "nobody-post"

	view := BuildView(BuildOptions{
		Definitions:  []*Definition{{Name: "worker", Post: orphan}},
		LocalMachine: "g14", Now: time.Now(), SkipHealth: true,
	})
	row := view.Machines[0].Agents[0]
	if row.ReportsToIssue == "" {
		t.Error("a post reporting to a nonexistent manager rendered with no complaint")
	}
}

// A schema complaint must not hide a dead connector.
func TestAttentionNamesBothInvariantAndConnector(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "pid"), "999999\n")

	post := NewPost("both", "post-b")
	post.Spec.Classification = ClassAgent
	post.Spec.Role = RoleRef{Name: RoleTriage}
	post.Spec.Triggers = []Trigger{{Name: "t", Type: TriggerCron, Enabled: true, External: false}}
	post.Spec.Connectors = []ConnectorRef{{Name: "mail", Kind: "mail", EvidencePath: dir}}

	view := BuildView(BuildOptions{
		Definitions:  []*Definition{{Name: "both", Post: post}},
		LocalMachine: "g14", Now: time.Now(),
	})
	attention := view.Machines[0].Agents[0].Attention
	if !strings.Contains(attention, "invariant") {
		t.Errorf("attention %q does not mention the invariant", attention)
	}
	if !strings.Contains(attention, "connector") {
		t.Errorf("attention %q hides the dead connector behind the schema complaint", attention)
	}
}

// --- regressions from review round 3 ---------------------------------------

// A legitimate manager reference must not be reported as unknown, and a real
// cycle must reach the row's attention (and therefore the "need attention"
// count) rather than living only in --json.
func TestReportsToFindingsNeedTheWholeSet(t *testing.T) {
	mgr := NewPost("mgr", "post-mgr")
	mgr.Spec.Classification = ClassAgent
	mgr.Spec.Role = RoleRef{Name: RoleManager}
	mgr.Spec.Placement.ReportsTo = PrincipalHuman

	wrk := NewPost("wrk", "post-wrk")
	wrk.Spec.Classification = ClassAgent
	wrk.Spec.Role = RoleRef{Name: RoleBuilder}
	wrk.Spec.Placement.ReportsTo = "mgr"

	full := BuildView(BuildOptions{
		Definitions:  []*Definition{{Name: "mgr", Post: mgr}, {Name: "wrk", Post: wrk}},
		LocalMachine: "g14", Now: time.Now(), SkipHealth: true,
	})
	for _, row := range full.Machines[0].Agents {
		if row.ReportsToIssue != "" {
			t.Errorf("%s warned about a manager that is in the same registry: %s", row.Name, row.ReportsToIssue)
		}
	}

	// A real cycle must be loud in the list, not just in JSON.
	a := NewPost("cyc-a", "post-a")
	a.Spec.Classification = ClassAgent
	a.Spec.Role = RoleRef{Name: RoleTriage}
	a.Spec.Placement.ReportsTo = "cyc-b"
	b := NewPost("cyc-b", "post-b")
	b.Spec.Classification = ClassAgent
	b.Spec.Role = RoleRef{Name: RoleManager}
	b.Spec.Placement.ReportsTo = "cyc-a"

	cycle := BuildView(BuildOptions{
		Definitions:  []*Definition{{Name: "cyc-a", Post: a}, {Name: "cyc-b", Post: b}},
		LocalMachine: "g14", Now: time.Now(), SkipHealth: true,
	})
	if cycle.NeedAttention == 0 {
		t.Error("a reports_to cycle produced no attention; the list would show nothing")
	}
	for _, row := range cycle.Machines[0].Agents {
		if row.Attention == "" {
			t.Errorf("%s: a cycle is invisible on the row", row.Name)
		}
	}
}
