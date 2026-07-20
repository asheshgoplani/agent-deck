package session

import "testing"

// omp is a short name like "pi": it must match only as a whitespace token,
// never as a substring ("docker compose", "stomp-server" contain "omp").
func TestRegistryMatchesOmp(t *testing.T) {
	r := Init(nil)
	cases := []struct {
		cmd  string
		want string
	}{
		{"omp", "omp"},
		{"omp --resume", "omp"},
		{"OMP", "omp"},                     // Match lowercases input
		{"docker compose up", "shell"},     // substring must not match
		{"stomp-server --port 1", "shell"}, // hyphenated token must not match
		{"my-omp-wrapper", "shell"},        // token boundaries respected
		{"pi", "pi"},                       // pi unaffected by insertion
	}
	for _, tc := range cases {
		if got := r.Match(tc.cmd); got != tc.want {
			t.Errorf("Match(%q) = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}
