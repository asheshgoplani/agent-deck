"""Tests for the Mattermost platform of the conductor bridge.

The pure filters are tested directly. MattermostBridge runs against a fake
Mattermost server on localhost (aiohttp.web): real HTTP requests and a real
WebSocket, with only the agent-deck side (send_to_conductor and friends)
patched out.
"""

from __future__ import annotations

import asyncio
import json
import logging
import sys
import threading
import time
import types
from pathlib import Path
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))
try:
    import toml  # noqa: F401
except ModuleNotFoundError:
    sys.modules["toml"] = types.SimpleNamespace(load=lambda *_args, **_kwargs: {})

aiohttp = pytest.importorskip("aiohttp")
from aiohttp import web  # noqa: E402

import bridge  # noqa: E402
from bridge import (  # noqa: E402
    MM_BRIDGE_POST_PROP,
    MattermostBridge,
    mattermost_post_text,
    mattermost_reply_root,
    mattermost_retry_after,
    mattermost_url_problem,
    parse_mattermost_command,
)

BOT_ID = "b" * 26
OWNER_ID = "o" * 26
STRANGER_ID = "s" * 26
DM_ID = "d" * 26
CHANNEL_ID = "c" * 26
TOKEN = "test-token"
FLEET = [{"name": "fleet", "profile": "default"}]


def _post(message="hello", user_id=OWNER_ID, channel_id=DM_ID, **extra):
    post = {"id": extra.pop("id", "p1"), "user_id": user_id, "channel_id": channel_id,
            "message": message, "create_at": extra.pop("create_at", 1000), "type": ""}
    post.update(extra)
    return post


def _text(post, home_is_dm=True, listen_mode="all", home=DM_ID):
    return mattermost_post_text(post, BOT_ID, "agentbot", OWNER_ID, home, home_is_dm, listen_mode)


# --- filters ---------------------------------------------------------------


def test_owner_message_in_home_dm_is_relayed():
    assert _text(_post("  status please ")) == "status please"


def test_bridge_never_relays_its_own_posts():
    assert _text(_post(user_id=BOT_ID)) is None
    assert _text(_post(props={MM_BRIDGE_POST_PROP: True})) is None


def test_system_posts_and_other_channels_are_ignored():
    assert _text(_post(type="system_join_channel")) is None
    assert _text(_post(channel_id="x" * 26)) is None


def test_only_the_owner_is_obeyed():
    assert _text(_post(user_id=STRANGER_ID)) is None


def test_mentions_mode_in_a_channel_needs_a_mention_and_strips_it():
    in_channel = dict(home_is_dm=False, listen_mode="mentions", home=CHANNEL_ID)
    assert _text(_post("deploy it", channel_id=CHANNEL_ID), **in_channel) is None
    assert _text(_post("@agentbot deploy it", channel_id=CHANNEL_ID), **in_channel) == "deploy it"
    assert _text(_post("@agentbot2 deploy it", channel_id=CHANNEL_ID), **in_channel) is None


def test_mentions_mode_does_not_apply_to_a_dm():
    assert _text(_post("deploy it"), listen_mode="mentions") == "deploy it"


def test_replies_stay_in_the_thread_and_channel_posts_start_one():
    assert mattermost_reply_root(_post(root_id="r1"), home_is_dm=True) == "r1"
    assert mattermost_reply_root(_post(), home_is_dm=True) == ""
    assert mattermost_reply_root(_post(), home_is_dm=False) == "p1"


def test_text_commands():
    assert parse_mattermost_command("!status") == ("status", "")
    assert parse_mattermost_command("!Restart ops") == ("restart", "ops")
    assert parse_mattermost_command("!deploy") is None
    assert parse_mattermost_command("status") is None


def test_webhook_bot_and_plugin_posts_are_ignored_even_when_attributed_to_the_owner():
    # Mattermost gives a webhook's posts the user_id of whoever created it.
    for prop in ("from_webhook", "from_bot", "from_plugin"):
        assert _text(_post(props={prop: "true"})) is None, prop
        assert _text(_post(props={prop: True})) is None, prop
    in_channel = dict(home_is_dm=False, listen_mode="mentions", home=CHANNEL_ID)
    webhook_restart = _post("@agentbot !restart fleet", channel_id=CHANNEL_ID, props={"from_webhook": "true"})
    assert _text(webhook_restart, **in_channel) is None


def test_a_false_automation_prop_does_not_hide_a_post():
    assert _text(_post(props={"from_webhook": "false", "from_bot": ""})) == "hello"


def test_plain_http_needs_a_loopback_host_or_an_explicit_opt_out():
    assert mattermost_url_problem("https://mm.example.com", False) is None
    assert mattermost_url_problem("http://localhost:8065", False) is None
    assert mattermost_url_problem("http://127.0.0.1:8065", False) is None
    assert mattermost_url_problem("http://[::1]:8065", False) is None
    assert "plain http" in mattermost_url_problem("http://mm.example.com", False)
    assert mattermost_url_problem("http://mm.example.com", True) is None
    assert mattermost_url_problem("ftp://mm.example.com", True) is not None
    with pytest.raises(ValueError):
        MattermostBridge({"server_url": "http://mm.example.com", "bot_token": TOKEN, "user": "mwallace"})
    MattermostBridge({"server_url": "http://mm.example.com", "bot_token": TOKEN, "user": "mwallace",
                      "allow_insecure_http": True})


# --- fake server -------------------------------------------------------------


class FakeMattermost:
    """Just enough of the Mattermost v4 API for the bridge.

    The server keeps every post with its own clock, which starts in 1970 so
    it is far behind the test machine's clock. Channel listings behave like
    the real server: pages newest first, `before=<post id>` returns posts
    strictly older than that post's create_at, and `since` is exclusive on
    update_at and capped at 1000 posts. The rate limiter answers in plain
    text, as Mattermost's does.
    """

    SINCE_LIMIT = 1000
    START_CLOCK = 1_000_000

    def __init__(self, channel_type="D"):
        self.channel_type = channel_type
        self.store: dict = {}
        self.clock = self.START_CLOCK
        self.posts: list[dict] = []
        self.typing: list[dict] = []
        self.sockets: list = []
        self.connections = 0
        self.connected = asyncio.Event()
        self.unauthorized = 0
        self.list_requests: list[dict] = []
        self.fail_posts = 0
        self.rate_limit_posts = 0
        self.rate_limit_headers: dict = {}
        self.post_attempts: list[float] = []
        self.rate_limit_lists = 0
        self.fail_list_requests: set = set()  # 1-based numbers of listing requests to fail
        self.on_list = None
        self.close_on_connect = 0
        self.on_connect = None
        app = web.Application(middlewares=[self._auth])
        app.router.add_get("/api/v4/users/me", self._me)
        app.router.add_get("/api/v4/users/username/{name}", self._user)
        app.router.add_get("/api/v4/users/{id}", self._user)
        app.router.add_post("/api/v4/users/{id}/typing", self._typing)
        app.router.add_post("/api/v4/channels/direct", self._direct)
        app.router.add_get("/api/v4/channels/{id}/posts", self._channel_posts)
        app.router.add_get("/api/v4/channels/{id}", self._channel)
        app.router.add_post("/api/v4/posts", self._create_post)
        app.router.add_get("/api/v4/websocket", self._websocket)
        self.runner = web.AppRunner(app)

    def make(self, message="hello", user_id=OWNER_ID, channel_id=None, same_ms=False, step=1, **extra):
        """Store a post as the server would, stamped with the server clock
        (advanced by step milliseconds first)."""
        if not same_ms:
            self.clock += step
        post_id = extra.pop("id", f"p{len(self.store):025d}")
        post = _post(message, user_id=user_id, channel_id=channel_id or self.home_id,
                     id=post_id, create_at=self.clock, update_at=self.clock, **extra)
        self.store[post_id] = post
        return post

    def delete(self, post_id):
        self.store[post_id]["delete_at"] = self.clock

    def _rate_limited(self):
        return web.Response(text="limit exceeded\n", status=429, content_type="text/plain",
                            headers=self.rate_limit_headers)

    @property
    def home_id(self):
        return DM_ID if self.channel_type == "D" else CHANNEL_ID

    @web.middleware
    async def _auth(self, request, handler):
        if request.headers.get("Authorization") != f"Bearer {TOKEN}":
            self.unauthorized += 1
            return web.json_response({"message": "unauthorized"}, status=401)
        return await handler(request)

    async def _me(self, request):
        return web.json_response({"id": BOT_ID, "username": "agentbot"})

    async def _user(self, request):
        key = request.match_info.get("name") or request.match_info["id"]
        if key not in ("mwallace", OWNER_ID):
            return web.json_response({"message": "not found"}, status=404)
        return web.json_response({"id": OWNER_ID, "username": "mwallace"})

    async def _typing(self, request):
        self.typing.append(await request.json())
        return web.json_response({"status": "OK"})

    async def _direct(self, request):
        assert sorted(await request.json()) == sorted([BOT_ID, OWNER_ID])
        return web.json_response({"id": DM_ID, "type": "D", "name": f"{BOT_ID}__{OWNER_ID}"})

    async def _channel(self, request):
        return web.json_response({"id": request.match_info["id"], "type": self.channel_type, "name": "town-square"})

    async def _channel_posts(self, request):
        self.list_requests.append(dict(request.query))
        if self.on_list is not None:
            await self.on_list(dict(request.query))
        if self.rate_limit_lists:
            self.rate_limit_lists -= 1
            return self._rate_limited()
        if len(self.list_requests) in self.fail_list_requests:
            self.fail_list_requests.discard(len(self.list_requests))
            return web.json_response({"message": "try again"}, status=503)
        in_channel = [p for p in self.store.values() if p["channel_id"] == request.match_info["id"]]
        if "since" in request.query:
            since = int(request.query["since"])
            # The real server promises no order before applying its cap.
            page = sorted((p for p in in_channel if p["update_at"] > since),
                          key=lambda p: p["update_at"], reverse=True)[:self.SINCE_LIMIT]
        else:
            per_page = int(request.query.get("per_page", 60))
            start = int(request.query.get("page", 0)) * per_page
            live = [p for p in in_channel if not p.get("delete_at")]
            if "before" in request.query:  # deleted posts still anchor, as on the server
                live = [p for p in live if p["create_at"] < self.store[request.query["before"]]["create_at"]]
            newest_first = sorted(live, key=lambda p: (p["create_at"], p["id"]), reverse=True)
            page = newest_first[start:start + per_page]
        posts = {p["id"]: p for p in page}
        for p in page:  # like the server, include the thread roots of replies
            if p.get("root_id") in self.store:
                posts[p["root_id"]] = self.store[p["root_id"]]
        return web.json_response({"order": [p["id"] for p in page], "posts": posts})

    async def _create_post(self, request):
        self.post_attempts.append(asyncio.get_running_loop().time())
        if self.rate_limit_posts:
            self.rate_limit_posts -= 1
            return self._rate_limited()
        if self.fail_posts:
            self.fail_posts -= 1
            return web.json_response({"message": "try again"}, status=503)
        post = await request.json()
        self.posts.append(post)
        self.clock += 1
        stored = {"id": f"reply{len(self.posts):021d}", "user_id": BOT_ID, "type": "",
                  "create_at": self.clock, "update_at": self.clock, **post}
        self.store[stored["id"]] = stored
        return web.json_response(stored, status=201)

    async def _websocket(self, request):
        ws = web.WebSocketResponse()
        await ws.prepare(request)
        self.connections += 1
        if self.close_on_connect:
            self.close_on_connect -= 1
            await ws.close()
            return ws
        self.sockets.append(ws)
        if self.on_connect is not None:
            await self.on_connect(ws)
        self.connected.set()
        async for _ in ws:
            pass
        return ws

    async def push(self, post, ws=None):
        """Deliver post as a live `posted` event (on the newest socket by default)."""
        self.store.setdefault(post["id"], post)
        await (ws or self.sockets[-1]).send_str(json.dumps({
            "event": "posted", "data": {"post": json.dumps(post)}, "broadcast": {}, "seq": 1,
        }))

    async def drop_connection(self):
        self.connected.clear()
        await self.sockets[-1].close()

    async def __aenter__(self):
        await self.runner.setup()
        site = web.TCPSite(self.runner, "127.0.0.1", 0)
        await site.start()
        port = site._server.sockets[0].getsockname()[1]
        self.url = f"http://127.0.0.1:{port}"
        return self

    async def __aexit__(self, *exc):
        await self.runner.cleanup()


async def _wait_for(predicate, timeout=10.0):
    deadline = asyncio.get_running_loop().time() + timeout
    while not predicate():
        if asyncio.get_running_loop().time() > deadline:
            raise AssertionError("condition not reached in time")
        await asyncio.sleep(0.01)


def _bridge(server, **settings):
    mm = MattermostBridge({"server_url": server.url, "bot_token": TOKEN, "user": "@mwallace", **settings})
    mm.RECONNECT_INITIAL_DELAY = 0.01
    mm.TYPING_INTERVAL = 0.01
    mm.POST_RETRY_DELAY = 0.01
    return mm


def _from_owner(text, channel="[dm]"):
    return f"[from:mwallace ({OWNER_ID})] {channel} {text}"


class _Agentdeck:
    """Patches the agent-deck side of the bridge; records what reached the conductor.

    With real_send, send_to_conductor itself runs (with its reply-owner
    bookkeeping) and only the CLI underneath it is replaced by run_cli.
    """

    def __init__(self, status="idle", reply=(True, "pong", False), run_cli=None, drain=False):
        self.sent: list[tuple] = []
        self.reply = reply
        self._patches = [
            mock.patch("bridge.discover_conductors", return_value=FLEET),
            mock.patch("bridge.get_default_conductor", return_value=FLEET[0]),
            mock.patch("bridge.ensure_conductor_running", new=mock.AsyncMock(return_value=True)),
            mock.patch("bridge.get_session_status", return_value=status),
        ]
        if run_cli is None:
            self._patches.append(mock.patch("bridge.send_to_conductor", side_effect=self._send))
        else:
            self._patches.append(mock.patch("bridge.run_cli", side_effect=run_cli))
            if not drain:
                # Queued messages stay in bridge._message_queue for the test to read.
                self._patches.append(mock.patch("bridge._ensure_drain_task"))

    def _send(self, session, message, **kwargs):
        self.sent.append((session, message, kwargs))
        if kwargs.get("wait_for_reply"):
            return self.reply
        return True, "", False

    def __enter__(self):
        for p in self._patches:
            p.start()
        return self

    def __exit__(self, *exc):
        for p in reversed(self._patches):
            p.stop()
        bridge._message_queue.clear()
        bridge._drain_task = None
        bridge._pending_reply_tasks.clear()
        bridge._wait_send_reservations.clear()


def _track_catch_ups(mm):
    """Count finished catch-ups, so a test knows the bridge is past one."""
    mm.catch_ups = 0
    catch_up = mm._catch_up

    async def counted():
        await catch_up()
        mm.catch_ups += 1

    mm._catch_up = counted


async def _running(mm, server):
    _track_catch_ups(mm)
    task = asyncio.create_task(mm.run())
    await asyncio.wait_for(server.connected.wait(), 5)
    await _wait_for(lambda: mm.catch_ups >= 1)
    return task


async def _stop(mm, task):
    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task
    await mm.close()


async def _reconnect(mm, server):
    done = mm.catch_ups
    await server.drop_connection()
    await asyncio.wait_for(server.connected.wait(), 5)
    await _wait_for(lambda: mm.catch_ups > done)


# --- bridge against the fake server -----------------------------------------


def test_start_resolves_the_owner_and_opens_a_dm():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            await mm.start()
            await mm.close()
            return mm

    mm = asyncio.run(scenario())
    assert (mm.bot_username, mm.owner_user_id, mm.home_channel_id) == ("agentbot", OWNER_ID, DM_ID)
    assert mm.home_is_dm and mm.home_channel_tag == "[dm]"


def test_a_configured_channel_is_the_home_instead_of_a_dm():
    async def scenario():
        async with FakeMattermost(channel_type="O") as server:
            mm = _bridge(server, channel_id=CHANNEL_ID)
            await mm.start()
            await mm.close()
            return mm

    mm = asyncio.run(scenario())
    assert mm.home_channel_id == CHANNEL_ID and not mm.home_is_dm
    assert mm.home_channel_tag == f"[channel:~town-square ({CHANNEL_ID})]"


def test_a_message_is_relayed_and_the_reply_posted_back():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck() as agentdeck:
                task = await _running(mm, server)
                await server.push(server.make("hello conductor"))
                await _wait_for(lambda: server.posts)
                await _stop(mm, task)
            return server, agentdeck

    server, agentdeck = asyncio.run(scenario())
    session, message, kwargs = agentdeck.sent[0]
    assert session == "conductor-fleet"
    assert message == _from_owner("hello conductor")
    assert kwargs["wait_for_reply"] is True
    assert server.posts == [{"channel_id": DM_ID, "message": "pong", "root_id": "",
                             "props": {MM_BRIDGE_POST_PROP: True}}]
    assert server.unauthorized == 0


def test_the_bridge_ignores_its_own_reply_when_the_server_echoes_it():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck() as agentdeck:
                task = await _running(mm, server)
                await server.push(server.make("pong", user_id=BOT_ID))
                await server.push(server.make("hi"))
                await _wait_for(lambda: server.posts)
                await _stop(mm, task)
            return agentdeck

    assert [m for _, m, _ in asyncio.run(scenario()).sent] == [_from_owner("hi")]


def test_a_webhook_post_attributed_to_the_owner_runs_nothing():
    async def scenario():
        async with FakeMattermost(channel_type="O") as server:
            mm = _bridge(server, channel_id=CHANNEL_ID, listen_mode="mentions")
            with _Agentdeck() as agentdeck, mock.patch("bridge.mattermost_command_reply") as command:
                task = await _running(mm, server)
                await server.push(server.make("@agentbot !restart fleet", props={"from_webhook": "true"}))
                await server.push(server.make("@agentbot run the deploy", props={"from_webhook": "true"}))
                await asyncio.sleep(0.2)
                await _stop(mm, task)
            return server, agentdeck, command

    server, agentdeck, command = asyncio.run(scenario())
    command.assert_not_called()
    assert agentdeck.sent == [] and server.posts == []


def test_a_busy_conductor_gets_the_message_queued_and_the_user_told():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck(status="running") as agentdeck:
                task = await _running(mm, server)
                await server.push(server.make("are you there", root_id="thread1"))
                await _wait_for(lambda: server.posts)
                await _stop(mm, task)
            return server, agentdeck

    server, agentdeck = asyncio.run(scenario())
    assert agentdeck.sent[0][2]["force_queue"] is True
    assert "message queued" in server.posts[0]["message"]
    assert server.posts[0]["root_id"] == "thread1"


def test_a_turn_that_outlasts_the_wait_is_handed_to_a_reply_watcher():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck(reply=(False, "", True)), \
                    mock.patch("bridge._register_pending_reply", return_value=True) as register:
                task = await _running(mm, server)
                await server.push(server.make("long job"))
                await _wait_for(lambda: server.posts)
                await _stop(mm, task)
            return server, register

    server, register = asyncio.run(scenario())
    assert "Still working" in server.posts[0]["message"]
    assert register.call_args.args[:3] == ("conductor-fleet", "default", None)


def _cli_result(returncode=0, payload=None):
    return types.SimpleNamespace(returncode=returncode, stdout=json.dumps(payload or {}), stderr="")


def test_a_message_during_a_pending_late_reply_is_queued_then_drained_and_both_replies_arrive():
    """Runs the real send_to_conductor, late-reply watcher and queue drain.
    The first turn outlasts --wait; while its watcher owns the session, a
    second message must queue rather than start an unowned turn, and once the
    first reply is out the queue delivers it and posts its own reply."""
    receipt = {"receipt_id": "r1", "instance_id": "i1", "codex_session_id": "s1",
               "turn_generation": "s1:7", "accepted_at": "2026-01-01T00:00:00Z"}
    release_output = threading.Event()
    cli_calls: list[tuple] = []
    latest = {"content": "late answer", "codex_turn_generation": "s1:7"}

    def run_cli(*args, **kwargs):
        cli_calls.append(args)
        if args[:2] == ("session", "send") and "-q" in args:  # the queue drain
            latest.update(content="second answer", codex_turn_generation="s1:8")
            return _cli_result(0)
        if args[:2] == ("session", "send"):
            return _cli_result(1, {"completion": "timeout", "delivery": "submitted", "submitted": True,
                                   "accepted_turn_kind": "codex_rollout", "accepted_turn": receipt})
        if args[:2] == ("session", "output"):
            release_output.wait(5)
            return _cli_result(0, dict(latest))
        raise AssertionError(f"unexpected CLI call {args}")

    real_sleep = asyncio.sleep

    async def fast_sleep(delay, *args, **kwargs):  # the drain polls every 5s
        await real_sleep(min(delay, 0.01), *args, **kwargs)

    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck(run_cli=run_cli, drain=True), mock.patch("asyncio.sleep", fast_sleep):
                try:
                    task = await _running(mm, server)
                    await server.push(server.make("long job"))
                    await _wait_for(lambda: any("Still working" in p["message"] for p in server.posts))
                    await server.push(server.make("and another thing"))
                    await _wait_for(lambda: any("message queued" in p["message"] for p in server.posts))
                    queued = [m for m, _, _ in bridge._message_queue.get("conductor-fleet", [])]
                    release_output.set()
                    await _wait_for(lambda: any("second answer" in p["message"] for p in server.posts))
                    await _wait_for(lambda: not bridge._message_queue)
                    await _stop(mm, task)
                finally:
                    release_output.set()
            return server, queued

    server, queued = asyncio.run(scenario())
    assert queued == [_from_owner("and another thing")]
    sends = [args for args in cli_calls if args[:2] == ("session", "send")]
    assert [args[3] for args in sends] == [_from_owner("long job"), _from_owner("and another thing")]
    replies = [p["message"] for p in server.posts if "answer" in p["message"]]
    assert len(replies) == 2
    assert replies[0].startswith("Queued response (waited") and replies[0].endswith("late answer")
    assert replies[1].startswith("Queued response (waited") and replies[1].endswith("second answer")


def test_a_reply_is_retried_when_the_server_fails_to_take_it():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck():
                task = await _running(mm, server)
                server.fail_posts = 2
                await server.push(server.make("hello"))
                await _wait_for(lambda: server.posts)
                await asyncio.sleep(0.1)
                await _stop(mm, task)
            return server

    server = asyncio.run(scenario())
    assert [p["message"] for p in server.posts] == ["pong"]


def test_retry_after_comes_from_the_rate_limit_headers():
    assert mattermost_retry_after({"X-Ratelimit-Reset": "3"}) == 3
    assert mattermost_retry_after({"Retry-After": "7", "X-Ratelimit-Reset": "3"}) == 7
    assert mattermost_retry_after({"Retry-After": "Wed, 21 Oct 2026 07:28:00 GMT", "X-Ratelimit-Reset": "2"}) == 2
    assert mattermost_retry_after({"Retry-After": "-1"}) == 0
    assert mattermost_retry_after({}) is None


def test_a_rate_limited_reply_is_sent_after_the_wait_the_server_asks_for():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            mm.RATE_LIMIT_DEFAULT_WAIT = 0.01  # so only the header can explain a 1s wait
            with _Agentdeck():
                task = await _running(mm, server)
                server.rate_limit_posts = 1
                server.rate_limit_headers = {"X-Ratelimit-Reset": "1"}
                await server.push(server.make("hello"))
                await _wait_for(lambda: server.posts)
                await asyncio.sleep(0.1)
                await _stop(mm, task)
            return server

    server = asyncio.run(scenario())
    assert [p["message"] for p in server.posts] == ["pong"]
    assert len(server.post_attempts) == 2
    assert server.post_attempts[1] - server.post_attempts[0] >= 0.9


def test_a_rate_limited_catch_up_waits_and_carries_on_without_reconnecting():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck() as agentdeck:
                task = await _running(mm, server)
                await server.drop_connection()
                server.make("missed while away")
                server.rate_limit_lists = 2
                server.rate_limit_headers = {"X-Ratelimit-Reset": "0"}
                await asyncio.wait_for(server.connected.wait(), 5)
                await _wait_for(lambda: server.posts)
                await _stop(mm, task)
            return server, agentdeck

    server, agentdeck = asyncio.run(scenario())
    assert server.rate_limit_lists == 0 and server.connections == 2
    assert [m for _, m, _ in agentdeck.sent] == [_from_owner("missed while away")]


def test_a_burst_of_rate_limited_replies_all_arrive():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            mm.RATE_LIMIT_DEFAULT_WAIT = 0.01
            with _Agentdeck(status="running"):
                task = await _running(mm, server)
                await server.drop_connection()
                for i in range(20):
                    server.make(f"burst {i}")
                server.rate_limit_posts = 15  # no retry guidance in these
                await asyncio.wait_for(server.connected.wait(), 5)
                await _wait_for(lambda: len(server.posts) == 20)
                await asyncio.sleep(0.1)
                await _stop(mm, task)
            return server

    server = asyncio.run(scenario())
    assert len(server.posts) == 20 and all("message queued" in p["message"] for p in server.posts)


def test_an_undeliverable_reply_is_logged_and_the_bridge_keeps_working(caplog):
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck() as agentdeck:
                task = await _running(mm, server)
                server.fail_posts = mm.POST_ATTEMPTS
                await server.push(server.make("first"))
                await _wait_for(lambda: len(agentdeck.sent) == 1 and server.fail_posts == 0)
                await server.push(server.make("second"))
                await _wait_for(lambda: server.posts)
                await _stop(mm, task)
            return server, agentdeck

    with caplog.at_level(logging.WARNING, logger="conductor-bridge"):
        server, agentdeck = asyncio.run(scenario())
    assert [m for _, m, _ in agentdeck.sent] == [_from_owner("first"), _from_owner("second")]
    assert [p["message"] for p in server.posts] == ["pong"]
    assert any("Mattermost post failed (attempt 3)" in r.getMessage() for r in caplog.records)


def test_a_failure_without_a_message_is_logged_by_its_type(caplog):
    attempts = []

    async def start_mattermost():
        attempts.append(1)
        if len(attempts) == 1:
            raise asyncio.TimeoutError()

    async def no_wait(_seconds):
        pass

    with caplog.at_level(logging.ERROR, logger="conductor-bridge"), mock.patch.object(
        bridge.asyncio, "sleep", no_wait
    ):
        asyncio.run(bridge._run_platform_task("Mattermost", start_mattermost))
    assert len(attempts) == 2
    assert [r.getMessage() for r in caplog.records] == [
        "Mattermost task failed: TimeoutError; retrying in 5s"
    ]


def test_typing_is_shown_while_waiting_for_the_conductor():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            agentdeck = _Agentdeck()

            def slow_send(session, message, **kwargs):
                time.sleep(0.1)
                return True, "done", False

            with agentdeck, mock.patch("bridge.send_to_conductor", side_effect=slow_send):
                task = await _running(mm, server)
                await server.push(server.make("think hard"))
                await _wait_for(lambda: server.posts)
                await _stop(mm, task)
            return server

    server = asyncio.run(scenario())
    assert server.typing and server.typing[0] == {"channel_id": DM_ID, "parent_id": ""}


def test_a_text_command_is_answered_without_the_conductor():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck() as agentdeck, \
                    mock.patch("bridge.mattermost_command_reply", return_value="all good") as command:
                task = await _running(mm, server)
                await server.push(server.make("!status"))
                await _wait_for(lambda: server.posts)
                await _stop(mm, task)
            return server, agentdeck, command

    server, agentdeck, command = asyncio.run(scenario())
    command.assert_called_once_with("status", "")
    assert server.posts[0]["message"] == "all good"
    assert agentdeck.sent == []


# --- catching up after a disconnect -----------------------------------------


def test_posts_made_while_disconnected_are_caught_up_once_after_reconnecting():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck() as agentdeck:
                task = await _running(mm, server)
                await server.push(server.make("before"))
                await _wait_for(lambda: len(server.posts) == 1)
                await server.drop_connection()
                server.make("while away")
                await asyncio.wait_for(server.connected.wait(), 5)
                await _wait_for(lambda: len(server.posts) == 2)
                await asyncio.sleep(0.1)
                await _stop(mm, task)
            return agentdeck

    sent = [m for _, m, _ in asyncio.run(scenario()).sent]
    assert sent == [_from_owner("before"), _from_owner("while away")]


def test_posts_already_in_the_channel_at_start_are_not_replayed():
    async def scenario():
        async with FakeMattermost() as server:
            server.make("old command")
            server.make("recent command")
            mm = _bridge(server)
            with _Agentdeck() as agentdeck:
                await mm.start()
                server.make("sent while the stream was connecting")
                task = await _running(mm, server)
                await _wait_for(lambda: server.posts)
                await _reconnect(mm, server)
                await asyncio.sleep(0.2)
                await _stop(mm, task)
            return agentdeck

    assert [m for _, m, _ in asyncio.run(scenario()).sent] == [_from_owner("sent while the stream was connecting")]


def test_a_local_clock_far_ahead_of_the_server_loses_nothing():
    # The fake server's clock reads 1970, decades behind this machine's.
    assert FakeMattermost.START_CLOCK < time.time() * 1000 - 1e12

    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck() as agentdeck:
                task = await _running(mm, server)
                await server.drop_connection()
                server.make("missed while away")
                await asyncio.wait_for(server.connected.wait(), 5)
                await _wait_for(lambda: server.posts)
                await _stop(mm, task)
            return agentdeck

    assert [m for _, m, _ in asyncio.run(scenario()).sent] == [_from_owner("missed while away")]


def test_a_missed_post_in_the_same_millisecond_as_the_last_one_seen_is_caught_up():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck() as agentdeck:
                task = await _running(mm, server)
                await server.push(server.make("seen live"))
                server.make("lost in the same millisecond", same_ms=True)
                await _wait_for(lambda: len(server.posts) == 1)
                await _reconnect(mm, server)
                await _wait_for(lambda: len(server.posts) == 2)
                await asyncio.sleep(0.1)
                await _stop(mm, task)
            return agentdeck

    sent = [m for _, m, _ in asyncio.run(scenario()).sent]
    assert sent == [_from_owner("seen live"), _from_owner("lost in the same millisecond")]


def test_a_backlog_over_the_servers_since_cap_is_recovered_in_full():
    """A long outage in a busy channel: the owner's command is followed by
    more than 1000 posts from other people."""
    async def scenario():
        async with FakeMattermost(channel_type="O") as server:
            mm = _bridge(server, channel_id=CHANNEL_ID)
            with _Agentdeck() as agentdeck:
                task = await _running(mm, server)
                await server.drop_connection()
                server.make("deploy when you can")
                for i in range(FakeMattermost.SINCE_LIMIT + 200):
                    server.make(f"chatter {i}", user_id=STRANGER_ID)
                await asyncio.wait_for(server.connected.wait(), 5)
                await _wait_for(lambda: server.posts)
                await asyncio.sleep(0.2)
                await _stop(mm, task)
            return server, agentdeck

    server, agentdeck = asyncio.run(scenario())
    assert [m for _, m, _ in agentdeck.sent] == [_from_owner("deploy when you can", f"[channel:~town-square ({CHANNEL_ID})]")]
    assert len(server.posts) == 1


def test_posts_seen_both_live_and_in_a_large_catch_up_are_handled_once():
    """More posts than any fixed-size ID cache, each delivered over the new
    socket and returned again by the catch-up."""
    count = 600

    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck(status="running") as agentdeck:
                task = await _running(mm, server)
                await server.drop_connection()
                missed = [server.make(f"message {i}") for i in range(count)]

                async def replay(ws):
                    for post in missed:
                        await server.push(post, ws)

                server.on_connect = replay
                await asyncio.wait_for(server.connected.wait(), 5)
                await _wait_for(lambda: len(server.posts) >= count)
                await asyncio.sleep(0.3)
                await _stop(mm, task)
            return server, agentdeck

    server, agentdeck = asyncio.run(scenario())
    sent = [m for _, m, _ in agentdeck.sent]
    assert len(sent) == count and len(set(sent)) == count
    assert len(server.posts) == count


def test_an_overlap_spanning_minutes_is_still_handled_once():
    """The posts the socket delivers after a catch-up can be spread over far
    more than the one-minute overlap; none may run twice."""
    count = 600

    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck(status="running") as agentdeck:
                task = await _running(mm, server)
                await server.drop_connection()
                missed = [server.make(f"message {i}", step=1000) for i in range(count)]  # ten minutes

                async def replay(ws):
                    for post in missed:
                        await server.push(post, ws)

                server.on_connect = replay
                await asyncio.wait_for(server.connected.wait(), 5)
                await _wait_for(lambda: len(server.posts) >= count)
                await asyncio.sleep(0.3)
                await _stop(mm, task)
            return server, agentdeck

    server, agentdeck = asyncio.run(scenario())
    sent = [m for _, m, _ in agentdeck.sent]
    assert len(sent) == count and len(set(sent)) == count


def test_a_command_arriving_while_the_bridge_reads_existing_posts_is_handled():
    async def scenario():
        async with FakeMattermost() as server:
            for i in range(3):
                server.make(f"old {i}")
            mm = _bridge(server)
            mm.CATCH_UP_PAGE_SIZE = 2  # the startup read takes several pages

            async def command_after_first_read(query):
                if len(server.list_requests) == 2:  # between the first read and the second
                    server.make("restart the build")

            server.on_list = command_after_first_read
            with _Agentdeck() as agentdeck:
                await mm.start()
                server.on_list = None
                task = await _running(mm, server)
                await _wait_for(lambda: server.posts)
                await asyncio.sleep(0.1)
                await _stop(mm, task)
            return server, agentdeck

    server, agentdeck = asyncio.run(scenario())
    assert len(server.list_requests) >= 3
    assert [m for _, m, _ in agentdeck.sent] == [_from_owner("restart the build")]


def test_more_than_a_page_of_commands_arriving_during_the_startup_read_are_all_handled():
    async def scenario():
        async with FakeMattermost() as server:
            for i in range(3):
                server.make(f"old {i}")
            mm = _bridge(server)
            mm.CATCH_UP_PAGE_SIZE = 2

            async def burst_after_first_read(query):
                if len(server.list_requests) == 2:
                    for i in range(3):
                        server.make(f"new command {i}")

            server.on_list = burst_after_first_read
            with _Agentdeck(status="running") as agentdeck:
                await mm.start()
                server.on_list = None
                task = await _running(mm, server)
                await _wait_for(lambda: len(server.posts) >= 3)
                await asyncio.sleep(0.1)
                await _stop(mm, task)
            return agentdeck

    sent = [m for _, m, _ in asyncio.run(scenario()).sent]
    assert sent == [_from_owner(f"new command {i}") for i in range(3)]


def test_a_post_deleted_between_catch_up_pages_does_not_hide_another():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            mm.CATCH_UP_PAGE_SIZE = 3
            with _Agentdeck(status="running") as agentdeck:
                task = await _running(mm, server)
                await server.drop_connection()
                commands = [server.make(f"command {i}") for i in range(9)]
                first_catch_up_read = len(server.list_requests) + 1

                async def delete_a_read_post(query):
                    if len(server.list_requests) == first_catch_up_read + 1:
                        server.delete(commands[7]["id"])  # on the first page, already read

                server.on_list = delete_a_read_post
                await asyncio.wait_for(server.connected.wait(), 5)
                await _wait_for(lambda: mm.catch_ups >= 2)
                await asyncio.sleep(0.2)
                await _stop(mm, task)
            return agentdeck

    sent = [m for _, m, _ in asyncio.run(scenario()).sent]
    assert sent == [_from_owner(f"command {i}") for i in range(9)]


def test_posts_sharing_a_millisecond_across_a_page_edge_are_all_caught_up():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            mm.CATCH_UP_PAGE_SIZE = 3
            with _Agentdeck(status="running") as agentdeck:
                task = await _running(mm, server)
                await server.drop_connection()
                server.make("tie 0")
                server.make("tie 1", same_ms=True)
                server.make("tie 2", same_ms=True)
                server.make("after the tie")
                await asyncio.wait_for(server.connected.wait(), 5)
                await _wait_for(lambda: mm.catch_ups >= 2)
                await asyncio.sleep(0.2)
                await _stop(mm, task)
            return agentdeck

    sent = sorted(m for _, m, _ in asyncio.run(scenario()).sent)
    assert sent == sorted(_from_owner(t) for t in ("tie 0", "tie 1", "tie 2", "after the tie"))


def test_a_backlog_of_hundreds_of_pages_is_recovered_in_full():
    """More pages of other people's posts than any fixed page limit."""
    async def scenario():
        async with FakeMattermost(channel_type="O") as server:
            mm = _bridge(server, channel_id=CHANNEL_ID)
            mm.CATCH_UP_PAGE_SIZE = 5
            with _Agentdeck() as agentdeck:
                task = await _running(mm, server)
                await server.drop_connection()
                server.make("deploy when you can")
                for i in range(600):
                    server.make(f"chatter {i}", user_id=STRANGER_ID)
                pages_before = len(server.list_requests)
                await asyncio.wait_for(server.connected.wait(), 5)
                await _wait_for(lambda: server.posts)
                await asyncio.sleep(0.1)
                await _stop(mm, task)
            return server, agentdeck, len(server.list_requests) - pages_before

    server, agentdeck, pages = asyncio.run(scenario())
    assert pages > 100
    assert [m for _, m, _ in agentdeck.sent] == [_from_owner("deploy when you can", f"[channel:~town-square ({CHANNEL_ID})]")]


def test_a_catch_up_that_fails_part_way_loses_nothing():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            mm.CATCH_UP_PAGE_SIZE = 5
            with _Agentdeck() as agentdeck:
                task = await _running(mm, server)
                await server.drop_connection()
                server.make("the oldest missed command")
                for i in range(30):
                    server.make(f"note {i}", user_id=BOT_ID, props={MM_BRIDGE_POST_PROP: True})
                server.make("the newest missed command")
                server.fail_list_requests = {len(server.list_requests) + 4}
                await _wait_for(lambda: len(server.posts) == 2)
                await asyncio.sleep(0.1)
                await _stop(mm, task)
            return server, agentdeck

    server, agentdeck = asyncio.run(scenario())
    assert server.fail_list_requests == set()
    assert [m for _, m, _ in agentdeck.sent] == [_from_owner("the oldest missed command"),
                                                _from_owner("the newest missed command")]


def test_posts_in_other_channels_do_not_move_the_catch_up_point():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            with _Agentdeck():
                task = await _running(mm, server)
                start = mm._last_create_at
                await server.push(server.make("elsewhere", channel_id="x" * 26))
                await asyncio.sleep(0.1)
                await _stop(mm, task)
            return start, mm._last_create_at

    start, after = asyncio.run(scenario())
    assert after == start


def _reconnect_delays(caplog):
    return [float(r.args[0]) for r in caplog.records if r.getMessage().startswith("Mattermost: reconnecting in")]


def test_connections_that_close_at_once_back_off(caplog):
    async def scenario():
        async with FakeMattermost() as server:
            server.close_on_connect = 4
            mm = _bridge(server)
            with _Agentdeck():
                task = await _running(mm, server)
                await _stop(mm, task)
            return server

    with caplog.at_level(logging.INFO, logger="conductor-bridge"):
        server = asyncio.run(scenario())
    assert server.connections == 5
    assert _reconnect_delays(caplog) == [0.01, 0.02, 0.04, 0.08]


def test_a_connection_that_stayed_up_resets_the_backoff(caplog):
    async def scenario():
        async with FakeMattermost() as server:
            server.close_on_connect = 3
            mm = _bridge(server)
            mm.STABLE_CONNECTION_SECONDS = 0
            with _Agentdeck():
                task = await _running(mm, server)
                await _stop(mm, task)

    with caplog.at_level(logging.INFO, logger="conductor-bridge"):
        asyncio.run(scenario())
    assert _reconnect_delays(caplog) == [0.01, 0.01, 0.01]


# --- posting -------------------------------------------------------------------


def test_long_replies_are_split_into_several_posts():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            await mm.start()
            ok = await mm.post("x" * (bridge.MM_MAX_LENGTH + 10), root_id="r")
            await mm.close()
            return ok, server

    ok, server = asyncio.run(scenario())
    assert ok and len(server.posts) == 2 and all(p["root_id"] == "r" for p in server.posts)


def test_need_alerts_reach_mattermost():
    async def scenario():
        async with FakeMattermost() as server:
            mm = _bridge(server)
            await mm.start()
            delivered = await bridge._deliver_need_alert("NEED: approve the deploy", None, None, None,
                                                         None, None, None, mm)
            await mm.close()
            return delivered, server

    delivered, server = asyncio.run(scenario())
    assert delivered and server.posts[0]["message"] == "NEED: approve the deploy"


def test_an_alert_before_the_bot_connects_is_not_counted_as_delivered():
    mm = MattermostBridge({"server_url": "https://unused", "bot_token": TOKEN, "user": "mwallace"})
    assert asyncio.run(bridge._deliver_need_alert("NEED: x", None, None, None, None, None, None, mm)) is False


# --- config ------------------------------------------------------------------


def _load_config(tmp_path, body):
    config_path = tmp_path / "config.toml"
    config_path.write_text(body)
    real_toml = pytest.importorskip("toml")
    with mock.patch("bridge.CONFIG_PATH", config_path), \
            mock.patch("bridge.discover_conductors", return_value=FLEET), \
            mock.patch("bridge.toml", real_toml):
        return bridge.load_config()


def test_load_config_reads_the_mattermost_table(tmp_path, monkeypatch):
    monkeypatch.setenv("MM_TEST_TOKEN", "secret")
    config = _load_config(tmp_path, (
        '[conductor.mattermost]\n'
        'server_url = "https://mattermost.example.com/"\n'
        'bot_token = "$MM_TEST_TOKEN"\n'
        'user = "@mwallace"\n'
    ))
    assert config["mattermost"] == {
        "server_url": "https://mattermost.example.com", "bot_token": "secret", "user": "mwallace",
        "channel_id": "", "listen_mode": "all", "allow_insecure_http": False, "configured": True,
    }


def test_load_config_refuses_a_remote_plain_http_server_unless_allowed(tmp_path):
    table = ('[conductor.slack]\nbot_token = "xoxb-1"\napp_token = "xapp-1"\nchannel_id = "C1"\n'
             '[conductor.mattermost]\nserver_url = "http://mm.example.com"\nbot_token = "t"\nuser = "me"\n')
    assert _load_config(tmp_path, table)["mattermost"]["configured"] is False
    allowed = _load_config(tmp_path, table + "allow_insecure_http = true\n")["mattermost"]
    assert allowed["configured"] is True and allowed["allow_insecure_http"] is True
