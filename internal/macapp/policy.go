package macapp

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Browser policy (docs/macapp-browser.md, "Policy"). The Mac app is the
// enforcement point; the core cannot be, since an agent on a host controls
// that host's CLI, config and events log. The contract is:
//
//   - A request from a remote host (a session the app reaches over SSH) must
//     never load a loopback, private, link-local (including cloud metadata
//     such as 169.254.169.254), unspecified or multicast address, nor
//     localhost or a .local/.internal name, as the app resolves it. A box
//     page reaching the owner's LAN or this Mac's own services is the risk.
//   - A Browser panel profile is isolated per session: a session's pages
//     never use the owner's own browsing profile, cookies or logins unless
//     the owner turns that on for it in the app. A remote host never picks
//     or switches a panel profile.
//
// CheckRemoteURL is the core's defence in depth for the first rule: the CLI
// on a remote host refuses such addresses before it writes a request. It
// sees only the literal host (no DNS), so it can never replace the app's
// check.

// ErrInternalAddress reports a URL whose host is a loopback, private,
// link-local or otherwise internal address.
var ErrInternalAddress = errors.New("refused: internal address")

// CheckRemoteURL refuses a URL a remote host may not open: an internal host
// by name (localhost, *.localhost, *.local, *.internal), an internal IP
// literal, or a numeric host form a browser reads as an IP (2130706433,
// 0x7f.1, 0177.0.0.1).
func CheckRemoteURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if reason := internalHost(host); reason != "" {
		return fmt.Errorf("%w: %s (%s)", ErrInternalAddress, host, reason)
	}
	return nil
}

func internalHost(host string) string {
	switch {
	case host == "":
		return "no host"
	case host == "localhost" || strings.HasSuffix(host, ".localhost"):
		return "localhost"
	case strings.HasSuffix(host, ".local"):
		return "link-local name"
	case strings.HasSuffix(host, ".internal"):
		return "internal name"
	}
	if ip := net.ParseIP(host); ip != nil {
		switch {
		case ip.IsLoopback():
			return "loopback"
		case ip.IsPrivate():
			return "private network"
		case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
			return "link-local"
		case ip.IsUnspecified():
			return "unspecified"
		case ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
			return "multicast"
		}
		return ""
	}
	if numericHostForm(host) {
		return "numeric host form"
	}
	return ""
}

// numericHostForm reports a host every label of which is a decimal, octal
// (leading 0) or 0x-hex number: browsers read such hosts as IPv4 addresses.
func numericHostForm(host string) bool {
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return false
		}
		digits := label
		hex := strings.HasPrefix(label, "0x")
		if hex {
			digits = label[2:]
		}
		for _, c := range digits {
			if !(c >= '0' && c <= '9' || hex && c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}
