package managed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeDSN = "https://public@example.invalid/1"

func writeManaged(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "managed.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFile(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    Config
		wantErr string
	}{
		{"all fields", `{"org_id":"pilot-7f3k2","telemetry_level":"Basic","crash_dsn":"` + fakeDSN + `","update_channel":"pilot"}`,
			Config{OrgID: "pilot-7f3k2", TelemetryLevel: "basic", CrashDSN: fakeDSN, UpdateChannel: "pilot"}, ""},
		{"uuid org", `{"org_id":"0b6c2a8e-1f2d-4c3b-9a8e-7d6c5b4a3f21"}`,
			Config{OrgID: "0b6c2a8e-1f2d-4c3b-9a8e-7d6c5b4a3f21"}, ""},
		{"empty object", `{}`, Config{}, ""},
		{"unknown keys ignored", `{"org_id":"abc","future_key":true}`, Config{OrgID: "abc"}, ""},
		{"email org refused", `{"org_id":"alice@example.com"}`, Config{}, "org_id"},
		{"domain org refused", `{"org_id":"example.com"}`, Config{}, "org_id"},
		{"spaces org refused", `{"org_id":"Alice Smith"}`, Config{}, "org_id"},
		{"too long org refused", `{"org_id":"` + strings.Repeat("a", 41) + `"}`, Config{}, "org_id"},
		{"bad level", `{"telemetry_level":"max"}`, Config{}, "telemetry_level"},
		{"bad channel", `{"update_channel":"nightly"}`, Config{}, "update_channel"},
		{"dsn with secret", `{"crash_dsn":"https://public:secret@example.invalid/1"}`, Config{}, "secret"},
		{"dsn plain http", `{"crash_dsn":"http://public@example.invalid/1"}`, Config{}, "https"},
		{"dsn no project", `{"crash_dsn":"https://public@example.invalid/"}`, Config{}, "project"},
		{"not json", `org_id = "abc"`, Config{}, "JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := LoadFile(writeManaged(t, tc.body))
			if !r.Present {
				t.Fatal("Present = false for an existing file")
			}
			if tc.wantErr != "" {
				if r.Err == nil || !strings.Contains(r.Err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one mentioning %q", r.Err, tc.wantErr)
				}
				if r.Config != (Config{}) {
					t.Fatalf("an invalid file must yield no settings, got %+v", r.Config)
				}
				if strings.Contains(r.Err.Error(), "secret@") || strings.Contains(r.Err.Error(), "example.invalid") {
					t.Fatalf("error quotes the DSN: %v", r.Err)
				}
				return
			}
			if r.Err != nil {
				t.Fatalf("unexpected err: %v", r.Err)
			}
			if r.Config != tc.want {
				t.Fatalf("got %+v, want %+v", r.Config, tc.want)
			}
		})
	}
}

func TestLoadMissingFileIsAbsent(t *testing.T) {
	r := LoadFile(filepath.Join(t.TempDir(), "nope.json"))
	if r.Present || r.Err != nil || r.Config != (Config{}) {
		t.Fatalf("missing file: %+v", r)
	}
	if r := LoadFile(""); r.Present || r.Err != nil {
		t.Fatalf("empty path: %+v", r)
	}
}

func TestLoadOversizedFileFails(t *testing.T) {
	r := LoadFile(writeManaged(t, `{"org_id":"abc","pad":"`+strings.Repeat("x", maxFileBytes)+`"}`))
	if r.Err == nil || r.Config != (Config{}) {
		t.Fatalf("oversized file accepted: err=%v cfg=%+v", r.Err, r.Config)
	}
}

func TestPathEnvOverride(t *testing.T) {
	p := writeManaged(t, `{"org_id":"abc"}`)
	t.Setenv(EnvPath, p)
	if got := Path(); got != p {
		t.Fatalf("Path() = %q, want %q", got, p)
	}
	if r := Load(); r.OrgID != "abc" {
		t.Fatalf("Load via env: %+v", r)
	}
	t.Setenv(EnvPath, "")
	if got := Path(); got != "" {
		t.Fatalf("empty override must disable the default path, got %q", got)
	}
}

func TestValidOrgID(t *testing.T) {
	for _, ok := range []string{"abc", "pilot-1", "a1b2c3d4e5", "0b6c2a8e-1f2d-4c3b-9a8e-7d6c5b4a3f21"} {
		if !ValidOrgID(ok) {
			t.Errorf("ValidOrgID(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "ab", "-abc", "abc-", "ABC", "a.b.c", "a@b.c", "a b c", "a_b_c", strings.Repeat("a", 41)} {
		if ValidOrgID(bad) {
			t.Errorf("ValidOrgID(%q) = true", bad)
		}
	}
}
