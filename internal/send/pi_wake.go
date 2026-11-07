package send

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

// Pre-write reasons for the pi wake socket. Like the Claude reasons in
// claudesocket.go, each is observable strictly before any byte is written,
// so the caller may type the line instead.
const (
	ReasonSocketPathTooLong UnavailableReason = "socket_path_too_long"
	ReasonNoSocket          UnavailableReason = "no_socket"
	ReasonSocketPerms       UnavailableReason = "socket_perms"
	// ReasonPeerNotInPaneTree: the kernel says the process listening on the
	// socket is outside the target's pane tree. Unlike ReasonNotInPaneTree,
	// which picks among session records, this reads the live peer itself.
	ReasonPeerNotInPaneTree UnavailableReason = "peer_not_in_pane_tree"
	// ReasonContextDone: the caller's context ended before the frame was
	// written, so no wake was sent and the caller may type the line.
	ReasonContextDone UnavailableReason = "context_done"
)

const (
	// MaxSockPath is the longest unix socket path macOS accepts (sun_path is
	// 104 bytes including the NUL); Linux allows 107, so one limit is used.
	// The launch side (session.PiSocketPath) refuses longer paths up front.
	MaxSockPath = 103
	// piWakeFrameCap is the extension's input cap: an unended or larger
	// frame is destroyed unsent, so the client refuses to write one.
	piWakeFrameCap = 4096
	// piWakeReplyCap bounds how much reply data is read in the window.
	piWakeReplyCap = 64 * 1024

	defaultPiDialTimeout  = time.Second
	defaultPiWriteTimeout = 2 * time.Second
	defaultPiReplyWindow  = 2 * time.Second
)

// PiWakeTarget is a pi extension's wake socket.
type PiWakeTarget struct {
	Path string
}

type PiWakeOptions struct {
	// PanePIDs is the target's pane process tree; empty or nil matches nothing.
	PanePIDs map[int]bool
	// DialTimeout bounds the connect; zero means 1s.
	DialTimeout time.Duration
	// ReplyWindow bounds the wait for a reply once the frame is written; zero means 2s.
	ReplyWindow time.Duration
}

type PiWakeState string

const (
	// PiWakeSubmitted: pi started a run carrying our nonce.
	PiWakeSubmitted PiWakeState = "submitted"
	// PiWakeQueued: the extension handed the wake to pi but saw no run start.
	PiWakeQueued PiWakeState = "queued"
	// PiWakePushed: the frame was written and no usable reply came back. The
	// wake may or may not have landed, so it is never retyped.
	PiWakePushed PiWakeState = "pushed"
)

type PiWakeResult struct {
	State PiWakeState
}

// PiWakeRefused is the extension saying it did not hand the wake to pi
// (reply ok:false, notSent:true). Reason is the reply's state word. It is
// neither Unavailable (which would type the line) nor Committed (nothing was
// sent), so the caller leaves the records pending.
//
// AfterWrite is true only for the extension's own notSent reply, which comes
// after the frame was written. notSent is authoritative: nothing reached the
// model, and the wake carries no records (those ride before_agent_start), so
// the caller decides by Reason: a transient refusal (busy, duplicate) goes
// back to pending, a permanent one (bad_request, an unknown word) stops socket
// wakes for those records. Nothing is typed after either, nor after silence, a
// committed write or pushed. A refusal made here, before the frame is written,
// has AfterWrite false.
type PiWakeRefused struct {
	Reason     string
	AfterWrite bool
}

func (r *PiWakeRefused) Error() string { return "pi wake refused: " + r.Reason }

// VetPiSocket checks, from the filesystem alone, that path is a socket owned
// by callerUID that neither the group nor others can reach, in a directory
// that is likewise private. Failures are *Unavailable.
func VetPiSocket(path string, callerUID int) error {
	if len(path) > MaxSockPath {
		// The launch never exports a path this long, so no extension listens on it.
		return &Unavailable{Reason: ReasonSocketPathTooLong, Absent: true,
			Err: fmt.Errorf("path is %d bytes, the unix socket limit is %d", len(path), MaxSockPath)}
	}
	// Lstat: a symlink at the path could point the wake at some other socket.
	info, err := os.Lstat(path)
	if err != nil {
		return &Unavailable{Reason: ReasonNoSocket, Err: err, Absent: errors.Is(err, os.ErrNotExist)}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return &Unavailable{Reason: ReasonSocketPerms, Err: fmt.Errorf("%s is a symlink", path)}
	}
	if info.Mode()&os.ModeSocket == 0 {
		return &Unavailable{Reason: ReasonNoSocket, Err: fmt.Errorf("%s is not a socket", path)}
	}
	if err := vetPrivate(info, callerUID); err != nil {
		return err
	}
	// Lstat here too: a symlinked parent would route the path through a
	// directory this check never looked at.
	parent := filepath.Dir(path)
	dir, err := os.Lstat(parent)
	if err != nil {
		return &Unavailable{Reason: ReasonNoSocket, Err: err}
	}
	if dir.Mode()&os.ModeSymlink != 0 {
		return &Unavailable{Reason: ReasonSocketPerms, Err: fmt.Errorf("%s is a symlink", parent)}
	}
	return vetPrivate(dir, callerUID)
}

func vetPrivate(info os.FileInfo, callerUID int) error {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != callerUID {
		return &Unavailable{Reason: ReasonSocketPerms,
			Err: fmt.Errorf("%s owned by uid %d, want %d", info.Name(), st.Uid, callerUID)}
	}
	if info.Mode().Perm()&0o077 != 0 {
		return &Unavailable{Reason: ReasonSocketPerms,
			Err: fmt.Errorf("%s has group/other permission bits (%#o)", info.Name(), info.Mode().Perm())}
	}
	return nil
}

// writeFrame writes the frame; tests replace it to inject short writes.
var writeFrame = func(conn net.Conn, frame []byte) (int, error) { return conn.Write(frame) }

// piWakeNonceRe is the nonce shape the extension is sent: 26 alphanumerics.
var piWakeNonceRe = regexp.MustCompile(`^[0-9A-Za-z]{26}$`)

// piWakeFrame is the one line the extension reads.
type piWakeFrame struct {
	V    int      `json:"v"`
	ID   string   `json:"id"`
	Line string   `json:"line"`
	IDs  []string `json:"ids"`
}

// encodePiWakeFrame returns f as one newline-terminated JSON line. HTML
// escaping is off: json.Marshal writes each of <, > and & as a six-byte
// \u escape, which would carry a 900-byte line of markup past the frame cap,
// and the extension's JSON.parse needs none of it. The cap is checked on
// these bytes, the ones written.
func encodePiWakeFrame(f piWakeFrame) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil { // Encode ends the line with '\n'
		return nil, err
	}
	return b.Bytes(), nil
}

// piWakeReply is what the extension writes back. OK is a pointer because it
// is optional on submitted and queued replies.
type piWakeReply struct {
	ID      string `json:"id"`
	OK      *bool  `json:"ok"`
	State   string `json:"state"`
	NotSent bool   `json:"notSent"`
}

// SendPiWake rings one doorbell: it vets the socket, dials, confirms the
// listener is a process in the target's pane tree, writes a single framed
// line (the wake text and the ids of the records it announces) and waits for
// the reply bearing nonce. Every failure before the write is *Unavailable. A
// refused reply is *PiWakeRefused. A write failure is *CommittedError. A
// written frame with no usable reply is a nil error with State pushed: the
// send is committed and is never retried.
//
// A cancelled context is *Unavailable (ReasonContextDone) up to the peer
// check. Two edges stay on the conservative side: a cancel between the peer
// check and the write closes the conn, so the write fails with zero bytes sent
// and is a *CommittedError; a cancel during the write is a *CommittedError. The
// caller never types after either, so neither can double-deliver.
func SendPiWake(ctx context.Context, target PiWakeTarget, nonce, line string, ids []string, opts PiWakeOptions) (PiWakeResult, error) {
	if err := ctx.Err(); err != nil {
		return PiWakeResult{}, &Unavailable{Reason: ReasonContextDone, Err: err}
	}
	// The extension answers a frame without a usable nonce, line or ids
	// bad_request, so say so without spending a dial.
	if !piWakeNonceRe.MatchString(nonce) || line == "" || len(ids) == 0 {
		return PiWakeResult{}, &PiWakeRefused{Reason: "bad_request"}
	}
	for _, id := range ids {
		if id == "" {
			return PiWakeResult{}, &PiWakeRefused{Reason: "bad_request"}
		}
	}
	if err := VetPiSocket(target.Path, os.Getuid()); err != nil {
		var u *Unavailable
		if errors.As(err, &u) && u.Reason == ReasonSocketPerms {
			// A socket the wake will not use may still have a live pi behind it
			// (a live one is not absent). A stale file nobody answers on is: say
			// so, from a bare connect that writes nothing.
			// Never through a symlinked directory, whose target nobody vetted.
			pfi, perr := os.Lstat(filepath.Dir(target.Path))
			if fi, lerr := os.Lstat(target.Path); perr == nil && pfi.Mode()&os.ModeSymlink == 0 && lerr == nil && fi.Mode()&os.ModeSocket != 0 {
				if c, derr := dialUnix(target.Path, defaultPiDialTimeout); derr == nil {
					_ = c.Close()
				} else if errors.Is(derr, syscall.ECONNREFUSED) {
					u.Absent = true
				}
			}
		}
		return PiWakeResult{}, err
	}
	frame, err := encodePiWakeFrame(piWakeFrame{V: 1, ID: nonce, Line: line, IDs: ids})
	if err != nil {
		// Unreachable (three plain fields), and pre-dial, so Unavailable.
		return PiWakeResult{}, &Unavailable{Reason: ReasonNoRecord, Err: fmt.Errorf("marshal wake frame: %w", err)}
	}
	if len(frame) > piWakeFrameCap {
		// Refused, not Unavailable: Unavailable would type the oversize line
		// into the pane, and the extension would have destroyed the frame anyway.
		return PiWakeResult{}, &PiWakeRefused{Reason: "too_large"}
	}

	dialTimeout := opts.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = defaultPiDialTimeout
	}
	conn, err := dialUnix(target.Path, dialTimeout)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return PiWakeResult{}, &Unavailable{Reason: ReasonContextDone, Err: cerr}
		}
		// Only a refused connection or a missing file proves nobody is listening.
		absent := errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
		return PiWakeResult{}, &Unavailable{Reason: ReasonDialFailed, Err: err, Absent: absent}
	}
	// Cancellation closes the connection, which fails any blocked read or
	// write. The goroutine is always joined before return.
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()
	defer func() {
		close(stop)
		<-done
		_ = conn.Close()
	}()

	pid, err := peerPID(conn)
	// Checked first: a cancel closes the conn, which would otherwise read as
	// a peer failure.
	if cerr := ctx.Err(); cerr != nil {
		return PiWakeResult{}, &Unavailable{Reason: ReasonContextDone, Err: cerr}
	}
	if err != nil || !opts.PanePIDs[pid] {
		return PiWakeResult{}, &Unavailable{Reason: ReasonPeerNotInPaneTree,
			Err: fmt.Errorf("listener pid %d (%v) is not in the target pane tree", pid, err)}
	}

	replyWindow := opts.ReplyWindow
	if replyWindow == 0 {
		replyWindow = defaultPiReplyWindow
	}
	// Bounds a stuck receiver; expiry mid-write is a CommittedError, since
	// some bytes may already be on the wire.
	_ = conn.SetWriteDeadline(time.Now().Add(defaultPiWriteTimeout))
	if n, err := writeFrame(conn, frame); n < len(frame) || err != nil {
		if err == nil {
			err = io.ErrShortWrite
		}
		return PiWakeResult{}, &CommittedError{Err: err}
	}

	_ = conn.SetReadDeadline(time.Now().Add(replyWindow))
	br := bufio.NewReader(io.LimitReader(conn, piWakeReplyCap))
	for {
		raw, err := br.ReadBytes('\n')
		if err != nil {
			return PiWakeResult{State: PiWakePushed}, nil
		}
		var reply piWakeReply
		if json.Unmarshal(raw, &reply) != nil || reply.ID != nonce {
			continue // not ours: a stray or malformed line
		}
		if reply.OK != nil && !*reply.OK && reply.NotSent {
			return PiWakeResult{}, &PiWakeRefused{Reason: reply.State, AfterWrite: true}
		}
		switch PiWakeState(reply.State) {
		case PiWakeSubmitted, PiWakeQueued:
			return PiWakeResult{State: PiWakeState(reply.State)}, nil
		}
		return PiWakeResult{State: PiWakePushed}, nil
	}
}

// peerPID returns the pid of the process listening on conn's socket.
func peerPID(conn net.Conn) (int, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var sockErr error
	if err := raw.Control(func(fd uintptr) { pid, sockErr = peerPIDOfFd(int(fd)) }); err != nil {
		return 0, err
	}
	return pid, sockErr
}
