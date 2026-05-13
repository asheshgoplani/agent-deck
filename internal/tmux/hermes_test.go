package tmux

import "testing"

// Hermes Agent CLI: tmux-layer detection tests.

func TestDetectToolFromCommand_Hermes(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"bare hermes", "hermes", "hermes"},
		{"hermes with yolo flag", "hermes --yolo", "hermes"},
		{"hermes absolute path", "/usr/local/bin/hermes", "hermes"},
		{"hermes home path", "/home/user/.local/bin/hermes", "hermes"},
		{"uppercase binary", "HERMES", "hermes"},
		{"hermes with args", "hermes --resume 20260225_143052_a1b2c3", "hermes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectToolFromCommand(tt.command); got != tt.want {
				t.Fatalf("detectToolFromCommand(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestDetectToolFromCommand_Hermes_Negative(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		// Note: strings.Contains fallback is intentionally permissive
		// (same as copilot matching "npx @github/copilot"). Only the
		// basename switch provides strict matching.
		{"herm is not hermes", "herm"},
		{"prometheus is not hermes", "prometheus --config.file=prometheus.yml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectToolFromCommand(tt.command)
			if got == "hermes" {
				t.Fatalf("detectToolFromCommand(%q) = %q, should NOT match hermes", tt.command, got)
			}
		})
	}
}

func TestDefaultRawPatterns_Hermes_ReturnsNil(t *testing.T) {
	// Hermes has no content-sniffing patterns (deferred).
	// DefaultRawPatterns should return nil, which is handled gracefully.
	raw := DefaultRawPatterns("hermes")
	if raw != nil {
		t.Errorf("DefaultRawPatterns(\"hermes\") = %+v, want nil (no patterns registered)", raw)
	}
}
