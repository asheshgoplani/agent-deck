package session

import (
	"errors"
	"strings"
	"testing"
)

func TestCursorCommandInstalled_OverrideProvenance(t *testing.T) {
	withStubbedProbe(t, []string{"cursor"}, func() {
		restore := resetUserConfigCache(t, &UserConfig{
			Cursor: CursorSettings{Command: "agent"},
		})
		defer restore()
		if cursorCommandInstalled() {
			t.Fatal(`command="agent" with only cursor installed should be hidden`)
		}
	})

	withStubbedProbe(t, []string{"agent"}, func() {
		restore := resetUserConfigCache(t, &UserConfig{
			Cursor: CursorSettings{Command: "cursor agent"},
		})
		defer restore()
		if cursorCommandInstalled() {
			t.Fatal(`command="cursor agent" with only agent installed should be hidden`)
		}
	})

	withStubbedProbe(t, []string{"cursor"}, func() {
		restore := resetUserConfigCache(t, &UserConfig{})
		defer restore()
		if !cursorCommandInstalled() {
			t.Fatal("stock default with cursor on PATH should be visible")
		}
	})
}

func TestDefaultCursorCommand_PrefersAgent(t *testing.T) {
	orig := lookPathFn
	t.Cleanup(func() { lookPathFn = orig })

	lookPathFn = func(file string) (string, error) {
		if file == "agent" {
			return "/usr/local/bin/agent", nil
		}
		return "", errors.New("not found")
	}
	if got := DefaultCursorCommand(); got != "agent" {
		t.Fatalf("DefaultCursorCommand() = %q, want %q", got, "agent")
	}
}

func TestDefaultCursorCommand_FallsBackToCursorAgent(t *testing.T) {
	orig := lookPathFn
	t.Cleanup(func() { lookPathFn = orig })

	lookPathFn = func(string) (string, error) {
		return "", errors.New("not found")
	}
	if got := DefaultCursorCommand(); got != "cursor agent" {
		t.Fatalf("DefaultCursorCommand() = %q, want %q", got, "cursor agent")
	}
}

func TestIsDefaultCursorInvocation(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"", true},
		{"cursor", true},
		{"CURSOR", true},
		{"cursor agent", true},
		{"Cursor Agent", true},
		{"agent", true},
		{"agent --continue", false},
		{"cursor agent --model x", false},
		{"echo hi", false},
	}
	for _, tt := range tests {
		if got := isDefaultCursorInvocation(tt.cmd); got != tt.want {
			t.Errorf("isDefaultCursorInvocation(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}

func TestGetToolCommand_CursorDefaultAndOverride(t *testing.T) {
	orig := lookPathFn
	t.Cleanup(func() { lookPathFn = orig })
	lookPathFn = func(file string) (string, error) {
		if file == "agent" {
			return "/bin/agent", nil
		}
		return "", errors.New("not found")
	}

	cfg := &UserConfig{}
	restore := resetUserConfigCache(t, cfg)
	defer restore()
	if got := GetToolCommand("cursor"); got != "agent" {
		t.Fatalf("GetToolCommand(cursor) default = %q, want %q", got, "agent")
	}

	cfg2 := &UserConfig{Cursor: CursorSettings{Command: "cursor agent --force"}}
	restore2 := resetUserConfigCache(t, cfg2)
	defer restore2()
	if got := GetToolCommand("cursor"); got != "cursor agent --force" {
		t.Fatalf("GetToolCommand(cursor) override = %q, want %q", got, "cursor agent --force")
	}
}

func TestBuildCursorCommand_ResolvesDefaultEntrypoint(t *testing.T) {
	orig := lookPathFn
	t.Cleanup(func() { lookPathFn = orig })
	lookPathFn = func(file string) (string, error) {
		if file == "agent" {
			return "/bin/agent", nil
		}
		return "", errors.New("not found")
	}
	restore := resetUserConfigCache(t, &UserConfig{})
	defer restore()

	inst := NewInstanceWithTool("c1", "/tmp/c1", "cursor")
	for _, base := range []string{"", "cursor", "cursor agent", "agent"} {
		got := inst.buildCursorCommand(base, false)
		if !strings.Contains(got, "agent") || strings.Contains(got, "cursor agent") {
			t.Fatalf("buildCursorCommand(%q) = %q, want resolved agent entrypoint", base, got)
		}
		if strings.Contains(strings.ToLower(got), "--continue") {
			t.Fatalf("buildCursorCommand(%q) unexpectedly includes --continue: %q", base, got)
		}
	}

	got := inst.buildCursorCommand("cursor agent", true)
	if !strings.Contains(got, "agent --continue") {
		t.Fatalf("restart command = %q, want agent --continue", got)
	}

	got = inst.buildCursorCommand("agent --model x", true)
	if !strings.Contains(got, "agent --model x --continue") {
		t.Fatalf("custom command = %q, want agent --model x --continue", got)
	}
}
