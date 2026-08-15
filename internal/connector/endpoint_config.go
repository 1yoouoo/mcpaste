package connector

import (
	"errors"
	"net/url"
	"strings"
)

// BuildEndpoint is populated in release builds with Go's -ldflags -X option.
// It intentionally has no hosted-service default in source.
var BuildEndpoint string

func ValidateBaseEndpoint(endpoint string) error {
	if endpoint == "" || strings.HasSuffix(endpoint, "/") || strings.ContainsAny(endpoint, "?#") {
		return errors.New("invalid MCPaste base endpoint")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" || parsed.Opaque != "" {
		return errors.New("invalid MCPaste base endpoint")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()) {
		return nil
	}
	return errors.New("MCPaste base endpoint must use HTTPS")
}

func ConfiguredBaseEndpoint() (string, error) {
	if err := ValidateBaseEndpoint(BuildEndpoint); err != nil {
		return "", err
	}
	return BuildEndpoint, nil
}

func ConfiguredMCPEndpoint() (string, error) {
	base, err := ConfiguredBaseEndpoint()
	if err != nil {
		return "", err
	}
	return base + "/v1/mcp", nil
}

func ValidateConfiguredEndpoint(endpoint string) error {
	expected, err := ConfiguredMCPEndpoint()
	if err != nil {
		return errors.New("MCPaste endpoint is not configured")
	}
	if endpoint != expected {
		return errors.New("credential endpoint does not match configured endpoint")
	}
	return nil
}
