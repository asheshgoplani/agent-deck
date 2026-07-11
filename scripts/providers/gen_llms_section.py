#!/usr/bin/env python3
"""Refresh the '# PROVIDER API REFERENCES' section of llms-full.txt.

For each docs/providers/<name>/README.md, append its summary to llms-full.txt with
relative links rewritten to repo-root-relative paths so an LLM reading the flat
bundle can locate the full files. Summary-only — full endpoint detail is referenced
by path, not inlined (keeps llms-full.txt lean as providers grow).

Idempotent: re-running replaces any existing provider section.
"""
import os, re, glob

HERE = os.path.dirname(os.path.abspath(__file__))


def _repo_root():
    d = HERE
    while d != os.path.dirname(d):
        if os.path.exists(os.path.join(d, "go.mod")):
            return d
        d = os.path.dirname(d)
    raise SystemExit("repo root (go.mod) not found above " + HERE)


REPO = _repo_root()
LLMS = os.path.join(REPO, "llms-full.txt")
PROVIDERS_DIR = os.path.join(REPO, "docs", "providers")
MARKER = "# PROVIDER API REFERENCES"

# link targets that are already absolute (http), anchor, or repo-root-relative
def rewrite_links(md, provider_name):
    base = f"docs/providers/{provider_name}/"
    out = []
    for line in md.splitlines():
        def repl(m):
            text, target = m.group(1), m.group(2)
            if target.startswith(("http://", "https://", "#", "mailto:")):
                return m.group(0)
            if target.startswith("/"):
                return f"[{text}]({target})"
            return f"[{text}]({base}{target})"
        line = re.sub(r"\[([^\]]+)\]\(([^)]+)\)", repl, line)
        out.append(line)
    return "\n".join(out)


def provider_summary(path):
    name = os.path.basename(os.path.dirname(path))
    md = open(path).read()
    # drop front-matter
    md = re.sub(r"^---\n.*?\n---\n", "", md, count=1, flags=re.S)
    # demote the provider's H1 to H2 so it nests under the section
    md = re.sub(r"^# ", "## ", md, flags=re.M)
    # rewrite relative links to repo-root-relative
    md = rewrite_links(md, name)
    # note where the full detail lives
    return f"## {name.title()} — full detail at `docs/providers/{name}/`\n\n{md}"


def main():
    readmes = sorted(glob.glob(os.path.join(PROVIDERS_DIR, "*", "README.md")))
    if not readmes:
        print("no provider READMEs found in", PROVIDERS_DIR)
    section = [f"\n\n{MARKER}", ""]
    section.append("External service-provider API references. Summary only — full per-endpoint")
    section.append("detail (parameters, request bodies, responses, examples) lives in the files")
    section.append("referenced by path under `docs/providers/<name>/`. See")
    section.append("`docs/providers/README.md` for the provider index.")
    section.append("")
    for r in readmes:
        section.append(provider_summary(r))
        section.append("\n---\n")
    new_section = "\n".join(section).rstrip() + "\n"

    existing = open(LLMS).read() if os.path.exists(LLMS) else ""
    # strip any prior provider section (marker to EOF)
    idx = existing.find(MARKER)
    base = existing[:idx].rstrip() if idx != -1 else existing.rstrip()
    with open(LLMS, "w") as f:
        f.write(base + "\n" + new_section)
    print(f"updated {os.path.relpath(LLMS, REPO)}: {len(readmes)} provider(s) ->", ", ".join(os.path.basename(os.path.dirname(r)) for r in readmes))


if __name__ == "__main__":
    main()
