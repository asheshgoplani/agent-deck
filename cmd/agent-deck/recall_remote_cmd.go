package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/cards"
	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Recall phase 4, remote (docs/recall.md "Remote"): the federated query is
// the default and stores nothing; card sync is opt-in behind
// [recall] remote_cards = true on both ends.

// ---- forwarding: what `agent-deck remote <host> recall ...` may carry ----

// remoteRecallVerbs are the read-only recall verbs a remote may be asked
// to run. Nothing here writes on the remote or opens a controller path.
var remoteRecallVerbs = map[string]bool{"search": true, "sessions": true, "show": true, "context": true, "export": true, "status": true}

// remoteRecallOptions is the closed option set per verb (true: takes a
// value), mirroring remoteSwitchOptions: an unknown option is refused
// before SSH. --into, --remote and --all-remotes are deliberately absent:
// a delivery or a second hop is never forwarded.
var remoteRecallOptions = map[string]map[string]bool{
	"search": {"harness": true, "profile": true, "project": true, "since": true, "hint": true, "tag": true, "session": true, "role": true,
		"phrase-scan-limit": true, "limit": true, "subagents": false, "phrase": false, "no-sweep": false, "json": false},
	"sessions": {"harness": true, "profile": true, "project": true, "since": true, "hint": true, "tag": true, "session": true, "limit": true,
		"subagents": false, "no-sweep": false, "json": false},
	"show":    {"tier": true, "turns": true, "json": false},
	"context": {"tier": true, "budget": true, "json": false},
	"export":  {"since": true, "cards": false, "json": false},
	"status":  {"json": false},
}

// validateRemoteRecallArgs checks `recall <verb> ...` before it is
// forwarded. A help request passes through so the remote's own usage
// answers.
func validateRemoteRecallArgs(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		return nil
	}
	verb := args[0]
	if !remoteRecallVerbs[verb] {
		return fmt.Errorf("unsupported remote command %q; remote recall forwards search, sessions, show, context, export and status only", "recall "+strings.Join(args, " "))
	}
	rest := args[1:]
	if len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h") {
		return nil
	}
	options := remoteRecallOptions[verb]
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			continue
		}
		name, _, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		takesValue, known := options[name]
		if !known {
			return fmt.Errorf("unsupported option %q for remote recall %s", arg, verb)
		}
		if !takesValue {
			if inline {
				return fmt.Errorf("option --%s takes no value", name)
			}
			continue
		}
		if !inline {
			// The value is the next argument, which must exist and must
			// not itself be an option (the shape validateRemoteSwitchArgs
			// uses, which gosec's slice-bounds pass can follow).
			after := rest[i+1:]
			if len(after) == 0 || strings.HasPrefix(after[0], "-") {
				return fmt.Errorf("option --%s needs a value", name)
			}
			i++
		}
	}
	return nil
}

func isRecallArgs(args []string) bool { return len(args) > 0 && args[0] == "recall" }

// remoteRecallUnsupported classifies an older remote's answer to a
// forwarded recall verb. Three shapes exist: an agent-deck before recall
// (v1.16.12 and older) says the command is not recognized; a v1.16.13
// remote knows the phase 1-3 verbs but not a phase-4 one (pull, context,
// export) and answers "unknown recall command: <verb>" with its own usage
// text, exit 1; and a remote with every verb but [recall] enabled = false
// answers "recall is off" with exit 2. Any other failure passes through as
// the remote printed it.
func remoteRecallUnsupported(args []string, code int, stdout, stderr string) (reason string, ok bool) {
	if code == 0 || !isRecallArgs(args) {
		return "", false
	}
	combined := stdout + "\n" + stderr
	switch {
	case strings.Contains(combined, `unknown command "recall"`), strings.Contains(combined, `"recall" is not a recognized command`):
		return reasonPredatesRecall, true
	case strings.Contains(combined, "recall is off"):
		return reasonRecallOff, true
	}
	const marker = "unknown recall command: "
	if idx := strings.Index(combined, marker); idx >= 0 {
		verb := strings.TrimSpace(strings.SplitN(combined[idx+len(marker):], "\n", 2)[0])
		if verb != "" {
			return reasonPredatesRecall + " " + verb, true
		}
		return reasonPredatesRecall, true
	}
	return "", false
}

// The two reasons remoteRecallUnsupported reports; the message picks the
// fix by them.
const (
	reasonPredatesRecall = "predates recall"
	reasonRecallOff      = "has [recall] enabled = false"
)

// remoteRecallUnsupportedMessage is the one clear line: which remote, what
// it runs, what fixes it (the phase-1 `session annotate` precedent).
func remoteRecallUnsupportedMessage(remote, remoteVersion, reason string) string {
	runs := "v" + remoteVersion
	if remoteVersion == "" {
		runs = "an unknown agent-deck version"
	}
	fix := fmt.Sprintf("update it with 'agent-deck remote update %s'", remote)
	if reason == reasonRecallOff {
		fix = "set [recall] enabled = true in its config.toml"
	}
	return fmt.Sprintf("remote %q runs %s that %s; %s", remote, runs, reason, fix)
}

// remoteRecallUnsupportedJSON is the --json shape: {error, remote, remote_version}.
func remoteRecallUnsupportedJSON(remote, remoteVersion, reason string) []byte {
	msg := remoteRecallUnsupportedMessage(remote, remoteVersion, reason)
	if remoteVersion == "" {
		remoteVersion = remoteVersionUnknown
	}
	out, _ := json.Marshal(struct {
		Error         string `json:"error"`
		Remote        string `json:"remote"`
		RemoteVersion string `json:"remote_version"`
	}{msg, remote, remoteVersion})
	return append(out, '\n')
}

// ---- federated search ----------------------------------------------------

// remoteSearchRunner is what the federated query needs from a remote:
// one command's stdout, and its version when the command is refused.
type remoteSearchRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
	CheckBinary(ctx context.Context) (string, bool)
}

// newRemoteSearchRunner builds the SSH runner; tests replace it.
var newRemoteSearchRunner = func(name string, rc session.RemoteConfig) remoteSearchRunner {
	return session.NewSSHRunner(name, rc)
}

// RemoteSearchResult is one remote's answer in a federated search.
type RemoteSearchResult struct {
	Remote        string      `json:"remote"`
	RemoteVersion string      `json:"remote_version,omitempty"`
	Hits          []query.Hit `json:"hits,omitempty"`
	Candidates    int         `json:"candidates,omitempty"`
	ElapsedMS     int64       `json:"elapsed_ms"`
	Error         string      `json:"error,omitempty"`
}

// resolveRemoteTargets turns --remote values / --all-remotes into the
// configured remote names, sorted.
func resolveRemoteTargets(cfg *session.UserConfig, named []string, all bool) ([]string, error) {
	if !all && len(named) == 0 {
		return nil, nil
	}
	var out []string
	if all {
		for name := range cfg.Remotes {
			out = append(out, name)
		}
		sort.Strings(out)
		if len(out) == 0 {
			return nil, errors.New("--all-remotes: no [remotes.<name>] configured")
		}
		return out, nil
	}
	seen := map[string]bool{}
	for _, name := range named {
		if _, ok := cfg.Remotes[name]; !ok {
			return nil, fmt.Errorf("remote %q not found", name)
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}

// federatedSearch runs `recall search <q> <filters> --json` on each remote
// in turn (one SSH round trip each, 1 to 2 s dominated by the handshake)
// and labels every hit with the remote it came from. The remote runs its
// own bounded sweep and its own ranking; hits are not re-ranked here.
func federatedSearch(ctx context.Context, cfg *session.UserConfig, remotes []string, forwarded []string) []RemoteSearchResult {
	var out []RemoteSearchResult
	for _, name := range remotes {
		out = append(out, queryOneRemote(ctx, name, newRemoteSearchRunner(name, cfg.Remotes[name]), forwarded))
	}
	return out
}

func queryOneRemote(ctx context.Context, name string, runner remoteSearchRunner, forwarded []string) RemoteSearchResult {
	start := time.Now()
	res := RemoteSearchResult{Remote: name}
	args := append([]string{"recall", "search"}, forwarded...)
	stdout, err := runner.Run(ctx, args...)
	res.ElapsedMS = time.Since(start).Milliseconds()
	if err != nil {
		if reason, ok := remoteRecallUnsupported(args, 1, string(stdout), err.Error()); ok {
			version, _ := runner.CheckBinary(ctx)
			res.RemoteVersion = version
			if version == "" {
				res.RemoteVersion = remoteVersionUnknown
			}
			res.Error = remoteRecallUnsupportedMessage(name, version, reason)
			return res
		}
		res.Error = fmt.Sprintf("remote %q: %v", name, err)
		return res
	}
	var reply struct {
		Success bool               `json:"success"`
		Error   string             `json:"error"`
		Result  query.SearchResult `json:"result"`
	}
	if err := json.Unmarshal(stdout, &reply); err != nil {
		res.Error = fmt.Sprintf("remote %q answered something that is not recall search --json: %v", name, err)
		return res
	}
	if !reply.Success && reply.Error != "" {
		res.Error = fmt.Sprintf("remote %q: %s", name, reply.Error)
		return res
	}
	res.Hits, res.Candidates = reply.Result.Hits, reply.Result.Candidates
	for i := range res.Hits {
		res.Hits[i].Remote = name
	}
	return res
}

func printRemoteSearch(remotes []RemoteSearchResult) {
	for _, r := range remotes {
		if r.Error != "" {
			fmt.Printf("remote %s: %s\n", r.Remote, r.Error)
			continue
		}
		fmt.Printf("remote %s: %d session(s), %d matching messages, %d ms (ranked there; 'agent-deck remote %s recall show <id>' to read one)\n", r.Remote, len(r.Hits), r.Candidates, r.ElapsedMS, r.Remote)
		for _, h := range r.Hits {
			fmt.Println(formatRecallHit(h))
			if h.Snippet != "" {
				fmt.Println("      " + h.Snippet)
			}
		}
	}
}

// ---- export / import / pull ------------------------------------------------

func recallRemoteCardsOff(out *CLIOutput, verb string) {
	out.Error(fmt.Sprintf("recall %s: card sync is off: set [recall] remote_cards = true in config.toml (docs/recall.md \"Remote\"); the federated query (recall search --remote) works without it", verb), ErrCodeInvalidOperation)
	os.Exit(2)
}

func handleRecallExport(profile string, args []string) {
	fs := newRecallFlagSet("recall export")
	jsonOutput := fs.Bool("json", false, "Accepted for parity; the stream is NDJSON either way (the trailer line carries the counts)")
	cardsOnly := fs.Bool("cards", true, "Cards only (the only export there is: bodies, offsets and paths never leave)")
	since := fs.String("since", "", "Only sessions active since (30d, YYYY-MM-DD, or a unix timestamp from a pull cursor)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `Usage: agent-deck recall export --cards [--since 30d] [--json]

Write this machine's session cards as NDJSON on stdout: one header line with
this machine's host_uid, then session, card, artifact and edge rows (titles,
hints, tags, 200-character previews, derived summaries), then a trailer. No
message body, byte offset, span or filesystem path is ever written. Needs
[recall] remote_cards = true; 'recall pull <host>' runs this on the remote.`)
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, false)
	if !*cardsOnly {
		out.Error("recall export: --cards=false is not a thing; only cards are ever exported", ErrCodeInvalidOperation)
		os.Exit(2)
	}
	env := openRecallEnv(profile, out)
	defer env.close()
	if !env.cfg.Recall.GetRemoteCards() {
		recallRemoteCardsOff(out, "export")
	}
	var sinceT time.Time
	if *since != "" {
		t, err := parseRecallSince(*since)
		if err != nil {
			var ts int64
			if _, serr := fmt.Sscan(*since, &ts); serr != nil || ts <= 0 {
				out.Error(err.Error(), ErrCodeInvalidOperation)
				os.Exit(2)
			}
			t = time.Unix(ts, 0)
		}
		sinceT = t
	}
	tr, err := cards.Export(os.Stdout, env.st, sinceT, time.Now())
	if err != nil {
		out.Error("recall export: "+err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if !*jsonOutput {
		fmt.Fprintf(os.Stderr, "exported %d session(s), %d card(s), %d artifact(s), %d edge(s)\n", tr.Sessions, tr.Cards, tr.Artifacts, tr.Edges)
	}
}

func handleRecallImport(profile string, args []string) {
	fs := newRecallFlagSet("recall import")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	host := fs.String("host", "", "The alias the cards belong to (required; the configured remote name)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `Usage: agent-deck recall import --host <alias> [file|-] [--json]

Import a 'recall export' stream (a file, or stdin with - or no argument) under
an explicit host alias. Refused: no --host; a stream without a host_uid; a
host_uid that disagrees with the one recorded for that alias; a host_uid
already imported under another alias; this machine's own cards. Imported
rows are cards only (digest_only) and every listing says so. Needs
[recall] remote_cards = true.`)
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, false)
	if strings.TrimSpace(*host) == "" {
		out.Error(cards.ErrNoHost.Error(), ErrCodeInvalidOperation)
		os.Exit(2)
	}
	var in io.Reader = os.Stdin
	if fs.NArg() == 1 && fs.Arg(0) != "-" {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			out.Error("recall import: "+err.Error(), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		defer f.Close()
		in = f
	} else if fs.NArg() > 1 {
		fs.Usage()
		os.Exit(2)
	}
	env := openRecallEnv(profile, out)
	defer env.close()
	if !env.cfg.Recall.GetRemoteCards() {
		recallRemoteCardsOff(out, "import")
	}
	release := env.lock(out)
	defer release()
	res, err := cards.Import(in, env.st, *host, time.Now())
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(2)
	}
	if *jsonOutput {
		out.printJSON(map[string]any{"success": true, "import": res})
		return
	}
	fmt.Printf("imported from %s (host %s): %d session(s), %d card(s), %d artifact(s), %d edge(s), %d line(s) skipped\n",
		res.Alias, res.HostUID, res.Sessions, res.Cards, res.Artifacts, res.Edges, res.Skipped)
}

func handleRecallPull(profile string, args []string) {
	fs := newRecallFlagSet("recall pull")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	full := fs.Bool("full", false, "Ignore the last pull cursor and fetch every card")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `Usage: agent-deck recall pull <host> [--full] [--json]

Run 'recall export --cards' on the configured remote and import the stream
here under that alias, from the last pull's cursor. Both ends need
[recall] remote_cards = true. Pulled sessions are cards only: search lists
them labelled, 'recall show' has no messages for them, and 'recall context'
stops at the brief tier.`)
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, false)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	name := fs.Arg(0)
	env := openRecallEnv(profile, out)
	defer env.close()
	if !env.cfg.Recall.GetRemoteCards() {
		recallRemoteCardsOff(out, "pull")
	}
	rc, ok := env.cfg.Remotes[name]
	if !ok {
		out.Error(fmt.Sprintf("remote %q not found", name), ErrCodeNotFound)
		os.Exit(2)
	}
	exportArgs := []string{"recall", "export", "--cards"}
	if !*full {
		if cursor, err := cards.Cursor(env.st, name); err == nil && cursor > 0 {
			exportArgs = append(exportArgs, "--since", fmt.Sprint(cursor))
		}
	}
	runner := newRemoteSearchRunner(name, rc)
	ctx := context.Background()
	stdout, err := runner.Run(ctx, exportArgs...)
	if err != nil {
		if reason, ok := remoteRecallUnsupported(exportArgs, 1, string(stdout), err.Error()); ok {
			version, _ := runner.CheckBinary(ctx)
			if *jsonOutput {
				_, _ = os.Stdout.Write(remoteRecallUnsupportedJSON(name, version, reason))
				os.Exit(1)
			}
			out.Error(remoteRecallUnsupportedMessage(name, version, reason), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		if strings.Contains(string(stdout)+err.Error(), "card sync is off") {
			out.Error(fmt.Sprintf("remote %q has [recall] remote_cards = false; nothing was pulled", name), ErrCodeInvalidOperation)
			os.Exit(2)
		}
		out.Error(fmt.Sprintf("recall pull %s: %v", name, err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	release := env.lock(out)
	defer release()
	res, err := cards.Import(bytes.NewReader(stdout), env.st, name, time.Now())
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(2)
	}
	if *jsonOutput {
		out.printJSON(map[string]any{"success": true, "import": res})
		return
	}
	fmt.Printf("pulled from %s (host %s): %d session(s), %d card(s), %d artifact(s), %d edge(s)\n",
		res.Alias, res.HostUID, res.Sessions, res.Cards, res.Artifacts, res.Edges)
}
