package watcher

import (
	"strings"
	"testing"
)

// TestNormalizeGitHubEvent_MalformedJSON pins the v1.9 contract that the
// four normalizers (issues, pull_request, push, generic) propagate
// json.Unmarshal failure as an error rather than silently producing an
// Event with zero fields. Pre-v1.9 each normalizer did
// `_ = json.Unmarshal(body, &p)`, which made unroutable webhooks
// indistinguishable from valid ones for the receiver.
//
// When this test was first written (RED), all four normalizers returned
// only an Event and the malformed-JSON cases passed silently. The
// consolidated safeUnmarshalGitHubPayload helper plus
// (Event, error)-shaped normalizers turn the test GREEN.
func TestNormalizeGitHubEvent_MalformedJSON(t *testing.T) {
	cases := []struct {
		eventType string
	}{
		{"issues"},
		{"pull_request"},
		{"push"},
		{"unknown_event_type"},
	}

	malformed := []byte("{not valid json")
	for _, tc := range cases {
		t.Run(tc.eventType, func(t *testing.T) {
			_, err := normalizeGitHubEvent(tc.eventType, malformed)
			if err == nil {
				t.Fatalf("normalizeGitHubEvent(%q, malformed) returned nil error; want non-nil — silent json.Unmarshal swallow", tc.eventType)
			}
			if !strings.Contains(err.Error(), "github webhook") && !strings.Contains(err.Error(), "json") {
				t.Errorf("error message %q should mention github webhook or json", err.Error())
			}
		})
	}
}

// TestNormalizeGitHubEvent_ValidJSON verifies the consolidation didn't
// regress the happy path. Each normalizer must return a populated Event
// with nil error for syntactically valid JSON.
func TestNormalizeGitHubEvent_ValidJSON(t *testing.T) {
	cases := []struct {
		eventType   string
		body        string
		wantSubject string
	}{
		{
			eventType:   "issues",
			body:        `{"action":"opened","issue":{"number":42,"title":"Hello","body":"Body"},"sender":{"login":"alice"}}`,
			wantSubject: "[opened] #42: Hello",
		},
		{
			eventType:   "pull_request",
			body:        `{"action":"opened","number":7,"pull_request":{"title":"PR title","body":"pr body"},"sender":{"login":"bob"}}`,
			wantSubject: "[PR opened] #7: PR title",
		},
		{
			eventType:   "push",
			body:        `{"ref":"refs/heads/main","commits":[{"message":"first commit"}],"pusher":{"email":"e@x"},"sender":{"login":"carol"}}`,
			wantSubject: "[push] main: 1 commit(s)",
		},
		{
			eventType:   "ping",
			body:        `{"sender":{"login":"dave"},"repository":{"full_name":"org/repo"}}`,
			wantSubject: "[ping] event from org/repo",
		},
	}

	for _, tc := range cases {
		t.Run(tc.eventType, func(t *testing.T) {
			evt, err := normalizeGitHubEvent(tc.eventType, []byte(tc.body))
			if err != nil {
				t.Fatalf("normalizeGitHubEvent(%q): unexpected error %v", tc.eventType, err)
			}
			if evt.Subject != tc.wantSubject {
				t.Errorf("Subject = %q, want %q", evt.Subject, tc.wantSubject)
			}
			if evt.Source != "github" {
				t.Errorf("Source = %q, want github", evt.Source)
			}
		})
	}
}
