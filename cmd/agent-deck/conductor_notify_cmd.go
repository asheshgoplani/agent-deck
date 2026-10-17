package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Conductor -> human tier CLIs (issue #2469): `conductor notify` queues an
// item for the human, `conductor outbox` lists and acks the queue (the bridge
// polls it), `conductor tier-filter` applies the tier and retire rules to a
// reply and reports whether the info digest is due.

func handleConductorNotify(profile string, args []string) {
	exitOnCLIError(runConductorNotify(os.Stdout, os.Stdin, args, profile))
}

func handleConductorOutbox(profile string, args []string) {
	exitOnCLIError(runConductorOutbox(os.Stdout, args, profile))
}

func handleConductorTierFilter(profile string, args []string) {
	exitOnCLIError(runConductorTierFilter(os.Stdout, os.Stdin, args, profile))
}

func exitOnCLIError(err error) {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return
	}
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	exitCLI(1)
}

// conductorTitleLookup returns the title of the session with this id, or ""
// (a test seam). It looks in the effective profile first, then every other.
var conductorTitleLookup = func(id, profile string) string {
	profiles, _ := session.ListProfiles()
	if p, err := session.ResolveProfileForStorage(profile); err == nil {
		profiles = append([]string{p}, profiles...)
	}
	for _, p := range profiles {
		_, instances, _, err := loadSessionData(p)
		if err != nil {
			continue
		}
		for _, inst := range instances {
			if inst.ID == id {
				return inst.Title
			}
		}
	}
	return ""
}

// conductorNameFromTitle maps a conductor session title (conductor-<name>)
// to the conductor name.
func conductorNameFromTitle(title string) (string, bool) {
	name, ok := strings.CutPrefix(strings.TrimSpace(title), session.ConductorSessionTitlePrefix)
	return name, ok && name != ""
}

// resolveHumanConductor picks the conductor a human-tier command acts on:
// --conductor when given, else the conductor whose session runs this command
// (AGENTDECK_INSTANCE_ID's title, falling back to AGENTDECK_TITLE).
func resolveHumanConductor(explicit, profile string) (string, error) {
	name := strings.TrimSpace(explicit)
	if name == "" {
		title := ""
		if id := strings.TrimSpace(os.Getenv("AGENTDECK_INSTANCE_ID")); id != "" {
			title = conductorTitleLookup(id, profile)
		}
		if title == "" {
			title = os.Getenv("AGENTDECK_TITLE")
		}
		var ok bool
		if name, ok = conductorNameFromTitle(title); !ok {
			return "", errors.New("no --conductor given and this is not a conductor session (title conductor-<name>)")
		}
	}
	if err := session.ValidateConductorName(name); err != nil {
		return "", err
	}
	return name, nil
}

func runConductorNotify(stdout io.Writer, stdin io.Reader, args []string, profile string) error {
	fs := flag.NewFlagSet("conductor notify", flag.ContinueOnError)
	tier := fs.String("tier", "", "urgent (forwarded to the human now) or info (batched into a digest)")
	conductor := fs.String("conductor", "", "conductor name (default: the conductor running this command)")
	messageFile := fs.String("message-file", "", "read the text from a file (- for stdin)")
	asJSON := fs.Bool("json", false, "print the stored record as JSON")
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: agent-deck conductor notify --tier urgent|info [--conductor <name>] [--json] \"<text>\"")
		fmt.Fprintln(stdout, "Queue a message for the human. The bridge forwards urgent at once and info as a digest.")
		fs.SetOutput(stdout)
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	text, err := resolveMessageInput(strings.Join(fs.Args(), " "), *messageFile, stdin)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		fs.Usage()
		return errors.New("notify needs a message")
	}
	name, err := resolveHumanConductor(*conductor, profile)
	if err != nil {
		return err
	}
	rec, created, err := session.AppendHumanOutbox(name, *tier, text)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(struct {
			session.HumanOutboxRecord
			Conductor string `json:"conductor"`
			Created   bool   `json:"created"`
		}{rec, name, created})
	}
	if created {
		fmt.Fprintf(stdout, "queued %s %s for %s\n", rec.Tier, rec.ID, name)
	} else {
		fmt.Fprintf(stdout, "already queued %s (same text within 24h)\n", rec.ID)
	}
	return nil
}

func runConductorOutbox(stdout io.Writer, args []string, profile string) error {
	fs := flag.NewFlagSet("conductor outbox", flag.ContinueOnError)
	conductor := fs.String("conductor", "", "conductor name (default: the conductor running this command)")
	asJSON := fs.Bool("json", false, "emit JSON")
	all := fs.Bool("all", false, "include delivered (acked) items")
	var ack stringValues
	fs.Var(&ack, "ack", "mark this id delivered (repeatable; further ids may follow as arguments)")
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: agent-deck conductor outbox [--json] [--conductor <name>] [--all] [--ack <id>...]")
		fmt.Fprintln(stdout, "List the items queued for the human (unacked by default), or mark them delivered.")
		fs.SetOutput(stdout)
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	if len(ack) == 0 && fs.NArg() > 0 {
		fs.Usage()
		return errors.New("outbox takes no positional arguments without --ack")
	}
	name, err := resolveHumanConductor(*conductor, profile)
	if err != nil {
		return err
	}
	if len(ack) > 0 {
		n, err := session.AckHumanOutbox(name, append([]string(ack), fs.Args()...))
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(stdout).Encode(map[string]any{"conductor": name, "acked": n})
		}
		fmt.Fprintf(stdout, "acked %d item(s) for %s\n", n, name)
		return nil
	}
	items, err := session.ListHumanOutbox(name, !*all)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(items)
	}
	if len(items) == 0 {
		fmt.Fprintf(stdout, "No items for %s.\n", name)
		return nil
	}
	for _, r := range items {
		state := "pending"
		if r.Acked {
			state = "delivered"
		}
		fmt.Fprintf(stdout, "%s  %-6s %-9s %s  %s\n", r.ID, r.Tier, state, r.TS.Local().Format("2006-01-02 15:04"), r.Text)
	}
	return nil
}

// tierFilterResult is the `conductor tier-filter --json` output.
type tierFilterResult struct {
	Conductor string                      `json:"conductor"`
	SendNow   []string                    `json:"send_now"`
	Queued    int                         `json:"queued"`
	DigestDue bool                        `json:"digest_due"`
	Digest    []session.HumanOutboxRecord `json:"digest"`
}

func runConductorTierFilter(stdout io.Writer, stdin io.Reader, args []string, profile string) error {
	fs := flag.NewFlagSet("conductor tier-filter", flag.ContinueOnError)
	conductor := fs.String("conductor", "", "conductor name (default: the conductor running this command)")
	asJSON := fs.Bool("json", false, "emit JSON")
	replyID := fs.String("reply-id", "", "id of this reply; its retire counts stay pending until --ack <id> confirms delivery")
	ackID := fs.String("ack", "", "confirm the reply filtered under this --reply-id reached the human (commits its retire counts; reads no stdin)")
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: agent-deck conductor tier-filter [--json] [--conductor <name>] [--reply-id <id>] < reply.txt")
		fmt.Fprintln(stdout, "       agent-deck conductor tier-filter [--json] [--conductor <name>] --ack <reply-id>")
		fmt.Fprintln(stdout, "Apply the human tier rules to a conductor reply on stdin: urgent lines to send now")
		fmt.Fprintln(stdout, "(retired after [conductor] need_retire_cycles), info lines queued, digest when due.")
		fmt.Fprintln(stdout, "With --reply-id the retire count advances only once --ack confirms the send.")
		fs.SetOutput(stdout)
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	name, err := resolveHumanConductor(*conductor, profile)
	if err != nil {
		return err
	}
	if ack := strings.TrimSpace(*ackID); ack != "" {
		return runConductorTierFilterAck(stdout, name, ack, *asJSON)
	}
	reply, err := io.ReadAll(io.LimitReader(stdin, 4<<20))
	if err != nil {
		return fmt.Errorf("read reply: %w", err)
	}
	now := time.Now()
	sendNow, queued, err := session.TierFilterReply(name, *replyID, string(reply), now)
	if err != nil {
		return err
	}
	settings := session.GetConductorSettings()
	due, items, err := session.HumanDigest(name, now, settings.GetHumanDigestMinutes(), len(sendNow) > 0)
	if err != nil {
		return err
	}
	// Empty slices, not nil, so the JSON always carries [] rather than null.
	res := tierFilterResult{
		Conductor: name,
		SendNow:   append([]string{}, sendNow...),
		Queued:    queued,
		DigestDue: due,
		Digest:    []session.HumanOutboxRecord{},
	}
	if due {
		res.Digest = append(res.Digest, items...)
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(res)
	}
	for _, line := range res.SendNow {
		fmt.Fprintln(stdout, line)
	}
	fmt.Fprintf(stdout, "send_now=%d queued=%d digest_due=%t digest=%d\n", len(res.SendNow), res.Queued, res.DigestDue, len(res.Digest))
	return nil
}

// runConductorTierFilterAck commits the retire counts of a delivered reply.
// Acking an id that is not the pending one (already acked, or superseded by a
// later reply) succeeds and reports committed=false.
func runConductorTierFilterAck(stdout io.Writer, name, replyID string, asJSON bool) error {
	committed, err := session.AckTierFilterReply(name, replyID, time.Now())
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(map[string]any{"conductor": name, "reply_id": replyID, "committed": committed})
	}
	fmt.Fprintf(stdout, "reply %s committed=%t for %s\n", replyID, committed, name)
	return nil
}
