package web

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

type allowedHost struct {
	name string
	port string // Empty means any port.
}

// parseHost accepts a single authority, never a URL, wildcard or suffix rule.
func parseHost(value string) (allowedHost, bool) {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "/\\@?#, \t\r\n") {
		return allowedHost{}, false
	}
	var name, port string
	switch {
	case strings.HasPrefix(value, "["):
		if strings.HasSuffix(value, "]") {
			name = value[1 : len(value)-1]
		} else {
			var err error
			name, port, err = net.SplitHostPort(value)
			if err != nil {
				return allowedHost{}, false
			}
		}
	case strings.Count(value, ":") > 1:
		name = value // Unbracketed IPv6 without a port.
	case strings.Contains(value, ":"):
		name, port, _ = strings.Cut(value, ":")
	default:
		name = value
	}
	if name == "" || strings.ContainsAny(name, "[]%") {
		return allowedHost{}, false
	}
	if strings.HasPrefix(value, "[") {
		if ip, err := netip.ParseAddr(name); err != nil || !ip.Is6() {
			return allowedHost{}, false
		}
	}
	if ip, err := netip.ParseAddr(name); err == nil {
		name = ip.Unmap().String()
	} else if strings.Contains(name, ":") || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") {
		return allowedHost{}, false
	} else {
		name = strings.ToLower(name)
		for _, label := range strings.Split(name, ".") {
			if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return allowedHost{}, false
			}
			for _, c := range label {
				if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
					return allowedHost{}, false
				}
			}
		}
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 0 || n > 65535 {
			return allowedHost{}, false
		}
		port = strconv.Itoa(n)
	} else if strings.HasSuffix(value, ":") {
		return allowedHost{}, false
	}
	return allowedHost{name, port}, true
}

func (s *Server) allowHosts(next http.Handler) http.Handler {
	allowed := []allowedHost{{"localhost", ""}, {"127.0.0.1", ""}, {"::1", ""}}
	wildcardBind := false
	if listen, ok := parseHost(s.cfg.ListenAddr); ok {
		allowed = append(allowed, listen)
		if ip, err := netip.ParseAddr(listen.name); err == nil && !ip.IsLoopback() {
			allowed = appendMachineHostname(allowed)
			if !ip.IsUnspecified() {
				allowed = append(allowed, allowedHost{listen.name, ""})
			} else {
				wildcardBind = true
			}
		}
	} else if host, _, err := net.SplitHostPort(s.cfg.ListenAddr); err == nil && host == "" {
		wildcardBind = true
		allowed = appendMachineHostname(allowed)
	}
	if wildcardBind {
		if addrs, err := net.InterfaceAddrs(); err == nil {
			allowed = appendLocalInterfaceIPs(allowed, addrs)
		}
	}
	for _, extra := range s.cfg.AllowedHosts {
		if host, ok := parseHost(extra); ok {
			allowed = append(allowed, host)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, ok := parseHost(r.Host)
		if ok {
			for _, candidate := range allowed {
				if host.name == candidate.name && (candidate.port == "" || host.port == candidate.port) {
					next.ServeHTTP(w, r)
					return
				}
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.Error(w, "unrecognized Host; set [web] allowed_hosts or --allowed-host", http.StatusMisdirectedRequest)
	})
}

func appendMachineHostname(allowed []allowedHost) []allowedHost {
	if hostname, err := os.Hostname(); err == nil {
		if host, ok := parseHost(hostname); ok {
			return append(allowed, allowedHost{host.name, ""})
		}
	}
	return allowed
}

func appendLocalInterfaceIPs(allowed []allowedHost, addrs []net.Addr) []allowedHost {
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || !ipNet.IP.IsGlobalUnicast() {
			continue
		}
		if ip, ok := netip.AddrFromSlice(ipNet.IP); ok {
			allowed = append(allowed, allowedHost{ip.Unmap().String(), ""})
		}
	}
	return allowed
}
