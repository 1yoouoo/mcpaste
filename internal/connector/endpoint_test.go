package connector

import "testing"

func TestValidateEndpointRequiresHTTPSOutsideLoopback(t *testing.T) {
	for _, endpoint := range []string{
		"http://example.com/v1/mcp",
		"http://192.0.2.10/v1/mcp",
		"https://example.com/v1/mcp?token=not-allowed",
		"https://user:pass@example.com/v1/mcp",
	} {
		if err := ValidateEndpoint(endpoint); err == nil {
			t.Fatalf("ValidateEndpoint(%q) unexpectedly succeeded", endpoint)
		}
	}
	for _, endpoint := range []string{
		"http://127.0.0.1:8080/v1/mcp",
		"http://[::1]:8080/v1/mcp",
		"http://localhost:8080/v1/mcp",
		"https://example.com/v1/mcp",
	} {
		if err := ValidateEndpoint(endpoint); err != nil {
			t.Fatalf("ValidateEndpoint(%q) error = %v", endpoint, err)
		}
	}
}
