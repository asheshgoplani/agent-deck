package watcher

import (
	"testing"

	"golang.org/x/oauth2"
)

func TestAdapterHealthCheckRejectsUninitializedState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func() error
		want  string
	}{
		{name: "gmail zero", check: (&GmailAdapter{}).HealthCheck, want: "gmail: adapter is not initialized: token source is unavailable"},
		{name: "github zero", check: (&GitHubAdapter{}).HealthCheck, want: "github: adapter is not initialized"},
		{name: "webhook zero", check: (&WebhookAdapter{}).HealthCheck, want: "webhook: adapter is not initialized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.check(); err == nil || err.Error() != tc.want {
				t.Fatalf("HealthCheck() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestGmailHealthCheckRejectsPartialState(t *testing.T) {
	partial := &GmailAdapter{
		name:     "partial",
		tokenSrc: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test"}),
	}
	if err := partial.HealthCheck(); err == nil {
		t.Fatal("partially initialized Gmail adapter reported healthy")
	}
}
