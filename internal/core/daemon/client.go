package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"
)

// Client is one protocol connection. It is not safe for concurrent use; a
// subscription takes the connection over (use another Client for calls).
type Client struct {
	c     net.Conn
	fc    *frameConn
	token string
	seq   int
	subID string
}

// helloTimeout bounds the wait for a daemon's hello.
const helloTimeout = 2 * time.Second

// Dial connects to the daemon socket and reads its hello.
func Dial(ctx context.Context, socket string) (*Client, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	cl := &Client{c: c, fc: newFrameConn(c)}
	_ = c.SetReadDeadline(time.Now().Add(helloTimeout))
	hello, err := cl.fc.read()
	_ = c.SetReadDeadline(time.Time{})
	if err == nil && hello.Type == TypeError && hello.Error != nil {
		err = hello.Error
	} else if err == nil && (hello.Type != TypeHello || hello.Token == "") {
		err = fmt.Errorf("daemon: unexpected first frame %q", hello.Type)
	}
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	cl.token = hello.Token
	return cl, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.c.Close() }

// roundtrip sends f and returns the reply with the same id. An error frame
// is returned as *FrameError.
func (c *Client) roundtrip(f Frame) (Frame, error) {
	c.seq++
	f.ID = strconv.Itoa(c.seq)
	f.Token = c.token
	if err := c.fc.write(f); err != nil {
		return Frame{}, err
	}
	for {
		reply, err := c.fc.read()
		if err != nil {
			return Frame{}, err
		}
		if reply.ID != f.ID {
			continue
		}
		if reply.Type == TypeError && reply.Error != nil {
			return reply, reply.Error
		}
		return reply, nil
	}
}

// Call runs a registered command and returns its response envelope. input
// is anything that marshals to the command's input object.
func (c *Client) Call(cmd string, input any) (json.RawMessage, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	reply, err := c.roundtrip(Frame{Type: TypeCall, Cmd: cmd, Input: raw})
	if err != nil {
		return nil, err
	}
	return reply.Envelope, nil
}

// Catalog returns the daemon's command catalog.
func (c *Client) Catalog() ([]CommandInfo, error) {
	reply, err := c.roundtrip(Frame{Type: TypeCatalog})
	return reply.Commands, err
}

// Status returns the daemon's status.
func (c *Client) Status() (Status, error) {
	reply, err := c.roundtrip(Frame{Type: TypeStatus})
	if err != nil {
		return Status{}, err
	}
	if reply.Status == nil {
		return Status{}, fmt.Errorf("daemon: status reply without status")
	}
	return *reply.Status, nil
}

// Shutdown asks the daemon to stop.
func (c *Client) Shutdown() error {
	_, err := c.roundtrip(Frame{Type: TypeShutdown})
	return err
}

// Subscribe starts streaming bus frames with a cursor greater than after.
// Read them with Next.
func (c *Client) Subscribe(after uint64) error {
	reply, err := c.roundtrip(Frame{Type: TypeSubscribe, After: after})
	if err != nil {
		return err
	}
	c.subID = reply.ID
	return nil
}

// Next returns the next event as the canonical JSON line `events follow
// --json` prints for it.
func (c *Client) Next() (json.RawMessage, error) {
	for {
		f, err := c.fc.read()
		if err != nil {
			return nil, err
		}
		if f.ID != c.subID {
			continue
		}
		switch f.Type {
		case TypeEvent:
			return f.Event, nil
		case TypeError:
			if f.Error != nil {
				return nil, f.Error
			}
		}
	}
}
