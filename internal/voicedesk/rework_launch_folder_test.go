package voicedesk

import (
	"context"
	"strings"
	"testing"
)

// plannerDeck is the fixture deck that also knows each group's default
// folder, as the real deck does through `group show`.
type plannerDeck struct {
	*fakeDeck
	folders map[string]string // group -> default folder
}

func (d plannerDeck) PlanLaunch(_ context.Context, spec LaunchSpec) (LaunchSpec, error) {
	f := d.folders[spec.Group]
	if f == "" {
		return spec, ErrNoFolder
	}
	spec.Path, spec.Tool = f, "claude"
	return spec, nil
}

func TestReworkVoiceLaunchSaysFolderAndTool(t *testing.T) {
	h := phase2Harness(t)
	h.desk.Deck = plannerDeck{fakeDeck: h.deck, folders: map[string]string{"team/alex/workers": "/w/projects/alpha"}}
	ask := h.say("start a new session called test one in workers")
	if !strings.Contains(ask.Say, "alpha") || !strings.Contains(ask.Say, "claude") {
		t.Fatalf("read-back %q does not say the folder and the tool", ask.Say)
	}
	got := h.yes(ask)
	if got.Say != "Started test one." || len(h.deck.launched) != 1 {
		t.Fatalf("got %+v launched=%v", got, h.deck.launched)
	}
	if l := h.deck.launched[0]; l.Path != "/w/projects/alpha" || l.Tool != "claude" {
		t.Fatalf("launch spec %+v: want the group's folder and tool claude", l)
	}
}

func TestReworkVoiceLaunchRefusesGroupWithoutFolder(t *testing.T) {
	h := phase2Harness(t)
	h.desk.Deck = plannerDeck{fakeDeck: h.deck, folders: map[string]string{}}
	got := h.say("start a new session called test one in workers")
	if got.Confirm != nil || refusedOf(got) != RefusedNotFound || !strings.Contains(got.Say, "no default folder") {
		t.Fatalf("got %+v: want a refusal naming the missing default folder and no question", got)
	}
	if len(h.deck.launched) != 0 {
		t.Fatalf("launched %v", h.deck.launched)
	}
}

// The folder and tool read back are bound to the yes: a stored question
// whose folder was changed afterwards is not acted on.
func TestReworkVoiceLaunchFolderBoundToToken(t *testing.T) {
	p := Pending{Token: "t", Profile: "work", Kind: "yes", Action: actionLaunch,
		Launch: &LaunchSpec{Title: "a", Group: "g", Path: "/w/a", Tool: "claude"}}
	p.Seal()
	q := p
	l := *p.Launch
	l.Path = "/w/elsewhere"
	q.Launch = &l
	if q.Valid("work") {
		t.Fatal("changing the folder of a stored launch kept the question valid")
	}
	l.Path, l.Tool = "/w/a", "shell"
	if q.Valid("work") {
		t.Fatal("changing the tool of a stored launch kept the question valid")
	}
}
