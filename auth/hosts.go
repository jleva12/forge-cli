package auth

import (
	"net"
	"net/url"
	"strings"
)

// HostAllowed reports whether u's host is one of hosts. Hosts may be bare
// ("api.example.com", "localhost:8080") or URLs; default ports are ignored.
func HostAllowed(u *url.URL, hosts []string) bool {
	want := HostKey(u)
	for _, h := range hosts {
		if NormalizeHost(h) == want {
			return true
		}
	}
	return false
}

// HostKey normalizes a URL's host: lower case, default port removed.
func HostKey(u *url.URL) string {
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

// NormalizeHost accepts a URL or a bare host and returns its HostKey, or ""
// if it has no host.
func NormalizeHost(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	return HostKey(u)
}
