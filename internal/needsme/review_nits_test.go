package needsme

import (
	"errors"
	"fmt"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRemoteFetchErrorUsesTheParseSentinel(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{name: "wrapped sentinel is bad-reply", err: fmt.Errorf("fetch: %w", session.ErrRemoteSessionsParse), want: ErrBadReply},
		{name: "same words without the sentinel is not", err: errors.New("failed to parse remote sessions: x"), want: nil},
		{name: "ssh failure passes through", err: errors.New("exit status 255"), want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := remoteFetchError(tc.err)
			if tc.want != nil {
				if !errors.Is(got, tc.want) {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
				return
			}
			if errors.Is(got, ErrBadReply) {
				t.Fatalf("got bad-reply for %v", tc.err)
			}
		})
	}
}
