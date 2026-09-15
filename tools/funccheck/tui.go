package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func (s *suite) tui() {
	checks := []struct{ name, test string }{
		{"home list", "TestFunctionalHomeGolden"},
		{"new-session dialog", "TestNewSessionFlow_Golden"},
		{"remote row states", "TestFunctionalRemoteGolden"},
	}
	for _, check := range checks {
		s.skip("TUI binary: "+check.name, "Compare the selected binary's rendered frame", "Headless golden tests validate checkout source only; selected binary rendering is unverified")
		name := "TUI source: " + check.name
		did := "Render checkout source and compare committed golden frames (" + check.test + ")"
		if os.Getenv("FUNCCHECK_SOURCE_CHECKS") != "1" {
			s.skip(name, did, "Set FUNCCHECK_SOURCE_CHECKS=1 inside Docker or native CI to enable source validation")
			continue
		}
		s.run(name, did, func() (string, error) {
			output, err := s.execIn(s.sourceDir, "go", "test", "-json", "-count=1", "-timeout=90s", "./internal/ui", "-run", "^"+check.test+"$")
			if err != nil {
				return output, err
			}
			// A missing or skipped test must not become a passing capability.
			for _, line := range strings.Split(output, "\n") {
				var event struct{ Action, Test string }
				if json.Unmarshal([]byte(line), &event) == nil && event.Action == "pass" && event.Test == check.test {
					return "Checkout source matches committed golden frames; selected binary rendering remains unverified", nil
				}
			}
			return output, fmt.Errorf("golden test %s did not report a passing execution", check.test)
		})
	}
}
