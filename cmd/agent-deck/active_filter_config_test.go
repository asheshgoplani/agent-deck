package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestActiveFilterConfigCLI(t *testing.T) {
	if os.Getenv("AGENT_DECK_FILTER_CONFIG_HELPER") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				handleConfig("_filter", os.Args[i+1:])
				os.Exit(0)
			}
		}
		os.Exit(2)
	}
	home := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestActiveFilterConfigCLI$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "AGENT_DECK_FILTER_CONFIG_HELPER=1", "AGENT_DECK_TASK6_HELPER_PROCESS=1", "HOME="+home,
			"XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_DATA_HOME="+filepath.Join(home, "data"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return string(out)
	}
	for _, tc := range []struct {
		key, raw string
		want     any
	}{
		{"display.default_filter", "active", "active"},
		{"display.active_filter_excludes", "error,stopped", []any{"error", "stopped"}},
	} {
		for _, args := range [][]string{{"set", tc.key, tc.raw, "--json"}, {"get", tc.key, "--json"}} {
			var got struct {
				Key   string `json:"key"`
				Value any    `json:"value"`
			}
			out := run(args...)
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("invalid JSON: %s: %v", out, err)
			}
			if got.Key != tc.key || !reflect.DeepEqual(got.Value, tc.want) {
				t.Fatalf("%v: %s", args, out)
			}
		}
	}
}
