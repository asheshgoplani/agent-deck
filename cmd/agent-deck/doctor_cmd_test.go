package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDoctorSharedAccountCLI(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "shared")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	config := []byte("[profiles.first.claude]\nconfig_dir='~/shared'\n[profiles.second.claude]\nconfig_dir='$HOME/shared'\n")
	path := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(path, config, 0600); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, home)
	stdout, stderr, code := runAgentDeck(t, home, "doctor", "--json")
	if code != 0 {
		t.Fatalf("doctor exit %d: %s %s", code, stdout, stderr)
	}
	var report struct {
		AccountSlots []struct {
			Name       string   `json:"name"`
			State      string   `json:"state"`
			SharedWith []string `json:"shared_with"`
		} `json:"account_slots"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("doctor JSON: %v: %s", err, stdout)
	}
	if len(report.AccountSlots) != 2 {
		t.Fatalf("expected two diagnosed slots: %s", stdout)
	}
	for _, slot := range report.AccountSlots {
		if slot.State != "warning" || len(slot.SharedWith) != 1 {
			t.Fatalf("missing duplicate warning: %s", stdout)
		}
	}
	stdout, stderr, code = runAgentDeck(t, home, "doctor")
	if code != 0 || !strings.Contains(stdout, "WARNING") || !strings.Contains(stdout, `"first"`) || !strings.Contains(stdout, `"second"`) {
		t.Fatalf("human warning absent: exit %d: %s %s", code, stdout, stderr)
	}
	after := snapshotTree(t, home)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("doctor changed HOME: before=%v after=%v", before, after)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(config) {
		t.Fatalf("doctor changed config: %v", err)
	}
}

func TestDoctorHelpAndArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"help", []string{"doctor", "--help"}, 0},
		{"short_help", []string{"doctor", "-h"}, 0},
		{"positional", []string{"doctor", "unexpected"}, 2},
		{"unknown_flag", []string{"doctor", "--unexpected"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			before := snapshotTree(t, home)
			stdout, stderr, code := runAgentDeck(t, home, tc.args...)
			if code != tc.code {
				t.Fatalf("exit %d expected %d: %s %s", code, tc.code, stdout, stderr)
			}
			if tc.code == 0 && !strings.Contains(stdout+stderr, "named Claude") {
				t.Fatalf("help missing diagnostic scope: %s %s", stdout, stderr)
			}
			if after := snapshotTree(t, home); !reflect.DeepEqual(before, after) {
				t.Fatalf("command changed HOME")
			}
		})
	}
}

func TestDoctorQuotesSlotControls(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, ".agent-deck")
	if err := os.Mkdir(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	config := `[profiles."bad\u001b\u0007\u000a".claude]
config_dir = '~/missing'
[profiles.other.claude]
config_dir = '~/missing'
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runAgentDeck(t, home, "doctor")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, stdout, stderr)
	}
	if strings.ContainsAny(stdout, "\x1b\a") || !strings.Contains(stdout, `"bad\x1b\a\n"`) {
		t.Fatalf("unsafe or unquoted diagnostic: %q", stdout)
	}
	if !strings.Contains(stdout, "WARNING") || !strings.Contains(stdout, "path: unknown") {
		t.Fatalf("missing duplicate/unknown distinction: %s", stdout)
	}
}

func TestDoctorRemoteNeverRunsLocally(t *testing.T) {
	home := t.TempDir()
	before := snapshotTree(t, home)
	stdout, stderr, code := runAgentDeck(t, home, "remote", "example", "doctor", "--json")
	out := strings.ToLower(stdout + stderr)
	if code == 0 || !strings.Contains(out, "remote") || (!strings.Contains(out, "unknown") && !strings.Contains(out, "unsupported") && !strings.Contains(out, "not supported")) {
		t.Fatalf("remote must reject unsupported doctor: exit %d: %s", code, out)
	}
	if strings.Contains(out, "account_slots") || strings.Contains(out, "named claude account directories") {
		t.Fatalf("remote ran doctor locally: %s", out)
	}
	if after := snapshotTree(t, home); !reflect.DeepEqual(before, after) {
		t.Fatalf("remote rejection changed HOME")
	}
}
