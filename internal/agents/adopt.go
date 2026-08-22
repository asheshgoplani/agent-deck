package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxRoleFileBytes caps a copied role body. A role is instructions and policy;
// anything larger is almost certainly a log or a database that wandered into
// the directory, and copying it would bloat every export.
const maxRoleFileBytes = 512 * 1024

// SessionInfo is the subset of an agent-deck session record adoption needs.
// The caller supplies these, which keeps this package free of the session
// store and makes every resolver testable from a literal.
type SessionInfo struct {
	ID          string
	Title       string
	Tool        string
	Account     string
	GroupPath   string
	ProjectPath string
	Command     string
	Status      string
	IsConductor bool
	Machine     string
}

// ConductorBlock is the `[conductors.<name>]` configuration for a conductor,
// supplied by the caller from the config the binary actually reads.
type ConductorBlock struct {
	Name    string
	Account string
	Tool    string
	// Source is the config file the block came from, for the evidence map.
	Source string
}

// Options controls one adoption run.
type Options struct {
	// Target is a conductor directory, a session id or title, a launchd
	// plist, or a systemd unit.
	Target string
	// ManagerPost is the post name that non-manager roles report to. Empty
	// means everything reports to the human principal directly.
	ManagerPost string
	// Sessions is the local session fleet, used by the session resolver and
	// to correlate a conductor directory with its live runtime.
	Sessions []SessionInfo
	// ConductorBlocks maps conductor name to its config block.
	ConductorBlocks map[string]ConductorBlock
	// UnitDirs are searched for a plist or unit that references the target.
	UnitDirs []string
	// Machine labels the machine this adoption ran on.
	Machine string
	// Now is injected so reports are reproducible under test.
	Now time.Time
}

func (o Options) now() time.Time {
	if o.Now.IsZero() {
		return time.Now().UTC()
	}
	return o.Now.UTC()
}

// Planned is one definition adoption proposes to write.
type Planned struct {
	Name      string            `json:"name"`
	Post      *Post             `json:"post"`
	Role      *Role             `json:"role,omitempty"`
	RoleFiles map[string][]byte `json:"-"`
	Report    string            `json:"-"`
	Findings  Findings          `json:"findings,omitempty"`
}

// Plan is the full result of an adoption. Producing a plan never writes
// anything; WriteTo is a separate, explicit step.
type Plan struct {
	Target      string     `json:"target"`
	TargetKind  string     `json:"target_kind"`
	Definitions []*Planned `json:"definitions"`
	Notes       []string   `json:"notes,omitempty"`
	SourcePaths []string   `json:"source_paths,omitempty"`
}

// Adopt introspects a target and produces a plan of disabled definitions.
//
// It is strictly read-only with respect to the target: it opens files for
// reading, stats paths, and reads the session records the caller handed it. It
// does not unload a plist, edit a unit, move a credential, or touch a live
// session.
func Adopt(opts Options) (*Plan, error) {
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		return nil, fmt.Errorf("adopt: empty target")
	}

	switch detectTargetKind(target) {
	case "launchd":
		return adoptLaunchd(target, opts)
	case "systemd":
		return adoptSystemd(target, opts)
	case "conductor-dir":
		return adoptConductorDir(target, opts)
	default:
		return adoptSession(target, opts)
	}
}

// detectTargetKind resolves what the user pointed at.
func detectTargetKind(target string) string {
	switch strings.ToLower(filepath.Ext(target)) {
	case ".plist":
		return "launchd"
	case ".service", ".timer":
		return "systemd"
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return "conductor-dir"
	}
	return "session"
}

// --- conductor directory ------------------------------------------------

// conductorRoleFiles maps a source filename to its portable role filename. The
// design's migration keeps the useful separation and only renames the entry
// point.
var conductorRoleFiles = []struct {
	source string
	target string
	field  string
}{
	{"CLAUDE.md", "INSTRUCTIONS.md", "role.spec.entrypoint"},
	{"AGENTS.md", "INSTRUCTIONS.md", "role.spec.entrypoint"},
	{"INSTRUCTIONS.md", "INSTRUCTIONS.md", "role.spec.entrypoint"},
	{"POLICY.md", "POLICY.md", "role.spec.policy[0]"},
	{"PR-POLICY.md", "PR-POLICY.md", "role.spec.policy[1]"},
	{"LEARNINGS.md", "LEARNINGS.md", "role.spec.learnings.file"},
}

func adoptConductorDir(dir string, opts Options) (*Plan, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("adopt conductor dir: %w", err)
	}
	name := sanitizeName(filepath.Base(absDir))
	plan := &Plan{Target: absDir, TargetKind: "conductor-dir"}

	classified := ClassifyRole(filepath.Base(absDir), false)
	// A directory carrying CLAUDE.md/POLICY.md is conductor-shaped whatever
	// it is called; the org chart puts conductors at manager.
	if _, statErr := os.Stat(filepath.Join(absDir, "CLAUDE.md")); statErr == nil && classified.Role == RoleUnresolved {
		classified = ClassifyResult{
			Class: ClassAgent, Role: RoleManager, Confidence: ConfidenceMedium,
			Reason: "directory holds CLAUDE.md, the conductor instruction shape",
		}
	}

	post := NewPost(name, postID(name, absDir))
	post.Spec.Classification = classified.Class
	post.Spec.AdoptedFrom = absDir
	post.Spec.AdoptedAt = opts.now()
	post.Spec.Placement.Machine = opts.Machine
	post.Spec.Placement.Project = absDir
	post.Spec.Placement.ReportsTo = ReportsToFor(classified.Role, opts.ManagerPost)
	post.AddInference("spec.classification", string(classified.Class), absDir, classified.Confidence, classified.Reason)
	post.AddInference("spec.role.name", classified.Role, absDir, classified.Confidence, classified.Reason)
	post.AddEvidence("spec.placement.project", absDir, "adoption target path", ConfidenceHigh, "")

	role := NewRole(classified.Role, "0.1.0")
	role.Spec.RequiresAgentDeck = ">=1.14.0"
	role.Spec.Description = "adopted from " + filepath.Base(absDir)
	role.Spec.Digests = map[string]string{}
	roleFiles := map[string][]byte{}
	var findings Findings
	var sourcePaths []string

	seenTargets := map[string]bool{}
	for _, mapping := range conductorRoleFiles {
		sourcePath := filepath.Join(absDir, mapping.source)
		body, readErr := readCapped(sourcePath)
		if readErr != nil {
			continue
		}
		if seenTargets[mapping.target] {
			continue
		}
		seenTargets[mapping.target] = true
		sourcePaths = append(sourcePaths, sourcePath)

		contentFindings := ValidateRoleContent("role/"+mapping.target, string(body))
		if contentFindings.HasErrors() {
			// The body carries something that looks like a credential. Copying
			// it would put that secret in a second place on disk, so the file
			// is left where it is and the definition records why it is absent.
			// The source is not touched: fixing it is the user's call.
			//
			// The finding is downgraded to a warning here because it has been
			// ACTED on — the leak cannot happen now. Leaving it fatal would
			// refuse the whole definition and leave the user with no inventory
			// of an agent that plainly exists, which is the opposite of what
			// this phase is for.
			findings = append(findings, handledCredentialFindings(contentFindings)...)
			post.AddUnresolved(mapping.source + " was not copied into the role because it looks like it " +
				"contains a credential; move the value to a connector's private store, then re-adopt")
			continue
		}
		findings = append(findings, contentFindings...)

		roleFiles[mapping.target] = body
		role.Spec.Digests[mapping.target] = Digest(body)

		switch {
		case mapping.target == "INSTRUCTIONS.md":
			role.Spec.Entrypoint = "INSTRUCTIONS.md"
		case mapping.target == "LEARNINGS.md":
			role.Spec.Learnings = &LearningsSpec{File: "LEARNINGS.md", Promotion: "reviewed-version-change"}
		default:
			role.Spec.Policy = append(role.Spec.Policy, mapping.target)
		}
		post.AddEvidence("role."+mapping.target, mapping.source, sourcePath, ConfidenceHigh, "")
	}
	sort.Strings(role.Spec.Policy)

	// workflows/ becomes named workflows, verbatim. The design is explicit
	// that a workflow is the user's Markdown, not a DSL we translate it into.
	workflowDir := filepath.Join(absDir, "workflows")
	if entries, dirErr := os.ReadDir(workflowDir); dirErr == nil {
		role.Spec.Workflows = map[string]string{}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
				continue
			}
			sourcePath := filepath.Join(workflowDir, entry.Name())
			body, readErr := readCapped(sourcePath)
			if readErr != nil {
				findings.warnf("role/workflows/"+entry.Name(), "%v", readErr)
				continue
			}
			rel := filepath.Join("workflows", entry.Name())
			workflowName := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			sourcePaths = append(sourcePaths, sourcePath)

			contentFindings := ValidateRoleContent("role/"+rel, string(body))
			if contentFindings.HasErrors() {
				findings = append(findings, handledCredentialFindings(contentFindings)...)
				post.AddUnresolved("workflows/" + entry.Name() + " was not copied into the role because it " +
					"looks like it contains a credential; move the value to a connector's private store, then re-adopt")
				continue
			}
			findings = append(findings, contentFindings...)

			role.Spec.Workflows[workflowName] = rel
			roleFiles[rel] = body
			role.Spec.Digests[rel] = Digest(body)
		}
	}

	if role.Spec.Entrypoint == "" {
		post.AddUnresolved("no instruction file found; the role has no entry point yet")
	}

	// The role must be set before bindings run: applyConductorBindings folds
	// in any unit that fires this post, and the trigger's fixed delivery
	// string is chosen from the role name. Assigning it afterwards gave every
	// adopted conductor the generic fallback string.
	post.Spec.Role = RoleRef{Name: classified.Role, Path: "./" + RoleDirName, Version: role.Metadata.Version}

	// Correlate the live runtime, the config block, and any unit that fires it.
	applyConductorBindings(post, absDir, name, opts, &sourcePaths)

	if classified.Role == RoleUnresolved {
		post.AddUnresolved("role is unresolved; a human must say what this agent is for")
	}
	post.Spec.SourceFingerprint = FingerprintPaths(sourcePaths)

	planned := &Planned{Name: name, Post: post, Role: role, RoleFiles: roleFiles}
	planned.Findings = append(findings, ValidateDefinition(post, role)...)
	planned.Report = renderReport(planned, plan.TargetKind, absDir, classified)
	plan.Definitions = append(plan.Definitions, planned)
	plan.SourcePaths = sourcePaths

	if pair := PairFor(classified.Role); pair != "" {
		plan.Notes = append(plan.Notes, fmt.Sprintf(
			"role %s is half of a pair; its %s seat is unfilled on this machine", classified.Role, pair))
	}
	return plan, nil
}

// applyConductorBindings folds the config block, the live session, and any
// firing unit into a conductor post.
func applyConductorBindings(post *Post, absDir, name string, opts Options, sourcePaths *[]string) {
	if block, ok := opts.ConductorBlocks[name]; ok {
		if block.Account != "" {
			post.Spec.Runtime.Account = block.Account
			post.AddEvidence("spec.runtime.account", block.Account, block.Source, ConfidenceHigh, "")
		}
		if block.Tool != "" {
			post.Spec.Runtime.Harness = block.Tool
			post.AddEvidence("spec.runtime.harness", block.Tool, block.Source, ConfidenceHigh, "")
		}
	}

	for _, s := range opts.Sessions {
		if !sessionMatchesConductor(s, name, absDir) {
			continue
		}
		post.Spec.Runtime.AdoptedSessionID = s.ID
		post.Metadata.Title = s.Title
		post.AddEvidence("spec.runtime.adoptedSessionId", s.ID, "agent-deck session record", ConfidenceHigh,
			"provenance only; phase 1 does not take the session over")
		if post.Spec.Runtime.Harness == "" && s.Tool != "" {
			post.Spec.Runtime.Harness = s.Tool
			post.AddEvidence("spec.runtime.harness", s.Tool, "agent-deck session record", ConfidenceHigh, "")
		}
		if post.Spec.Runtime.Account == "" && s.Account != "" {
			post.Spec.Runtime.Account = s.Account
			post.AddEvidence("spec.runtime.account", s.Account, "agent-deck session record", ConfidenceHigh, "")
		}
		if s.GroupPath != "" {
			post.Spec.Placement.Group = s.GroupPath
			post.AddEvidence("spec.placement.group", s.GroupPath, "agent-deck session record", ConfidenceHigh, "")
		}
		break
	}

	for _, unit := range findRelatedUnits(name, opts.UnitDirs) {
		*sourcePaths = append(*sourcePaths, launchSourcePaths(unit)...)
		applyUnitToPost(post, unit)
	}
}

func sessionMatchesConductor(s SessionInfo, name, dir string) bool {
	if s.IsConductor && sanitizeName(s.Title) == name {
		return true
	}
	if s.ProjectPath != "" && filepath.Clean(s.ProjectPath) == filepath.Clean(dir) {
		return true
	}
	return sanitizeName(s.Title) == name
}

// findRelatedUnits scans the caller's unit directories for a plist or unit
// whose name refers to the target. It reads; it never loads or unloads.
func findRelatedUnits(name string, dirs []string) []*LaunchSource {
	var found []*LaunchSource
	matcher, err := unitNameMatcher(name)
	if err != nil {
		return nil
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			lower := strings.ToLower(entry.Name())
			if !matcher.MatchString(unitStem(lower)) {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			var (
				src *LaunchSource
				err error
			)
			switch {
			case strings.HasSuffix(lower, ".plist"):
				src, err = ParseLaunchdPlist(path)
			case strings.HasSuffix(lower, ".service"), strings.HasSuffix(lower, ".timer"):
				src, err = ParseSystemdUnit(path)
			default:
				continue
			}
			if err == nil && src != nil {
				found = append(found, src)
			}
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Path < found[j].Path })
	return dropPairedTimers(found)
}

// dropPairedTimers removes a systemd .timer whose sibling .service was also
// found. ParseSystemdUnit already folds a paired timer's schedule into its
// service, so keeping both would emit the same firing twice — once as
// "x.service" and once as "x.timer" — and a fleet view that double-counts a
// schedule is worse than one that misses it.
func dropPairedTimers(sources []*LaunchSource) []*LaunchSource {
	services := map[string]bool{}
	for _, src := range sources {
		if strings.HasSuffix(src.Path, ".service") {
			services[strings.TrimSuffix(src.Path, ".service")] = true
		}
	}
	kept := make([]*LaunchSource, 0, len(sources))
	for _, src := range sources {
		if strings.HasSuffix(src.Path, ".timer") && services[strings.TrimSuffix(src.Path, ".timer")] {
			continue
		}
		kept = append(kept, src)
	}
	return kept
}

// applyUnitToPost turns a firing unit into declared, external, disabled
// triggers on the post.
func applyUnitToPost(post *Post, unit *LaunchSource) {
	base := sanitizeName(unit.Label)
	scheduleSource := unit.ScheduleSource
	if scheduleSource == "" {
		scheduleSource = unit.Path
	}
	switch {
	case len(unit.CalendarSpecs) > 0 || unit.CalendarSpec != "":
		// launchd already produced cron; systemd produces OnCalendar syntax,
		// which only sometimes has an exact cron equivalent. A spec that
		// cannot be converted is recorded verbatim as an opaque trigger so
		// the row shows the real schedule text instead of a cron expression
		// that would never parse.
		specs := unit.CalendarSpecs
		if len(specs) == 0 {
			specs = []string{unit.CalendarSpec}
		}
		for i, spec := range specs {
			name := base + "-calendar"
			if len(specs) > 1 {
				name = fmt.Sprintf("%s-calendar-%d", base, i+1)
			}
			trigger := Trigger{
				Name: name, Type: TriggerCron, Schedule: spec,
				Enabled: false, External: true, ExternalSource: scheduleSource,
				Deliver: fixedDeliveryFor(post.Spec.Role.Name),
			}
			note := "the unit still owns this firing; agent-deck only displays it"
			if unit.Kind == "systemd" {
				if converted, ok := SystemdCalendarToCron(spec); ok {
					trigger.Schedule = converted
				} else {
					trigger.Type = TriggerOpaque
					note = "OnCalendar=" + spec + " has no exact cron equivalent; no next-due time is computed"
				}
			}
			post.Spec.Triggers = append(post.Spec.Triggers, trigger)
			post.AddEvidence("spec.triggers."+name, spec, scheduleSource, ConfidenceHigh, note)
		}
	case unit.IntervalSeconds > 0:
		post.Spec.Triggers = append(post.Spec.Triggers, Trigger{
			Name: base + "-interval", Type: TriggerCron, IntervalSeconds: unit.IntervalSeconds,
			Enabled: false, External: true, ExternalSource: scheduleSource,
			Deliver: fixedDeliveryFor(post.Spec.Role.Name),
		})
		post.AddEvidence("spec.triggers."+base+"-interval", fmt.Sprintf("every %ds", unit.IntervalSeconds), scheduleSource,
			ConfidenceHigh, "a poll cadence is not proof of the desired business cadence")
	case unit.KeepAlive:
		post.Spec.Triggers = append(post.Spec.Triggers, Trigger{
			Name: base + "-keepalive", Type: TriggerOpaque,
			Enabled: false, External: true, ExternalSource: unit.Path,
		})
		post.AddEvidence("spec.triggers."+base+"-keepalive", "KeepAlive", unit.Path, ConfidenceHigh,
			"kept alive by the launcher rather than fired on a schedule")
	case unit.HasUnrepresentableSchedule:
		// The source demonstrably fires this post; we just cannot express its
		// schedule. Declining the SCHEDULE is right, but dropping the TRIGGER
		// would show an agent with nothing firing it — understating the fleet,
		// which is the failure this phase exists to avoid. Same vocabulary the
		// systemd path already uses for an inexpressible OnCalendar.
		post.Spec.Triggers = append(post.Spec.Triggers, Trigger{
			Name: base + "-calendar", Type: TriggerOpaque,
			Schedule: unit.RawScheduleText,
			Enabled:  false, External: true, ExternalSource: scheduleSource,
			Deliver: fixedDeliveryFor(post.Spec.Role.Name),
		})
		post.AddEvidence("spec.triggers."+base+"-calendar", unit.RawScheduleText, scheduleSource, ConfidenceHigh,
			"this fires on a schedule with no exact cron equivalent; no next-due time is computed")
	}

	if unit.RestartMode != "" {
		post.Spec.RestartPolicy = &RestartPolicy{Mode: unit.RestartMode}
		post.AddEvidence("spec.restartPolicy.mode", unit.RestartMode, unit.Path, ConfidenceHigh, "")
	}
	for _, key := range unit.EnvKeys {
		if secretPattern.MatchString(key) {
			post.AddUnresolved("unit " + filepath.Base(unit.Path) + " depends on environment key " + key +
				"; bind it as a connector credential rather than copying the value")
		}
	}
	for _, envFile := range unit.EnvFiles {
		post.AddUnresolved("unit " + filepath.Base(unit.Path) + " reads env file " + envFile +
			"; its contents were not read and must not be copied into a definition")
	}
	for _, warning := range unit.Warnings {
		post.AddEvidence("source."+filepath.Base(unit.Path), "", unit.Path, ConfidenceLow, warning)
	}
}

func launchSourcePaths(unit *LaunchSource) []string {
	if len(unit.SourcePaths) > 0 {
		return unit.SourcePaths
	}
	return []string{unit.Path}
}

// fixedDeliveryFor returns the fixed, locally declared delivery string for a
// role. It is a constant per role by construction — no source content can
// reach it, which is the injection invariant expressed as code.
func fixedDeliveryFor(role string) string {
	switch role {
	case RoleManager:
		return "MANAGER CYCLE: run one supervision cycle, update status, then end the turn."
	case RoleBuilder:
		return "BUILDER CYCLE: pick up the next assigned item, do bounded work, then end the turn."
	case RoleReviewer:
		return "REVIEW CYCLE: review what is waiting, record a verdict, then end the turn."
	case RoleTriage:
		return "TRIAGE: new work may be waiting; check the source through its connector and triage it."
	case RoleDevOps:
		return "DEVOPS CYCLE: check deployment and infrastructure state, then end the turn."
	case RoleRegistrar:
		return "REGISTRAR CYCLE: reconcile the registry with reality, then end the turn."
	default:
		return "CYCLE: run one cycle of your declared workflow, then end the turn."
	}
}

// --- launchd / systemd targets -----------------------------------------

func adoptLaunchd(path string, opts Options) (*Plan, error) {
	src, err := ParseLaunchdPlist(path)
	if err != nil {
		return nil, err
	}
	return planFromUnit(src, opts)
}

func adoptSystemd(path string, opts Options) (*Plan, error) {
	src, err := ParseSystemdUnit(path)
	if err != nil {
		return nil, err
	}
	return planFromUnit(src, opts)
}

func planFromUnit(src *LaunchSource, opts Options) (*Plan, error) {
	name := sanitizeName(src.Label)
	paths := launchSourcePaths(src)
	plan := &Plan{Target: src.Path, TargetKind: src.Kind, SourcePaths: append([]string(nil), paths...)}

	classified := ClassifyLaunchSource(src)
	post := NewPost(name, postID(name, src.Path))
	post.Spec.Classification = classified.Class
	post.Spec.AdoptedFrom = src.Path
	post.Spec.AdoptedAt = opts.now()
	post.Spec.Placement.Machine = opts.Machine
	post.Spec.Placement.Project = src.WorkingDirectory
	post.Spec.Role = RoleRef{Name: classified.Role}
	post.Spec.Placement.ReportsTo = ReportsToFor(classified.Role, opts.ManagerPost)

	post.AddInference("spec.classification", string(classified.Class), src.Path, classified.Confidence, classified.Reason)
	post.AddInference("spec.role.name", classified.Role, src.Path, classified.Confidence, classified.Reason)
	if src.Program != "" {
		post.AddEvidence("source.program", src.Program, src.Path, ConfidenceHigh, "")
	}
	if src.WorkingDirectory != "" {
		post.AddEvidence("spec.placement.project", src.WorkingDirectory, src.Path, ConfidenceHigh, "")
	}

	applyUnitToPost(post, src)

	// A connector-classified unit gets a connector reference with the local
	// freshness evidence we can actually check later.
	if classified.Class == ClassConnector {
		connector := ConnectorRef{
			Name: name, Kind: connectorKindFor(src),
			EvidencePath: connectorEvidencePath(src),
		}
		post.Spec.Connectors = append(post.Spec.Connectors, connector)
		post.AddEvidence("spec.connectors[0].name", connector.Name, src.Path, ConfidenceMedium,
			"naming a connector never creates or enables one")
	}

	switch classified.Class {
	case ClassDebris:
		post.AddUnresolved("this unit's program is missing; decide whether to remove the unit")
	case ClassExternal, ClassConnector, ClassService:
		post.AddUnresolved("adopted for visibility; agent-deck does not control this")
	}
	if classified.Role == RoleUnresolved {
		post.AddUnresolved("role is unresolved; a human must say what this does before it can be hired")
	}
	post.Spec.SourceFingerprint = FingerprintPaths(plan.SourcePaths)

	planned := &Planned{Name: name, Post: post}
	planned.Findings = ValidateDefinition(post, nil)
	planned.Report = renderReport(planned, plan.TargetKind, src.Path, classified)
	plan.Definitions = append(plan.Definitions, planned)
	for _, warning := range src.Warnings {
		plan.Notes = append(plan.Notes, warning)
	}
	return plan, nil
}

func connectorKindFor(src *LaunchSource) string {
	joined := strings.ToLower(src.Label + " " + strings.Join(src.Arguments, " "))
	switch {
	case strings.Contains(joined, "imap"), strings.Contains(joined, "mail"), strings.Contains(joined, "gmail"):
		return "mail"
	case strings.Contains(joined, "telegram"):
		return "telegram"
	case strings.Contains(joined, "slack"):
		return "slack"
	case strings.Contains(joined, "webhook"):
		return "webhook"
	default:
		return "unknown"
	}
}

// connectorEvidencePath guesses where this connector leaves a freshness trace.
// A guess that turns out to be absent is reported as UNKNOWN health later, not
// as a failure — the difference between "quiet" and "not looking" is the whole
// point of the health row.
func connectorEvidencePath(src *LaunchSource) string {
	if src.WorkingDirectory != "" {
		return src.WorkingDirectory
	}
	if src.Program != "" && strings.ContainsAny(src.Program, "/\\") {
		return filepath.Dir(src.Program)
	}
	return ""
}

// --- session target -----------------------------------------------------

func adoptSession(target string, opts Options) (*Plan, error) {
	var match *SessionInfo
	for i := range opts.Sessions {
		s := opts.Sessions[i]
		if s.ID == target || strings.EqualFold(s.Title, target) {
			match = &opts.Sessions[i]
			break
		}
	}
	if match == nil {
		return nil, fmt.Errorf("adopt: no session, directory, plist or unit matches %q", target)
	}

	name := sanitizeName(match.Title)
	if name == "" {
		name = sanitizeName(match.ID)
	}
	plan := &Plan{Target: target, TargetKind: "session"}

	classified := ClassifyRole(match.Title, match.IsConductor)
	post := NewPost(name, postID(name, match.ID))
	post.Metadata.Title = match.Title
	post.Spec.Classification = classified.Class
	post.Spec.AdoptedFrom = "session:" + match.ID
	post.Spec.AdoptedAt = opts.now()
	post.Spec.Role = RoleRef{Name: classified.Role}
	post.Spec.Placement = Placement{
		Project:   match.ProjectPath,
		Group:     match.GroupPath,
		Machine:   firstNonEmpty(match.Machine, opts.Machine),
		ReportsTo: ReportsToFor(classified.Role, opts.ManagerPost),
	}
	post.Spec.Runtime = RuntimeSpec{
		Harness:          match.Tool,
		Account:          match.Account,
		AdoptedSessionID: match.ID,
	}

	post.AddInference("spec.classification", string(classified.Class), "agent-deck session record", classified.Confidence, classified.Reason)
	post.AddInference("spec.role.name", classified.Role, "session title "+match.Title, classified.Confidence, classified.Reason)
	post.AddEvidence("spec.runtime.harness", match.Tool, "agent-deck session record", ConfidenceHigh, "")
	post.AddEvidence("spec.runtime.adoptedSessionId", match.ID, "agent-deck session record", ConfidenceHigh,
		"provenance only; the session keeps its own id, title and lifecycle")
	if match.Account != "" {
		post.AddEvidence("spec.runtime.account", match.Account, "agent-deck session record", ConfidenceHigh, "")
	}
	if match.GroupPath != "" {
		post.AddEvidence("spec.placement.group", match.GroupPath, "agent-deck session record", ConfidenceHigh, "")
	}

	// A watcher usually references a poller directory in its command. Record
	// it as a connector with a freshness path, and nothing more.
	if dir := pollerDirFromCommand(match.Command); dir != "" {
		post.Spec.Connectors = append(post.Spec.Connectors, ConnectorRef{
			Name: sanitizeName(filepath.Base(dir)), Kind: "unknown", EvidencePath: dir,
		})
		post.AddEvidence("spec.connectors[0].evidencePath", dir, "session command line", ConfidenceMedium,
			"inferred from the command; confirm before binding")
	}

	sourcePaths := []string{}
	for _, unit := range findRelatedUnits(name, opts.UnitDirs) {
		sourcePaths = append(sourcePaths, launchSourcePaths(unit)...)
		applyUnitToPost(post, unit)
	}
	post.Spec.SourceFingerprint = FingerprintPaths(sourcePaths)

	if classified.Role == RoleUnresolved {
		post.AddUnresolved("role is unresolved; a human must say what this session is for")
	}
	post.AddUnresolved("no role content was recovered from a live session; supply instructions or adopt its directory")

	planned := &Planned{Name: name, Post: post}
	planned.Findings = ValidateDefinition(post, nil)
	planned.Report = renderReport(planned, plan.TargetKind, "session:"+match.ID, classified)
	plan.Definitions = append(plan.Definitions, planned)
	plan.SourcePaths = sourcePaths

	if pair := PairFor(classified.Role); pair != "" {
		plan.Notes = append(plan.Notes, fmt.Sprintf(
			"role %s is half of a pair; its %s seat is unfilled", classified.Role, pair))
	}
	return plan, nil
}

var pollerDirPattern = regexp.MustCompile(`(/[\w.\-/]*(?:poll|imap|watch|bridge)[\w.\-/]*)`)

func pollerDirFromCommand(command string) string {
	match := pollerDirPattern.FindString(command)
	if match == "" {
		return ""
	}
	if info, err := os.Stat(match); err == nil {
		if info.IsDir() {
			return match
		}
		return filepath.Dir(match)
	}
	return ""
}

// --- writing ------------------------------------------------------------

// WriteTo writes every definition in the plan under root and returns the
// directories written. It refuses to overwrite an existing definition, so a
// re-run cannot silently clobber a definition a human has edited.
func (p *Plan) WriteTo(root string) ([]string, error) {
	var written []string
	for _, def := range p.Definitions {
		dir := filepath.Join(root, def.Name)
		// Guard on the whole directory, not just agent.yaml. A directory that
		// already holds role/INSTRUCTIONS.md or ADOPTION-REPORT.md but no
		// agent.yaml would otherwise have those files silently overwritten —
		// even if a caller supplies a pre-existing registry root.
		if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
			return written, fmt.Errorf("refusing to write definition %q: %s already exists and is not empty; "+
				"remove it or adopt under a different name", def.Name, dir)
		}
		if err := Write(dir, def.Post, def.Role, def.RoleFiles, def.Report); err != nil {
			return written, err
		}
		written = append(written, dir)
	}
	return written, nil
}

// --- helpers ------------------------------------------------------------

// handledCredentialFindings downgrades a credential error to a warning, for
// the case where adoption has already prevented the leak by not copying the
// file. The text still says what was found and what to do about it.
func handledCredentialFindings(in Findings) Findings {
	out := make(Findings, 0, len(in))
	for _, f := range in {
		if f.Severity == SeverityError {
			f.Severity = SeverityWarn
		}
		out = append(out, f)
	}
	return out
}

func readCapped(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	if info.Size() > maxRoleFileBytes {
		return nil, fmt.Errorf("%s is %d bytes, over the %d byte role-file cap; not copied",
			filepath.Base(path), info.Size(), maxRoleFileBytes)
	}
	return os.ReadFile(path)
}

// unitNameMatcher matches a unit whose name refers to the post, at a token
// boundary rather than anywhere in the string.
//
// Plain substring matching is wrong here: "gmail-watcher.plist" contains
// "mail-watcher", so a mail-watcher post silently inherited a different
// agent's schedule and displayed it as its own. A boundary is any character
// that is not a letter or digit, which still matches the real-world reverse
// DNS shapes ("com.ashesh.gmail-watcher.plist") and the bare stem
// ("repo-maintainer.service").
func unitNameMatcher(name string) (*regexp.Regexp, error) {
	needle := regexp.QuoteMeta(strings.ToLower(strings.TrimSpace(name)))
	if needle == "" {
		return nil, fmt.Errorf("empty unit name")
	}
	return regexp.Compile(`(^|[^a-z0-9])` + needle + `([^a-z0-9]|$)`)
}

// unitStem strips the unit suffix so the boundary check sees the name only.
func unitStem(fileName string) string {
	for _, suffix := range []string{".plist", ".service", ".timer"} {
		if strings.HasSuffix(fileName, suffix) {
			return strings.TrimSuffix(fileName, suffix)
		}
	}
	return fileName
}

var unsafeNameChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func sanitizeName(raw string) string {
	cleaned := unsafeNameChars.ReplaceAllString(strings.TrimSpace(raw), "-")
	cleaned = strings.Trim(cleaned, "-._")
	if cleaned == "" {
		return ""
	}
	if len(cleaned) > 64 {
		cleaned = cleaned[:64]
	}
	return strings.ToLower(cleaned)
}

// postID is stable for a given (name, source) pair so re-adopting the same
// target does not mint a new identity.
func postID(name, source string) string {
	digest := Digest([]byte(name + "\x00" + source))
	return "post-" + strings.TrimPrefix(digest, "sha256:")[:16]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// renderReport writes the evidence map. Every generated field appears here
// with where it came from and how much to trust it.
func renderReport(def *Planned, targetKind, target string, classified ClassifyResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Adoption report: %s\n\n", def.Name)
	fmt.Fprintf(&sb, "- target: `%s`\n", target)
	fmt.Fprintf(&sb, "- target kind: %s\n", targetKind)
	fmt.Fprintf(&sb, "- post id: `%s`\n", def.Post.Metadata.PostID)
	fmt.Fprintf(&sb, "- classification: **%s** (%s confidence)\n", def.Post.Spec.Classification, classified.Confidence)
	fmt.Fprintf(&sb, "- role: **%s** — %s\n", def.Post.Spec.Role.Name, classified.Reason)
	fmt.Fprintf(&sb, "- reports to: `%s`\n", def.Post.Spec.Placement.ReportsTo)
	fmt.Fprintf(&sb, "- source fingerprint: `%s`\n\n", def.Post.Spec.SourceFingerprint)

	sb.WriteString("This definition is **disabled**. Nothing in it fires. ")
	sb.WriteString("The source automation was read, never modified, and still owns every trigger listed below.\n\n")

	sb.WriteString("## Where each generated field came from\n\n")
	sb.WriteString("| field | value | source | confidence | note |\n")
	sb.WriteString("|---|---|---|---|---|\n")
	for _, e := range def.Post.Spec.Provenance {
		note := e.Reason
		if e.Warning != "" {
			// A warning is louder than a derivation note, so it wins the
			// column and is marked as the caution it is.
			note = "warning: " + e.Warning
		}
		fmt.Fprintf(&sb, "| `%s` | %s | `%s` | %s | %s |\n",
			e.Field, mdCell(e.Value), e.Source, e.Confidence, mdCell(note))
	}
	sb.WriteString("\n")

	if len(def.Post.Spec.Triggers) > 0 {
		sb.WriteString("## Declared triggers (display only)\n\n")
		sb.WriteString("| name | kind | schedule | owned by |\n|---|---|---|---|\n")
		for _, t := range def.Post.Spec.Triggers {
			schedule := t.Schedule
			if schedule == "" && t.IntervalSeconds > 0 {
				schedule = "every " + strconv.Itoa(t.IntervalSeconds) + "s"
			}
			fmt.Fprintf(&sb, "| %s | %s | %s | `%s` |\n", t.Name, t.Type, mdCell(schedule), t.ExternalSource)
		}
		sb.WriteString("\nagent-deck renders the next due time for these. It does not schedule them.\n\n")
	}

	if len(def.Post.Spec.Unresolved) > 0 {
		sb.WriteString("## Unresolved — a human owes this definition an answer\n\n")
		for _, item := range def.Post.Spec.Unresolved {
			fmt.Fprintf(&sb, "- %s\n", item)
		}
		sb.WriteString("\n")
	}

	if len(def.Findings) > 0 {
		sb.WriteString("## Validation\n\n")
		for _, f := range def.Findings {
			fmt.Fprintf(&sb, "- **%s** `%s`: %s\n", f.Severity, f.Field, f.Message)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Rollback\n\n")
	fmt.Fprintf(&sb, "Delete the definition directory. The source at `%s` was never modified, ", target)
	sb.WriteString("so there is nothing else to undo.\n")
	return sb.String()
}

func mdCell(value string) string {
	cleaned := strings.ReplaceAll(value, "|", "\\|")
	cleaned = strings.ReplaceAll(cleaned, "\n", " ")
	if cleaned == "" {
		return "—"
	}
	if len(cleaned) > 120 {
		cleaned = cleaned[:117] + "..."
	}
	return cleaned
}
