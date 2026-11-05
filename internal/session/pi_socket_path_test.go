package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// piSocketShortData points XDG_DATA_HOME at a short directory: the socket path
// sits under it and macOS caps a unix socket path at 103 bytes. IsolateHome
// (the package TestMain) already keeps HOME off the real one (#2790).
func piSocketShortData(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_DATA_HOME", dir)
	return dir
}

// installPiExtension installs the current extension into a private pi dir: the
// launch exports the socket only for a pi that has the extension serving it.
func installPiExtension(t *testing.T) string {
	t.Helper()
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	dir := PiExtensionsDir()
	if _, err := InstallPiHooks(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeFakePi installs a `pi` script that runs body, replacing any earlier one.
func writeFakePi(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "pi")
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	return path
}

// piSocketOff is what a local pi launch with the socket off carries: an
// explicitly empty assignment, so the pane cannot inherit a value.
const piSocketOff = "AGENTDECK_PI_SOCKET= "

// exportsPiSocket reports whether a launch command exports a socket path: an
// AGENTDECK_PI_SOCKET assignment with a value, even when the command also
// carries the empty form.
func exportsPiSocket(cmd string) bool {
	const key = "AGENTDECK_PI_SOCKET="
	for rest := cmd; ; {
		i := strings.Index(rest, key)
		if i < 0 {
			return false
		}
		rest = rest[i+len(key):]
		if rest != "" && rest[0] != ' ' {
			return true
		}
	}
}

// A command with a socket path counts as exporting even when it also carries
// the empty form; only the empty form alone does not.
func TestExportsPiSocket_PathBeatsTheEmptyForm(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		want bool
	}{
		{"pi", false},
		{piSocketOff + "pi", false},
		{"AGENTDECK_PI_SOCKET=/run/x.sock pi", true},
		{"AGENTDECK_PI_SOCKET='/run/a b.sock' pi", true},
		{piSocketOff + "AGENTDECK_PI_SOCKET=/run/x.sock pi", true},
		{"AGENTDECK_PI_SOCKET=/run/x.sock " + piSocketOff + "pi", true},
		{"AGENTDECK_PI_SOCKET=", false},
	} {
		if got := exportsPiSocket(tc.cmd); got != tc.want {
			t.Errorf("exportsPiSocket(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

func expectedPiSocketPath(t *testing.T, profile, instance string) string {
	t.Helper()
	runtimeDir, err := agentpaths.ProfileRuntimeDir(profile)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(instance))
	return filepath.Join(runtimeDir, "pi", hex.EncodeToString(sum[:])[:12]+".sock")
}

// TestPiSocketPath_ReadsPiVersionAtEachLaunchAndNeverCaches pins R4: the version
// comes from running the resolved pi binary (`pi --version`) with the child's
// PATH at each launch, never from a package.json and never from a memo.
func TestPiSocketPath_ReadsPiVersionAtEachLaunchAndNeverCaches(t *testing.T) {
	piSocketShortData(t)
	bin := t.TempDir()
	env := []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + t.TempDir()}
	const profile, instance = "work", "inst-1234"
	want := expectedPiSocketPath(t, profile, instance)
	if len(want) > 103 {
		t.Fatalf("test setup: %q is %d bytes", want, len(want))
	}

	cases := []struct {
		name, script string
		version      string
		path         string // "" when off
		reason       string
	}{
		{"below the floor", `echo 1.1.0`, "1.1.0", "", "pi_too_old"},
		{"at the floor", `echo 1.2.0`, "1.2.0", want, ""},
		{"above the floor", `echo 1.3.0`, "1.3.0", want, ""},
		{"two-digit minor is numeric, not lexical", `echo 1.10.0`, "1.10.0", want, ""},
		{"a later major", `echo 2.0.0`, "2.0.0", want, ""},
		{"an earlier major", `echo 0.9.9`, "0.9.9", "", "pi_too_old"},
		{"first x.y.z token of a longer line", `echo "pi-coding-agent v1.2.3 (build 77)"`, "1.2.3", want, ""},
		{"garbage", `echo "not a version"`, "", "", "pi_version_unknown"},
		{"non-zero exit", `echo 1.3.0; exit 1`, "", "", "pi_version_unknown"},
		{"no output", `exit 0`, "", "", "pi_version_unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeFakePi(t, bin, tc.script)
			got := PiSocketPath(profile, instance, env)
			if got.Path != tc.path {
				t.Errorf("Path = %q, want %q", got.Path, tc.path)
			}
			if got.Reason != tc.reason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.reason)
			}
			if got.Version != tc.version {
				t.Errorf("Version = %q, want %q", got.Version, tc.version)
			}
			if tc.path != "" && got.Binary != filepath.Join(bin, "pi") {
				t.Errorf("Binary = %q, want %q", got.Binary, filepath.Join(bin, "pi"))
			}
			if tc.path == "" && got.Detail == "" {
				t.Error("an off socket carries no detail for `pi-hooks status` to print")
			}
		})
	}

	t.Run("a slow pi is unknown within the 2 s bound", func(t *testing.T) {
		writeFakePi(t, bin, `sleep 6; echo 1.3.0`)
		start := time.Now()
		got := PiSocketPath(profile, instance, env)
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("the probe took %v, want it bounded near 2s", elapsed)
		}
		if got.Path != "" || got.Reason != "pi_version_unknown" {
			t.Errorf("got (%q, %q), want off with pi_version_unknown", got.Path, got.Reason)
		}
	})

	t.Run("no pi on the launch PATH", func(t *testing.T) {
		empty := t.TempDir()
		got := PiSocketPath(profile, instance, []string{"PATH=" + empty + ":/usr/bin:/bin"})
		if got.Path != "" || got.Reason != "pi_version_unknown" {
			t.Errorf("got (%q, %q), want off with pi_version_unknown", got.Path, got.Reason)
		}
	})

	t.Run("each launch re-reads: the script changes between two launches", func(t *testing.T) {
		writeFakePi(t, bin, `echo 1.1.0`)
		first := PiSocketPath(profile, instance, env)
		writeFakePi(t, bin, `echo 1.3.0`)
		second := PiSocketPath(profile, instance, env)
		third := PiSocketPath(profile, instance, env)
		writeFakePi(t, bin, `echo 1.1.0`)
		fourth := PiSocketPath(profile, instance, env)
		if first.Path != "" || second.Path != want || third.Path != want || fourth.Path != "" {
			t.Errorf("paths across launches = [%q %q %q %q], want [off on on off]: a memo would repeat the first answer",
				first.Path, second.Path, third.Path, fourth.Path)
		}
	})

	t.Run("a package.json beside pi is never read", func(t *testing.T) {
		writeFakePi(t, bin, `echo 1.3.0`)
		for _, dir := range []string{bin, filepath.Dir(bin)} {
			_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"pi","version":"1.1.0"}`), 0o644)
		}
		if got := PiSocketPath(profile, instance, env); got.Path != want {
			t.Errorf("Path = %q, want %q: the probe must not trust a package.json over what pi prints", got.Path, want)
		}
		writeFakePi(t, bin, `echo 1.1.0`)
		for _, dir := range []string{bin, filepath.Dir(bin)} {
			_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"pi","version":"1.3.0"}`), 0o644)
		}
		if got := PiSocketPath(profile, instance, env); got.Path != "" || got.Reason != "pi_too_old" {
			t.Errorf("got (%q, %q), want off with pi_too_old although package.json says 1.3.0", got.Path, got.Reason)
		}
	})

	t.Run("the child's PATH wins over this process's", func(t *testing.T) {
		other := t.TempDir()
		writeFakePi(t, other, `echo 1.1.0`)
		writeFakePi(t, bin, `echo 1.3.0`)
		t.Setenv("PATH", other+":"+os.Getenv("PATH"))
		if got := PiSocketPath(profile, instance, env); got.Path != want {
			t.Errorf("Path = %q, want %q: the launch env's PATH resolves pi, not the daemon's", got.Path, want)
		}
	})
}

func TestPiSocketPath_PathOver103BytesIsOffWithItsReason(t *testing.T) {
	long := filepath.Join(piSocketShortData(t), strings.Repeat("d", 60))
	t.Setenv("XDG_DATA_HOME", long)
	bin := t.TempDir()
	writeFakePi(t, bin, `echo 1.3.0`)
	got := PiSocketPath("work", "inst-1234", []string{"PATH=" + bin + ":/usr/bin:/bin"})
	wantLen := len(expectedPiSocketPath(t, "work", "inst-1234"))
	if wantLen <= 103 {
		t.Fatalf("test setup: path is %d bytes", wantLen)
	}
	if got.Path != "" || got.Reason != "path_too_long" {
		t.Errorf("got (%q, %q), want off with path_too_long", got.Path, got.Reason)
	}
	if want := fmt.Sprintf("path %d bytes, limit 103", wantLen); got.Detail != want {
		t.Errorf("Detail = %q, want %q", got.Detail, want)
	}
}

func TestPiSocketPath_LeafIsTheHashedInstanceAndIsStablePerInstance(t *testing.T) {
	piSocketShortData(t)
	bin := t.TempDir()
	writeFakePi(t, bin, `echo 1.3.0`)
	env := []string{"PATH=" + bin + ":/usr/bin:/bin"}
	a := PiSocketPath("work", "inst-a", env)
	again := PiSocketPath("work", "inst-a", env)
	b := PiSocketPath("work", "inst-b", env)
	if a.Path == "" || a.Path != again.Path || a.Path == b.Path {
		t.Errorf("paths = %q %q %q, want a stable path per instance and a different one per instance", a.Path, again.Path, b.Path)
	}
	if want := expectedPiSocketPath(t, "work", "inst-a"); a.Path != want {
		t.Errorf("Path = %q, want %q (ProfileRuntimeDir/pi/<sha256(instance)[:12]>.sock)", a.Path, want)
	}
}

// piSocketOffRecords returns the log records PiSocketPath wrote for an off socket.
func piSocketOffRecords(t *testing.T, buf interface{ String() string }) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("unreadable log line %q: %v", line, err)
		}
		if msg, _ := rec["msg"].(string); strings.HasPrefix(msg, "pi_wake_socket off") {
			out = append(out, rec)
		}
	}
	return out
}

// TestPiSocketPath_LogsNothing: PiSocketPath is pure, so `pi-hooks status`,
// which calls it, never emits a launch warning.
func TestPiSocketPath_LogsNothing(t *testing.T) {
	piSocketShortData(t)
	bin := t.TempDir()
	env := []string{"PATH=" + bin + ":/usr/bin:/bin"}
	buf := captureSessionLogJSON(t)
	for _, script := range []string{`echo 1.1.0`, `exit 3`, `echo 1.3.0`} {
		writeFakePi(t, bin, script)
		PiSocketPath("work", "inst-log", env)
	}
	if got := strings.TrimSpace(buf.String()); got != "" {
		t.Errorf("PiSocketPath logged:\n%s", got)
	}
}

// TestPiSocketEnvForLaunch_LogsOnceWhenOffNeverWhenOn pins the launch-side
// caller (design A2): it returns the AGENTDECK_PI_SOCKET assignment for the
// launch env, or "" when the socket is off, and logs exactly one
// `pi_wake_socket off` record, with the version seen and the reason, only in
// the off case.
func TestPiSocketEnvForLaunch_LogsOnceWhenOffNeverWhenOn(t *testing.T) {
	piSocketShortData(t)
	installPiExtension(t)
	bin := t.TempDir()
	env := []string{"PATH=" + bin + ":/usr/bin:/bin"}
	for _, tc := range []struct{ name, script, version, reason string }{
		{"below the floor", `echo 1.1.0`, "1.1.0", "pi_too_old"},
		{"probe failed", `exit 3`, "", "pi_version_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureSessionLogJSON(t)
			writeFakePi(t, bin, tc.script)
			envVar, info := piSocketEnvForLaunch("work", "inst-log", env)
			if envVar != "" {
				t.Errorf("envVar = %q, want none while the socket is off", envVar)
			}
			if info.Reason != tc.reason {
				t.Errorf("info.Reason = %q, want %q", info.Reason, tc.reason)
			}
			recs := piSocketOffRecords(t, buf)
			if len(recs) != 1 {
				t.Fatalf("%d pi_wake_socket off records, want exactly 1:\n%s", len(recs), buf.String())
			}
			if got := recs[0]["reason"]; got != tc.reason {
				t.Errorf("logged reason = %v, want %q", got, tc.reason)
			}
			if got, _ := recs[0]["version"].(string); got != tc.version {
				t.Errorf("logged version = %q, want %q", got, tc.version)
			}
		})
	}

	t.Run("on logs nothing and returns the assignment", func(t *testing.T) {
		buf := captureSessionLogJSON(t)
		writeFakePi(t, bin, `echo 1.3.0`)
		envVar, info := piSocketEnvForLaunch("work", "inst-log", env)
		want := "AGENTDECK_PI_SOCKET=" + expectedPiSocketPath(t, "work", "inst-log")
		if envVar != want || info.Path == "" {
			t.Errorf("got (%q, %+v), want (%q, an on socket)", envVar, info, want)
		}
		if recs := piSocketOffRecords(t, buf); len(recs) != 0 {
			t.Errorf("an on socket logged %d pi_wake_socket off records", len(recs))
		}
	})
}

// TestBuildPiCommand_ExportsTheWakeSocketOnlyWhenOn pins the real launch path:
// buildPiCommand exports a socket path in AGENTDECK_PI_SOCKET when pi is at or
// above 1.2.0, and sets it empty (typed wake only, one off record) otherwise.
func TestBuildPiCommand_ExportsTheWakeSocketOnlyWhenOn(t *testing.T) {
	piSocketShortData(t)
	installPiExtension(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	inst := &Instance{ID: "inst-launch", Tool: "pi", IdentityInjectionDisabled: true}

	buf := captureSessionLogJSON(t)
	writeFakePi(t, bin, `echo 1.3.0`)
	on := inst.buildPiCommand("pi")
	want := "AGENTDECK_PI_SOCKET=" + expectedPiSocketPath(t, sessionProfileEnvValue(), inst.ID)
	// Exactly one assignment: in sh a later AGENTDECK_PI_SOCKET= would win.
	if !strings.Contains(on, want) || strings.Count(on, "AGENTDECK_PI_SOCKET=") != 1 {
		t.Errorf("buildPiCommand with pi 1.3.0 = %q, want exactly one AGENTDECK_PI_SOCKET assignment, containing %q", on, want)
	}
	if recs := piSocketOffRecords(t, buf); len(recs) != 0 {
		t.Errorf("an on launch logged %d off records", len(recs))
	}

	buf = captureSessionLogJSON(t)
	writeFakePi(t, bin, `echo 1.1.0`)
	off := inst.buildPiCommand("pi")
	if !strings.Contains(off, piSocketOff) || exportsPiSocket(off) {
		t.Errorf("buildPiCommand with pi 1.1.0 = %q, want an empty AGENTDECK_PI_SOCKET", off)
	}
	if recs := piSocketOffRecords(t, buf); len(recs) != 1 {
		t.Errorf("an off launch logged %d off records, want exactly 1:\n%s", len(recs), buf.String())
	}
}

func TestPiSocketPath_NoRuntimeDirIsOffWithItsReason(t *testing.T) {
	piSocketShortData(t)
	bin := t.TempDir()
	writeFakePi(t, bin, `echo 1.3.0`)
	// A profile that is not one path segment has no runtime directory.
	got := PiSocketPath("../x", "inst-1234", []string{"PATH=" + bin + ":/usr/bin:/bin"})
	if got.Path != "" || got.Reason != "no_runtime_dir" || got.Detail == "" {
		t.Errorf("got %+v, want off with reason no_runtime_dir and a detail", got)
	}
}

// TestBuildPiCommand_ProbesPiOnThePanesPath: the launch string runs in a tmux
// pane whose PATH is the spawn-path prelude's directories ahead of this
// process's PATH, then any leading PATH= word of the command, exactly as
// openCodeLaunchBinary resolves opencode. A pi that only the daemon's PATH sees
// must not decide whether the socket is exported.
func TestBuildPiCommand_ProbesPiOnThePanesPath(t *testing.T) {
	piSocketShortData(t)
	installPiExtension(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	spawn := filepath.Join(home, ".local", "bin") // a spawn-path dir
	if err := os.MkdirAll(spawn, 0o755); err != nil {
		t.Fatal(err)
	}
	proc := t.TempDir()
	t.Setenv("PATH", proc+":/usr/bin:/bin")
	inst := &Instance{ID: "inst-pane", Tool: "pi", IdentityInjectionDisabled: true}
	exportOf := func() string {
		return "AGENTDECK_PI_SOCKET=" + expectedPiSocketPath(t, sessionProfileEnvValue(), inst.ID)
	}

	t.Run("an older pi in a spawn-path dir wins over a newer one on this process's PATH", func(t *testing.T) {
		writeFakePi(t, spawn, `echo 1.1.0`)
		writeFakePi(t, proc, `echo 1.3.0`)
		buf := captureSessionLogJSON(t)
		got := inst.buildPiCommand("pi")
		if exportsPiSocket(got) {
			t.Errorf("buildPiCommand exported the socket for a pane whose pi is 1.1.0: %q", got)
		}
		recs := piSocketOffRecords(t, buf)
		if len(recs) != 1 || recs[0]["reason"] != "pi_too_old" || recs[0]["version"] != "1.1.0" {
			t.Errorf("off records = %v, want one pi_too_old for 1.1.0", recs)
		}
	})

	t.Run("a newer pi in a spawn-path dir wins over an older one on this process's PATH", func(t *testing.T) {
		writeFakePi(t, spawn, `echo 1.3.0`)
		writeFakePi(t, proc, `echo 1.1.0`)
		if got := inst.buildPiCommand("pi"); !strings.Contains(got, exportOf()) {
			t.Errorf("buildPiCommand = %q, want it to contain %q", got, exportOf())
		}
	})

	// The probe resolves the instance's own command word, not a literal "pi":
	// a custom pi (a nightly build, a wrapper) is what actually launches.
	writeNamed := func(dir, name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("an absolute command word is probed, not the pi on PATH", func(t *testing.T) {
		_ = os.Remove(filepath.Join(spawn, "pi"))
		writeFakePi(t, proc, `echo 1.3.0`)
		nightly := writeNamed(t.TempDir(), "pi-nightly", `echo 1.1.0`)
		for _, command := range []string{nightly, nightly + " --model small"} {
			buf := captureSessionLogJSON(t)
			got := inst.buildPiCommand(command)
			if exportsPiSocket(got) {
				t.Errorf("buildPiCommand(%q) exported the socket for a pi-nightly that prints 1.1.0: %q", command, got)
			}
			recs := piSocketOffRecords(t, buf)
			if len(recs) != 1 || recs[0]["reason"] != "pi_too_old" || recs[0]["version"] != "1.1.0" {
				t.Errorf("buildPiCommand(%q) off records = %v, want one pi_too_old for 1.1.0", command, recs)
			}
		}
	})

	t.Run("an absolute command word at 1.3.0 exports although pi on PATH is older", func(t *testing.T) {
		writeFakePi(t, proc, `echo 1.1.0`)
		nightly := writeNamed(t.TempDir(), "pi-nightly", `echo 1.3.0`)
		if got := inst.buildPiCommand(nightly); !strings.Contains(got, exportOf()) {
			t.Errorf("buildPiCommand(%q) = %q, want it to contain %q", nightly, got, exportOf())
		}
	})

	t.Run("a bare command word resolves on the pane PATH", func(t *testing.T) {
		writeFakePi(t, spawn, `echo 1.1.0`)
		writeFakePi(t, proc, `echo 1.3.0`)
		got := inst.buildPiCommand("pi")
		if exportsPiSocket(got) {
			t.Errorf("a bare pi resolved on the daemon's PATH, not the pane's: %q", got)
		}
		_ = os.Remove(filepath.Join(spawn, "pi"))
	})

	t.Run("a leading PATH= word in the command is honoured", func(t *testing.T) {
		_ = os.Remove(filepath.Join(spawn, "pi"))
		old := t.TempDir()
		writeFakePi(t, old, `echo 1.1.0`)
		writeFakePi(t, proc, `echo 1.3.0`)
		buf := captureSessionLogJSON(t)
		got := inst.buildPiCommand("PATH=" + old + ":$PATH pi")
		if exportsPiSocket(got) {
			t.Errorf("buildPiCommand exported the socket for a command whose PATH= puts pi 1.1.0 first: %q", got)
		}
		if recs := piSocketOffRecords(t, buf); len(recs) != 1 || recs[0]["reason"] != "pi_too_old" {
			t.Errorf("off records = %v, want one pi_too_old", recs)
		}
	})
}

// TestPiSocketEnvForLaunch_NamesAMissingOrOutdatedExtension: the extension is
// what serves the socket, so a launch without the current one exports nothing
// and says why, the same first cause `pi-hooks status` prints.
func TestPiSocketEnvForLaunch_NamesAMissingOrOutdatedExtension(t *testing.T) {
	piSocketShortData(t)
	bin := t.TempDir()
	writeFakePi(t, bin, `echo 1.3.0`)
	env := []string{"PATH=" + bin + ":/usr/bin:/bin"}

	cases := []struct {
		name   string
		setup  func(dir string)
		script string
		reason string
	}{
		{"not installed", func(string) {}, `echo 1.3.0`, "extension_not_installed"},
		{"a file agent-deck did not write", func(dir string) {
			_ = os.MkdirAll(dir, 0o755)
			_ = os.WriteFile(PiHookExtensionPath(dir), []byte("// somebody else's extension\n"), 0o644)
		}, `echo 1.3.0`, "extension_not_installed"},
		{"an older extension", func(dir string) {
			_ = os.MkdirAll(dir, 0o755)
			_ = os.WriteFile(PiHookExtensionPath(dir), []byte("// AGENTDECK PI HOOK EXTENSION v3\n"), 0o644)
		}, `echo 1.3.0`, "extension_outdated"},
		{"the extension comes before the pi version", func(string) {}, `echo 1.1.0`, "extension_not_installed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
			tc.setup(PiExtensionsDir())
			writeFakePi(t, bin, tc.script)
			buf := captureSessionLogJSON(t)
			envVar, info := piSocketEnvForLaunch("work", "inst-ext", env)
			if envVar != "" || info.Reason != tc.reason {
				t.Errorf("got (%q, reason %q), want (\"\", %q)", envVar, info.Reason, tc.reason)
			}
			recs := piSocketOffRecords(t, buf)
			if len(recs) != 1 || recs[0]["reason"] != tc.reason {
				t.Errorf("off records = %v, want exactly one with reason %q", recs, tc.reason)
			}
		})
	}

	t.Run("the buildPiCommand launch exports nothing without the extension", func(t *testing.T) {
		t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
		t.Setenv("PATH", bin+":/usr/bin:/bin")
		writeFakePi(t, bin, `echo 1.3.0`)
		inst := &Instance{ID: "inst-ext", Tool: "pi", IdentityInjectionDisabled: true}
		if got := inst.buildPiCommand("pi"); exportsPiSocket(got) {
			t.Errorf("buildPiCommand exported the socket with no extension installed: %q", got)
		}
	})
}

// The extension is looked for where the launched pi loads it from: a leading
// PI_CODING_AGENT_DIR= in the command wins over this process's, both ways.
func TestBuildPiCommand_ExtensionCheckHonoursTheCommandsPiCodingAgentDir(t *testing.T) {
	piSocketShortData(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	writeFakePi(t, bin, `echo 1.3.0`)
	inst := &Instance{ID: "inst-agentdir", Tool: "pi", IdentityInjectionDisabled: true}
	want := "AGENTDECK_PI_SOCKET=" + expectedPiSocketPath(t, sessionProfileEnvValue(), inst.ID)

	t.Run("installed in this process's dir, the command points pi at another", func(t *testing.T) {
		installPiExtension(t)
		if got := inst.buildPiCommand("pi"); !strings.Contains(got, want) {
			t.Fatalf("control: a plain pi with the extension installed must export the socket: %q", got)
		}
		buf := captureSessionLogJSON(t)
		got := inst.buildPiCommand("PI_CODING_AGENT_DIR=/alt pi")
		if exportsPiSocket(got) || inst.PiSocketExported {
			t.Errorf("exported the socket for a pi that loads its extensions from /alt (flag %v): %q", inst.PiSocketExported, got)
		}
		if recs := piSocketOffRecords(t, buf); len(recs) != 1 || recs[0]["reason"] != "extension_not_installed" {
			t.Errorf("off records = %v, want one extension_not_installed", recs)
		}
	})

	t.Run("installed only in the command's dir", func(t *testing.T) {
		t.Setenv("PI_CODING_AGENT_DIR", t.TempDir()) // nothing installed here
		alt := t.TempDir()
		if _, err := InstallPiHooks(filepath.Join(alt, "extensions")); err != nil {
			t.Fatal(err)
		}
		if got := inst.buildPiCommand("pi"); exportsPiSocket(got) {
			t.Fatalf("control: a plain pi without the extension must not export the socket: %q", got)
		}
		command := "PI_CODING_AGENT_DIR=" + alt + " pi"
		if got := inst.buildPiCommand(command); !strings.Contains(got, want) || !inst.PiSocketExported {
			t.Errorf("buildPiCommand(%q) = %q (flag %v), want it to contain %q", command, got, inst.PiSocketExported, want)
		}
	})
}

// `pi-hooks status` resolves pi the way a launch does: the spawn-path prelude's
// directories come ahead of this process's PATH.
func TestPiStatusEnv_ResolvesPiOnTheLaunchPath(t *testing.T) {
	piSocketShortData(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	spawn := filepath.Join(home, ".local", "bin") // a spawn-path dir
	if err := os.MkdirAll(spawn, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()+":/usr/bin:/bin")
	writeFakePi(t, spawn, `echo 1.3.0`)

	if got := PiSocketPath("work", "status", []string{"PATH=" + os.Getenv("PATH")}); got.Version != "" {
		t.Fatalf("control: this process's PATH alone has no pi, got %+v", got)
	}
	got := PiSocketPath("work", "status", PiStatusEnv())
	if got.Binary != filepath.Join(spawn, "pi") || got.Version != "1.3.0" {
		t.Errorf("status resolved %q at %q, want %q at 1.3.0", got.Binary, got.Version, filepath.Join(spawn, "pi"))
	}
}

// procGone: dead, or a zombie nobody reaped (a container without init keeps orphans as zombies).
func procGone(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if _, perr := os.Stat("/proc/self/stat"); perr == nil {
			return os.IsNotExist(err)
		}
		return syscall.Kill(pid, 0) != nil
	}
	rest := string(data[strings.LastIndexByte(string(data), ')')+2:])
	return rest[0] == 'Z' || rest[0] == 'X'
}

// A pi (or a wrapper around it) that wedges is killed with everything it
// started: the probe's timeout must not leave a process behind per launch.
func TestPiSocketPath_ProbeTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	piSocketShortData(t)
	bin := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	writeFakePi(t, bin, "sleep 30 &\necho $! > \"$PIDFILE\"\nsleep 30")
	env := []string{"PATH=" + bin + ":/usr/bin:/bin", "PIDFILE=" + pidFile}

	start := time.Now()
	got := PiSocketPath("work", "inst-1234", env)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("the probe took %v, want it bounded near 2s", elapsed)
	}
	if got.Path != "" || got.Reason != "pi_version_unknown" {
		t.Errorf("got (%q, %q), want off with pi_version_unknown", got.Path, got.Reason)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the fake pi never recorded its child: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	for i := 0; i < 100; i++ {
		if procGone(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("the pi's child (pid %d) is still running after the probe returned: kill the whole process group", pid)
}

// pi --version prints a few bytes; a pi that floods stdout must not be buffered
// whole.
func TestPiSocketPath_ProbeOutputIsCapped(t *testing.T) {
	piSocketShortData(t)
	bin := t.TempDir()
	// 20 MB of noise, then a valid version the capped read never reaches.
	writeFakePi(t, bin, "head -c 20000000 /dev/zero | tr '\\0' x\necho\necho 1.3.0")
	env := []string{"PATH=" + bin + ":/usr/bin:/bin"}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	got := PiSocketPath("work", "inst-1234", env)
	runtime.ReadMemStats(&after)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("the probe took %v on a noisy pi", elapsed)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Errorf("the probe allocated %d MB reading 20 MB of output, want a small cap", allocated>>20)
	}
	if got.Path != "" || got.Reason != "pi_version_unknown" {
		t.Errorf("got (%q, %q), want off with pi_version_unknown: the version came after the cap", got.Path, got.Reason)
	}
}

// Semver: a prerelease is below its own release, so 1.2.0-beta.1 is below the
// 1.2.0 floor and 1.2.1-rc.1 is above it.
func TestPiSocketPath_PrereleaseIsBelowItsRelease(t *testing.T) {
	piSocketShortData(t)
	bin := t.TempDir()
	env := []string{"PATH=" + bin + ":/usr/bin:/bin"}
	for _, tc := range []struct {
		version string
		on      bool
	}{
		{"1.2.0-beta.1", false},
		{"1.2.0-rc.2", false},
		{"1.2.0", true},
		{"1.2.1-rc.1", true},
		{"1.3.0-beta.2", true},
		{"1.1.9", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			writeFakePi(t, bin, "echo "+tc.version)
			got := PiSocketPath("work", "inst-1234", env)
			if tc.on && got.Path == "" {
				t.Errorf("pi %s is off (%q), want on", tc.version, got.Reason)
			}
			if !tc.on && (got.Path != "" || got.Reason != "pi_too_old") {
				t.Errorf("pi %s gave (%q, %q), want off with pi_too_old", tc.version, got.Path, got.Reason)
			}
		})
	}
}

// The probe sees the environment the launch will: every leading VAR=value of
// the command, not only PATH=.
func TestBuildPiCommand_ProbeCarriesEveryLeadingAssignment(t *testing.T) {
	piSocketShortData(t)
	installPiExtension(t)
	proc := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", proc+":/usr/bin:/bin")
	t.Setenv("FOO", "")
	// Only a pi launched with FOO=1 is new enough.
	fooGated := `if [ "$FOO" = 1 ]; then echo 1.3.0; else echo 1.1.0; fi`
	writeFakePi(t, proc, fooGated)
	inst := &Instance{ID: "inst-assign", Tool: "pi", IdentityInjectionDisabled: true}
	want := "AGENTDECK_PI_SOCKET=" + expectedPiSocketPath(t, sessionProfileEnvValue(), inst.ID)

	if got := inst.buildPiCommand("pi"); exportsPiSocket(got) {
		t.Fatalf("control: with FOO unset the pi is 1.1.0 and the socket must be off: %q", got)
	}
	if got := inst.buildPiCommand("FOO=1 pi"); !strings.Contains(got, want) {
		t.Errorf("buildPiCommand(\"FOO=1 pi\") = %q, want it to contain %q: the probe dropped FOO=1", got, want)
	}

	newer := t.TempDir()
	writeFakePi(t, newer, fooGated)
	writeFakePi(t, proc, `echo 1.1.0`)
	command := "FOO=1 PATH=" + newer + ":$PATH pi"
	if got := inst.buildPiCommand(command); !strings.Contains(got, want) {
		t.Errorf("buildPiCommand(%q) = %q, want it to contain %q: FOO=1 and PATH= must both reach the probe", command, got, want)
	}
}

// The socket is for local launches only: the extension of an SSH or sandboxed
// pi runs in another filesystem and process namespace, which the host daemon
// cannot reach. Such a launch exports nothing, not even the empty form, and
// records PiSocketExported = false, even with a pi and extension that would
// turn the socket on locally.
func TestBuildPiCommand_SSHAndSandboxedLaunchesNeverExportTheSocket(t *testing.T) {
	piSocketShortData(t)
	installPiExtension(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	writeFakePi(t, bin, `echo 1.3.0`)

	local := &Instance{ID: "inst-local", Tool: "pi", IdentityInjectionDisabled: true}
	if cmd := local.buildPiCommand("pi"); !exportsPiSocket(cmd) || !local.PiSocketExported {
		t.Fatalf("test setup: a local launch must export the socket (flag %v): %q", local.PiSocketExported, cmd)
	}

	for name, inst := range map[string]*Instance{
		"ssh":       {ID: "inst-ssh", Tool: "pi", IdentityInjectionDisabled: true, SSHHost: "remote", PiSocketExported: true},
		"sandboxed": {ID: "inst-sandboxed", Tool: "pi", IdentityInjectionDisabled: true, Sandbox: NewSandboxConfig(""), PiSocketExported: true},
	} {
		cmd := inst.buildPiCommand("pi")
		if strings.Contains(cmd, "AGENTDECK_PI_SOCKET") {
			t.Errorf("%s launch exports AGENTDECK_PI_SOCKET: %q", name, cmd)
		}
		if inst.PiSocketExported {
			t.Errorf("%s launch recorded PiSocketExported = true", name)
		}
	}
}
