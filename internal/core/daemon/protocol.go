package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/core"
)

// ProtocolVersion is the frame layout version. Every client frame carries it
// as "v"; a frame with another value is answered with UNSUPPORTED_VERSION.
const ProtocolVersion = 1

// MaxFrameBytes bounds one frame (one line, excluding the newline).
const MaxFrameBytes = 1 << 20

// Frame types. See docs/daemon-protocol.md.
const (
	TypeHello      = "hello"
	TypeCall       = "call"
	TypeResult     = "result"
	TypeCatalog    = "catalog"
	TypeSubscribe  = "subscribe"
	TypeSubscribed = "subscribed"
	TypeEvent      = "event"
	TypeStatus     = "status"
	TypeShutdown   = "shutdown"
	TypeError      = "error"
)

// Protocol error codes, carried by error frames. Command failures are not
// protocol errors: they arrive as an envelope with ok=false and a core code.
const (
	CodeBadFrame           = "BAD_FRAME"
	CodeFrameTooLarge      = "FRAME_TOO_LARGE"
	CodeReadTimeout        = "READ_TIMEOUT"
	CodeServerBusy         = "SERVER_BUSY"
	CodePeerRejected       = "PEER_REJECTED"
	CodeAuthFailed         = "AUTH_FAILED"
	CodeUnsupportedVersion = "UNSUPPORTED_VERSION"
	CodeUnknownType        = "UNKNOWN_TYPE"
	CodeAlreadySubscribed  = "ALREADY_SUBSCRIBED"
	CodeEventsUnavailable  = "EVENTS_UNAVAILABLE"
	CodeCursorTooOld       = "CURSOR_TOO_OLD"
	// CodeRemoteDenied is an envelope error code: the command exists but its
	// Def is marked RemoteDeny, so it only runs from the local CLI.
	CodeRemoteDenied = "REMOTE_DENIED"
)

// Frame is one protocol message in either direction: a single line of JSON.
// Fields not used by a frame type are omitted.
type Frame struct {
	V     int    `json:"v"`
	Type  string `json:"type"`
	ID    string `json:"id,omitempty"`
	Token string `json:"token,omitempty"`

	// call
	Cmd   string          `json:"cmd,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// subscribe
	After uint64 `json:"after,omitempty"`

	// server to client
	Envelope json.RawMessage `json:"envelope,omitempty"`
	Event    json.RawMessage `json:"event,omitempty"`
	Commands []CommandInfo   `json:"commands,omitempty"`
	Status   *Status         `json:"status,omitempty"`
	Error    *FrameError     `json:"error,omitempty"`
}

func isBulkRestart(f Frame) bool {
	if f.Type != TypeCall || f.Cmd != core.IDSessionRestart {
		return false
	}
	var in struct {
		All bool `json:"all"`
	}
	return json.Unmarshal(f.Input, &in) == nil && in.All
}

// FrameError is the body of an error frame.
type FrameError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *FrameError) Error() string { return e.Code + ": " + e.Message }

// CommandInfo is one catalog entry: a registered command as the CLI sees it.
type CommandInfo struct {
	ID      string   `json:"id"`
	CLI     []string `json:"cli,omitempty"`
	Summary string   `json:"summary,omitempty"`
	Class   string   `json:"class"`
	Remote  string   `json:"remote"`
}

// Status describes a running daemon (hello and status frames).
type Status struct {
	PID         int    `json:"pid"`
	Profile     string `json:"profile"`
	Socket      string `json:"socket"`
	Version     string `json:"version"`
	StartedAt   string `json:"started_at"`
	Calls       uint64 `json:"calls"`
	Connections int64  `json:"connections"`
}

var (
	errFrameTooLarge = errors.New("frame too large")
	errBadFrame      = errors.New("bad frame")
)

// frameConn reads and writes frames on any byte stream, so the same layer
// serves a Unix socket today and a TCP connection later. Writes are
// serialized; reads must come from one goroutine.
type frameConn struct {
	r  *bufio.Reader
	mu sync.Mutex
	w  io.Writer
}

func newFrameConn(rw io.ReadWriter) *frameConn {
	return &frameConn{r: bufio.NewReader(rw), w: rw}
}

func (c *frameConn) readLine() ([]byte, error) {
	var line []byte
	for {
		part, err := c.r.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > MaxFrameBytes+1 {
			return nil, errFrameTooLarge
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return line[:len(line)-1], nil
	}
}

// read returns the next frame, errFrameTooLarge, errBadFrame, or the
// transport's error (io.EOF on a clean close).
func (c *frameConn) read() (Frame, error) {
	line, err := c.readLine()
	if err != nil {
		return Frame{}, err
	}
	var f Frame
	if err := json.Unmarshal(line, &f); err != nil {
		return Frame{}, fmt.Errorf("%w: %v", errBadFrame, err)
	}
	return f, nil
}

// readStrict applies the documented client-frame grammar. Response reads
// remain permissive so older clients can ignore fields added by a server.
func (c *frameConn) readStrict() (Frame, error) {
	line, err := c.readLine()
	if err != nil {
		return Frame{}, err
	}
	var f Frame
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return Frame{}, fmt.Errorf("%w: %v", errBadFrame, err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Frame{}, fmt.Errorf("%w: trailing JSON value", errBadFrame)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return Frame{}, fmt.Errorf("%w: %v", errBadFrame, err)
	}
	for key := range fields {
		if key == "v" || key == "type" || key == "id" || key == "token" ||
			(f.Type == TypeCall && (key == "cmd" || key == "input")) ||
			(f.Type == TypeSubscribe && key == "after") {
			continue
		}
		return Frame{}, fmt.Errorf("%w: field %q is not valid for %s", errBadFrame, key, f.Type)
	}
	return f, nil
}

// write sends f as one line, stamping the protocol version.
func (c *frameConn) write(f Frame) error {
	f.V = ProtocolVersion
	line, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(line) > MaxFrameBytes {
		tooLarge := errorFrame(f.ID, CodeFrameTooLarge, "outgoing frame exceeds %d bytes", MaxFrameBytes)
		tooLarge.V = ProtocolVersion
		line, err = json.Marshal(tooLarge)
		if err != nil {
			return err
		}
	}
	line = append(line, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	if conn, ok := c.w.(net.Conn); ok {
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
	}
	_, err = c.w.Write(line)
	return err
}

func errorFrame(id, code, format string, args ...any) Frame {
	return Frame{Type: TypeError, ID: id, Error: &FrameError{Code: code, Message: fmt.Sprintf(format, args...)}}
}
