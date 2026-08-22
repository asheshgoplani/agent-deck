#!/usr/bin/env python3
"""Deterministic interactive stand-in for claude/codex/gemini.

It intentionally implements only terminal input/output: every submitted line is
logged and echoed with its nonce.  Agent-specific transcript and hook protocols
are not forged, so --wait/--stream gaps remain visible in the matrix.
"""
import os
import re
import signal
import sys
import time

tool = os.path.basename(sys.argv[0])
prompt = {"claude": "❯ ", "codex": "› ", "gemini": "> "}.get(tool, "> ")
log_path = os.environ.get("COMMS_FAKE_LOG", "/tmp/agent-deck-comms-fake.log")
draft = os.environ.get("COMMS_FAKE_DRAFT", "")

def render():
    print(prompt + draft, end="", flush=True)

def clear(_sig, _frame):
    global draft
    draft = ""
    print()
    render()

signal.signal(signal.SIGINT, clear)
print(f"COMMS_FAKE_READY tool={tool}")
render()
for raw in sys.stdin:
    line = raw.rstrip("\r\n")
    if line == "__COMMS_BUSY__":
        print("working…")
        time.sleep(0.15)
        print("turn complete")
        render()
        continue
    nonces = re.findall(r"COMMS-[A-Za-z0-9_-]+", line)
    with open(log_path, "a", encoding="utf-8") as fh:
        fh.write(tool + "\t" + line + "\n")
    print("REPLY " + " ".join(nonces) + " :: " + line)
    render()
