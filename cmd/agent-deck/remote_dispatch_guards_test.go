package main

import (
	"os"
	"strings"
	"testing"
)

func TestRemoteDispatchRecordsTelemetryBeforeEarlyReturn(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	remote := strings.Index(s, `if len(args) > 0 && args[0] == "remote"`)
	if remote < 0 {
		t.Fatal("remote early dispatch missing")
	}
	call := strings.Index(s[remote:], "recordCLITelemetry(args[0], args[1:])")
	if call < 0 {
		t.Fatal("remote dispatch does not record telemetry")
	}
	if strings.Index(s[remote:], "handleRemote(profile, args[1:])") < call {
		t.Fatal("remote dispatch returns before recording telemetry")
	}
}
