package session

import "testing"

func TestIssue1977_OMPIsRecognizedAsBuiltin(t *testing.T) {
	r := Init(nil)

	for _, command := range []string{"omp", "omp --model sonnet", "/usr/local/bin/omp --continue"} {
		if got := r.Match(command); got != "omp" {
			t.Errorf("Match(%q) = %q, want omp", command, got)
		}
	}
	if !r.IsBuiltin("omp") {
		t.Error("IsBuiltin(\"omp\") = false, want true")
	}
}
