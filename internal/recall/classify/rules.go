package classify

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// rules.json is the one taxonomy both classifiers read: this package
// (embedded, so the binary never looks for a file) and distill.py (its
// sibling copy under skills/agent-deck/scripts/self-improvement). A test
// asserts the two files are byte-identical so the Go and Python paths
// cannot drift.
//
//go:embed rules.json
var rulesJSON []byte

// Rules is the decoded rules.json.
type Rules struct {
	Version         int              `json:"version"`
	NoiseTypes      []string         `json:"noise_types"`
	SkillLoadMarker string           `json:"skill_load_marker"`
	HeartbeatPrefix []string         `json:"heartbeat_prefixes"`
	InterruptMarker string           `json:"interrupt_marker"`
	MetaPrefixes    []string         `json:"meta_prefixes"`
	Clip            ClipRules        `json:"clip"`
	LostTime        LostTimeRules    `json:"lost_time"`
	SessionKind     SessionKindRules `json:"session_kind"`
	Outcome         OutcomeRules     `json:"outcome"`
	noise           map[string]bool
}

// ClipRules are distill.py's preview clip lengths.
type ClipRules struct {
	ArgPreviewChars    int `json:"arg_preview_chars"`
	AssistPreviewChars int `json:"assist_preview_chars"`
	UserPreviewChars   int `json:"user_preview_chars"`
	ErrorPreviewChars  int `json:"error_preview_chars"`
}

// LostTimeRules drive the "where did we lose time" classifier.
type LostTimeRules struct {
	// SlowCallMS is the duration above which one tool call is reported.
	SlowCallMS int64 `json:"slow_call_ms"`
	// ErrorShareMin is the errored share of a tool's calls above which the
	// tool is named (with at least MinErrors errors).
	ErrorShareMin float64 `json:"error_share_min"`
	MinErrors     int     `json:"min_errors"`
	// RetryWindow is how many consecutive calls of one tool count as a
	// retry loop when every one of them errored.
	RetryWindow int `json:"retry_window"`
	// TopTools is how many tools the artifact names.
	TopTools int `json:"top_tools"`
}

// SessionKindRules drive the session-kind classifier.
type SessionKindRules struct {
	// HeartbeatShareMin is the heartbeat share of user messages above
	// which a session is a conductor loop (with at least MinHeartbeats).
	HeartbeatShareMin float64 `json:"heartbeat_share_min"`
	MinHeartbeats     int     `json:"min_heartbeats"`
	// ConductorPurposePrefix marks a purpose hint written by conductor setup.
	ConductorPurposePrefix string `json:"conductor_purpose_prefix"`
	// ParentHintKey is the hint a child session carries.
	ParentHintKey string `json:"parent_hint_key"`
	// SubagentMinToolCalls is reserved (0: any subagent transcript is a
	// subagent session).
	SubagentMinToolCalls int `json:"subagent_min_tool_calls"`
}

// OutcomeRules drive the outcome classifier.
type OutcomeRules struct {
	// HintKey is the hint that, when present, decides the outcome outright.
	HintKey string `json:"hint_key"`
	// ErrorShareFailed is the errored share of tool calls above which a
	// session without an outcome hint reads as failed (with at least
	// MinToolCalls calls).
	ErrorShareFailed float64 `json:"error_share_failed"`
	MinToolCalls     int     `json:"min_tool_calls"`
	// InterruptsAbandoned is the interrupt count at or above which a
	// session with no later prompt reads as abandoned.
	InterruptsAbandoned int `json:"interrupts_abandoned"`
}

// IsNoise reports whether a Claude record type is distill.py noise.
func (r *Rules) IsNoise(recordType string) bool { return r.noise[recordType] }

// RulesJSON returns the embedded rules.json bytes (for the drift test and
// for `recall status --json`).
func RulesJSON() []byte { return rulesJSON }

var rules = mustRules()

// Current returns the embedded rules.
func Current() *Rules { return rules }

func mustRules() *Rules {
	r, err := ParseRules(rulesJSON)
	if err != nil {
		panic(err)
	}
	return r
}

// ParseRules decodes a rules.json document and checks the fields every
// classifier depends on.
func ParseRules(data []byte) (*Rules, error) {
	var r Rules
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("classify: rules.json: %w", err)
	}
	if r.SkillLoadMarker == "" || r.InterruptMarker == "" || len(r.HeartbeatPrefix) == 0 {
		return nil, fmt.Errorf("classify: rules.json: skill_load_marker, interrupt_marker and heartbeat_prefixes are required")
	}
	r.noise = make(map[string]bool, len(r.NoiseTypes))
	for _, t := range r.NoiseTypes {
		r.noise[t] = true
	}
	return &r, nil
}
