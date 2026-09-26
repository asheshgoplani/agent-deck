package main

import (
	"os"
	"path/filepath"
	"strings"
)

// visualCheckI18NReply is the canned assistant response used to prove Hindi
// and emoji render correctly in the preview pane and contact sheet. It is
// literal UTF-8, embedded directly in the fixture script and in JSON
// transcript lines (JSON permits raw UTF-8; no \uXXXX escaping needed).
const visualCheckI18NReply = "नमस्ते दुनिया 🚀🌟 यह परीक्षण है।"

// installFixtureTools writes the synthetic "claude" CLI used to reach every
// session status headlessly, plus the fixtureSSH stand-in. Other tools
// (gemini/opencode/codex/pi) are only ever `add`ed without starting, so the
// gallery shows them at their natural post-add StatusIdle without needing
// a fixture binary on PATH. "shell" sessions run the sandboxed HOME's real
// /bin/sh, so it needs no fixture either.
//
// This is a disposable interactive local process standing in for a model
// client, not a real model client: it emits the same transcript shape and
// hook ingress a client produces, and the binary under test derives its own
// status from that ingress, exactly like tools/funccheck's fixture.
func (s *suite) installFixtureTools() error {
	s.env = append(s.env, "VISUALCHECK_BINARY="+s.bin)
	script := `#!/bin/sh
set -eu
case "${1:-}" in --version) printf '2.1.0 (synthetic visualcheck)\n'; exit 0;; esac
sid=22222222-2222-4222-8222-222222222222
while [ "$#" -gt 0 ]; do
  case "$1" in
    --session-id) shift; sid="$1";;
  esac
  shift
done
project_key=$(printf '%s' "$PWD" | tr '/.' '--')
transcript_dir="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/projects/$project_key"
mkdir -p "$transcript_dir"
transcript="$transcript_dir/$sid.jsonl"
printf '{"type":"user","sessionId":"%s","message":{"role":"user","content":"synthetic fixture"}}\n' "$sid" >> "$transcript"
hook() {
  printf '{"hook_event_name":"%s","session_id":"%s","cwd":"%s","transcript_path":"%s"}\n' "$1" "$sid" "$PWD" "$transcript" | "$VISUALCHECK_BINARY" hook-handler
}
hook SessionStart
hook Stop
stty -echo
printf 'Claude Code synthetic fixture\n\342\235\257 '
busy=0
while IFS= read -r line; do
  case "$line" in
    *VC_GO*)
      if [ "$busy" = 1 ]; then
        printf '{"type":"assistant","sessionId":"%s","message":{"role":"assistant","content":[{"type":"text","text":"REPLY_TEXT"}]}}\n' "$sid" >> "$transcript"
        printf '\033[2J\033[HREPLY_TEXT\n'
        hook Stop
        printf '\342\235\257 '
        busy=0
      fi
      continue;;
    *VC_BUSY*|*VC_I18N*) ;;
    *) continue;;
  esac
  # session send's post-send delivery verification (issue #876) needs a
  # sustained "active" hook status to count as evidence -- an instant
  # reply flips back to waiting before it accumulates enough samples. So
  # every token first enters (and stays in) a busy phase, exactly like a
  # real turn in flight; VC_GO (sent directly over tmux, bypassing that
  # verification because delivery of the busy-triggering token already
  # proved the pane is live) is what finalizes it.
  printf '{"type":"user","sessionId":"%s","message":{"role":"user","content":"%s"}}\n' "$sid" "$line" >> "$transcript"
  hook UserPromptSubmit
  printf '\033[2J\033[Hfixture received: %s\nesc to interrupt\n' "$line"
  busy=1
done
`
	script = strings.ReplaceAll(script, "REPLY_TEXT", visualCheckI18NReply)
	if err := os.WriteFile(filepath.Join(s.root, "bin", "claude"), []byte(script), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.root, "bin", "ssh"), []byte(fixtureSSH), 0o700)
}

// fixtureSSH shadows the real ssh on the sandbox PATH. The "lab" remote
// (fixture@vc-lab.invalid) must be unreachable the same way on every
// machine; the real ssh made that depend on the runner's resolver and
// network (offline g14 container vs networked GitHub runner). It fails
// every call immediately with ssh's own resolver error text and exit code,
// which the poll classifies as "host down", without touching the network.
const fixtureSSH = `#!/bin/sh
printf 'ssh: Could not resolve hostname vc-lab.invalid: Name or service not known\n' >&2
exit 255
`
