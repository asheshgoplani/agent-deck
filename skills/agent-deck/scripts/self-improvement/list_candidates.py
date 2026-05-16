#!/usr/bin/env python3
"""Extract filable bug candidates from a FINDINGS.md.

Reads FINDINGS.md, parses the "Bug-filing shortlist" table + "Recurring issues"
and "Single-occurrence issues" sections, optionally cross-references with the
analysis manifest (already-filed) and `gh issue list` (already exists).

Outputs a ranked JSON list to stdout (or to --out if specified).

Usage:
    python list_candidates.py --findings <path> [--manifest <path>] [--gh-repo <owner/repo>] [--out <path>]
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path


SEV_RANK = {"🔴": 0, "🟡": 1, "🟢": 2}


def slugify(title: str) -> str:
    s = re.sub(r"[^a-zA-Z0-9\s-]", "", title.lower())
    s = re.sub(r"\s+", "-", s.strip())
    return s[:60]


def parse_shortlist(text: str) -> list[dict]:
    """Pull the 'Bug-filing shortlist' table at the end of FINDINGS.md.

    Expected rows like:
      | 🔴 1 | <title> | <source> |
    """
    out: list[dict] = []
    in_table = False
    for line in text.splitlines():
        if "Bug-filing shortlist" in line or "## Bug-filing shortlist" in line:
            in_table = True
            continue
        if in_table:
            line = line.strip()
            if not line:
                continue
            if not line.startswith("|"):
                # ended
                if out:
                    break
                continue
            cells = [c.strip() for c in line.strip("|").split("|")]
            if len(cells) < 3:
                continue
            # skip header / separator rows
            if cells[0].lower() == "priority" or set(cells[0]) <= {"-", " "}:
                continue
            sev = ""
            for marker in ("🔴", "🟡", "🟢"):
                if marker in cells[0]:
                    sev = marker
                    break
            title = cells[1]
            source = cells[2] if len(cells) > 2 else ""
            if not title:
                continue
            out.append({
                "fingerprint": slugify(title),
                "title": title,
                "severity": sev or "🟡",
                "source_citation": source,
                "from_section": "shortlist",
            })
    return out


def parse_recurring(text: str) -> list[dict]:
    """Pull 'Recurring issues (seen in ≥2 reports)' bullets."""
    out: list[dict] = []
    section_re = re.compile(r"^## Recurring issues", re.M)
    m = section_re.search(text)
    if not m:
        return out
    rest = text[m.end():]
    end = re.search(r"^## ", rest, re.M)
    block = rest[: end.start()] if end else rest
    # parse each issue bullet
    issues = re.split(r"\n-\s+\*\*Issue:\*\*", block)
    for chunk in issues[1:]:
        title_line = chunk.splitlines()[0].strip()
        freq_m = re.search(r"\*\*Frequency:\*\*\s*(\d+)\s*reports?", chunk)
        sev_m = re.search(r"\*\*Severity guess:\*\*\s*(\S+)", chunk)
        surface_m = re.search(r"\*\*Surface affected:\*\*\s*(.+)", chunk)
        frequency = int(freq_m.group(1)) if freq_m else 1
        severity_text = sev_m.group(1).lower() if sev_m else "friction"
        sev_emoji = "🔴" if "bug" in severity_text else ("🟡" if "friction" in severity_text else "🟢")
        out.append({
            "fingerprint": slugify(title_line),
            "title": title_line.rstrip("."),
            "severity": sev_emoji,
            "frequency": frequency,
            "surface": (surface_m.group(1).strip() if surface_m else ""),
            "from_section": "recurring",
        })
    return out


def parse_single_occurrence(text: str) -> list[dict]:
    """Pull 'Single-occurrence issues worth investigating' bullets."""
    out: list[dict] = []
    section_re = re.compile(r"^## Single-occurrence issues", re.M)
    m = section_re.search(text)
    if not m:
        return out
    rest = text[m.end():]
    end = re.search(r"^## ", rest, re.M)
    block = rest[: end.start()] if end else rest
    issues = re.split(r"\n-\s+\*\*Issue:\*\*", block)
    for chunk in issues[1:]:
        title_line = chunk.splitlines()[0].strip()
        sev_m = re.search(r"\*\*Severity guess:\*\*\s*(\S+)", chunk)
        severity_text = sev_m.group(1).lower() if sev_m else "friction"
        sev_emoji = "🔴" if "bug" in severity_text else ("🟡" if "friction" in severity_text else "🟢")
        out.append({
            "fingerprint": slugify(title_line),
            "title": title_line.rstrip("."),
            "severity": sev_emoji,
            "frequency": 1,
            "from_section": "single",
        })
    return out


def fetch_gh_titles(repo: str) -> list[str]:
    """List existing GH issues' titles (open + closed). Empty list on any error."""
    try:
        r = subprocess.run(
            ["gh", "issue", "list", "-R", repo, "--state", "all",
             "--limit", "500", "--json", "title,number"],
            capture_output=True, text=True, timeout=30,
        )
        if r.returncode != 0:
            return []
        data = json.loads(r.stdout)
        return [item["title"] for item in data]
    except (subprocess.SubprocessError, json.JSONDecodeError, FileNotFoundError):
        return []


def fuzzy_match(title: str, existing: list[str]) -> str | None:
    """Naive token overlap. Returns the existing title if >=50% tokens overlap."""
    t = set(re.findall(r"[a-z0-9]+", title.lower()))
    if not t:
        return None
    for ex in existing:
        e = set(re.findall(r"[a-z0-9]+", ex.lower()))
        if not e:
            continue
        overlap = len(t & e) / max(len(t), len(e))
        if overlap >= 0.5:
            return ex
    return None


def load_filed_fingerprints(manifest_path: Path) -> set[str]:
    if not manifest_path.exists():
        return set()
    try:
        m = json.loads(manifest_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return set()
    filed = set()
    for entry in m.get("filed_issues", []):
        fp = entry.get("fingerprint")
        if fp:
            filed.add(fp)
    return filed


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--findings", type=Path, required=True)
    ap.add_argument("--manifest", type=Path)
    ap.add_argument("--gh-repo", default="asheshgoplani/agent-deck")
    ap.add_argument("--out", type=Path)
    args = ap.parse_args()

    text = args.findings.read_text(encoding="utf-8")

    candidates = []
    # Shortlist first (curated), then recurring, then single-occurrence
    candidates.extend(parse_shortlist(text))
    seen_fp = {c["fingerprint"] for c in candidates}
    for src in (parse_recurring(text), parse_single_occurrence(text)):
        for c in src:
            if c["fingerprint"] in seen_fp:
                # promote shortlist version: merge frequency if newer is higher
                for existing in candidates:
                    if existing["fingerprint"] == c["fingerprint"]:
                        if c.get("frequency", 0) > existing.get("frequency", 0):
                            existing["frequency"] = c["frequency"]
                continue
            candidates.append(c)
            seen_fp.add(c["fingerprint"])

    # Cross-reference with manifest (already filed)
    filed = load_filed_fingerprints(args.manifest) if args.manifest else set()

    # Cross-reference with GH (already exists)
    existing_titles = fetch_gh_titles(args.gh_repo) if args.gh_repo else []

    for c in candidates:
        c["already_filed_by_us"] = c["fingerprint"] in filed
        match = fuzzy_match(c["title"], existing_titles) if existing_titles else None
        c["existing_gh_match"] = match  # may be None
        c["frequency"] = c.get("frequency", 1)

    # Rank: severity asc → frequency desc → already-filed last
    def rank_key(c: dict):
        return (
            1 if c["already_filed_by_us"] else 0,
            1 if c["existing_gh_match"] else 0,
            SEV_RANK.get(c["severity"], 3),
            -c["frequency"],
        )

    candidates.sort(key=rank_key)

    result = {
        "total": len(candidates),
        "filed_in_manifest": sum(1 for c in candidates if c["already_filed_by_us"]),
        "exists_on_gh": sum(1 for c in candidates if c["existing_gh_match"]),
        "candidates": candidates,
    }

    out_str = json.dumps(result, indent=2, ensure_ascii=False)
    if args.out:
        args.out.write_text(out_str + "\n", encoding="utf-8")
        print(f"Wrote {len(candidates)} candidates to {args.out}")
    else:
        print(out_str)
    return 0


if __name__ == "__main__":
    sys.exit(main())
