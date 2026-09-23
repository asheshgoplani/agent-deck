package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Probing from inside a foreign tmux server (rc feedback 2026-09-23).
//
// A session with no socket name was created for the user's DEFAULT tmux
// server, but a socket-less `tmux` subprocess does not go there: tmux takes the
// server from $TMUX first. An agent-deck TUI started inside a private server
// (`tmux -L uxaudit`, with AGENT_DECK_ALLOW_OUTER_TMUX=1) therefore listed and
// probed the wrong server, found none of the fleet's sessions, and published
// "error" for every one of them to the shared status table. The notify daemon
// read those rows and sent running -> error events to every parent, again each
// time another process wrote the real verdict back.
//
// A session that is missing from the $TMUX server but present on the default
// server is not gone; this process is simply looking at the wrong server.

// emptySocketResolvesAwayFromDefault reports whether a socket-less tmux
// subprocess of this process resolves, through $TMUX, to a server other than
// the default one. Pure for testability; mirrors resolveTmuxSocketPath.
func emptySocketResolvesAwayFromDefault(lookupEnv func(string) string, uid int) bool {
	envPath := socketPathFromTmuxEnv(lookupEnv("TMUX"))
	if envPath == "" {
		return false
	}
	base := strings.TrimSpace(lookupEnv("TMUX_TMPDIR"))
	if base == "" {
		base = "/tmp"
	}
	defaultPath := filepath.Join(base, fmt.Sprintf("tmux-%d", uid), defaultTmuxSocketName)
	return normalizeTmpPath(envPath) != normalizeTmpPath(defaultPath)
}

// defaultServerSessionsTTL bounds how often a process probing from a foreign
// server re-lists the default server: one subprocess per TTL, not per session.
const defaultServerSessionsTTL = 2 * time.Second

var defaultServerSessions struct {
	sync.Mutex
	names map[string]struct{}
	err   error
	at    time.Time
}

// defaultServerHasSession answers from one cached `list-sessions` of the
// default server. An indeterminate probe (timeout) counts as present: it is no
// evidence that the session is gone.
func defaultServerHasSession(name string) bool {
	defaultServerSessions.Lock()
	defer defaultServerSessions.Unlock()
	if defaultServerSessions.at.IsZero() || time.Since(defaultServerSessions.at) > defaultServerSessionsTTL {
		defaultServerSessions.names, defaultServerSessions.err = ListSessionNamesOnSocket(defaultTmuxSocketName)
		defaultServerSessions.at = time.Now()
	}
	if defaultServerSessions.err != nil {
		return true
	}
	_, ok := defaultServerSessions.names[name]
	return ok
}

// AbsenceIsForeignServer reports whether Exists() == false for this session is
// an artifact of where this process runs rather than evidence that the session
// is gone: the session has no socket name, this process's $TMUX names a server
// other than the default one, and the default server has the session. Callers
// must then keep their last-known status instead of concluding error.
func (s *Session) AbsenceIsForeignServer() bool {
	if strings.TrimSpace(s.SocketName) != "" {
		return false
	}
	if !emptySocketResolvesAwayFromDefault(os.Getenv, os.Getuid()) {
		return false
	}
	return defaultServerHasSession(s.Name)
}
