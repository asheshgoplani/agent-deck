#!/usr/bin/env python3
"""Compress a Claude Code JSONL transcript into a compact markdown timeline.

Keeps: user text, tool_use (name + abbreviated args), tool errors, assistant text.
Drops: permission-mode, queue-operation, attachment, file-history-snapshot,
       ai-title, pr-link, last-prompt, system, non-error tool_results.

Usage:
    python distill.py <input.jsonl> <output.md>
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

NOISE_TYPES = {
    "permission-mode",
    "queue-operation",
    "attachment",
    "file-history-snapshot",
    "ai-title",
    "pr-link",
    "system",
}

SKILL_LOAD_MARKER = "Base directory for this skill:"

ARG_PREVIEW_CHARS = 200
ASSIST_PREVIEW_CHARS = 500
USER_PREVIEW_CHARS = 1000


def short_uuid(u: str | None) -> str:
    return (u or "????????")[:8]


def text_of(content) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        chunks = []
        for c in content:
            if isinstance(c, dict) and c.get("type") == "text":
                chunks.append(c.get("text", ""))
        return "\n".join(chunks)
    return ""


def clip(s: str, n: int) -> str:
    s = s.replace("\n", " ").strip()
    return s if len(s) <= n else s[:n].rstrip() + "…"


def abbreviate_args(inp) -> str:
    if isinstance(inp, dict):
        # Bash: prefer command
        if "command" in inp:
            return clip(str(inp["command"]), ARG_PREVIEW_CHARS)
        # File ops: prefer file_path
        if "file_path" in inp:
            extra = ""
            if "pattern" in inp:
                extra = f" pattern={clip(str(inp['pattern']), 60)}"
            return f"{inp['file_path']}{extra}"
        # Otherwise: compact JSON
        return clip(json.dumps(inp, ensure_ascii=False), ARG_PREVIEW_CHARS)
    return clip(str(inp), ARG_PREVIEW_CHARS)


def is_heartbeat(text: str) -> bool:
    t = text.strip()
    return t.startswith("[HEARTBEAT]") or t.startswith("[EVENT]")


def extract_skill_name(text: str) -> str | None:
    """Detect a 'skill load' user-message and return just the skill identifier."""
    if SKILL_LOAD_MARKER not in text:
        return None
    # Path follows the marker on the same line; skill name is its last segment.
    after = text.split(SKILL_LOAD_MARKER, 1)[1].strip()
    first_line = after.split("\n", 1)[0].strip()
    if first_line:
        # last path segment, strip trailing slashes
        seg = first_line.rstrip("/").rsplit("/", 1)[-1]
        return seg or first_line
    return "unknown"


def distill(path: Path, out: Path) -> dict:
    stats = {
        "lines": 0,
        "kept_user": 0,
        "kept_tool_use": 0,
        "kept_error": 0,
        "kept_assist": 0,
        "kept_heartbeat": 0,
        "kept_skill_load": 0,
        "kept_prompt": 0,
        "dropped_noise": 0,
    }
    out_lines: list[str] = []
    src_bytes = path.stat().st_size

    with path.open("r", encoding="utf-8") as f:
        for line in f:
            stats["lines"] += 1
            try:
                d = json.loads(line)
            except json.JSONDecodeError:
                continue

            t = d.get("type")
            if t in NOISE_TYPES:
                stats["dropped_noise"] += 1
                continue

            uid = short_uuid(d.get("uuid"))
            ts = d.get("timestamp", "")[:19]  # YYYY-MM-DDTHH:MM:SS

            if t == "last-prompt":
                txt = d.get("lastPrompt", "")
                if txt:
                    out_lines.append(
                        f"[{uid}] {ts} PROMPT: {clip(txt, USER_PREVIEW_CHARS)}"
                    )
                    stats["kept_prompt"] += 1
                continue

            msg = d.get("message")
            if t == "user":
                # user message: could be text or could be tool_result wrapped in content list
                if isinstance(msg, dict):
                    content = msg.get("content")
                    if isinstance(content, list):
                        for c in content:
                            if not isinstance(c, dict):
                                continue
                            ctype = c.get("type")
                            if ctype == "tool_result":
                                if c.get("is_error"):
                                    err = text_of(c.get("content", ""))
                                    out_lines.append(
                                        f"[{uid}] {ts} ERROR: {clip(err, 400)}"
                                    )
                                    stats["kept_error"] += 1
                                # silently drop non-error tool_results
                            elif ctype == "text":
                                txt = c.get("text", "")
                                skill = extract_skill_name(txt)
                                if skill:
                                    out_lines.append(
                                        f"[{uid}] {ts} SKILL_LOAD: {skill}"
                                    )
                                    stats["kept_skill_load"] += 1
                                elif is_heartbeat(txt):
                                    out_lines.append(
                                        f"[{uid}] {ts} HEARTBEAT"
                                    )
                                    stats["kept_heartbeat"] += 1
                                else:
                                    out_lines.append(
                                        f"[{uid}] {ts} USER: {clip(txt, USER_PREVIEW_CHARS)}"
                                    )
                                    stats["kept_user"] += 1
                    elif isinstance(content, str):
                        skill = extract_skill_name(content)
                        if skill:
                            out_lines.append(
                                f"[{uid}] {ts} SKILL_LOAD: {skill}"
                            )
                            stats["kept_skill_load"] += 1
                        elif is_heartbeat(content):
                            out_lines.append(f"[{uid}] {ts} HEARTBEAT")
                            stats["kept_heartbeat"] += 1
                        else:
                            out_lines.append(
                                f"[{uid}] {ts} USER: {clip(content, USER_PREVIEW_CHARS)}"
                            )
                            stats["kept_user"] += 1
            elif t == "assistant":
                if isinstance(msg, dict):
                    content = msg.get("content")
                    if isinstance(content, list):
                        for c in content:
                            if not isinstance(c, dict):
                                continue
                            ctype = c.get("type")
                            if ctype == "tool_use":
                                name = c.get("name", "?")
                                args = abbreviate_args(c.get("input"))
                                out_lines.append(
                                    f"[{uid}] {ts} TOOL: {name} | {args}"
                                )
                                stats["kept_tool_use"] += 1
                            elif ctype == "text":
                                txt = c.get("text", "")
                                if txt.strip():
                                    out_lines.append(
                                        f"[{uid}] {ts} ASSIST: {clip(txt, ASSIST_PREVIEW_CHARS)}"
                                    )
                                    stats["kept_assist"] += 1
            # everything else (unknown types): drop silently

    # Header
    header = [
        f"# Distilled transcript",
        f"",
        f"- Source: `{path}`",
        f"- Source size: {src_bytes:,} bytes",
        f"- Source lines: {stats['lines']:,}",
        f"- Kept: prompt={stats['kept_prompt']} user={stats['kept_user']} "
        f"tool={stats['kept_tool_use']} error={stats['kept_error']} "
        f"assist={stats['kept_assist']} heartbeat={stats['kept_heartbeat']} "
        f"skill_load={stats['kept_skill_load']}",
        f"- Dropped noise events: {stats['dropped_noise']:,}",
        f"",
        f"---",
        f"",
    ]
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text("\n".join(header + out_lines) + "\n", encoding="utf-8")

    dst_bytes = out.stat().st_size
    stats["src_bytes"] = src_bytes
    stats["dst_bytes"] = dst_bytes
    stats["compression"] = round(src_bytes / dst_bytes, 1) if dst_bytes else 0
    return stats


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    src = Path(sys.argv[1])
    dst = Path(sys.argv[2])
    if not src.exists():
        print(f"Input not found: {src}", file=sys.stderr)
        return 1
    stats = distill(src, dst)
    print(json.dumps(stats, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
