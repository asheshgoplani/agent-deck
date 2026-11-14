package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// slackBridgeHarness loads the embedded bridge with stand-ins for slack-bolt
// and toml, builds the Slack app from a [conductor.slack] table through the
// real load_config and create_slack_app, and lets each test drive the
// registered handlers. An accepted message reaches routing, which replies
// ACCEPTED because the harness reports no conductors.
const slackBridgeHarness = `
import asyncio, inspect, logging, sys, types
from pathlib import Path

def _mod(name, **attrs):
    m = types.ModuleType(name)
    m.__dict__.update(attrs)
    sys.modules[name] = m
    return m

class AuthorizeResult:
    def __init__(self, **kw):
        self.__dict__.update(kw)

class FakeWebClient:
    def __init__(self, *a, **k):
        pass
    async def auth_test(self):
        return {"team_id": "T_HOME", "user_id": "UBOT", "bot_id": "BBOT"}

class FakeApp:
    def __init__(self, token=None, authorize=None):
        self.authorize = authorize
        self.events = {}
        self.commands = {}
        self.client = None
    def event(self, name):
        def deco(fn):
            self.events[name] = fn
            return fn
        return deco
    def command(self, name):
        def deco(fn):
            self.commands[name] = fn
            return fn
        return deco

_mod("toml", load=lambda *a, **k: {})
_mod("slack_bolt")
_mod("slack_bolt.async_app", AsyncApp=FakeApp)
_mod("slack_bolt.adapter")
_mod("slack_bolt.adapter.socket_mode")
_mod("slack_bolt.adapter.socket_mode.async_handler", AsyncSocketModeHandler=object)
_mod("slack_bolt.authorization", AuthorizeResult=AuthorizeResult)
_mod("slack_sdk")
_mod("slack_sdk.web")
_mod("slack_sdk.web.async_client", AsyncWebClient=FakeWebClient)

sys.path.insert(0, sys.argv[1])
import bridge
assert bridge.HAS_SLACK

class _Cap(logging.Handler):
    def __init__(self):
        super().__init__()
        self.lines = []
    def emit(self, record):
        self.lines.append((record.levelno, record.getMessage()))

CAP = _Cap()
bridge.log.addHandler(CAP)
ACCEPTED = "[No conductors configured. Run: agent-deck conductor setup <name>]"

def refusal(uid):
    return (
        "Not allowed: this agent-deck bridge only takes commands from its allow-list. "
        "Your Slack member ID is " + uid + ". The owner can add it under [conductor.slack] "
        "in ~/.agent-deck/config.toml: allowed_user_ids = [\"" + uid + "\"]"
    )

STARTUP_WARNING = (
    "Slack bridge: no allowed users are set, so every Slack message and command is refused. "
    "Add your Slack member ID under [conductor.slack] in ~/.agent-deck/config.toml: "
    "allowed_user_ids = [\"U01234567\"] (or set allow_all_users = true to let every "
    "workspace member in), then restart the bridge."
)

def make_app(extra, listen_mode="mentions"):
    table = {"bot_token": "xoxb-x", "app_token": "xapp-x", "channel_id": "C_HOME",
             "listen_mode": listen_mode}
    table.update(extra)
    bridge.toml = types.SimpleNamespace(load=lambda *a, **k: {"conductor": {"slack": table}})
    bridge.CONFIG_PATH = Path(sys.argv[1]) / "bridge.py"
    bridge.discover_conductors = lambda: [{"name": "a", "profile": "default"}]
    cfg = bridge.load_config()
    bridge.discover_conductors = lambda: []
    app, _ = bridge.create_slack_app(cfg)
    return app

SAME = object()

async def _dispatch(fn, ev, user, team, body_team, extra):
    # Like slack-bolt: arguments are passed by name, and body (the envelope,
    # whose team_id is the receiving workspace) only when the handler asks.
    said = []
    async def say(**kw):
        said.append(kw.get("text"))
    if user is not None:
        ev["user"] = user
    if team is not None:
        ev["team"] = team
    ev.update(extra)
    kw = {"event": ev, "say": say}
    if "body" in inspect.signature(fn).parameters:
        body = {"type": "event_callback", "event": ev}
        body_team = team if body_team is SAME else body_team
        if body_team is not None:
            body["team_id"] = body_team
        kw["body"] = body
    await fn(**kw)
    return said

async def mention(app, user, channel="C_HOME", team="T_HOME", ts="1.1", body_team=SAME, **extra):
    ev = {"text": "<@UBOT> secret words", "channel": channel, "ts": ts}
    return await _dispatch(app.events["app_mention"], ev, user, team, body_team, extra)

async def message(app, user, channel="C_HOME", team="T_HOME", ts="2.1", body_team=SAME, **extra):
    ev = {"text": "secret words", "channel": channel, "ts": ts}
    return await _dispatch(app.events["message"], ev, user, team, body_team, extra)

async def slash(app, user, channel="C_HOME", team="T_HOME", cmd="/ad-help"):
    out = []
    async def ack():
        pass
    async def respond(*a, **kw):
        out.append((a[0] if a else kw.get("text"), kw.get("response_type")))
    await app.commands[cmd](ack, respond, {"user_id": user, "channel_id": channel,
                                           "team_id": team, "text": ""})
    return out

def warnings():
    return [m for lvl, m in CAP.lines if lvl >= logging.WARNING]

async def scenario():
%s

asyncio.run(scenario())
print("OK")
`

func runSlackBridgeScenario(t *testing.T, body string) {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found; cannot exercise the embedded Slack bridge")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bridge.py"), []byte(conductorBridgePy), 0o644); err != nil {
		t.Fatalf("write temp bridge.py: %v", err)
	}
	lines := strings.Split(strings.Trim(body, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	script := strings.Replace(slackBridgeHarness, "%s", strings.Join(lines, "\n"), 1)
	cmd := exec.Command(py, "-c", script, dir)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("Slack bridge scenario failed: %v\n%s", err, out)
	}
}

// An explicit empty allow-list refuses everyone: one reply per user in the
// thread with their member ID, nothing routed, and a log line without the
// message text. Slash commands get the same text as an ephemeral reply.
func TestSlackBridge_EmptyAllowListRefusesEveryone(t *testing.T) {
	runSlackBridgeScenario(t, `
app = make_app({"allowed_user_ids": []})
await app.authorize()
assert await mention(app, "U_BAD") == [refusal("U_BAD")], "first refusal must reply once"
assert await mention(app, "U_BAD", ts="1.2") == [], "second refusal must stay silent"
assert await mention(app, "U_TWO") == [refusal("U_TWO")]
assert await slash(app, "U_BAD") == [(refusal("U_BAD"), "ephemeral")]
w = warnings()
assert any("U_BAD" in m and "C_HOME" in m and "T_HOME" in m for m in w), w
assert not any("secret words" in m for _, m in CAP.lines), CAP.lines
`)
}

// A config without allowed_user_ids now fails closed (shipped default of the
// migration switch) and logs the exact startup warning once.
func TestSlackBridge_AbsentAllowListFailsClosedWithWarning(t *testing.T) {
	runSlackBridgeScenario(t, `
assert bridge.SLACK_LEGACY_OPEN_WHEN_ALLOWLIST_ABSENT is False
app = make_app({})
await app.authorize()
assert [m for m in warnings() if m == STARTUP_WARNING] == [STARTUP_WARNING], warnings()
assert await mention(app, "U_ANY") == [refusal("U_ANY")]
assert await slash(app, "U_ANY") == [(refusal("U_ANY"), "ephemeral")]
assert [m for m in warnings() if m == STARTUP_WARNING] == [STARTUP_WARNING]
`)
}

// With the migration switch on, only a table that has neither
// allowed_user_ids nor allow_all_users stays open; an explicit empty list or
// an explicit allow_all_users = false still fails closed.
func TestSlackBridge_LegacySwitchKeepsOnlyAbsentKeysOpen(t *testing.T) {
	runSlackBridgeScenario(t, `
bridge.SLACK_LEGACY_OPEN_WHEN_ALLOWLIST_ABSENT = True
app = make_app({})
await app.authorize()
assert await mention(app, "U_ANY") == [ACCEPTED]
assert any("allowed_user_ids" in m and "every member" in m for m in warnings()), warnings()
app = make_app({"allowed_user_ids": []})
await app.authorize()
assert await mention(app, "U_ANY") == [refusal("U_ANY")]
app = make_app({"allow_all_users": False})
await app.authorize()
assert await mention(app, "U_ANY") == [refusal("U_ANY")]
`)
}

// allow_all_users = true opens the bot to every workspace member, but only in
// the configured channel: mentions and slash commands elsewhere are refused.
func TestSlackBridge_AllowAllUsersOnlyInConfiguredChannel(t *testing.T) {
	runSlackBridgeScenario(t, `
app = make_app({"allow_all_users": True})
await app.authorize()
assert STARTUP_WARNING not in warnings()
assert await mention(app, "U_ANY") == [ACCEPTED]
assert await mention(app, "U_ANY", channel="C_OTHER") == []
assert (await slash(app, "U_ANY"))[0][0].startswith("Conductor Commands:")
assert await slash(app, "U_ANY", channel="C_OTHER") == []
assert any("C_OTHER" in m for m in warnings()), warnings()
app = make_app({"allow_all_users": True, "allowed_user_ids": ["U_OK"]})
await app.authorize()
assert await mention(app, "U_OK", channel="C_OTHER") == [ACCEPTED]
assert await mention(app, "U_ANY", channel="C_OTHER") == []
`)
}

// An allow-listed user keeps working from any channel; others are refused.
func TestSlackBridge_AllowListAcceptsListedUserAnywhere(t *testing.T) {
	runSlackBridgeScenario(t, `
app = make_app({"allowed_user_ids": ["U_OK"]})
await app.authorize()
assert STARTUP_WARNING not in warnings()
assert await mention(app, "U_OK", channel="C_OTHER") == [ACCEPTED]
assert (await slash(app, "U_OK", channel="C_OTHER"))[0][0].startswith("Conductor Commands:")
assert await mention(app, "U_BAD") == [refusal("U_BAD")]
app = make_app({"allowed_user_ids": ["U_OK"]}, listen_mode="all")
await app.authorize()
assert await message(app, "U_OK") == [ACCEPTED]
assert await message(app, "U_BAD") == [refusal("U_BAD")]
`)
}

// Events from another Slack team are dropped silently with a log line, even
// for an allow-listed user id; an unknown team on either side does not block.
func TestSlackBridge_RejectsForeignTeam(t *testing.T) {
	runSlackBridgeScenario(t, `
app = make_app({"allowed_user_ids": ["U_OK"]})
await app.authorize()
assert await mention(app, "U_OK", team="T_OTHER") == []
assert await slash(app, "U_OK", team="T_OTHER") == []
assert any("T_OTHER" in m for m in warnings()), warnings()
assert await mention(app, "U_OK", team=None) == [ACCEPTED]
app = make_app({"allowed_user_ids": ["U_OK"]})
assert await mention(app, "U_OK", team="T_OTHER") == [ACCEPTED]
`)
}

// In open mode an event without a user id (posted by an integration rather
// than a person) is refused on every entry point, logged, and gets no reply.
func TestSlackBridge_OpenModeRefusesEventsWithoutUser(t *testing.T) {
	runSlackBridgeScenario(t, `
app = make_app({"allow_all_users": True})
await app.authorize()
assert await mention(app, "U_ANY") == [ACCEPTED]
assert await mention(app, "") == []
assert await mention(app, None) == []
assert await slash(app, "") == []
app = make_app({"allow_all_users": True}, listen_mode="all")
await app.authorize()
assert await message(app, "U_ANY") == [ACCEPTED]
assert await message(app, "") == []
assert await message(app, None) == []
assert any(m.startswith("Refused Slack event") and "user= " in m for m in warnings()), warnings()
`)
}

// Mentions posted by bots or integrations (bot_id or a message subtype) are
// ignored like channel messages, even when they carry an allow-listed user.
func TestSlackBridge_MentionIgnoresBotAndSubtypeEvents(t *testing.T) {
	runSlackBridgeScenario(t, `
app = make_app({"allow_all_users": True, "allowed_user_ids": ["U_OK"]})
await app.authorize()
assert await mention(app, "U_OK") == [ACCEPTED]
assert await mention(app, "U_OK", bot_id="B_INT") == []
assert await mention(app, None, bot_id="B_INT", subtype="bot_message") == []
assert await mention(app, "U_ANY", subtype="bot_message") == []
assert await mention(app, "U_OK", channel="C_OTHER", bot_id="B_INT") == []
`)
}

// The team check reads the envelope team_id as well as the event's own team:
// a foreign envelope is refused even when the event's team is missing or
// names the bot's team, and a foreign event team is still refused.
func TestSlackBridge_TeamCheckPrefersEnvelopeTeam(t *testing.T) {
	runSlackBridgeScenario(t, `
app = make_app({"allowed_user_ids": ["U_OK"]})
await app.authorize()
assert await mention(app, "U_OK", team=None, body_team="T_OTHER") == []
assert await mention(app, "U_OK", team="T_HOME", body_team="T_OTHER") == []
assert await mention(app, "U_OK", team="T_OTHER", body_team=None) == []
assert await mention(app, "U_OK", team="T_OTHER", body_team="T_HOME") == []
assert await mention(app, "U_OK", team=None, body_team=None) == [ACCEPTED]
app = make_app({"allowed_user_ids": ["U_OK"]}, listen_mode="all")
await app.authorize()
assert await message(app, "U_OK", team=None, body_team="T_OTHER") == []
assert await message(app, "U_OK", team="T_HOME", body_team="T_OTHER") == []
assert await message(app, "U_OK", team="T_HOME", body_team="T_HOME") == [ACCEPTED]
`)
}
