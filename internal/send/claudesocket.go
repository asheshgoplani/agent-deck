package send

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
)

// UnavailableReason names a pre-write condition that makes the Claude
// messaging socket unusable for a target. Only these reasons may fall back
// to tmux (#2089 / discussion #2089 open question 1): once a write to the
// socket has started, the send is committed and is never retried on tmux.
type UnavailableReason string

const (
	ReasonNoRecord       UnavailableReason = "no_record"
	ReasonNoSocketPath   UnavailableReason = "no_socket_path"
	ReasonOldProtocol    UnavailableReason = "old_peer_protocol"
	ReasonDeadPid        UnavailableReason = "dead_pid"
	ReasonProcStartDrift UnavailableReason = "proc_start_mismatch"
	ReasonWrongUID       UnavailableReason = "uid_mismatch"
	ReasonNoKey          UnavailableReason = "key_unreadable"
	ReasonDialFailed     UnavailableReason = "dial_failed"
	ReasonTooLarge       UnavailableReason = "message_too_large"
)

// Unavailable means the socket cannot be used for this target for a reason
// observable strictly before any byte is written. The caller should fall
// back to tmux.
type Unavailable struct {
	Reason UnavailableReason
	Err    error
}

func (u *Unavailable) Error() string {
	if u.Err != nil {
		return fmt.Sprintf("claude socket unavailable (%s): %v", u.Reason, u.Err)
	}
	return fmt.Sprintf("claude socket unavailable (%s)", u.Reason)
}

func (u *Unavailable) Unwrap() error { return u.Err }

// CommittedError means a write to the socket was started and failed midway.
// The message may or may not have reached the target's inbox, so it is a
// hard error: NEVER retried on tmux (that would risk double delivery).
type CommittedError struct{ Err error }

func (c *CommittedError) Error() string {
	return fmt.Sprintf("claude socket write failed after send was committed: %v", c.Err)
}

func (c *CommittedError) Unwrap() error { return c.Err }

// ClaudeSocketTarget is a vetted, dialable Claude Code messaging-socket
// endpoint. Build one with ResolveClaudeSocketTarget; never construct it by
// hand from unverified input.
type ClaudeSocketTarget struct {
	SocketPath string
	Pid        int
	SessionID  string

	// AuthToken is the peerToken read from the target's key file, or "" when
	// no well-formed key file exists. Per the 2.1.259 binary, auth is enforced
	// only on Windows (offset 159,610,776: authRequired = opts.requireAuth ??
	// $Ct(), $Ct() true only on windows); on macOS/Linux the token is not a
	// security gate — identity there comes from kernel peer credentials and
	// filesystem permissions on the 0700 cc-socks dir / 0600 socket. We still
	// send it when available because it's what Claude's own client does, and
	// because a well-formed key file's procStart is a real liveness/torn-pair
	// signal (checked in ResolveClaudeSocketTarget, not here).
	AuthToken string
}

// keyFilenameRe matches Claude's own key filename shape, `<pid>.<sha256hex>.key`.
var keyFilenameRe = regexp.MustCompile(`^(\d+)\.[0-9a-f]{64}\.key$`)

// peerTokenRe matches the expected peerToken shape: 32 lowercase hex chars
// (16 random bytes).
var peerTokenRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// minPeerProtocol is the exact peerProtocol version this transport was
// verified against (Claude Code 2.1.259). Gating on exact equality, not
// >=, is deliberate: a future protocol bump could change the frame shape,
// and the honest failure mode is falling back to tmux rather than silently
// sending a frame the new protocol version might reject or misinterpret.
// Flagged to the maintainer in the PR body (discussion #2089 open question 3).
const minPeerProtocol = 1

// killCheck, psLstart and dialUnix are seams so tests can exercise resolver
// and write-path branches without a real live process or a flaky real-socket
// mid-write failure. Defaults are the real syscalls; see
// claudesocket_test.go.
var (
	killCheck = func(pid int) error { return syscall.Kill(pid, 0) }
	psLstart  = realPsLstart
	dialUnix  = func(path string, timeout time.Duration) (net.Conn, error) {
		return net.DialTimeout("unix", path, timeout)
	}
)

// realPsLstart runs `LC_ALL=C TZ=UTC ps -o lstart= -p <pid>`, trimmed. This
// is the exact incantation Claude's own client uses to derive procStart
// (verified against the 2.1.259 binary, offset 158,929,000): comparison is
// exact string equality, so LC_ALL/TZ must match or the check silently
// always fails (which fails safe, to tmux).
func realPsLstart(pid int) (string, error) {
	// #nosec G204 -- "ps" is a fixed binary; only arg is strconv.Itoa(int),
	// matching the existing #nosec G204 precedent at
	// internal/tmux/pipemanager.go:1858 (readProcessIdentity).
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	// childenv.ForLaunch, not os.Environ() directly: golangci forbidigo bans
	// raw os.Environ() in spawn paths (internal/childenv's package doc), so
	// this is the one allowlisted entry point even for a short-lived `ps`
	// child. childConfigDir="" strips TELEGRAM_*/CLAUDE_CONFIG_DIR without
	// pinning a config dir ps has no use for.
	cmd.Env = append(childenv.ForLaunch(""), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ClaudeSocketRecord is the subset of a Claude session record
// ResolveClaudeSocketTarget needs — exactly the fields the vetting logic
// below reads. session.ClaudeSessionRecord is a type alias for this
// (internal/session/claude_title_reconcile.go): session already imports
// send (instance.go, for the #1777 Enter-attribution machinery), so
// defining the record once here and aliasing it in session avoids both an
// import cycle and a field-by-field mirror that can drift.
type ClaudeSocketRecord struct {
	Pid                 int
	SessionID           string
	ProcStart           string
	ProcStartFt         string
	PeerProtocol        int
	MessagingSocketPath string
}

// ResolveClaudeSocketTarget vets a Claude session record and, on success,
// returns a dialable target. On failure it returns *Unavailable, meaning the
// caller should fall back to tmux — every check here runs strictly before
// any byte is written to the socket.
//
// Vetting order (cheapest first): peerProtocol, socket path present, pid
// liveness, procStart identity, socket ownership, then the key file (whose
// absence is NOT a fallback reason — see the AuthToken doc comment and the
// #2089 CORRECTION in the plan: auth is optional on macOS/Linux, so an
// absent or unreadable key file just means "send without the auth line",
// mirroring Claude's own client. ReasonNoKey is reserved for a key file that
// DOES exist and IS readable but is malformed, or whose own procStart
// disagrees with the record — a torn pair, which is a real staleness signal).
func ResolveClaudeSocketTarget(rec ClaudeSocketRecord, claudeDir string) (ClaudeSocketTarget, error) {
	// An empty SessionID is never valid on its own: the frame's session_id
	// would be "", which Claude's receiver silently drops (session_id
	// mismatch, §1.4) rather than delivers — a send that LOOKS like success
	// but never reaches the target. Reject it here rather than letting it
	// through to become a false "queued_socket".
	if rec.SessionID == "" {
		return ClaudeSocketTarget{}, &Unavailable{Reason: ReasonNoRecord}
	}
	if rec.PeerProtocol != minPeerProtocol {
		return ClaudeSocketTarget{}, &Unavailable{Reason: ReasonOldProtocol,
			Err: fmt.Errorf("peerProtocol=%d, want %d", rec.PeerProtocol, minPeerProtocol)}
	}
	socketPath := strings.TrimSpace(rec.MessagingSocketPath)
	if socketPath == "" {
		return ClaudeSocketTarget{}, &Unavailable{Reason: ReasonNoSocketPath}
	}
	if err := killCheck(rec.Pid); err != nil {
		return ClaudeSocketTarget{}, &Unavailable{Reason: ReasonDeadPid, Err: err}
	}

	expectedProcStart := rec.ProcStart
	if expectedProcStart == "" {
		expectedProcStart = rec.ProcStartFt
	}
	if expectedProcStart != "" {
		actual, err := psLstart(rec.Pid)
		if err != nil || actual != expectedProcStart {
			return ClaudeSocketTarget{}, &Unavailable{Reason: ReasonProcStartDrift,
				Err: fmt.Errorf("ps lstart mismatch or unreadable for pid %d", rec.Pid)}
		}
	}

	info, err := os.Stat(socketPath)
	if err != nil {
		return ClaudeSocketTarget{}, &Unavailable{Reason: ReasonDialFailed, Err: err}
	}
	if info.Mode()&os.ModeSocket == 0 {
		return ClaudeSocketTarget{}, &Unavailable{Reason: ReasonDialFailed,
			Err: fmt.Errorf("%s is not a socket", socketPath)}
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if int(st.Uid) != os.Getuid() {
			return ClaudeSocketTarget{}, &Unavailable{Reason: ReasonWrongUID,
				Err: fmt.Errorf("socket owned by uid %d, want %d", st.Uid, os.Getuid())}
		}
	}

	target := ClaudeSocketTarget{
		SocketPath: socketPath,
		Pid:        rec.Pid,
		SessionID:  rec.SessionID,
	}

	token, keyErr := resolveAuthToken(claudeDir, rec.Pid, socketPath, expectedProcStart)
	if keyErr != nil {
		return ClaudeSocketTarget{}, keyErr
	}
	target.AuthToken = token
	return target, nil
}

// claudeKeyFile is the subset of a `<pid>.<sha256hex>.key` file's JSON body
// agent-deck reads.
type claudeKeyFile struct {
	PeerToken string `json:"peerToken"`
	ProcStart string `json:"procStart"`
}

// resolveAuthToken locates and validates the target's key file at its
// deterministic path (<pid>.<sha256(socketPath)>.key — the exact name
// Claude itself publishes, §1.3). Returns ("", nil) when that file doesn't
// exist or can't be read at all — that is NOT an error, per the #2089
// CORRECTION: Claude's own client sends no auth line when it has no token,
// and auth is optional on macOS/Linux anyway. Returns *Unavailable{ReasonNoKey}
// only when the deterministic-name file DOES exist and IS readable but is
// malformed (bad shape, bad perms, bad JSON, bad token) or whose own
// procStart disagrees with the record (a torn pair — real evidence of
// staleness). No <pid>.*.key glob fallback: a glob match isn't proof of
// authenticity the way the deterministic name is, so it isn't consulted.
func resolveAuthToken(claudeDir string, pid int, socketPath, expectedProcStart string) (string, error) {
	sessionsDir := filepath.Join(claudeDir, "sessions")
	sum := sha256.Sum256([]byte(filepath.Clean(socketPath)))
	keyPath := filepath.Join(sessionsDir, fmt.Sprintf("%d.%s.key", pid, hex.EncodeToString(sum[:])))

	data, err := os.ReadFile(keyPath)
	if err != nil {
		return "", nil // no key file at all: send without an auth line
	}

	if !keyFilenameRe.MatchString(filepath.Base(keyPath)) {
		return "", &Unavailable{Reason: ReasonNoKey, Err: fmt.Errorf("key filename does not match the expected shape")}
	}

	info, statErr := os.Stat(keyPath)
	if statErr == nil && info.Mode().Perm()&0o077 != 0 {
		return "", &Unavailable{Reason: ReasonNoKey, Err: fmt.Errorf("key file %s has group/other permission bits set", filepath.Base(keyPath))}
	}

	var kf claudeKeyFile
	if err := json.Unmarshal(data, &kf); err != nil {
		return "", &Unavailable{Reason: ReasonNoKey, Err: fmt.Errorf("key file is not valid JSON")}
	}
	if !peerTokenRe.MatchString(kf.PeerToken) {
		return "", &Unavailable{Reason: ReasonNoKey, Err: fmt.Errorf("key file token is not 32 lowercase hex chars")}
	}
	if expectedProcStart != "" && kf.ProcStart != "" && kf.ProcStart != expectedProcStart {
		return "", &Unavailable{Reason: ReasonNoKey, Err: fmt.Errorf("key file procStart disagrees with the session record (torn pair)")}
	}
	return kf.PeerToken, nil
}

// maxLineChars mirrors Claude's own preflight (offset 160,404,115): the
// receiver destroys any connection whose first line exceeds this many chars.
const maxLineChars = 1048576

// authFrame / userFrame are the exact JSON shapes Claude's own client writes
// (verified §1.4, offsets 160,413,700 / 159,609,800).
type authFrame struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

type userMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type userFrame struct {
	MsgV      int         `json:"msgV"`
	MsgID     string      `json:"msg_id"`
	Type      string      `json:"type"`
	SessionID string      `json:"session_id"`
	Message   userMessage `json:"message"`
	Priority  string      `json:"priority"`
}

// SendOverClaudeSocket writes the auth line (when t.AuthToken is non-empty)
// followed by the user-message line, then closes the connection. It returns
// the generated msg_id on success.
//
// Wire behaviour (verified §1.4/§1.5 against 2.1.259): ONE Write() of
// "<authLine>\n<userLine>\n" (authLine omitted entirely when there is no
// token — mirrors `R = token !== undefined ? URn(token) : ""`), then a
// 150ms delay before Close (Claude's own `qe = 150` macOS behaviour; applied
// unconditionally here since it is a no-op cost on a CLI send and Linux
// tolerates it fine). The server NEVER writes anything back on any path,
// success or refusal — so a nil error here means "the bytes were written to
// a live, identity-verified endpoint that had an inbox bound", nothing
// stronger. Once Write returns, the send is committed: a write-phase error
// becomes *CommittedError and must never be retried on tmux.
func SendOverClaudeSocket(t ClaudeSocketTarget, message string) (msgID string, err error) {
	var authLine string
	if t.AuthToken != "" {
		b, mErr := json.Marshal(authFrame{Type: "auth", Token: t.AuthToken})
		if mErr != nil {
			// Unreachable in practice (authFrame is two plain strings, always
			// marshalable) — pre-dial, so *Unavailable, not *CommittedError.
			return "", &Unavailable{Reason: ReasonNoRecord, Err: fmt.Errorf("marshal auth frame: %w", mErr)}
		}
		authLine = string(b) + "\n"
	}

	id := uuid.NewString()
	userB, mErr := json.Marshal(userFrame{
		MsgV:      1,
		MsgID:     id,
		Type:      "user",
		SessionID: t.SessionID,
		Message:   userMessage{Role: "user", Content: message},
		Priority:  "next",
	})
	if mErr != nil {
		// Unreachable in practice (userFrame is plain ints/strings, always
		// marshalable) — pre-dial, so *Unavailable, not *CommittedError.
		return "", &Unavailable{Reason: ReasonNoRecord, Err: fmt.Errorf("marshal user frame: %w", mErr)}
	}
	userLine := string(userB) + "\n"

	if len(authLine)+len(userLine) > maxLineChars {
		return "", &Unavailable{Reason: ReasonTooLarge,
			Err: fmt.Errorf("message is %d chars over Claude's %d-char line cap", len(authLine)+len(userLine)-maxLineChars, maxLineChars)}
	}

	conn, dialErr := dialUnix(t.SocketPath, 5*time.Second)
	if dialErr != nil {
		return "", &Unavailable{Reason: ReasonDialFailed, Err: dialErr}
	}
	defer func() { _ = conn.Close() }()

	// A local Unix-socket Write essentially never blocks in practice, but
	// "essentially never" is not a bound: a stuck/misbehaving receiver on
	// the other end (a wedged Claude process, a full kernel socket buffer
	// with nobody reading) must not hang this CLI forever. Expiry surfaces
	// as a Write() error below, which is already the correct *CommittedError
	// path — a deadline can expire mid-write, after some bytes are already
	// on the wire, so it is never safe to treat as a pre-write refusal.
	// Error discarded: *net.UnixConn's SetWriteDeadline cannot fail on a live
	// connection, and even if it somehow did, the failure mode is just "no
	// deadline" — the 5s dial timeout already happened, and Close() (deferred
	// above) still bounds how long a stuck receiver can hold this call.
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))

	if _, wErr := conn.Write([]byte(authLine + userLine)); wErr != nil {
		return "", &CommittedError{Err: wErr}
	}
	// Claude's own client: 150ms delay before End() on macOS to avoid racing
	// the server's read; applied unconditionally (§1.4).
	time.Sleep(150 * time.Millisecond)
	return id, nil
}
