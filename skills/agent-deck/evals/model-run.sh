#!/usr/bin/env bash
# Model-graded task evals: real Claude Code sessions answer a skill's task evals, and
# check.py grades the answers. Never `claude -p`: each batch of evals is one interactive
# Claude session that agent-deck launches in a throwaway HOME on a private tmux socket.
#
#   skills/agent-deck/evals/model-run.sh [--skill agent-deck|watcher-creator]
#       [--skill-dir DIR --evals-dir DIR] [--mode with|without] [--runs N] [--batch K]
#       [--parallel P] [--ids 1,2,af-1] [--token-secret SCOPE/NAME | --token-file PATH]
#       [--binary PATH] [--work-dir DIR] [--log-dir DIR] [--deadline MIN] [--keep] [--preflight]
#
#   --skill NAME      a skill of this repo (default agent-deck); --skill-dir and
#                     --evals-dir name any other skill (check.py's global options).
#   --mode with       the agent reads only a copy of the skill (never evals/), then answers.
#   --mode without    no skill; the agent may run only `agent-deck <cmd> --help`.
#   --runs N          repeat the run N times (default 3 with, 1 without); an eval that
#                     passes 1..N-1 times is flaky.
#   --batch K         evals per session (default 20); --parallel P sessions at once
#                     (default 4, max 6).
#   --token-secret S  the DEDICATED Claude token the sessions run on, read with
#                     `deck-secret to-file` into the sandbox (default
#                     conductor/CLAUDE_DEMO_TOKEN; $DECK_SECRET overrides the binary).
#   --token-file PATH or a dedicated token file (`claude setup-token`, chmod 600).
#                     Never a live login: a ~/.claude*/.credentials.json shared with the
#                     sandbox gets refreshed there, which logs out every other session on
#                     that account. The run refuses to start without a dedicated token,
#                     and refuses a login file passed as one.
#   --preflight       only check and fetch the token, then exit (no build, no sessions).
#   --deadline MIN    per-session deadline (default 20, max 30).
#
# Output in --log-dir: answers-<mode>-<run>.json, grade-<mode>-<run>.log, one pane and
# transcript copy per session, and summary.json / summary.md (pass counts, flaky evals,
# in/out tokens per session, the skill's git sha and content hash). See RUNNER.md.
set -u
EVALS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$EVALS_DIR/../../.." && pwd)"
CHECK="$EVALS_DIR/check.py"
SKILL="agent-deck" SKILL_DIR="" SKILL_EVALS="" MODE="with" RUNS="" BATCH=20 PAR=4 IDS=""
BINARY="" WORK_DIR="${TMPDIR:-/tmp}" LOG_DIR="" DEADLINE=20 KEEP="" PREFLIGHT=""
TOKEN_SECRET="conductor/CLAUDE_DEMO_TOKEN" TOKEN_FILE="" DECK_SECRET="${DECK_SECRET:-deck-secret}"
while [ $# -gt 0 ]; do
    case "$1" in
        --skill) SKILL="$2"; shift 2 ;;
        --skill-dir) SKILL_DIR="$2"; shift 2 ;;
        --evals-dir) SKILL_EVALS="$2"; shift 2 ;;
        --mode) MODE="$2"; shift 2 ;;
        --runs) RUNS="$2"; shift 2 ;;
        --batch) BATCH="$2"; shift 2 ;;
        --parallel) PAR="$2"; shift 2 ;;
        --ids) IDS="$2"; shift 2 ;;
        --token-secret) TOKEN_SECRET="$2"; shift 2 ;;
        --token-file) TOKEN_FILE="$2"; shift 2 ;;
        --preflight) PREFLIGHT=1; shift ;;
        --binary) BINARY="$2"; shift 2 ;;
        --work-dir) WORK_DIR="$2"; shift 2 ;;
        --log-dir) LOG_DIR="$2"; shift 2 ;;
        --deadline) DEADLINE="$2"; shift 2 ;;
        --keep) KEEP=1; shift ;;
        -h|--help) sed -n '2,34p' "$0"; exit 0 ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done
case "$SKILL" in
    agent-deck) : "${SKILL_DIR:=$REPO_DIR/skills/agent-deck}" "${SKILL_EVALS:=$EVALS_DIR}" ;;
    watcher-creator) : "${SKILL_DIR:=$REPO_DIR/cmd/agent-deck/assets/skills/watcher-creator}" \
                       "${SKILL_EVALS:=$REPO_DIR/tests/skill-evals/watcher-creator}" ;;
    *) [ -n "$SKILL_DIR" ] && [ -n "$SKILL_EVALS" ] || { echo "unknown skill $SKILL: pass --skill-dir and --evals-dir" >&2; exit 2; } ;;
esac
case "$MODE" in with) : "${RUNS:=3}" ;; without) : "${RUNS:=1}" ;; *) echo "--mode is with or without" >&2; exit 2 ;; esac
[ "$PAR" -ge 1 ] && [ "$PAR" -le 6 ] || { echo "--parallel must be 1..6" >&2; exit 2; }
[ "$DEADLINE" -ge 1 ] && [ "$DEADLINE" -le 30 ] || { echo "--deadline must be 1..30 minutes" >&2; exit 2; }
SKILL_DIR="$(cd "$SKILL_DIR" && pwd)" && SKILL_EVALS="$(cd "$SKILL_EVALS" && pwd)" || exit 2

# A dedicated token or nothing: checked before any sandbox exists.
is_login_file() {  # a Claude login (has a refresh token), not a token
    case "$(basename "$1")" in .credentials.json|credentials.json) return 0 ;; esac
    grep -qE '"(claudeAiOauth|refreshToken|refresh_token)"' "$1" 2>/dev/null
}
if [ -n "$TOKEN_FILE" ]; then
    [ -f "$TOKEN_FILE" ] || { echo "--token-file $TOKEN_FILE: no such file" >&2; exit 2; }
    if is_login_file "$TOKEN_FILE"; then
        echo "refusing --token-file $TOKEN_FILE: it is a Claude login, not a dedicated token; sharing it logs out every session on that account (use claude setup-token)" >&2
        exit 2
    fi
    TOKEN_FILE="$(cd "$(dirname "$TOKEN_FILE")" && pwd)/$(basename "$TOKEN_FILE")"
elif ! command -v "$DECK_SECRET" >/dev/null 2>&1; then
    echo "model runs need a dedicated Claude token: $TOKEN_SECRET in deck-secret, or --token-file; never a live login (no deck-secret found)" >&2
    exit 2
elif ! "$DECK_SECRET" exists "$TOKEN_SECRET" >/dev/null 2>&1; then  # exit 3 when missing
    echo "model runs need a dedicated Claude token: $TOKEN_SECRET is not in deck-secret; the owner creates it (claude setup-token, then deck-secret set $TOKEN_SECRET), or pass --token-file" >&2
    exit 2
fi
SOCK="skill-model-$$"

mkdir -p "$WORK_DIR" && WORK_DIR="$(cd "$WORK_DIR" && pwd)" || exit 2
ROOT="$(mktemp -d "$WORK_DIR/admr.XXXXXX")"
[ "${#ROOT}" -le 60 ] || { echo "work dir path too long for a tmux socket: $ROOT (use --work-dir)" >&2; exit 2; }
H="$ROOT/home" CC="$ROOT/.claude"  # basename .claude: agent-deck infers no profile from it
if [ -z "$LOG_DIR" ]; then LOG_DIR="$(mktemp -d "$WORK_DIR/skill-model-run.XXXXXX")"; fi
mkdir -p "$LOG_DIR" && LOG_DIR="$(cd "$LOG_DIR" && pwd)"
GUARD_LOG="$LOG_DIR/tmux-calls.log"
: >"$GUARD_LOG"

trash() { if command -v gio >/dev/null 2>&1; then gio trash "$1" 2>/dev/null || rm -rf "$1"; else rm -rf "$1"; fi; }
SESSIONS=()
cleanup() {
    for s in "${SESSIONS[@]}"; do ad session stop "$s" >/dev/null 2>&1; ad remove "$s" >/dev/null 2>&1; done
    if [ -n "$KEEP" ]; then echo "kept sandbox: $ROOT"; else trash "$ROOT"; fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

TOKEN_PATH="$ROOT/claude-token"
if [ -n "$TOKEN_FILE" ]; then
    TOKEN_PATH="$TOKEN_FILE" TOKEN_FROM="--token-file $TOKEN_FILE"
else
    "$DECK_SECRET" to-file "$TOKEN_SECRET" "$TOKEN_PATH" || { echo "deck-secret to-file $TOKEN_SECRET failed" >&2; exit 2; }
    TOKEN_FROM="deck-secret $TOKEN_SECRET"
fi
[ -s "$TOKEN_PATH" ] || { echo "dedicated token is empty: $TOKEN_FROM" >&2; exit 2; }
if is_login_file "$TOKEN_PATH"; then echo "refusing $TOKEN_FROM: it holds a Claude login, not a dedicated token" >&2; exit 2; fi
mode="$(stat -c %a "$TOKEN_PATH" 2>/dev/null || stat -f %Lp "$TOKEN_PATH")"
[ "$mode" = 600 ] || [ "$mode" = 400 ] || { echo "dedicated token $TOKEN_PATH has mode $mode; chmod 600 it" >&2; exit 2; }
echo "token: $TOKEN_FROM, mode $mode (dedicated; no live login is used)"
[ -n "$PREFLIGHT" ] && { echo "preflight ok"; exit 0; }

CLAUDE_BIN="$(command -v claude || true)"
REAL_TMUX="$(command -v tmux || true)"
[ -n "$CLAUDE_BIN" ] && [ -n "$REAL_TMUX" ] || { echo "model runs need claude and tmux on PATH" >&2; exit 2; }
CLAUDE_BIN="$(readlink -f "$CLAUDE_BIN")"

if [ -z "$BINARY" ]; then
    command -v go >/dev/null || { echo "go not found; pass --binary" >&2; exit 2; }
    (cd "$REPO_DIR" && go build -o "$ROOT/agent-deck" ./cmd/agent-deck) >"$LOG_DIR/build.log" 2>&1 \
        || { echo "build failed (see $LOG_DIR/build.log)" >&2; exit 2; }
    BINARY="$ROOT/agent-deck"
fi
BINARY="$(cd "$(dirname "$BINARY")" && pwd)/$(basename "$BINARY")"

mkdir -p "$ROOT/bin" "$ROOT/tmux" "$H/.config/agent-deck" "$H/.cache" "$H/.local/share" "$H/.local/state" "$H/work" "$CC"
cp "$EVALS_DIR/flows/tmux-guard" "$ROOT/bin/tmux" && chmod 700 "$ROOT/bin/tmux"
ln -s "$BINARY" "$ROOT/bin/agent-deck"
ln -s "$CLAUDE_BIN" "$ROOT/bin/claude"
# A fresh Claude config: no login in it. The sessions authenticate with the dedicated
# token only, exported from [claude] env_file (the file names the token, never holds it).
printf 'export CLAUDE_CODE_OAUTH_TOKEN="$(cat %q)"\n' "$TOKEN_PATH" >"$ROOT/claude.env"
chmod 600 "$ROOT/claude.env"
python3 -I - "$CC" "$("$CLAUDE_BIN" --version 2>/dev/null | cut -d' ' -f1)" <<'EOF'
import json, sys
json.dump({"hasCompletedOnboarding": True, "lastOnboardingVersion": sys.argv[2] or "0",
           "hasResetAutoModeOptInForDefaultOffer": True}, open(sys.argv[1] + "/.claude.json", "w"))
# agent-deck passes --dangerously-skip-permissions ([claude] dangerous_mode); a defaultMode
# here would make Claude offer "make auto mode your default?" over the composer.
json.dump({"skipDangerousModePermissionPrompt": True},
          open(sys.argv[1] + "/settings.json", "w"))
EOF
cat >"$H/.config/agent-deck/config.toml" <<EOF
[tmux]
socket_name = "$SOCK"
launch_in_user_scope = false
[telemetry]
disabled = true
[updates]
auto_update = false
check_enabled = false
[launch]
inject_identity = false
[claude]
dangerous_mode = true
config_dir = "$CC"
env_file = "$ROOT/claude.env"
EOF

ENV=(env -i
    HOME="$H" XDG_CONFIG_HOME="$H/.config" XDG_CACHE_HOME="$H/.cache"
    XDG_DATA_HOME="$H/.local/share" XDG_STATE_HOME="$H/.local/state"
    PATH="$ROOT/bin:/usr/local/bin:/usr/bin:/bin" SHELL=/bin/bash
    LANG=C.UTF-8 LC_ALL=C.UTF-8 TERM=xterm-256color
    AGENTDECK_PROFILE=default AGENTDECK_SKIP_UPDATE_CHECK=1 AGENTDECK_TELEMETRY=0 DO_NOT_TRACK=1 DISABLE_AUTOUPDATER=1
    TMUX_TMPDIR="$ROOT/tmux" CLAUDE_CONFIG_DIR="$CC" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
    AGENT_FLOW_SOCKET="$SOCK" AGENT_FLOW_REAL_TMUX="$REAL_TMUX" AGENT_FLOW_TMUX_LOG="$GUARD_LOG")
ad() { (cd "$H/work" && timeout 120 "${ENV[@]}" "$BINARY" "$@"); }
check() { python3 -I "$CHECK" --skill-dir "$SKILL_DIR" --evals-dir "$SKILL_EVALS" "$@"; }

# The prompts only: no expected output, no checks.
check prompts >"$ROOT/prompts.json" || { echo "check.py prompts failed" >&2; exit 2; }
NAME="$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["skill_name"])' "$ROOT/prompts.json")"
python3 -I - "$ROOT/prompts.json" "$BATCH" "$IDS" "$ROOT/batches" <<'EOF'
import json, os, sys
data, k, ids, out = json.load(open(sys.argv[1])), int(sys.argv[2]), sys.argv[3], sys.argv[4]
evals = data["evals"]
if ids:
    want = set(ids.split(","))
    evals = [e for e in evals if str(e["id"]) in want]
os.makedirs(out, exist_ok=True)
for n, i in enumerate(range(0, len(evals), k)):
    json.dump({"tasks": [{"id": e["id"], "prompt": e["prompt"]} for e in evals[i:i + k]]},
              open(os.path.join(out, f"b{n:02d}.json"), "w"), indent=1)
EOF

task_md() {  # task_md <mode>: the instructions every session gets
    if [ "$1" = with ]; then cat <<EOF
You are being evaluated on how well you use the skill \`$NAME\`. A copy of the skill is in
\`./skill/\`. Read \`./skill/SKILL.md\` first, then only the files under \`./skill/\` that
you need. Do not read anything outside this directory, do not search the web, and do not
run agent-deck or any other command except to read files under \`./skill/\`.
EOF
    else cat <<EOF
You are being evaluated on what you know without help. You may run \`agent-deck <command> --help\`
(and \`agent-deck --help\`) and nothing else: no other commands, no files outside this
directory, no web.
EOF
    fi
    cat <<'EOF'

`tasks.json` lists tasks, each a message a user sent you. For each one, write the answer you
would give that user: the exact commands, flags, file paths, values and caveats you would
tell them. Be concrete and complete; do not ask questions back. Do not carry out the tasks.

Write all answers to `answers.json` in this directory, exactly in this shape, one entry per
task id, then reply with the single word DONE:

{"answers": [{"id": <task id>, "answer": "<your full answer>"}]}
EOF
}

run_batch() {  # run_batch <run> <batch file>: one session; writes answers into its dir
    local run="$1" bf="$2" b; b="$(basename "$bf" .json)"
    local title="eval-$MODE-r$run-$b" dir="$H/work/$MODE-r$run-$b"
    mkdir -p "$dir"
    cp "$bf" "$dir/tasks.json"
    task_md "$MODE" >"$dir/TASK.md"
    if [ "$MODE" = with ]; then
        mkdir -p "$dir/skill"
        (cd "$SKILL_DIR" && tar --exclude=./evals --exclude=./tests --exclude=__pycache__ -cf - .) | (cd "$dir/skill" && tar -xf -)
    fi
    python3 -I - "$CC/.claude.json" "$dir" <<'EOF'
import fcntl, json, sys
with open(sys.argv[1] + ".lock", "w") as lk:
    fcntl.flock(lk, fcntl.LOCK_EX)
    d = json.load(open(sys.argv[1]))
    d.setdefault("projects", {})[sys.argv[2]] = {"hasTrustDialogAccepted": True, "hasCompletedProjectOnboarding": True}
    json.dump(d, open(sys.argv[1], "w"))
EOF
    local log="$LOG_DIR/session-$title.log"
    { echo "start $(date -Is)"; ad add "$dir" -t "$title" -c claude --no-identity; ad session start "$title"; } >>"$log" 2>&1
    local try
    for try in 1 2 3 4 5 6; do  # the composer can be busy with a startup notice
        ad session send "$title" "Read TASK.md in this directory and follow it." >>"$log" 2>&1 && break
        sleep 10
    done
    local end=$((SECONDS + DEADLINE * 60)) nudged="" ok=""
    while [ $SECONDS -lt $end ]; do
        if python3 -I - "$dir/answers.json" "$dir/tasks.json" <<'EOF' 2>/dev/null
import json, sys
a = {str(x["id"]) for x in json.load(open(sys.argv[1]))["answers"]}
t = {str(x["id"]) for x in json.load(open(sys.argv[2]))["tasks"]}
sys.exit(0 if t <= a else 1)
EOF
        then ok=1; break; fi
        local st; st="$(ad session show "$title" --json 2>/dev/null | python3 -I -c 'import json,sys; print(json.load(sys.stdin).get("status",""))' 2>/dev/null)"
        if [ -z "$nudged" ] && { [ "$st" = waiting ] || [ "$st" = idle ]; } && [ $((end - SECONDS)) -lt $((DEADLINE * 60 - 120)) ]; then
            ad session send "$title" "answers.json is missing or incomplete: write every task's answer to answers.json as TASK.md says, then reply DONE." >>"$log" 2>&1
            nudged=1
        fi
        sleep 10
    done
    echo "end $(date -Is) answers=${ok:-missing}" >>"$log"
    env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$ROOT/tmux" "$REAL_TMUX" -L "$SOCK" capture-pane -p -S -200 -t "$(ad session show "$title" --json 2>/dev/null | python3 -I -c 'import json,sys; print(json.load(sys.stdin).get("tmux_session",""))' 2>/dev/null)" \
        >"$LOG_DIR/pane-$title.txt" 2>/dev/null </dev/null || true
    [ -f "$dir/answers.json" ] && cp "$dir/answers.json" "$LOG_DIR/answers-$title.json"
    ad session stop "$title" >>"$log" 2>&1
    return 0
}

T0="$(date -Is)"
# The skill under test, recorded before the run (a commit made during it must not be cited).
SHA="$(git -C "$SKILL_DIR" rev-parse HEAD 2>/dev/null || echo none)"
DIRTY="$(git -C "$SKILL_DIR" status --porcelain -- . 2>/dev/null | head -c1)"
HASH="$( (cd "$SKILL_DIR" && find . \( -path ./evals -o -name __pycache__ \) -prune -o -type f -print | LC_ALL=C sort | xargs sha256sum) | sha256sum | cut -c1-16)"
EHASH="$( (cd "$SKILL_EVALS" && cat evals.json promises.json) | sha256sum | cut -c1-16)"
for run in $(seq 1 "$RUNS"); do
    for bf in "$ROOT"/batches/b*.json; do
        SESSIONS+=("eval-$MODE-r$run-$(basename "$bf" .json)")
        run_batch "$run" "$bf" &
        while [ "$(jobs -rp | wc -l)" -ge "$PAR" ]; do wait -n; done
    done
done
wait

# Merge, grade, count tokens.
python3 -I - "$LOG_DIR" "$MODE" "$RUNS" "$CC/projects" "$H/work" <<'EOF'
import glob, json, os, sys
log, mode, runs, proj, work = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4], sys.argv[5]
for r in range(1, runs + 1):
    merged = []
    for f in sorted(glob.glob(os.path.join(log, f"answers-eval-{mode}-r{r}-b*.json"))):
        try:
            merged += json.load(open(f))["answers"]
        except (OSError, ValueError, KeyError):
            pass
    json.dump({"answers": merged}, open(os.path.join(log, f"answers-{mode}-{r}.json"), "w"), indent=1)
tokens = {}
for t in glob.glob(os.path.join(proj, "*", "*.jsonl")):
    seen, tin, tout, cwd = set(), 0, 0, ""
    for line in open(t, errors="replace"):
        try:
            d = json.loads(line)
        except ValueError:
            continue
        cwd = cwd or d.get("cwd", "")
        m = d.get("message") or {}
        u = m.get("usage") if isinstance(m, dict) else None
        if not u or m.get("id") in seen:
            continue
        seen.add(m.get("id"))
        tin += u.get("input_tokens", 0) + u.get("cache_creation_input_tokens", 0) + u.get("cache_read_input_tokens", 0)
        tout += u.get("output_tokens", 0)
    name = os.path.basename(cwd) or os.path.basename(t)
    tokens[name] = {"in": tokens.get(name, {}).get("in", 0) + tin, "out": tokens.get(name, {}).get("out", 0) + tout}
    os.makedirs(os.path.join(log, "transcripts"), exist_ok=True)
    with open(t, "rb") as src, open(os.path.join(log, "transcripts", name + ".jsonl"), "wb") as dst:
        dst.write(src.read())
json.dump(tokens, open(os.path.join(log, f"tokens-{mode}.json"), "w"), indent=1)
EOF
for run in $(seq 1 "$RUNS"); do
    check grade "$LOG_DIR/answers-$MODE-$run.json" >"$LOG_DIR/grade-$MODE-$run.log" 2>&1
done

python3 -I - "$LOG_DIR" "$MODE" "$RUNS" "$NAME" "$SHA${DIRTY:+ (dirty)}" "$HASH" "$EHASH" "$T0" "$(date -Is)" "$ROOT/prompts.json" "$IDS" <<'EOF'
import json, os, re, sys
log, mode, runs, name, sha, h, eh, t0, t1, prompts, ids = sys.argv[1:12]
runs = int(runs)
all_ids = [str(e["id"]) for e in json.load(open(prompts))["evals"]]
if ids:
    all_ids = [i for i in all_ids if i in set(ids.split(","))]
per = {i: [] for i in all_ids}
totals = []
for r in range(1, runs + 1):
    text = open(os.path.join(log, f"grade-{mode}-{r}.log")).read()
    got = {v: k for k, v in re.findall(r"^(pass|FAIL) (\S+) \[", text, re.M)}
    n = 0
    for i in all_ids:
        ok = got.get(i) == "pass"
        per[i].append(ok)
        n += ok
    totals.append(n)
tokens = json.load(open(os.path.join(log, f"tokens-{mode}.json")))
tin, tout = sum(v["in"] for v in tokens.values()), sum(v["out"] for v in tokens.values())
flaky = [i for i, v in per.items() if 0 < sum(v) < runs]
never = [i for i, v in per.items() if not any(v)]
summary = {"skill": name, "mode": mode, "runs": runs, "evals": len(all_ids), "pass_per_run": totals,
           "always_pass": sum(1 for v in per.values() if all(v)), "flaky": flaky, "never_pass": never,
           "skill_sha": sha, "skill_content_sha256_16": h, "evals_sha256_16": eh,
           "started": t0, "finished": t1, "tokens_in": tin, "tokens_out": tout, "sessions": tokens,
           "per_eval": per, "log_dir": log}
json.dump(summary, open(os.path.join(log, f"summary-{mode}.json"), "w"), indent=1)
with open(os.path.join(log, f"summary-{mode}.md"), "w") as f:
    f.write(f"# {name}: model run, {mode} skill\n\n")
    f.write(f"- skill sha {sha}, content {h}, evals {eh}\n- {t0} .. {t1}\n")
    f.write(f"- pass per run: {', '.join(f'{n}/{len(all_ids)}' for n in totals)}\n")
    f.write(f"- always pass {summary['always_pass']}, flaky {len(flaky)}, never pass {len(never)}\n")
    f.write(f"- tokens in:{tin} out:{tout} over {len(tokens)} sessions\n")
    if flaky:
        f.write(f"- flaky: {', '.join(flaky)}\n")
    if never:
        f.write(f"- never pass: {', '.join(never)}\n")
print(open(os.path.join(log, f"summary-{mode}.md")).read())
EOF
# Claude Code itself queries tmux from its pane (show -Av mouse, display-message -t %N)
# without -L; the guard refuses those too, which Claude tolerates. Only a refused call
# that is not one of Claude's own probes means agent-deck missed the private socket.
refused="$(grep -c '^REFUSED' "$GUARD_LOG" || true)"
foreign="$(grep '^REFUSED' "$GUARD_LOG" | grep -Ecv 'argv=(show -[Ag]?v |display-message -p -t %[0-9]+ )' || true)"
echo "tmux guard: $(grep -c '^ok' "$GUARD_LOG" || true) calls on the private socket, ${refused:-0} refused (${foreign:-0} not Claude's own probes)"
echo "logs: $LOG_DIR"
[ "${foreign:-0}" -eq 0 ]
