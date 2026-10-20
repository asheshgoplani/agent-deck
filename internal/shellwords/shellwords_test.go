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

func TestCommands(t *testing.T) {
	tests := []struct {
		command string
		want    []string
		ok      bool
	}{
		{`cd -- /src && unset CLAUDE_JOB_DIR && pi --continue`, []string{"cd -- /src", "unset CLAUDE_JOB_DIR", "pi --continue"}, true},
		{`a; b || c | d`, []string{"a", "b", "c", "d"}, true},
		{"a\nb", []string{"a", "b"}, true},
		{`export X='1;2' && echo "a && b"`, []string{`export X='1;2'`, `echo "a && b"`}, true},
		{`printf '%s\n' "$code" > /x`, []string{`printf '%s\n' "$code" > /x`}, true},
		{`echo a\;b; c`, []string{`echo a\;b`, "c"}, true},
		{`"unterminated && x`, nil, false},
		{``, nil, true},
	}
	for _, tt := range tests {
		got, ok := Commands(tt.command)
		if ok != tt.ok || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Commands(%q) = (%q, %v), want (%q, %v)", tt.command, got, ok, tt.want, tt.ok)
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
