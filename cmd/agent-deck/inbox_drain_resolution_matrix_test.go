package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// writeCorruptInboxProfile makes profile's state database unreadable.
func writeCorruptInboxProfile(t *testing.T, profile string) {
	t.Helper()
	dbPath, err := session.GetDBPathForProfile(profile)
	if err != nil {
		t.Fatalf("corrupt profile path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatalf("mkdir corrupt profile: %v", err)
	}
	if err := os.WriteFile(dbPath, []byte("this is not sqlite"), 0o600); err != nil {
		t.Fatalf("write corrupt profile: %v", err)
	}
}

// TestInboxDrain_ResolutionMatrix pins the whole drain-target resolution
// contract in one place. Drain is destructive, so every cell either names
// exactly one session or fails with the documented exit code (2 not found,
// 3 ambiguous, 1 unreadable store). The resolver was fixed three times
// (#2022, #2030, #2038); each row notes the change that made it escape or
// the gap it closes. The profile order is passed explicitly so the
// corrupt-store rows cover both scan orders.
func TestInboxDrain_ResolutionMatrix(t *testing.T) {
	type sess struct{ title, id string }
	inst := func(title, id string) sess { return sess{title: title, id: id} }

	for _, tc := range []struct {
		name       string
		stores     map[string][]sess // profile -> sessions
		corrupt    []string          // profiles with an unreadable store
		order      []string          // profile scan order
		identifier string
		effective  string
		qualified  bool   // -p/--profile given
		wantID     string // set on success
		wantCode   int    // set on failure
		wantInErr  []string
	}{
		{
			name:       "unknown id is not found, not an empty inbox (#1991)",
			stores:     map[string][]sess{"work": {inst("alpha", "id-alpha")}},
			order:      []string{"work"},
			identifier: "does-not-exist",
			effective:  "work",
			wantCode:   2,
			wantInErr:  []string{"does-not-exist", "NOT an empty inbox"},
		},
		{
			name:       "full id in another profile resolves (#2030 false missing)",
			stores:     map[string][]sess{"personal": {inst("remote", "id-remote")}, "work": nil},
			order:      []string{"personal", "work"},
			identifier: "id-remote",
			effective:  "work",
			wantID:     "id-remote",
		},
		{
			name:       "full id outside an explicit profile is not found",
			stores:     map[string][]sess{"personal": {inst("remote", "id-remote")}, "work": nil},
			order:      []string{"personal", "work"},
			identifier: "id-remote",
			effective:  "work",
			qualified:  true,
			wantCode:   2,
		},
		{
			name:       "duplicate full id across profiles is ambiguous (#2030)",
			stores:     map[string][]sess{"aaa": {inst("a", "id-dup")}, "bbb": {inst("b", "id-dup")}, "work": nil},
			order:      []string{"aaa", "bbb", "work"},
			identifier: "id-dup",
			effective:  "work",
			wantCode:   3,
			wantInErr:  []string{"aaa", "bbb", "-p/--profile"},
		},
		{
			name:       "duplicate full id qualified to one profile resolves",
			stores:     map[string][]sess{"aaa": {inst("a", "id-dup")}, "bbb": {inst("b", "id-dup")}},
			order:      []string{"aaa", "bbb"},
			identifier: "id-dup",
			effective:  "bbb",
			qualified:  true,
			wantID:     "id-dup",
		},
		{
			name:       "ambiguous prefix keeps its ambiguity (#2030 swallowed)",
			stores:     map[string][]sess{"work": {inst("alpha", "abcdef01-1777000200"), inst("beta", "abcdef02-1777000201")}},
			order:      []string{"work"},
			identifier: "abcdef",
			effective:  "work",
			wantCode:   3,
			wantInErr:  []string{"alpha", "beta"},
		},
		{
			name:       "prefix stays scoped to the effective profile",
			stores:     map[string][]sess{"aaa": {inst("remote", "abcdef02-1777000201")}, "work": {inst("local", "abcdef01-1777000200")}},
			order:      []string{"aaa", "work"},
			identifier: "abcdef",
			effective:  "work",
			wantID:     "abcdef01-1777000200",
		},
		{
			name:       "effective-profile exact title resolves to its owner",
			stores:     map[string][]sess{"work": {inst("worker", "id-worker")}},
			order:      []string{"work"},
			identifier: "worker",
			effective:  "work",
			wantID:     "id-worker",
		},
		{
			name:       "title in a non-effective profile is not found",
			stores:     map[string][]sess{"personal": {inst("worker", "id-worker")}, "work": nil},
			order:      []string{"personal", "work"},
			identifier: "worker",
			effective:  "work",
			wantCode:   2,
		},
		{
			name:       "title equal to its own id is not a collision",
			stores:     map[string][]sess{"work": {inst("id-self", "id-self")}},
			order:      []string{"work"},
			identifier: "id-self",
			effective:  "work",
			wantID:     "id-self",
		},
		{
			name:       "full id elsewhere plus local title equal to it is ambiguous (#2038)",
			stores:     map[string][]sess{"personal": {inst("exact-owner", "id-x")}, "work": {inst("id-x", "id-title-owner")}},
			order:      []string{"personal", "work"},
			identifier: "id-x",
			effective:  "work",
			wantCode:   3,
			wantInErr:  []string{"exact-owner", "id-title-owner", "personal", "work"},
		},
		{
			name:       "id/title collision stays ambiguous under an explicit profile (#2038)",
			stores:     map[string][]sess{"personal": {inst("exact-owner", "id-x")}, "work": {inst("id-x", "id-title-owner")}},
			order:      []string{"personal", "work"},
			identifier: "id-x",
			effective:  "work",
			qualified:  true,
			wantCode:   3,
			wantInErr:  []string{"exact-owner", "id-title-owner"},
		},
		{
			name:       "foreign title equal to a local full id does not collide",
			stores:     map[string][]sess{"personal": {inst("id-local", "id-other")}, "work": {inst("local", "id-local")}},
			order:      []string{"personal", "work"},
			identifier: "id-local",
			effective:  "work",
			wantID:     "id-local",
		},
		{
			name:       "corrupt store first fails closed on a unique id (#2038)",
			stores:     map[string][]sess{"work": {inst("target", "id-target")}},
			corrupt:    []string{"aaa-corrupt"},
			order:      []string{"aaa-corrupt", "work"},
			identifier: "id-target",
			effective:  "work",
			wantCode:   1,
			wantInErr:  []string{"aaa-corrupt", "resolution aborted"},
		},
		{
			name:       "corrupt store last fails closed on a unique id (#2038)",
			stores:     map[string][]sess{"work": {inst("target", "id-target")}},
			corrupt:    []string{"zzz-corrupt"},
			order:      []string{"work", "zzz-corrupt"},
			identifier: "id-target",
			effective:  "work",
			wantCode:   1,
			wantInErr:  []string{"zzz-corrupt", "resolution aborted"},
		},
		{
			name:       "corrupt store does not hide a known id/title collision (#2038)",
			stores:     map[string][]sess{"personal": {inst("exact-owner", "id-x")}, "work": {inst("id-x", "id-title-owner")}},
			corrupt:    []string{"aaa-corrupt"},
			order:      []string{"aaa-corrupt", "personal", "work"},
			identifier: "id-x",
			effective:  "work",
			wantCode:   3,
			wantInErr:  []string{"exact-owner", "id-title-owner", "aaa-corrupt"},
		},
		{
			name:       "corrupt store does not hide a known duplicate id",
			stores:     map[string][]sess{"aaa": {inst("a", "id-dup")}, "bbb": {inst("b", "id-dup")}, "work": nil},
			corrupt:    []string{"aaa-corrupt"},
			order:      []string{"aaa-corrupt", "aaa", "bbb", "work"},
			identifier: "id-dup",
			effective:  "work",
			wantCode:   3,
			wantInErr:  []string{"aaa", "bbb", "aaa-corrupt"},
		},
		{
			name:       "qualified duplicate id still fails closed on a corrupt store",
			stores:     map[string][]sess{"aaa": {inst("a", "id-dup")}, "bbb": {inst("b", "id-dup")}},
			corrupt:    []string{"zzz-corrupt"},
			order:      []string{"aaa", "bbb", "zzz-corrupt"},
			identifier: "id-dup",
			effective:  "bbb",
			qualified:  true,
			wantCode:   1,
			wantInErr:  []string{"zzz-corrupt", "resolution aborted"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cliInboxTestHome(t)
			for profile, sessions := range tc.stores {
				instances := make([]*session.Instance, 0, len(sessions))
				for _, s := range sessions {
					i := session.NewInstance(s.title, t.TempDir())
					i.ID = s.id
					instances = append(instances, i)
				}
				saveInboxResolutionSessions(t, profile, instances...)
			}
			for _, profile := range tc.corrupt {
				writeCorruptInboxProfile(t, profile)
			}

			got, err := resolveInboxDrainSessionInProfiles(tc.identifier, tc.effective, tc.qualified, tc.order)
			if tc.wantID != "" {
				if err != nil {
					t.Fatalf("resolve %q: unexpected error (exit %d): %v", tc.identifier, inboxExitCode(err), err)
				}
				if got != tc.wantID {
					t.Fatalf("resolve %q = %q, want %q", tc.identifier, got, tc.wantID)
				}
				return
			}
			if err == nil {
				t.Fatalf("resolve %q = %q, want failure with exit %d", tc.identifier, got, tc.wantCode)
			}
			if code := inboxExitCode(err); code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d: %v", code, tc.wantCode, err)
			}
			if !strings.Contains(err.Error(), "Nothing was drained") {
				t.Fatalf("resolution failure lacks the fail-closed contract: %v", err)
			}
			for _, want := range tc.wantInErr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q missing %q", err, want)
				}
			}
		})
	}
}
