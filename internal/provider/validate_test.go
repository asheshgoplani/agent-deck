package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validSandbox(t *testing.T) Sandbox {
	return Sandbox{ID: "sb-1", Host: "127.0.0.1", Port: 2222, User: "dev",
		Auth: Auth{Type: "key", KeyPath: privateKey(t)}, Labels: map[string]string{"provider": "fake"}}
}

func TestSandboxValidate(t *testing.T) {
	symlinkKey := func(t *testing.T) string {
		target := privateKey(t)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		return link
	}
	worldReadableKey := func(t *testing.T) string {
		p := privateKey(t)
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	manyLabels := map[string]string{}
	for i := 0; i < 33; i++ {
		manyLabels["k"+strings.Repeat("x", i)] = "v"
	}
	tests := []struct {
		name    string
		mut     func(t *testing.T, s *Sandbox)
		wantErr string // "" = valid
	}{
		{name: "valid key auth", mut: func(*testing.T, *Sandbox) {}},
		{name: "valid agent auth", mut: func(_ *testing.T, s *Sandbox) { s.Auth = Auth{Type: "agent"} }},
		{name: "dns host", mut: func(_ *testing.T, s *Sandbox) { s.Host = "sb-1.sandbox.example.internal" }},
		{name: "ipv6 host", mut: func(_ *testing.T, s *Sandbox) { s.Host = "fd00::1" }},
		{name: "port defaults to 22", mut: func(_ *testing.T, s *Sandbox) { s.Port = 0 }},
		{name: "host keys pinned", mut: func(_ *testing.T, s *Sandbox) {
			s.HostKeys = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"}
		}},
		{name: "labels with shell metachar values are kept as data", mut: func(_ *testing.T, s *Sandbox) {
			s.Labels = map[string]string{"note": "$(touch /tmp/x); `id` | rm -rf ~"}
		}},
		{name: "empty id", mut: func(_ *testing.T, s *Sandbox) { s.ID = "" }, wantErr: "id"},
		{name: "id with slash", mut: func(_ *testing.T, s *Sandbox) { s.ID = "../sb" }, wantErr: "id"},
		{name: "host option injection", mut: func(_ *testing.T, s *Sandbox) { s.Host = "-oProxyCommand=sh" }, wantErr: "must not begin with '-'"},
		{name: "host with space", mut: func(_ *testing.T, s *Sandbox) { s.Host = "a b" }, wantErr: "host"},
		{name: "host with at", mut: func(_ *testing.T, s *Sandbox) { s.Host = "root@box" }, wantErr: "host"},
		{name: "host with zone", mut: func(_ *testing.T, s *Sandbox) { s.Host = "fe80::1%eth0" }, wantErr: "host"},
		{name: "host with slash", mut: func(_ *testing.T, s *Sandbox) { s.Host = "box/x" }, wantErr: "host"},
		{name: "host with newline", mut: func(_ *testing.T, s *Sandbox) { s.Host = "box\n  ProxyCommand sh" }, wantErr: "host"},
		{name: "host too long", mut: func(_ *testing.T, s *Sandbox) { s.Host = strings.Repeat("a", 254) }, wantErr: "host"},
		{name: "port too big", mut: func(_ *testing.T, s *Sandbox) { s.Port = 65536 }, wantErr: "port"},
		{name: "port negative", mut: func(_ *testing.T, s *Sandbox) { s.Port = -1 }, wantErr: "port"},
		{name: "bad user", mut: func(_ *testing.T, s *Sandbox) { s.User = "-l root" }, wantErr: "user"},
		{name: "empty user", mut: func(_ *testing.T, s *Sandbox) { s.User = "" }, wantErr: "user"},
		{name: "bad auth type", mut: func(_ *testing.T, s *Sandbox) { s.Auth.Type = "password" }, wantErr: "auth"},
		{name: "relative key path", mut: func(_ *testing.T, s *Sandbox) { s.Auth.KeyPath = "id_ed25519" }, wantErr: "key_path"},
		{name: "key path traversal", mut: func(_ *testing.T, s *Sandbox) {
			s.Auth.KeyPath = filepath.Dir(s.Auth.KeyPath) + "/../" + filepath.Base(filepath.Dir(s.Auth.KeyPath)) + "/id_ed25519"
		}, wantErr: "key_path"},
		{name: "key path with quote", mut: func(_ *testing.T, s *Sandbox) { s.Auth.KeyPath = "/tmp/a\"b" }, wantErr: "key_path"},
		{name: "key path with percent token", mut: func(_ *testing.T, s *Sandbox) { s.Auth.KeyPath = "/tmp/%d/id" }, wantErr: "key_path"},
		{name: "missing key", mut: func(_ *testing.T, s *Sandbox) { s.Auth.KeyPath = "/nonexistent/agent-deck/id" }, wantErr: "key_path"},
		{name: "symlinked key", mut: func(t *testing.T, s *Sandbox) { s.Auth.KeyPath = symlinkKey(t) }, wantErr: ": is a symlink"},
		{name: "0644 key", mut: func(t *testing.T, s *Sandbox) { s.Auth.KeyPath = worldReadableKey(t) }, wantErr: "group or other"},
		{name: "key is a directory", mut: func(t *testing.T, s *Sandbox) { s.Auth.KeyPath = t.TempDir() }, wantErr: "regular file"},
		{name: "too many labels", mut: func(_ *testing.T, s *Sandbox) { s.Labels = manyLabels }, wantErr: "labels"},
		{name: "label key with shell metachars", mut: func(_ *testing.T, s *Sandbox) { s.Labels = map[string]string{"a;rm -rf": "v"} }, wantErr: "label"},
		{name: "label value with control char", mut: func(_ *testing.T, s *Sandbox) { s.Labels = map[string]string{"a": "x\x1b[2J"} }, wantErr: "label"},
		{name: "label value too long", mut: func(_ *testing.T, s *Sandbox) { s.Labels = map[string]string{"a": strings.Repeat("v", 257)} }, wantErr: "label"},
		{name: "bad state", mut: func(_ *testing.T, s *Sandbox) { s.State = "exploded" }, wantErr: "state"},
		{name: "host key with newline", mut: func(_ *testing.T, s *Sandbox) { s.HostKeys = []string{"ssh-ed25519 AAAA\n* ssh-rsa AAAA"} }, wantErr: "host_keys"},
		{name: "host key bad base64", mut: func(_ *testing.T, s *Sandbox) { s.HostKeys = []string{"ssh-ed25519 !!!"} }, wantErr: "host_keys"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSandbox(t)
			tt.mut(t, &s)
			err := s.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				if s.Port == 0 {
					t.Fatalf("port not defaulted")
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate accepted %+v", s)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestSandboxValidate_ExpiresAtKept(t *testing.T) {
	s := validSandbox(t)
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	s.ExpiresAt = &at
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}
