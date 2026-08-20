package agents

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Severity separates "this definition is wrong" from "a human should look".
type Severity string

const (
	// SeverityError means the definition is invalid and must not be treated
	// as usable.
	SeverityError Severity = "error"
	// SeverityWarn means it parses and is usable, but something in it is
	// suspect — most often portability rot copied in from the source.
	SeverityWarn Severity = "warn"
)

// Finding is one validation result.
type Finding struct {
	Severity Severity `json:"severity"`
	Field    string   `json:"field"`
	Message  string   `json:"message"`
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: %s: %s", f.Severity, f.Field, f.Message)
}

// Findings is an ordered set of results.
type Findings []Finding

// HasErrors reports whether any finding is fatal.
func (fs Findings) HasErrors() bool {
	for _, f := range fs {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

func (fs *Findings) errorf(field, format string, args ...any) {
	*fs = append(*fs, Finding{Severity: SeverityError, Field: field, Message: fmt.Sprintf(format, args...)})
}

func (fs *Findings) warnf(field, format string, args ...any) {
	*fs = append(*fs, Finding{Severity: SeverityWarn, Field: field, Message: fmt.Sprintf(format, args...)})
}

// harnessTokens are names a portable role must not contain. A role describes a
// profession; the post picks the tool. Catching this at validation is what
// keeps "switch harness with one field" honest.
var harnessTokens = []string{
	"claude", "codex", "deepseek", "hermes", "gemini", "copilot", "tmux",
}

// secretPattern catches the shapes of a credential that must never reach a
// definition file. It is deliberately broad: a false warning costs a glance,
// a missed token costs a credential.
var secretPattern = regexp.MustCompile(`(?i)(api[_-]?key|secret|token|password|passwd|bearer|authorization|client[_-]?secret|private[_-]?key)`)

// tokenishValue catches long opaque strings that look like real credentials
// rather than the word "token" appearing in prose.
var tokenishValue = regexp.MustCompile(`\b(sk-[A-Za-z0-9_\-]{16,}|gh[pousr]_[A-Za-z0-9]{16,}|xox[baprs]-[A-Za-z0-9\-]{10,}|[A-Za-z0-9_\-]{40,})\b`)

// hostnamePattern catches machine-specific references in role content.
var hostnamePattern = regexp.MustCompile(`(?i)\b([a-z0-9][a-z0-9\-]*\.(local|lan|internal|home|arpa))\b|\b\d{1,3}(\.\d{1,3}){3}\b`)

// ValidateRole checks a role manifest for the portability invariants. It does
// not read the role's Markdown bodies; ValidateRoleContent does that.
func ValidateRole(r *Role) Findings {
	var fs Findings
	if r == nil {
		fs.errorf("role", "role is nil")
		return fs
	}
	if strings.TrimSpace(r.APIVersion) != APIVersion {
		fs.errorf("role.apiVersion", "want %q, got %q", APIVersion, r.APIVersion)
	}
	if strings.TrimSpace(r.Kind) != KindRole {
		fs.errorf("role.kind", "want %q, got %q", KindRole, r.Kind)
	}
	if strings.TrimSpace(r.Metadata.Name) == "" {
		fs.errorf("role.metadata.name", "required")
	}
	if strings.TrimSpace(r.Metadata.Version) == "" {
		fs.errorf("role.metadata.version", "required")
	}

	// Every file reference must be relative and contained in the role dir.
	refs := map[string]string{}
	if r.Spec.Entrypoint != "" {
		refs["role.spec.entrypoint"] = r.Spec.Entrypoint
	}
	for i, p := range r.Spec.Policy {
		refs[fmt.Sprintf("role.spec.policy[%d]", i)] = p
	}
	for name, p := range r.Spec.Playbooks {
		refs["role.spec.playbooks."+name] = p
	}
	for name, p := range r.Spec.Workflows {
		refs["role.spec.workflows."+name] = p
	}
	if r.Spec.Learnings != nil && r.Spec.Learnings.File != "" {
		refs["role.spec.learnings.file"] = r.Spec.Learnings.File
	}

	fields := make([]string, 0, len(refs))
	for field := range refs {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		ref := refs[field]
		if filepath.IsAbs(ref) {
			fs.errorf(field, "absolute path %q; role references must be relative to the role directory", ref)
			continue
		}
		if !filepath.IsLocal(filepath.Clean(ref)) {
			fs.errorf(field, "%q escapes the role directory", ref)
		}
	}

	// A role that names a harness is not portable.
	lowerName := strings.ToLower(r.Metadata.Name + " " + r.Spec.Description)
	for _, tok := range harnessTokens {
		if strings.Contains(lowerName, tok) {
			fs.warnf("role.metadata.name", "mentions harness %q; roles should be harness-neutral", tok)
			break
		}
	}
	return fs
}

// ValidateRoleContent checks one copied role file body for the things a role
// must never carry. Content is never rewritten — the user's Markdown is
// authoritative — so every result here is a warning addressed to a human.
func ValidateRoleContent(field, body string) Findings {
	var fs Findings
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if tokenishValue.MatchString(trimmed) && secretPattern.MatchString(trimmed) {
			fs.warnf(field, "looks like it contains a credential; move it to a connector's private store")
			break
		}
	}
	if hostnamePattern.MatchString(body) {
		fs.warnf(field, "contains a hostname or IP; machine specifics belong on the post, not the role")
	}
	if strings.Contains(body, "/Users/") || strings.Contains(body, "/home/") {
		fs.warnf(field, "contains an absolute home path; it will not resolve on another machine")
	}
	return fs
}

// ValidatePost checks a post manifest.
func ValidatePost(p *Post) Findings {
	var fs Findings
	if p == nil {
		fs.errorf("post", "post is nil")
		return fs
	}
	if strings.TrimSpace(p.APIVersion) != APIVersion {
		fs.errorf("post.apiVersion", "want %q, got %q", APIVersion, p.APIVersion)
	}
	if strings.TrimSpace(p.Kind) != KindPost {
		fs.errorf("post.kind", "want %q, got %q", KindPost, p.Kind)
	}
	if strings.TrimSpace(p.Metadata.Name) == "" {
		fs.errorf("post.metadata.name", "required")
	}
	if strings.TrimSpace(p.Metadata.PostID) == "" {
		fs.errorf("post.metadata.postId", "required; triggers and delivery target the post id, never the title")
	}
	if strings.TrimSpace(p.Spec.Role.Name) == "" {
		fs.errorf("post.spec.role.name", "required")
	}

	// Every chain must terminate at a human principal.
	if strings.TrimSpace(p.Spec.Placement.ReportsTo) == "" {
		fs.errorf("post.spec.placement.reportsTo", "required; every post reports to a manager post or a human principal")
	}

	switch p.Spec.Classification {
	case ClassAgent, ClassConnector, ClassService, ClassExternal, ClassDebris:
	case "":
		fs.errorf("post.spec.classification", "required")
	default:
		fs.errorf("post.spec.classification", "unknown classification %q", p.Spec.Classification)
	}

	// Phase-1 invariant. Recognition only: nothing this package emits or reads
	// may claim to be live, and no trigger it carries may claim to be armed.
	if p.Spec.Enabled {
		fs.errorf("post.spec.enabled", "phase 1 emits disabled posts only")
	}

	seen := map[string]bool{}
	for i, t := range p.Spec.Triggers {
		field := fmt.Sprintf("post.spec.triggers[%d]", i)
		if strings.TrimSpace(t.Name) == "" {
			fs.errorf(field+".name", "required")
		} else if seen[t.Name] {
			fs.errorf(field+".name", "duplicate trigger name %q", t.Name)
		}
		seen[t.Name] = true

		switch t.Type {
		case TriggerCron, TriggerMailDoorbell, TriggerFileWatch, TriggerWebhook, TriggerSessionTransition, TriggerOpaque:
		default:
			fs.errorf(field+".type", "unknown trigger kind %q", t.Type)
		}
		if t.Enabled {
			fs.errorf(field+".enabled", "phase 1 emits disabled triggers only; the source automation still owns the firing")
		}
		if !t.External {
			fs.errorf(field+".external", "phase 1 triggers are external: the plist, timer or manager that fires today keeps owning it")
		}
		if t.External && strings.TrimSpace(t.ExternalSource) == "" {
			fs.errorf(field+".externalSource", "an external trigger must name the file that owns its firing")
		}
		if t.Type == TriggerCron && t.Schedule != "" && strings.TrimSpace(t.Timezone) == "" {
			fs.warnf(field+".timezone", "cron schedule without an explicit timezone; next-due rendering assumes local time")
		}
		// The injection invariant, enforced rather than documented.
		if placeholderPattern.MatchString(t.Deliver) {
			fs.errorf(field+".deliver", "delivery string interpolates source-controlled content; it must be a fixed local string")
		}
	}

	for i, c := range p.Spec.Connectors {
		field := fmt.Sprintf("post.spec.connectors[%d]", i)
		if strings.TrimSpace(c.Name) == "" {
			fs.errorf(field+".name", "required")
		}
		if secretPattern.MatchString(c.Name) {
			fs.warnf(field+".name", "connector name looks like a credential reference; connectors hold references, never secrets")
		}
	}
	return fs
}

// placeholderPattern catches the convenient-but-forbidden templates from the
// design: {{sender}}, ${SUBJECT}, %(path)s and friends.
var placeholderPattern = regexp.MustCompile(`\{\{[^}]+\}\}|\$\{[A-Za-z_][A-Za-z0-9_]*\}|%\([A-Za-z_]+\)s`)

// ValidateDefinition validates a post together with its role.
func ValidateDefinition(p *Post, r *Role) Findings {
	fs := ValidatePost(p)
	if r != nil {
		fs = append(fs, ValidateRole(r)...)
		if p != nil && r.Metadata.Name != "" && p.Spec.Role.Name != "" && p.Spec.Role.Name != r.Metadata.Name {
			fs.errorf("post.spec.role.name", "post names role %q but the role manifest is %q", p.Spec.Role.Name, r.Metadata.Name)
		}
	}
	return fs
}

// ValidateReportsTo walks the reports_to graph across a set of posts and
// reports cycles and chains that never reach a human principal. Adoption emits
// a flat "everyone reports to the manager" shape, but a hand-edited registry
// can grow a loop, and a loop is the one error that makes escalation silently
// unreachable.
func ValidateReportsTo(posts []*Post) Findings {
	var fs Findings
	byName := map[string]*Post{}
	for _, p := range posts {
		if p != nil && p.Metadata.Name != "" {
			byName[p.Metadata.Name] = p
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		visited := map[string]bool{}
		current := byName[name]
		reachedHuman := false
		for current != nil {
			if visited[current.Metadata.Name] {
				fs.errorf("post."+name+".spec.placement.reportsTo",
					"reports_to cycle through %q; escalation would never reach a human", current.Metadata.Name)
				break
			}
			visited[current.Metadata.Name] = true
			next := strings.TrimSpace(current.Spec.Placement.ReportsTo)
			if strings.HasPrefix(next, "human:") {
				reachedHuman = true
				break
			}
			parent, ok := byName[next]
			if !ok {
				fs.warnf("post."+name+".spec.placement.reportsTo",
					"reports to %q, which is not a known post or a human principal", next)
				break
			}
			current = parent
		}
		if !reachedHuman && !fs.HasErrors() {
			continue
		}
	}
	return fs
}
