package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveGroupClaudeHomeSkillsAllowsIdenticalSharedHome(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "~/.claude-shared"
[groups.alpha.claude]
skills = ["store/alpha"]
[groups.beta.claude]
skills = ["store/alpha"]
`)

	gotHome, skills, err := ResolveGroupClaudeHomeSkills("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if gotHome != filepath.Join(home, ".claude-shared") {
		t.Fatalf("home=%q", gotHome)
	}
	if len(skills) != 1 || skills[0] != "store/alpha" {
		t.Fatalf("skills=%v", skills)
	}
}

func TestResolveGroupClaudeHomeSkillsRejectsDivergentSharedHome(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "~/.claude-shared"
[groups.alpha.claude]
skills = ["store/alpha"]
[groups.beta.claude]
skills = ["store/beta"]
`)

	_, _, err := ResolveGroupClaudeHomeSkills("alpha")
	if err == nil || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("expected beta conflict, got %v", err)
	}
}

func TestResolveGroupClaudeHomeSkillsRejectsEmptyGroupSharingPopulatedHome(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "~/.claude-shared"
[groups.alpha.claude]
skills = ["store/alpha"]
[groups.beta.claude]
skills = []
`)

	_, _, err := ResolveGroupClaudeHomeSkills("beta")
	if err == nil || !strings.Contains(err.Error(), "alpha") {
		t.Fatalf("expected populated-home conflict, got %v", err)
	}
}

func TestResolveGroupClaudeHomeSkillsRejectsChildAdditionInSharedHome(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "~/.claude-shared"
[groups.work.claude]
skills = ["store/alpha"]
[groups."work/sub".claude]
skills = ["store/beta"]
`)

	_, _, err := ResolveGroupClaudeHomeSkills("work/sub")
	if err == nil || !strings.Contains(err.Error(), "work") {
		t.Fatalf("expected parent conflict, got %v", err)
	}
}

func TestResolveGroupClaudeHomeSkillsAllowsIsolatedChildAddition(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "~/.claude-shared"
[groups.work.claude]
skills = ["store/alpha"]
[groups."work/sub".claude]
config_dir = "~/.claude-sub"
skills = ["store/beta"]
`)

	gotHome, skills, err := ResolveGroupClaudeHomeSkills("work/sub")
	if err != nil {
		t.Fatal(err)
	}
	if gotHome != filepath.Join(home, ".claude-sub") {
		t.Fatalf("home=%q", gotHome)
	}
	if len(skills) != 2 || skills[0] != "store/alpha" || skills[1] != "store/beta" {
		t.Fatalf("skills=%v", skills)
	}
}

func TestResolveGroupClaudeHomeSkillsRejectsAliasedSharedHome(t *testing.T) {
	home := t.TempDir()
	realParent := filepath.Join(home, "real")
	if err := os.MkdirAll(realParent, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(home, "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Fatal(err)
	}
	withIsolatedHomeAndConfig(t, `
[groups.alpha.claude]
config_dir = "`+filepath.Join(realParent, "shared")+`"
skills = ["store/alpha"]
[groups.beta.claude]
config_dir = "`+filepath.Join(aliasParent, "shared")+`"
skills = ["store/beta"]
`)

	_, _, err := ResolveGroupClaudeHomeSkills("alpha")
	if err == nil || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("expected aliased-home conflict, got %v", err)
	}
}

func TestResolveGroupClaudeHomeSkillsRejectsMissingCaseAlias(t *testing.T) {
	home := t.TempDir()
	withIsolatedHomeAndConfig(t, `
[groups.alpha.claude]
config_dir = "`+filepath.Join(home, "MissingHome")+`"
skills = ["store/alpha"]
[groups.beta.claude]
config_dir = "`+filepath.Join(home, "missinghome")+`"
skills = ["store/beta"]
`)

	_, _, err := ResolveGroupClaudeHomeSkills("alpha")
	if err == nil || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("expected missing case-alias conflict, got %v", err)
	}
}

func TestResolveGroupClaudeHomeSkillsRejectsParentTraversal(t *testing.T) {
	home := t.TempDir()
	withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "`+filepath.Join(home, "link")+`/../shared"
[groups.alpha.claude]
skills = ["store/alpha"]
`)

	_, _, err := ResolveGroupClaudeHomeSkills("alpha")
	if err == nil || !strings.Contains(err.Error(), "parent traversal") {
		t.Fatalf("expected parent-traversal error, got %v", err)
	}
}

func TestResolveInstanceClaudeHomeSkillsRejectsConductorAdditionInSharedHome(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "~/.claude-shared"
[groups.work.claude]
skills = ["store/alpha"]
[conductors.main.claude]
skills = ["store/beta"]
`)
	inst := NewInstanceWithGroupAndTool("conductor-main", t.TempDir(), "work", "claude")

	_, _, err := ResolveInstanceClaudeHomeSkills(inst)
	if err == nil || !strings.Contains(err.Error(), "conductor") {
		t.Fatalf("expected conductor isolation error, got %v", err)
	}
}

func TestResolveInstanceClaudeHomeSkillsAllowsIsolatedConductorAddition(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "~/.claude-shared"
[groups.work.claude]
skills = ["store/alpha"]
[conductors.main.claude]
config_dir = "~/.claude-conductor"
skills = ["store/beta"]
`)
	inst := NewInstanceWithGroupAndTool("conductor-main", t.TempDir(), "work", "claude")

	gotHome, skills, err := ResolveInstanceClaudeHomeSkills(inst)
	if err != nil {
		t.Fatal(err)
	}
	if gotHome != filepath.Join(home, ".claude-conductor") {
		t.Fatalf("home=%q", gotHome)
	}
	if len(skills) != 2 || skills[0] != "store/alpha" || skills[1] != "store/beta" {
		t.Fatalf("skills=%v", skills)
	}
}

func TestResolveInstanceClaudeHomeSkillsRejectsConductorAdditionInExplicitGroupHome(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups.work.claude]
config_dir = "~/.claude-work"
skills = ["store/alpha"]
[conductors.main.claude]
config_dir = "~/.claude-work"
skills = ["store/beta"]
`)
	inst := NewInstanceWithGroupAndTool("conductor-main", t.TempDir(), "work", "claude")

	_, _, err := ResolveInstanceClaudeHomeSkills(inst)
	if err == nil || !strings.Contains(err.Error(), "shares group") {
		t.Fatalf("expected explicit shared-home rejection, got %v", err)
	}
}

func TestResolveInstanceClaudeHomeSkillsRejectsConductorAdditionInAliasedGroupHome(t *testing.T) {
	home := t.TempDir()
	realParent := filepath.Join(home, "real")
	if err := os.MkdirAll(realParent, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(home, "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Fatal(err)
	}
	withIsolatedHomeAndConfig(t, `
[groups.work.claude]
config_dir = "`+filepath.Join(realParent, "shared")+`"
skills = ["store/alpha"]
[conductors.main.claude]
config_dir = "`+filepath.Join(aliasParent, "shared")+`"
skills = ["store/beta"]
`)
	inst := NewInstanceWithGroupAndTool("conductor-main", t.TempDir(), "work", "claude")

	_, _, err := ResolveInstanceClaudeHomeSkills(inst)
	if err == nil || !strings.Contains(err.Error(), "shares group") {
		t.Fatalf("expected aliased shared-home rejection, got %v", err)
	}
}

func TestResolveInstanceClaudeHomeSkillsUsesAccountHomeForUniformSkills(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "~/.claude-shared"
[profiles.work.claude]
config_dir = "~/.claude-account"
[groups.alpha.claude]
skills = ["store/alpha"]
[groups.beta.claude]
skills = ["store/alpha"]
`)
	inst := NewInstanceWithGroupAndTool("session", t.TempDir(), "alpha", "claude")
	inst.Account = "work"

	gotHome, skills, err := ResolveInstanceClaudeHomeSkills(inst)
	if err != nil {
		t.Fatal(err)
	}
	if gotHome != filepath.Join(home, ".claude-account") {
		t.Fatalf("home=%q", gotHome)
	}
	if len(skills) != 1 || skills[0] != "store/alpha" {
		t.Fatalf("skills=%v", skills)
	}
}

func TestResolveGroupClaudeReportsUnsafeSharedHomeSkills(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "~/.claude-shared"
[groups.alpha.claude]
skills = ["store/alpha"]
[groups.beta.claude]
skills = ["store/beta"]
`)

	res := ResolveGroupClaude("alpha")
	if !strings.Contains(res.ConfigError, "beta") {
		t.Fatalf("config_error=%q", res.ConfigError)
	}
	if len(res.Skills) != 0 {
		t.Fatalf("unsafe skills exposed: %v", res.Skills)
	}
}

func TestPrepareCommandRejectsUnsafeClaudeHomeSkills(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "~/.claude-shared"
[groups.alpha.claude]
skills = ["store/alpha"]
[groups.beta.claude]
skills = ["store/beta"]
`)
	inst := NewInstanceWithGroupAndTool("session", t.TempDir(), "alpha", "claude")

	_, _, err := inst.prepareCommand("claude")
	if err == nil || !strings.Contains(err.Error(), "unsafe Claude") {
		t.Fatalf("expected unsafe launch rejection, got %v", err)
	}
}
