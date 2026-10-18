package macapp

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
)

// Browser policy (docs/macapp-browser.md, "Policy"). The Mac app is the
// enforcement point; the core cannot be, since an agent on a host controls
// that host's CLI, config and events log. The contract is:
//
//   - A request from a remote host (a session the app reaches over SSH) must
//     never load, as the app resolves the name:
//   - loopback (127.0.0.0/8, ::1), private (10/8, 172.16/12, 192.168/16,
//     fc00::/7), link-local (169.254/16 incl. cloud metadata
//     169.254.169.254, fe80::/10), unspecified and "this network"
//     (0.0.0.0/8, ::), multicast and broadcast (224/4, 255.255.255.255,
//     ff00::/8), reserved (240/4), benchmarking (198.18.0.0/15) or IETF
//     protocol (192.0.0.0/24) addresses;
//   - CGNAT 100.64.0.0/10, which tailnets (Tailscale and the like) use for
//     their nodes: a box page must not reach the owner's other machines
//     over the tailnet;
//   - IPv4-mapped (::ffff:0:0/96), IPv4-compatible (::/96) and NAT64
//     (64:ff9b::/96, 64:ff9b:1::/48) IPv6 forms, and any address with a
//     zone ID;
//   - localhost and *.localhost, *.local, *.lan, *.internal, home.arpa,
//     *.ts.net (tailnet MagicDNS) names, and single-label names (a LAN or
//     tailnet machine name).
//     A box page reaching the owner's LAN, tailnet or this Mac's own
//     services is the risk.
//   - A Browser panel profile is isolated per session: a session's pages
//     never use the owner's own browsing profile, cookies or logins unless
//     the owner turns that on for it in the app. A remote host never picks
//     or switches a panel profile.
//
// CheckRemoteURL is the core's defence in depth for the first rule: the CLI
// on a remote host refuses such hosts before it writes a request. It maps
// the host the way a browser does (UTS-46: full-width digits and letters,
// ideographic full stops) and sees only the literal host (no DNS), so it can
// never replace the app's check.

// ErrInternalAddress reports a URL whose host is a loopback, private,
// link-local, tailnet or otherwise internal address or name.
var ErrInternalAddress = errors.New("refused: internal address")

// hostMapping is the browser's host mapping (UTS-46, as in URL parsing).
var hostMapping = idna.New(idna.MapForLookup(), idna.BidiRule(), idna.Transitional(false))

// CheckRemoteURL refuses a URL a remote host may not open (see the policy
// above). A host that cannot be mapped is refused too.
func CheckRemoteURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host, reason := normalizeHost(u.Hostname())
	if reason == "" {
		reason = internalHost(host)
	}
	if reason != "" {
		return fmt.Errorf("%w: %s (%s)", ErrInternalAddress, u.Hostname(), reason)
	}
	return nil
}

// normalizeHost maps a URL host as a browser would: percent-decoding, then
// UTS-46 (full-width forms, ideographic full stops, case), then a trailing
// dot dropped. An IP literal is returned as is. A non-empty reason refuses.
func normalizeHost(host string) (string, string) {
	decoded, err := url.PathUnescape(host)
	if err != nil {
		return "", "undecodable host"
	}
	if strings.Contains(decoded, "%") {
		return "", "zone ID or escaped host"
	}
	if strings.Contains(decoded, ":") {
		// IPv6 literal (Hostname dropped the brackets).
		if _, err := netip.ParseAddr(decoded); err != nil {
			return "", "unparsable IPv6 literal"
		}
		return decoded, ""
	}
	mapped, err := hostMapping.ToASCII(decoded)
	if err != nil {
		return "", "host does not map to a valid name"
	}
	return strings.TrimSuffix(strings.ToLower(mapped), "."), ""
}

var refusedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
		"::/96", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "fc00::/7", "fe80::/10", "ff00::/8", "::1/128",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

func internalHost(host string) string {
	switch {
	case host == "":
		return "no host"
	case host == "localhost" || strings.HasSuffix(host, ".localhost"):
		return "localhost"
	case host == "home.arpa" || strings.HasSuffix(host, ".home.arpa"):
		return "home network name"
	case strings.HasSuffix(host, ".ts.net"):
		return "tailnet name"
	case strings.HasSuffix(host, ".local"), strings.HasSuffix(host, ".lan"):
		return "LAN name"
	case strings.HasSuffix(host, ".internal"):
		return "internal name"
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return "zone ID"
		}
		for _, p := range refusedPrefixes {
			if p.Contains(addr) {
				return "internal range " + p.String()
			}
		}
		if ip := net.IP(addr.AsSlice()); ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() || ip.Equal(net.IPv4bcast) {
			return "internal address"
		}
		return ""
	}
	if numericHostForm(host) {
		return "numeric host form"
	}
	if !strings.Contains(host, ".") {
		return "single-label name"
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
