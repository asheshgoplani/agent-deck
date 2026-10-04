#!/usr/bin/env python3
"""Boundary doubles. Never writes status, inbox, spool, or ledger files.

Recorded payloads go unchanged to the production hook/notify entrypoint. Pi
loads the installed production extension; OpenCode serves the production SSE
wire protocol. The caller owns isolated HOME/XDG config, sessions and teardown.
"""
import argparse
import base64
import http.server
import json
import os
from pathlib import Path
import queue
import select
import signal
import subprocess
import sys
import termios
import threading
import time
import tty
import urllib.request

TOOLS = ("claude", "codex", "gemini", "pi", "hermes", "opencode", "cursor")
HERE = Path(__file__).resolve().parent


def replay(tool, event, payload, instance, binary="agent-deck", pi_extension=None):
    """Return the real hook's CompletedProcess; stdout remains unmodified."""
    env = dict(os.environ, AGENTDECK_INSTANCE_ID=instance)
    if tool == "opencode":
        raise ValueError("OpenCode uses --serve and POST /emit, not hook-handler")
    if tool == "pi":
        extension = pi_extension or str(
            Path(env.get("PI_CODING_AGENT_DIR", str(Path.home() / ".pi/agent")))
            / "extensions/agent-deck.ts"
        )
        # The production extension resolves agent-deck on PATH.
        if os.path.dirname(binary):
            env["PATH"] = str(Path(binary).resolve().parent) + os.pathsep + env["PATH"]
        return subprocess.run(
            ["node", str(HERE / "pi_driver.mjs"), extension, event],
            input=payload, capture_output=True, env=env, timeout=15,
        )
    json.loads(payload)  # Reject a corrupt fixture before touching the receiver.
    if tool == "codex" and event == "agent-turn-complete":
        command = [binary, "codex-notify", payload.decode()]
        stdin = None
    else:
        command = [binary, "hook-handler"]
        stdin = payload
    return subprocess.run(command, input=stdin, capture_output=True, env=env, timeout=15)


def pane(capture, state, state_file=None, tool="claude"):
    """Record input before any line discipline, including every CR and LF.

    No command is evaluated, including approval responses. A separate .jsonl
    records chunk arrival times; the capture itself is the exact byte stream.
    State can change through a plain file so tests never send control input.
    """
    ready = {
        "claude": "❯ ", "codex": "› ", "gemini": "gemini> ",
        "pi": "pi> ", "hermes": "❯ ", "opencode": "Ask anything\r\n", "cursor": "› ",
    }
    busy = "Working…\r\nesc to interrupt\r\n"
    if tool == "gemini":
        busy = "Working…\r\nesc to cancel\r\n"
    elif tool == "opencode":
        busy = "Working…\r\nesc interrupt\r\n"
    displays = {
        "ready": ready[tool],
        "blocked": busy,
        "approval": "Do you want to proceed?\r\n  1. Yes\r\n❯ 2. No\r\n",
    }
    fd = sys.stdin.fileno()
    old = termios.tcgetattr(fd) if os.isatty(fd) else None
    stopped = threading.Event()
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, lambda *_: stopped.set())
    Path(capture).parent.mkdir(parents=True, exist_ok=True)
    try:
        if old is not None:
            tty.setraw(fd)
        current = None
        with open(capture, "ab", buffering=0) as raw, open(capture + ".jsonl", "a", buffering=1) as log:
            while not stopped.is_set():
                wanted = state
                if state_file and Path(state_file).exists():
                    wanted = Path(state_file).read_text().strip()
                if wanted not in displays:
                    raise ValueError("invalid pane state: " + wanted)
                if current != wanted:
                    current = wanted
                    sys.stdout.write("\033[2J\033[H" + displays[current])
                    sys.stdout.flush()
                if not select.select([fd], [], [], 0.1)[0]:
                    continue
                data = os.read(fd, 65536)
                if not data:
                    break
                raw.write(data)
                log.write(json.dumps({"ts_ns": time.time_ns(), "state": current,
                                      "bytes_b64": base64.b64encode(data).decode(),
                                      "cr": data.count(b"\r"), "lf": data.count(b"\n")}) + "\n")
                # Echo safe text for production paste-verification; control bytes
                # remain in the evidence without controlling this fake process.
                shown = data.decode(errors="replace").replace("\x1b[200~", "").replace("\x1b[201~", "")
                sys.stdout.write(shown.replace("\x1b", "").replace("\r", "\r\n"))
                sys.stdout.flush()
    finally:
        if old is not None:
            termios.tcsetattr(fd, termios.TCSANOW, old)


class EventServer(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, port, initial, messages, receipt):
        super().__init__(("0.0.0.0", port), EventHandler)
        self.latest = initial
        self.messages = messages
        self.receipt = receipt
        self.clients = []
        self.lock = threading.Lock()

    def record(self, method, path, payload=None):
        if not self.receipt:
            return
        with self.lock, open(self.receipt, "a") as out:
            out.write(json.dumps({"ts_ns": time.time_ns(), "method": method,
                                  "path": path, "payload": payload}) + "\n")


class EventHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, data):
        body = json.dumps(data).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        server = self.server
        server.record("GET", self.path)
        if self.path == "/session/status":
            with server.lock:
                props = server.latest.get("properties", {})
                status = props.get("status", {"type": "idle"})
                session = props.get("sessionID", "matrix-session")
            self.reply({session: status} if status.get("type") != "idle" else {})
        elif self.path == "/event":
            events = queue.Queue()
            with server.lock:
                server.clients.append(events)
                events.put(server.latest)
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.end_headers()
            try:
                while True:
                    try:
                        data = events.get(timeout=1)
                        frame = "data: " + json.dumps(data) + "\n\n"
                    except queue.Empty:
                        frame = ": heartbeat\n\n"
                    self.wfile.write(frame.encode())
                    self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                pass
            finally:
                with server.lock:
                    server.clients.remove(events)
        elif self.path.startswith("/session/") and self.path.endswith("/message"):
            self.reply(server.messages)
        elif self.path.startswith("/session/"):
            self.reply({"id": self.path.split("/")[2], "parentID": ""})
        else:
            self.send_error(404)

    def do_POST(self):
        try:
            payload = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
        except (ValueError, json.JSONDecodeError):
            self.send_error(400)
            return
        self.server.record("POST", self.path, payload)
        if self.path == "/emit":
            with self.server.lock:
                self.server.latest = payload
                for client in self.server.clients:
                    client.put(payload)
            self.reply({"emitted": True})
        elif self.path.startswith("/session/") and self.path.endswith(("/prompt_async", "/message")):
            self.reply({"accepted": True})
        else:
            self.send_error(404)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tool", required=True, choices=TOOLS)
    parser.add_argument("--event", default="")
    parser.add_argument("--payload")
    parser.add_argument("--instance", default=os.environ.get("AGENTDECK_INSTANCE_ID", ""))
    parser.add_argument("--binary", default="agent-deck")
    parser.add_argument("--pi-extension")
    parser.add_argument("--pane", action="store_true")
    parser.add_argument("--capture", default=os.environ.get("COMMS_CAPTURE"))
    parser.add_argument("--state", choices=("ready", "blocked", "approval"), default=os.environ.get("COMMS_PANE_STATE", "ready"))
    parser.add_argument("--state-file", default=os.environ.get("COMMS_PANE_STATE_FILE"))
    parser.add_argument("--serve", action="store_true")
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--messages", help="OpenCode /session/:id/message response fixture")
    parser.add_argument("--receipt", help="OpenCode HTTP request evidence JSONL")
    parser.add_argument("--emit-url", help="POST payload to an already running fake OpenCode server")
    args, unknown = parser.parse_known_args()
    if unknown and not args.pane:
        parser.error("unrecognized arguments: " + " ".join(unknown))
    if args.pane:
        if not args.capture:
            capture_dir = Path(os.environ.get("COMMS_CAPTURE_DIR", "/tmp/comms-matrix-captures"))
            args.capture = str(capture_dir / ((args.instance or str(os.getpid())) + ".raw"))
        server = None
        if args.tool == "opencode" and args.port:
            initial = json.loads((HERE / "fixtures/opencode_idle_v1.json").read_text())
            messages = json.loads((HERE / "fixtures/opencode_messages_v1.json").read_text())
            server = EventServer(args.port, initial, messages, os.environ.get("COMMS_HTTP_RECEIPT"))
            threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            pane(args.capture, args.state, args.state_file, args.tool)
        finally:
            if server is not None:
                server.shutdown()
                server.server_close()
        return
    if not args.payload:
        parser.error("replay/serve requires --payload")
    payload = Path(args.payload).read_bytes()
    if args.serve:
        if args.tool != "opencode":
            parser.error("--serve requires --tool opencode")
        messages = json.loads(Path(args.messages).read_text()) if args.messages else []
        server = EventServer(args.port, json.loads(payload), messages, args.receipt)
        print(json.dumps({"port": server.server_port}), flush=True)
        try:
            server.serve_forever()
        finally:
            server.server_close()
    elif args.emit_url:
        request = urllib.request.Request(args.emit_url.rstrip("/") + "/emit", data=payload,
                                         headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(request, timeout=5) as response:
            sys.stdout.buffer.write(response.read())
    else:
        if not args.instance or not args.event:
            parser.error("replay requires --instance and --event")
        result = replay(args.tool, args.event, payload, args.instance, args.binary, args.pi_extension)
        sys.stdout.buffer.write(result.stdout)
        sys.stderr.buffer.write(result.stderr)
        raise SystemExit(result.returncode)


if __name__ == "__main__":
    main()
