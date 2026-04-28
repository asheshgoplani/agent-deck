# Skills: on-demand capabilities for agent-deck sessions

Skills are packaged instructions that a Claude Code session can load to gain new capabilities. Each skill is a single directory with a `SKILL.md` file and optional scripts, references, and assets alongside. When Claude reads a `SKILL.md`, its instructions enter the current context and stay active until the session ends or the context is compacted away.

agent-deck uses skills in two tiers. Understanding when to use which is the whole point of this doc.

## The two tiers

### User skills: always present

**Where they live.** `~/.claude/skills/` (default profile) or `~/.claude-work/skills/` (work profile, if you use one), and inside `CLAUDE_PLUGIN_ROOT` directories for installed plugins.

**How they load.** Every `claude` session, on every start and every resume, scans these locations and registers every skill it finds. The skills' descriptions (but not their full bodies) go into context so the model knows they exist and can invoke them when relevant.

**Cost.** Constant, per session. Every skill in these directories costs you some tokens on every startup, whether you use it or not. Ten small skills is fine; a hundred large ones will eat noticeable context.

**When to use.** Capabilities you need in 95%+ of your sessions. Examples: code simplification rules, commit hygiene, a universally-needed project-conventions skill. Anything you always want at hand.

### Pool skills: loaded on demand

**Where they live.** `<AGENT_DECK_ROOT>/skills/pool/` (typically `~/.agent-deck/skills/pool/`). Flat directory, one subfolder per skill. Not auto-discovered by Claude Code.

**How they load.** Manually. Either:

- From the terminal by attaching to a session: `agent-deck skill attach <session> <skill-name>` (the project-level attach workflow).
- From inside a running Claude session by reading the file directly: `Read <AGENT_DECK_ROOT>/skills/pool/<skill-name>/SKILL.md`. Once read, the contents are in context.

**Cost.** Zero when not loaded. When loaded, only for the lifetime of that session's context window.

**When to use.** Capabilities that are project-specific, experimental, rarely-needed, or heavy (long references, big asset lists). A WordPress migration skill you use twice a quarter; a customer-specific worklog skill; a research playbook that is 1500 lines of instructions. Keep them in the pool and pay the cost only when you need them.

### The rule of thumb

Put a skill in user-level directories if you will use it in almost every session. Put it in the pool otherwise. In practice, `code-simplifier` is user-level; `ryan-worklog` and `ard-worklog` sit in the pool.

## Invoking skills

Inside Claude Code, an auto-loaded skill is invoked with a slash command that matches its directory name:

```
/skill-name
```

Claude sees the slash, matches it against the registered skill set, and (typically) loads the full `SKILL.md` body into context before the next turn.

A pool skill that you have not auto-loaded is invoked by a direct read:

```
Read ~/.agent-deck/skills/pool/skill-name/SKILL.md
```

From that point in the conversation, its instructions are active. Skills "detach" naturally when the conversation ends or is compacted; there is no explicit detach command.

### From agent-deck's CLI

If you are running agent-deck, you can attach and detach skills at the project level without being inside the session:

```bash
# List what is discoverable.
agent-deck skill list

# Show what is currently attached to a session's project.
agent-deck skill attached my-project

# Attach a skill. The --source flag picks where to resolve from (pool, user, team, etc.).
agent-deck skill attach my-project ryan-worklog --source pool --restart

# Detach.
agent-deck skill detach my-project ryan-worklog
```

Attach writes the skill's materialized form into the project's `.claude/skills/` directory and updates `.agent-deck/skills.toml` so the state survives session restarts.

## Authoring a skill

The canonical path: use Anthropic's own `example-skills:skill-creator` skill. It has a bootstrap script (`init_skill.py`) that scaffolds a correctly-shaped SKILL.md, a `package_skill.py` helper for distribution, and lint rules that match what Claude Code expects.

### Directory shape

```
skill-name/
├── SKILL.md          # required
├── scripts/          # optional: executables the skill wants to run
├── references/       # optional: long docs the skill wants to cite
└── assets/           # optional: images, templates, fixtures
```

### SKILL.md frontmatter

```yaml
---
name: skill-name
description: |
  One to three sentences describing WHAT this skill does and, more importantly,
  WHEN Claude should use it. The description is what Claude sees to decide
  relevance, so be specific about trigger conditions: "use when X", "applies
  to Y", "do not use for Z".
---
```

The `name` field must match the directory name. The `description` is what gets loaded into every session that auto-discovers the skill, so keep it tight but explicit: a vague description means the skill never fires when it should, or worse, fires when it should not.

### SKILL.md body

The body is markdown. Claude reads it only when the skill is invoked. There is no hard size limit, but keep it focused: a skill that tries to teach everything teaches nothing.

Common structure:

1. A one-paragraph restatement of purpose.
2. Concrete triggers: "use this when the user says X" or "use after completing Y".
3. The procedure: ordered steps, with file paths and command examples.
4. Constraints: what NOT to do, what to escalate.
5. Pointers to the references/ and scripts/ subdirs for detail.

## agent-deck's own shipped skills

The repo ships a small set of skills inside `skills/`:

- `skills/agent-deck/SKILL.md`: the canonical orchestration skill. Covers session lifecycle, MCP attach/detach, groups, profiles, worktree sessions, and sub-agent launching. Every installation gets a curl-one-liner in the README that drops it straight into `~/.claude/skills/agent-deck/`.
- `skills/watcher-creator/`: guides the creation of watcher processes under the watcher framework.
- `skills/session-share/`: helpers for exporting and importing sessions between developers.

These are shipped as reference implementations: you can read them to understand how a well-shaped skill is written.

## When to prefer user-level vs pool

Use **user-level** when:

- You use the skill in almost every session (>90% of the time).
- The skill is small enough that loading its description on every start is negligible.
- The skill represents universal rules (style, commit hygiene, TDD gates).

Use **pool** when:

- The skill is scoped to one project, one customer, or one workflow.
- The skill is experimental and you want to invoke it consciously.
- The skill is large (long references, many procedures) and loading its description everywhere would be wasteful.
- You are iterating on the skill's wording and want to control when it is active.

A skill can start in the pool, prove itself useful, and get promoted to user-level with a symlink. Going the other way (demoting a user skill to the pool) is equally easy: move the directory, leave a note for your future self.

## Caveats

- **Pool skills do not auto-discover.** If you add a skill to the pool and expect `/skill-name` to work without attaching it first, it will not. You either attach it via `agent-deck skill attach` or `Read` it explicitly.
- **Skills are text, not code.** They persuade, they do not compile. A skill that says "always run the tests" only works if the model actually runs the tests. Write skills that are easy to follow and include command examples the model can copy verbatim.
- **SKILL.md frontmatter drift.** If you rename a skill's directory, update the `name` field. Mismatches cause quiet registration failures that look like "Claude knows the skill but will not invoke it".
- **Context budget on startup.** Every auto-discovered skill contributes to context on every session start. If startup feels heavy, list `~/.claude/skills/` and consider which of them really belong in the pool.

## Related docs

- [CONDUCTOR.md](CONDUCTOR.md): conductors lean heavily on skills for policy and routines.
- [WATCHDOG.md](WATCHDOG.md): the watchdog itself has no skill, but conductors it restarts typically do.
