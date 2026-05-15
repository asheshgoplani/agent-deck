#!/usr/bin/env python3
"""File one GitHub issue from a prepared issue body file.

Parses a body file with the format produced by issue-drafter.md:
    # TITLE
    <title>
    # LABELS
    <comma-separated labels>
    # BODY
    <multi-line body>

Then runs `gh issue create -R <repo> --title ... --label ... --body-file <body>`.
Updates the manifest with the filing record.

Usage:
    python file_issue.py <body-file> --repo <owner/repo> --fingerprint <slug> \
                        [--manifest <path>] [--dry-run]
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path


def parse_body_file(path: Path) -> tuple[str, list[str], str]:
    """Return (title, labels, body)."""
    text = path.read_text(encoding="utf-8")
    sections = re.split(r"^# (TITLE|LABELS|BODY)\s*$", text, flags=re.M)
    # sections is [pre, "TITLE", title_content, "LABELS", labels_content, "BODY", body_content]
    parsed = {}
    i = 1
    while i < len(sections):
        key = sections[i]
        val = sections[i + 1] if i + 1 < len(sections) else ""
        parsed[key] = val.strip()
        i += 2
    title = parsed.get("TITLE", "").strip().splitlines()[0] if parsed.get("TITLE") else ""
    labels = [l.strip() for l in parsed.get("LABELS", "").split(",") if l.strip()]
    body = parsed.get("BODY", "").strip()
    if not title:
        raise ValueError(f"No # TITLE section found in {path}")
    if not body:
        raise ValueError(f"No # BODY section found in {path}")
    return title, labels, body


def fetch_valid_labels(repo: str) -> set[str]:
    """Return the set of labels that exist on the repo. Empty set on error."""
    try:
        r = subprocess.run(
            ["gh", "label", "list", "-R", repo, "--limit", "200", "--json", "name"],
            capture_output=True, text=True, timeout=30,
        )
        if r.returncode != 0:
            return set()
        return {item["name"] for item in json.loads(r.stdout)}
    except (subprocess.SubprocessError, json.JSONDecodeError, FileNotFoundError):
        return set()


def run_gh(repo: str, title: str, labels: list[str], body: str, dry_run: bool) -> dict:
    """Returns {'url': ..., 'number': ..., 'dropped_labels': [...]} or {'dry_run': True}."""
    # Filter labels against what actually exists in the repo
    valid = fetch_valid_labels(repo)
    if valid:
        kept = [l for l in labels if l in valid]
        dropped = [l for l in labels if l not in valid]
        if dropped:
            print(f"[label filter] dropped (not in repo): {dropped}")
            print(f"[label filter] keeping:                {kept}")
        labels = kept

    with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False) as fh:
        fh.write(body)
        body_file = fh.name
    cmd = ["gh", "issue", "create", "-R", repo,
           "--title", title,
           "--body-file", body_file]
    if labels:
        cmd += ["--label", ",".join(labels)]
    print(f"[gh command]\n  {' '.join(repr(c) for c in cmd)}")
    if dry_run:
        return {"dry_run": True, "cmd": cmd}
    r = subprocess.run(cmd, capture_output=True, text=True, timeout=60)
    Path(body_file).unlink(missing_ok=True)
    if r.returncode != 0:
        raise RuntimeError(f"gh failed: {r.stderr.strip()}")
    url = r.stdout.strip().splitlines()[-1]
    m = re.search(r"/issues/(\d+)$", url)
    number = int(m.group(1)) if m else None
    return {"url": url, "number": number}


def update_manifest(manifest_path: Path, entry: dict) -> None:
    if manifest_path.exists():
        data = json.loads(manifest_path.read_text(encoding="utf-8"))
    else:
        data = {"version": 1, "filed_issues": []}
    data.setdefault("filed_issues", []).append(entry)
    manifest_path.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("body_file", type=Path)
    ap.add_argument("--repo", required=True, help="owner/repo")
    ap.add_argument("--fingerprint", required=True, help="finding fingerprint slug")
    ap.add_argument("--source-citation", default="", help="local citation for traceback")
    ap.add_argument("--manifest", type=Path)
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    title, labels, body = parse_body_file(args.body_file)

    print(f"\n[issue preview]\n  title:  {title}\n  labels: {labels}\n  body bytes: {len(body)}\n")
    result = run_gh(args.repo, title, labels, body, args.dry_run)

    if result.get("dry_run"):
        print("\n[DRY RUN] No issue created. To file, re-run without --dry-run.")
        return 0

    print(f"\n✓ Filed: {result['url']}")

    if args.manifest:
        entry = {
            "fingerprint": args.fingerprint,
            "title": title,
            "gh_issue_number": result.get("number"),
            "gh_url": result["url"],
            "source_citation": args.source_citation,
            "filed_at": datetime.now(timezone.utc).isoformat(),
            "status": "open",
        }
        update_manifest(args.manifest, entry)
        print(f"✓ Manifest updated: {args.manifest}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
