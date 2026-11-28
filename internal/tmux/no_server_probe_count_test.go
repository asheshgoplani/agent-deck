package tmux

import (
	"fmt"
	"testing"
)

// The path the user hit: after a reboot (no socket file) or a tmux server death
// (a stale socket file), `agent-deck list --json` read every stored session's
// status through Exists / ExistsListed (status pass), Exists again (substate),
// PaneDeadExitStatus, HasSessionOnSocket and the batched pane capture. With no
// server, each of those started its own tmux client per row, all answering the
// same "gone": 200 stopped sessions cost 424 tmux processes per listing.
//
// Once one client on the socket has found no server, the rest of the reads must
// not start any more tmux processes. Only APIs that exist before the fix are
// used here, so this test runs (and fails by assertion) on the base.
func TestExists_NoServerStatusReadsStartNoFurtherTmuxClients(t *testing.T) {
	for _, kind := range serverlessKinds() {
		t.Run(kind.name, func(t *testing.T) {
			socket, _ := serverlessSocket(t, kind.stale)

			// The status pass lists the socket once per pass.
			names, err := ListSessionNamesOnSocket(socket)
			if kind.stale && (err != nil || len(names) != 0) {
				t.Fatalf("ListSessionNamesOnSocket on a stale socket = (%v, %v), want an empty listing", names, err)
			}
			if !kind.stale && err == nil {
				t.Fatalf("ListSessionNamesOnSocket with no socket file = (%v, nil), want an error (unknown, not empty)", names)
			}

			sessions := make([]*Session, 20)
			for i := range sessions {
				sessions[i] = &Session{Name: fmt.Sprintf("agentdeck_nsprow%d_0123abcd", i), SocketName: socket}
			}
			before := SnapshotStarts()
			for _, s := range sessions {
				if s.Exists() {
					t.Fatalf("Exists(%s) = true on a socket with no server", s.Name)
				}
				if exists, listedAbsent := s.ExistsListed(func() map[string]struct{} { return nil }); exists || listedAbsent {
					t.Fatalf("ExistsListed(%s) = (%v, %v), want (false, false)", s.Name, exists, listedAbsent)
				}
				if s.Exists() {
					t.Fatalf("second Exists(%s) = true on a socket with no server", s.Name)
				}
				if code, ok := s.PaneDeadExitStatus(); ok {
					t.Fatalf("PaneDeadExitStatus(%s) = (%d, true) with no server", s.Name, code)
				}
				if HasSessionOnSocket(socket, s.TmuxName()) {
					t.Fatalf("HasSessionOnSocket(%s) = true with no server", s.Name)
				}
			}
			if primed := PrimePaneCaptures(sessions); primed != 0 {
				t.Fatalf("PrimePaneCaptures primed %d panes with no server", primed)
			}
			if n := before.Charged(); n != 0 {
				t.Errorf("status reads of %d sessions on a serverless socket started %d tmux clients after the listing, want 0", len(sessions), n)
			}
		})
	}
}
