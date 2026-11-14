package assistant

import (
	"net/http"
	"testing"
)

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

func TestRequestBearerToken(t *testing.T) {
	tok := TokenPrefix + "abc234"
	cases := []struct {
		name    string
		headers []string
		want    string
		ok      bool
	}{
		{"none", nil, "", true},
		{"one", []string{"Bearer " + tok}, tok, true},
		{"one, lowercase", []string{"bearer " + tok}, tok, true},
		{"one, not bearer", []string{"Basic dXNlcjpwYXNz"}, "", true},
		{"two", []string{"Bearer " + tok, "Bearer " + tok}, "", false},
		{"two, one empty", []string{"", "Bearer " + tok}, "", false},
	}
	for _, c := range cases {
		h := http.Header{}
		for _, v := range c.headers {
			h.Add("Authorization", v)
		}
		got, ok := RequestBearerToken(h)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: RequestBearerToken = %q, %v; want %q, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}
