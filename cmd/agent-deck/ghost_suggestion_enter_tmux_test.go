package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Claude Code shows a "prompt suggestion" in an EMPTY composer: a predicted
// next user message, rendered as the composer placeholder. In 2.1.295 the
// placeholder renderer paints it as
//
//	focused:            ESC[7m<first char>ESC[27m ESC[2m<rest>ESC[22m
//	unfocused:          ESC[2m<whole text>ESC[22m
//	native cursor mode: <first char> ESC[2m<rest>ESC[22m
//
// Tab or Right arrow on the empty composer copies the suggestion into the
// input, and the next Enter submits it. Typing replaces it. A bare Enter
// pressed by automation while it is shown therefore risks submitting an
// instruction nobody wrote (on 2.1.295 the Enter itself submits an empty
// prompt; a later Enter after any accept key, or another build, would not).
//
// The fake below models that composer in a real tmux pane and records every
// key that reaches it while a suggestion is on screen. agent-deck must
// deliver its own message and must never press Enter, Tab or Right while the
// suggestion is shown.
const fakeClaudeGhostScript = `#!/bin/bash
log="$1"; mode="$2"
sugg="Run the release checklist and tag the build now"
stty raw -echo
div=$(printf '\342\224\200%.0s' $(seq 1 60))
buf=""; ghost="$sugg"; last=""
render() {
  printf '\033[2J\033[H'
  printf 'fake claude\r\n'
  if [ -n "$last" ]; then printf '\033[38;5;239m\342\235\257 \033[38;5;231m%s\033[39m\r\n' "$last"; fi
  printf '\r\n%s\r\n' "$div"
  if [ -n "$buf" ]; then
    printf '\342\235\257\302\240%s' "$buf"
  elif [ -n "$ghost" ]; then
    first="${ghost:0:1}"; rest="${ghost:1}"
    case "$mode" in
      focused) printf '\342\235\257\302\240\033[7m%s\033[27m\033[2m%s\033[22m' "$first" "$rest" ;;
      native) printf '\342\235\257\302\240%s\033[2m%s\033[22m' "$first" "$rest" ;;
      *) printf '\342\235\257\302\240\033[2m%s\033[22m' "$ghost" ;;
    esac
  else
    printf '\342\235\257\302\240'
  fi
  printf '\r\n%s\r\n  ? for shortcuts\r\n' "$div"
}
render
while IFS= read -r -n1 -d '' c; do
  if [ "$c" = $'\r' ] || [ "$c" = $'\n' ]; then
    if [ -n "$buf" ]; then
      echo "SUBMITTED: $buf" >> "$log"
      last="$buf"; buf=""; ghost="$sugg"
    elif [ -n "$ghost" ]; then
      echo "ENTER_WHILE_GHOST" >> "$log"
    fi
  elif [ "$c" = $'\t' ]; then
    if [ -z "$buf" ] && [ -n "$ghost" ]; then echo "ACCEPTED_TAB" >> "$log"; buf="$ghost"; fi
  elif [ "$c" = $'\033' ]; then
    rest=""; IFS= read -r -n2 -t 0.05 -d '' rest
    if [ "$rest" = "[C" ] && [ -z "$buf" ] && [ -n "$ghost" ]; then echo "ACCEPTED_RIGHT" >> "$log"; buf="$ghost"; fi
  elif [ "$c" = $'\003' ]; then
    buf=""
  elif [ "$c" = $'\177' ]; then
    buf="${buf%?}"
  else
    buf="$buf$c"
  fi
  render
done
`

func TestExecuteSend_NeverPressesEnterOnGhostSuggestion_Tmux(t *testing.T) {
	if testing.Short() {
		t.Skip("tmux pane test")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude.sh")
	if err := os.WriteFile(script, []byte(fakeClaudeGhostScript), 0o755); err != nil {
		t.Fatal(err)
	}
	const msg = "AUTOMATED_GHOST_PROBE_MESSAGE"
	for _, mode := range []string{"focused", "unfocused", "native"} {
		t.Run(mode, func(t *testing.T) {
			logPath := filepath.Join(dir, mode+".log")
			sess := startFakeClaudePane(t, "ghost-"+mode, "bash "+script+" "+logPath+" "+mode)
			if raw, err := sess.CapturePaneFresh(); err == nil {
				for _, line := range strings.Split(raw, "\n") {
					if strings.Contains(line, "❯") {
						t.Logf("captured composer row (%s): %q", mode, line)
					}
				}
			}

			res, err := executeSend(sess, "claude", msg, true, integrationGuardTuning())
			t.Logf("delivery=%q err=%v", res.delivery, err)
			// Let any late nudge land before reading the record.
			time.Sleep(300 * time.Millisecond)
			data, _ := os.ReadFile(logPath)
			record := string(data)
			t.Logf("fake claude record:\n%s", record)

			if strings.Contains(record, "ACCEPTED_") {
				t.Errorf("a suggestion accept key reached the composer:\n%s", record)
			}
			if n := strings.Count(record, "ENTER_WHILE_GHOST"); n != 0 {
				t.Errorf("bare Enter pressed %d time(s) while the prompt suggestion was shown", n)
			}
			if strings.Contains(record, "SUBMITTED: Run the release checklist") {
				t.Errorf("the prompt suggestion was submitted as an instruction:\n%s", record)
			}
			if got := strings.Count(record, "SUBMITTED: "+msg); got != 1 {
				t.Errorf("our message must be submitted exactly once, got %d (delivery=%q err=%v)", got, res.delivery, err)
			}
		})
	}
}
