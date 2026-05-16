#!/usr/bin/env python3
"""Sanitize a FINDINGS.md / CAPABILITIES.md for public sharing.

Hardened version — privacy-first. Aggressive redaction over signal preservation
on disagreement. Always emits a parallel mapping JSON locally for traceback.

Redacts:
  - Absolute home paths              (/home/<user>, /Users/<user>)
  - Scratch / log paths              (/tmp/..., /var/...)
  - Worktree names                   (.worktrees/<name>)
  - Session UUID prefixes            ([abc12345] -> [event-NNN])
  - Report-filename SIDs             (5ed9163e.md -> report-NN.md)
  - API tokens / secrets             (sk-..., gh[ps]_..., AKIA..., JWTs, Telegram tokens, generic TOKEN=)
  - Email addresses with TLD         (user@example.com)
  - IP addresses (v4)                (any A.B.C.D)
  - Long hex strings (commit SHAs)   (>=12 hex chars)
  - User-supplied substitutions      (innotrade, ryan, internal domains, etc.)
  - Maintainer GitHub handle         (@asheshgoplani -> <maintainer>)
  - Generic GitHub repo refs         (foo/bar -> <org>/bar if foo is in --map)

Audits afterwards and flags any line still containing suspicious patterns
for HUMAN REVIEW before sharing.

Usage:
    python sanitize.py <input.md> <output.md> [--map key=value ...]

The mapping JSON written alongside the output is for LOCAL traceback only.
Never share it.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path


# -- Paths -------------------------------------------------------------------
HOME_PATH_RE = re.compile(r"/(home|Users)/[a-zA-Z0-9_.-]+")
TMP_PATH_RE  = re.compile(r"/tmp/[a-zA-Z0-9._/-]+")
VAR_PATH_RE  = re.compile(r"/var/[a-zA-Z0-9._/-]+")
WORKTREE_RE  = re.compile(r"\.worktrees/[a-zA-Z0-9._/-]+")

# -- Identifiers in our findings docs ----------------------------------------
UUID_PREFIX_RE = re.compile(r"\[([0-9a-f]{8})\]")           # citation tokens [346296cb]
REPORT_SID_RE  = re.compile(r"\b([0-9a-f]{8})\.md\b")       # report-file references
FULL_UUID_RE   = re.compile(r"\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b")
LONG_HEX_RE    = re.compile(r"\b[a-f0-9]{12,}\b")           # commit SHAs / content hashes

# -- Secrets / tokens (HIGH PRIORITY) ----------------------------------------
ANTHROPIC_KEY_RE = re.compile(r"sk-(?:ant-)?[a-zA-Z0-9_-]{20,}")
OPENAI_KEY_RE    = re.compile(r"sk-(?:proj-)?[a-zA-Z0-9_-]{40,}")
GITHUB_TOKEN_RE  = re.compile(r"gh[opsu]_[a-zA-Z0-9]{20,}")
AWS_KEY_RE       = re.compile(r"AKIA[0-9A-Z]{16}")
TELEGRAM_BOT_RE  = re.compile(r"\b\d{8,12}:[A-Za-z0-9_-]{30,}\b")
JWT_RE           = re.compile(r"\beyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\b")
GENERIC_SECRET_RE = re.compile(
    r"(?i)\b(token|key|secret|password|api[_-]?key|auth(?:orization)?)\s*[=:]\s*['\"]?([a-zA-Z0-9_+/=.-]{12,})['\"]?"
)

SECRET_PATTERNS = [
    ("anthropic_key", ANTHROPIC_KEY_RE),
    ("openai_key", OPENAI_KEY_RE),
    ("github_token", GITHUB_TOKEN_RE),
    ("aws_access_key", AWS_KEY_RE),
    ("telegram_bot_token", TELEGRAM_BOT_RE),
    ("jwt", JWT_RE),
]

# -- Other PII ---------------------------------------------------------------
EMAIL_RE = re.compile(r"\b[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}\b")
IPV4_RE  = re.compile(r"\b(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\b")
GITHUB_HANDLE_RE = re.compile(r"@asheshgoplani\b")
REPO_OWNER_RE = re.compile(r"\basheshgoplani/")

# -- Audit (post-sanitize check) ---------------------------------------------
# After sanitization, flag any line still containing these patterns.
SUSPICIOUS_PATTERNS = [
    ("possible_long_hex", re.compile(r"\b[a-f0-9]{20,}\b")),
    ("possible_b64_blob", re.compile(r"\b[A-Za-z0-9+/=]{40,}\b")),
    ("env_var_with_value", re.compile(r"\b[A-Z_][A-Z0-9_]{4,}\s*[=:]\s*[^\s,]{8,}")),
    ("possible_phone", re.compile(r"\+\d[\d\s\-]{7,}")),
    ("private_ip_range", re.compile(r"\b(10|172|192\.168|100)\.\d")),  # rough heuristic
]


def redact_with_label(label: str, text: str) -> str:
    return f"<REDACTED:{label}>"


def sanitize(text: str, user_subs: dict[str, str]) -> tuple[str, dict]:
    """Return (sanitized_text, mapping)."""
    mapping: dict = {
        "user_subs": user_subs,
        "home_paths_seen": [],
        "tmp_paths_seen": [],
        "var_paths_seen": [],
        "worktree_paths_seen": [],
        "uuid_prefixes": {},
        "report_sids": {},
        "full_uuids_seen": 0,
        "long_hex_seen": 0,
        "secrets_redacted_by_kind": {},
        "emails_seen": 0,
        "ipv4_seen": 0,
    }

    # 1. User-supplied substitutions first (business names, conductor names)
    for raw, sub in user_subs.items():
        if raw in text:
            text = text.replace(raw, sub)

    # 2. Secrets — HIGHEST PRIORITY, do these first before any other pattern
    for kind, pat in SECRET_PATTERNS:
        def make_repl(k):
            def repl(m):
                mapping["secrets_redacted_by_kind"][k] = (
                    mapping["secrets_redacted_by_kind"].get(k, 0) + 1
                )
                return redact_with_label(k, m.group(0))
            return repl
        text = pat.sub(make_repl(kind), text)

    # Generic KEY=VALUE secrets — only redact the value
    def repl_generic_secret(m):
        kind = "generic_secret"
        mapping["secrets_redacted_by_kind"][kind] = (
            mapping["secrets_redacted_by_kind"].get(kind, 0) + 1
        )
        return f"{m.group(1)}=<REDACTED:{kind}>"
    text = GENERIC_SECRET_RE.sub(repl_generic_secret, text)

    # 3. Emails
    def repl_email(m):
        mapping["emails_seen"] += 1
        return "<REDACTED:email>"
    text = EMAIL_RE.sub(repl_email, text)

    # 4. IPv4 addresses
    def repl_ip(m):
        mapping["ipv4_seen"] += 1
        return "<REDACTED:ipv4>"
    text = IPV4_RE.sub(repl_ip, text)

    # 5. GitHub handle and repo owner
    text = GITHUB_HANDLE_RE.sub("<maintainer>", text)
    text = REPO_OWNER_RE.sub("<owner>/", text)

    # 6. Paths
    seen_home = set()
    seen_tmp  = set()
    seen_var  = set()
    seen_wt   = set()

    def repl_home(m):
        s = m.group(0)
        if s not in seen_home:
            seen_home.add(s)
            mapping["home_paths_seen"].append(s)
        return f"/{m.group(1)}/<user>"
    text = HOME_PATH_RE.sub(repl_home, text)

    def repl_tmp(m):
        s = m.group(0)
        if s not in seen_tmp:
            seen_tmp.add(s)
            mapping["tmp_paths_seen"].append(s)
        return "/tmp/<scratch>"
    text = TMP_PATH_RE.sub(repl_tmp, text)

    def repl_var(m):
        s = m.group(0)
        if s not in seen_var:
            seen_var.add(s)
            mapping["var_paths_seen"].append(s)
        return "/var/<system>"
    text = VAR_PATH_RE.sub(repl_var, text)

    def repl_wt(m):
        s = m.group(0)
        if s not in seen_wt:
            seen_wt.add(s)
            mapping["worktree_paths_seen"].append(s)
        return ".worktrees/<branch>"
    text = WORKTREE_RE.sub(repl_wt, text)

    # 7. Full session UUIDs (before short ones)
    def repl_full_uuid(m):
        mapping["full_uuids_seen"] += 1
        return "<REDACTED:session-uuid>"
    text = FULL_UUID_RE.sub(repl_full_uuid, text)

    # 8. UUID prefixes -> deterministic [event-NNN] tokens
    uuid_order: dict[str, str] = {}
    def repl_uuid_prefix(m):
        raw = m.group(1)
        if raw not in uuid_order:
            uuid_order[raw] = f"event-{len(uuid_order) + 1:03d}"
        return f"[{uuid_order[raw]}]"
    text = UUID_PREFIX_RE.sub(repl_uuid_prefix, text)
    mapping["uuid_prefixes"] = uuid_order

    # 9. Report SIDs -> deterministic report-NN.md
    sid_order: dict[str, str] = {}
    def repl_sid(m):
        raw = m.group(1)
        if raw not in sid_order:
            sid_order[raw] = f"report-{len(sid_order) + 1:02d}"
        return f"{sid_order[raw]}.md"
    text = REPORT_SID_RE.sub(repl_sid, text)
    mapping["report_sids"] = sid_order

    # 10. Long hex (commits, hashes) — replace with placeholder
    def repl_long_hex(m):
        mapping["long_hex_seen"] += 1
        return "<hash>"
    text = LONG_HEX_RE.sub(repl_long_hex, text)

    return text, mapping


def audit(text: str) -> list[dict]:
    """Run post-sanitize audit. Returns list of suspicious finds (line + reason)."""
    findings = []
    for ln_no, line in enumerate(text.splitlines(), 1):
        # Skip lines that are clearly our own REDACTED markers / generated tokens
        for label, pat in SUSPICIOUS_PATTERNS:
            for m in pat.finditer(line):
                # Filter false positives: our REDACTED labels and event tokens
                snippet = m.group(0)
                if snippet.startswith("<REDACTED") or snippet.startswith("event-") or snippet.startswith("report-"):
                    continue
                findings.append({
                    "line": ln_no,
                    "label": label,
                    "snippet": snippet[:120],
                    "context": line.strip()[:200],
                })
                break  # only first finding per line per pattern, to avoid spam
    return findings


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("input", type=Path)
    ap.add_argument("output", type=Path)
    ap.add_argument(
        "--map",
        nargs="*",
        default=[],
        help="User substitutions: key=value (e.g. innotrade=<work-conductor>)",
    )
    ap.add_argument("--no-audit", action="store_true", help="Skip post-audit (not recommended)")
    args = ap.parse_args()

    if not args.input.exists():
        print(f"Input not found: {args.input}", file=sys.stderr)
        return 1

    user_subs: dict[str, str] = {}
    for kv in args.map:
        if "=" not in kv:
            print(f"Bad --map entry: {kv} (expected key=value)", file=sys.stderr)
            return 2
        k, v = kv.split("=", 1)
        user_subs[k] = v

    text = args.input.read_text(encoding="utf-8")
    sanitized, mapping = sanitize(text, user_subs)

    args.output.write_text(sanitized, encoding="utf-8")
    map_path = args.output.with_suffix(args.output.suffix + ".map.json")
    map_path.write_text(json.dumps(mapping, indent=2), encoding="utf-8")

    audit_findings = [] if args.no_audit else audit(sanitized)
    audit_path = args.output.with_suffix(args.output.suffix + ".audit.json")
    audit_path.write_text(json.dumps(audit_findings, indent=2), encoding="utf-8")

    summary = {
        "input": str(args.input),
        "output": str(args.output),
        "mapping_path (LOCAL ONLY)": str(map_path),
        "audit_path": str(audit_path),
        "stats": {
            "in_bytes": len(text),
            "out_bytes": len(sanitized),
            "secrets_redacted_by_kind": mapping["secrets_redacted_by_kind"],
            "home_paths": len(mapping["home_paths_seen"]),
            "tmp_paths": len(mapping["tmp_paths_seen"]),
            "var_paths": len(mapping["var_paths_seen"]),
            "worktree_paths": len(mapping["worktree_paths_seen"]),
            "uuid_prefixes": len(mapping["uuid_prefixes"]),
            "report_sids": len(mapping["report_sids"]),
            "full_uuids": mapping["full_uuids_seen"],
            "long_hex": mapping["long_hex_seen"],
            "emails": mapping["emails_seen"],
            "ipv4": mapping["ipv4_seen"],
            "user_subs_applied": len(user_subs),
        },
        "audit": {
            "suspicious_findings": len(audit_findings),
            "must_review_before_sharing": len(audit_findings) > 0,
        },
    }
    print(json.dumps(summary, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
