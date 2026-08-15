package connector

import "testing"

func TestConfiguredBaseEndpointRequiresHTTPSOrigin(t *testing.T) {
	original := BuildEndpoint
	t.Cleanup(func() { BuildEndpoint = original })

	for _, endpoint := range []string{
		"",
		"http://example.test",
		"https://example.test/",
		"https://example.test/path",
		"https://example.test?query=not-allowed",
		"https://example.test#fragment-not-allowed",
		"https://user:pass@example.test",
		"not an endpoint",
	} {
		BuildEndpoint = endpoint
		if _, err := ConfiguredBaseEndpoint(); err == nil {
			t.Fatalf("ConfiguredBaseEndpoint(%q) unexpectedly succeeded", endpoint)
		}
	}

	BuildEndpoint = "https://example.test"
	base, err := ConfiguredBaseEndpoint()
	if err != nil || base != "https://example.test" {
		t.Fatalf("ConfiguredBaseEndpoint() = %q/%v", base, err)
	}
	mcpEndpoint, err := ConfiguredMCPEndpoint()
	if err != nil || mcpEndpoint != "https://example.test/v1/mcp" {
		t.Fatalf("ConfiguredMCPEndpoint() = %q/%v", mcpEndpoint, err)
	}
}

func TestValidateConfiguredEndpointRequiresExactBuildEndpoint(t *testing.T) {
	original := BuildEndpoint
	t.Cleanup(func() { BuildEndpoint = original })
	BuildEndpoint = "https://example.test"

	if err := ValidateConfiguredEndpoint("https://example.test/v1/mcp"); err != nil {
		t.Fatalf("ValidateConfiguredEndpoint() error = %v", err)
	}
	for _, endpoint := range []string{
		"https://other.example/v1/mcp",
		"http://example.test/v1/mcp",
		"https://example.test/v1/mcp/",
	} {
		if err := ValidateConfiguredEndpoint(endpoint); err == nil {
			t.Fatalf("ValidateConfiguredEndpoint(%q) unexpectedly succeeded", endpoint)
		}
	}
}
