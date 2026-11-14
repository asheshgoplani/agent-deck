package session

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// The marker follows a parent's harness at the poll after a restart that
// changed it, but the hooks of the new harness can run before that poll. The
// receipt semantics are the calling hook's, so the window costs neither side a
// record: a pi run start under a Claude marker is bound to a token its receipt
// must echo, and a Claude prompt under a pi marker counts as shown once printed.

// promptShows reports whether a prompt at the fixture's clock carries text,
// and settles it as printed.
func promptShows(t *testing.T, f *deliverFixture, fromPi bool, text string) (*LedgerDelivery, bool) {
	t.Helper()
	dl, ok := LedgerPromptContext(f.parent.ID, "go on", "", fromPi)
	if !ok {
		t.Fatal("enrolled parent fell back to the inbox path")
	}
	shown := strings.Contains(dl.Text, text)
	dl.Done(true)
	return dl, shown
}

func TestLedgerPromptContext_APiRunStartUnderAClaudeMarkerWaitsForItsReceipt(t *testing.T) {
	f := newDeliverFixture(t) // a Claude parent: marker tool claude
	if en, _ := LoadCommsEnrollment(f.parent.ID); en.Tool == "pi" {
		t.Fatalf("test setup: marker %+v", en)
	}
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "XYZ result"})
	f.deliver(t, "waiting")

	// The parent was restarted as pi; its first run start comes before the poll
	// that turns the marker pi.
	dl, shown := promptShows(t, f, true, "XYZ result")
	if !shown {
		t.Fatalf("the run start did not carry the record: %q", dl.Text)
	}
	if dl.Token == "" {
		t.Fatal("a pi run start under a Claude marker got no receipt token: its printed output, which pi may discard, acknowledged the record")
	}
	// No receipt came back (the extension discarded the output). Once the
	// receipt window has passed, the record is owed again.
	f.clock = f.clock.Add(commsReceiptWindow + 1)
	if _, again := promptShows(t, f, true, "XYZ result"); !again {
		t.Error("a pi injection nobody acknowledged was dropped: printing alone settled it")
	}
}

func TestLedgerPromptContext_AClaudePromptUnderAPiMarkerIsShownOncePrinted(t *testing.T) {
	f := newPiParentFixture(t) // marker tool pi, socket exported
	if en, _ := LoadCommsEnrollment(f.parent.ID); en.Tool != "pi" {
		t.Fatalf("test setup: marker %+v", en)
	}
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "XYZ result"})
	f.deliver(t, "waiting")

	// The parent was restarted as Claude; its first prompt comes before the poll
	// that turns the marker claude. Claude never echoes a token.
	dl, shown := promptShows(t, f, false, "XYZ result")
	if !shown {
		t.Fatalf("the prompt did not carry the record: %q", dl.Text)
	}
	if dl.Token != "" {
		t.Errorf("a Claude prompt was given a receipt token %q it can never echo", dl.Token)
	}
	f.clock = f.clock.Add(commsReceiptWindow + 1)
	if _, again := promptShows(t, f, false, "XYZ result"); again {
		t.Error("a record the Claude prompt printed was shown again: it waited for a receipt Claude never sends")
	}
}
