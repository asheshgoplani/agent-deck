package procowner

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Prober errors. Every caller distinguishes them: "the process is gone" is
// proof, "I could not look" is not.
var (
	// ErrNoProcess means the PID does not exist. This is positive evidence of
	// death, and the only evidence that lets a receipt be cleared.
	ErrNoProcess = errors.New("no such process")
	// ErrUnreadable means the PID may or may not exist but its identity could
	// not be read (permissions, a vanished /proc entry mid-read, a malformed
	// stat line). Fail closed.
	ErrUnreadable = errors.New("process identity is unreadable")
	// ErrUnsupported means this platform has no start-identity source, so
	// ownership cannot be established or checked here.
	ErrUnsupported = errors.New("process start identity is unsupported on this platform")
)

// ProcInfo is a live process observation.
type ProcInfo struct {
	PID     int
	PPID    int
	PGID    int
	UID     int
	Comm    string
	State   string
	StartID string
}

// IsZombie reports whether the observation is of a process that has already
// exited and is only waiting to be reaped by its parent.
//
// A zombie still has a /proc entry, still reports its original start identity,
// and can still be signalled without error — so without this check a process
// that died on the first SIGTERM reads as "survived SIGTERM and SIGKILL" for as
// long as its parent takes to call wait(). That turns a successful reap into a
// permanently un-clearable receipt.
func (p ProcInfo) IsZombie() bool {
	return p.State == "Z" || p.State == "X" || strings.HasPrefix(p.State, "Z")
}

// Prober reads process identity from the operating system. It is an interface
// so the whole state machine is testable without spawning processes, and so the
// platform-specific half stays as small as possible.
type Prober interface {
	// Name identifies the provider, recorded in the receipt so a receipt is
	// never verified against a different notion of start identity.
	Name() string
	// BootID returns an identifier for the current boot.
	BootID() (string, error)
	// Inspect returns the live identity of pid, or ErrNoProcess/ErrUnreadable.
	Inspect(pid int) (ProcInfo, error)
	// Descendants returns every process currently reachable from root by a
	// parent walk, each with its own start identity from the same observation.
	Descendants(root ProcInfo) ([]ProcInfo, error)
}

// MemberState is the verified state of one recorded identity.
type MemberState string

const (
	// StateOwned: the PID exists and its start identity matches the receipt.
	// This is the only state that may ever be signalled.
	StateOwned MemberState = "owned"
	// StateGone: the PID does not exist. Proof of death.
	StateGone MemberState = "gone"
	// StateStranger: the PID exists but carries a different start identity.
	//
	// This is the reused-PID case, and it says two things at once. The obvious
	// one: the process now holding that pid is a stranger and must never be
	// signalled. The less obvious one: a pid can only be reused after its
	// previous holder exited, so the process we recorded is DEAD — which is why
	// a stranger does not block a replacement spawn. Anything still alive from
	// the owned tree would still be holding its own pid and would verify as
	// owned; treating a stranger as ambiguous instead would block restarts
	// forever on any session whose short-lived children had their pids recycled,
	// while detecting nothing extra.
	StateStranger MemberState = "stranger"
	// StateUnknown: identity could not be determined, or matched a process now
	// running as a different user. Reported and left alone.
	StateUnknown MemberState = "unknown"
)

// Verdict is the receipt-level answer.
type Verdict string

const (
	// VerdictClear: every recorded identity is proven gone. Safe to clear the
	// receipt and admit a replacement spawn.
	VerdictClear Verdict = "clear"
	// VerdictOwned: at least one recorded identity is still ours and alive, and
	// nothing is ambiguous. A replacement must not be admitted until it is
	// terminated and verified.
	VerdictOwned Verdict = "owned"
	// VerdictUnknown: something could not be proven either way. Fail closed:
	// block the replacement, signal nothing, report the receipt.
	VerdictUnknown Verdict = "unknown"
)

// MemberStatus pairs a recorded identity with what verification found.
type MemberStatus struct {
	Member Member
	State  MemberState
	Detail string
}

// Report is the outcome of verifying a whole receipt.
type Report struct {
	Verdict Verdict
	Members []MemberStatus
	// Reason is a one-line, operator-facing explanation of the verdict.
	Reason string
	// BootChanged records that the receipt predates the current boot, which is
	// positive proof that every recorded identity is gone.
	BootChanged bool
}

// Owned returns the members that verified as still ours and alive.
func (r Report) Owned() []Member {
	var out []Member
	for _, s := range r.Members {
		if s.State == StateOwned {
			out = append(out, s.Member)
		}
	}
	return out
}

// Counts returns the number of members in each state.
func (r Report) Counts() map[MemberState]int {
	counts := map[MemberState]int{}
	for _, s := range r.Members {
		counts[s.State]++
	}
	return counts
}

// Describe renders the report for operator surfaces.
func (r Report) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "verdict=%s", r.Verdict)
	if r.Reason != "" {
		fmt.Fprintf(&b, " (%s)", r.Reason)
	}
	states := make([]string, 0, len(r.Members))
	for _, s := range r.Members {
		entry := fmt.Sprintf("%s -> %s", s.Member.String(), s.State)
		if s.Detail != "" {
			entry += ": " + s.Detail
		}
		states = append(states, entry)
	}
	sort.Strings(states)
	for _, s := range states {
		b.WriteString("\n  " + s)
	}
	return b.String()
}

// VerifyMember checks one recorded identity against the live process table.
//
// The identity check is exactly the ruling's: same PID AND same start time.
// The uid comparison on top of it can only ever turn "owned" into "unknown" —
// it never widens what may be signalled.
func VerifyMember(p Prober, m Member) MemberStatus {
	info, err := p.Inspect(m.PID)
	switch {
	case errors.Is(err, ErrNoProcess):
		return MemberStatus{Member: m, State: StateGone, Detail: "process does not exist"}
	case errors.Is(err, ErrUnsupported):
		return MemberStatus{Member: m, State: StateUnknown, Detail: "no start-identity provider on this platform"}
	case err != nil:
		return MemberStatus{Member: m, State: StateUnknown, Detail: "identity unreadable: " + err.Error()}
	}
	if info.StartID != m.StartID {
		return MemberStatus{
			Member: m,
			State:  StateStranger,
			Detail: fmt.Sprintf("pid reused: recorded start %s, live start %s", m.StartID, info.StartID),
		}
	}
	if info.UID != m.UID {
		return MemberStatus{
			Member: m,
			State:  StateUnknown,
			Detail: fmt.Sprintf("identity matches but the process now runs as uid %d (recorded %d)", info.UID, m.UID),
		}
	}
	return MemberStatus{Member: m, State: StateOwned}
}

// Verify checks every identity in a receipt and returns the receipt-level
// verdict. It never signals anything.
func Verify(p Prober, r *Receipt) Report {
	if r == nil {
		return Report{Verdict: VerdictClear, Reason: "no ownership receipt"}
	}
	if err := r.Validate(); err != nil {
		return Report{Verdict: VerdictUnknown, Reason: err.Error()}
	}
	if name := p.Name(); name != r.Provider {
		return Report{
			Verdict: VerdictUnknown,
			Reason: fmt.Sprintf("receipt was written by provider %q but this host verifies with %q",
				r.Provider, name),
		}
	}

	// A boot change is the one situation where every recorded identity is
	// provably gone without inspecting a single PID: start identities are
	// measured from boot, so nothing recorded before it can still be running.
	// Without this a rebooted host would carry a permanently ambiguous receipt
	// for every session that did not shut down cleanly.
	boot, err := p.BootID()
	if err != nil {
		return Report{Verdict: VerdictUnknown, Reason: "current boot id is unreadable: " + err.Error()}
	}
	if boot != r.BootID {
		members := r.All()
		statuses := make([]MemberStatus, 0, len(members))
		for _, m := range members {
			statuses = append(statuses, MemberStatus{
				Member: m,
				State:  StateGone,
				Detail: "recorded before the current boot",
			})
		}
		return Report{
			Verdict:     VerdictClear,
			Members:     statuses,
			Reason:      "receipt predates the current boot, so every recorded process is gone",
			BootChanged: true,
		}
	}

	members := r.All()
	statuses := make([]MemberStatus, 0, len(members))
	counts := map[MemberState]int{}
	for _, m := range members {
		status := VerifyMember(p, m)
		statuses = append(statuses, status)
		counts[status.State]++
	}

	report := Report{Members: statuses}
	switch {
	case counts[StateUnknown] > 0:
		report.Verdict = VerdictUnknown
		report.Reason = fmt.Sprintf("%d recorded process(es) could not be verified; nothing was signalled",
			counts[StateUnknown])
	case counts[StateOwned] > 0:
		report.Verdict = VerdictOwned
		report.Reason = fmt.Sprintf("%d owned process(es) are still alive", counts[StateOwned])
	default:
		report.Verdict = VerdictClear
		report.Reason = "every recorded process is gone"
		if counts[StateStranger] > 0 {
			report.Reason = fmt.Sprintf("every recorded process is gone (%d pid(s) have since been reused by unrelated processes, which were not signalled)",
				counts[StateStranger])
		}
	}
	return report
}
