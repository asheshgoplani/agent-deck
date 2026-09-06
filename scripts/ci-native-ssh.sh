#!/usr/bin/env bash
# Run only the required native SSH acceptance tests as an unprivileged user.
set -euo pipefail
export GOTOOLCHAIN=local
: "${NATIVE_SSH_ARTIFACT_DIR:?Set an absolute artifact directory outside the checkout}"
: "${NATIVE_SSH_EXPECTED_SHA:?Set the exact reviewed source commit}"
case "$(uname -s)" in
  Linux) hash=(sha256sum); authorized_cat=/usr/bin/cat ;;
  Darwin) hash=(shasum -a 256); authorized_cat=/bin/cat ;;
  *) echo "Native SSH acceptance requires Linux or macOS" >&2; exit 1 ;;
esac
export NATIVE_SSH_AUTHORIZED_CAT="$authorized_cat"
test "$(id -u)" -ne 0
root=$(git rev-parse --show-toplevel)
cd "$root"
test "$(git rev-parse HEAD)" = "$NATIVE_SSH_EXPECTED_SHA"
test -z "$(git status --porcelain)"
python3 - "$root" "$NATIVE_SSH_ARTIFACT_DIR" <<'PY'
import pathlib,sys
root=pathlib.Path(sys.argv[1]).resolve()
p=pathlib.Path(sys.argv[2])
assert p.is_absolute() and p.resolve()==p and not p.is_relative_to(root)
PY
mkdir -p "$NATIVE_SSH_ARTIFACT_DIR"
run="$NATIVE_SSH_ARTIFACT_DIR/run"
mkdir "$run"
exec > >(tee "$run/commands.log") 2>&1
finish() {
 result=$?
 trap - EXIT
 set +e
 "${hash[@]}" -c "$run/source.sha256" > "$run/source-check.log" 2>&1
 check=$?
 git status --porcelain > "$run/source-status.txt"
 if test "$check" -ne 0 || test -s "$run/source-status.txt"; then result=94; fi
 printf '%s\n' "$result" > "$run/exit"
 exit "$result"
}
git ls-files -z | xargs -0 "${hash[@]}" > "$run/source.sha256"
trap finish EXIT
git rev-parse HEAD 'HEAD^{tree}' > "$run/source"
"${hash[@]}" "$0" > "$run/runner.sha256"
for tool in go tmux ssh ssh-keygen python3; do command -v "$tool"; done
python3 - <<'PY'
import os,stat
for p in ['/usr/sbin/sshd',os.environ['NATIVE_SSH_AUTHORIZED_CAT']]:
 s=os.stat(p)
 assert s.st_uid==0 and not s.st_mode & 0o022 and os.access(p,os.X_OK),p
# The acceptance fixture uses this production namespace. Refuse active sockets.
p='/tmp/agent-deck-ssh'
if os.path.lexists(p):
 s=os.lstat(p)
 assert stat.S_ISDIR(s.st_mode) and s.st_uid==os.getuid() and s.st_mode & 0o077==0
 assert not any(stat.S_ISSOCK(os.lstat(e.path).st_mode) for e in os.scandir(p))
PY
id
go version
tmux -V
ssh -V
/usr/sbin/sshd -V
sandbox=$(mktemp -d /tmp/ns.XXXXXX)
mkdir "$sandbox/home" "$sandbox/tmp" "$run/receipts"
printf '%s\n' "$sandbox" > "$run/sandbox-path"
export GOMODCACHE="$(go env GOMODCACHE)" GOCACHE="$(go env GOCACHE)"
export HOME="$sandbox/home" TMPDIR="$sandbox/tmp"
export XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= XDG_STATE_HOME= XDG_RUNTIME_DIR=
unset TMUX SSH_AUTH_SOCK SSH_AGENT_PID AGENTDECK_PROFILE CLAUDE_CONFIG_DIR CODEX_HOME DBUS_SESSION_BUS_ADDRESS
export GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local NATIVE_SSH_REQUIRED=1
export NATIVE_SSHD=/usr/sbin/sshd NATIVE_SSH_RECEIPT_DIR="$run/receipts"
set +e
go test -json -p 1 -count=1 -timeout 10m ./cmd/agent-deck -run '^TestNativeSSHAttachLifecycle$' > "$run/lifecycle.jsonl" 2> "$run/lifecycle.stderr"
lifecycle=$?
go test -json -p 1 -count=1 -timeout 5m ./cmd/agent-deck -run '^TestNativeSSHTUIRegistryLifecycle$' > "$run/tui.jsonl" 2> "$run/tui.stderr"
tui=$?
go test -json -p 1 -count=1 -timeout 5m ./internal/session -run '^TestSSHAttachPortableTERM$' > "$run/term.jsonl" 2> "$run/term.stderr"
term=$?
printf '%s\n' "$lifecycle" > "$run/lifecycle.exit"
printf '%s\n' "$tui" > "$run/tui.exit"
printf '%s\n' "$term" > "$run/term.exit"
set -e
python3 - "$run" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1]); inventory={}
required={'tui':['TestNativeSSHTUIRegistryLifecycle'],'lifecycle':['TestNativeSSHAttachLifecycle'],'term':['TestSSHAttachPortableTERM','TestSSHAttachPortableTERM/xterm-256color','TestSSHAttachPortableTERM/screen','TestSSHAttachPortableTERM/tmux-256color','TestSSHAttachPortableTERM/xterm-ghostty','TestSSHAttachPortableTERM/#00','TestSSHAttachPortableTERM/x;_touch_/unwanted']}
for name,tests in required.items():
 rows=[json.loads(line) for line in (root/(name+'.jsonl')).read_text().splitlines()]
 assert rows and (root/(name+'.exit')).read_text().strip()=='0',name
 assert all(e['Action'] not in ('fail','skip') for e in rows),name
 assert any(e['Action']=='pass' and 'Test' not in e for e in rows),name
 inventory[name]={}
 for test in tests:
  actions=[e['Action'] for e in rows if e.get('Test')==test and e['Action']!='output']
  inventory[name][test]=actions
  assert actions==['run','pass'],(test,actions)
(root/'required-tests.json').write_text(json.dumps(inventory,indent=2)+'\n')
PY
