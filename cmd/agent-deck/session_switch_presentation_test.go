package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSessionSwitchPresentationStatus(t *testing.T) {
	tests := []struct {
		name      string
		committed bool
		ready     bool
		want      string
	}{
		{name: "ready success", committed: true, ready: true, want: "success"},
		{name: "committed native readiness pending", committed: true, ready: false, want: "pending"},
		{name: "failure", committed: false, ready: false, want: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := switchPresentationStatus(tt.committed, tt.ready); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCrossHarnessSwitchPresentationStatus(t *testing.T) {
	if got := crossHarnessPresentationStatus(&session.CrossHarnessSwitchResult{TargetCreated: true, Pending: true}); got != "pending" {
		t.Fatalf("pending cross-harness status = %q, want pending", got)
	}
	if got := crossHarnessPresentationStatus(&session.CrossHarnessSwitchResult{TargetCreated: true, TargetReady: true}); got != "success" {
		t.Fatalf("ready cross-harness status = %q, want success", got)
	}
	if got := crossHarnessPresentationStatus(nil); got != "failed" {
		t.Fatalf("absent cross-harness status = %q, want failed", got)
	}
}

func TestReadinessPresentation(t *testing.T) {
	if got := readinessPresentation(false); got != "pending" {
		t.Fatalf("not-ready presentation = %q, want pending", got)
	}
	if got := readinessPresentation(true); got != "ready" {
		t.Fatalf("ready presentation = %q, want ready", got)
	}
}
