package openclaw

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The gateway password is sent in the first frame after the handshake, so a
// plain ws:// URL must only point at this machine or the tailnet unless the
// user opts out.

func TestCheckGatewayURL(t *testing.T) {
	tests := []struct {
		url      string
		insecure bool
		wantErr  bool
	}{
		{"ws://127.0.0.1:31337", false, false},
		{"ws://127.8.9.10:31337", false, false},
		{"ws://localhost:31337", false, false},
		{"ws://LOCALHOST:31337", false, false},
		{"ws://[::1]:31337", false, false},
		{"ws://100.64.0.1:31337", false, false},
		{"ws://100.127.255.254:31337", false, false},
		{"ws://[fd7a:115c:a1e0::1]:31337", false, false},
		{"ws://gateway.tail1234.ts.net:31337", false, false},
		{"wss://gateway.example.com", false, false},
		{"wss://192.168.1.50:31337", false, false},
		{"ws://192.168.1.50:31337", false, true},
		{"ws://10.0.0.5:31337", false, true},
		{"ws://100.128.0.1:31337", false, true}, // just outside 100.64.0.0/10
		{"ws://gateway.example.com", false, true},
		{"ws://ts.net.example.com", false, true},
		{"ws://[fd7a:115c:a1e1::1]:31337", false, true},
		{"ws://192.168.1.50:31337", true, false},
		{"ws://gateway.example.com", true, false},
	}
	for _, tt := range tests {
		err := CheckGatewayURL(tt.url, tt.insecure)
		if (err != nil) != tt.wantErr {
			t.Errorf("CheckGatewayURL(%q, insecure=%v) error = %v, wantErr %v", tt.url, tt.insecure, err, tt.wantErr)
		}
	}
}

func TestCheckGatewayURL_ExactMessage(t *testing.T) {
	err := CheckGatewayURL("ws://192.168.1.50:31337", false)
	want := "openclaw: refusing to send the gateway password over unencrypted ws:// to 192.168.1.50; " +
		"use wss://, a loopback or tailnet address, or pass --insecure"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v\nwant    %s", err, want)
	}
}

// Connect enforces the policy itself, so every caller (CLI, bridge TUI,
// reconnect loop) is covered, and refuses before any dial.
func TestConnect_RefusesPlainWSToLANHost(t *testing.T) {
	c := NewClient("ws://192.0.2.10:31337", "pw")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	err := c.Connect(ctx)
	if err == nil || !strings.Contains(err.Error(), "refusing to send the gateway password") {
		t.Fatalf("Connect error = %v, want the plain ws:// refusal", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("Connect took %s: it dialed before refusing", time.Since(start))
	}

	c = NewClient("ws://192.0.2.10:31337", "pw", WithAllowInsecure(true))
	if err := CheckGatewayURL(c.url, c.allowInsecure); err != nil {
		t.Errorf("WithAllowInsecure(true) client still refused: %v", err)
	}
}
