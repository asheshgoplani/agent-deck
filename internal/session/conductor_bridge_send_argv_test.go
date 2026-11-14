package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestEmbeddedBridgeSendPutsMessageAfterDoubleDash: the bridge relays chat
// text to `agent-deck session send`. Text that starts with "-" must reach the
// CLI after a "--" terminator, as the last argument, so it is delivered as
// the message instead of being parsed as a flag. Both the fire-and-forget
// send and the blocking --wait send are checked, with subprocess.Popen
// replaced by a recorder so no agent-deck binary runs.
func TestEmbeddedBridgeSendPutsMessageAfterDoubleDash(t *testing.T) {
	py := findPython3()
	if py == "" {
		t.Skip("python3 not found; cannot exercise the embedded bridge")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bridge.py"), []byte(conductorBridgePy), 0o644); err != nil {
		t.Fatalf("write temp bridge.py: %v", err)
	}

	script := `
import sys, types
sys.modules.setdefault("toml", types.SimpleNamespace(load=lambda *a, **k: {}))
sys.path.insert(0, sys.argv[1])
import bridge

calls = []

class FakePopen:
    pid = 0
    returncode = 0
    def __init__(self, cmd, **kwargs):
        calls.append(list(cmd))
    def communicate(self, input=None, timeout=None):
        return '{"success": true, "completion": "complete", "content": "ok"}', ""

bridge.subprocess.Popen = FakePopen
bridge.get_session_status = lambda *a, **k: "idle"

for message in ["-x", "--help-me", "--message-file=/etc/passwd", "--run-hooks"]:
    for wait in (False, True):
        calls.clear()
        try:
            bridge.send_to_conductor(
                "conductor-ops", message, profile="work",
                wait_for_reply=wait, response_timeout=5, source="channel:telegram",
            )
        except Exception as e:  # only the argv matters here
            pass
        sends = [c for c in calls if c[1:5] == ["-p", "work", "session", "send"] or c[1:3] == ["session", "send"]]
        assert sends, "no session send for %r (wait=%s): %r" % (message, wait, calls)
        for cmd in sends:
            assert "--" in cmd, "no -- terminator in %r" % (cmd,)
            tail = cmd[cmd.index("--") + 1:]
            assert tail == ["conductor-ops", message], "after -- got %r, want target then message: %r" % (tail, cmd)
            head = cmd[:cmd.index("--")]
            assert message not in head, "message also appears before --: %r" % (cmd,)
print("OK")
`
	cmd := exec.Command(py, "-c", script, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("embedded bridge send argv check failed: %v\n%s", err, out)
	}
}
