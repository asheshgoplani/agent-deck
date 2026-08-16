// Package procowner implements the durable process-ownership receipt for
// agent-deck sessions (#1873).
//
// # The contract
//
// A session launched through a wrapper can lose its tmux pane while the wrapped
// process tree survives, reparented to PID 1. agent-deck then records the
// session as errored, and a later legitimate restart spawns a SECOND tree
// against the same instance, worktree and conversation. Nothing in the existing
// sweeps sees the survivor: they walk a live pane tree (there is none) or scan
// tmux sessions (the survivor is outside tmux).
//
// The maintainer ruling on #1873 fixes the primitive:
//
//   - the durable ownership receipt is the start-time + PID pair, recorded AT
//     SPAWN;
//   - the identity check is same PID AND same start time;
//   - any mismatch is a stranger and must not be signalled;
//   - no external provider and no new dependencies;
//   - an unverifiable process is reported as unknown and left alone.
//
// "At spawn" is the whole point. Reading a process's start time at sweep or
// kill time proves only that the pid did not change hands between the read and
// the signal; it says nothing about whether we ever owned that process. A
// receipt written while the process is provably ours is what turns a pid into
// ownership.
//
// # Leader and members
//
// The leader is the pane's initial process, captured the instant tmux reports
// it. Members are its descendants, each recorded with its own start identity
// while it is still attributable — that is, while it is reachable from the
// verified-live leader by a parent walk. Attribution is only ever performed
// during the spawn window and only downward from a leader whose identity still
// matches the receipt; the sweep and kill paths never extend a receipt, they
// only verify it. A member's recorded start identity must additionally be no
// older than the leader's, because a descendant of a process cannot have
// started before it — a cheap invariant that keeps a mid-scan pid recycle from
// widening the receipt.
//
// A process that detaches (setsid) AND outlives its whole recorded ancestry
// before any attribution pass observes it is, by construction, not attributable
// to a PID+start-time receipt. Such a process is never recorded and therefore
// never signalled: unowned, not "owned by guess". Closing that last gap needs a
// containment primitive (cgroup/job object), which the ruling explicitly did
// not take.
package procowner

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ReceiptVersion is the on-disk schema version. A receipt written by a newer
// version is refused rather than reinterpreted: a half-understood receipt is
// exactly the "ambiguous identity" case the contract says to fail closed on.
const ReceiptVersion = 1

// Receipt states. A cleared receipt is an absent file, never a state value, so
// there is no way to leave a tombstone that later reads as ownership.
const (
	// StateLive means the recorded identities were owned by this instance at
	// the moment the receipt was committed.
	StateLive = "live"
	// StateReaping is written before the first signal and cleared after death
	// is verified, so a crash mid-reap is recoverable rather than silent.
	StateReaping = "reaping"
	// StateRecoveryRequired means verification could not prove the receipt
	// either dead or ours. Nothing is signalled in this state.
	StateRecoveryRequired = "recovery_required"
)

// Member roles.
const (
	RoleLeader     = "leader"
	RoleDescendant = "descendant"
)

// ErrCorruptReceipt is returned when a receipt exists but cannot be understood:
// truncated JSON, an unknown schema version, a missing identity. It is a
// fail-closed signal, never a licence to treat the receipt as absent.
var ErrCorruptReceipt = errors.New("ownership receipt is unreadable or incomplete")

// Member is one owned process: a PID bound to a start identity. Neither half is
// ownership on its own — a PID is reused, and a start identity without its PID
// names nothing.
type Member struct {
	PID int `json:"pid"`
	// StartID is the platform's process start identity (Linux: the start time
	// in clock ticks since boot, from /proc/<pid>/stat field 22).
	StartID string `json:"start_id"`
	PGID    int    `json:"pgid,omitempty"`
	// UID is the owning uid at capture time. It is not part of the identity
	// ruling; it is a strictly narrowing guard, so a PID whose process now runs
	// as a different user is reported unknown instead of signalled.
	UID  int    `json:"uid"`
	Comm string `json:"comm,omitempty"`
	Role string `json:"role,omitempty"`
	// SeenAt is when this identity was captured (unix seconds), for diagnostics.
	SeenAt int64 `json:"seen_at,omitempty"`
}

// Key identifies a member for de-duplication.
func (m Member) Key() string { return fmt.Sprintf("%d/%s", m.PID, m.StartID) }

func (m Member) valid() bool {
	return m.PID > 1 && strings.TrimSpace(m.StartID) != ""
}

// String renders a member for operator-facing output.
func (m Member) String() string {
	role := m.Role
	if role == "" {
		role = RoleDescendant
	}
	return fmt.Sprintf("pid=%d start=%s uid=%d role=%s", m.PID, m.StartID, m.UID, role)
}

// Receipt is the durable record of what a spawn took ownership of.
type Receipt struct {
	Version    int    `json:"version"`
	InstanceID string `json:"instance_id"`
	// Generation increases by one per committed spawn. It is the compare-and-
	// swap token: a writer from a superseded spawn cannot overwrite a newer
	// receipt, which is what keeps two racing restarts from interleaving.
	Generation uint64 `json:"generation"`
	State      string `json:"state"`
	// Provider names the identity source, so a receipt written by /proc is
	// never verified against something else's notion of a start identity.
	Provider string `json:"provider"`
	// BootID scopes every start identity to one boot. Start times are measured
	// from boot, so without this a receipt from a previous boot could match a
	// present-day PID by coincidence. A boot mismatch instead proves every
	// recorded identity is gone.
	BootID     string   `json:"boot_id"`
	TmuxName   string   `json:"tmux_name,omitempty"`
	TmuxSocket string   `json:"tmux_socket,omitempty"`
	Command    string   `json:"command,omitempty"`
	CreatedAt  int64    `json:"created_at"`
	UpdatedAt  int64    `json:"updated_at,omitempty"`
	Leader     Member   `json:"leader"`
	Members    []Member `json:"members,omitempty"`
	// Note carries the last verification reason for operator surfaces.
	Note string `json:"note,omitempty"`
}

// All returns the leader followed by every member, de-duplicated by identity.
// Callers signal in reverse order so descendants are reaped before the leader.
func (r *Receipt) All() []Member {
	if r == nil {
		return nil
	}
	out := make([]Member, 0, len(r.Members)+1)
	seen := make(map[string]bool, len(r.Members)+1)
	if r.Leader.valid() {
		out = append(out, r.Leader)
		seen[r.Leader.Key()] = true
	}
	for _, m := range r.Members {
		if !m.valid() || seen[m.Key()] {
			continue
		}
		seen[m.Key()] = true
		out = append(out, m)
	}
	return out
}

// HasMember reports whether the identity is already recorded.
func (r *Receipt) HasMember(m Member) bool {
	for _, existing := range r.All() {
		if existing.Key() == m.Key() {
			return true
		}
	}
	return false
}

// Validate enforces every field the verifier depends on. Anything missing means
// the receipt cannot be checked, and an uncheckable receipt is not evidence of
// non-ownership.
func (r *Receipt) Validate() error {
	switch {
	case r == nil:
		return fmt.Errorf("%w: nil receipt", ErrCorruptReceipt)
	case r.Version != ReceiptVersion:
		return fmt.Errorf("%w: unsupported version %d", ErrCorruptReceipt, r.Version)
	case strings.TrimSpace(r.InstanceID) == "":
		return fmt.Errorf("%w: missing instance id", ErrCorruptReceipt)
	case strings.TrimSpace(r.Provider) == "":
		return fmt.Errorf("%w: missing provider", ErrCorruptReceipt)
	case strings.TrimSpace(r.BootID) == "":
		return fmt.Errorf("%w: missing boot id", ErrCorruptReceipt)
	case !r.Leader.valid():
		return fmt.Errorf("%w: leader identity is incomplete", ErrCorruptReceipt)
	}
	switch r.State {
	case StateLive, StateReaping, StateRecoveryRequired:
	default:
		return fmt.Errorf("%w: unknown state %q", ErrCorruptReceipt, r.State)
	}
	for idx, m := range r.Members {
		if !m.valid() {
			return fmt.Errorf("%w: member %d identity is incomplete", ErrCorruptReceipt, idx)
		}
	}
	return nil
}

// Encode renders a receipt for durable storage.
func Encode(r *Receipt) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(r, "", "  ")
}

// Decode parses a receipt, refusing anything it cannot fully understand.
//
// A truncated write, a half-written file recovered from a crash, or a receipt
// from a future schema all land on ErrCorruptReceipt. Callers must treat that
// as "ownership is ambiguous", not as "there is no receipt": the difference is
// the difference between blocking a restart and spawning a duplicate.
func Decode(data []byte) (*Receipt, error) {
	var r Receipt
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptReceipt, err)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}
