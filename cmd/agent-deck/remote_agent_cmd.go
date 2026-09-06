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
//
// The change feed's probe does not fork: it keeps the profile's storage open
// in this process and builds the two listings through the same functions
// `list --json` and `group list --json` print through (buildListJSON,
// buildGroupListJSON). Booting the binary twice per change, each opening
// storage and refreshing every status, was most of the second between a
// change on the remote and the local screen; the event carries the probe's
// duration as probe_ms so that cost stays visible.

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
	// A "changed" event carries the fresh listings so the local side can
	// apply them at once instead of asking again.
	Sessions string `json:"sessions,omitempty"`
	Groups   string `json:"groups,omitempty"`
	// ProbeMS is how long the "changed" event's listings took to build.
	ProbeMS int64 `json:"probe_ms,omitempty"`
}

// remoteAgentProbeFunc builds the current `list --json` and `group list
// --json` bodies for the change feed. The in-process one is the default;
// tests inject their own.
type remoteAgentProbeFunc func() (listJSON, groupJSON string, err error)

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
	// The in-process probe refreshes statuses through tmux like `list` does,
	// and `list` fixes up PATH for tmux before it starts.
	ensureTmuxOnPath()
	probe, closeProbe, err := newRemoteAgentProbe(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "remote-agent: %v\n", err)
		os.Exit(1)
	}
	defer closeProbe()
	runner := func(ctx context.Context, reqArgs []string) (string, string, int) {
		full := append([]string{"-p", profile}, reqArgs...)
		// The peer is the ssh-authenticated user who could run any of these
		// verbs as `ssh host agent-deck ...` anyway; the verb is checked
		// against the CLI's own registry and the deny list above, and the
		// arguments are passed as argv, never through a shell.
		cmd := exec.CommandContext(ctx, self, full...) //nolint:gosec // see comment above
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
	serveRemoteAgent(context.Background(), os.Stdin, os.Stdout, runner, probe, dbPath, 250*time.Millisecond)
}

// newRemoteAgentProbe opens the profile's storage once and returns a probe
// that loads it, refreshes statuses, and formats both listings exactly as
// the CLI would, plus the close for the storage handle. Like `list --json`
// it warms the tmux and hook-status caches once per probe, then formats
// both listings from that one load.
func newRemoteAgentProbe(profile string) (remoteAgentProbeFunc, func(), error) {
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		return nil, nil, err
	}
	probe := func() (string, string, error) {
		instances, groups, err := storage.LoadWithGroups()
		if err != nil {
			return "", "", err
		}
		session.RefreshInstancesForCLIStatus(instances)
		l, err := buildListJSON(storage.Profile(), instances)
		if err != nil {
			return "", "", err
		}
		g, err := buildGroupListJSON(session.NewGroupTreeWithGroups(instances, groups))
		if err != nil {
			return "", "", err
		}
		return string(l), string(g), nil
	}
	return probe, func() { _ = storage.Close() }, nil
}

// serveRemoteAgent is the agent loop, separated from process wiring so it is
// testable with pipes. It returns when stdin closes.
func serveRemoteAgent(ctx context.Context, in io.Reader, out io.Writer, run func(context.Context, []string) (string, string, int), probe remoteAgentProbeFunc, watchPath string, watchEvery time.Duration) {
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
	// forever. The stamp only triggers a probe: the agent builds the two
	// listings the TUI fetches and pushes "changed" only when their content
	// differs from what it last pushed.
	if watchPath != "" && watchEvery > 0 && probe != nil {
		go func() {
			last := remoteAgentStamp(watchPath)
			// Seed with the current content so the first stamp change is
			// judged against what the TUI already fetched at startup.
			l0, g0, _ := probe()
			lastHash := remoteAgentContentHash(l0, g0)
			t := time.NewTicker(watchEvery)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if remoteAgentStamp(watchPath) == last {
						continue
					}
					started := time.Now()
					l, g, err := probe()
					elapsed := time.Since(started).Milliseconds()
					if err != nil {
						// No listings to compare or push: tell the TUI
						// something changed and let it fetch.
						fmt.Fprintf(os.Stderr, "remote-agent: probe: %v\n", err)
						write(remoteAgentReply{Event: "changed", ProbeMS: elapsed})
					} else if h := remoteAgentContentHash(l, g); h != lastHash {
						lastHash = h
						write(remoteAgentReply{Event: "changed", Sessions: l, Groups: g, ProbeMS: elapsed})
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
		if !remoteAgentArgsAllowed(req.Args) {
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

// remoteAgentArgsAllowed admits only a known CLI verb that is not on the
// deny list, with arguments that cannot break the line protocol.
func remoteAgentArgsAllowed(args []string) bool {
	if len(args) == 0 || remoteAgentDeniedVerbs[args[0]] || !commandRegistry[args[0]] {
		return false
	}
	for _, a := range args {
		if strings.ContainsAny(a, "\n\r") {
			return false
		}
	}
	return true
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
