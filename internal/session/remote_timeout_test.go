package session

import (
	"testing"
	"time"
)

// The hardcoded 10s remote command timeout made any remote whose
// `list --json` exceeds it look permanently unavailable. The timeout is
// now per-remote configurable with a 30s default.
func TestRemoteConfig_GetCommandTimeout_Default(t *testing.T) {
	rc := RemoteConfig{Host: "user@host"}
	if got := rc.GetCommandTimeout(); got != 30*time.Second {
		t.Fatalf("default timeout = %v, want 30s", got)
	}
}

func TestRemoteConfig_GetCommandTimeout_Configured(t *testing.T) {
	rc := RemoteConfig{Host: "user@host", CommandTimeoutSeconds: 90}
	if got := rc.GetCommandTimeout(); got != 90*time.Second {
		t.Fatalf("configured timeout = %v, want 90s", got)
	}
}

func TestRemoteConfig_GetCommandTimeout_RejectsNonPositive(t *testing.T) {
	rc := RemoteConfig{Host: "user@host", CommandTimeoutSeconds: -5}
	if got := rc.GetCommandTimeout(); got != 30*time.Second {
		t.Fatalf("negative timeout = %v, want 30s fallback", got)
	}
}

func TestNewSSHRunner_CarriesConfiguredTimeout(t *testing.T) {
	r := NewSSHRunner("g14", RemoteConfig{Host: "user@host", CommandTimeoutSeconds: 90})
	if r.commandTimeout != 90*time.Second {
		t.Fatalf("runner timeout = %v, want 90s", r.commandTimeout)
	}
}
