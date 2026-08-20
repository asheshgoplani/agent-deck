// Package agents implements the phase-1 slice of the Agents concept: portable
// role/post definitions, adoption of an existing organic setup into those
// definitions, and read-only views over them.
//
// Phase 1 is recognition before automation. Nothing in this package fires a
// trigger, starts a runtime, or writes to a source system. Triggers carried in
// a definition are DECLARATIVE ONLY: the plists and systemd units that own the
// firing today keep owning it, and such rows are marked External so no reader
// mistakes a rendered "next due" for a schedule agent-deck controls.
package agents

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// APIVersion is stamped on every emitted document.
	APIVersion = "agent-deck.io/v1alpha1"
	// KindPost is the assignment-in-a-place document (agent.yaml).
	KindPost = "AgentPost"
	// KindRole is the portable-profession document (role/role.yaml).
	KindRole = "AgentRole"

	// PostFileName is the canonical post filename inside a definition dir.
	// Design open question 1 chose the friendlier spelling; the directory is
	// the "agent definition" and the role lives under ./role.
	PostFileName = "agent.yaml"
	// RoleDirName is the role subdirectory of a definition dir.
	RoleDirName = "role"
	// RoleFileName is the role manifest inside RoleDirName.
	RoleFileName = "role.yaml"
	// ReportFileName is the human-readable evidence map emitted by adoption.
	ReportFileName = "ADOPTION-REPORT.md"
)

// Confidence grades an inferred field. Adoption is skeptical by construction:
// anything it could not read directly out of the source is Low, and Low fields
// never silently become behavior.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// Classification labels what a target actually turned out to be. Only Agent
// targets produce a post worth hiring; the rest are recorded so the fleet
// inventory is honest about what else is running.
type Classification string

const (
	// ClassAgent is something that does work on the user's behalf.
	ClassAgent Classification = "agent"
	// ClassConnector is a boundary to an external source (mail poller, bridge).
	ClassConnector Classification = "connector"
	// ClassService is local infrastructure that keeps other things alive.
	ClassService Classification = "service"
	// ClassExternal is real, running, and owned by something other than
	// agent-deck — adopted for visibility, never for control.
	ClassExternal Classification = "external"
	// ClassDebris is a leftover: a unit pointing at a path that is gone, a
	// plist for a binary that no longer exists.
	ClassDebris Classification = "debris"
)

// Role names from the reference org chart. Adoption maps a target onto one of
// these only when the evidence is obvious; everything else stays RoleUnresolved
// so a human supplies the purpose rather than the tool guessing it.
const (
	RoleManager    = "manager"
	RoleRegistrar  = "registrar"
	RoleBuilder    = "builder"
	RoleReviewer   = "reviewer"
	RoleDevOps     = "devops"
	RoleTriage     = "triage"
	RoleUnresolved = "unresolved"
)

// PrincipalHuman is the root of every reports_to chain.
const PrincipalHuman = "human:ashesh"

// Evidence records how one generated field got its value. Every inferred field
// in an emitted definition has exactly one of these; a field with no evidence
// is a bug, not a default.
type Evidence struct {
	Field      string     `yaml:"field" json:"field"`
	Value      string     `yaml:"value" json:"value"`
	Source     string     `yaml:"source" json:"source"`
	Confidence Confidence `yaml:"confidence" json:"confidence"`
	// Reason says why the value was inferred. It is not a problem report.
	Reason string `yaml:"reason,omitempty" json:"reason,omitempty"`
	// Warning is a caution about trusting or acting on the value. Keeping it
	// separate from Reason matters: a reader scanning for warnings should not
	// have to read ordinary derivation notes filed under the same key.
	Warning string `yaml:"warning,omitempty" json:"warning,omitempty"`
}

// RoleRef points a post at its role.
type RoleRef struct {
	Name    string `yaml:"name" json:"name"`
	Path    string `yaml:"path,omitempty" json:"path,omitempty"`
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	Digest  string `yaml:"digest,omitempty" json:"digest,omitempty"`
}

// Placement binds a post to a project, a group, a machine and a manager.
type Placement struct {
	Project   string `yaml:"project,omitempty" json:"project,omitempty"`
	Group     string `yaml:"group,omitempty" json:"group,omitempty"`
	Machine   string `yaml:"machine,omitempty" json:"machine,omitempty"`
	ReportsTo string `yaml:"reportsTo,omitempty" json:"reportsTo,omitempty"`
}

// RuntimeSpec selects the harness and account. Roles stay harness-neutral; this
// is the only place a tool name is allowed to appear.
type RuntimeSpec struct {
	Harness string `yaml:"harness,omitempty" json:"harness,omitempty"`
	Account string `yaml:"account,omitempty" json:"account,omitempty"`
	Start   string `yaml:"start,omitempty" json:"start,omitempty"`
	// AdoptedSessionID records which live session this post was recognized
	// from. It is provenance, not control: phase 1 never takes the session
	// over, renames it, or changes its lifecycle.
	AdoptedSessionID string `yaml:"adoptedSessionId,omitempty" json:"adopted_session_id,omitempty"`
}

// RestartPolicy is recorded from the source's own settings so a later phase has
// something to compare against. Phase 1 never acts on it.
type RestartPolicy struct {
	Mode        string `yaml:"mode,omitempty" json:"mode,omitempty"`
	MaxAttempts int    `yaml:"maxAttempts,omitempty" json:"max_attempts,omitempty"`
	Window      string `yaml:"window,omitempty" json:"window,omitempty"`
}

// ConnectorRef names a connector and the capabilities the post needs from it.
// Naming a connector never creates or enables one.
type ConnectorRef struct {
	Name    string   `yaml:"name" json:"name"`
	Require []string `yaml:"require,omitempty" json:"require,omitempty"`
	// Kind is descriptive ("mail", "telegram", "webhook", "drain").
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`
	// EvidencePath is a local directory or file whose freshness tells us
	// whether this connector is actually working (a seen-db, a health stamp).
	// It is read, never written.
	EvidencePath string `yaml:"evidencePath,omitempty" json:"evidence_path,omitempty"`
}

// Trigger kinds from the design's v1 set, plus the honest phase-1 addition.
const (
	TriggerCron              = "cron"
	TriggerMailDoorbell      = "mail-doorbell"
	TriggerFileWatch         = "file-watch"
	TriggerWebhook           = "webhook"
	TriggerSessionTransition = "session-transition"
	// TriggerOpaque is what adoption emits when it can see that something
	// fires but cannot honestly say on what terms.
	TriggerOpaque = "opaque"
)

// Trigger is a DECLARED trigger. In phase 1 it is display-only.
type Trigger struct {
	Name     string `yaml:"name" json:"name"`
	Type     string `yaml:"type" json:"type"`
	Schedule string `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	// IntervalSeconds carries a StartInterval-style cadence that has no cron
	// spelling.
	IntervalSeconds int    `yaml:"intervalSeconds,omitempty" json:"interval_seconds,omitempty"`
	Timezone        string `yaml:"timezone,omitempty" json:"timezone,omitempty"`
	Workflow        string `yaml:"workflow,omitempty" json:"workflow,omitempty"`
	// Deliver is the fixed, locally declared string. External content is never
	// interpolated into it — see the injection invariant in the design.
	Deliver  string `yaml:"deliver,omitempty" json:"deliver,omitempty"`
	Coalesce string `yaml:"coalesce,omitempty" json:"coalesce,omitempty"`
	// Enabled is false for everything adoption emits. A definition that
	// arrived disabled must never be rendered as if it were live.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// External marks a trigger whose firing still lives in a plist, a systemd
	// timer, or a shell manager. agent-deck displays its next due time and
	// does not schedule it. Phase 1 sets this on every adopted trigger.
	External bool `yaml:"external" json:"external"`
	// ExternalSource is the file that actually owns the firing.
	ExternalSource string `yaml:"externalSource,omitempty" json:"external_source,omitempty"`
	// Connector names the source connector where applicable.
	Connector string `yaml:"connector,omitempty" json:"connector,omitempty"`
}

// PostMetadata identifies the post.
type PostMetadata struct {
	Name   string `yaml:"name" json:"name"`
	PostID string `yaml:"postId" json:"post_id"`
	Title  string `yaml:"title,omitempty" json:"title,omitempty"`
}

// PostSpec is the body of an agent.yaml.
type PostSpec struct {
	Role           RoleRef        `yaml:"role" json:"role"`
	Placement      Placement      `yaml:"placement" json:"placement"`
	Runtime        RuntimeSpec    `yaml:"runtime" json:"runtime"`
	RestartPolicy  *RestartPolicy `yaml:"restartPolicy,omitempty" json:"restart_policy,omitempty"`
	Connectors     []ConnectorRef `yaml:"connectors,omitempty" json:"connectors,omitempty"`
	Triggers       []Trigger      `yaml:"triggers,omitempty" json:"triggers,omitempty"`
	Classification Classification `yaml:"classification" json:"classification"`
	// Enabled is always false in phase 1. It exists so the field is present
	// and inspectable, not so it can be flipped here.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Unresolved lists the decisions a human still owes this definition.
	Unresolved []string   `yaml:"unresolved,omitempty" json:"unresolved,omitempty"`
	Provenance []Evidence `yaml:"provenance,omitempty" json:"provenance,omitempty"`
	// SourceFingerprint lets a later `adopt --refresh` show drift.
	SourceFingerprint string    `yaml:"sourceFingerprint,omitempty" json:"source_fingerprint,omitempty"`
	AdoptedAt         time.Time `yaml:"adoptedAt,omitempty" json:"adopted_at,omitempty"`
	AdoptedFrom       string    `yaml:"adoptedFrom,omitempty" json:"adopted_from,omitempty"`
}

// Post is a full agent.yaml document.
type Post struct {
	APIVersion string       `yaml:"apiVersion" json:"api_version"`
	Kind       string       `yaml:"kind" json:"kind"`
	Metadata   PostMetadata `yaml:"metadata" json:"metadata"`
	Spec       PostSpec     `yaml:"spec" json:"spec"`
}

// RoleMetadata identifies a role.
type RoleMetadata struct {
	Name    string `yaml:"name" json:"name"`
	Version string `yaml:"version" json:"version"`
}

// LearningsSpec points at the curated learning surface.
type LearningsSpec struct {
	File      string `yaml:"file,omitempty" json:"file,omitempty"`
	Promotion string `yaml:"promotion,omitempty" json:"promotion,omitempty"`
}

// RoleSpec is the body of a role.yaml. It names files; it never re-expresses
// their steps.
type RoleSpec struct {
	RequiresAgentDeck string            `yaml:"requiresAgentDeck,omitempty" json:"requires_agent_deck,omitempty"`
	Description       string            `yaml:"description,omitempty" json:"description,omitempty"`
	Entrypoint        string            `yaml:"entrypoint,omitempty" json:"entrypoint,omitempty"`
	Policy            []string          `yaml:"policy,omitempty" json:"policy,omitempty"`
	Playbooks         map[string]string `yaml:"playbooks,omitempty" json:"playbooks,omitempty"`
	Workflows         map[string]string `yaml:"workflows,omitempty" json:"workflows,omitempty"`
	Learnings         *LearningsSpec    `yaml:"learnings,omitempty" json:"learnings,omitempty"`
	RequiresCaps      []string          `yaml:"requiresCapabilities,omitempty" json:"requires_capabilities,omitempty"`
	// Digests records a content digest per copied file so a runtime's inputs
	// are inspectable by digest.
	Digests map[string]string `yaml:"digests,omitempty" json:"digests,omitempty"`
}

// Role is a full role.yaml document.
type Role struct {
	APIVersion string       `yaml:"apiVersion" json:"api_version"`
	Kind       string       `yaml:"kind" json:"kind"`
	Metadata   RoleMetadata `yaml:"metadata" json:"metadata"`
	Spec       RoleSpec     `yaml:"spec" json:"spec"`
}

// NewPost returns a post skeleton with the invariant fields already correct:
// disabled, reporting to a human principal, and stamped with this API version.
func NewPost(name, postID string) *Post {
	return &Post{
		APIVersion: APIVersion,
		Kind:       KindPost,
		Metadata:   PostMetadata{Name: name, PostID: postID},
		Spec: PostSpec{
			Enabled:   false,
			Placement: Placement{ReportsTo: PrincipalHuman},
		},
	}
}

// NewRole returns a role skeleton.
func NewRole(name, version string) *Role {
	return &Role{
		APIVersion: APIVersion,
		Kind:       KindRole,
		Metadata:   RoleMetadata{Name: name, Version: version},
	}
}

// AddEvidence records provenance for one generated field.
func (p *Post) AddEvidence(field, value, source string, confidence Confidence, warning string) {
	p.Spec.Provenance = append(p.Spec.Provenance, Evidence{
		Field:      field,
		Value:      value,
		Source:     source,
		Confidence: confidence,
		Warning:    warning,
	})
}

// AddInference records provenance whose note explains HOW the value was
// derived rather than warning about it.
func (p *Post) AddInference(field, value, source string, confidence Confidence, reason string) {
	p.Spec.Provenance = append(p.Spec.Provenance, Evidence{
		Field:      field,
		Value:      value,
		Source:     source,
		Confidence: confidence,
		Reason:     reason,
	})
}

// AddUnresolved records a decision the definition still owes a human. Repeated
// entries collapse so a refresh does not grow the list without bound.
func (p *Post) AddUnresolved(item string) {
	for _, existing := range p.Spec.Unresolved {
		if existing == item {
			return
		}
	}
	p.Spec.Unresolved = append(p.Spec.Unresolved, item)
}

// MarshalPost renders a post as YAML.
func MarshalPost(p *Post) ([]byte, error) {
	out, err := yaml.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal post %q: %w", p.Metadata.Name, err)
	}
	return out, nil
}

// MarshalRole renders a role as YAML.
func MarshalRole(r *Role) ([]byte, error) {
	out, err := yaml.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("marshal role %q: %w", r.Metadata.Name, err)
	}
	return out, nil
}

// ParsePost decodes an agent.yaml. It is strict about the document header so a
// stray YAML file in the agents directory is reported rather than half-read.
func ParsePost(data []byte) (*Post, error) {
	var p Post
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse post: %w", err)
	}
	if strings.TrimSpace(p.Kind) != KindPost {
		return nil, fmt.Errorf("parse post: kind is %q, want %q", p.Kind, KindPost)
	}
	if strings.TrimSpace(p.APIVersion) == "" {
		return nil, fmt.Errorf("parse post %q: missing apiVersion", p.Metadata.Name)
	}
	return &p, nil
}

// ParseRole decodes a role.yaml.
func ParseRole(data []byte) (*Role, error) {
	var r Role
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse role: %w", err)
	}
	if strings.TrimSpace(r.Kind) != KindRole {
		return nil, fmt.Errorf("parse role: kind is %q, want %q", r.Kind, KindRole)
	}
	return &r, nil
}
