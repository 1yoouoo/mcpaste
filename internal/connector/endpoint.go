package connector

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

func ValidateEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Path != "/v1/mcp" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("invalid MCP endpoint")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname()) {
		return errors.New("MCP endpoint must use HTTPS")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
