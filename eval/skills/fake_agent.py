#!/usr/bin/env python3
"""Deterministic, offline agent: choose a command from wording found in the skill."""
import os, pathlib, re, shlex, subprocess, sys

if not pathlib.Path("/.dockerenv").exists() or os.environ.get("HOME") != "/throwaway-home":
    raise SystemExit("refusing to run outside the per-case container and throwaway HOME")

prompt, expected = sys.argv[1], sys.argv[2]
skill = open("/skill/SKILL.md", encoding="utf-8").read()
refs = "\n".join(open(p, encoding="utf-8").read() for p in ["/skill/references/cli-reference.md"])
text = skill + refs

rules = [
    (r"cheap machine-readable.*status", "agent-deck status --json"),
    (r"Find the session", "agent-deck session search 'Eval Project'"),
    (r"latest output", "agent-deck session output 'Eval Project'"),
    (r"Drain pending inbox", "agent-deck inbox drain --until-done"),
    (r"detach key", "agent-deck session attach 'Eval Project'"),
]
command = expected
for pattern, candidate in rules:
    if re.search(pattern, prompt, re.I):
        command = candidate
        break

# A command is only usable without guessing if its command phrase appears in the supplied skill.
phrase = " ".join(shlex.split(command)[:3])
clear = phrase.replace("agent-deck ", "") in text
run = command if clear else "agent-deck --help"
proc = subprocess.run(run, shell=True, text=True, capture_output=True, timeout=20)
print(run)
print(proc.stdout, end="")
print(proc.stderr, end="", file=sys.stderr)
sys.exit(0 if clear and run == expected and proc.returncode == 0 else 1)
