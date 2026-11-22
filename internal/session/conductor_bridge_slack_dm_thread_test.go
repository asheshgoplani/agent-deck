package session

import "testing"

// A top-level direct message is answered top-level, not in a thread under
// the user's message; a reply inside a DM thread stays in that thread, and
// channel messages keep threading under the user's message.
func TestSlackBridge_DirectMessageRepliesAreNotThreaded(t *testing.T) {
	runSlackBridgeScenario(t, `
app = make_app({"allowed_user_ids": ["U_OK"]}, listen_mode="all")
await app.authorize()

async def reply_threads(**extra):
    threads = []
    async def say(**kw):
        threads.append(kw.get("thread_ts", "<unset>"))
    ev = {"text": "status", "channel": "C_HOME", "ts": "5.1", "user": "U_OK", "team": "T_HOME"}
    ev.update(extra)
    await app.events["message"](event=ev, say=say, body={"team_id": "T_HOME"})
    return threads

assert await reply_threads(channel_type="im") == [None]
assert await reply_threads(channel_type="im", thread_ts="5.0") == ["5.0"]
assert await reply_threads(channel_type="channel") == ["5.1"]
assert await reply_threads(channel_type="channel", thread_ts="5.0") == ["5.0"]
assert await reply_threads() == ["5.1"]
`)
}

// A top-level DM is answered top-level, so there is no thread for a follow-up
// to land in. Routing remembers the conductor per DM channel instead: after
// "beta: status", an unprefixed top-level DM goes to beta until a message
// names another conductor. An alert the bridge posts in the DM tags only its
// own thread and leaves the DM's conductor alone. Channels and group DMs
// (mpim) keep threading and per-thread routing.
func TestSlackBridge_DirectMessageFollowUpKeepsTheConductor(t *testing.T) {
	runSlackBridgeScenario(t, `
app = make_app({"allowed_user_ids": ["U_OK"], "channel_id": "D_DM", "default_conductor": "alpha"}, listen_mode="all")
await app.authorize()
cs = [{"name": n, "profile": "default"} for n in ("alpha", "beta", "gamma")]
bridge.discover_conductors = lambda: cs
async def running(name, profile):
    return True
bridge.ensure_conductor_running = running
bridge.get_session_status = lambda *a, **k: "idle"
routed = []
def send(title, msg, **k):
    routed.append(title.split("conductor-", 1)[1])
    return (True, "ok", False)
bridge.send_to_conductor = send

async def deliver(handler, text, **extra):
    threads = []
    async def say(**kw):
        threads.append(kw.get("thread_ts", "<unset>"))
    ev = {"text": text, "user": "U_OK", "team": "T_HOME"}
    ev.update(extra)
    await app.events[handler](event=ev, say=say, body={"team_id": "T_HOME"})
    return routed[-1], set(threads)

async def dm(text, ts, **extra):
    return await deliver("message", text, channel="D_DM", channel_type="im", ts=ts, **extra)

assert await dm("beta: status", "5.1") == ("beta", {None})
assert await dm("and the logs?", "5.2") == ("beta", {None})
assert await dm("gamma: status", "5.3") == ("gamma", {None})
assert await dm("and the logs?", "5.4") == ("gamma", {None})

# An alert the bridge posts for alpha in the DM.
class Client:
    async def chat_postMessage(self, **kw):
        return {"ok": True, "ts": "9.1"}
with bridge._delivering_for("alpha"):
    assert await bridge._deliver_need_alert(
        "[alpha] Conductor alert:\nNEED: x", None, None,
        types.SimpleNamespace(client=Client()), "D_DM", None, None,
    )
assert await dm("still there?", "5.5") == ("gamma", {None})
assert await dm("yes", "9.2", thread_ts="9.1") == ("alpha", {"9.1"})
assert await dm("more?", "5.6") == ("gamma", {None})

# Channels and group DMs: threaded under the message, no DM memory.
for channel_type in ("channel", "mpim"):
    assert await deliver("app_mention", "<@UBOT> status", channel="C_" + channel_type,
                         channel_type=channel_type, ts="6.1") == ("alpha", {"6.1"})
    assert await deliver("app_mention", "<@UBOT> beta: status", channel="C_" + channel_type,
                         channel_type=channel_type, ts="6.2") == ("beta", {"6.2"})
    assert await deliver("app_mention", "<@UBOT> more?", channel="C_" + channel_type,
                         channel_type=channel_type, ts="6.3", thread_ts="6.2") == ("beta", {"6.2"})
    assert await deliver("app_mention", "<@UBOT> status", channel="C_" + channel_type,
                         channel_type=channel_type, ts="6.4") == ("alpha", {"6.4"})
`)
}
