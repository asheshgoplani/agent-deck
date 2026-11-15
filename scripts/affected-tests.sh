#!/usr/bin/env bash
# affected-tests.sh: pick the Go packages a change can affect, for the dev loop.
#
#   scripts/affected-tests.sh [options] [base]      print the affected packages
#   scripts/affected-tests.sh --run [base] [-- go-test-args...]
#                                                   run only those via deck-test
#
# base defaults to the merge-base of HEAD and origin/main (or main). The change
# set is `git diff base...HEAD` plus, unless --head is given, staged, unstaged
# and untracked files, so it works mid-edit.
#
# Options:
#   --explain        print every changed file, why each package was chosen, and
#                    anything that forced widening
#   --list           one package per line, nothing else (default prints a summary
#                    and the packages on one line)
#   --run            run the affected packages with `deck-test -count=1 -timeout 20m` (or
#                    `go test -count=1` when deck-test is absent or --local)
#   --local          with --run: use plain `go test`
#   --head <ref>     diff base...<ref> only, ignore the working tree (for
#                    measuring a past commit; the tree is not checked out)
#   -h, --help
#
# Selection rules (conservative: when unsure, run more):
#   1. go.mod, go.sum, go.work*, CI test plumbing (go-test.yml, known-flaky,
#      ci-test-summary, scripts/ci*) => full suite (./...).
#   2. A changed .go file (including *_test.go, ignored-by-build-tag files and
#      testdata) marks its package directory.
#   3. Any other changed file (golden, //go:embed target, fixture) marks the
#      nearest enclosing package directory. Files outside every package (docs/,
#      skills/, root files) mark each package whose .go sources mention the
#      file's repo path or, for common names, its base name.
#   4. Every module package that imports a marked package, directly or
#      transitively (non-test and test imports, in-package and external test
#      packages, and build-tag-ignored files), is added.
#   5. A test line that runs go build/run/test/vet against ./<package dir>
#      (for example `go build ./cmd/agent-deck`) counts as a dependency on it.
#   6. A deleted package directory or a file the tool cannot place widens to
#      the full suite.
# Not covered: shell-only tests under tests/ (run those directly) and
# cross-package effects through the filesystem at runtime beyond rule 3/5.
# PR CI, merge and release still run the whole suite.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
exec python3 -I - "$@" <<'PY'
import json, os, re, subprocess, sys, shutil

def sh(*a, check=True):
    r = subprocess.run(a, capture_output=True, text=True)
    if check and r.returncode:
        sys.exit("affected-tests: %s failed: %s" % (" ".join(a), r.stderr.strip()))
    return r.stdout

args = sys.argv[1:]
explain = listonly = run = local = False
head = None
base = None
passthru = []
i = 0
while i < len(args):
    a = args[i]
    if a == "--":
        passthru = args[i + 1:]; break
    elif a == "--explain": explain = True
    elif a == "--list": listonly = True
    elif a == "--run": run = True
    elif a == "--local": local = True
    elif a == "--head": i += 1; head = args[i]
    elif a in ("-h", "--help"):
        print(open(os.environ.get("AFFECTED_SELF", "scripts/affected-tests.sh")).read().split("set -euo")[0])
        sys.exit(0)
    elif a.startswith("-"): sys.exit("affected-tests: unknown option " + a)
    else: base = a
    i += 1

if base is None:
    for ref in ("origin/main", "main"):
        if subprocess.run(["git", "rev-parse", "-q", "--verify", ref], capture_output=True).returncode == 0:
            base = sh("git", "merge-base", "HEAD", ref).strip(); break
    if base is None: sys.exit("affected-tests: no base given and no origin/main or main found")

# ---- changed files -------------------------------------------------------
changed = set(sh("git", "diff", "--name-only", "--no-renames", "%s...%s" % (base, head or "HEAD")).split("\n"))
if head is None:
    changed |= set(sh("git", "diff", "--name-only", "--no-renames", "HEAD").split("\n"))
    changed |= set(sh("git", "ls-files", "--others", "--exclude-standard").split("\n"))
changed = sorted(c for c in changed if c)

# ---- package graph -------------------------------------------------------
mod = sh("go", "list", "-m").strip()
raw = sh("go", "list", "-e", "-json", "./...")
pkgs = {}
dec = json.JSONDecoder(); pos = 0
while pos < len(raw):
    while pos < len(raw) and raw[pos].isspace(): pos += 1
    if pos >= len(raw): break
    o, pos = dec.raw_decode(raw, pos)
    pkgs[o["ImportPath"]] = o
root = os.getcwd()
def rel(p): return os.path.relpath(p["Dir"], root)
bydir = {rel(p): ip for ip, p in pkgs.items()}
imp_re = re.compile(r'"(%s(?:/[^"\s]*)?)"' % re.escape(mod))

# edges: ip -> set of module packages it (or its tests) depend on, with a reason
deps = {ip: {} for ip in pkgs}
def add(ip, dep, why):
    if dep in pkgs and dep != ip: deps[ip].setdefault(dep, why)
for ip, p in pkgs.items():
    for k, why in (("Imports", "imports"), ("TestImports", "test imports"), ("XTestImports", "external test imports")):
        for d in p.get(k) or []: add(ip, d, why)
    d = rel(p)
    for f in p.get("IgnoredGoFiles") or []:
        try: txt = open(os.path.join(d, f)).read()
        except OSError: continue
        for m in imp_re.findall(txt): add(ip, m, "imports (build-tag-ignored %s)" % f)
# rule 5: test files that name another package dir as a string
dirs = sorted((d for d in bydir if d != "."), key=len, reverse=True)
dir_re = re.compile(r'(?<![\w/.-])\./(%s)(?![\w-])' % "|".join(re.escape(d) for d in dirs)) if dirs else None
goline_re = re.compile(r'\b(build|run|install|vet|test|list)\b')
texts = {}  # ip -> {dep: why}: test-only edges, never propagated to importers
gosrc = {}  # ip -> concatenated sources, for rule 3 mention search
for ip, p in pkgs.items():
    d = rel(p); chunks = []
    files = (p.get("GoFiles") or []) + (p.get("TestGoFiles") or []) + (p.get("XTestGoFiles") or []) + (p.get("IgnoredGoFiles") or [])
    for f in files:
        try: txt = open(os.path.join(d, f)).read()
        except OSError: continue
        chunks.append(txt)
        if f.endswith("_test.go") and dir_re:
            for line in txt.split("\n"):
                if goline_re.search(line):
                    for m in set(dir_re.findall(line)):
                        texts.setdefault(ip, {}).setdefault(bydir[m], "test file %s runs go against ./%s" % (f, m))
    gosrc[ip] = "\n".join(chunks)

rdeps = {ip: {} for ip in pkgs}
for ip, ds in deps.items():
    for d, why in ds.items(): rdeps[d].setdefault(ip, why)

# ---- map changes to packages --------------------------------------------
FULL_RE = re.compile(r'^(go\.mod|go\.sum|go\.work(\.sum)?|\.github/workflows/go-test\.yml|tests/known-flaky\.txt|scripts/ci-[^/]*|scripts/ci/.*)$')
direct = {}   # ip -> [reasons]
notes = []
full = []
def mark(ip, why): direct.setdefault(ip, []).append(why)
generic = {"README.md", "CHANGELOG.md", "LICENSE", "Makefile", ".gitignore", "index.html"}
for f in changed:
    if FULL_RE.match(f):
        full.append("%s changes the build or the CI test plan" % f); continue
    d = os.path.dirname(f) or "."
    # nearest enclosing package directory (excluding the module root)
    cur = d; hit = None
    while cur not in ("", "."):
        if cur in bydir: hit = cur; break
        cur = os.path.dirname(cur)
    if hit:
        mark(bydir[hit], "%s is inside %s" % (f, hit)); continue
    if f.endswith(".go"):
        # a .go file in a directory that is no longer (or not yet) a package
        if os.path.exists(f) or True:
            full.append("%s sits in %s, which is not a Go package (deleted or empty); cannot place it" % (f, d))
        continue
    # outside every package: who mentions it?
    base_name = os.path.basename(f)
    needles = [f] + ([] if base_name in generic else [base_name])
    found = [ip for ip, s in gosrc.items() if any(n in s for n in needles)]
    if f.startswith("tests/") and f.endswith(".sh"):
        notes.append("%s is a shell test; run it directly (not a Go test)" % f); continue
    if found:
        for ip in found: mark(ip, "%s is mentioned in its Go sources" % f)
    else:
        notes.append("%s is outside every package and no Go source mentions it: no Go test impact" % f)

selected = {}   # ip -> why
for ip, rs in direct.items(): selected[ip] = "changed: " + "; ".join(rs[:3]) + (" (+%d more)" % (len(rs) - 3) if len(rs) > 3 else "")
queue = list(direct)
while queue:
    ip = queue.pop()
    for r, why in rdeps[ip].items():
        if r not in selected:
            selected[r] = "%s %s" % (why, ip[len(mod) + 1:] or ip)
            queue.append(r)

# rule 5 edges are test-only: the importer's tests build/run dep, but packages that
# import the importer do not inherit that, so they are added one level, not closed.
for ip, ds in texts.items():
    for d, why in ds.items():
        if d in selected and ip not in selected:
            selected[ip] = why
allp = sorted(pkgs)
if full:
    out = ["./..."]; reason = "full suite: " + "; ".join(full[:3])
else:
    out = ["./" + ip[len(mod) + 1:] if ip != mod else "." for ip in sorted(selected)]
    reason = "%d of %d packages" % (len(out), len(allp))

# ---- report --------------------------------------------------------------
if listonly:
    print("\n".join(out))
elif not run or explain:
    p = (lambda *a: print(*a, file=sys.stderr)) if run else print
    p("base %s -> %s; %d changed files; %s" % (base[:10], head or "HEAD+worktree", len(changed), reason))
    if explain:
        p("\nchanged files:")
        for f in changed: p("  " + f)
        if full:
            p("\nwidened to the full suite because:")
            for w in full: p("  " + w)
        p("\npackages (direct = changed itself, else via dependency):")
        for ip in sorted(selected):
            p("  %-45s %s" % (ip[len(mod) + 1:] or ".", selected[ip]))
        if notes:
            p("\nnotes:")
            for n in notes: p("  " + n)
    if not full: p("\n" + " ".join(out)) if not run else None
    elif not explain: p(" ".join(out))
if run:
    if not out: print("affected-tests: nothing affected, no Go tests to run", file=sys.stderr); sys.exit(0)
    if not full and not selected: sys.exit(0)
    runner = ["go", "test", "-count=1"] if (local or not shutil.which("deck-test")) else ["deck-test", "-count=1"]
    # CI runs -timeout 20m per package; go's 10m default kills internal/session.
    if not any(a.startswith(("-timeout", "--timeout")) for a in passthru): passthru = ["-timeout", "20m"] + passthru
    cmd = runner + passthru + out
    print("+ " + " ".join(cmd[:8]) + (" ... (%d packages)" % len(out) if len(out) > 5 else " " + " ".join(cmd[8:])), file=sys.stderr)
    os.execvp(cmd[0], cmd)
PY
