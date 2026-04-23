# SKILL.md Updates — Phase 4 Audit Artifact (CFG-09)

Captures the external-file updates applied during Plan 04-02 Task 2. External
paths are not part of the repo working tree, so this artifact provides the
repo-visible audit trail required for CFG-09 closure.

## Canonical plugin-cache SKILL.md

**Path resolved:** `~/.claude/plugins/cache/agent-deck/agent-deck/12c0a65dfb13/skills/agent-deck/SKILL.md`

**Status:** applied

**Diff applied** (unified format, context-3):

```diff
--- /tmp/skill.canonical.before.md	2026-04-16 00:11:24.911461994 +0200
+++ ~/.claude/plugins/cache/agent-deck/agent-deck/12c0a65dfb13/skills/agent-deck/SKILL.md	2026-04-16 00:12:14.455170358 +0200
@@ -281,6 +281,22 @@

 See [config-reference.md](references/config-reference.md) for all options.

+### Per-conductor Claude config (v1.5.4+)
+
+Conductors can carry their own Claude `config_dir` and `env_file` via:
+
+```toml
+[conductors.<name>.claude]
+config_dir = "~/.claude-work"
+env_file = "~/git/work/.envrc"
+```
+
+Keyed by conductor name (the string passed to `agent-deck conductor setup <name>`). Precedence (high → low): `CLAUDE_CONFIG_DIR` env > `[conductors.<name>.claude]` > `[groups."<g>".claude]` > `[profiles.<p>.claude]` > `[claude]` > `~/.claude`.
+
+Closes [issue #602](https://github.com/asheshgoplani/agent-deck/issues/602).
+
+**Canonical vs pool:** This SKILL.md lives at `~/.claude/plugins/cache/agent-deck/agent-deck/<hash>/skills/agent-deck/SKILL.md` (auto-loaded by Claude Code when the agent-deck plugin is active). A pool copy at `~/.agent-deck/skills/pool/agent-deck/SKILL.md` exists on some hosts — the pool version is loaded on-demand via `Read ~/.agent-deck/skills/pool/agent-deck/SKILL.md`.
+
 ## Troubleshooting

 | Issue | Solution |
```

**Post-edit verification** (`grep` against the canonical SKILL.md):

```
$ grep -n "\[conductors\..*\.claude\]\|Per-conductor\|issues/602" \
    ~/.claude/plugins/cache/agent-deck/agent-deck/12c0a65dfb13/skills/agent-deck/SKILL.md
```

All three substrings (`[conductors.<name>.claude]`, `Per-conductor Claude config`, `issues/602`) are present.

## Pool SKILL.md

**Path:** `~/.agent-deck/skills/pool/agent-deck/SKILL.md`

**Status:** no pool skill found — host lacks pool directory; SKILL.md update skipped

**Diff applied** (unified format, or skip string):

```diff
no pool skill found — host lacks pool directory; SKILL.md update skipped
```

**Absence check** (evidence):

```
$ ls -la ~/.agent-deck/skills/pool/agent-deck/SKILL.md 2>/dev/null || echo "POOL NOT FOUND"
POOL NOT FOUND
```

The pool directory `~/.agent-deck/skills/pool/agent-deck/` does not exist on this
execution host. Per the spec (REQUIREMENTS.md CFG-09 "pool path if present"),
absence is not a gap — this artifact documents it for audit.

## Verification

- Canonical path resolved to a single hash directory (`12c0a65dfb13`); update applied.
- Pool path absent; skip recorded with the canonical skip string.
- Plan 04-02 acceptance criterion: "BOTH sections populated (either with a diff OR the literal skip string)" — satisfied.
- Phase 4 plan-02 Task 2 done.
