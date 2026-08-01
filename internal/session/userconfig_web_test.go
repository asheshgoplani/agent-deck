package session

import (
	"os"
	"path/filepath"
	"testing"
)

// withTempHomeAndConfig sets HOME to a temp dir, writes the given config.toml
// contents (or no file when contents is empty), clears the user-config cache,
// and registers cleanup that restores HOME and clears the cache again. It
// returns the temp dir for tests that need to inspect the path.
func withTempHomeAndConfig(t *testing.T, contents string) string {
	t.Helper()
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	// Keep XDG_CONFIG_HOME inside this temp HOME too. TestMain clears XDG so
	// HOME-only isolation usually works, but this helper writes legacy config
	// files and should stay isolated even if a caller adds an XDG override.
	// An empty XDG config dir makes reads fall back to the legacy file below.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempDir, ".config"))
	t.Cleanup(func() {
		os.Setenv("HOME", originalHome)
		ClearUserConfigCache()
	})
	ClearUserConfigCache()

	if contents != "" {
		agentDeckDir := filepath.Join(tempDir, ".agent-deck")
		if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(contents), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	return tempDir
}

func TestWebSettings_DefaultsTrueWhenAbsent(t *testing.T) {
	withTempHomeAndConfig(t, `
[claude]
config_dir = "~/.claude"
`)
	if !GetWebMutationsEnabled() {
		t.Errorf("GetWebMutationsEnabled() = false, want true when [web] is absent")
	}
}

func TestWebSettings_DefaultsTrueWhenNoConfigFile(t *testing.T) {
	withTempHomeAndConfig(t, "")
	if !GetWebMutationsEnabled() {
		t.Errorf("GetWebMutationsEnabled() = false, want true when config.toml is missing")
	}
}

func TestWebSettings_ExplicitTrue(t *testing.T) {
	withTempHomeAndConfig(t, `
[web]
mutations_enabled = true
`)
	if !GetWebMutationsEnabled() {
		t.Errorf("GetWebMutationsEnabled() = false, want true when explicitly enabled")
	}
}

func TestWebSettings_ExplicitFalse(t *testing.T) {
	withTempHomeAndConfig(t, `
[web]
mutations_enabled = false
`)
	if GetWebMutationsEnabled() {
		t.Errorf("GetWebMutationsEnabled() = true, want false when explicitly disabled")
	}
}

// --- [web] trusted_domains / confirm_link_open (issue #1682) ---------------

func TestWebTrustedDomains_EmptyWhenAbsent(t *testing.T) {
	withTempHomeAndConfig(t, `
[web]
mutations_enabled = true
`)
	if got := GetWebTrustedDomains(); len(got) != 0 {
		t.Errorf("GetWebTrustedDomains() = %v, want empty when the key is absent", got)
	}
}

func TestWebTrustedDomains_ReadsAndNormalizes(t *testing.T) {
	withTempHomeAndConfig(t, `
[web]
trusted_domains = ["GitLab.Corp.Example", "https://gerrit.corp.example:8443/c/1", "  ", "*.ci.corp.example", "gitlab.corp.example"]
`)
	got := GetWebTrustedDomains()
	want := []string{"gitlab.corp.example", "gerrit.corp.example", "*.ci.corp.example"}
	if len(got) != len(want) {
		t.Fatalf("GetWebTrustedDomains() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("GetWebTrustedDomains()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWebConfirmLinkOpen_DefaultsTrue(t *testing.T) {
	withTempHomeAndConfig(t, `
[web]
trusted_domains = ["gitlab.corp.example"]
`)
	if !GetWebConfirmLinkOpen() {
		t.Error("GetWebConfirmLinkOpen() = false, want true when the key is omitted")
	}
}

func TestWebConfirmLinkOpen_ExplicitFalse(t *testing.T) {
	withTempHomeAndConfig(t, `
[web]
confirm_link_open = false
`)
	if GetWebConfirmLinkOpen() {
		t.Error("GetWebConfirmLinkOpen() = true, want false when explicitly disabled")
	}
}

func TestNormalizeTrustedDomains(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil stays nil", nil, nil},
		{"plain host", []string{"gitlab.corp.example"}, []string{"gitlab.corp.example"}},
		{"case folded", []string{"GitLab.CORP.example"}, []string{"gitlab.corp.example"}},
		{"trims space", []string{"  gitlab.corp.example  "}, []string{"gitlab.corp.example"}},
		{"strips scheme and path", []string{"https://gitlab.corp.example/group/repo/-/merge_requests/7"}, []string{"gitlab.corp.example"}},
		{"strips port", []string{"gerrit.corp.example:8443"}, []string{"gerrit.corp.example"}},
		{"strips userinfo", []string{"https://user:pw@gitlab.corp.example"}, []string{"gitlab.corp.example"}},
		{"strips trailing dot", []string{"gitlab.corp.example."}, []string{"gitlab.corp.example"}},
		{"keeps subdomain wildcard", []string{"*.corp.example"}, []string{"*.corp.example"}},
		{"wildcard with port", []string{"*.corp.example:8443"}, []string{"*.corp.example"}},
		{"ipv6 literal keeps brackets", []string{"[::1]:8443"}, []string{"[::1]"}},
		{"drops blanks", []string{"", "   ", "\t"}, nil},
		{"drops bare wildcard", []string{"*", "*.", "*.example"}, nil},
		{"drops embedded wildcard", []string{"git.*.corp.example"}, nil},
		{"drops scheme-only", []string{"https://"}, nil},
		{"dedupes after normalizing", []string{"gitlab.corp.example", "GITLAB.corp.example:443"}, []string{"gitlab.corp.example"}},
		{"preserves order", []string{"b.example", "a.example"}, []string{"b.example", "a.example"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeTrustedDomains(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("NormalizeTrustedDomains(%v) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("NormalizeTrustedDomains(%v)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}
