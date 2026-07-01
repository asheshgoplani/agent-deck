package tmux

import "testing"

func TestRedactInlineExports(t *testing.T) {
	in := `bash -lc 'export AGENTDECK_INSTANCE_ID='\''x'\''; export TOKEN='supersecret'; exec claude'`
	got := redactInlineExports(in)
	if want := "supersecret"; contains(got, want) {
		t.Fatalf("secret leaked: %s", got)
	}
	if !contains(got, "export TOKEN='***'") {
		t.Fatalf("not redacted: %s", got)
	}
}

// A value with an embedded (shell-escaped) single quote must be redacted WHOLE —
// the escaped tail must not leak. buildSessionEnvExports renders a literal quote
// in a value as `'\”`, so TOKEN=a'secret becomes export TOKEN='a'\”secret'.
func TestRedactInlineExports_EscapedQuoteValue(t *testing.T) {
	in := `export TOKEN='a'\''secret'; exec claude`
	got := redactInlineExports(in)
	if contains(got, "secret") {
		t.Fatalf("escaped-quote value leaked: %s", got)
	}
	if !contains(got, "export TOKEN='***'") {
		t.Fatalf("not redacted: %s", got)
	}
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
