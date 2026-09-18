package session

import (
	"testing"
	"time"
)

// TestParseRemoteHostStats_SSHAndReasons pins the two wire additions: an
// account's unknown_reason rides through, and ssh_sessions is first-class
// unknown (absent key → SSHAvailable false, ssh_error kept) rather than an
// empty "nobody".
func TestParseRemoteHostStats_SSHAndReasons(t *testing.T) {
	t.Run("full", func(t *testing.T) {
		stats, err := parseRemoteHostStats([]byte(`{
  "accounts": [{"name": "a", "known": false, "unknown_reason": "no_feed"}, {"name": "b", "known": true, "five_hour_percent": 8}],
  "ssh_sessions": [{"user": "yasir", "count": 2, "since": 1758179400, "from": "10.0.0.5"}, {"user": "ashesh", "count": 1}]
}`))
		if err != nil {
			t.Fatal(err)
		}
		if stats.Accounts[0].UnknownReason != AccountUsageNoFeed || stats.Accounts[1].UnknownReason != "" {
			t.Errorf("reasons = %+v", stats.Accounts)
		}
		if !stats.SSHAvailable || len(stats.SSHSessions) != 2 {
			t.Fatalf("ssh = %+v", stats)
		}
		if s := stats.SSHSessions[0]; s.User != "yasir" || s.Count != 2 || !s.HasSince || !s.Since.Equal(time.Unix(1758179400, 0)) || s.From != "10.0.0.5" {
			t.Errorf("session = %+v", s)
		}
		if s := stats.SSHSessions[1]; s.HasSince || s.From != "" {
			t.Errorf("session without since/from = %+v", s)
		}
	})
	t.Run("older remote", func(t *testing.T) {
		stats, err := parseRemoteHostStats([]byte(`{"accounts": [{"name": "a", "known": false}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if stats.SSHAvailable || stats.SSHError != "" || stats.Accounts[0].UnknownReason != "" {
			t.Errorf("stats = %+v", stats)
		}
	})
	t.Run("who failed", func(t *testing.T) {
		stats, err := parseRemoteHostStats([]byte(`{"ssh_error": "who: not found"}`))
		if err != nil {
			t.Fatal(err)
		}
		if stats.SSHAvailable || stats.SSHError != "who: not found" {
			t.Errorf("stats = %+v", stats)
		}
	})
	t.Run("nobody", func(t *testing.T) {
		stats, err := parseRemoteHostStats([]byte(`{"ssh_sessions": []}`))
		if err != nil {
			t.Fatal(err)
		}
		if !stats.SSHAvailable || len(stats.SSHSessions) != 0 {
			t.Errorf("stats = %+v", stats)
		}
	})
}
