package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRemoteCreationCatalogMatchesParsers(t *testing.T) {
	for _, command := range []string{"add", "launch"} {
		fields := creationCommandFields(command)
		seen := map[string]bool{}
		for _, field := range fields {
			seen[field.Name] = true
		}
		for _, name := range []string{"additional-path", "startup-query", "effort", "parent", "no-parent", "yolo", "model", "account"} {
			if !seen[name] {
				t.Errorf("%s missing %s", command, name)
			}
		}
		if seen["capabilities"] {
			t.Error("discovery flag advertised as creation option")
		}
	}
}

func TestRemoteCreationFlagTypes(t *testing.T) {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	fs.Bool("enabled", false, "")
	fs.String("value", "", "")
	fields := creationFieldsFromFlags(fs)
	if len(fields) != 2 || fields[0].TakesValue || !fields[1].TakesValue {
		t.Fatalf("wrong field types: %+v", fields)
	}
}

func TestCreationAdditionalPathsValidateBeforeMutation(t *testing.T) {
	root := t.TempDir()
	paths, err := validateCreationPaths([]string{root})
	if err != nil || len(paths) != 1 {
		t.Fatalf("path resolution: %v %v", paths, err)
	}
	if _, err := validateCreationPaths([]string{root, filepath.Join(root, ".")}); err == nil {
		t.Fatal("duplicate path accepted")
	}
	if _, err := validateCreationPaths([]string{filepath.Join(root, "missing")}); err == nil {
		t.Fatal("missing directory accepted")
	}
}

func TestCreationHermesYolo(t *testing.T) {
	inst := &session.Instance{Tool: "hermes"}
	if err := applyCLIYoloOverride(inst, true); err != nil {
		t.Fatal(err)
	}
	if opts := inst.GetHermesOptions(); opts == nil || opts.YoloMode == nil || !*opts.YoloMode {
		t.Fatal("Hermes YOLO not applied")
	}
}

func TestCreationStartupQueryValidation(t *testing.T) {
	for _, tc := range []struct {
		tool, mode string
		start      bool
	}{{"codex", "", true}, {"claude", "resume", true}, {"claude", "continue", true}, {"claude", "", false}} {
		if err := validateCreationStartupQuery("hello", tc.tool, tc.mode, tc.start); err == nil {
			t.Errorf("accepted %+v", tc)
		}
	}
	if err := validateCreationStartupQuery("hello", "claude", "new", true); err != nil {
		t.Fatal(err)
	}
}

func TestCreationArgumentNormalization(t *testing.T) {
	fs := flag.NewFlagSet("launch", flag.ContinueOnError)
	idle := fs.String("idle-timeout", "", "")
	query := fs.String("startup-query", "", "")
	fs.Bool("no-wait", false, "")
	args := normalizeCreationArgs(fs, []string{"/project", "--idle-timeout", "30m", "--startup-query", "two words", "--no-wait", "--", "-repo"})
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	if *idle != "30m" || *query != "two words" || fs.NArg() != 2 || fs.Arg(1) != "-repo" {
		t.Fatalf("bad parse %v %v", args, fs.Args())
	}
}

func TestCreationOptionPreflightRejectsBeforeEffects(t *testing.T) {
	fs := flag.NewFlagSet("creation", flag.ContinueOnError)
	flags := registerClaudeOptionFlags(fs)
	if err := validateCreationOptions("shell", "", "", "", false, flags, []string{"missing"}, nil, nil, nil); err == nil {
		t.Fatal("unsupported MCP accepted")
	}
	if err := validateCreationOptions("codex", "", "", "invalid", false, flags, nil, nil, nil, nil); err == nil {
		t.Fatal("invalid effort accepted")
	}
}

func TestCreationExplicitFalseOverridesOwnerDefaults(t *testing.T) {
	for _, tool := range []string{"gemini", "codex", "hermes"} {
		inst := &session.Instance{Tool: tool}
		if err := applyCLIYoloOverride(inst, true); err != nil {
			t.Fatal(err)
		}
		if err := applyCLIYoloOverride(inst, false, true); err != nil {
			t.Fatal(err)
		}
		switch tool {
		case "gemini":
			if inst.GeminiYoloMode == nil || *inst.GeminiYoloMode {
				t.Fatal("gemini false lost")
			}
		case "codex":
			if opts := inst.GetCodexOptions(); opts == nil || opts.YoloMode == nil || *opts.YoloMode {
				t.Fatal("codex false lost")
			}
		case "hermes":
			if opts := inst.GetHermesOptions(); opts == nil || opts.YoloMode == nil || *opts.YoloMode {
				t.Fatal("hermes false lost")
			}
		}
	}
	fs := flag.NewFlagSet("creation", flag.ContinueOnError)
	flags := registerClaudeOptionFlags(fs)
	if err := fs.Parse([]string{"--skip-permissions=false", "--auto-mode=false", "--chrome=false", "--teammate-mode=false", "--continue=false"}); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{Tool: "claude"}
	if err := inst.SetClaudeOptions(&session.ClaudeOptions{SkipPermissions: true, AutoMode: true, UseChrome: true, UseTeammateMode: true, SessionMode: "continue"}); err != nil {
		t.Fatal(err)
	}
	if err := applyCLIClaudeOptionFlags(inst, flags); err != nil {
		t.Fatal(err)
	}
	opts := inst.GetClaudeOptions()
	if opts.SkipPermissions || opts.AutoMode || opts.UseChrome || opts.UseTeammateMode || opts.SessionMode == "continue" {
		t.Fatalf("false overrides lost: %+v", opts)
	}
}

func TestCreationCLIHelpAndCatalog(t *testing.T) {
	home := t.TempDir()
	for _, command := range []string{"add", "launch"} {
		stdout, stderr, code := runAgentDeck(t, home, command, "--help")
		if code != 0 || !strings.Contains(stdout+stderr, "startup-query") || !strings.Contains(stdout+stderr, "additional-path") {
			t.Fatalf("%s help code=%d out=%s err=%s", command, code, stdout, stderr)
		}
	}
	stdout, stderr, code := runAgentDeck(t, home, "add", "--capabilities", "--json")
	if code != 0 {
		t.Fatalf("catalog code=%d out=%s err=%s", code, stdout, stderr)
	}
	var catalog session.RemoteCreationCatalog
	if err := json.Unmarshal([]byte(stdout), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Version != 1 || len(catalog.Commands["launch"]) == 0 {
		t.Fatalf("incomplete catalog: %+v", catalog)
	}
	for _, args := range [][]string{
		{"launch", filepath.Join(home, "never-created"), "--create-dir", "-c", "claude", "--additional-path", home, "--worktree", "task", "--location", "sibling"},
		{"launch", filepath.Join(home, "never-created"), "--create-dir", "-c", "claude", "--mcp", "unknown"},
		{"add", filepath.Join(home, "never-created"), "--create-dir", "--attach", "--json"},
		{"launch", filepath.Join(home, "never-created"), "--create-dir", "-c", "claude", "--startup-query", "hello", "--extra-arg=--resume"},
	} {
		stdout, stderr, code = runAgentDeck(t, home, args...)
		if code == 0 {
			t.Fatalf("invalid creation succeeded: %v %s %s", args, stdout, stderr)
		}
		if _, err := os.Stat(filepath.Join(home, "never-created")); !os.IsNotExist(err) {
			t.Fatalf("invalid args created directory: %v, %v", args, err)
		}
	}
}
