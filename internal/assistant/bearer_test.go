package assistant

import "testing"

// xAI's remote MCP tool sets its "authorization" value in the Authorization
// header, and its docs do not say whether a "Bearer " prefix is added. A bare
// assistant token (recognisable by TokenPrefix) is accepted as well.
func TestBearerToken(t *testing.T) {
	tok := TokenPrefix + "abc234"
	cases := []struct {
		header, want string
	}{
		{"Bearer " + tok, tok},
		{"bearer " + tok, tok},
		{"  Bearer   " + tok + "  ", tok},
		{tok, tok},
		{" " + tok + " ", tok},
		{"", ""},
		{"Bearer", ""},
		{"Basic dXNlcjpwYXNz", ""},
		{"someothertoken", ""},
		{tok + " extra", ""},
	}
	for _, c := range cases {
		if got := BearerToken(c.header); got != c.want {
			t.Errorf("BearerToken(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}
