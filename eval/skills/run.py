#!/usr/bin/env python3
"""Container-only skill eval runner. Host entrypoint is run.sh."""
import argparse, concurrent.futures, json, os, pathlib, subprocess, time
from cases import CASES

ROOT = pathlib.Path(__file__).resolve().parents[2]
OUT = ROOT / "eval/skills/RESULTS.json"

def tokens(s):
    return (len(s) + 3) // 4

def one(case, image, binary, skill):
    cid, prompt, expected, cheap = case
    started = time.monotonic()
    cmd = ["docker", "run", "--rm", "--network", "none", "--read-only",
           "--tmpfs", "/tmp", "--tmpfs", "/throwaway-home", "-e", "HOME=/throwaway-home",
           "-v", f"{binary}:/usr/local/bin/agent-deck:ro", "-v", f"{skill}:/skill:ro",
           "-v", f"{ROOT / 'eval/skills/fake_agent.py'}:/runner/fake_agent.py:ro",
           image, "python3", "/runner/fake_agent.py", prompt, expected]
    p = subprocess.run(cmd, text=True, capture_output=True)
    wall = round(time.monotonic() - started, 3)
    commands = p.stdout.splitlines()[:1]
    consumed = pathlib.Path(skill / "SKILL.md").read_text() + pathlib.Path(skill / "references/cli-reference.md").read_text() + prompt + p.stdout + p.stderr
    return {"id": cid, "passed": p.returncode == 0, "cheap_path": bool(commands and commands[0] == expected),
            "expected_command": expected, "commands": commands, "tokens": tokens(consumed),
            "wall_seconds": wall, "failure": "" if p.returncode == 0 else "skill phrase missing, wrong command, or command failed"}

def help_commands(binary, image):
    p = subprocess.run(["docker", "run", "--rm", "--network", "none", "-v", f"{binary}:/usr/local/bin/agent-deck:ro", image, "agent-deck", "--help"], text=True, capture_output=True)
    return set(__import__('re').findall(r"^  ([a-z][a-z0-9-]+)\s", p.stdout, __import__('re').M))

def main():
    if not pathlib.Path("/.dockerenv").exists() and os.environ.get("SKILL_EVAL_HOST_LAUNCHER") != "1":
        raise SystemExit("refusing direct host execution: use eval/skills/run.sh")
    ap = argparse.ArgumentParser(); ap.add_argument("--jobs", type=int, default=4); ap.add_argument("--image", default="agent-deck-go-test:latest"); ap.add_argument("--binary", required=True); ap.add_argument("--skill", required=True)
    a = ap.parse_args()
    with concurrent.futures.ThreadPoolExecutor(max_workers=a.jobs) as ex:
        rows = list(ex.map(lambda c: one(c, a.image, pathlib.Path(a.binary), pathlib.Path(a.skill)), CASES))
    documented = {c[2].split()[1] for c in CASES if len(c[2].split()) > 1}
    real = help_commands(a.binary, a.image)
    dm, md = sorted(documented-real), sorted(real-documented)
    result = {"harness": "deterministic fake agent; no model/network access", "cases": rows,
              "correctness_percent": round(100*sum(r["passed"] for r in rows)/len(rows), 2),
              "cheap_path_percent": round(100*sum(r["cheap_path"] for r in rows)/len(rows), 2),
              "drift": {"documented_but_missing": dm, "missing_but_documented": md, "defects": len(dm)+len(md), "score": max(0, 100-len(dm)-len(md))}}
    OUT.write_text(json.dumps(result, indent=2)+"\n")
    lines = ["# Skill eval baseline", "", f"Correctness: **{result['correctness_percent']}%** · Cheap path: **{result['cheap_path_percent']}%** · Drift score: **{result['drift']['score']}**", "", "| Case | Pass | Cheap | Tokens | Seconds |", "|---|---:|---:|---:|---:|"]
    lines += [f"| {r['id']} | {'yes' if r['passed'] else 'no'} | {'yes' if r['cheap_path'] else 'no'} | {r['tokens']} | {r['wall_seconds']:.3f} |" for r in rows]
    (OUT.parent/"RESULTS.md").write_text("\n".join(lines)+"\n")

if __name__ == "__main__": main()
