package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestCLIAccountHelpExplainsStoredSlot(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		args        []string
	}{
		{"list", "ACCOUNT", []string{"list"}},
		{"all_profiles", "ACCOUNT", []string{"list", "--all"}},
		{"show", "Account:", []string{"session", "show"}},
	} {
		for _, flag := range []string{"--help", "-h"} {
			t.Run(tc.name+"/"+flag, func(t *testing.T) {
				home := t.TempDir()
				before := snapshotTree(t, home)
				args := append(append([]string{}, tc.args...), flag)
				stdout, stderr, code := runAgentDeck(t, home, args...)
				out := stdout + stderr
				if code != 0 {
					t.Fatalf("help exit %d: %s", code, out)
				}
				for _, want := range []string{tc.field, "quoted stored account slot", "not a resolved account or login identity", `JSON always includes the raw "account" string`, `including "" when no slot is stored`} {
					if !strings.Contains(out, want) {
						t.Errorf("help omits %q: %s", want, out)
					}
				}
				if after := snapshotTree(t, home); !reflect.DeepEqual(before, after) {
					t.Errorf("help changed HOME: before=%v after=%v", before, after)
				}
			})
		}
	}
}
