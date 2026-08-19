package connector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestConfigureCodexPreservesUnrelatedServersAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	fixture := "[profiles]\nname = \"keep\"\n\n[mcp_servers.other]\ncommand = \"other-tool\"\nargs = [\"--safe\"]\n\n[mcp_servers.mcpaste]\ncommand = \"/different/mcpaste\"\nargs = [\"--safe\"]\n"
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write Codex fixture: %v", err)
	}
	command := "/opt/mcpaste/bin/mcpaste"
	if err := ConfigureCodex(path, command); err != nil {
		t.Fatalf("ConfigureCodex() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	output := string(data)
	for _, marker := range []string{"name = 'keep'", "command = 'other-tool'", "mcpaste-2", command} {
		if !strings.Contains(output, marker) {
			t.Fatalf("Codex config missing %q: %s", marker, output)
		}
	}
	if strings.Contains(output, "--endpoint") {
		t.Fatalf("Codex config contains an endpoint override: %s", output)
	}
	if strings.Contains(output, "example-token-not-real") {
		t.Fatal("Codex config contains credential")
	}
	before := string(data)
	if err := ConfigureCodex(path, command); err != nil {
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

func TestConfigureCodexMigratesExistingEndpointOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	fixture := "[mcp_servers.other]\ncommand = \"other-tool\"\nargs = [\"--keep\"]\n\n[mcp_servers.mcpaste]\ncommand = \"/opt/mcpaste/bin/mcpaste\"\nargs = [\"--endpoint\", \"https://old.invalid/v1/mcp\", \"--credential-file\", \"/tmp/legacy.json\", \"--safe\"]\nlegacy = true\n\n[mcp_servers.mcpaste.env]\nLEGACY = \"remove\"\n"
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write Codex fixture: %v", err)
	}
	if err := ConfigureCodex(path, "/opt/mcpaste/bin/mcpaste"); err != nil {
		t.Fatalf("ConfigureCodex() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	root := make(map[string]any)
	if err := toml.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	servers := root["mcp_servers"].(map[string]any)
	want := map[string]any{"command": "/opt/mcpaste/bin/mcpaste", "args": []any{}}
	if got := servers["mcpaste"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated Codex entry = %#v, want %#v", got, want)
	}
	other := servers["other"].(map[string]any)
	if got, want := other["args"], []any{"--keep"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unrelated Codex args = %#v, want %#v", got, want)
	}
}

func TestConfigureClaudePreservesUnrelatedServersAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.json")
	fixture := `{"preferences":{"theme":"dark"},"mcpServers":{"other":{"command":"other-tool","args":["--safe"]},"mcpaste":{"command":"/different/mcpaste","args":["--safe"]}}}`
	if err := os.WriteFile(path, []byte(fixture), 0o640); err != nil {
		t.Fatalf("write Claude fixture: %v", err)
	}
	command := "/opt/mcpaste/bin/mcpaste"
	if err := ConfigureClaude(path, command); err != nil {
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
	for _, marker := range []string{`"theme": "dark"`, `"command": "other-tool"`, `"mcpaste-2"`, command} {
		if !strings.Contains(output, marker) {
			t.Fatalf("Claude config missing %q: %s", marker, output)
		}
	}
	if strings.Contains(output, "--endpoint") {
		t.Fatalf("Claude config contains an endpoint override: %s", output)
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
	_, err := ConfigureClients(ClientConfigOptions{CommandPath: "/opt/mcpaste/bin/mcpaste"})
	if err == nil || !strings.Contains(err.Error(), "no Codex or Claude Code configuration") {
		t.Fatalf("ConfigureClients() error = %v", err)
	}
}

func TestConfigureClientsReturnsDeterministicConfiguredNames(t *testing.T) {
	directory := t.TempDir()
	configured, err := ConfigureClients(ClientConfigOptions{
		CommandPath: "/opt/mcpaste/bin/mcpaste",
		CodexPath:   filepath.Join(directory, "codex.toml"),
		ClaudePath:  filepath.Join(directory, "claude.json"),
	})
	if err != nil {
		t.Fatalf("ConfigureClients() error = %v", err)
	}
	if want := []string{"Codex", "Claude Code"}; !reflect.DeepEqual(configured.Names, want) {
		t.Fatalf("configured names = %q, want %q", configured.Names, want)
	}
}

func TestConfigureClaudeMigratesMatchedEntryToCanonical(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.json")
	fixture := `{"mcpServers":{"other":{"command":"other-tool","args":["--keep"]},"mcpaste":{"command":"/opt/mcpaste/bin/mcpaste","args":["--safe","--endpoint=https://old.invalid/v1/mcp","--credential-file","/local/credential.json"],"env":{"LEGACY":"remove"},"unknown":true}}}`
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureClaude(path, "/opt/mcpaste/bin/mcpaste"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	root := make(map[string]any)
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	servers := root["mcpServers"].(map[string]any)
	want := map[string]any{"command": "/opt/mcpaste/bin/mcpaste", "args": []any{}}
	if got := servers["mcpaste"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated Claude entry = %#v, want %#v", got, want)
	}
	other := servers["other"].(map[string]any)
	if got, want := other["args"], []any{"--keep"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unrelated Claude args = %#v, want %#v", got, want)
	}
}
