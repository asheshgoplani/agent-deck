package session

import (
	"log/slog"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// Session selectors in persisted Claude ExtraArgs.
//
// `--extra-arg --resume --extra-arg <parent> --extra-arg --fork-session` is a
// valid way to start a Claude session as a fork of another conversation, and
// the tokens are persisted with the rest of ExtraArgs. They are only right for
// that first start. Once the session has a conversation of its own, every
// builder emits its own selector (`--resume <own>` or `--session-id <own>`),
// and the user tokens used to be appended after it, so the process ran
// `claude --resume <own> ... --resume <parent> --fork-session` and Claude
// (last --resume wins) forked the parent again on every restart.

// valuedSessionSelector reports whether tok is a Claude session selector that
// takes a value as the next token.
func valuedSessionSelector(tok string) bool {
	switch tok {
	case "--resume", "-r", "--session-id":
		return true
	}
	return false
}

// stripStaleSessionSelectors splits Claude extra args into the tokens to keep
// and the session-selector tokens that would fight the builder's own selector
// for a session whose conversation id is own.
//
// With own == "" (the session has no conversation yet: its first start) the
// args are returned unchanged: a fork pair there is the operator's intent.
//
// With an own id:
//   - --resume/-r/--session-id <id> (and their = forms) are always dropped:
//     the builder already selects the own id, and a different id is a stale
//     fork source;
//   - a bare --resume/-r and -c/--continue are dropped (a second selector);
//   - --fork-session is dropped when the args also name a conversation other
//     than own (the stale fork pair). A lone --fork-session with no foreign id
//     is kept: it may be an operator fork of the session's own --resume target
//     (`launch --resume-session <src> --extra-arg --fork-session`), and
//     dropping it there would resume the source conversation in place.
func stripStaleSessionSelectors(args []string, own string) (kept, dropped []string) {
	if own == "" || len(args) == 0 {
		return args, nil
	}

	// First pass: does any valued selector name a conversation other than own?
	foreign := false
	for idx := 0; idx < len(args); idx++ {
		tok := args[idx]
		if flag, val, ok := strings.Cut(tok, "="); ok && valuedSessionSelector(flag) {
			if val != own {
				foreign = true
			}
			continue
		}
		if valuedSessionSelector(tok) && idx+1 < len(args) && !strings.HasPrefix(args[idx+1], "-") {
			if args[idx+1] != own {
				foreign = true
			}
			idx++
		}
	}

	for idx := 0; idx < len(args); idx++ {
		tok := args[idx]
		if flag, _, ok := strings.Cut(tok, "="); ok && valuedSessionSelector(flag) {
			dropped = append(dropped, tok)
			continue
		}
		switch {
		case valuedSessionSelector(tok):
			dropped = append(dropped, tok)
			if idx+1 < len(args) && !strings.HasPrefix(args[idx+1], "-") {
				dropped = append(dropped, args[idx+1])
				idx++
			}
		case tok == "-c" || tok == "--continue":
			dropped = append(dropped, tok)
		case tok == "--fork-session" && foreign:
			dropped = append(dropped, tok)
		default:
			kept = append(kept, tok)
		}
	}
	return kept, dropped
}

// claudeExtraArgsForCommand returns the ExtraArgs a Claude command builder may
// append for this instance: with a conversation id of its own, every session
// selector that would fight the builder's own selector is left out. It never
// mutates the instance (the fork builder calls it on the PARENT).
func (i *Instance) claudeExtraArgsForCommand() []string {
	kept, _ := stripStaleSessionSelectors(i.ExtraArgs, i.ClaudeSessionID)
	return kept
}

// healStaleClaudeSelectorExtraArgs removes stale session selectors from the
// persisted ExtraArgs of a Claude session that already has its own
// conversation id, so the next save writes the repaired list. reason names
// the caller ("load", "restart") for the log line. Returns true when it
// changed anything.
func (i *Instance) healStaleClaudeSelectorExtraArgs(reason string) bool {
	if i == nil || !IsClaudeCompatible(i.Tool) || i.ClaudeSessionID == "" || len(i.ExtraArgs) == 0 {
		return false
	}
	kept, dropped := stripStaleSessionSelectors(i.ExtraArgs, i.ClaudeSessionID)
	if len(dropped) == 0 {
		return false
	}
	if len(kept) == 0 {
		kept = nil
	}
	i.ExtraArgs = kept
	sessionLog.Info("extra_args_stale_session_selector_removed",
		slog.String("instance_id", logging.SanitizeValue(i.ID)),
		slog.String("claude_session_id", logging.SanitizeValue(i.ClaudeSessionID)),
		slog.String("dropped", logging.SanitizeValue(strings.Join(dropped, " "))),
		slog.String("reason", reason))
	return true
}
