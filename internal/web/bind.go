package web

import (
	"fmt"
	"net"
	"strings"
)

// CheckBindSecurity refuses an unauthenticated bind to a non-loopback address.
//
// Authentication is opt-in (an empty token authorizes every request), so a
// non-loopback bind with no token turns the box into an unauthenticated
// remote-code-execution surface (terminal-bridge keystroke injection +
// POST /api/sessions). When that combination is detected and the operator has
// not explicitly acknowledged it via insecureBind, this returns an actionable
// error so the server refuses to start. See /tmp/sec-web-REPORT.md finding #1.
func CheckBindSecurity(listenAddr, token string, insecureBind bool) error {
	if token != "" || insecureBind {
		return nil
	}
	loopback, err := bindIsLoopback(listenAddr)
	if err != nil {
		return fmt.Errorf("invalid --listen address %q: %w", listenAddr, err)
	}
	if loopback {
		return nil
	}
	return fmt.Errorf(
		"refusing to bind %q without an auth token: this exposes an unauthenticated "+
			"remote-code-execution surface (terminal bridge + session-create API) to the network.\n"+
			"  Fix one of:\n"+
			"    - bind loopback only:  --listen 127.0.0.1:8420  (default)\n"+
			"    - set an auth token:   --token <secret>\n"+
			"    - override (unsafe):   --insecure-bind",
		listenAddr,
	)
}

// bindIsLoopback reports whether listenAddr binds to a loopback interface only.
// An empty host (e.g. ":9000") means all interfaces and is NOT loopback.
func bindIsLoopback(listenAddr string) (bool, error) {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return false, err
	}
	host = strings.TrimSpace(host)
	if host == "" {
		// ":9000" / "0.0.0.0:..." style — binds every interface.
		return false, nil
	}
	if strings.EqualFold(host, "localhost") {
		return true, nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A non-localhost hostname we can't classify; treat as non-loopback
		// (fail safe — refuse rather than assume it's local).
		return false, nil
	}
	return ip.IsLoopback(), nil
}

// checkBindSecurity is the server-bound wrapper around CheckBindSecurity used
// as a defense-in-depth gate at Start() time.
func (s *Server) checkBindSecurity() error {
	return CheckBindSecurity(s.cfg.ListenAddr, s.cfg.Token, s.cfg.InsecureBind)
}
