package main

import (
	"strings"
	"testing"
)

// Second real case (12 Nov): a --message-file send whose body named PNG files
// was refused by the truncation guard ("composer shows a paste with 5 line
// breaks ... but the message has 12"), because Claude turned the file paths
// into image attachments and collapsed the rest. The composer was left
// holding placeholders, not the literal text:
//
//	[Image #8] [Image #9] [Image #10][Pasted text #11 +5 lines]
//
// agent-deck cannot attribute that line to its own text without guessing
// (the placeholders are not a prefix of the message, the line is not the one
// paste chip of this send, and whether Backspace drops an attachment or only
// its label is not knowable from the pane), so it must delete nothing, say
// its own payload is still there (case c, composer_residue "left",
// unverified), and the next send must name that leftover instead of the
// generic "occupied" refusal, never clearing it.

const attachmentsComposerLine = "[Image #8] [Image #9] [Image #10][Pasted text #11 +5 lines]"

// attachmentsMessage has 12 hard line breaks, three PNG paths and backticks.
const attachmentsMessage = "Review the screenshots below and report what is broken.\n" +
	"\n" +
	"/tmp/shots/login-before.png\n" +
	"/tmp/shots/login-after.png\n" +
	"/tmp/shots/settings.png\n" +
	"\n" +
	"Checks:\n" +
	"1. does `Sign in` render at full width\n" +
	"2. is the `Remember me` box aligned\n" +
	"3. any clipped text in settings\n" +
	"\n" +
	"Reply with one line per screenshot.\n" +
	"Thanks"

func TestOrphanAttachments_PlaceholderLeftoverIsNeverDeletedAndReportedLeft(t *testing.T) {
	if got := strings.Count(attachmentsMessage, "\n"); got != 12 {
		t.Fatalf("setup: message has %d line breaks, want 12", got)
	}
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			store := newMemResidueStore()
			pane := &orphanPaneBS{orphanPane: &orphanPane{replaceWith: attachmentsComposerLine}}
			res, err := executeSend(pane, "claude", attachmentsMessage, mode.noWait, residueTuning(mode.noWait, store))
			if err == nil {
				t.Fatalf("send reported success; delivery=%q", res.delivery)
			}
			if res.delivery != "typed_not_submitted" {
				t.Fatalf("delivery = %q, want typed_not_submitted (case c); err=%v", res.delivery, err)
			}
			f := requireOrphanResidue(t, res, "left")
			if f["composer_residue_verified"] != false {
				t.Fatalf("composer_residue_verified = %v, want false (placeholders cannot be attributed)", f["composer_residue_verified"])
			}
			if got := pane.composer(); got != attachmentsComposerLine {
				t.Fatalf("composer changed: %q, want the placeholder line untouched", got)
			}
			if _, enters, bs := pane.counts(); enters != 0 || bs != 0 {
				t.Fatalf("enters=%d backspace calls=%d (%v), want 0 and 0", enters, bs, pane.bsCalls)
			}
			msg := err.Error()
			for _, want := range []string{"prompt truncated in transit", "still typed", "attachments"} {
				if !strings.Contains(msg, want) {
					t.Fatalf("error %q does not say %q", msg, want)
				}
			}

			// The next send is refused, names the leftover as agent-deck's
			// and its age, and touches nothing.
			rec, ok, _ := store.Load(residueTarget)
			if !ok || rec.Verified {
				t.Fatalf("record = %+v ok=%v, want an unverified record", rec, ok)
			}
			res2, err2 := executeSend(pane, "claude", "[inbox] next", mode.noWait, residueTuning(mode.noWait, store))
			if err2 == nil || res2.delivery != "composer_blocked" {
				t.Fatalf("follow-up: delivery=%q err=%v, want composer_blocked", res2.delivery, err2)
			}
			if !strings.Contains(err2.Error(), "left by a failed agent-deck send") {
				t.Fatalf("follow-up error %q must name the agent-deck leftover", err2)
			}
			if res2.jsonFields()["composer_holds"] != "agent_deck_residue" {
				t.Fatalf("follow-up fields = %v, want composer_holds=agent_deck_residue", res2.jsonFields())
			}
			if got := pane.composer(); got != attachmentsComposerLine {
				t.Fatalf("follow-up changed the composer: %q", got)
			}
			if _, enters, bs := pane.counts(); enters != 0 || bs != 0 {
				t.Fatalf("follow-up pressed keys: enters=%d backspace calls=%d", enters, bs)
			}
		})
	}
}
