package main

import (
	"bytes"
	"testing"
)

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
