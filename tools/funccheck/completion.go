package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This fixture is an interactive local process, never a real model client. It
// emits the same transcript and hook ingress a client produces; the binary
// under test must derive its own completion ledger and inbox from that ingress.
func (s *suite) installClaude() error {
	s.env = append(s.env, "FUNCCHECK_BINARY="+s.bin)
	const script = `#!/bin/sh
set -eu
case "${1:-}" in --version) printf '2.1.0 (synthetic funccheck)\n'; exit 0;; esac
sid=11111111-1111-4111-8111-111111111111
explicit_sid=
resume_sid=
fork_session=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --session-id) shift; sid="$1"; explicit_sid=1;;
    --resume) shift; resume_sid="$1"; if [ -z "$explicit_sid" ]; then sid="$1"; fi;;
    --fork-session) fork_session=1;;
    -p|--print) printf 'noninteractive model execution forbidden\n' >&2; exit 91;;
  esac
  shift
done
project_key=$(printf '%s' "$PWD" | tr '/.' '--')
transcript_dir="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/projects/$project_key"
mkdir -p "$transcript_dir"
if [ -n "$fork_session" ] && [ -z "$explicit_sid" ]; then
  sid=$(cat /proc/sys/kernel/random/uuid)
fi
transcript="$transcript_dir/$sid.jsonl"
if [ -n "$fork_session" ]; then
  if [ -z "$resume_sid" ] || [ "$resume_sid" = "$sid" ]; then
    printf 'fork requires a source and independent destination conversation\n' >&2
    exit 92
  fi
  cp "$transcript_dir/$resume_sid.jsonl" "$transcript"
fi
# Preserve resumed history, including the source conversation of a fork.
printf '{"type":"user","sessionId":"%s","message":{"role":"user","content":"synthetic fixture"}}\n' "$sid" >> "$transcript"
hook() {
  printf '{"hook_event_name":"%s","session_id":"%s","cwd":"%s","transcript_path":"%s"}\n' "$1" "$sid" "$PWD" "$transcript" | "$FUNCCHECK_BINARY" hook-handler
}
hook SessionStart
hook Stop
# A terminal client owns its rendering. Kernel echo would leave old prompts
# and Enter retries on screen, confusing the real submission verifier.
stty -echo
printf 'Claude Code synthetic fixture\n❯ '
while IFS= read -r line; do
  case "$line" in
    *FUNCHECK_BUSY*) token=FUNCHECK_BUSY;;
    *FUNCHECK_COMPLETE*) token=FUNCHECK_COMPLETE;;
    *FUNCHECK_PARENT_PING*) token=FUNCHECK_PARENT_PING;;
    *FUNCHECK_FORK_PING*) token=FUNCHECK_FORK_PING;;
    *) continue;;
  esac
  printf '{"type":"user","sessionId":"%s","message":{"role":"user","content":"%s"}}\n' "$sid" "$token" >> "$transcript"
  hook UserPromptSubmit
  printf '\033[2J\033[Hfixture received: %s\nesc to interrupt\n' "$token"
  if [ "$token" = FUNCHECK_BUSY ]; then continue; fi
  # Two independent status polls must observe processing before it completes.
  sleep 3
  printf '\033[2J\033[Hfixture received: %s\n' "$token"
  if [ "$token" = FUNCHECK_COMPLETE ]; then
    printf '{"type":"assistant","sessionId":"%s","message":{"role":"assistant","content":[{"type":"text","text":"===AGENTDECK_DONE=== status=ok summary=synthetic prompt completed"}]}}\n' "$sid" >> "$transcript"
    printf '===AGENTDECK_DONE=== status=ok summary=synthetic prompt completed\n'
  else
    printf '{"type":"assistant","sessionId":"%s","message":{"role":"assistant","content":[{"type":"text","text":"fixture received: %s"}]}}\n' "$sid" "$token" >> "$transcript"
  fi
  hook Stop
  printf '❯ '
done
`
	return os.WriteFile(filepath.Join(s.root, "bin", "claude"), []byte(script), 0o700)
}

func (s *suite) completion() {
	if s.sessionID == "" {
		for _, name := range []string{"Launch prompt", "Status running", "Status waiting", "Completion sentinel", "Inbox delivers once", "Inbox drain consumes", "Fork", "Fork removal", "Child removal and hooks"} {
			s.skip(name, "Exercise synthetic child lifecycle", "primary session creation failed")
		}
		return
	}
	var childID, forkID string
	s.run("Launch prompt", "Launch a synthetic Claude child with a prompt and explicit parent", func() (string, error) {
		out, err := s.cmd("launch", "--json", "--no-wait", "--parent", s.sessionID, "-t", "funccheck-child", "-c", "claude", "-m", "FUNCHECK_BUSY", s.project)
		if err != nil {
			return out, err
		}
		var row struct {
			ID string `json:"id"`
		}
		if err := auxDecode(out, &row); err != nil {
			return out, err
		}
		childID = row.ID
		if childID == "" {
			return out, fmt.Errorf("launch returned no child ID")
		}
		return completionPoll("Synthetic child received its launch prompt in its live pane", func() (string, bool, error) {
			out, err := s.cmd("session", "output", childID)
			return out, strings.Contains(out, "fixture received: FUNCHECK_BUSY"), err
		})
	})
	if childID == "" {
		for _, name := range []string{"Status running", "Status waiting", "Completion sentinel", "Inbox delivers once", "Inbox drain consumes", "Fork", "Fork removal", "Child removal and hooks"} {
			s.skip(name, "Exercise synthetic child lifecycle", "launch returned no child ID")
		}
		return
	}
	s.run("Status running", "Inspect session status after synthetic UserPromptSubmit hook", func() (string, error) {
		return s.completionStatus(childID, "running")
	})
	s.run("Status waiting", "Send completion request, emit Stop hook, and inspect session status", func() (string, error) {
		if out, err := s.cmd("session", "send", "--no-wait", childID, "FUNCHECK_COMPLETE"); err != nil {
			return out, err
		}
		return s.completionStatus(childID, "waiting")
	})
	s.run("Completion sentinel", "Run notifier from synthetic Stop hook and inspect session children JSON", func() (string, error) {
		return completionPoll("Children lists the child as done=ok with its asserted completion summary", func() (string, bool, error) {
			if out, err := s.cmd("notify-daemon", "--once"); err != nil {
				return out, false, err
			}
			out, err := s.cmd("session", "children", "--json", s.sessionID)
			if err != nil {
				return out, false, err
			}
			var rows struct {
				Children []struct {
					ID      string `json:"id"`
					Status  string `json:"done_status"`
					Summary string `json:"done_summary"`
				} `json:"children"`
			}
			if err := auxDecode(out, &rows); err != nil {
				return out, false, err
			}
			for _, row := range rows.Children {
				if row.ID == childID && row.Status == "ok" && row.Summary == "synthetic prompt completed" {
					return out, true, nil
				}
			}
			return out, false, nil
		})
	})
	delivered := false
	s.run("Inbox delivers once", "Drain parent inbox and require exactly one asserted child completion", func() (string, error) {
		out, err := s.cmd("inbox", "drain", "--json", s.sessionID)
		if err != nil {
			return out, err
		}
		count, err := completionCount(out, childID)
		if err != nil {
			return out, err
		}
		if count != 1 {
			return out, fmt.Errorf("expected one child completion, got %d", count)
		}
		delivered = true
		return "Parent inbox delivered exactly one asserted child completion", nil
	})
	if delivered {
		s.run("Inbox drain consumes", "Re-run notifier and drain twice; consumed completion must not return", func() (string, error) {
			for range 2 {
				if out, err := s.cmd("notify-daemon", "--once"); err != nil {
					return out, err
				}
				out, err := s.cmd("inbox", "drain", "--json", s.sessionID)
				if err != nil {
					return out, err
				}
				count, err := completionCount(out, childID)
				if err != nil {
					return out, err
				}
				if count != 0 {
					return out, fmt.Errorf("consumed completion was delivered again")
				}
			}
			return "Child completion consumed; no duplicate after notifier replay and two drains", nil
		})
	} else {
		s.skip("Inbox drain consumes", "Verify consumed completion stays absent", "initial completion was not delivered")
	}
	s.run("Fork", "Fork synthetic child and verify its independent pane responds", func() (string, error) {
		out, err := s.cmd("session", "fork", "--json", "-t", "funccheck-fork", childID)
		if err != nil {
			return out, err
		}
		var row struct {
			ID string `json:"new_id"`
		}
		if err := auxDecode(out, &row); err != nil {
			return out, err
		}
		forkID = row.ID
		if forkID == "" || forkID == childID {
			return out, fmt.Errorf("fork did not return an independent ID")
		}
		child, err := s.auxRecord("session", "show", "--json", childID)
		if err != nil {
			return "", err
		}
		fork, err := s.auxRecord("session", "show", "--json", forkID)
		if err != nil {
			return "", err
		}
		childPane, _ := child["tmux_session"].(string)
		forkPane, _ := fork["tmux_session"].(string)
		if childPane == "" || forkPane == "" || childPane == forkPane {
			return "", fmt.Errorf("fork must have an independent pane: child=%q fork=%q", childPane, forkPane)
		}
		if out, err := s.cmd("session", "send", "--no-wait", forkID, "FUNCHECK_FORK_PING"); err != nil {
			return out, err
		}
		return completionPoll("Fork has an independent session ID and its live pane responded to a message", func() (string, bool, error) {
			out, err := s.cmd("session", "output", forkID)
			return out, strings.Contains(out, "fixture received: FUNCHECK_FORK_PING"), err
		})
	})
	if forkID != "" {
		s.run("Fork removal", "Remove fork and verify session record is absent", func() (string, error) { return s.removeCompletionSession(forkID, false) })
	} else {
		s.skip("Fork removal", "Remove fork", "fork returned no ID")
	}
	s.run("Child removal and hooks", "Require hook files, remove child, and verify record and hooks disappear", func() (string, error) { return s.removeCompletionSession(childID, true) })
}

func completionCount(out, childID string) (int, error) {
	var events []struct {
		ChildID string `json:"child_session_id"`
		Status  string `json:"done_status"`
		Summary string `json:"done_summary"`
	}
	if err := auxDecode(out, &events); err != nil {
		return 0, err
	}
	count := 0
	for _, event := range events {
		if event.ChildID == childID && event.Status == "ok" && event.Summary == "synthetic prompt completed" {
			count++
		}
	}
	return count, nil
}

func (s *suite) completionStatus(id, expected string) (string, error) {
	return completionPoll("Session status is "+expected+" after its lifecycle hook", func() (string, bool, error) {
		row, err := s.auxRecord("session", "show", "--json", id)
		if err != nil {
			return "", false, err
		}
		return fmt.Sprintf("Observed session status: %v", row["status"]), row["status"] == expected, nil
	})
}

func completionPoll(success string, probe func() (string, bool, error)) (string, error) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		out, ok, err := probe()
		if err != nil {
			return out, err
		}
		if ok {
			return success, nil
		}
		if time.Now().After(deadline) {
			return out, fmt.Errorf("expected child behavior not observed within 15 seconds")
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (s *suite) removeCompletionSession(id string, requireHooks bool) (string, error) {
	var hookFiles []string
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Base(filepath.Dir(path)) == "hooks" && (d.Name() == id+".json" || d.Name() == id+".sid") {
			hookFiles = append(hookFiles, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if requireHooks && len(hookFiles) == 0 {
		return "", fmt.Errorf("no hook files existed before removal; cleanup cannot be verified")
	}
	if out, err := s.cmd("remove", "--json", id); err != nil {
		return out, err
	}
	listed, err := s.sessionListed(id)
	if err != nil {
		return "", err
	}
	if listed {
		return "", fmt.Errorf("removed session still appears in list")
	}
	if err := s.requireMissing(id); err != nil {
		return "", err
	}

	for _, path := range hookFiles {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			return "", fmt.Errorf("removed session hook remains: %s (stat: %v)", path, err)
		}
	}
	return fmt.Sprintf("Session %s absent; %d hook files removed", id, len(hookFiles)), nil
}
