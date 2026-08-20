package agents

import (
	"path/filepath"
	"regexp"
	"strings"
)

// ClassifyResult is what classification concluded about a target, with the
// evidence that got it there. Adoption never records a conclusion without one.
type ClassifyResult struct {
	Class      Classification `json:"class"`
	Role       string         `json:"role"`
	Confidence Confidence     `json:"confidence"`
	Reason     string         `json:"reason"`
	// PairWith names the role that must be hired alongside this one. The
	// reference org chart pairs Builder with Reviewer, so recognizing a
	// builder means recognizing that its reviewer seat is currently empty.
	PairWith string `json:"pair_with,omitempty"`
}

// The reference org chart:
//
//	human:ashesh
//	  └─ manager
//	       ├─ registrar
//	       ├─ builder + reviewer   (always a pair)
//	       ├─ devops
//	       └─ triage/monitor
//
// ReportsToFor returns the manager a recognized role reports to. Everything
// except the manager itself reports to the manager; the manager reports to the
// human principal. A role we could not recognize reports to the human directly
// rather than being filed under a manager that may not want it.
func ReportsToFor(role, managerPost string) string {
	switch role {
	case RoleManager, RoleUnresolved, "":
		return PrincipalHuman
	default:
		if strings.TrimSpace(managerPost) == "" {
			return PrincipalHuman
		}
		return managerPost
	}
}

// PairFor returns the role that must be hired alongside this one, if any.
func PairFor(role string) string {
	switch role {
	case RoleBuilder:
		return RoleReviewer
	case RoleReviewer:
		return RoleBuilder
	default:
		return ""
	}
}

var (
	conductorNamePattern  = regexp.MustCompile(`(?i)\bconductor\b`)
	watcherNamePattern    = regexp.MustCompile(`(?i)\b(watcher|watch|doorbell|triage)\b`)
	monitorNamePattern    = regexp.MustCompile(`(?i)\b(monitor|poll|poller)\b`)
	maintainerNamePattern = regexp.MustCompile(`(?i)\b(maintainer|repo-maintainer|hygiene)\b`)
	registrarNamePattern  = regexp.MustCompile(`(?i)\b(registrar|registry|inventory|steward|cred-steward)\b`)
	devopsNamePattern     = regexp.MustCompile(`(?i)\b(devops|deploy|release|infra|ops|ci)\b`)
	builderNamePattern    = regexp.MustCompile(`(?i)\b(builder|worker|impl|exec)\b`)
	reviewerNamePattern   = regexp.MustCompile(`(?i)\b(reviewer|review|verifier|adversarial|vet)\b`)
)

// ClassifyRole maps a target's name and evidence onto the reference org chart.
// It fires only when the evidence is obvious. Anything else is RoleUnresolved,
// which is not a failure: it is the tool declining to invent a job description
// for something it does not understand.
func ClassifyRole(name string, isConductor bool) ClassifyResult {
	base := filepath.Base(strings.TrimSpace(name))

	if isConductor {
		return ClassifyResult{
			Class:      ClassAgent,
			Role:       RoleManager,
			Confidence: ConfidenceHigh,
			Reason:     "marked as a conductor by agent-deck's own session record",
		}
	}

	switch {
	case conductorNamePattern.MatchString(base):
		return ClassifyResult{
			Class: ClassAgent, Role: RoleManager, Confidence: ConfidenceHigh,
			Reason: "name contains \"conductor\"; conductors supervise other posts",
		}
	case maintainerNamePattern.MatchString(base):
		return ClassifyResult{
			Class: ClassAgent, Role: RoleBuilder, Confidence: ConfidenceMedium,
			Reason: "name matches the maintainer family, which does bounded repository work", PairWith: RoleReviewer,
		}
	case watcherNamePattern.MatchString(base):
		return ClassifyResult{
			Class: ClassAgent, Role: RoleTriage, Confidence: ConfidenceHigh,
			Reason: "name matches the watcher family, which triages arriving work",
		}
	case monitorNamePattern.MatchString(base):
		return ClassifyResult{
			Class: ClassAgent, Role: RoleTriage, Confidence: ConfidenceMedium,
			Reason: "name suggests a monitor; monitors and triage share the triage role",
		}
	case reviewerNamePattern.MatchString(base):
		return ClassifyResult{
			Class: ClassAgent, Role: RoleReviewer, Confidence: ConfidenceMedium,
			Reason: "name matches the reviewer family", PairWith: RoleBuilder,
		}
	case registrarNamePattern.MatchString(base):
		return ClassifyResult{
			Class: ClassAgent, Role: RoleRegistrar, Confidence: ConfidenceLow,
			Reason: "name suggests registry or steward work",
		}
	case devopsNamePattern.MatchString(base):
		return ClassifyResult{
			Class: ClassAgent, Role: RoleDevOps, Confidence: ConfidenceLow,
			Reason: "name suggests deployment or infrastructure work",
		}
	case builderNamePattern.MatchString(base):
		return ClassifyResult{
			Class: ClassAgent, Role: RoleBuilder, Confidence: ConfidenceLow,
			Reason: "name suggests implementation work", PairWith: RoleReviewer,
		}
	}

	return ClassifyResult{
		Class: ClassAgent, Role: RoleUnresolved, Confidence: ConfidenceLow,
		Reason: "no obvious role in the name; a human must say what this does",
	}
}

var (
	pollerCmdPattern  = regexp.MustCompile(`(?i)(imap|gmail|poll|seen\.db|doorbell|bridge|webhook)`)
	harnessCmdPattern = regexp.MustCompile(`(?i)^(agent-deck|claude|codex|deepseek|hermes)`)
	// daemonSubcommandPattern recognizes an invocation that keeps something
	// alive rather than doing a unit of work. It is checked against the
	// ARGUMENTS, so `agent-deck notify-daemon` is correctly a service even
	// though the binary is also how agents are launched.
	daemonSubcommandPattern = regexp.MustCompile(`(?i)(^|\s)(notify-daemon|[\w-]*daemon|--daemon|serve|supervisor)(\s|$)`)
	serviceNamePattern      = regexp.MustCompile(`(?i)\b(daemon|keepalive|notifier|notify|steward|supervisor|automount|tunnel)\b`)
	opaqueCmdPattern        = regexp.MustCompile(`(?i)\b(curl|wget)\b`)
)

// ClassifyLaunchSource labels a launchd plist or systemd unit from what it
// actually runs.
//
// Matching is deliberately anchored to the program's BASENAME and its
// arguments, never to the full path. A unit whose script merely lives under
// a directory called agent-deck-g14 is not thereby an agent — that path
// substring is a fact about the filesystem, not about the job.
func ClassifyLaunchSource(src *LaunchSource) ClassifyResult {
	if src == nil {
		return ClassifyResult{Class: ClassExternal, Role: RoleUnresolved, Confidence: ConfidenceLow,
			Reason: "no source to inspect"}
	}
	// Only a program we resolved AND found absent is debris. An unresolved
	// path is unknown, and unknown must never be rendered as a leftover to
	// delete.
	if src.ProgramStatus() == ProgramMissing {
		return ClassifyResult{
			Class: ClassDebris, Role: RoleUnresolved, Confidence: ConfidenceHigh,
			Reason: "its program path does not exist on this machine",
		}
	}

	program := filepath.Base(strings.TrimSpace(src.Program))
	args := strings.Join(src.Arguments, " ")
	label := src.Label

	switch {
	// A daemon subcommand wins over everything: it is what the job DOES.
	case daemonSubcommandPattern.MatchString(args), serviceNamePattern.MatchString(label), serviceNamePattern.MatchString(program):
		return ClassifyResult{
			Class: ClassService, Role: RoleUnresolved, Confidence: ConfidenceHigh,
			Reason: "runs a daemon that keeps local infrastructure alive, not a unit of agent work",
		}
	case pollerCmdPattern.MatchString(program), pollerCmdPattern.MatchString(args), pollerCmdPattern.MatchString(label):
		return ClassifyResult{
			Class: ClassConnector, Role: RoleUnresolved, Confidence: ConfidenceMedium,
			Reason: "runs a poller or bridge; this is a boundary to an external source, not an agent",
		}
	case harnessCmdPattern.MatchString(program):
		result := ClassifyRole(label, false)
		result.Reason = "invokes an agent harness (" + program + "): " + result.Reason
		return result
	case opaqueCmdPattern.MatchString(args):
		return ClassifyResult{
			Class: ClassExternal, Role: RoleUnresolved, Confidence: ConfidenceLow,
			Reason: "fetches and runs something opaque; adoption will not pretend to understand it",
		}
	}

	return ClassifyResult{
		Class: ClassExternal, Role: RoleUnresolved, Confidence: ConfidenceLow,
		Reason: "runs on this machine but agent-deck cannot tell what it is for",
	}
}
