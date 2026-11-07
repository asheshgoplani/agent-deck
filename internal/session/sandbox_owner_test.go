package session

import "testing"

// ASK-172: every sandbox carries its owner. [sandbox] owner wins; without
// it the login name ($USER) is used, so two people on their own machines
// get their own names with no setup.
func TestSandboxOwner(t *testing.T) {
	tests := []struct {
		name string
		cfg  *UserConfig
		user string
		want string
	}{
		{name: "config wins over $USER", cfg: &UserConfig{Sandbox: SandboxSettings{Owner: "alice"}}, user: "dev", want: "alice"},
		{name: "config value is trimmed", cfg: &UserConfig{Sandbox: SandboxSettings{Owner: "  alice  "}}, user: "dev", want: "alice"},
		{name: "no config falls back to $USER", cfg: &UserConfig{}, user: "bob", want: "bob"},
		{name: "blank config falls back to $USER", cfg: &UserConfig{Sandbox: SandboxSettings{Owner: "   "}}, user: "bob", want: "bob"},
		{name: "nil config falls back to $USER", cfg: nil, user: "bob", want: "bob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("USER", tt.user)
			if got := tt.cfg.SandboxOwner(); got != tt.want {
				t.Fatalf("SandboxOwner() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The [sandbox] owner key round-trips through config.toml.
func TestSandboxOwner_ConfigKeyRoundTrips(t *testing.T) {
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	if err := SaveUserConfig(&UserConfig{Sandbox: SandboxSettings{Owner: "alice"}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := ReloadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("USER", "someone-else")
	if got := cfg.SandboxOwner(); got != "alice" {
		t.Fatalf("SandboxOwner() after reload = %q, want alice", got)
	}
}
