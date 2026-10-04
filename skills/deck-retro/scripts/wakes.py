#!/usr/bin/env python3
"""Read-only analysis of agent-deck conductor transcripts (last 7 days).

Streams each jsonl line by line, builds "wakes" (one non-tool_result user trigger
plus all assistant/tool turns until the next trigger), classifies triggers, and
dumps per-wake records to wakes_<conductor>.json plus a sends/telegram dump.
"""
import json, re, sys, os, collections, datetime, difflib

W0 = None
W1 = None

def in_window(ts):
    ts = re.sub(r"\.(\d+)(?=[+-]|Z|$)", lambda match: "." + (match.group(1) + "000000")[:6], ts)
    value = datetime.datetime.fromisoformat(ts.replace("Z", "+00:00"))
    return W0 <= value < W1

ID_RE = re.compile(r"([0-9a-f]{8})-(17|18)\d{8}")

def text_of(content):
    if isinstance(content, str):
        return content
    out = []
    for b in content or []:
        if isinstance(b, dict) and b.get("type") == "text":
            out.append(b.get("text", ""))
    return "\n".join(out)

def tool_result_text(b):
    c = b.get("content")
    if isinstance(c, str):
        return c
    return "\n".join(x.get("text", "") for x in (c or []) if isinstance(x, dict))

def strip_paste(s):
    return re.sub(r'^\s*(\[Image #\d+\]\s*)*<pasted_content id="[^"]*">\s*', "", s)

CHILD_SEND_RE = re.compile(r"^\[(?:worker|child|agent)[^]]*\]", re.I)

def classify(text, promptSource):
    s = text.lstrip()
    if s.startswith("Stop hook feedback"):
        return "stop-hook (child completions)"
    if s.startswith('<channel source="plugin:telegram'):
        return "human-telegram"
    if s.startswith("<task-notification>"):
        return "bg-task-notification"
    if s.startswith("<command-message>loop") or "<command-name>/loop" in s[:200] or s.startswith("# /loop") or re.match(r"\[\d+ prior /loop", s):
        return "timer (/loop ScheduleWakeup)"
    if (s.startswith("/compact") or s.startswith("<command-name>/") or s.startswith("<local-command")
            or s.startswith("This session is being continued") or s.startswith("Context was just compacted")
            or s.startswith("[Request interrupted")):
        return "system (compact/local-cmd)"
    if s.startswith("[HEARTBEAT]") or "heartbeat" in s[:40].lower() or s.startswith("[STATUS]"):
        return "heartbeat"
    if s.startswith("[INBOX] A child just committed"):
        return "child-inbox doorbell (generic)"
    if s.startswith("[INBOX]") or s.startswith("[DONE]") or "===AGENTDECK_DONE===" in s or s.startswith("[child"):
        return "child-inbox urgent (with summary)"
    s2 = strip_paste(s)
    if re.match(r"\[peer[^]]*\]", s2, re.I) or "From conductor-" in s2[:80]:
        return "peer-conductor relay"
    if CHILD_SEND_RE.match(s2):
        return "child direct send"
    if promptSource in ("typed", "queued"):
        return "human-typed (deck/tmux)"
    return "unknown"

NOTHING = ["nothing to act on", "no change", "all clear", "no action", "nothing new", "still running",
           "no news", "quiet", "noop", "nothing pending"]
EXTRA_NOTHING = ["nothing actionable", "no new", "unchanged", "still waiting", "nothing else", "no-op"]

SEND_RE = re.compile(r"agent-deck(?:\s+-p\s+\S+)?\s+session\s+send\s+(?:(?:--\S+(?:\s+(?!\")\S+)?|-q)\s+)*([\"']?[^\s\"']+[\"']?)")
REMOTE_SEND_RE = re.compile(r"agent-deck\s+remote\s+\S+\s+send\s+(?:--\S+\s+)*([^\s\"']+)")
LAUNCH_RE = re.compile(r"agent-deck(?:\s+-p\s+\S+)?\s+launch\s+\S+.*?-t\s+[\"']?([^\"'\s]+)", re.S)
LAUNCH_SCRIPT_RE = re.compile(r"launch-(?:codex|work)\.sh\s+\S+\s+([^\s;|&]+)")
START_M_RE = re.compile(r"agent-deck(?:\s+-p\s+\S+)?\s+session\s+start\s+([^\s]+)[^\n|;&]*\s-m\s")
TMUX_TYPE_RE = re.compile(r"tmux[^\n]*send-keys[^\n]*\s-l\s")
MSG_RE = re.compile(r"session\s+send\s+.*?\"((?:[^\"\\]|\\.){10,})\"", re.S)

def send_events(cmd):
    ev = []
    for m in SEND_RE.finditer(cmd):
        tgt = m.group(1).strip("\"'")
        if tgt.startswith("$"):
            tgt = "<var>" + tgt
        ev.append(("send", tgt))
    for m in REMOTE_SEND_RE.finditer(cmd):
        ev.append(("remote-send", m.group(1)))
    for m in LAUNCH_RE.finditer(cmd):
        ev.append(("launch", m.group(1)))
    for m in LAUNCH_SCRIPT_RE.finditer(cmd):
        ev.append(("launch", m.group(1).strip("\"'")))
    for m in START_M_RE.finditer(cmd):
        ev.append(("start -m", m.group(1)))
    if TMUX_TYPE_RE.search(cmd):
        ev.append(("tmux send-keys -l", "<pane>"))
    return ev

def outcome(res):
    r = res.strip()
    low = r.lower()
    if "did not complete within" in low:
        return "timeout->backgrounded"
    if "message not delivered" in low or "composer is occupied" in low:
        return "NOT delivered (composer occupied)"
    if "blocked by destructive-guard" in low:
        return "blocked by hook"
    if "error:" in low or "failed" in low or "traceback" in low or "typeerror" in low:
        return "error"
    if "queued session" in low:
        return "queued (group cap)"
    if "launched session" in low:
        return "launched (message sent)"
    if "submitted" in low or "delivered" in low or re.search(r"(^|\n)sent\b", low) or "message sent" in low:
        return "sent/delivered"
    if r == "" or r == "(Bash completed with no output)" or re.fullmatch(r"(Shell cwd was reset to \S+|Session cwd remains.*)", r):
        return "no output (--no-wait -q / tail)"
    return "other output (chained cmds)"

def usage_add(acc, u):
    for k in ("input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"):
        acc[k] = acc.get(k, 0) + (u.get(k) or 0)

def new_wake(entry, text, cls, acct):
    return dict(start=entry["timestamp"], end=entry["timestamp"], trigger=text, cls=cls, acct=acct,
                tools=0, tool_names=collections.Counter(), msg_usage={}, n_assistant=0, last_text="",
                sends=[], telegram=[], tasklog=0, drains=0, children=0, outputs=0, show=0, capture=0,
                absorbed=[], models=collections.Counter(), first_ctx=None, last_ctx=None,
                promptSource=entry.get("promptSource"))

def analyze(name, path):
    wakes = []
    cur = None
    pending_tools = {}       # tool_use_id -> (kind, payload)
    owner = None
    reactions = []           # (ts, text) evidence of child activity reaching the conductor
    compacts = []
    turn_durations = []
    queue_ops = collections.Counter()
    hook_block = 0
    source_errors = 0
    source_timestamped = 0
    source_first = None
    source_last = None
    with open(path, "rb") as fh:
        for line in fh:
            if b'"bridge-session"' in line:
                try:
                    d = json.loads(line)
                    if d.get("type") == "bridge-session":
                        owner = d.get("ownerAccountUuid")
                        continue
                except Exception:
                    pass
            try:
                d = json.loads(line)
            except Exception:
                source_errors += 1
                continue
            ts = d.get("timestamp")
            if not ts:
                continue
            try:
                included = in_window(ts)
            except (ValueError, TypeError):
                source_errors += 1
                continue
            source_timestamped += 1
            source_first = min(source_first, ts) if source_first else ts
            source_last = max(source_last, ts) if source_last else ts
            if not included:
                continue
            t = d.get("type")
            acct = "unattributed"
            if t == "system":
                st = d.get("subtype")
                if st == "compact_boundary":
                    compacts.append((ts, (d.get("compactMetadata") or {}).get("preTokens"), (d.get("compactMetadata") or {}).get("trigger")))
                elif st == "turn_duration":
                    turn_durations.append(d.get("durationMs") or 0)
                if cur:
                    cur["end"] = max(cur["end"], ts)
                continue
            if t == "queue-operation":
                queue_ops[(d.get("operation"), d.get("reason"))] += 1
                continue
            if t == "attachment":
                a = d.get("attachment") or {}
                if a.get("type") == "queued_command" and cur is not None:
                    p = a.get("prompt")
                    p = p if isinstance(p, str) else text_of(p)
                    cur["absorbed"].append((ts, classify(p, "queued"), p[:300]))
                    reactions.append((ts, p))
                elif a.get("type") == "hook_blocking_error":
                    hook_block += 1
                    be = (a.get("blockingError") or {}).get("blockingError", "")
                    reactions.append((ts, be))
                continue
            if t not in ("user", "assistant"):
                continue
            msg = d.get("message") or {}
            content = msg.get("content")
            if t == "user":
                is_tr = isinstance(content, list) and any(isinstance(b, dict) and b.get("type") == "tool_result" for b in content)
                if is_tr:
                    for b in content:
                        if isinstance(b, dict) and b.get("type") == "tool_result":
                            pt = pending_tools.pop(b.get("tool_use_id"), None)
                            if pt:
                                kind, payload = pt
                                res = tool_result_text(b)
                                if kind == "send":
                                    for s in payload:
                                        s["outcome"] = outcome(res)
                                        s["result"] = res[:300]
                                elif kind == "drain":
                                    reactions.append((ts, res))
                    if cur:
                        cur["end"] = max(cur["end"], ts)
                    continue
                txt = text_of(content)
                if d.get("isMeta") and not (txt.lstrip().startswith("Stop hook feedback") or txt.lstrip().startswith("<channel source")):
                    # skill bodies, image captions, /loop skill expansion: part of current wake
                    if cur:
                        cur["trigger"] += "\n" + txt[:2000] if cur["n_assistant"] == 0 else ""
                        cur["end"] = max(cur["end"], ts)
                    continue
                if cur is not None and cur["n_assistant"] == 0:
                    cur["trigger"] += "\n" + txt
                    cur["end"] = max(cur["end"], ts)
                    continue
                if cur is not None:
                    wakes.append(cur)
                cls = classify(txt, d.get("promptSource"))
                cur = new_wake(d, txt, cls, acct)
                reactions.append((ts, txt))
                continue
            # assistant
            if cur is None:
                cur = new_wake(d, "", "unknown (pre-window continuation)", acct)
            cur["end"] = max(cur["end"], ts)
            cur["n_assistant"] += 1
            mid = msg.get("id") or d.get("uuid")
            model = msg.get("model")
            u = msg.get("usage") or {}
            if model and model != "<synthetic>":
                prev = cur["msg_usage"].get(mid)
                if prev is None:
                    cur["models"][model] += 1
                cur["msg_usage"][mid] = {k: max((prev or {}).get(k, 0), u.get(k) or 0) for k in
                                         ("input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens")}
                ctx = (u.get("input_tokens") or 0) + (u.get("cache_read_input_tokens") or 0) + (u.get("cache_creation_input_tokens") or 0)
                if cur["first_ctx"] is None:
                    cur["first_ctx"] = ctx
                cur["last_ctx"] = ctx
            for b in content or []:
                if not isinstance(b, dict):
                    continue
                if b.get("type") == "text" and b.get("text", "").strip():
                    cur["last_text"] = b["text"]
                elif b.get("type") == "tool_use":
                    cur["tools"] += 1
                    nm = b.get("name")
                    cur["tool_names"][nm] += 1
                    inp = b.get("input") or {}
                    if nm == "Bash":
                        cmd = inp.get("command", "")
                        ev = send_events(cmd)
                        if ev:
                            mm = MSG_RE.search(cmd)
                            mtxt = mm.group(1) if mm else ""
                            recs = [dict(ts=ts, kind=k, target=tg, text=mtxt[:400], cmd=cmd[:300], outcome="no tool_result", acct=acct) for k, tg in ev]
                            cur["sends"].extend(recs)
                            pending_tools[b.get("id")] = ("send", recs)
                        if "inbox drain" in cmd:
                            cur["drains"] += 1
                            if not ev:
                                pending_tools[b.get("id")] = ("drain", None)
                        if "session children" in cmd:
                            cur["children"] += 1
                        if "session output" in cmd:
                            cur["outputs"] += 1
                        if "session show" in cmd:
                            cur["show"] += 1
                        if "tmux" in cmd and "capture-pane" in cmd:
                            cur["capture"] += 1
                        if "task-log.md" in cmd and (">>" in cmd or "tee -a" in cmd):
                            cur["tasklog"] += 1
                        if re.search(r"api\.telegram|send-telegram|telegram-send", cmd):
                            cur["telegram"].append(dict(ts=ts, via="bash", text=cmd[:300]))
                    elif nm in ("Write", "Edit") and "task-log.md" in (inp.get("file_path") or ""):
                        cur["tasklog"] += 1
                    elif nm and "telegram" in nm and nm.endswith("reply"):
                        cur["telegram"].append(dict(ts=ts, via=nm, text=(inp.get("text") or "")))
    if cur is not None:
        wakes.append(cur)
    for w in wakes:
        tot = {}
        for u in w["msg_usage"].values():
            usage_add(tot, u)
        w["usage"] = tot
        w["api_calls"] = len(w["msg_usage"])
        del w["msg_usage"]
        w["tool_names"] = dict(w["tool_names"])
        w["models"] = dict(w["models"])
    meta = dict(source_errors=source_errors, source_timestamped=source_timestamped, source_first=source_first, source_last=source_last, compacts=compacts, turn_duration_ms_sum=sum(turn_durations), turn_count=len(turn_durations),
                queue_ops={f"{k[0]}:{k[1]}": v for k, v in queue_ops.items()}, hook_blocking_errors=hook_block)
    return wakes, reactions, meta
