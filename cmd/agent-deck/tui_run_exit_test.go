package main

import (
	"os"
	"strings"
	"testing"
)

// The TUI's p.Run error path must hand the error to CloseTelemetryAfterRun,
// which tells a recovered panic apart from an interrupt or a terminal setup
// failure. Passing ExitPanic for every Run error mislabels those as panics.
func TestTUIRunErrorPathDoesNotAssumePanic(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if strings.Contains(s, "CloseTelemetry(telemetry.ExitPanic)") {
		t.Fatal("main.go records ExitPanic for any tea.Program.Run error")
	}
	if !strings.Contains(s, "homeModel.CloseTelemetryAfterRun(runErr)") {
		t.Fatal("main.go does not pass the tea.Program.Run error to CloseTelemetryAfterRun")
	}
}
