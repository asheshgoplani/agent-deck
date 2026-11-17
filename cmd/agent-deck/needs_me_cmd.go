package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/needsme"
)

func handleNeedsMe(profile string, args []string) {
	code := runNeedsMe(profile, args, os.Stdout, os.Stderr, func() []needsme.Source {
		self, err := os.Executable()
		if err != nil {
			self = os.Args[0]
		}
		return needsme.DefaultSources(self, profile)
	})
	if code != 0 {
		exitCLI(code)
	}
}

// runNeedsMe is `needs-me`: read-only, all local profiles + all remotes.
// sources builds the sources lazily so flag errors stay cheap.
func runNeedsMe(_ string, args []string, stdout, stderr io.Writer, sources func() []needsme.Source) int {
	fs := flag.NewFlagSet("needs-me", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Output as JSON")
	timeout := fs.Duration("timeout", needsme.DefaultTimeout, "Time budget per host (local profile or remote)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: agent-deck needs-me [--json] [--timeout 3s]")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Sessions waiting for you, longest first: every local profile plus every configured")
		fmt.Fprintln(stderr, "remote, asked in parallel. An unreachable host is listed under errors, never a failure.")
		fmt.Fprintln(stderr, "Plain text prints one line per session. Read-only.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		if err == nil {
			fs.Usage()
		}
		return 2
	}
	var srcs []needsme.Source
	if sources != nil {
		srcs = sources()
	}
	res := needsme.Collect(context.Background(), needsme.Options{Sources: srcs, Timeout: *timeout, Now: time.Now})
	writeNeedsMe(stdout, stderr, res, *asJSON)
	return 0
}

func writeNeedsMe(stdout, stderr io.Writer, res needsme.Result, asJSON bool) {
	if asJSON {
		b, _ := json.Marshal(res)
		fmt.Fprintln(stdout, string(b))
		return
	}
	if len(res.Sessions) == 0 {
		fmt.Fprintln(stdout, "Nobody is waiting for you.")
	}
	for _, s := range res.Sessions {
		fmt.Fprintln(stdout, needsme.Line(s))
	}
	for _, e := range res.Errors {
		fmt.Fprintf(stderr, "warning: %s: %s\n", e.Host, e.Error)
	}
}
