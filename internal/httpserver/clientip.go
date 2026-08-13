package httpserver

import (
	"net"
	"net/http"
	"strings"
)

func clientIP(r *http.Request, trusted []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote := net.ParseIP(host)
	if remote == nil || !inNetworks(remote, trusted) {
		return host
	}
	values := r.Header.Values("X-Forwarded-For")
	if len(values) != 1 {
		return host
	}
	parts := strings.Split(values[0], ",")
	for index := len(parts) - 1; index >= 0; index-- {
		candidate := net.ParseIP(strings.TrimSpace(parts[index]))
		if candidate == nil {
			return host
		}
		if !inNetworks(candidate, trusted) {
			return candidate.String()
		}
	}
	return host
}

func inNetworks(ip net.IP, networks []*net.IPNet) bool {
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
