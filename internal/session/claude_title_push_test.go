package session

import "testing"

func TestExtraArgsSupplyName(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"nil", nil, false},
		{"unrelated flags", []string{"--model", "opus"}, false},
		{"long flag", []string{"--name", "mine"}, true},
		{"short flag", []string{"-n", "mine"}, true},
		{"equals form", []string{"--name=mine"}, true},
		// A VALUE that happens to read like the flag must not count: the
		// operator passed no name of their own here.
		{"value that looks like a flag name", []string{"--append-system-prompt", "call it --names"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extraArgsSupplyName(tc.args); got != tc.want {
				t.Fatalf("extraArgsSupplyName(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
