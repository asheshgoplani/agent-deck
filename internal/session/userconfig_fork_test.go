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
