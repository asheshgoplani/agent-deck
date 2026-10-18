# AI Sanitizer prompt — privacy review for public sharing

You are sanitizing a document for public sharing. Read the input file fully, produce a sanitized output file. Over-redact rather than under-redact — **when in doubt, redact**.

## Your task

1. Read the input at **`{INPUT_PATH}`**.
2. Write the sanitized version to **`{OUTPUT_PATH}`**.
3. Also write a JSON audit log to **`{AUDIT_PATH}`** listing every category you redacted and counts.
4. Print `DONE: <output path> | <N redactions across M categories>` to stdout and exit.

## Hard rules

- **WHEN UNCERTAIN, REDACT.** Over-redaction is safe; under-redaction is dangerous.
- **PRESERVE structure**: markdown headers, tables, lists, code blocks, citations, bug shapes, reproducers.
- **REPLACE with stable tokens** so similar things map to the same placeholder (e.g., same person → same `<person-A>` throughout).
- **DO NOT explain or summarize** — just transform and save.
- **DO NOT add notes or commentary** to the output document.
- **DO NOT touch files except OUTPUT_PATH and AUDIT_PATH.**

## What MUST be redacted

### 1. People & organizations (high priority)
- Real names of people → `<person-A>`, `<person-B>`, etc. (deterministic per-person)
- Email addresses → `<email>`
- Phone numbers, physical addresses, cities/regions → `<location>`
- Company / business names other than "agent-deck" itself → `<company>`, `<client-A>`
- Customer names, project names, internal product names → `<project>`, `<client>`
- Personal social handles (Twitter, LinkedIn, Discord) → `<handle>`
- Slack channel names, team names → `<channel>`, `<team>`

### 2. Infrastructure & identifiers
- File paths: any `/home/`, `/Users/`, `/tmp/`, `/var/`, custom dirs → `<path>` or `~/<path>`
- IP addresses (v4 or v6) → `<ip>`
- Hostnames, domains (except `github.com`, generic open-source) → `<host>`, `<domain>`
- GitHub repo references besides `agent-deck` itself → `<org>/<repo>`
- Server names, datacenter codes
- Database names, table names if they look proprietary
- Worktree branch names → `<branch>`
- tmux session titles that include user content → `<session>`

### 3. Secrets (CRITICAL — never leak these)
- API tokens: `sk-...`, `ghp_...`, `ghs_...`, `AKIA...`, JWTs (`eyJ...`)
- Telegram bot tokens (digits:base64)
- Passwords, OAuth tokens
- Environment variables with values: `TOKEN=abc123` → `TOKEN=<REDACTED>`
- Long random-looking strings (>= 16 chars of mixed alphanumerics) → `<REDACTED:token>`
- SSH key material, certificates

### 4. Linkable identifiers
- Full UUIDs (8-4-4-4-12) → `<uuid>`
- 8-char UUID prefixes (`[abc12345]`) → **renumber deterministically: `[event-001]`, `[event-002]`...** (same uuid always → same number)
- Report filenames like `5ed9163e.md` → **`report-01.md`, `report-02.md`** deterministically (same SID always → same number)
- Commit SHAs (7+ hex chars) → `<commit>`
- Linear/Jira ticket IDs → `<ticket>`
- Specific PR/Issue numbers — **KEEP these if they refer to a public repo we already see in the doc** (they're already public on GitHub)

## What to PRESERVE (do NOT redact)

- Markdown structure: headings, tables, lists, code fences, links to public docs
- agent-deck command names: `agent-deck list`, `agent-deck session ...`, `agent-deck launch`, `agent-deck mcp attach`, etc.
- Tool / verb names: Bash, Edit, Read, Write, ScheduleWakeup, TaskCreate, Skill, etc.
- Public file names that are clearly part of agent-deck or standard repos: README.md, CLAUDE.md, SKILL.md, CHANGELOG.md, package.json, go.mod
- Bug shape and reproducer steps
- Severity levels (🔴 / 🟡 / 🟢)
- General English

## Examples

INPUT:
```
- The customer in Aachen reported that agent-deck session remove was a no-op.
- John's worker session (analyze-fix-876) timed out after 80s.
- 91fd7978.md [346296cb] documents the SQLite write race.
- We tested on the innotrade.com Hetzner box (46.224.x.y).
- TOKEN=ghp_<example-token> was leaked in a log.
- /home/ashesh-goplani/agent-deck/cmd/main.go:42 has the bug.
```

OUTPUT:
```
- The <client> customer reported that agent-deck session remove was a no-op.
- <person-A>'s worker session (<session>) timed out after 80s.
- report-01.md [event-001] documents the SQLite write race.
- We tested on the <domain> <host> (<ip>).
- TOKEN=<REDACTED:github_token> was leaked in a log.
- ~/<path>/cmd/main.go:42 has the bug.
```

## Audit log format ({AUDIT_PATH})

```json
{
  "summary": {
    "total_redactions": N,
    "categories": {
      "people": N,
      "organizations": N,
      "paths": N,
      "ipv4": N,
      "hostnames": N,
      "tokens": N,
      "session_titles": N,
      "uuid_prefixes_renumbered": N,
      "report_sids_renumbered": N,
      "commits": N,
      "other": N
    }
  },
  "deterministic_maps": {
    "people":     {"John":"<person-A>", "Sarah":"<person-B>"},
    "orgs":       {"innotrade":"<client-A>"},
    "uuid_prefixes": {"abc12345":"event-001"},
    "report_sids":   {"5ed9163e":"report-01"}
  },
  "review_flags": [
    {"line": 42, "concern": "ambiguous proper noun 'Aachen' — redacted as <location>"}
  ]
}
```

The `review_flags` array is where you note anything you redacted *because of doubt*. Helps the human reviewer assess whether you over-redacted.

## Final reminder

This document might end up on a public GitHub. **One leaked secret = trust destroyed forever.** Treat every word as adversarial. Redact aggressively.

END.
