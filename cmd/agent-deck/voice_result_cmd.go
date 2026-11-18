package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/voicestream"
)

// `agent-deck voice result-publish` puts the desk's result on the bus as a
// voice.result frame so every screen shows the same list, confirm card or
// "switching to …". It reads the `voice do --json` object on stdin; the
// spoken line ("say") and the confirm token never reach the frame.

func init() {
	voiceSubs = append(voiceSubs, voiceSub{
		name:    "result-publish",
		summary: "Publish the desk's result (voice do --json on stdin) as a voice.result frame",
		usage:   "  agent-deck voice result-publish [--from mac|glasses|phone|text] < result.json   (say and the confirm token are dropped)",
		run:     runVoiceResultPublish,
	})
}

// voiceResultCap bounds the stdin object: a desk result is a few KB.
const voiceResultCap = 256 << 10

func runVoiceResultPublish(profile string, args []string, stdout, stderr io.Writer) int {
	return voiceResultPublish(profile, args, os.Stdin, stdout, stderr)
}

func voiceResultPublish(profile string, args []string, in io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("voice result-publish", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "text", "the client the sentence came from: mac, glasses, phone or text")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	switch *from {
	case "mac", "glasses", "phone", "text":
	default:
		fmt.Fprintf(stderr, "voice result-publish: --from %q: want mac, glasses, phone or text\n", *from)
		return 2
	}
	raw, err := io.ReadAll(io.LimitReader(in, voiceResultCap+1))
	if err != nil {
		fmt.Fprintln(stderr, "voice result-publish: reading stdin:", err)
		return 1
	}
	if len(raw) > voiceResultCap || len(strings.TrimSpace(string(raw))) == 0 {
		fmt.Fprintln(stderr, "voice result-publish: stdin must hold one desk result object (at most 256 KiB)")
		return 2
	}
	res, err := voicestream.ResultFromDesk(raw, *from)
	if errors.Is(err, voicestream.ErrNotPublishable) {
		fmt.Fprintln(stdout, `{"published":false}`)
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "voice result-publish:", err)
		return 2
	}
	// main's deferred events.CloseDefault drains the frame to disk before
	// the process exits.
	voicestream.PublishResult(session.GetEffectiveProfile(profile), res)
	out, _ := json.Marshal(map[string]any{"published": true, "kind": voicestream.KindResult})
	fmt.Fprintln(stdout, string(out))
	return 0
}
