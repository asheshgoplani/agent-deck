#!/usr/bin/env bash
# Render an orchestrate child prompt from a template.
#
#   bash "$RUN_DIR/prompts/render.sh" <template> <out-file> KEY=value KEY@=path ...
#
# The point is context, not convenience: the template body never passes through
# the conductor's context. A conductor that writes prompts with `cat > f <<EOF`
# pays the full template — ~6k characters — on every implementer, every
# reviewer and every fix round, and pays it forever, because a tool call stays
# in the transcript. Rendering costs the varying part only.
#
#   KEY=value   inline substitution for {{KEY}} (small values)
#   KEY@=path   substitute the CONTENTS of path (findings lists, specs, issue
#               bodies) — the whole reason a fix round costs the conductor
#               nothing: the findings go file -> prompt without a detour
#               through the conversation.
#
# Templates may also carry {{include:other.md}}, resolved against this
# directory before variable substitution.
#
# Substitution is literal: findings lists and issue bodies are full of
# backticks, backslashes and $, and a shell-expanding here-doc mangles them.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"

if [ "$#" -lt 2 ]; then
  echo "usage: render.sh <template> <out-file> [KEY=value|KEY@=path ...]" >&2
  echo "templates: $(cd "$DIR" && ls *.md 2>/dev/null | sed 's/\.md$//' | tr '\n' ' ')" >&2
  exit 2
fi

TEMPLATE="$1"; shift
OUT="$1"; shift
case "$TEMPLATE" in
  */*|*.md) TPL="$TEMPLATE" ;;
  *)        TPL="$DIR/$TEMPLATE.md" ;;
esac
[ -f "$TPL" ] || { echo "render.sh: no such template: $TPL" >&2; exit 2; }

mkdir -p "$(dirname "$OUT")"
TPL="$TPL" OUT="$OUT" DIR="$DIR" python3 - "$@" <<'PY'
import os, re, sys

tpl_path, out_path, inc_dir = os.environ["TPL"], os.environ["OUT"], os.environ["DIR"]

vals = {}
for arg in sys.argv[1:]:
    key, sep, value = arg.partition("=")
    if not sep:
        sys.exit(f"render.sh: argument is not KEY=value or KEY@=path: {arg}")
    if key.endswith("@"):
        with open(value, encoding="utf-8") as fh:
            vals[key[:-1]] = fh.read().rstrip("\n")
    else:
        vals[key] = value

with open(tpl_path, encoding="utf-8") as fh:
    text = fh.read()

# Includes first, so an included fragment's own placeholders get filled too.
def include(match):
    name = match.group(1)
    with open(os.path.join(inc_dir, name), encoding="utf-8") as fh:
        return fh.read().rstrip("\n")

for _ in range(5):
    text, n = re.subn(r"\{\{include:([A-Za-z0-9_.-]+)\}\}", include, text)
    if not n:
        break

# str.replace, never re.sub: a findings list containing \1 or & must land
# verbatim, and a regex replacement would silently eat it.
for key, value in vals.items():
    text = text.replace("{{" + key + "}}", value)

leftover = sorted(set(re.findall(r"\{\{([A-Za-z0-9_]+)\}\}", text)))
if leftover:
    # Fail loud. A child handed a prompt still containing {{BASE_BRANCH}}
    # improvises around it, and the run finds out one review round later.
    sys.exit("render.sh: unfilled placeholders: " + ", ".join(leftover))

with open(out_path, "w", encoding="utf-8") as fh:
    fh.write(text if text.endswith("\n") else text + "\n")
print(f"rendered {out_path} ({len(text)} chars)")

# A child prompt this large is almost always a spec or findings list pasted in
# whole. It is not a hard error — agent-deck spills an oversize prompt to a file
# rather than failing the launch — but every one of these characters is context
# the child spends before it starts, so say so once, here, where the fix is one
# argument away.
if len(text) > 8000:
    sys.stderr.write(
        f"render.sh: WARNING {out_path} is {len(text)} chars (>8000). "
        "Pass large specs and findings by PATH (a line the child reads itself) "
        "instead of KEY@=path inlining, unless the child truly needs the body.\n"
    )
PY
