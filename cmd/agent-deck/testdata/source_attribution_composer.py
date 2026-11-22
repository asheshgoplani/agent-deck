#!/usr/bin/env python3
"""Record raw input and clear a framed composer on submit; execute nothing."""
import argparse
import json
import uuid
from datetime import datetime, timezone
import os
import select
import sys
import termios
import tty

parser = argparse.ArgumentParser()
parser.add_argument("--capture", required=True)
parser.add_argument("--transcript", required=True)
args = parser.parse_args()
fd = sys.stdin.fileno()
original = termios.tcgetattr(fd)
body = ""
submitted = []


def render():
    rule = "─" * 70
    transcript = "\r\n".join(submitted)
    activity = "\r\n✽ Thinking…\r\nesc to interrupt\r\n" if submitted else ""
    sys.stdout.write("\033[2J\033[H" + transcript + activity + "\r\n" + rule + "\r\n❯ " + body.replace("\n", "\r\n  ") + "\r\n" + rule + "\r\n")
    sys.stdout.flush()


try:
    tty.setraw(fd)
    sys.stdout.write("\033[?2004h")
    with open(args.capture, "ab", buffering=0) as capture:
        render()
        while True:
            if not select.select([fd], [], [], 1)[0]:
                continue
            chunk = os.read(fd, 65536)
            if not chunk:
                break
            capture.write(chunk)
            text = chunk.decode("utf-8")
            text = text.replace("\033[200~", "").replace("\033[201~", "")
            for char in text:
                if char == "\r":
                    record = {"type": "user", "uuid": str(uuid.uuid4()),
                              "timestamp": datetime.now(timezone.utc).isoformat(),
                              "sessionId": "11111111-1111-4111-8111-111111111111",
                              "message": {"role": "user", "content": body}}
                    with open(args.transcript, "a") as transcript:
                        transcript.write(json.dumps(record) + "\n")
                    submitted.append(body.replace("\n", "\r\n"))
                    body = ""
                elif char != "\033":
                    body += char
            render()
finally:
    termios.tcsetattr(fd, termios.TCSANOW, original)
