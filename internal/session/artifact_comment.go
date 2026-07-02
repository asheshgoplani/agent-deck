package session

import (
	"fmt"
	"strings"
	"time"
)

// artifactCommentKind marks an inbox record as a Fleet Console annotation
// rather than a child-completion transition, so a draining consumer can tell
// "the operator highlighted my report and left a note" apart from "a child
// finished". It is carried in TransitionNotificationEvent.Kind.
const artifactCommentKind = "fleet-console-annotation"

// ArtifactComment is one highlight-to-route annotation from the Fleet Console:
// a passage the operator highlighted in a rendered artifact plus their note.
type ArtifactComment struct {
	ArtifactID    string
	ArtifactTitle string
	Excerpt       string // the highlighted passage
	Comment       string // the operator's note
	Profile       string
}

// FormatArtifactComment renders the tagged, human-readable payload delivered to
// the owning session. The tag + the three meaningful parts (title, excerpt,
// comment) travel verbatim so a future skill can parse and act on it.
func FormatArtifactComment(c ArtifactComment) string {
	var b strings.Builder
	b.WriteString("[fleet-console] annotation on artifact: ")
	b.WriteString(strings.TrimSpace(c.ArtifactTitle))
	b.WriteString("\n")
	if ex := strings.TrimSpace(c.Excerpt); ex != "" {
		b.WriteString("> highlighted: ")
		b.WriteString(ex)
		b.WriteString("\n")
	}
	b.WriteString("comment: ")
	b.WriteString(strings.TrimSpace(c.Comment))
	return b.String()
}

// DeliverArtifactComment routes an annotation to its owning session.
//
// When busy is true the target session is actively working (running/starting),
// so a raw send-keys would be silently swallowed by the live pane — the comment
// is committed to the session's DURABLE INBOX instead, where it is drained on
// the session's next turn boundary (exactly the reliability guarantee the inbox
// exists for). When busy is false the session is at a prompt and the comment is
// delivered through the same reliable queued send path as `agent-deck session
// send`.
//
// Each annotation gets a UNIQUE inbox child id so the inbox's last-wins-per-
// child collapse keeps multiple comments on the same artifact distinct.
func DeliverArtifactComment(targetSessionID string, busy bool, c ArtifactComment) error {
	targetSessionID = strings.TrimSpace(targetSessionID)
	if targetSessionID == "" {
		return fmt.Errorf("artifact comment: empty target session id")
	}
	payload := FormatArtifactComment(c)

	if busy {
		childID := fmt.Sprintf("fleet-console:%s:%d", c.ArtifactID, time.Now().UnixNano())
		ev := TransitionNotificationEvent{
			ChildSessionID:  childID,
			ChildTitle:      c.ArtifactTitle,
			Profile:         c.Profile,
			Kind:            artifactCommentKind,
			DoneStatus:      "comment",
			DoneSummary:     payload,
			FromStatus:      "running",
			ToStatus:        "waiting",
			Timestamp:       time.Now(),
			TargetSessionID: targetSessionID,
			TargetKind:      "session",
		}
		return CommitToInbox(targetSessionID, ev)
	}
	return SendSessionMessageReliable(c.Profile, targetSessionID, payload)
}
