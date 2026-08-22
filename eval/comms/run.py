#!/usr/bin/env python3
"""Agent Deck communication reliability matrix (report-only baseline)."""
from __future__ import annotations
import argparse, datetime, html, itertools, json, os, pathlib, shutil, subprocess, tempfile, time

ROOT = pathlib.Path(__file__).resolve().parents[2]
HERE = pathlib.Path(__file__).resolve().parent
HARNESSES = ["claude", "codex", "gemini"]
STATES = ["idle", "mid-turn-busy", "dialog-picker-open", "composer-unsent", "just-restarted", "remote-ssh"]
SHAPES = ["short", "very-long", "multiline", "quotes-escapes-unicode", "rapid-consecutive"]
MODES = ["wait", "stream", "no-wait", "inbox-talkback"]

def run(cmd, env, timeout=8, stdin=None):
    try:
        p = subprocess.run(cmd, text=True, input=stdin, stdout=subprocess.PIPE,
                           stderr=subprocess.STDOUT, env=env, timeout=timeout)
        return p.returncode, p.stdout
    except subprocess.TimeoutExpired as e:
        out = (e.stdout or "") + (e.stderr or "")
        return 124, str(out) + "\nTIMEOUT"

def payload(shape, nonce):
    if shape == "very-long": return nonce + " " + ("long-payload-" * 700)
    if shape == "multiline": return nonce + " line-one\nline-two\nline-three"
    if shape == "quotes-escapes-unicode": return nonce + " 'single' \"double\" \\ $HOME `literal` café 雪 🚀"
    return nonce + " ping"

def capture(binary, env, title):
    rc, out = run([str(binary), "-p", "comms-eval", "session", "output", title, "--pane", "-q"], env, 4)
    return out

def write_reports(results, metrics):
    stamp = datetime.datetime.now(datetime.timezone.utc).isoformat()
    data = {"generated_at": stamp, "dimensions": {"harness": HARNESSES, "state": STATES, "shape": SHAPES, "mode": MODES},
            "counts": metrics, "cells": results}
    (HERE / "matrix.json").write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n")
    lines = ["# Communications reliability matrix", "", f"Generated: `{stamp}`", "",
             "> Baseline/report-only: red cells are expected and are evidence, not skipped tests.", "",
             "| Harness | State | Shape | Mode | Result | Evidence |", "|---|---|---|---|---|---|"]
    for r in results:
        mark = "PASS" if r["pass"] else "FAIL"
        evidence = r["reason"].replace("|", "\\|").replace("\n", " ")[:180]
        lines.append(f'| {r["harness"]} | {r["state"]} | {r["shape"]} | {r["mode"]} | {mark} | {evidence} |')
    lines += ["", "## Weekly counts", "", "```json", json.dumps(metrics, indent=2), "```", "",
              "See [README.md](README.md) for exercised boundaries and [matrix.html](matrix.html) for visual verification."]
    (HERE / "MATRIX.md").write_text("\n".join(lines) + "\n")
    rows = "".join(f'<tr class="{"pass" if r["pass"] else "fail"}"><td>{html.escape(r[k])}</td>' for r in results for k in [])
    rows = "".join("<tr class='%s'>%s</tr>" % ("pass" if r["pass"] else "fail", "".join(f"<td>{html.escape(str(r[k]))}</td>" for k in ("harness","state","shape","mode","reason"))) for r in results)
    (HERE / "matrix.html").write_text("<!doctype html><meta charset=utf-8><title>Comms matrix</title><style>body{font:14px system-ui;margin:2rem}table{border-collapse:collapse}td,th{border:1px solid #aaa;padding:.3rem}.pass{background:#d7f5df}.fail{background:#ffd9d9}</style><h1>Communications reliability matrix</h1><p>Green is confirmed; red is unconfirmed or failed.</p><table><tr><th>Harness</th><th>State</th><th>Shape</th><th>Mode</th><th>Evidence</th></tr>" + rows + "</table>\n")

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--binary", type=pathlib.Path, help="existing agent-deck binary")
    args = ap.parse_args()
    with tempfile.TemporaryDirectory(prefix="agent-deck-comms-") as td:
        tmp = pathlib.Path(td); bindir = tmp / "bin"; bindir.mkdir()
        binary = args.binary or tmp / "agent-deck"
        if not args.binary:
            subprocess.run(["go", "build", "-o", str(binary), "./cmd/agent-deck"], cwd=ROOT, check=True)
        for tool in HARNESSES:
            shutil.copy2(HERE / "fake_harness.py", bindir / tool); (bindir / tool).chmod(0o755)
        socket = "ad-comms-" + str(os.getpid())
        env = os.environ.copy(); env.update({"HOME": str(tmp / "home"), "XDG_CONFIG_HOME": str(tmp / "xdg"),
            "PATH": str(bindir) + os.pathsep + env["PATH"], "COMMS_FAKE_LOG": str(tmp / "deliveries.log"),
            "COMMS_TMUX_SOCKET": socket, "AGENT_DECK_DISABLE_UPDATE_CHECK": "1"})
        pathlib.Path(env["HOME"]).mkdir(); results=[]; seq=0
        try:
            for harness in HARNESSES:
                title = "comms-eval-" + harness
                rc, out = run([str(binary), "-p", "comms-eval", "add", "-t", title, "-c", harness,
                               "--no-parent", "--tmux-socket", socket, "--json", str(ROOT)], env, 15)
                sr, so = run([str(binary), "-p", "comms-eval", "session", "start", title, "--json"], env, 15)
                session_ok = rc == 0 and sr == 0
                time.sleep(.6)
                for state, shape, mode in itertools.product(STATES, SHAPES, MODES):
                    seq += 1; nonce = f"COMMS-{seq:04d}-{harness}"; messages=[payload(shape, nonce)]
                    if shape == "rapid-consecutive": messages=[f"{nonce}-{i} rapid" for i in range(1,4)]
                    prep=[]
                    if state == "just-restarted": prep=[str(binary), "-p", "comms-eval", "session", "restart", title, "--force", "--json"]
                    elif state == "mid-turn-busy": prep=[str(binary), "-p", "comms-eval", "session", "send-keys", title, "--text", "__COMMS_BUSY__", "--json"]
                    elif state == "dialog-picker-open": prep=[str(binary), "-p", "comms-eval", "session", "send-keys", title, "--named-key", "C-p", "--json"]
                    elif state == "composer-unsent": prep=[str(binary), "-p", "comms-eval", "session", "send", title, "operator-draft", "--draft", "--json"]
                    if prep: run(prep, env, 8)
                    outputs=[]; command_ok=session_ok
                    for msg in messages:
                        cmd=[str(binary), "-p", "comms-eval", "session", "send", title, "--message-file", "-", "--json", "--timeout", "2s"]
                        if mode == "wait": cmd += ["--wait"]
                        elif mode == "stream": cmd += ["--stream", "--stream-idle", "1s"]
                        else: cmd += ["--no-wait"]
                        cr, co = run(cmd, env, 3, msg); outputs.append(co); command_ok &= cr == 0
                        if mode == "inbox-talkback": run([str(binary), "-p", "comms-eval", "inbox", "drain", "--json", title], env, 3)
                    time.sleep(.12); pane=capture(binary, env, title)
                    counts=[pane.count(m.split()[0]) for m in messages]
                    exact=all(c == 1 for c in counts); ordered=all(pane.find(messages[i].split()[0]) < pane.find(messages[i+1].split()[0]) for i in range(len(messages)-1))
                    reply_owned=all(("REPLY " + m.split()[0]) in pane or m.split()[0] in "".join(outputs) for m in messages)
                    # A remote state is truthful only when backed by product SSH; this local baseline is intentionally red.
                    state_confirmed = state != "remote-ssh"
                    passed=command_ok and exact and ordered and reply_owned and state_confirmed
                    reason = "confirmed exactly once, ordered, nonce-owned reply" if passed else f"rc_ok={command_ok} counts={counts} ordered={ordered} reply_owned={reply_owned} state_confirmed={state_confirmed}"
                    results.append({"harness":harness,"state":state,"shape":shape,"mode":mode,"pass":passed,"reason":reason,"nonce":nonce})
        finally:
            subprocess.run(["tmux", "-L", socket, "kill-server"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env)
        attempted=len(results); duplicated=sum(any(int(x) > 1 for x in r["reason"].split("counts=")[-1].split("]")[0].strip("[").split(",") if x.strip().isdigit()) for r in results)
        metrics={"attempted":attempted,"unconfirmed":sum(not r["pass"] for r in results),"retried":0,"duplicated":duplicated,
                 "lost":sum("counts=[0" in r["reason"] or "counts=[0," in r["reason"] for r in results),"passed":sum(r["pass"] for r in results),"failed":sum(not r["pass"] for r in results)}
        write_reports(results, metrics)
        print(json.dumps(metrics)); return 0  # report-only: red cells do not fail the workflow yet
if __name__ == "__main__": raise SystemExit(main())
