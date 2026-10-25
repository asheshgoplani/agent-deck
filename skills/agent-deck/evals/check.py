#!/usr/bin/env python3
"""Deterministic checks for the agent-deck skill evals. No model calls.

    python3 -I check.py static [--binary PATH]   schema, anchors, grounding, trigger terms,
                                                 coverage; with --binary also CLI flag drift
    python3 -I check.py mutate [--binary PATH]   delete each eval's source sections in a temp
                                                 copy; every eval must then fail grounding
    python3 -I check.py grade ANSWERS.json       grade model answers by each eval's checks
    python3 -I check.py coverage [--md]          promise coverage table

Data: evals.json (task, agent-friendliness and trigger evals) and promises.json (what the
skill teaches). Exit 0 = all checks passed, 1 = a check failed, 2 = bad usage or input.
"""
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import tempfile

EVALS_DIR = os.path.dirname(os.path.abspath(__file__))
SKILL_DIR = os.path.dirname(EVALS_DIR)
REPO_DIR = os.path.dirname(os.path.dirname(SKILL_DIR))
AREAS = ("core", "fleet", "session-share", "recall", "retro", "repro", "contributing")
CHECKERS = ("cli-drift", "config-drift", "trigger-terms")
HEADING = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")


def load(skill_dir=SKILL_DIR):
    with open(os.path.join(skill_dir, "evals", "evals.json")) as f:
        evals = json.load(f)
    with open(os.path.join(skill_dir, "evals", "promises.json")) as f:
        promises = json.load(f)["promises"]
    return evals, promises


def task_evals(evals):
    """Every task eval (core + agent friendliness), each with its list name."""
    out = [("evals", e) for e in evals["evals"]]
    out += [("agent_friendliness_evals", e) for e in evals["agent_friendliness_evals"]["evals"]]
    return out


def triggers(evals):
    """Every trigger eval as (label, eval), e.g. ("should_trigger[3]", {...})."""
    t = evals["trigger_evals"]
    return [(f"{k}[{i}]", e) for k in ("should_trigger", "should_not_trigger") for i, e in enumerate(t.get(k, []))]


def read(skill_dir, rel):
    path = os.path.join(skill_dir, rel)
    if not os.path.isfile(path):
        return None
    with open(path, encoding="utf-8") as f:
        return f.read()


def strip_fences(lines):
    """Yield (index, line, in_code) so headings inside code fences are ignored."""
    fence = None
    for i, line in enumerate(lines):
        m = re.match(r"^\s*(```|~~~)", line)
        if m:
            fence = None if fence == m.group(1) else (fence or m.group(1))
            yield i, line, True
            continue
        yield i, line, fence is not None


def section(text, heading):
    """Text of the section under `heading` up to the next heading of the same or higher
    level, or None when the heading is missing. An empty heading means the whole file."""
    if not heading:
        return text
    lines = text.splitlines()
    start = level = None
    for i, line, in_code in strip_fences(lines):
        if in_code:
            continue
        m = HEADING.match(line)
        if not m:
            continue
        if start is None:
            if m.group(2).strip() == heading.strip():
                start, level = i, len(m.group(1))
        elif len(m.group(1)) <= level:
            return "\n".join(lines[start:i])
    return None if start is None else "\n".join(lines[start:])


def split_source(src):
    rel, _, heading = src.partition("#")
    return rel, heading


def source_text(skill_dir, src):
    rel, heading = split_source(src)
    text = read(skill_dir, rel)
    return None if text is None else section(text, heading)


def frontmatter_description(skill_dir):
    text = read(skill_dir, "SKILL.md") or ""
    m = re.search(r"^description:\s*(.*)$", text, re.M)
    return m.group(1) if m else ""


def match(pattern, text):
    return re.search(pattern, text, re.I | re.S) is not None


# ---------------------------------------------------------------- static checks

def go_test_exists(name):
    """True when `func <name>(` is defined in some _test.go file of the repo. Without a
    repo checkout next to the skill (a plugin install) the check is skipped."""
    if not os.path.isfile(os.path.join(REPO_DIR, "go.mod")):
        return True
    pat = re.compile(r"^func " + re.escape(name) + r"\(", re.M)
    for sub in ("cmd", "internal", "tests"):
        for root, _, files in os.walk(os.path.join(REPO_DIR, sub)):
            for f in files:
                if f.endswith("_test.go"):
                    with open(os.path.join(root, f), encoding="utf-8") as fh:
                        if pat.search(fh.read()):
                            return True
    return False


def check_schema(evals, promises, errs):
    pids = [p["id"] for p in promises]
    for dup in {x for x in pids if pids.count(x) > 1}:
        errs.append(f"promise id duplicated: {dup}")
    known = set(pids)
    for p in promises:
        if p.get("area") not in AREAS:
            errs.append(f"promise {p['id']}: unknown area {p.get('area')!r}")
        if p.get("kind") not in ("behaviour", "reference", "script"):
            errs.append(f"promise {p['id']}: unknown kind {p.get('kind')!r}")
        for c in p.get("checked_by", []):
            if c not in CHECKERS and not c.startswith(("script:", "go:")):
                errs.append(f"promise {p['id']}: unknown checker {c!r}")
            if c.startswith("go:") and not go_test_exists(c[3:]):
                errs.append(f"promise {p['id']}: Go test not found in the repo: {c[3:]}")
            if c.startswith("script:") and not os.path.exists(os.path.join(SKILL_DIR, c[7:])):
                errs.append(f"promise {p['id']}: checker script missing: {c[7:]}")
    ids = [str(e["id"]) for _, e in task_evals(evals)]
    for dup in {x for x in ids if ids.count(x) > 1}:
        errs.append(f"eval id duplicated: {dup}")
    for _, e in task_evals(evals):
        eid = e["id"]
        for key in ("prompt", "expected_output", "promises", "sources", "checks"):
            if not e.get(key):
                errs.append(f"eval {eid}: missing {key}")
        for pid in e.get("promises", []):
            if pid not in known:
                errs.append(f"eval {eid}: unknown promise {pid}")
        checks = e.get("checks", {})
        if not checks.get("must"):
            errs.append(f"eval {eid}: checks.must is empty")
        for pat in checks.get("must", []) + checks.get("must_not", []):
            try:
                re.compile(pat)
            except re.error as ex:
                errs.append(f"eval {eid}: bad regex {pat!r}: {ex}")
    for i, t in triggers(evals):
        for pid in t.get("promises", []):
            if pid not in known:
                errs.append(f"trigger {i}: unknown promise {pid}")
        if not t.get("terms") and (t["should_trigger"] or t.get("near_miss")):
            errs.append(f"trigger {i}: missing terms (regex the description must match)")


def check_anchors(skill_dir, promises, errs):
    for p in promises:
        text = read(skill_dir, p["file"])
        if text is None:
            errs.append(f"promise {p['id']}: file missing: {p['file']}")
            continue
        body = section(text, p.get("heading", ""))
        if body is None:
            errs.append(f"promise {p['id']}: heading missing in {p['file']}: {p['heading']!r}")
        elif p["anchor"] not in body:
            errs.append(f"promise {p['id']}: anchor not found under its heading: {p['anchor']!r}")


def grounding_errors(skill_dir, e):
    """An eval is grounded when every must pattern matches the text of its sources."""
    out = []
    texts = []
    for src in e.get("sources", []):
        t = source_text(skill_dir, src)
        if t is None:
            out.append(f"eval {e['id']}: source missing: {src}")
        else:
            texts.append(t)
    joined = "\n".join(texts)
    for pat in e.get("checks", {}).get("must", []):
        if texts and not match(pat, joined):
            out.append(f"eval {e['id']}: must pattern not in its sources: {pat!r}")
    return out


def headings(text):
    """(level, title) of every markdown heading outside code fences."""
    out = []
    lines = text.splitlines()
    for _, line, in_code in strip_fences(lines):
        m = None if in_code else HEADING.match(line)
        if m:
            out.append((len(m.group(1)), m.group(2).strip()))
    return out


SPECIFIC_MAX = 12  # a must matching more often than this across the skill is not specific


def check_specific(skill_dir, evals, promises, errs):
    """Checks pin facts, not vocabulary: no must may be satisfied by echoing the eval's
    own prompt, each eval needs one must that matches at most SPECIFIC_MAX times across the
    skill's markdown, and cited headings are unique and narrower than a whole file."""
    corpus = "\n".join(read(skill_dir, f) or "" for f in md_files(skill_dir))
    for _, e in task_evals(evals):
        musts = e.get("checks", {}).get("must", [])
        echo = [p for p in musts if match(p, e.get("prompt", ""))]
        for p in echo:
            errs.append(f"eval {e['id']}: must {p!r} is satisfied by the eval's own prompt")
        specific = [p for p in musts if p not in echo
                    and len(re.findall(p, corpus, re.I | re.S)) <= SPECIFIC_MAX]
        if musts and not specific:
            errs.append(f"eval {e['id']}: no specific must (each matches > {SPECIFIC_MAX} times in the skill)")
        for src in e.get("sources", []):
            rel, heading = split_source(src)
            text = read(skill_dir, rel)
            if text is None:
                continue
            hs = headings(text)
            if not heading and len(text.splitlines()) > 80:
                errs.append(f"eval {e['id']}: source {src} is a whole file; cite a section")
            elif heading and (1, heading) in hs and len(text.splitlines()) > 80:
                errs.append(f"eval {e['id']}: source {src} is the file's title section; cite a subsection")
            if heading and sum(1 for _, h in hs if h == heading) > 1:
                errs.append(f"eval {e['id']}: heading {heading!r} occurs more than once in {rel}")
    for p in promises:
        text = read(skill_dir, p["file"]) or ""
        if p.get("heading") and sum(1 for _, h in headings(text) if h == p["heading"]) > 1:
            errs.append(f"promise {p['id']}: heading {p['heading']!r} occurs more than once in {p['file']}")


def check_grounding(skill_dir, evals, errs):
    for _, e in task_evals(evals):
        errs.extend(grounding_errors(skill_dir, e))


def trigger_errors(skill_dir, i, t):
    """should_trigger: every term is in the description. A near miss that should not
    trigger: its terms are in the description's "Not for ..." exclusion clause. Any other
    should-not query: its terms (optional) must stay out of the description."""
    desc = frontmatter_description(skill_dir)
    if t["should_trigger"]:
        hay, want, where = desc, True, "description"
    elif t.get("near_miss"):
        m = re.search(r"Not for (.*)$", desc)
        hay, want, where = (m.group(1) if m else ""), True, "its Not-for clause"
    else:
        hay, want, where = desc, False, "description"
    return [f"trigger {i} ({t['query'][:50]!r}): term {term!r} "
            f"{'not in' if want else 'must stay out of the'} {where}"
            for term in t.get("terms", []) if match(term, hay) != want]


def check_triggers(skill_dir, evals, errs):
    trig = [t for _, t in triggers(evals)]
    for i, t in triggers(evals):
        errs.extend(trigger_errors(skill_dir, i, t))
    for area in AREAS:
        n = sum(1 for t in trig if t["should_trigger"] and t.get("area") == area)
        if n < 2:
            errs.append(f"trigger evals: area {area} has {n} should-trigger queries, want >= 2")
    if sum(1 for t in trig if not t["should_trigger"] and t.get("near_miss")) < 5:
        errs.append("trigger evals: want >= 5 near-miss should-not-trigger queries")


def coverage(evals, promises):
    """promise id -> (status, [what covers it])."""
    by = {p["id"]: [] for p in promises}
    weak = {p["id"]: [] for p in promises}
    for lst, e in task_evals(evals):
        for pid in e.get("promises", []):
            if pid in by:
                (by if e.get("checks", {}).get("must") else weak)[pid].append(f"{lst}:{e['id']}")
    for i, t in triggers(evals):
        for pid in t.get("promises", []):
            if pid in weak:
                weak[pid].append(f"trigger:{i}")
    out = {}
    for p in promises:
        strong = by[p["id"]] + p.get("checked_by", [])
        if strong and all(c in ("cli-drift", "config-drift") for c in strong):
            # the drift checkers prove the commands, flags and keys exist, not behaviour
            out[p["id"]] = ("drift-only", strong + weak[p["id"]])
        elif strong:
            out[p["id"]] = ("covered", strong + weak[p["id"]])
        elif weak[p["id"]]:
            out[p["id"]] = ("partly", weak[p["id"]])
        else:
            out[p["id"]] = ("not covered", [])
    return out


def check_coverage(evals, promises, errs):
    kinds = {p["id"]: p["kind"] for p in promises}
    for pid, (status, _) in coverage(evals, promises).items():
        if status == "drift-only" and kinds[pid] == "reference":
            continue  # a lookup table: its commands, flags or keys are what it promises
        if status != "covered":
            errs.append(f"promise {pid}: {status}")


# ------------------------------------------------------------------ CLI drift

def md_files(skill_dir):
    out = ["SKILL.md"]
    for root, _, files in os.walk(os.path.join(skill_dir, "references")):
        out += [os.path.relpath(os.path.join(root, f), skill_dir) for f in sorted(files) if f.endswith(".md")]
    return out


def invocations(text):
    """Yield agent-deck invocations found in code spans and code blocks."""
    snippets = re.findall(r"```[^\n]*\n(.*?)```", text, re.S)
    rest = re.sub(r"```.*?```", "", text, flags=re.S)
    snippets += re.findall(r"`([^`\n]+)`", rest)
    for snip in snippets:
        snip = snip.replace("\\\n", " ")
        for line in snip.splitlines():
            line = line.split(" #")[0]
            for part in re.split(r"\|\||&&|[|;]|\$\(|\)", line):
                # only where agent-deck starts the command (after a prompt, env assignments,
                # timeout/env/!), not prose such as "agent-deck will auto-migrate"
                m = re.match(r"\s*(?:[$!>]\s*)?(?:(?:env|timeout\s+\S+|[A-Z_][A-Z0-9_]*=\S*)\s+)*"
                             r"agent-deck((?:\s+\S+)+)", part)
                if m:
                    yield "agent-deck" + m.group(1)


class Cli:
    def __init__(self, binary, scratch):
        self.binary = binary
        self.cache = {}
        os.makedirs(os.path.join(scratch, "tmux"), exist_ok=True)
        self.env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": scratch,
                    "XDG_CONFIG_HOME": os.path.join(scratch, ".config"),
                    "XDG_DATA_HOME": os.path.join(scratch, ".local", "share"),
                    "TMUX_TMPDIR": os.path.join(scratch, "tmux"), "TERM": "dumb"}

    def help(self, path):
        """Help text for a subcommand path, or None when it is not a command."""
        key = tuple(path)
        if key not in self.cache:
            try:
                r = subprocess.run([self.binary, *path, "--help"], env=self.env, cwd=self.env["HOME"],
                                   stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=15)
                out = r.stdout + r.stderr
                bad = (r.returncode != 0 or "not a recognized command" in out
                       or re.search(r"unknown [\w -]{0,40}?command", out, re.I))
                self.cache[key] = None if bad else out
            except subprocess.TimeoutExpired:
                self.cache[key] = None
        return self.cache[key]


def source_flags():
    """Flag names the CLI defines (cmd/agent-deck/*.go). Some subcommands print their
    parent's usage for --help, so a flag missing there but defined here is not drift."""
    names = set()
    src = os.path.join(REPO_DIR, "cmd", "agent-deck")
    if not os.path.isdir(src):
        return names
    for f in os.listdir(src):
        if f.endswith(".go") and not f.endswith("_test.go"):
            with open(os.path.join(src, f), encoding="utf-8") as fh:
                names.update(re.findall(r"\.(?:Bool|String|Int|Int64|Uint|Float64|Duration|Var|Func|BoolFunc|TextVar)"
                                        r"(?:Var)?\(\s*(?:&?[\w.]+,\s*)?\"([\w-]+)\"", fh.read()))
    return names


def subcommands(helptext, path):
    """Subcommand names a help page lists: rows under "Commands:" (with aliases) and
    usage lines of the form `agent-deck <path> <word>`."""
    names = set()
    block = re.search(r"^Commands:\n(.*?)(?:\n\s*\n|\Z)", helptext, re.M | re.S)
    if block:
        for line in block.group(1).splitlines():
            m = re.match(r"\s+([a-z][a-z0-9-]*)(?=[\s/,]|$)", line)
            if m:
                names.add(m.group(1))
                for alias in re.findall(r"alias(?:es)?: ([\w, -]+)\)", line):
                    names.update(a.strip() for a in alias.split(","))
    names.update(re.findall(r"agent-deck " + re.escape(" ".join(path)) + r" ([a-z][a-z0-9-]*)", helptext))
    for alts in re.findall(r"agent-deck " + re.escape(" ".join(path)) + r" <([a-z][a-z0-9|-]*\|[a-z0-9|-]+)>", helptext):
        names.update(alts.split("|"))
    return names


def takes_positional(helptext, path):
    """True when a usage line puts a positional (not <command>) right after the path."""
    pat = r"agent-deck " + re.escape(" ".join(path)) + r" [<\[](?!(sub)?command\b)(?![a-z0-9-]+\|)"
    return re.search(pat, helptext) is not None


def allowed(allow, rel, inv):
    return any(a["file"] == rel and re.search(a["pattern"], inv) for a in allow)


def check_cli(skill_dir, binary, errs, notes):
    allow = []
    allow_path = os.path.join(skill_dir, "evals", "drift-allow.json")
    if os.path.exists(allow_path):
        with open(allow_path) as f:
            allow = json.load(f)["allow"]
    defined = source_flags()
    scratch = tempfile.mkdtemp(prefix="skill-evals-home-")
    cli = Cli(binary, scratch)
    seen = checked = 0
    for rel in md_files(skill_dir):
        for inv in invocations(read(skill_dir, rel)):
            try:
                toks = shlex.split(inv)[1:]
            except ValueError:
                continue
            seen += 1
            gflags, i = [], 0
            while i < len(toks) and toks[i].startswith("-"):
                gflags.append(toks[i])
                i += 2 if toks[i] in ("-p", "--profile") else 1
            path = []
            while i < len(toks) and re.fullmatch(r"[a-z][a-z0-9-]*", toks[i]) and len(path) < 3:
                if cli.help(path + [toks[i]]) is None:
                    break
                path.append(toks[i])
                i += 1
            if not path:
                if i < len(toks) and re.fullmatch(r"[a-z][a-z0-9-]*", toks[i]) and not allowed(allow, rel, inv):
                    errs.append(f"cli: {rel}: not a command: agent-deck {toks[i]}  ({inv[:80]})")
                continue
            helptext = cli.help(path)
            checked += 1
            if path[0] == "remote" and (len(path) == 1 or path[1] == "exec"):
                continue  # the rest is a command run on the remote, with its own flags
            nxt = toks[i] if i < len(toks) else ""
            subs = subcommands(helptext, path)
            if (re.fullmatch(r"[a-z][a-z0-9-]*", nxt) and subs and nxt not in subs
                    and not takes_positional(helptext, path) and not allowed(allow, rel, inv)):
                errs.append(f"cli: {rel}: not a subcommand: agent-deck {' '.join(path)} {nxt}  ({inv[:80]})")
                continue
            # a subcommand whose --help is its parent's usage: its flags are checked
            # against the flag definitions in the source instead
            borrowed = len(path) > 1 and helptext == cli.help(path[:-1])
            for t in toks[i:]:
                m = re.fullmatch(r"(--?)([a-zA-Z][\w-]*)(=.*)?", t)
                if not m or t in ("-", "--"):
                    continue
                name = m.group(2)
                if re.search(r"(^|[\s,\[(|/])--?" + re.escape(name) + r"\b", helptext):
                    continue
                if cli.help([]) and re.search(r"(^|[\s,\[(|])--?" + re.escape(name) + r"\b", cli.help([])):
                    continue
                if (borrowed and name in defined) or allowed(allow, rel, inv):
                    continue
                errs.append(f"cli: {rel}: flag {t} not in `agent-deck {' '.join(path)} --help`  ({inv[:90]})")
    shutil.rmtree(scratch, ignore_errors=True)
    notes.append(f"cli: {seen} invocations, {checked} resolved to a subcommand, "
                 f"{len(cli.cache)} help pages read")


# ---------------------------------------------------------------- config drift

def check_config(skill_dir, errs, notes):
    """Every key and top-level table in config-reference.md's toml blocks is a toml tag
    in the Go source (UserConfig and friends)."""
    if not os.path.isdir(os.path.join(REPO_DIR, "internal")):
        notes.append("config: skipped (no repo source next to the skill)")
        return
    tags = set()
    for sub in ("internal", "cmd"):
        for root, _, files in os.walk(os.path.join(REPO_DIR, sub)):
            for f in files:
                if f.endswith(".go") and not f.endswith("_test.go"):
                    with open(os.path.join(root, f), encoding="utf-8") as fh:
                        tags.update(re.findall(r'toml:"([\w-]+)', fh.read()))
    text = read(skill_dir, "references/config-reference.md") or ""
    keys = 0
    for block in re.findall(r"```toml\n(.*?)```", text, re.S):
        for line in block.splitlines():
            line = line.strip()
            m = re.match(r"\[+\s*\"?([\w-]+)", line)
            name = m.group(1) if m else None
            if not m:
                m = re.match(r"([a-z_][a-z0-9_]*)\s*=", line)
                name = m.group(1) if m else None
            if name:
                keys += 1
                if name not in tags:
                    errs.append(f"config: references/config-reference.md: {name!r} is no toml tag in the Go source  ({line[:70]})")
    notes.append(f"config: {keys} keys and tables checked against toml tags")


# ------------------------------------------------------------------- commands

def run_static(skill_dir, binary=None, quiet=False):
    evals, promises = load(skill_dir)
    errs, notes = [], []
    check_schema(evals, promises, errs)
    check_anchors(skill_dir, promises, errs)
    check_grounding(skill_dir, evals, errs)
    check_specific(skill_dir, evals, promises, errs)
    check_triggers(skill_dir, evals, errs)
    check_coverage(evals, promises, errs)
    check_config(skill_dir, errs, notes)
    if binary:
        check_cli(skill_dir, binary, errs, notes)
    if not quiet:
        cov = coverage(evals, promises)
        n = sum(1 for s, _ in cov.values() if s == "covered")
        d = sum(1 for s, _ in cov.values() if s == "drift-only")
        print(f"static: {len(promises)} promises, {n} covered by evals or tests ({100 * n // max(1, len(promises))}%), "
              f"{d} reference tables drift-checked only; "
              f"{len(task_evals(evals))} task evals, {len(triggers(evals))} trigger evals")
        for line in notes:
            print(line)
        for line in errs:
            print("FAIL", line)
        print(f"static: {'PASS' if not errs else 'FAIL'} ({len(errs)} problems)")
    return errs


def checker_mutants(binary):
    """The drift checkers must catch an invented flag, command and config key."""
    cases = [("config", "references/config-reference.md", "\n```toml\n[core]\nno_such_key_xyz = true\n```\n")]
    if binary:
        cases += [("cli flag", "SKILL.md", "\n`agent-deck session send --no-such-flag-xyz w hi`\n"),
                  ("cli command", "SKILL.md", "\n`agent-deck frobnicate-xyz now`\n"),
                  ("cli subcommand", "SKILL.md", "\n`agent-deck session clear-parent-xyz worker`\n"),
                  ("cli flag of another subcommand", "SKILL.md", "\n`agent-deck session send --ticket SB-1 worker hi`\n")]
    rows = []
    for name, rel, extra in cases:
        tmp = tempfile.mkdtemp(prefix="skill-mutant-")
        mut = os.path.join(tmp, "agent-deck")
        shutil.copytree(SKILL_DIR, mut, ignore=shutil.ignore_patterns("scripts", "tests"))
        with open(os.path.join(mut, rel), "a", encoding="utf-8") as f:
            f.write(extra)
        errs, notes = [], []
        if name == "config":
            check_config(mut, errs, notes)
        else:
            check_cli(mut, binary, errs, notes)
        shutil.rmtree(tmp, ignore_errors=True)
        rows.append((bool(errs), f"checker {name}: appended {extra.strip()!r} to {rel}"))
    return rows


def cmd_mutate(binary=None):
    """Each eval must fail grounding once its sources are deleted, each trigger eval
    must fail once its terms are garbled out of the description, and each drift checker
    must catch an invented flag, command or key."""
    evals, _ = load()
    killed = survived = 0
    rows = []
    for dead, row in checker_mutants(binary):
        killed += dead
        survived += not dead
        rows.append(f"{'killed  ' if dead else 'SURVIVED'} {row}")
    for _, e in task_evals(evals):
        tmp = tempfile.mkdtemp(prefix="skill-mutant-")
        mut = os.path.join(tmp, "agent-deck")
        shutil.copytree(SKILL_DIR, mut)
        for src in e["sources"]:
            rel, heading = split_source(src)
            text = read(mut, rel)
            if text is None:
                continue
            body = section(text, heading)
            if body is not None:
                text = text.replace(body, "", 1) if heading else ""
                with open(os.path.join(mut, rel), "w", encoding="utf-8") as f:
                    f.write(text)
        dead = bool(grounding_errors(mut, e))
        shutil.rmtree(tmp, ignore_errors=True)
        killed += dead
        survived += not dead
        rows.append(f"{'killed  ' if dead else 'SURVIVED'} eval {e['id']}: removed {', '.join(e['sources'])}")
    desc = frontmatter_description(SKILL_DIR)
    for i, t in triggers(evals):
        tmp = tempfile.mkdtemp(prefix="skill-mutant-")
        mut = os.path.join(tmp, "agent-deck")
        shutil.copytree(SKILL_DIR, mut, ignore=shutil.ignore_patterns("scripts", "tests"))
        garbled = desc
        for term in t["terms"]:
            if t["should_trigger"] or t.get("near_miss"):
                garbled = re.sub(term, "xxxx", garbled, flags=re.I)
            else:  # creep: the description starts to claim the unrelated topic
                garbled = desc + " Also for " + term.split("|")[0].replace("\\", "").replace("?", "") + "."
        path = os.path.join(mut, "SKILL.md")
        with open(path, encoding="utf-8") as f:
            text = f.read()
        with open(path, "w", encoding="utf-8") as f:
            f.write(text.replace(desc, garbled, 1))
        dead = bool(trigger_errors(mut, i, t))
        shutil.rmtree(tmp, ignore_errors=True)
        killed += dead
        survived += not dead
        how = "garbled" if t["should_trigger"] or t.get("near_miss") else "added to the description"
        rows.append(f"{'killed  ' if dead else 'SURVIVED'} trigger {i}: {how} {t['terms']}")
    for r in rows:
        print(r)
    print(f"mutate: {killed} killed, {survived} survived: {'PASS' if not survived else 'FAIL'}")
    return 1 if survived else 0


def grade_one(e, answer):
    checks = e.get("checks", {})
    missing = [p for p in checks.get("must", []) if not match(p, answer)]
    wrong = [p for p in checks.get("must_not", []) if match(p, answer)]
    return not missing and not wrong, missing, wrong


def cmd_grade(path):
    evals, _ = load()
    with open(path) as f:
        data = json.load(f)
    answers = {str(a["id"]): a["answer"] for a in data["answers"]}
    by_area = {}
    fails = 0
    for _, e in task_evals(evals):
        eid = str(e["id"])
        if eid not in answers:
            continue
        ok, missing, wrong = grade_one(e, answers[eid])
        area = e.get("area", "agent-friendliness")
        tot = by_area.setdefault(area, [0, 0])
        tot[0] += ok
        tot[1] += 1
        fails += not ok
        detail = "" if ok else f"  missing={missing} wrong={wrong}"
        print(f"{'pass' if ok else 'FAIL'} {eid} [{area}]{detail}")
    unknown = sorted(set(answers) - {str(e["id"]) for _, e in task_evals(evals)})
    for u in unknown:
        print(f"FAIL {u}: no such eval")
    total = sum(v[1] for v in by_area.values())
    for area, (ok, n) in sorted(by_area.items()):
        print(f"area {area}: {ok}/{n}")
    print(f"grade: {total - fails}/{total} passed")
    return 1 if fails or unknown else 0


def cmd_coverage(md):
    evals, promises = load()
    cov = coverage(evals, promises)
    if md:
        print("| Promise | Area | Kind | Where | Status | Checked by |")
        print("|---|---|---|---|---|---|")
    for p in promises:
        status, what = cov[p["id"]]
        where = p["file"] + (f" § {p['heading']}" if p.get("heading") else "")
        if md:
            print(f"| `{p['id']}` {p['summary']} | {p['area']} | {p['kind']} | {where} | {status} | {', '.join(what)} |")
        else:
            print(f"{status:12} {p['id']:40} {', '.join(what)}")
    n = sum(1 for s, _ in cov.values() if s == "covered")
    d = sum(1 for s, _ in cov.values() if s == "drift-only")
    print(f"\ncoverage: {n}/{len(promises)} covered by evals or tests ({100 * n // max(1, len(promises))}%), "
          f"{d} drift-checked only")
    return 0


def main(argv):
    if not argv or argv[0] in ("-h", "--help"):
        print(__doc__)
        return 0 if argv else 2
    cmd, rest = argv[0], argv[1:]
    if cmd == "static":
        binary = None
        if "--binary" in rest:
            binary = os.path.abspath(rest[rest.index("--binary") + 1])
        return 1 if run_static(SKILL_DIR, binary) else 0
    if cmd == "mutate":
        binary = os.path.abspath(rest[rest.index("--binary") + 1]) if "--binary" in rest else None
        return cmd_mutate(binary)
    if cmd == "grade" and rest:
        return cmd_grade(rest[0])
    if cmd == "coverage":
        return cmd_coverage("--md" in rest)
    print(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
