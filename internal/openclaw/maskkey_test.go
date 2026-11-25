//go:debug randseednop=0

package openclaw

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"math/rand"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestClientMaskKeyNotFromMathRand guards GO-2026-6278: the websocket library
// the OpenClaw client dials with must draw frame mask keys from crypto/rand,
// not from math/rand. The test reseeds math/rand to the same value before two
// dials and reads the mask key of the first client frame off the wire. With a
// math/rand mask key both keys are equal (predictable); with crypto/rand they
// differ (a 1 in 2^32 chance of a false failure).
//
// The //go:debug line above turns rand.Seed back on for this test binary; Go
// 1.24+ ignores it by default.
func TestClientMaskKeyNotFromMathRand(t *testing.T) {
	first := captureClientMaskKey(t, 1)
	second := captureClientMaskKey(t, 1)
	if first == second {
		t.Fatalf("client mask key %x repeats after reseeding math/rand: "+
			"keys are predictable (GO-2026-6278); bump github.com/gorilla/websocket", first)
	}
}

// captureClientMaskKey seeds math/rand, dials a raw TCP websocket endpoint
// with the same dialer the OpenClaw client uses, sends one text frame and
// returns the 4-byte mask key from that frame's header.
func captureClientMaskKey(t *testing.T, seed int64) [4]byte {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	type result struct {
		key [4]byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		var res result
		defer func() { done <- res }()
		conn, err := ln.Accept()
		if err != nil {
			res.err = err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil {
			res.err = err
			return
		}
		h := sha1.New()
		_, _ = io.WriteString(h, req.Header.Get("Sec-Websocket-Key")+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
		accept := base64.StdEncoding.EncodeToString(h.Sum(nil))
		_, err = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: "+accept+"\r\n\r\n")
		if err != nil {
			res.err = err
			return
		}
		// Frame header: FIN/opcode, MASK bit + 7-bit length, then the key.
		var hdr [6]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			res.err = err
			return
		}
		if hdr[1]&0x80 == 0 {
			res.err = io.ErrUnexpectedEOF
			return
		}
		copy(res.key[:], hdr[2:6])
	}()

	rand.Seed(seed) //nolint:staticcheck // deliberate: makes a math/rand mask key repeat
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}

	res := <-done
	if res.err != nil {
		t.Fatalf("server side: %v", res.err)
	}
	return res.key
}
