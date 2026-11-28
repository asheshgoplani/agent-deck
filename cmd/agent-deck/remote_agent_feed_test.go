package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// {"id":N,"feed":"bare"} is acknowledged and from then on the connection's
// "changed" events carry no listings (stamp and probe_ms kept); without it
// the listings are pushed as before. An unknown feed value is refused, and a
// line an older agent cannot read as a feed request (no args) is refused
// with a non-zero code: that is what an agent predating the field answers to
// {"feed":"bare"}, which tells the client to keep applying pushed listings.
func TestRemoteAgent_FeedBare(t *testing.T) {
	for _, tc := range []struct {
		name       string
		lines      []string
		wantCodes  []int
		wantBare   bool
		errContain []string
	}{
		{name: "default pushes listings", wantBare: false},
		{name: "bare", lines: []string{`{"id":5,"feed":"bare"}`}, wantCodes: []int{0}, wantBare: true},
		{
			name:       "unknown feed refused",
			lines:      []string{`{"id":5,"feed":"full"}`},
			wantCodes:  []int{2},
			errContain: []string{"unknown feed"},
		},
		{
			// An old agent decodes {"id":5,"feed":"bare"} as this.
			name:       "old agent view: no args",
			lines:      []string{`{"id":5,"feedx":"bare"}`},
			wantCodes:  []int{2},
			errContain: []string{"verb not allowed"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			db := filepath.Join(dir, "state.db")
			if err := writeFileAtomic(db, "v1"); err != nil {
				t.Fatal(err)
			}
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			run := func(context.Context, []string) (string, string, int) { return "", "", 0 }
			probe := func() (string, string, error) {
				content, _ := os.ReadFile(db)
				return `["` + string(content) + `"]`, "{}", nil
			}
			done := make(chan struct{})
			go func() {
				serveRemoteAgent(context.Background(), inR, outW, remoteAgentConfig{Run: run, Probe: probe, WatchPath: db, WatchEvery: 20 * time.Millisecond, ProbeQuiet: 30 * time.Millisecond})
				_ = outW.Close()
				close(done)
			}()
			lines := make(chan remoteAgentReply, 16)
			go func() {
				sc := bufio.NewScanner(outR)
				for sc.Scan() {
					var r remoteAgentReply
					if err := json.Unmarshal(sc.Bytes(), &r); err == nil {
						lines <- r
					}
				}
				close(lines)
			}()
			next := func() remoteAgentReply {
				t.Helper()
				for {
					select {
					case r := <-lines:
						if r.Event == "ping" {
							continue
						}
						return r
					case <-time.After(3 * time.Second):
						t.Fatal("no line from the agent within 3s")
						return remoteAgentReply{}
					}
				}
			}
			if r := next(); r.Event != "ready" {
				t.Fatalf("first line %+v, want ready", r)
			}
			for i, line := range tc.lines {
				if _, err := io.WriteString(inW, line+"\n"); err != nil {
					t.Fatal(err)
				}
				r := next()
				if r.ID != 5 || r.Code != tc.wantCodes[i] || r.Event != "" {
					t.Fatalf("reply to %s = %+v, want code %d", line, r, tc.wantCodes[i])
				}
				if len(tc.errContain) > i && !strings.Contains(r.Error, tc.errContain[i]) {
					t.Fatalf("reply to %s: error %q, want %q", line, r.Error, tc.errContain[i])
				}
			}
			time.Sleep(40 * time.Millisecond)
			if err := writeFileAtomic(db, "v2-longer"); err != nil {
				t.Fatal(err)
			}
			r := next()
			if r.Event != "changed" || r.Stamp == 0 {
				t.Fatalf("want a stamped changed event, got %+v", r)
			}
			if tc.wantBare {
				if r.Sessions != "" || r.Groups != "" {
					t.Fatalf("bare feed: changed event carries listings: %+v", r)
				}
			} else if r.Sessions != `["v2-longer"]` || r.Groups != "{}" {
				t.Fatalf("default feed: changed event %+v, want the listings", r)
			}
			_ = inW.Close()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("agent did not exit when stdin closed")
			}
		})
	}
}
