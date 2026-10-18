package main

import (
	"bytes"
	"errors"
	"testing"
)

// limitWriter accepts up to limit bytes, then fails.
type limitWriter struct {
	buf   bytes.Buffer
	limit int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	n := len(p)
	if n > w.limit {
		n = w.limit
	}
	w.buf.Write(p[:n])
	w.limit -= n
	if n < len(p) {
		return n, errors.New("closed")
	}
	return n, nil
}

func forwardAcross(t *testing.T, limit int, chunks ...string) (string, string) {
	t.Helper()
	f := &mcpProxyForwarder{}
	first := &limitWriter{limit: limit}
	f.reset()
	i := 0
	for ; i < len(chunks); i++ {
		if !f.send(first, []byte(chunks[i])) {
			i++
			break
		}
	}
	second := &limitWriter{limit: 1 << 20}
	f.reset()
	if len(f.pending) > 0 && !f.send(second, nil) {
		t.Fatal("second connection rejected carried data")
	}
	for ; i < len(chunks); i++ {
		if !f.send(second, []byte(chunks[i])) {
			t.Fatal("second connection failed")
		}
	}
	return first.buf.String(), second.buf.String()
}

func TestMCPProxyForwarderKeepsUnwrittenRequest(t *testing.T) {
	// Connection closes before any byte of the dequeued request is written.
	a, b := forwardAcross(t, len("{\"id\":1}\n"), "{\"id\":1}\n", "{\"id\":2}\n", "{\"id\":3}\n")
	if a != "{\"id\":1}\n" || b != "{\"id\":2}\n{\"id\":3}\n" {
		t.Fatalf("got %q then %q", a, b)
	}
}

func TestMCPProxyForwarderKeepsUnwrittenTailOfChunk(t *testing.T) {
	// Short write ending exactly on a request boundary inside one chunk.
	a, b := forwardAcross(t, 9, "{\"id\":1}\n{\"id\":2}\n")
	if a != "{\"id\":1}\n" || b != "{\"id\":2}\n" {
		t.Fatalf("got %q then %q", a, b)
	}
}

func TestMCPProxyForwarderDropsPartialRequestSuffix(t *testing.T) {
	// Prefix delivered, then the connection dies mid-request.
	a, b := forwardAcross(t, 4, "{\"id\":1}\n{\"id\":2}\n")
	if a != "{\"id" || b != "{\"id\":2}\n" {
		t.Fatalf("got %q then %q", a, b)
	}
}

func TestMCPProxyForwarderDropsContinuationAcrossReads(t *testing.T) {
	// Request spans two stdin reads; the first read was delivered, the
	// continuation gets zero progress. It must not be replayed alone.
	a, b := forwardAcross(t, 6, "{\"id\":", "1}\n{\"id\":2}\n")
	if a != "{\"id\":" || b != "{\"id\":2}\n" {
		t.Fatalf("got %q then %q", a, b)
	}
}

func TestMCPProxyForwarderDropsContinuationSpanningLaterReads(t *testing.T) {
	a, b := forwardAcross(t, 3, "{\"a", "bc", "def", "}\n{\"id\":2}\n")
	if a != "{\"a" || b != "{\"id\":2}\n" {
		t.Fatalf("got %q then %q", a, b)
	}
}

func TestMCPProxyExpectedParent(t *testing.T) {
	cases := []struct {
		current int
		env     string
		want    int
		gone    bool
	}{
		{current: 1, env: "", want: 1},                    // PID-1 container host stays supported
		{current: 1, env: "4242", want: 4242, gone: true}, // launcher exited before startup
		{current: 4242, env: "4242", want: 4242},
		{current: 77, env: "bogus", want: 77},
	}
	for _, c := range cases {
		got, gone := mcpProxyExpectedParent(c.current, c.env)
		if got != c.want || gone != c.gone {
			t.Errorf("(%d,%q) = %d,%v; want %d,%v", c.current, c.env, got, gone, c.want, c.gone)
		}
	}
}
