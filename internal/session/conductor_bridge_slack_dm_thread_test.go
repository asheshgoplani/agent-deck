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
