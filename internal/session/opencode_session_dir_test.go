package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setFakeOpenCodeSessionGet answers `api session.get` with getPayload and any
// other call with listPage, recording each argv under dir/argv.N.
func setFakeOpenCodeSessionGet(t *testing.T, listPage, getPayload string) string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\n"+
		"n=0; read -r n < %[1]q/count 2>/dev/null; n=$((n+1)); echo $n > %[1]q/count\n"+
		"printf '%%s\\n' \"$@\" > %[1]q/argv.$n\n"+
		"case \"$*\" in *session.get*) printf '%%s\\n' %[3]q;; *) printf '%%s\\n' %[2]q;; esac\n",
		dir, listPage, getPayload)
	setFakeOpenCodePath(t, script, false)
	configureFakeOpenCode(t)
	return dir
}

func movedSessionPayload(id, directory string) string {
	return fmt.Sprintf(`{"data":{"id":%q,"parentID":null,"location":{"directory":%q},"time":{"created":1000,"updated":2000}}}`, id, directory)
}

// After session_move the bound session no longer lists under ProjectPath; the
// binding must survive and learn where the session went.
func TestQueryOpenCodeSession_V2BoundSessionMovedToOtherDirectory(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	movedTo := t.TempDir()
	emptyPage := `{"data":[],"cursor":{"previous":null,"next":null}}`
	dir := setFakeOpenCodeSessionGet(t, emptyPage, movedSessionPayload("ses_BOUND", movedTo))

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeSessionID: "ses_BOUND"}
	if got := inst.queryOpenCodeSession(); got != "ses_BOUND" {
		t.Fatalf("session ID = %q, want ses_BOUND", got)
	}
	if got := inst.openCodeSessionDir(); got != movedTo {
		t.Fatalf("session dir = %q, want %q", got, movedTo)
	}
	if got := fakeOpenCodeCallCount(t, dir); got != 1 {
		t.Fatalf("opencode call count = %d, want 1 (one lookup by id, no paging)", got)
	}
	argv, err := os.ReadFile(filepath.Join(dir, "argv.1"))
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	if want := "api\nsession.get\n--param\nsessionID=ses_BOUND\n"; string(argv) != want {
		t.Fatalf("opencode argv =\n%s\nwant\n%s", argv, want)
	}
}

func TestQueryOpenCodeSession_V2BoundLookupFailureKeepsBinding(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	sibling := fmt.Sprintf(`{"data":[`+
		`{"id":"ses_SIBLING","parentID":null,"location":{"directory":%q},"time":{"created":5000,"updated":6000}}`+
		`],"cursor":{"previous":null,"next":null}}`, projectPath)
	// 2.0.25 answers an unknown id with exit 1, this JSON on stdout and the HTTP status on stderr.
	notFound := "#!/bin/sh\ncase \"$*\" in *session.get*) echo '{\"_tag\":\"SessionNotFoundError\"}'; " +
		"echo 'HTTP 404 Not Found' >&2; exit 1;; esac\nprintf '%s\\n' '" + sibling + "'\n"
	for name, fake := range map[string]func(t *testing.T){
		"session not found": func(t *testing.T) { setFakeOpenCodePath(t, notFound, false); configureFakeOpenCode(t) },
		"reply for another": func(t *testing.T) {
			setFakeOpenCodeSessionGet(t, sibling, movedSessionPayload("ses_OTHER", projectPath))
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake(t)
			inst := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeSessionID: "ses_GONE"}
			if got := inst.queryOpenCodeSession(); got != "" {
				t.Fatalf("session ID = %q, want empty (no rebind, no sibling adopted)", got)
			}
		})
	}
}

// A moved session relaunches where it lives: the lookup by id records the
// directory, and the resume command starts the TUI there.
func TestBuildOpenCodeCommand_V2ResumesInSessionDirectory(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	projectPath := t.TempDir()
	movedTo := filepath.Join(t.TempDir(), "wt feature")
	if err := os.Mkdir(movedTo, 0o755); err != nil {
		t.Fatal(err)
	}
	setFakeOpenCodeSessionGet(t, `{"data":[]}`, movedSessionPayload("ses_BOUND", movedTo))

	inst := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeSessionID: "ses_BOUND"}
	inst.queryOpenCodeSession()
	if got, want := inst.buildOpenCodeCommand("opencode"), "opencode -s ses_BOUND '"+movedTo+"'"; !strings.HasSuffix(got, want) {
		t.Fatalf("resume command = %q, want suffix %q", got, want)
	}
}

func TestBuildOpenCodeCommand_V2ResumeDirectoryFallsBackToProjectPath(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	isolateOpenCodeConfig(t, "opencode")
	projectPath := t.TempDir()
	inst := &Instance{Tool: "opencode", ProjectPath: projectPath, OpenCodeSessionID: "ses_BOUND",
		OpenCodeSessionDir: projectPath, openCodeSessionDirID: "ses_BOUND"}
	if got := inst.buildOpenCodeCommand("opencode"); !strings.HasSuffix(got, "opencode -s ses_BOUND") {
		t.Fatalf("session in ProjectPath: command = %q, want no directory argument", got)
	}

	inst.OpenCodeSessionDir = filepath.Join(t.TempDir(), "removed-worktree")
	if got := inst.buildOpenCodeCommand("opencode"); !strings.HasSuffix(got, "opencode -s ses_BOUND") {
		t.Fatalf("missing directory: command = %q, want fallback to ProjectPath", got)
	}

	inst.OpenCodeSessionDir = t.TempDir()
	inst.OpenCodeSessionID = "ses_OTHER"
	if got := inst.buildOpenCodeCommand("opencode"); !strings.HasSuffix(got, "opencode -s ses_OTHER") {
		t.Fatalf("rebound id: command = %q, want the old directory dropped", got)
	}
}

func TestOpenCodeSessionDir_ToolDataKeyedToSessionID(t *testing.T) {
	td := WriteOpenCodeSessionDirToToolData([]byte(`{"color":"#ff00aa"}`), "ses_A", "/work/wt-a")
	if got := ReadOpenCodeSessionDirFromToolData(td, "ses_A"); got != "/work/wt-a" {
		t.Fatalf("read for ses_A = %q, want /work/wt-a", got)
	}
	if got := ReadOpenCodeSessionDirFromToolData(td, "ses_B"); got != "" {
		t.Fatalf("read for ses_B = %q, want empty (recorded for another id)", got)
	}
	if !strings.Contains(string(td), `"color":"#ff00aa"`) {
		t.Fatalf("write dropped unrelated keys: %s", td)
	}
	if got := ReadOpenCodeSessionDirFromToolData(WriteOpenCodeSessionDirToToolData(td, "ses_A", ""), "ses_A"); got != "" {
		t.Fatalf("empty dir should clear the key, read %q", got)
	}
}

func TestOpenCodeSessionDir_SQLiteRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("opencode-moved", "/tmp")
	inst.Tool = "opencode"
	inst.OpenCodeSessionID = "ses_BOUND"
	inst.OpenCodeSessionDir = "/work/wt-feature"
	inst.openCodeSessionDirID = "ses_BOUND"

	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(loaded))
	}
	if got := loaded[0].openCodeSessionDir(); got != "/work/wt-feature" {
		t.Fatalf("session dir after reload = %q, want /work/wt-feature", got)
	}
}
