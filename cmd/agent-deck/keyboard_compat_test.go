package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ui"
)

func TestExecSelfRestartDisablesModifyOtherKeys(t *testing.T) {
	var out bytes.Buffer
	err := execSelfRestart(&out, filepath.Join(t.TempDir(), "missing"), ui.RestartHandoff{})
	if err == nil {
		t.Fatal("expected exec to fail for a missing executable")
	}
	if got, want := out.String(), "\x1b[>4;0m"; got != want {
		t.Fatalf("wrote %q before exec, want %q", got, want)
	}
}

func TestEnableModifyOtherKeysHonoursExtendedKeysOff(t *testing.T) {
	tests := []struct {
		name    string
		options map[string]string
		want    string
	}{
		{name: "default", want: "\x1b[>4;1m"},
		{name: "explicit on", options: map[string]string{"extended-keys": "on"}, want: "\x1b[>4;1m"},
		{name: "explicit off", options: map[string]string{"extended-keys": "off"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			enableModifyOtherKeys(&out, tt.options)
			if got := out.String(); got != tt.want {
				t.Fatalf("wrote %q, want %q", got, tt.want)
			}
		})
	}
}
