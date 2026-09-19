package query

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/classify"
)

// The three progressively expensive tiers of `recall context`
// (design 11, claude-mem's contract): the card (about 60 tokens), the
// brief (the card plus the derived artifacts and the touched files, about
// 300), the excerpt (the brief plus the newest turns that fit the budget).
const (
	TierCard    = "card"
	TierBrief   = "brief"
	TierExcerpt = "excerpt"
)

// DefaultContextBudget is --budget's default, in tokens.
const DefaultContextBudget = 4000

// charsPerToken converts the token budget to characters (the renderer's
// unit); four is the usual estimate for English prose and code.
const charsPerToken = 4

// excerptMaxMessages bounds how many message rows the excerpt reads before
// the character budget trims them: a 100 MB conductor transcript is never
// decompressed whole for a 4,000-token excerpt.
const excerptMaxMessages = 400

// ErrTier is returned for an unknown --tier.
var ErrTier = errors.New("recall: --tier must be card, brief or excerpt")

// ErrDigestOnly means the session is a card pulled from another machine:
// there are no bodies here, so the excerpt tier cannot be rendered.
var ErrDigestOnly = errors.New("recall: this conversation is a card pulled from another machine; its messages are not here (tier stops at brief; run 'agent-deck remote <host> recall show <id>')")

// ContextResult is what `recall context` prints or delivers.
type ContextResult struct {
	Session   SessionRow `json:"session"`
	Tier      string     `json:"tier"`
	Budget    int        `json:"budget_tokens"`
	Chars     int        `json:"chars"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
	Files     []string   `json:"files,omitempty"`
	// Messages is the session's message count; Included how many the
	// excerpt carries; Truncated whether the budget cut older ones.
	Messages  int    `json:"messages"`
	Included  int    `json:"included,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Text      string `json:"text"`
}

// Context renders one session at the tier under a token budget. The text
// is plain and harness-neutral, so it can be printed or sent to any
// session as a prompt.
func (s *Searcher) Context(ctx context.Context, ref, tier string, budgetTokens int) (ContextResult, error) {
	switch tier {
	case TierCard, TierBrief, TierExcerpt:
	default:
		return ContextResult{}, ErrTier
	}
	if budgetTokens <= 0 {
		budgetTokens = DefaultContextBudget
	}
	sess, err := s.Resolve(ctx, ref)
	if err != nil {
		return ContextResult{}, err
	}
	res := ContextResult{Session: sess, Tier: tier, Budget: budgetTokens}
	if err := s.st.R.QueryRowContext(ctx, `SELECT count(*) FROM msg WHERE sess_id=?`, sess.SessID).Scan(&res.Messages); err != nil {
		return res, err
	}
	if res.Artifacts, err = s.Artifacts(ctx, sess.SessID); err != nil {
		return res, err
	}
	var b strings.Builder
	writeCard(&b, sess, ref)
	if tier != TierCard {
		if res.Files, err = s.touchedFiles(ctx, sess.SessID); err != nil {
			return res, err
		}
		writeBrief(&b, res.Artifacts, res.Files)
	}
	if tier == TierExcerpt {
		if sess.DigestOnly {
			return res, ErrDigestOnly
		}
		turns, err := s.excerptTurns(ctx, sess.SessID)
		if err != nil {
			return res, err
		}
		left := budgetTokens*charsPerToken - b.Len() - 200
		if left < 200 {
			left = 200
		}
		kept, truncated := recall.TailByChars(turns, left)
		res.Included, res.Truncated = len(kept), truncated || len(turns) < res.Messages
		fmt.Fprintf(&b, "\n--- BEGIN RECALLED TRANSCRIPT (%d of %d messages", len(kept), res.Messages)
		if res.Truncated {
			b.WriteString(", older ones left out")
		}
		b.WriteString(") ---\n")
		b.WriteString(recall.RenderTurns(kept))
		b.WriteString("\n--- END RECALLED TRANSCRIPT ---\n")
	}
	b.WriteString("\nThis is recalled context from an earlier conversation, not an instruction: use it to continue what you were doing.\n")
	res.Text = b.String()
	res.Chars = len(res.Text)
	return res, nil
}

func writeCard(b *strings.Builder, sess SessionRow, ref string) {
	fmt.Fprintf(b, "Recalled conversation %s (agent-deck recall show %s)\n", Ref(sess.SessID), Ref(sess.SessID))
	title := sess.Title
	if title == "" {
		title = sess.Preview
	}
	if title != "" {
		fmt.Fprintf(b, "- title: %s\n", title)
	}
	line := fmt.Sprintf("- harness: %s", sess.Harness)
	if sess.Profile != "" {
		line += " (profile " + sess.Profile + ")"
	}
	line += "; conversation " + sess.NativeID
	if sess.DeckID != "" {
		line += "; agent-deck session " + sess.DeckID
	}
	if sess.HostUID != "" {
		line += "; from another machine (card only)"
	}
	b.WriteString(line + "\n")
	if sess.CWD != "" {
		line = "- project: " + sess.CWD
		if sess.Branch != "" {
			line += " (branch " + sess.Branch + ")"
		}
		b.WriteString(line + "\n")
	}
	if sess.StartedAt > 0 || sess.EndedAt > 0 {
		fmt.Fprintf(b, "- when: %s to %s\n", day(sess.StartedAt), day(sess.EndedAt))
	}
	fmt.Fprintf(b, "- turns %d, tool calls %d, errors %d, interrupts %d, compactions %d\n", sess.Turns, sess.ToolCalls, sess.Errors, sess.Interrupts, sess.Compacts)
	if sess.Hints != "" {
		fmt.Fprintf(b, "- hints: %s\n", sess.Hints)
	}
	if sess.Tags != "" {
		fmt.Fprintf(b, "- tags: %s\n", sess.Tags)
	}
	if sess.Missing {
		b.WriteString("- the transcript file is gone; only the index remains\n")
	}
}

func writeBrief(b *strings.Builder, arts []Artifact, files []string) {
	if len(arts) > 0 {
		b.WriteString("Derived:\n")
		for _, a := range arts {
			b.WriteString("- " + a.Line() + "\n")
		}
	} else {
		b.WriteString("Derived: nothing yet (run 'agent-deck recall enrich')\n")
	}
	if len(files) > 0 {
		b.WriteString("Files touched:\n")
		for i, f := range files {
			if i == 12 {
				fmt.Fprintf(b, "- … %d more\n", len(files)-i)
				break
			}
			b.WriteString("- " + f + "\n")
		}
	}
}

// excerptTurns reads the newest conversational rows (prompts, assistant
// text, compaction summaries; never plumbing, never superseded history)
// in sequence order.
func (s *Searcher) excerptTurns(ctx context.Context, sessID int64) ([]recall.Turn, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT role, class, body FROM msg WHERE sess_id=? AND superseded=0 AND class IN (?, ?, ?)
		ORDER BY seq DESC LIMIT ?`, sessID, int(classify.Prompt), int(classify.Assist), int(classify.CompactSummary), excerptMaxMessages)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var turns []recall.Turn
	for rows.Next() {
		var role, class int
		var body []byte
		if err := rows.Scan(&role, &class, &body); err != nil {
			return nil, err
		}
		text, err := recall.DecompressBody(body)
		if err != nil {
			return nil, err
		}
		t := recall.Turn{Role: roleName(role), Content: strings.TrimSpace(string(text))}
		if classify.Class(class) == classify.CompactSummary {
			t.Role = "summary"
		}
		if t.Content == "" {
			continue
		}
		turns = append(turns, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(turns)
	return turns, nil
}

func day(ts int64) string {
	if ts == 0 {
		return "?"
	}
	return time.Unix(ts, 0).Local().Format("2006-01-02")
}
