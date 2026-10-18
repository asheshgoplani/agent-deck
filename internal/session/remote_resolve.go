package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrRemoteSessionNotFound is ResolveSession's answer when the remote says
// no session matches the reference.
var ErrRemoteSessionNotFound = errors.New("remote session not found")

// remoteShowAnswer is the subset of `session show --json` (success or
// CLIOutput error) that ResolveSession reads.
type remoteShowAnswer struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Success *bool  `json:"success"`
	Error   string `json:"error"`
	Code    string `json:"code"`
}

// ResolveSession finds the one remote session a single-target command
// (attach, rename) refers to, by title or ID, without listing the whole
// remote: it asks the remote's own `session show --json <ref>`, which
// resolves one session instead of running a status pass over every one
// (`list --json --stats`). A remote whose `session show` does not answer in
// that shape (an older or unusual build) falls back to a plain `list --json`
// (never --stats) matched here as before. A remote that answers "not found"
// or "ambiguous" is final: listing would not change the answer.
func (r *SSHRunner) ResolveSession(ctx context.Context, ref string) (RemoteSessionInfo, error) {
	if ref != "" && !strings.HasPrefix(ref, "-") {
		out, err := r.Run(ctx, "session", "show", "--json", ref)
		if ctx.Err() != nil {
			return RemoteSessionInfo{}, ctx.Err()
		}
		var answer remoteShowAnswer
		if json.Unmarshal(bytes.TrimSpace(out), &answer) == nil {
			switch {
			case err == nil && answer.ID != "":
				return RemoteSessionInfo{ID: answer.ID, Title: answer.Title}, nil
			case answer.Success != nil && !*answer.Success && answer.Code == "NOT_FOUND":
				return RemoteSessionInfo{}, fmt.Errorf("%w: %s", ErrRemoteSessionNotFound, answer.Error)
			case answer.Success != nil && !*answer.Success && answer.Code == "AMBIGUOUS":
				return RemoteSessionInfo{}, errors.New(answer.Error)
			}
		}
		var exitErr *exec.ExitError
		if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() == 255) {
			// Transport failure (ssh's own 255, unreachable host, timeout):
			// a list over the same link would fail the same way, only slower.
			return RemoteSessionInfo{}, err
		}
	}
	out, err := r.Run(ctx, "list", "--json")
	if err != nil {
		return RemoteSessionInfo{}, err
	}
	sessions, err := parseRemoteSessions(out)
	if err != nil {
		return RemoteSessionInfo{}, err
	}
	for _, s := range sessions {
		if s.Title == ref || strings.HasPrefix(s.ID, ref) {
			return s, nil
		}
	}
	return RemoteSessionInfo{}, ErrRemoteSessionNotFound
}
