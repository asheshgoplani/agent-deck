package session

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Hermes Agent CLI support tests.
// These tests define the Tool="hermes" contract: options marshalling,
// ToArgs for yolo mode, factory from config, and identity gates
// (icon, IsClaudeCompatible, builtin-name filter).
//
// Binary: `hermes` (github.com/NousResearch/hermes-agent), interactive TUI.

func TestHermesOptions_ToolName(t *testing.T) {
	opts := &HermesOptions{}
	if got := opts.ToolName(); got != "hermes" {
		t.Errorf("HermesOptions.ToolName() = %q, want %q", got, "hermes")
	}
}

func TestHermesOptions_ToArgs(t *testing.T) {
	yoloTrue := true
	yoloFalse := false

	tests := []struct {
		name     string
		opts     HermesOptions
		expected []string
	}{
		{
			name:     "default - no args",
			opts:     HermesOptions{},
			expected: nil,
		},
		{
			name:     "yolo nil - no args",
			opts:     HermesOptions{YoloMode: nil},
			expected: nil,
		},
		{
			name:     "yolo false - no args",
			opts:     HermesOptions{YoloMode: &yoloFalse},
			expected: nil,
		},
		{
			name:     "yolo true - --yolo present",
			opts:     HermesOptions{YoloMode: &yoloTrue},
			expected: []string{"--yolo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.opts.ToArgs()
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("ToArgs() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestNewHermesOptions_Defaults(t *testing.T) {
	opts := NewHermesOptions(nil)
	if opts == nil {
		t.Fatal("NewHermesOptions(nil) returned nil")
	}
	if opts.YoloMode != nil {
		t.Errorf("default YoloMode = %v, want nil", opts.YoloMode)
	}
}

func TestNewHermesOptions_WithYoloConfig(t *testing.T) {
	cfg := &UserConfig{
		Hermes: HermesSettings{YoloMode: true},
	}
	opts := NewHermesOptions(cfg)
	if opts == nil {
		t.Fatal("NewHermesOptions returned nil")
	}
	if opts.YoloMode == nil || !*opts.YoloMode {
		t.Errorf("YoloMode = %v, want true", opts.YoloMode)
	}
}

func TestNewHermesOptions_WithoutYoloConfig(t *testing.T) {
	cfg := &UserConfig{
		Hermes: HermesSettings{YoloMode: false},
	}
	opts := NewHermesOptions(cfg)
	if opts == nil {
		t.Fatal("NewHermesOptions returned nil")
	}
	if opts.YoloMode != nil {
		t.Errorf("YoloMode = %v, want nil (not set when config is false)", opts.YoloMode)
	}
}

func TestHermesOptions_MarshalUnmarshalRoundtrip(t *testing.T) {
	yolo := true
	orig := &HermesOptions{YoloMode: &yolo}

	data, err := MarshalToolOptions(orig)
	if err != nil {
		t.Fatalf("MarshalToolOptions: %v", err)
	}

	var wrapper ToolOptionsWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		t.Fatalf("unmarshal wrapper: %v", err)
	}
	if wrapper.Tool != "hermes" {
		t.Errorf("wrapper.Tool = %q, want %q", wrapper.Tool, "hermes")
	}

	got, err := UnmarshalHermesOptions(data)
	if err != nil {
		t.Fatalf("UnmarshalHermesOptions: %v", err)
	}
	if got == nil {
		t.Fatal("UnmarshalHermesOptions returned nil")
	}
	if got.YoloMode == nil || !*got.YoloMode {
		t.Errorf("roundtrip mismatch: YoloMode = %v", got.YoloMode)
	}
}

func TestUnmarshalHermesOptions_WrongTool(t *testing.T) {
	raw := json.RawMessage(`{"tool":"codex","options":{}}`)
	got, err := UnmarshalHermesOptions(raw)
	if err != nil {
		t.Fatalf("UnmarshalHermesOptions: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for wrong tool, got %+v", got)
	}
}

func TestIsClaudeCompatible_HermesNotCompatible(t *testing.T) {
	if IsClaudeCompatible("hermes") {
		t.Error("IsClaudeCompatible(\"hermes\") must be false")
	}
}

func TestGetToolIcon_Hermes(t *testing.T) {
	icon := GetToolIcon("hermes")
	if icon == "" {
		t.Error("GetToolIcon(\"hermes\") returned empty")
	}
	if icon == GetToolIcon("shell") {
		t.Errorf("GetToolIcon(\"hermes\") = %q equals shell fallback (want a distinct icon)", icon)
	}
	if icon != "☤" {
		t.Errorf("GetToolIcon(\"hermes\") = %q, want %q", icon, "☤")
	}
}

func TestGetCustomToolNames_HermesIsBuiltin(t *testing.T) {
	oldCache := userConfigCache
	defer func() { userConfigCache = oldCache }()

	userConfigCache = &UserConfig{
		Tools: map[string]ToolDef{
			"hermes":     {Command: "hermes"},
			"my-wrapper": {Command: "claude"},
		},
	}

	names := GetCustomToolNames()
	for _, n := range names {
		if n == "hermes" {
			t.Errorf("GetCustomToolNames() returned %q as custom; hermes is built-in", n)
		}
	}
}

func TestNewInstanceWithTool_Hermes(t *testing.T) {
	inst := NewInstanceWithTool("hermes-test", "/tmp/hermes-test-proj", "hermes")
	if inst == nil {
		t.Fatal("NewInstanceWithTool returned nil")
	}
	if inst.Tool != "hermes" {
		t.Errorf("inst.Tool = %q, want %q", inst.Tool, "hermes")
	}
}

func TestBuildHermesCommand_CommandOverride(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{
		Hermes: HermesSettings{
			Command: "hermes --model gpt-5.5-pro --provider openai",
		},
	})
	defer restore()

	inst := &Instance{Tool: "hermes"}
	cmd := inst.buildHermesCommand("hermes")
	want := "hermes --model gpt-5.5-pro --provider openai"
	if !strings.HasSuffix(cmd, want) {
		t.Errorf("buildHermesCommand() = %q, want suffix %q", cmd, want)
	}
}

func TestBuildHermesCommand_CommandOverrideWithYolo(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{
		Hermes: HermesSettings{
			Command:  "hermes --model gpt-5.5-pro --provider openai",
			YoloMode: true,
		},
	})
	defer restore()

	inst := &Instance{Tool: "hermes"}
	cmd := inst.buildHermesCommand("hermes")
	want := "hermes --model gpt-5.5-pro --provider openai --yolo"
	if !strings.HasSuffix(cmd, want) {
		t.Errorf("buildHermesCommand() = %q, want suffix %q", cmd, want)
	}
}

func TestBuildHermesCommand_DefaultCommand(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{
		Hermes: HermesSettings{},
	})
	defer restore()

	inst := &Instance{Tool: "hermes"}
	cmd := inst.buildHermesCommand("hermes")
	if !strings.HasSuffix(cmd, "hermes") {
		t.Errorf("buildHermesCommand() = %q, want suffix %q", cmd, "hermes")
	}
	if strings.Contains(cmd, "--model") {
		t.Errorf("buildHermesCommand() = %q, should not contain --model", cmd)
	}
}

func TestBuildHermesCommand_Passthrough(t *testing.T) {
	cfg := &UserConfig{}
	restore := resetUserConfigCache(t, cfg)
	defer restore()

	inst := &Instance{Tool: "hermes"}
	got := inst.buildHermesCommand("hermes --special-flag")
	if !strings.HasSuffix(got, "hermes --special-flag") {
		t.Errorf("buildHermesCommand passthrough = %q, want suffix \"hermes --special-flag\"", got)
	}
}

// ---------------------------------------------------------------------------
// Gateway health check tests
// ---------------------------------------------------------------------------

func TestIsHermesGatewayReachable_EmptyURL(t *testing.T) {
	if IsHermesGatewayReachable("") {
		t.Error("IsHermesGatewayReachable(\"\") = true, want false")
	}
}

func TestIsHermesGatewayReachable_InvalidURL(t *testing.T) {
	// Port 1 almost certainly has no listener; the connection should be refused.
	if IsHermesGatewayReachable("http://127.0.0.1:1") {
		t.Error("IsHermesGatewayReachable(\"http://127.0.0.1:1\") = true, want false")
	}
}

func TestIsHermesGatewayReachable_ReachableServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if !IsHermesGatewayReachable(srv.URL) {
		t.Errorf("IsHermesGatewayReachable(%q) = false, want true (200 OK)", srv.URL)
	}
}

func TestIsHermesGatewayReachable_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if IsHermesGatewayReachable(srv.URL) {
		t.Errorf("IsHermesGatewayReachable(%q) = true, want false (500 >= 500 means unreachable)", srv.URL)
	}
}

func TestIsHermesGatewayReachable_ServerRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound) // 302
	}))
	defer srv.Close()

	if !IsHermesGatewayReachable(srv.URL) {
		t.Errorf("IsHermesGatewayReachable(%q) = false, want true (302 < 500)", srv.URL)
	}
}

// ---------------------------------------------------------------------------
// Shared workspace tests
// ---------------------------------------------------------------------------

func TestHermesSharedWorkspaceDir_NonEmpty(t *testing.T) {
	if HermesSharedWorkspaceDir() == "" {
		t.Error("HermesSharedWorkspaceDir() returned empty string, want non-empty")
	}
}

func TestHermesSharedWorkspaceDir_IsAbsolute(t *testing.T) {
	dir := HermesSharedWorkspaceDir()
	if !filepath.IsAbs(dir) {
		t.Errorf("HermesSharedWorkspaceDir() = %q, want absolute path", dir)
	}
}
