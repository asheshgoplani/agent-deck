package openclaw

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithAllowInsecure lets the client send the gateway password over plain
// ws:// to any host (the --insecure flag / allow_insecure setting).
func WithAllowInsecure(allow bool) ClientOption {
	return func(c *Client) { c.allowInsecure = allow }
}

// Address ranges a plain ws:// gateway may use without --insecure: this
// machine and the Tailscale tailnet, whose traffic is already encrypted.
var (
	tailnetIPv4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnetIPv6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// CheckGatewayURL refuses a ws:// gateway URL whose host is neither loopback
// (127.0.0.0/8, ::1, localhost) nor tailnet (100.64.0.0/10,
// fd7a:115c:a1e0::/48, *.ts.net), because the password is sent in the clear.
// wss:// is always allowed, and allowInsecure turns the check off.
func CheckGatewayURL(rawURL string, allowInsecure bool) error {
	if allowInsecure {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "ws" {
		// wss://, or something the dialer will reject on its own.
		return nil
	}
	host := u.Hostname()
	if plainWSHostAllowed(host) {
		return nil
	}
	return fmt.Errorf("openclaw: refusing to send the gateway password over unencrypted ws:// to %s; "+
		"use wss://, a loopback or tailnet address, or pass --insecure", host)
}

func plainWSHostAllowed(host string) bool {
	name := strings.ToLower(strings.TrimSuffix(host, "."))
	if name == "localhost" || strings.HasSuffix(name, ".ts.net") {
		return true
	}
	addr, err := netip.ParseAddr(name)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLoopback() || tailnetIPv4.Contains(addr) || tailnetIPv6.Contains(addr)
}
