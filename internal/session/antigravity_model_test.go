package session

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAntigravityApplyLaunchModel(t *testing.T) {
	for _, tt := range []struct {
		name, command, model, want string
	}{
		{"bare agy", "agy", "gemini-4-hydrogen", "agy --model gemini-4-hydrogen"},
		{"tool name resolves to agy", "antigravity", "gemini-4-hydrogen", "agy --model gemini-4-hydrogen"},
		{"orchestrator id", "agy", "gemini-pro-orchestrator-neon", "agy --model gemini-pro-orchestrator-neon"},
		{"space is quoted", "agy", "my model", "agy --model 'my model'"},
		{"other flags kept", "agy --mode plan", "gemini-4-hydrogen", "agy --mode plan --model gemini-4-hydrogen"},
		{"existing model replaced", "agy --model old --effort high", "new", "agy --effort high --model new"},
		{"existing model= replaced", "agy --model=old", "new", "agy --model new"},
		{"custom agy path", "/opt/bin/agy", "m", "/opt/bin/agy --model m"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inst := &Instance{Tool: "antigravity", Command: tt.command}
			if err := inst.ApplyLaunchModel(tt.model); err != nil {
				t.Fatalf("ApplyLaunchModel(%q) error = %v", tt.model, err)
			}
			if inst.Command != tt.want {
				t.Fatalf("Command = %q, want %q", inst.Command, tt.want)
			}
			if got := inst.LaunchModelID(); got != tt.model {
				t.Fatalf("LaunchModelID() = %q, want %q", got, tt.model)
			}
		})
	}
}

func TestAntigravityApplyLaunchModelRefusesUnsafe(t *testing.T) {
	for _, tt := range []struct {
		name, command, model string
	}{
		{"pipeline command", "agy | tee log", "m"},
		{"not agy", "my-launcher", "m"},
		{"flag-like model", "agy", "--dangerously-skip-permissions"},
		{"control character", "agy", "m\nrm -rf x"},
		{"display name with parentheses", "agy", "Orchestrator (Neon)"},
		{"command substitution", "agy", "$(id)"},
		{"empty", "agy", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inst := &Instance{Tool: "antigravity", Command: tt.command}
			if tt.model == "" {
				if err := inst.applyAntigravityModel(tt.model); err == nil {
					t.Fatal("empty model accepted")
				}
				return
			}
			if err := inst.ApplyLaunchModel(tt.model); err == nil {
				t.Fatalf("ApplyLaunchModel accepted %q on %q: command now %q", tt.model, tt.command, inst.Command)
			}
			if inst.Command != tt.command {
				t.Fatalf("refused model still changed Command to %q", inst.Command)
			}
		})
	}
}

func TestAntigravityClearLaunchModel(t *testing.T) {
	inst := &Instance{Tool: "antigravity", Command: "agy --mode plan --model gemini-pro-orchestrator-neon"}
	if err := inst.ClearLaunchModel(); err != nil {
		t.Fatalf("ClearLaunchModel() error = %v", err)
	}
	if inst.Command != "agy --mode plan" || inst.LaunchModelID() != "" {
		t.Fatalf("after clear: Command = %q, LaunchModelID = %q", inst.Command, inst.LaunchModelID())
	}
}

// The model must survive restart (resume) and fork, which rebuild from the
// command.
func TestAntigravityLaunchModelSurvivesResumeAndFork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := resetUserConfigCache(t, &UserConfig{})
	defer restore()

	repoDir := filepath.Join(home, "repo")
	inst := NewInstanceWithGroupAndTool("agy-parent", repoDir, "projects/agy", "antigravity")
	inst.Command = "agy"
	if err := inst.ApplyLaunchModel("gemini-pro-orchestrator-neon"); err != nil {
		t.Fatal(err)
	}
	inst.AntigravitySessionID = "conv-a"
	inst.AntigravityDetectedAt = time.Now()
	if got, want := inst.buildAntigravityResumeCommand(), "agy --model gemini-pro-orchestrator-neon --conversation conv-a"; got != want {
		t.Fatalf("resume command = %q, want %q", got, want)
	}
	forked, cmd, err := inst.CreateForkedInstanceForTool("agy-child", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedInstanceForTool: %v", err)
	}
	if forked.Command != "agy --model gemini-pro-orchestrator-neon" || forked.LaunchModelID() != "gemini-pro-orchestrator-neon" {
		t.Fatalf("forked.Command = %q, LaunchModelID = %q", forked.Command, forked.LaunchModelID())
	}
	if !strings.Contains(cmd, "--model gemini-pro-orchestrator-neon") || !strings.Contains(cmd, "--conversation conv-a") {
		t.Fatalf("fork command = %q, want model and parent conversation", cmd)
	}
}

func TestAntigravitySupportsLaunchModel(t *testing.T) {
	for _, tool := range []string{"antigravity"} {
		if !SupportsLaunchModel(tool) {
			t.Fatalf("SupportsLaunchModel(%q) = false", tool)
		}
	}
}

func TestAntigravitySetFieldModel(t *testing.T) {
	inst := &Instance{Tool: "antigravity", Command: "agy --mode plan"}
	if _, _, err := SetField(inst, FieldModel, "gemini-4-hydrogen", nil); err != nil {
		t.Fatalf("set model: %v", err)
	}
	if inst.LaunchModelID() != "gemini-4-hydrogen" || inst.Command != "agy --mode plan --model gemini-4-hydrogen" {
		t.Fatalf("after set: Command = %q, LaunchModelID = %q", inst.Command, inst.LaunchModelID())
	}
	old, _, err := SetField(inst, FieldModel, "", nil)
	if err != nil {
		t.Fatalf("clear model: %v", err)
	}
	if old != "gemini-4-hydrogen" || inst.LaunchModelID() != "" || inst.Command != "agy --mode plan" {
		t.Fatalf("after clear: old = %q, Command = %q, LaunchModelID = %q", old, inst.Command, inst.LaunchModelID())
	}
}

func TestAntigravitySetFieldModelRefusesShellCharacters(t *testing.T) {
	for _, model := range []string{"$(id)", "m\nx", "--dangerously-skip-permissions", "`id`"} {
		inst := &Instance{Tool: "antigravity", Command: "agy"}
		if _, _, err := SetField(inst, FieldModel, model, nil); err == nil {
			t.Fatalf("SetField accepted %q: Command = %q", model, inst.Command)
		}
		if inst.Command != "agy" || inst.LaunchModelID() != "" {
			t.Fatalf("refused %q still changed Command to %q", model, inst.Command)
		}
	}
}

// A model set through session set must survive resume and fork.
func TestAntigravitySetFieldModelSurvivesResumeAndFork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := resetUserConfigCache(t, &UserConfig{})
	defer restore()

	inst := NewInstanceWithGroupAndTool("agy-parent", filepath.Join(home, "repo"), "projects/agy", "antigravity")
	inst.Command = "agy"
	if _, _, err := SetField(inst, FieldModel, "gemini-4-hydrogen", nil); err != nil {
		t.Fatal(err)
	}
	inst.AntigravitySessionID = "conv-a"
	inst.AntigravityDetectedAt = time.Now()
	if got, want := inst.buildAntigravityResumeCommand(), "agy --model gemini-4-hydrogen --conversation conv-a"; got != want {
		t.Fatalf("resume command = %q, want %q", got, want)
	}
	forked, _, err := inst.CreateForkedInstanceForTool("agy-child", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if forked.LaunchModelID() != "gemini-4-hydrogen" {
		t.Fatalf("forked LaunchModelID = %q", forked.LaunchModelID())
	}
}
