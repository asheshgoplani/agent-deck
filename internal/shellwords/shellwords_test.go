package shellwords

import (
	"reflect"
	"testing"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		command string
		want    []string
		ok      bool
	}{
		{`"/opt/AI Tools/pi" --profile dev`, []string{"/opt/AI Tools/pi", "--profile", "dev"}, true},
		{`'/opt/AI Tools/pi'`, []string{"/opt/AI Tools/pi"}, true},
		{`/opt/AI\ Tools/pi`, []string{"/opt/AI Tools/pi"}, true},
		{`env PROFILE='team dev' /opt/AI\ Tools/pi`, []string{"env", "PROFILE=team dev", "/opt/AI Tools/pi"}, true},
		{`"" pi`, []string{"", "pi"}, true},
		{`"unterminated`, nil, false},
		{`trailing\`, nil, false},
	}
	for _, tt := range tests {
		got, ok := Split(tt.command)
		if ok != tt.ok || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Split(%q) = (%q, %v), want (%q, %v)", tt.command, got, ok, tt.want, tt.ok)
		}
	}
}

func TestExecutableBase(t *testing.T) {
	words, ok := Split(`env PROFILE='team dev' sudo "/opt/AI Tools/pi" --profile dev`)
	if !ok {
		t.Fatal("Split rejected valid command")
	}
	if got := ExecutableBase(words); got != "pi" {
		t.Fatalf("ExecutableBase() = %q, want pi", got)
	}
}
