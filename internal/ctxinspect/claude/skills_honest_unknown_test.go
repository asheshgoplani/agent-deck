package claude

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

// recSkillListingBroken is a skill_listing whose discriminator reads and whose
// body does not: the burst was witnessed, and nothing about the session's skills
// was learnt from it.
const recSkillListingBroken = `{"type":"attachment","sessionId":"s1","attachment":{"type":"skill_listing",` +
	`"content":"- alpha: does alpha things.","skillCount":1,"isInitial":true,"names":"not-an-array"}}`

// bareFixture is a session with instruction files and no skills anywhere on
// disk. newFixture always plants two, which is precisely why the bug survived:
// the on-disk rows papered over the missing floor.
func bareFixture(t *testing.T, transcriptLines ...string) ctxinspect.Request {
	t.Helper()
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "repo")
	writeFile(t, filepath.Join(config, "CLAUDE.md"), "global instructions")
	writeFile(t, filepath.Join(project, "CLAUDE.md"), "project instructions")

	req := ctxinspect.Request{
		Tool:        "claude",
		SessionRef:  "my-session",
		ProjectPath: project,
		ConfigDir:   config,
		Host:        &ctxinspect.StaticHost{ClaudeTools: []string{"claude"}},
	}
	if len(transcriptLines) > 0 {
		req.TranscriptPath = writeTranscript(t, transcriptLines...)
	}
	return req
}

// TestSkillsCategoryUndecodableCatalogueIsNotAZero is the regression test for
// the last bare zero on the panel.
//
// Every other category routes an unreadable record to the honest-unknown floor.
// skills never set Unobserved at all, and unknownSkillItems returned before it
// could say anything when no skills were on disk — so a session whose catalogue
// WAS recorded and could not be decoded rendered "skills (0 items)" at 0 tokens:
// a claim about the user's configuration made out of a failure to read.
func TestSkillsCategoryUndecodableCatalogueIsNotAZero(t *testing.T) {
	rep := inspect(t, bareFixture(t, recSkillListingBroken, recAssistant))

	cat, ok := rep.Category(CategorySkills)
	if !ok {
		t.Fatal("want a skills category: the burst was witnessed, so the category is unread rather than absent")
	}
	if len(cat.Items) != 0 {
		t.Fatalf("fixture is wrong: want no skills on disk, got %d items", len(cat.Items))
	}
	if !cat.Unobserved {
		t.Error("the skills category claims its contents were established: its catalogue was recorded and could not be decoded")
	}
	if _, complete := cat.DisplayTotal(); complete {
		t.Error("the skills category reports a complete total: an unread category's 0 prints as a measured zero")
	}
	if _, known := cat.DisplayItemCount(); known {
		t.Error("the skills category reports an item count: '(0 items)' reads as 'this session loaded no skills'")
	}
	if !hasCaveat(rep, "skill-catalogue-undecodable") {
		t.Errorf("the failed decode is not reported anywhere: caveats were %v", caveatCodes(rep))
	}
	for _, note := range cat.Notes {
		if strings.Contains(note, "recorded no skill catalogue") {
			t.Errorf("note claims an absence it cannot establish: %q", note)
		}
	}
	if len(rep.Violations) != 0 {
		t.Fatalf("violations: %v", rep.Violations)
	}
}

// TestSkillsCategoryUnreadTranscriptIsNotAZero covers the other way the floor is
// reached: no startup burst was found at all, so silence means the parser never
// looked rather than that the session listed nothing.
func TestSkillsCategoryUnreadTranscriptIsNotAZero(t *testing.T) {
	rep := inspect(t, bareFixture(t))

	cat, ok := rep.Category(CategorySkills)
	if !ok {
		t.Fatal("want a skills category")
	}
	if !cat.Unobserved {
		t.Error("a skills category with no session observed claims its emptiness was established")
	}
	if len(rep.Violations) != 0 {
		t.Fatalf("violations: %v", rep.Violations)
	}
}

// TestSkillsCategoryObservedEmptyStillReportsACertainZero is the other half of
// the rule, and the reason the fix is a predicate rather than a blanket
// "unknown". When the burst was read in full and carried no skill catalogue,
// and nothing is on disk, the session really does load no skills — and that is
// a measurement worth printing as one.
func TestSkillsCategoryObservedEmptyStillReportsACertainZero(t *testing.T) {
	rep := inspect(t, bareFixture(t, recMCPInstructions, recAssistant))

	cat, ok := rep.Category(CategorySkills)
	if !ok {
		t.Fatal("want a skills category")
	}
	if cat.Unobserved {
		t.Error("a skills category whose burst was read in full is marked unobserved: an established absence is a finding, not a gap")
	}
	if _, complete := cat.DisplayTotal(); !complete {
		t.Error("an established absence reports an incomplete total")
	}
	if n, known := cat.DisplayItemCount(); !known || n != 0 {
		t.Errorf("DisplayItemCount() = %d, %v; want 0, true for an established absence", n, known)
	}
}

// TestSkillsCategoryWithItemsIsNeverUnobserved pins the invariant the flag has
// to respect: [ctxinspect.Report.Validate] rejects a category that claims its
// contents could not be established and then lists them.
func TestSkillsCategoryWithItemsIsNeverUnobserved(t *testing.T) {
	f := newFixture(t, recSkillListingBroken, recAssistant)
	rep := inspect(t, f.request())

	cat, ok := rep.Category(CategorySkills)
	if !ok {
		t.Fatal("want a skills category")
	}
	if len(cat.Items) == 0 {
		t.Fatal("fixture is wrong: newFixture plants skills on disk")
	}
	if cat.Unobserved {
		t.Error("a category that lists its contents is marked unobserved; Validate rejects that pairing")
	}
	if len(rep.Violations) != 0 {
		t.Fatalf("violations: %v", rep.Violations)
	}
}
