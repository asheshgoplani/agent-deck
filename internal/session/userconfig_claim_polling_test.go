package session

import "testing"

func TestClaimPollingDefaultOff(t *testing.T) {
	var c UserConfig
	if c.ClaimPollingEnabled() {
		t.Error("claim_polling must default to false")
	}
	var nilC *UserConfig
	if nilC.ClaimPollingEnabled() {
		t.Error("nil UserConfig must report false")
	}
}

func TestClaimPollingEnabled(t *testing.T) {
	on := true
	c := UserConfig{Performance: PerformanceSettings{ClaimPolling: &on}}
	if !c.ClaimPollingEnabled() {
		t.Error("claim_polling=true not honored")
	}
}
