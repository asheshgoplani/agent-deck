package session

import "testing"

// A conductor turn that ends with no text must not be posted as an empty
// message (Slack rejects it with no_text and the user is left with only the
// "⏳" placeholder); the bridge posts a short notice instead.
func TestSlackBridge_EmptyConductorReplyPostsNotice(t *testing.T) {
	runSlackBridgeScenario(t, `
app = make_app({"allowed_user_ids": ["U_OK"]})
await app.authorize()
conductor = {"name": "a", "profile": "default"}
async def running(name, profile):
    return True
bridge.discover_conductors = lambda: [conductor]
bridge.ensure_conductor_running = running
bridge.get_session_status = lambda *a, **k: "idle"
for reply in ("", "pong"):
    bridge.send_to_conductor = lambda *a, _r=reply, **k: (True, _r, False)
    said = await mention(app, "U_OK")
    assert said == ["⏳", reply or bridge.NO_OUTPUT_REPLY], said
`)
}
