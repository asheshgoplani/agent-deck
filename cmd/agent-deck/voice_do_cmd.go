package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
	"github.com/asheshgoplani/agent-deck/internal/needsme"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/voicedesk"
)

const voiceDoUsage = `  agent-deck voice do [--json] [--dry-run] [--from mac|glasses|phone|text] [--on <session id>]
                     [--pending <token>] [--answer-delay-ms <n>] "<sentence>"
                   one heard sentence -> deck action + the line to speak (exit 0 = a result was printed,
                   2 = usage, 1 = internal error). Without --json only the line to speak is printed`

func init() {
	voiceSubs = append(voiceSubs, voiceSub{
		name:    "do",
		summary: "Voice desk: a heard sentence -> deck action + line to speak [--json]",
		usage:   voiceDoUsage,
		run:     runVoiceDo,
	})
}

// voiceDoTimeout bounds one desk call; the voice daemon waits 8 s.
const voiceDoTimeout = 7 * time.Second

type voiceDoArgs struct {
	jsonOut, dryRun bool
	from, on        string
	pending         string
	delayMs         int64
	hasDelay        bool
	sentence        string
}

func parseVoiceDoArgs(args []string) (voiceDoArgs, error) {
	a := voiceDoArgs{from: "text"}
	var words []string
	value := func(i *int, name string) (string, error) {
		arg := args[*i]
		if k, v, ok := strings.Cut(arg, "="); ok && strings.HasPrefix(k, "-") {
			return v, nil
		}
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value", name)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, _, _ := strings.Cut(arg, "=")
		switch strings.TrimLeft(name, "-") {
		case "json":
			if strings.HasPrefix(arg, "-") {
				a.jsonOut = true
				continue
			}
		case "dry-run":
			if strings.HasPrefix(arg, "-") {
				a.dryRun = true
				continue
			}
		case "from", "on", "pending", "answer-delay-ms":
			if !strings.HasPrefix(arg, "-") {
				break
			}
			v, err := value(&i, name)
			if err != nil {
				return a, err
			}
			switch strings.TrimLeft(name, "-") {
			case "from":
				switch v {
				case "mac", "glasses", "phone", "text":
					a.from = v
				default:
					return a, fmt.Errorf("--from must be mac, glasses, phone or text")
				}
			case "on":
				a.on = strings.TrimSpace(v)
			case "pending":
				a.pending = strings.TrimSpace(v)
			case "answer-delay-ms":
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil || n < 0 {
					return a, fmt.Errorf("--answer-delay-ms must be a whole number of milliseconds")
				}
				a.delayMs, a.hasDelay = n, true
			}
			continue
		case "":
			if arg == "--" {
				words = append(words, args[i+1:]...)
				i = len(args)
				continue
			}
		}
		if strings.HasPrefix(arg, "-") && len(words) == 0 && arg != "-" && !(len(arg) > 1 && arg[1] >= '0' && arg[1] <= '9') {
			return a, fmt.Errorf("unknown flag %s", arg)
		}
		words = append(words, arg)
	}
	a.sentence = strings.TrimSpace(strings.Join(words, " "))
	if a.sentence == "" {
		return a, errors.New("a sentence is required")
	}
	return a, nil
}

func runVoiceDo(profile string, args []string, stdout, stderr io.Writer) int {
	// help only as the first argument: "help" inside a sentence is a sentence
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || (len(args) == 1 && args[0] == "help")) {
		fmt.Fprintln(stdout, "Usage:")
		fmt.Fprintln(stdout, voiceDoUsage)
		return 0
	}
	a, err := parseVoiceDoArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "voice do: %v\n\nUsage:\n%s\n", err, voiceDoUsage)
		return 2
	}
	// Not mine: decided from the words alone, before any config, state or
	// session listing, so the caller types the sentence on at once.
	if a.pending == "" && voicedesk.ParseFrom(a.sentence, a.from).Intent == voicedesk.IntentNone {
		code := writeVoiceDo(stdout, stderr, voicedesk.Result{Handled: false, Intent: voicedesk.IntentNone,
			Lang: voicedesk.Parse(a.sentence).Lang, Show: voicedesk.Show{Kind: "none", Items: []voicedesk.ShowItem{}}, DryRun: a.dryRun}, a.jsonOut)
		sweepVoicePending(profile) // after the answer is out: removes only questions older than 60 s
		return code
	}
	resolved, err := session.ResolveProfileForStorageQuiet(profile)
	if err != nil {
		fmt.Fprintf(stderr, "voice do: resolving the profile: %v\n", err)
		return 1
	}
	profDir, err := session.GetProfileDir(resolved)
	if err != nil {
		fmt.Fprintf(stderr, "voice do: profile dir: %v\n", err)
		return 1
	}
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	desk0 := voicedesk.FileStore{Dir: filepath.Join(profDir, "voice", "pending")}
	desk0.Sweep() // an unanswered question never outlives 60 s by much
	// The owner rules come from config.toml. A config that cannot be read
	// never falls back to the open default ("everything is his"): the desk
	// refuses to act at all.
	uc, cfgErr := session.LoadUserConfig()
	if cfgErr != nil || uc == nil {
		p := voicedesk.Parse(a.sentence)
		why := voicedesk.RefusedUnsafe
		intent := p.Intent
		if intent == voicedesk.IntentNone {
			intent = voicedesk.IntentConfirmYes // an answer to a pending question
		}
		return writeVoiceDo(stdout, stderr, voicedesk.Result{Handled: true, Intent: intent, Lang: p.Lang,
			Say: "I cannot read the voice settings.", Show: voicedesk.Show{Kind: "none", Items: []voicedesk.ShowItem{}},
			Refused: &why, DryRun: a.dryRun}, a.jsonOut)
	}
	cfg := voiceDeskConfig(uc.Voice)
	// --on absent means on no session (the daemon's own id can be null):
	// every tell then needs the yes. The voice state is never guessed from.
	on := a.on
	desk := &voicedesk.Desk{
		Deck:   &cliVoiceDeck{self: self, profile: resolved, from: a.from},
		Store:  desk0,
		Config: cfg,
	}
	ctx, cancel := context.WithTimeout(context.Background(), voiceDoTimeout)
	defer cancel()
	res, err := desk.Do(ctx, voicedesk.Request{
		Sentence: a.sentence, Profile: resolved, From: a.from, On: on, Pending: a.pending,
		AnswerDelayMs: a.delayMs, HasAnswerDelay: a.hasDelay, DryRun: a.dryRun,
	})
	if err != nil {
		fmt.Fprintf(stderr, "voice do: %v\n", err) // never the sentence
		return 1
	}
	return writeVoiceDo(stdout, stderr, res, a.jsonOut)
}

func writeVoiceDo(stdout, stderr io.Writer, res voicedesk.Result, asJSON bool) int {
	if !asJSON {
		if res.Handled && res.Say != "" {
			fmt.Fprintln(stdout, res.Say)
		}
		return 0
	}
	b, err := json.Marshal(res)
	if err != nil {
		fmt.Fprintf(stderr, "voice do: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, string(b))
	return 0
}

func voiceDeskConfig(v session.VoiceSettings) voicedesk.Config {
	c := voicedesk.Config{OwnerGroups: v.OwnerGroups, OwnerProfiles: v.OwnerProfiles, People: map[string]voicedesk.Person{}}
	for name, p := range v.People {
		c.People[name] = voicedesk.Person{Groups: p.Groups, Profiles: p.Profiles, Names: p.Names}
	}
	return c
}

// cliVoiceDeck is the desk's Deck on top of agent-deck itself: the needs-me
// sources for listing (the current profile + every remote), and this same
// binary's `session output`, `session send` and `remote <name> ...` for
// acting, so every send goes through the path a typed command takes.
type cliVoiceDeck struct {
	self    string
	profile string
	from    string
}

// sources: the current profile only (plus the remotes). Voice never lists
// or targets a session of another local profile.
func (d *cliVoiceDeck) sources() []needsme.Source { return needsme.ProfileSources(d.self, d.profile) }

func (d *cliVoiceDeck) Sessions(ctx context.Context) ([]voicedesk.Session, []voicedesk.HostError) {
	srcs := d.sources()
	type got struct {
		src  needsme.Source
		rows []needsme.Row
		err  error
	}
	ch := make(chan got, len(srcs))
	for _, s := range srcs {
		go func(s needsme.Source) {
			c, cancel := context.WithTimeout(ctx, needsme.DefaultTimeout)
			defer cancel()
			rows, err := s.Fetch(c)
			ch <- got{s, rows, err}
		}(s)
	}
	var out []voicedesk.Session
	var errs []voicedesk.HostError
	for range srcs {
		g := <-ch
		if g.err != nil {
			errs = append(errs, voicedesk.HostError{Host: g.src.Host, Profile: g.src.Profile, Error: "unreachable"})
			continue
		}
		for _, r := range g.rows {
			s := voicedesk.Session{ID: r.ID, Title: r.Title, Group: r.Group, Profile: r.Profile, Host: g.src.Host,
				Status: r.Status, Substate: r.Substate, Archived: r.Archived, Tool: r.Tool, Conductor: r.ConductorName != ""}
			if r.Profile == "" {
				s.Profile = g.src.Profile
			}
			if t, err := time.Parse(time.RFC3339, r.LastActivityAt); err == nil {
				s.LastActivity = t
			}
			out = append(out, s)
		}
	}
	return out, errs
}

func (d *cliVoiceDeck) NeedsMe(ctx context.Context) ([]voicedesk.Session, []voicedesk.HostError) {
	res := needsme.Collect(ctx, needsme.Options{Sources: d.sources(), Timeout: needsme.DefaultTimeout, Now: time.Now})
	out := make([]voicedesk.Session, 0, len(res.Sessions))
	for _, s := range res.Sessions {
		out = append(out, voicedesk.Session{ID: s.ID, Title: s.Title, Group: s.Group, Profile: s.Profile, Host: s.Host,
			Status: s.Status, Substate: s.Substate, Reason: s.Reason, WaitingSeconds: s.WaitingSeconds,
			Conductor: s.ConductorName != ""})
	}
	errs := make([]voicedesk.HostError, 0, len(res.Errors))
	for _, e := range res.Errors {
		errs = append(errs, voicedesk.HostError{Host: e.Host, Profile: e.Profile, Error: e.Error})
	}
	return out, errs
}

// run runs this binary; stdin is the message (never an argument, so it is
// not visible in a process list).
func (d *cliVoiceDeck) run(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, d.self, args...) //nolint:gosec // G204: re-exec of this binary (os.Executable) with fixed verbs; the message goes on stdin, no shell
	cmd.Env = childenv.ForSelfReexec("AGENTDECK_INSTANCE_ID")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("agent-deck %s failed: %w", args[min(2, len(args)-1)], err)
	}
	return out.Bytes(), nil
}

func (d *cliVoiceDeck) profileOf(s voicedesk.Session) string {
	if s.Profile != "" {
		return s.Profile
	}
	return d.profile
}

func (d *cliVoiceDeck) LastReply(ctx context.Context, s voicedesk.Session) (string, error) {
	var out []byte
	var err error
	if s.Host != "" {
		out, err = d.run(ctx, "", "remote", s.Host, "output", s.ID, "--json")
	} else {
		out, err = d.run(ctx, "", "-p", d.profileOf(s), "session", "output", s.ID, "--json")
	}
	if err != nil {
		return "", err
	}
	var v struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &v); err != nil {
		return "", fmt.Errorf("reading session output: %w", err)
	}
	return v.Content, nil
}

// Send types the message through `session send` with the product's own
// guard (--require-input-prompt: nothing is typed unless an input prompt is
// visible, so a menu is never answered by voice). Its own time budget is
// what is left of the call. A send whose outcome is not known (time ran
// out after it started, or the send could not confirm) is ErrUnconfirmed,
// never a failure: he must not repeat a message that landed.
func (d *cliVoiceDeck) Send(ctx context.Context, s voicedesk.Session, message string) error {
	var args []string
	if s.Host != "" {
		args = []string{"remote", s.Host, "send", s.ID, "--message-file", "-", "--no-wait", "-q"}
	} else {
		args = []string{"-p", d.profileOf(s), "session", "send", s.ID, "--message-file", "-", "--no-wait", "--no-tag", "-q",
			"--source", "voice:" + d.from}
	}
	// an agent tool shows an input prompt; a plain shell never does (its
	// open menus are caught by the substate check before the send)
	if s.Tool != "shell" {
		args = append(args, "--require-input-prompt")
	}
	cmd := exec.CommandContext(ctx, d.self, args...) //nolint:gosec // G204: re-exec of this binary with fixed verbs; the message goes on stdin
	cmd.Env = childenv.ForSelfReexec("AGENTDECK_INSTANCE_ID")
	cmd.Stdin = strings.NewReader(message)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	text := strings.ToLower(errb.String() + out.String())
	switch {
	case err == nil && (strings.Contains(text, "unconfirmed") || strings.Contains(text, "not confirmed") || strings.Contains(text, "unverified")):
		return voicedesk.ErrUnconfirmed
	case err == nil:
		return nil
	case strings.Contains(text, "menu_open") || strings.Contains(text, "menu is open"):
		return voicedesk.ErrMenuOpen
	case strings.Contains(text, "no keys typed") || strings.Contains(text, "input prompt unavailable"):
		return voicedesk.ErrNoPrompt
	case strings.Contains(text, "unconfirmed") || strings.Contains(text, "not confirmed") || strings.Contains(text, "unverified"):
		return voicedesk.ErrUnconfirmed // typed, the submit was not proven
	case ctx.Err() != nil:
		return voicedesk.ErrUnconfirmed // killed mid-way: it may have landed
	}
	// any other refusal (unknown flag on an older remote, not found, not
	// running): the send stopped before typing
	return voicedesk.ErrNotSent
}

// Lookup reads the one session again from its own host only (one local
// profile's list, or one remote's), for the check right before a yes acts.
func (d *cliVoiceDeck) Lookup(ctx context.Context, s voicedesk.Session) (voicedesk.Session, bool, error) {
	profile := d.profileOf(s)
	for _, src := range d.sources() {
		if src.Host != s.Host || (s.Host == "" && src.Profile != profile) {
			continue
		}
		c, cancel := context.WithTimeout(ctx, needsme.DefaultTimeout)
		rows, err := src.Fetch(c)
		cancel()
		if err != nil {
			return voicedesk.Session{}, false, err
		}
		for _, r := range rows {
			if r.ID != s.ID {
				continue
			}
			x := voicedesk.Session{ID: r.ID, Title: r.Title, Group: r.Group, Profile: r.Profile, Host: s.Host,
				Status: r.Status, Substate: r.Substate, Archived: r.Archived, Tool: r.Tool, Conductor: r.ConductorName != ""}
			if x.Profile == "" {
				x.Profile = src.Profile
			}
			return x, true, nil
		}
		return voicedesk.Session{}, false, nil
	}
	return voicedesk.Session{}, false, nil
}

// Show asks the Mac app to show a local session (the switch command's
// frame on the events bus; the frame waits for an app that connects later).
// A remote session is shown by the caller's own switch (then.action).
func (d *cliVoiceDeck) Show(ctx context.Context, s voicedesk.Session) error {
	if s.Host != "" {
		return nil
	}
	profile := d.profileOf(s)
	dl := switchDelivery{
		localMac: localMacappLaunchAllowed(runtime.GOOS, os.Getenv),
		openApp:  openMacappFocus,
		publish:  func(id string, wait time.Duration) (bool, error) { return publishSwitchRequest(profile, id, wait) },
	}
	_, _, err := deliverSwitch(dl, s.ID, 300*time.Millisecond)
	return err
}

// sweepVoicePending removes unanswered questions older than 60 s, so a heard
// message never stays on disk long. It only deletes; it creates nothing.
func sweepVoicePending(profile string) {
	resolved, err := session.ResolveProfileForStorageQuiet(profile)
	if err != nil {
		return
	}
	dir, err := session.GetProfileDir(resolved)
	if err != nil {
		return
	}
	pending := filepath.Join(dir, "voice", "pending")
	if _, err := os.Stat(pending); err != nil {
		return
	}
	voicedesk.FileStore{Dir: pending}.Sweep()
}

// Act runs stop / restart / archive through `session <verb>` (local) or
// `remote <name> session <verb>` (forwarded by the remote allowlist).
func (d *cliVoiceDeck) Act(ctx context.Context, s voicedesk.Session, verb string) error {
	switch verb {
	case "stop", "restart", "archive":
	default:
		return fmt.Errorf("voice never runs %q", verb)
	}
	var out []byte
	var err error
	if s.Host != "" {
		out, err = d.run(ctx, "", "remote", s.Host, "session", verb, s.ID, "--json")
	} else {
		out, err = d.run(ctx, "", "-p", d.profileOf(s), "session", verb, s.ID, "--json")
	}
	if err != nil {
		return err
	}
	var v struct {
		Success *bool `json:"success"`
	}
	if json.Unmarshal(bytes.TrimSpace(out), &v) == nil && v.Success != nil && !*v.Success {
		return fmt.Errorf("session %s did not succeed", verb)
	}
	return nil
}

// Launch starts a session with `launch -t <title> -g <group> --json`; the
// path and tool come from the group's defaults as for a typed launch.
func (d *cliVoiceDeck) Launch(ctx context.Context, spec voicedesk.LaunchSpec) (voicedesk.Session, error) {
	args := []string{"launch", "-t", spec.Title, "--json"}
	if spec.Group != "" {
		args = append(args, "-g", spec.Group)
	}
	if spec.Host != "" {
		args = append([]string{"remote", spec.Host}, args...)
	} else {
		profile := spec.Profile
		if profile == "" {
			profile = d.profile
		}
		args = append([]string{"-p", profile}, args...)
	}
	out, err := d.run(ctx, "", args...)
	var v struct {
		Success bool   `json:"success"`
		ID      string `json:"id"`
		Title   string `json:"title"`
		Group   string `json:"group"`
		Error   string `json:"error"`
	}
	if jerr := json.Unmarshal(bytes.TrimSpace(out), &v); jerr != nil || !v.Success {
		if err == nil {
			err = errors.New("launch did not succeed")
		}
		return voicedesk.Session{}, err
	}
	return voicedesk.Session{ID: v.ID, Title: v.Title, Group: v.Group, Host: spec.Host, Profile: spec.Profile}, nil
}

// Search is `recall search --json --limit 5` (plus --all-remotes when
// remotes are configured; a remote that does not answer leaves the local
// hits).
func (d *cliVoiceDeck) Search(ctx context.Context, query string) ([]voicedesk.SearchHit, error) {
	args := []string{"-p", d.profile, "recall", "search", "--json", "--limit", "5"}
	if uc, err := session.LoadUserConfig(); err == nil && uc != nil && len(uc.Remotes) > 0 {
		args = append(args, "--all-remotes")
	}
	args = append(args, "--", query)
	out, err := d.run(ctx, "", args...)
	var v struct {
		Success *bool  `json:"success"`
		Error   string `json:"error"`
		Hits    []struct {
			Title   string `json:"title"`
			DeckID  string `json:"deck_id"`
			Remote  string `json:"remote"`
			Snippet string `json:"snippet"`
		} `json:"hits"`
	}
	if jerr := json.Unmarshal(bytes.TrimSpace(out), &v); jerr != nil {
		if err == nil {
			err = jerr
		}
		return nil, err
	}
	if v.Success != nil && !*v.Success {
		if strings.Contains(v.Error, "recall is off") {
			return nil, voicedesk.ErrSearchOff
		}
		return nil, errors.New("recall search failed")
	}
	hits := make([]voicedesk.SearchHit, 0, len(v.Hits))
	for _, h := range v.Hits {
		hits = append(hits, voicedesk.SearchHit{Title: h.Title, DeckID: h.DeckID, Host: h.Remote, Snippet: h.Snippet})
	}
	return hits, nil
}

// Children is `session children <id> --json` (this machine only: the
// remote allowlist does not forward children).
func (d *cliVoiceDeck) Children(ctx context.Context, s voicedesk.Session) ([]voicedesk.Session, error) {
	if s.Host != "" {
		return nil, voicedesk.ErrUnsupported
	}
	out, err := d.run(ctx, "", "-p", d.profileOf(s), "session", "children", s.ID, "--json")
	if err != nil {
		return nil, err
	}
	var v struct {
		Children []struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"children"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &v); err != nil {
		return nil, err
	}
	kids := make([]voicedesk.Session, 0, len(v.Children))
	for _, c := range v.Children {
		kids = append(kids, voicedesk.Session{ID: c.ID, Title: c.Title, Status: c.Status, Profile: d.profileOf(s)})
	}
	return kids, nil
}
