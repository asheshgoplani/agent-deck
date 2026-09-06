package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `agent-deck remote-agent` is the remote end of a persistent channel
// (#2174). A local TUI opens ONE ssh session per remote to it and keeps it
// open: requests arrive as JSON lines on stdin ({"id":1,"args":["list",
// "--json"]}), answers leave as JSON lines on stdout ({"id":1,"stdout":"...",
// "stderr":"...","code":0}). Between answers the agent pushes
// {"event":"changed"} whenever the profile's state.db changes, so the TUI
// refetches at once instead of at its next poll.
//
// Each request runs this same binary as a subprocess with the given args, so
// the answer is byte-for-byte what `ssh host agent-deck <args>` would print;
// only the ssh handshake and channel setup per command are gone, and the
// change feed is new. Requests run concurrently; answers carry their id.

type remoteAgentRequest struct {
	ID   int64    `json:"id"`
	Args []string `json:"args"`
}

type remoteAgentReply struct {
	ID     int64  `json:"id,omitempty"`
	Event  string `json:"event,omitempty"`
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	Code   int    `json:"code"`
	Error  string `json:"error,omitempty"`
}

// remoteAgentDeniedVerbs are never run through the channel: they need a
// terminal, or must not be reachable from a remote TUI at all.
var remoteAgentDeniedVerbs = map[string]bool{
	"remote-agent": true, "web": true, "uninstall": true, "update": true,
}

func handleRemoteAgent(profile string, args []string) {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			fmt.Println("Usage: agent-deck remote-agent")
			fmt.Println()
			fmt.Println("Serve JSON-line requests on stdin for a local agent-deck TUI (one persistent")
			fmt.Println("channel per remote) and push {\"event\":\"changed\"} when the profile's state changes.")
			return
		}
	}
	dbPath, err := session.GetDBPathForProfile(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "remote-agent: %v\n", err)
		os.Exit(1)
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "remote-agent: %v\n", err)
		os.Exit(1)
	}
	runner := func(ctx context.Context, reqArgs []string) (string, string, int) {
		full := append([]string{"-p", profile}, reqArgs...)
		cmd := exec.CommandContext(ctx, self, full...)
		var out, errb strings.Builder
		cmd.Stdout = &out
		cmd.Stderr = &errb
		code := 0
		if err := cmd.Run(); err != nil {
			code = 1
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		}
		return out.String(), errb.String(), code
	}
	serveRemoteAgent(context.Background(), os.Stdin, os.Stdout, runner, dbPath, 250*time.Millisecond)
}

// serveRemoteAgent is the agent loop, separated from process wiring so it is
// testable with pipes. It returns when stdin closes.
func serveRemoteAgent(ctx context.Context, in io.Reader, out io.Writer, run func(context.Context, []string) (string, string, int), watchPath string, watchEvery time.Duration) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wmu sync.Mutex
	write := func(r remoteAgentReply) {
		b, err := json.Marshal(r)
		if err != nil {
			return
		}
		wmu.Lock()
		_, _ = out.Write(append(b, '\n'))
		wmu.Unlock()
	}

	write(remoteAgentReply{Event: "ready"})

	// Change feed. A cheap mtime/size poll on state.db notices any write by
	// any process on the remote; but a status refresh by `list` itself also
	// writes, so a bare stamp would feed back into the TUI's own refetch
	// forever. The stamp only triggers a probe: the agent runs the two
	// listings the TUI fetches and pushes "changed" only when their content
	// differs from what it last pushed.
	if watchPath != "" && watchEvery > 0 {
		go func() {
			last := remoteAgentStamp(watchPath)
			// Seed with the current content so the first stamp change is
			// judged against what the TUI already fetched at startup.
			sctx, scancel := context.WithTimeout(ctx, 30*time.Second)
			l0, _, _ := run(sctx, []string{"list", "--json"})
			g0, _, _ := run(sctx, []string{"group", "list", "--json"})
			scancel()
			lastHash := remoteAgentContentHash(l0, g0)
			t := time.NewTicker(watchEvery)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					cur := remoteAgentStamp(watchPath)
					if cur == last {
						continue
					}
					last = cur
					pctx, pcancel := context.WithTimeout(ctx, 30*time.Second)
					l, _, _ := run(pctx, []string{"list", "--json"})
					g, _, _ := run(pctx, []string{"group", "list", "--json"})
					pcancel()
					h := remoteAgentContentHash(l, g)
					if h != lastHash {
						lastHash = h
						write(remoteAgentReply{Event: "changed"})
					}
					// The probe's own status refresh may have touched the
					// DB again; take that stamp as seen.
					last = remoteAgentStamp(watchPath)
				}
			}
		}()
	}

	var wg sync.WaitGroup
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req remoteAgentRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			write(remoteAgentReply{Code: 2, Error: "bad request: " + err.Error()})
			continue
		}
		if len(req.Args) == 0 || remoteAgentDeniedVerbs[req.Args[0]] {
			write(remoteAgentReply{ID: req.ID, Code: 2, Error: "verb not allowed over the channel"})
			continue
		}
		wg.Add(1)
		go func(req remoteAgentRequest) {
			defer wg.Done()
			rctx, rcancel := context.WithTimeout(ctx, 5*time.Minute)
			defer rcancel()
			stdout, stderr, code := run(rctx, req.Args)
			write(remoteAgentReply{ID: req.ID, Stdout: stdout, Stderr: stderr, Code: code})
		}(req)
	}
	cancel()
	wg.Wait()
}

// remoteAgentContentHash hashes what the TUI would see, ignoring the
// last_activity timestamps that a status refresh rewrites on every listing.
func remoteAgentContentHash(listJSON, groupJSON string) string {
	scrub := remoteAgentActivityField.ReplaceAllString(listJSON, "")
	sum := sha256.Sum256([]byte(scrub + "\x00" + groupJSON))
	return hex.EncodeToString(sum[:8])
}

var remoteAgentActivityField = regexp.MustCompile(`"last_activity_at":\s*"[^"]*",?`)

// remoteAgentStamp folds the mtime and size of the state DB, its WAL and
// SHM into one comparable string.
func remoteAgentStamp(dbPath string) string {
	var b strings.Builder
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if st, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", filepath.Base(p), st.ModTime().UnixNano(), st.Size())
		}
	}
	return b.String()
}
