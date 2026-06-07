package session

import (
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
)

func decodeForkConfig(t *testing.T, doc string) UserConfig {
	t.Helper()
	var cfg UserConfig
	if _, err := toml.Decode(doc, &cfg); err != nil {
		t.Fatalf("toml.Decode: %v", err)
	}
	return cfg
}

func TestForkSettings_StructuralDefaults_WhenSectionAbsent(t *testing.T) {
	cfg := decodeForkConfig(t, ``)
	assert.True(t, cfg.Fork.GetWorktree(), "worktree default ON when unset")
	assert.True(t, cfg.Fork.GetWithState(), "with_state default ON when unset")
	assert.True(t, cfg.Fork.GetWithIgnored(), "with_ignored default ON when unset")
	assert.Equal(t, "auto", cfg.Fork.GetDocker(), "docker default 'auto' when unset")
	assert.Equal(t, "fork/", cfg.Fork.GetBranchPrefix(), "branch_prefix default when unset")
	assert.False(t, cfg.Fork.InheritFromParent, "inherit_from_parent default false")
}

func TestForkSettings_ExplicitFalseHonored(t *testing.T) {
	cfg := decodeForkConfig(t, "[fork]\nworktree = false\nwith_state = false\nwith_ignored = false\n")
	assert.False(t, cfg.Fork.GetWorktree())
	assert.False(t, cfg.Fork.GetWithState())
	assert.False(t, cfg.Fork.GetWithIgnored())
}

func TestForkSettings_GetDocker_Canonicalizes(t *testing.T) {
	cases := map[string]string{
		`[fork]` + "\n" + `docker = "ON"`:    "on",
		`[fork]` + "\n" + `docker = " Off "`: "off",
		`[fork]` + "\n" + `docker = "auto"`:  "auto",
		`[fork]` + "\n" + `docker = "bogus"`: "auto", // unknown -> default
	}
	for doc, want := range cases {
		cfg := decodeForkConfig(t, doc)
		assert.Equal(t, want, cfg.Fork.GetDocker(), "doc=%q", doc)
	}
}

func TestForkSettings_GetBranchPrefix_Override(t *testing.T) {
	cfg := decodeForkConfig(t, "[fork]\nbranch_prefix = \"wip/\"\n")
	assert.Equal(t, "wip/", cfg.Fork.GetBranchPrefix())
}

// A whitespace-only or padded branch_prefix would otherwise pass through and
// produce invalid fork branch names (e.g. "  /slug"). GetBranchPrefix must trim,
// mirroring GetDocker's canonicalization.
func TestForkSettings_GetBranchPrefix_TrimsWhitespace(t *testing.T) {
	assert.Equal(t, "fork/", ForkSettings{BranchPrefix: "   "}.GetBranchPrefix(), "whitespace-only -> default")
	assert.Equal(t, "wip/", ForkSettings{BranchPrefix: "  wip/  "}.GetBranchPrefix(), "padded value is trimmed")
}

func TestForkSettings_Resolve_ComprehensiveDefault_DockerAuto(t *testing.T) {
	cfg := decodeForkConfig(t, ``) // all defaults
	// parent NOT sandboxed -> auto resolves sandbox off
	p := cfg.Fork.Resolve(false)
	assert.Equal(t, ResolvedForkPlan{Worktree: true, WithState: true, WithIgnored: true, Sandbox: false}, p)
	// parent sandboxed -> auto resolves sandbox on
	p = cfg.Fork.Resolve(true)
	assert.True(t, p.Sandbox, "docker=auto with sandboxed parent -> sandbox on")
}

func TestForkSettings_Resolve_DockerOnOff_OverrideParent(t *testing.T) {
	on := decodeForkConfig(t, "[fork]\ndocker = \"on\"\n").Fork.Resolve(false)
	assert.True(t, on.Sandbox, "docker=on forces sandbox even if parent not sandboxed")
	off := decodeForkConfig(t, "[fork]\ndocker = \"off\"\n").Fork.Resolve(true)
	assert.False(t, off.Sandbox, "docker=off forces no sandbox even if parent sandboxed")
}

func TestForkSettings_Resolve_InheritFromParent_OverridesStructuralKeys(t *testing.T) {
	// Even with structural keys turned off, inherit_from_parent forces the
	// comprehensive worktree+state mapping and matches parent docker.
	cfg := decodeForkConfig(t, "[fork]\ninherit_from_parent = true\nworktree = false\nwith_state = false\nwith_ignored = false\ndocker = \"off\"\n")
	p := cfg.Fork.Resolve(true) // parent sandboxed
	assert.Equal(t, ResolvedForkPlan{Worktree: true, WithState: true, WithIgnored: true, Sandbox: true}, p)
}

func TestForkSettings_Resolve_WithIgnoredImpliesWithState(t *testing.T) {
	cfg := decodeForkConfig(t, "[fork]\nwith_state = false\nwith_ignored = true\n")
	p := cfg.Fork.Resolve(false)
	assert.True(t, p.WithState, "with_ignored must imply with_state")
	assert.True(t, p.WithIgnored)
}
