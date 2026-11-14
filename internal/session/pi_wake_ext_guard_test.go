package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// piExtGuardDriver loads the real extension with a minimal pi, starts its
// session and reports what it registered, listened on and warned. It runs in
// a process of its own because the extension reads its env at import.
const piExtGuardDriver = `
import net from "node:net";
const listened = [];
const rawListen = net.Server.prototype.listen;
net.Server.prototype.listen = function (...a) { if (typeof a[0] === "string") listened.push(a[0]); return rawListen.apply(this, a); };
const warns = [];
console.error = (...a) => { warns.push(a.join(" ")); };
const { default: ext } = await import(process.env.PITEST_EXT);
const handlers = {};
ext({ on(ev, h) { (handlers[ev] ??= []).push(h); }, sendMessage() {} });
const ctx = { hasUI: false, isIdle: () => true, sessionManager: { getSessionId: () => "s" } };
for (const h of handlers.session_start ?? []) await h({ type: "session_start" }, ctx);
const out = { beforeAgentStart: (handlers.before_agent_start ?? []).length, listened, warns };
for (const h of handlers.session_shutdown ?? []) await h({ type: "session_shutdown" }, ctx);
console.log(JSON.stringify(out));
process.exit(0);
`

type piExtGuardResult struct {
	BeforeAgentStart int      `json:"beforeAgentStart"`
	Listened         []string `json:"listened"`
	Warns            []string `json:"warns"`
}

// runPiExtGuard runs the guard driver with AGENTDECK_PI_SOCKET set to sock
// (empty: set empty, as a socket-off launch does). agent-deck is not on PATH,
// so the extension's hook emits fail at once.
func runPiExtGuard(t *testing.T, dir, sock string) piExtGuardResult {
	t.Helper()
	nodePath := nodeForExtensionTests(t)
	ext := filepath.Join(dir, "ext.ts")
	driver := filepath.Join(dir, "guard.mjs")
	if err := os.WriteFile(ext, []byte(PiHookExtensionSource()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(driver, []byte(piExtGuardDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(nodePath, "--experimental-strip-types", "--no-warnings", driver)
	cmd.Env = append(os.Environ(), "AGENTDECK_INSTANCE_ID=pi-guard-test", "PITEST_EXT="+ext,
		"AGENTDECK_PI_SOCKET="+sock, "PATH=/nonexistent")
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.Output(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("the guard driver did not finish")
	}
	if err != nil {
		t.Fatalf("guard driver: %v\n%s", err, out)
	}
	var res piExtGuardResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("guard driver output %q: %v", out, err)
	}
	return res
}

func piExtGuardDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pg") // short: a unix socket path is capped at 103 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// before_agent_start is registered with or without the socket. A socket-off
// pi spawns one hook-handler per run start, as it does for its other events,
// because the ledger drains a listed parent relaunched without the socket
// through its run starts (#3024): the records it was owed arrive there.
func TestPiWakeExtension_BeforeAgentStartIsRegisteredWithOrWithoutTheSocket(t *testing.T) {
	dir := piExtGuardDir(t)
	on := runPiExtGuard(t, dir, filepath.Join(dir, "r", "pi", "0123456789ab.sock"))
	if on.BeforeAgentStart != 1 || len(on.Listened) == 0 {
		t.Fatalf("with the socket: %d before_agent_start handlers, listened %v; want 1 and a listener", on.BeforeAgentStart, on.Listened)
	}
	if off := runPiExtGuard(t, dir, ""); off.BeforeAgentStart != 1 || len(off.Listened) != 0 {
		t.Errorf("without the socket: %d before_agent_start handlers, listened %v; want 1 and no listener", off.BeforeAgentStart, off.Listened)
	}
}

// The extension serves only the launch's own layout, <runtime dir>/pi/<name>.sock,
// and never creates or tightens another directory: a mistaken value such as a
// path in the home directory leaves that directory's mode alone. Nor does it
// tighten a pi directory that is a symlink, since chmod would follow it to its
// target.
func TestPiWakeExtension_ServesOnlyItsOwnSocketDirectory(t *testing.T) {
	dir := piExtGuardDir(t)

	other := filepath.Join(dir, "home")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	res := runPiExtGuard(t, dir, filepath.Join(other, "w.sock"))
	if len(res.Listened) != 0 || !strings.Contains(strings.Join(res.Warns, "\n"), "could not serve") {
		t.Errorf("a socket outside a pi directory: listened %v, warns %v; want no listener and a warning", res.Listened, res.Warns)
	}
	if m := modeOf(t, other); m != 0o755 {
		t.Errorf("the extension changed %s to %#o, want it left 0755", other, m)
	}

	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "r"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "r", "pi")); err != nil {
		t.Fatal(err)
	}
	res = runPiExtGuard(t, dir, filepath.Join(dir, "r", "pi", "0123456789ab.sock"))
	if len(res.Listened) != 0 || !strings.Contains(strings.Join(res.Warns, "\n"), "could not serve") {
		t.Errorf("a symlinked pi directory: listened %v, warns %v; want no listener and a warning", res.Listened, res.Warns)
	}
	if m := modeOf(t, target); m != 0o755 {
		t.Errorf("the extension followed the symlink and changed %s to %#o, want it left 0755", target, m)
	}
}
