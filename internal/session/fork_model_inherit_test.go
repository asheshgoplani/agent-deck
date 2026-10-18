package session

import (
	"strings"
	"testing"
)

// Review 2736 item 2: a model switched with `session command /model` is
// stored on the instance; a fork starts on it unless given its own model.
func TestInheritForkModel(t *testing.T) {
	extraArgsTestEnv(t)
	src := NewInstanceWithTool("src", t.TempDir(), "claude")
	if got := inheritForkModel(src, nil); got != nil {
		t.Fatalf("a source without its own model leaves the fork options alone, got %+v", got)
	}
	if _, _, err := SetField(src, FieldModel, "opus[1m]", nil); err != nil {
		t.Fatal(err)
	}
	got := inheritForkModel(src, nil)
	if got == nil || got.Model != "opus[1m]" {
		t.Fatalf("fork options = %+v, want model opus[1m]", got)
	}
	if flags := src.buildClaudeExtraFlags(got); !strings.Contains(flags, "--model 'opus[1m]'") {
		t.Fatalf("fork flags %q lack --model 'opus[1m]'", flags)
	}
	explicit := &ClaudeOptions{Model: "haiku", ModelExplicit: true, WorkDir: "/tmp/wt"}
	if kept := inheritForkModel(src, explicit); kept != explicit || kept.Model != "haiku" {
		t.Fatalf("an explicit fork model must win, got %+v", kept)
	}
	noModel := &ClaudeOptions{WorkDir: "/tmp/wt"}
	out := inheritForkModel(src, noModel)
	if out.Model != "opus[1m]" || out.WorkDir != "/tmp/wt" || noModel.Model != "" {
		t.Fatalf("inherit must copy, not modify: out=%+v in=%+v", out, noModel)
	}
}

// Review 2736 round 2: with a global [claude].default_model, `session fork`
// (nil options) and `session fork -w` (options from NewClaudeOptions, Model
// filled with the default) both inherit the source's switched model; the
// filled-in default is not an explicit choice.
func TestInheritForkModel_GlobalDefaultModelIsNotExplicit(t *testing.T) {
	extraArgsTestEnv(t)
	restore := resetUserConfigCache(t, &UserConfig{Claude: ClaudeSettings{DefaultModel: "sonnet"}})
	defer restore()
	src := NewInstanceWithTool("src", t.TempDir(), "claude")
	if _, _, err := SetField(src, FieldModel, "haiku", nil); err != nil {
		t.Fatal(err)
	}
	cfg, _ := LoadUserConfig()
	worktree := NewClaudeOptions(cfg)
	worktree.WorkDir = "/tmp/wt"
	if worktree.Model != "sonnet" || worktree.ModelExplicit {
		t.Fatalf("NewClaudeOptions = %+v, want the default model, not explicit", worktree)
	}
	for name, opts := range map[string]*ClaudeOptions{"plain fork": nil, "worktree fork": worktree} {
		got := inheritForkModel(src, opts)
		if got == nil || got.Model != "haiku" {
			t.Fatalf("%s: fork model = %+v, want the source's haiku", name, got)
		}
		if flags := src.buildClaudeExtraFlags(got); !strings.Contains(flags, "--model haiku") || strings.Contains(flags, "sonnet") {
			t.Fatalf("%s: fork flags %q, want --model haiku and not the global default", name, flags)
		}
	}
}
