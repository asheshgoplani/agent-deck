#!/usr/bin/env bash
# model-run.sh must run Claude only on a dedicated token, never on a live login.
# Sharing a live ~/.claude*/.credentials.json with sandbox sessions refreshes and
# rotates that login, which logs out every other session on the account.
#
# Fake `deck-secret` binaries stand in for the vault; nothing here starts Claude,
# tmux or agent-deck, and everything runs in a temp HOME.
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MR="${MODEL_RUN:-$HERE/../evals/model-run.sh}"
T="$(mktemp -d -t model-run-token.XXXXXX)"
trap 'if command -v gio >/dev/null 2>&1; then gio trash "$T" 2>/dev/null || rm -rf "$T"; else rm -rf "$T"; fi' EXIT
pass=0 fail=0
ok() { pass=$((pass + 1)); echo "ok   $1"; }
bad() { fail=$((fail + 1)); echo "FAIL $1"; }

mkdir -p "$T/home" "$T/work" "$T/vault-none" "$T/vault-has"
cat >"$T/vault-none/deck-secret" <<'EOF'
#!/bin/sh
case "$1" in exists) echo no; exit 3 ;; *) echo "deck-secret fake: no such secret" >&2; exit 3 ;; esac
EOF
cat >"$T/vault-has/deck-secret" <<EOF
#!/bin/sh
echo "\$*" >>"$T/vault-calls.log"
case "\$1" in
  exists) echo yes; exit 0 ;;
  to-file) printf 'fake-dedicated-token' >"\$3"; chmod 600 "\$3"; exit 0 ;;
  *) exit 2 ;;
esac
EOF
chmod 700 "$T/vault-none/deck-secret" "$T/vault-has/deck-secret"
run() {  # run <vault dir or ""> <args...>: model-run.sh in a temp HOME, no ambient Claude config
    local vault="$1"; shift
    local path="/usr/local/bin:/usr/bin:/bin"
    [ -n "$vault" ] && path="$vault:$path"
    env -i HOME="$T/home" PATH="$path" DECK_SECRET="${DECK_SECRET_OVERRIDE:-}" TMPDIR="$T/work" \
        bash "$MR" --preflight --work-dir "$T/work" --log-dir "$T/logs" "$@" >"$T/out" 2>&1
}
sandboxes() { find "$T/work" -maxdepth 1 -name 'admr.*' | wc -l; }

# 1. The vault has no dedicated token: refuse before any sandbox exists.
DECK_SECRET_OVERRIDE="$T/vault-none/deck-secret" run "$T/vault-none"
rc=$?
if [ $rc -eq 2 ] && grep -q 'conductor/CLAUDE_DEMO_TOKEN' "$T/out" && [ "$(sandboxes)" -eq 0 ]; then
    ok "refuses without the dedicated token (exit 2, names it, no sandbox)"
else bad "missing token: rc=$rc sandboxes=$(sandboxes) out=$(head -c 300 "$T/out")"; fi

# 2. No vault at all and no --token-file: refuse.
DECK_SECRET_OVERRIDE="$T/no-such-deck-secret" run ""
rc=$?
if [ $rc -eq 2 ] && grep -q 'dedicated Claude token' "$T/out" && [ "$(sandboxes)" -eq 0 ]; then ok "refuses with no vault and no --token-file"
else bad "no vault: rc=$rc out=$(head -c 300 "$T/out")"; fi

# 3. A live login file is refused even when passed on purpose.
mkdir -p "$T/fake-claude"
printf '{"claudeAiOauth":{"accessToken":"x","refreshToken":"y"}}' >"$T/fake-claude/.credentials.json"
cp "$T/fake-claude/.credentials.json" "$T/fake-claude/login.json"
for f in "$T/fake-claude/.credentials.json" "$T/fake-claude/login.json"; do
    DECK_SECRET_OVERRIDE="$T/no-such-deck-secret" run "" --token-file "$f"
    rc=$?
    if [ $rc -eq 2 ] && grep -q 'is a Claude login' "$T/out" && [ "$(sandboxes)" -eq 0 ]; then ok "refuses a login file as token: $(basename "$f")"
    else bad "login file $(basename "$f") accepted: rc=$rc out=$(head -c 300 "$T/out")"; fi
done

# 4. The old way in is gone: --claude-config is not an option any more.
DECK_SECRET_OVERRIDE="$T/vault-has/deck-secret" run "$T/vault-has" --claude-config "$T/fake-claude"
rc=$?
if [ $rc -eq 2 ] && grep -q 'unknown argument: --claude-config' "$T/out"; then ok "--claude-config is rejected"
else bad "--claude-config: rc=$rc out=$(head -c 300 "$T/out")"; fi

# 5. Nothing in the script links or copies a credentials file into the sandbox.
if grep -nE '(ln|cp|install)[^#]*credentials\.json' "$MR" >/dev/null; then
    bad "model-run.sh links or copies a credentials.json: $(grep -nE '(ln|cp|install)[^#]*credentials\.json' "$MR" | head -2)"
else ok "no credentials.json is linked or copied"; fi

# 6. With the dedicated token: preflight passes, fetches it with to-file into the
#    sandbox (0600), and leaves no sandbox behind.
: >"$T/vault-calls.log"
DECK_SECRET_OVERRIDE="$T/vault-has/deck-secret" run "$T/vault-has"
rc=$?
if [ $rc -eq 0 ] && grep -q '^exists conductor/CLAUDE_DEMO_TOKEN' "$T/vault-calls.log" \
    && grep -qE "^to-file conductor/CLAUDE_DEMO_TOKEN $T/work/admr\.[^/]+/" "$T/vault-calls.log" \
    && grep -q 'mode 600' "$T/out" && [ "$(sandboxes)" -eq 0 ]; then
    ok "dedicated token: preflight ok, fetched with to-file into the sandbox, 0600"
else bad "with token: rc=$rc calls=$(cat "$T/vault-calls.log") out=$(head -c 400 "$T/out")"; fi

echo "passed: $pass, failed: $fail"
[ "$fail" -eq 0 ]
