package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterPreservesClientConfigurationAndPrintsOnlyNames(t *testing.T) {
	directory := t.TempDir()
	codexPath := filepath.Join(directory, "codex.toml")
	claudePath := filepath.Join(directory, "claude.json")
	commandPath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	codex := "[profiles]\nname = \"keep\"\n\n[mcp_servers.other]\ncommand = \"other-tool\"\nargs = [\"--safe\"]\n\n[mcp_servers.mcpaste]\ncommand = \"" + commandPath + "\"\nargs = [\"--endpoint\", \"https://old.invalid/v1/mcp\", \"--credential-file\", \"/local/credential.json\"]\n"
	claude := `{"preferences":{"theme":"dark"},"mcpServers":{"other":{"command":"other-tool","args":["--safe"]},"mcpaste":{"command":` + quotedJSON(commandPath) + `,"args":["--endpoint=https://old.invalid/v1/mcp","--credential-file","/local/credential.json"]}}}`
	if err := os.WriteFile(codexPath, []byte(codex), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudePath, []byte(claude), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runRegister([]string{"--codex-config", codexPath, "--claude-config", claudePath}, &output); err != nil {
		t.Fatalf("runRegister() error = %v", err)
	}
	if got, want := output.String(), "{\"configured_clients\":[\"Codex\",\"Claude Code\"]}\n"; got != want {
		t.Fatalf("register output = %q, want %q", got, want)
	}
	for _, test := range []struct {
		path    string
		markers []string
	}{
		{path: codexPath, markers: []string{"name = 'keep'", "command = 'other-tool'", "args = []"}},
		{path: claudePath, markers: []string{`"theme": "dark"`, `"command": "other-tool"`, `"args": []`}},
	} {
		data, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range test.markers {
			if !strings.Contains(string(data), marker) {
				t.Fatalf("%s missing %q: %s", test.path, marker, data)
			}
		}
		for _, legacy := range []string{"--endpoint", "old.invalid", "--credential-file", "/local/credential.json"} {
			if strings.Contains(string(data), legacy) {
				t.Fatalf("%s retained legacy MCPaste field %q: %s", test.path, legacy, data)
			}
		}
	}
}

func TestRegisterPreservesNoClientError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_CONFIG_PATH", "")
	t.Setenv("CLAUDE_CONFIG_PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	var output bytes.Buffer
	err := runRegister(nil, &output)
	if err == nil || !strings.Contains(err.Error(), "no Codex or Claude Code configuration detected") {
		t.Fatalf("runRegister() error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("runRegister() output on error = %q", output.String())
	}
}

func TestRegisterExposesOnlyClientConfigFlags(t *testing.T) {
	for _, args := range [][]string{{"--credential-file", "/tmp/credential.json"}, {"--endpoint", "https://example.test"}, {"extra"}} {
		if err := runRegister(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("runRegister(%q) unexpectedly succeeded", args)
		}
	}
}

func quotedJSON(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}
