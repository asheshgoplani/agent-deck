package macapp

import (
	"errors"
	"testing"
)

// The core's defence in depth for the app's remote URL policy, one row per
// rule.
func TestCheckRemoteURL(t *testing.T) {
	for _, bad := range []string{
		// names
		"http://localhost/", "http://LOCALHOST.:8080/", "http://app.localhost/", "http://printer.local/", "http://metadata.google.internal/",
		"http://router.lan/", "http://nas.home.arpa/", "http://home.arpa/", "http://mybox.tail1234.ts.net/", "http://router/", "https://box-1/",
		// full-width and ideographic forms a browser maps (UTS-46)
		"http://１２７.０.０.１/", "http://ｌｏｃａｌｈｏｓｔ/", "http://127。0。0。1/", "http://%EF%BC%91%EF%BC%92%EF%BC%97.0.0.1/", "http://xn--/",
		// IPv4
		"http://127.0.0.1/", "http://127.1.2.3:9000/", "http://10.0.0.1/", "http://172.16.5.4/", "http://192.168.1.1/",
		"http://169.254.169.254/latest/meta-data/", "http://0.0.0.0/", "http://0.1.2.3/", "http://224.0.0.1/", "http://255.255.255.255/",
		"http://100.64.0.1/", "http://100.101.102.103/", "http://100.127.255.254/", "http://198.18.0.1/", "http://198.19.255.1/", "http://192.0.0.8/", "http://240.0.0.1/",
		// numeric forms
		"http://2130706433/", "http://0x7f.1/", "http://0177.0.0.1/", "http://0x7f000001/",
		// IPv6
		"http://[::1]/", "http://[::]/", "http://[fe80::1]/", "http://[fe80::1%25en0]/", "http://[fd00::1]/", "http://[ff02::1]/",
		"http://[::ffff:127.0.0.1]/", "http://[::ffff:192.168.1.1]/", "http://[::127.0.0.1]/", "http://[::8.8.8.8]/",
		"http://[64:ff9b::7f00:1]/", "http://[64:ff9b:1::1]/",
	} {
		if err := CheckRemoteURL(bad); !errors.Is(err, ErrInternalAddress) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	for _, ok := range []string{"https://example.com/", "http://93.184.216.34/", "https://[2606:2800:220:1::]/", "https://cafe.be/", "https://123.example/", "http://100.128.0.1/", "http://198.20.0.1/", "https://bücher.example/", "https://ＥＸＡＭＰＬＥ.com/"} {
		if err := CheckRemoteURL(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
}

// Only absolute http(s) URLs with a host open, whatever else has a host.
func TestValidateURLRefusesOtherSchemesWithAHost(t *testing.T) {
	for _, bad := range []string{"ftp://h/", "javascript://x/%0aalert(1)", "file://host/etc/passwd", "data://x/,hi", "ws://h/", "HTTPS://user@h/"} {
		if _, err := ValidateURL(bad); err == nil || errors.Is(err, ErrNotURL) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if _, err := ValidateURL("HTTPS://example.com/"); err != nil {
		t.Errorf("upper-case scheme refused: %v", err)
	}
}
