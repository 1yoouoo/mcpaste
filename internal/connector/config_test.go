package connector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureCodexPreservesUnrelatedServersAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	fixture := "[profiles]\nname = \"keep\"\n\n[mcp_servers.other]\ncommand = \"other-tool\"\nargs = [\"--safe\"]\n\n[mcp_servers.mcpaste]\ncommand = \"/different/mcpaste\"\nargs = [\"--endpoint\", \"https://old.invalid/v1/mcp\"]\n"
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write Codex fixture: %v", err)
	}
	command := "/opt/mcpaste/bin/mcpaste"
	endpoint := "https://example.invalid/v1/mcp"
	if err := ConfigureCodex(path, command, endpoint); err != nil {
		t.Fatalf("ConfigureCodex() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	output := string(data)
	for _, marker := range []string{"name = 'keep'", "command = 'other-tool'", "mcpaste-2", command, endpoint} {
		if !strings.Contains(output, marker) {
			t.Fatalf("Codex config missing %q: %s", marker, output)
		}
	}
	if strings.Contains(output, "example-token-not-real") {
		t.Fatal("Codex config contains credential")
	}
	before := string(data)
	if err := ConfigureCodex(path, command, endpoint); err != nil {
		t.Fatalf("idempotent ConfigureCodex() error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read idempotent Codex config: %v", err)
	}
	if string(after) != before {
		t.Fatal("idempotent Codex configuration changed bytes")
	}
}

func TestConfigureClaudePreservesUnrelatedServersAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.json")
	fixture := `{"preferences":{"theme":"dark"},"mcpServers":{"other":{"command":"other-tool","args":["--safe"]},"mcpaste":{"command":"/different/mcpaste","args":["--endpoint","https://old.invalid/v1/mcp"]}}}`
	if err := os.WriteFile(path, []byte(fixture), 0o640); err != nil {
		t.Fatalf("write Claude fixture: %v", err)
	}
	command := "/opt/mcpaste/bin/mcpaste"
	endpoint := "https://example.invalid/v1/mcp"
	if err := ConfigureClaude(path, command, endpoint); err != nil {
		t.Fatalf("ConfigureClaude() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat Claude config: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("Claude mode = %o, want 640", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Claude config: %v", err)
	}
	output := string(data)
	for _, marker := range []string{`"theme": "dark"`, `"command": "other-tool"`, `"mcpaste-2"`, command, endpoint} {
		if !strings.Contains(output, marker) {
			t.Fatalf("Claude config missing %q: %s", marker, output)
		}
	}
	if strings.Contains(output, "example-token-not-real") {
		t.Fatal("Claude config contains credential")
	}
}

func TestConfigureClientsRejectsMissingClientConfigurations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_CONFIG_PATH", "")
	t.Setenv("CLAUDE_CONFIG_PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	err := ConfigureClients(ClientConfigOptions{CommandPath: "/opt/mcpaste/bin/mcpaste", Endpoint: "https://example.invalid/v1/mcp"})
	if err == nil || !strings.Contains(err.Error(), "no Codex or Claude Code configuration") {
		t.Fatalf("ConfigureClients() error = %v", err)
	}
}
