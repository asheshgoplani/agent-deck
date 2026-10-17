package main

import (
	"errors"
	"testing"
)

// The session send result tells the queue which acceptance refusals are a
// provably unavailable identity: the same class --codex-composer-fallback
// accepts (#2549).
func TestIssue2549_RefusalDataMarksUnavailableIdentity(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errCodexIdentityUnavailable, true},
		{errCodexGenerationUnavailable, true},
		{errors.New("live Codex session identity is already owned by another session"), false},
		{errors.New("Codex acceptance lock is held"), false},
	} {
		data := codexAcceptanceRefusalData(tc.err)
		if data["delivery"] != deliveryAcceptanceRefused {
			t.Fatalf("%v: delivery %v", tc.err, data["delivery"])
		}
		if got, _ := data["acceptance_unavailable"].(bool); got != tc.want {
			t.Fatalf("%v: acceptance_unavailable = %v, want %v", tc.err, got, tc.want)
		}
	}
}
