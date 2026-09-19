package tmux

import "testing"

func TestDetectToolFromCommandOMPSupportedLaunchersAndNoArgumentFalsePositive(t *testing.T) {
	tests := map[string]string{
		"omp":                           "omp",
		"oh-my-pi --model opus":         "omp",
		"npx @oh-my-pi/pi-coding-agent": "omp",
		"env OMP_PROFILE=work npx @oh-my-pi/pi-coding-agent": "omp",
		"grep omp README.md": "",
	}
	for command, want := range tests {
		if got := detectToolFromCommand(command); got != want {
			t.Errorf("detectToolFromCommand(%q) = %q, want %q", command, got, want)
		}
	}
}
