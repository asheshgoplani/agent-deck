package procowner

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// MaxMembers caps how many identities one receipt records.
//
// A tool that forks short-lived helpers (a shell loop, an MCP child per
// request) produces a new pid every fraction of a second, and every one of them
// is a genuine descendant while it lives. Without a ceiling the receipt would
// grow for as long as the attribution window lasts, and a receipt that cannot
// be written is a receipt that cannot be verified. The cap fails in the safe
// direction: past it we record fewer processes, never more, so the worst case
// is a process we decline to claim — and therefore never signal.
const MaxMembers = 512

// ClaimInput is everything a spawn knows about what it just launched.
type ClaimInput struct {
	InstanceID string
	Generation uint64
	// PanePID is the pane's initial process: the leader of the tree this spawn
	// owns.
	PanePID    int
	TmuxName   string
	TmuxSocket string
	Command    string
	Now        func() time.Time
}

// Claim builds the spawn-time receipt for a freshly launched pane.
//
// It runs at spawn, in the caller, before anything can have died — that is the
// whole difference between a receipt and a guess. If the leader's identity
// cannot be read here, no receipt is written at all: agent-deck records no
// ownership rather than a claim it could not substantiate, and consequently
// will never signal on the strength of one.
func Claim(p Prober, in ClaimInput) (*Receipt, error) {
	if in.PanePID <= 1 {
		return nil, fmt.Errorf("%w: pane pid %d", ErrNoProcess, in.PanePID)
	}
	if in.PanePID == os.Getpid() {
		// Defensive: a receipt naming agent-deck itself would authorise
		// agent-deck to kill agent-deck.
		return nil, fmt.Errorf("%w: pane pid %d is this process", ErrUnreadable, in.PanePID)
	}
	boot, err := p.BootID()
	if err != nil {
		return nil, err
	}
	info, err := p.Inspect(in.PanePID)
	if err != nil {
		return nil, err
	}
	now := in.Now
	if now == nil {
		now = time.Now
	}
	stamp := now().Unix()
	return &Receipt{
		Version:    ReceiptVersion,
		InstanceID: in.InstanceID,
		Generation: in.Generation,
		State:      StateLive,
		Provider:   p.Name(),
		BootID:     boot,
		TmuxName:   in.TmuxName,
		TmuxSocket: in.TmuxSocket,
		Command:    in.Command,
		CreatedAt:  stamp,
		UpdatedAt:  stamp,
		Leader: Member{
			PID:     info.PID,
			StartID: info.StartID,
			PGID:    info.PGID,
			UID:     info.UID,
			Comm:    info.Comm,
			Role:    RoleLeader,
			SeenAt:  stamp,
		},
	}, nil
}

// Attribute adds the leader's live descendants to the receipt, each with its
// own start identity.
//
// It is the only way a member ever enters a receipt, and it refuses to run
// unless the LEADER still verifies as owned. That is what makes the result
// ownership rather than a process-table guess: every member recorded here was,
// at the moment of a single stat read, a live descendant of a process this
// spawn is proven to own.
//
// Two further narrowings, both cheap and both one-directional (they can only
// reject a candidate, never admit one):
//
//   - a descendant cannot have started before its ancestor, so a member whose
//     start identity predates the leader's is dropped;
//   - agent-deck's own pid is never recorded.
//
// Returns the members added. The caller decides whether to persist; nothing
// here writes to disk.
func Attribute(p Prober, r *Receipt, now func() time.Time) ([]Member, error) {
	if r == nil {
		return nil, errors.New("procowner: nil receipt")
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if p.Name() != r.Provider {
		return nil, fmt.Errorf("provider mismatch: receipt %q, host %q", r.Provider, p.Name())
	}
	leaderStatus := VerifyMember(p, r.Leader)
	if leaderStatus.State != StateOwned {
		// The leader is gone, reused or unreadable. Anything reachable from
		// that pid now is not attributable to this spawn.
		return nil, fmt.Errorf("leader is %s: %s", leaderStatus.State, leaderStatus.Detail)
	}
	leaderInfo, err := p.Inspect(r.Leader.PID)
	if err != nil {
		return nil, err
	}
	descendants, err := p.Descendants(leaderInfo)
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	stamp := now().Unix()
	self := os.Getpid()

	var added []Member
	for _, info := range descendants {
		if len(r.Members) >= MaxMembers {
			break
		}
		if info.PID <= 1 || info.PID == self {
			continue
		}
		notBefore, cmpErr := startNotBefore(p, info.StartID, r.Leader.StartID)
		if cmpErr != nil || !notBefore {
			// Either the start identities are not comparable, or this process
			// started before the leader did and therefore cannot be its
			// descendant. Both mean: do not claim it.
			continue
		}
		m := Member{
			PID:     info.PID,
			StartID: info.StartID,
			PGID:    info.PGID,
			UID:     info.UID,
			Comm:    info.Comm,
			Role:    RoleDescendant,
			SeenAt:  stamp,
		}
		if r.HasMember(m) {
			continue
		}
		r.Members = append(r.Members, m)
		added = append(added, m)
	}
	if len(added) > 0 {
		r.UpdatedAt = stamp
	}
	return added, nil
}

// startNotBefore reports whether start identity a is no earlier than b.
func startNotBefore(p Prober, a, b string) (bool, error) {
	cmp, err := CompareStart(p, a, b)
	if err != nil {
		return false, err
	}
	return cmp >= 0, nil
}

// StartComparer is implemented by providers whose start identities are ordered.
// Ordering is what lets Attribute reject a "descendant" that started before its
// ancestor.
type StartComparer interface {
	// CompareStart returns -1, 0 or 1, or an error when the identities cannot
	// be compared.
	CompareStart(a, b string) (int, error)
}

// CompareStart compares two start identities using the provider's own ordering.
// A provider that cannot order its identities makes every comparison an error,
// which Attribute treats as "do not record" rather than "record anyway".
func CompareStart(p Prober, a, b string) (int, error) {
	comparer, ok := p.(StartComparer)
	if !ok {
		return 0, fmt.Errorf("%w: provider %q cannot order start identities", ErrUnsupported, p.Name())
	}
	return comparer.CompareStart(a, b)
}
