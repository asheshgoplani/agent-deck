package session

import (
	"os"
	"strings"
	"testing"
)

func TestRunIOCleansStaleControlMasterSockets(t *testing.T) {
	b, err := os.ReadFile("ssh_command_io.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "CleanStaleSSHSockets()") {
		t.Fatal("RunIO must sweep confirmed-dead control sockets before invoking ssh")
	}
}
