package main

import "testing"

// `--token-file` is read, trimmed, and rejected when it is empty or carries
// whitespace or control bytes — with the reason stated in resolveWebToken's own
// comment: such a token can never match a Bearer header, so it boots a server
// nobody can authenticate to while still satisfying the non-loopback bind
// check. `--token` fed the same field and got none of it.
//
// The case that matters most is `--token "   "`. Trimming it to empty would
// hand back the documented "authorization disabled" value, turning an operator
// who set a token into a server with no auth at all — fail-open, from an input
// that looks like a credential. It must be an error.
func TestResolveWebToken_InlineTokenGetsTheSameFloorAsTokenFile(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		want    string
		wantErr bool
		why     string
	}{
		{
			name:  "whitespace-only is an error, never a fall-through to no-auth",
			token: "   ", wantErr: true,
			why: "trimming to empty would disable authorization on an input that reads as setting it",
		},
		{
			name:  "interior whitespace cannot appear in a Bearer header",
			token: "ab cd", wantErr: true,
			why: "the server would boot and refuse everyone, which looks like working auth",
		},
		{
			name:  "control characters are rejected for the same reason",
			token: "abc\tdef", wantErr: true,
		},
		{
			name:  "surrounding whitespace is trimmed, matching the file path",
			token: "  s3cret  ", want: "s3cret",
			why: "--token-file trims before validating; the flag should not be stricter or looser",
		},
		{
			name:  "an unset token still means authorization disabled",
			token: "", want: "",
			why: "documented behaviour: Server.authorize allows everything when Token is empty",
		},
		{
			name:  "an ordinary token is unchanged",
			token: "s3cret", want: "s3cret",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveWebToken(tc.token, "")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveWebToken(%q, \"\") = %q, nil — want an error. %s", tc.token, got, tc.why)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveWebToken(%q, \"\") returned %v, want %q. %s", tc.token, err, tc.want, tc.why)
			}
			if got != tc.want {
				t.Errorf("resolveWebToken(%q, \"\") = %q, want %q. %s", tc.token, got, tc.want, tc.why)
			}
		})
	}
}
