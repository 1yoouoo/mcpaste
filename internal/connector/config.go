package connector

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"github.com/pelletier/go-toml/v2"
)

const configEntryName = "mcpaste"

type ClientConfigOptions struct {
	CommandPath string
	CodexPath   string
	ClaudePath  string
}

type ConfiguredClients struct {
	Names []string `json:"configured_clients"`
}

func ConfigureClients(options ClientConfigOptions) (ConfiguredClients, error) {
	if options.CommandPath == "" {
		return ConfiguredClients{}, errors.New("command path is required")
	}
	commandPath, err := absolutePath(options.CommandPath)
	if err != nil {
		return ConfiguredClients{}, err
	}
	if options.CodexPath == "" {
		options.CodexPath = detectCodexPath()
	}
	if options.ClaudePath == "" {
		options.ClaudePath = detectClaudePath()
	}
	if options.CodexPath != "" {
		if err := ConfigureCodex(options.CodexPath, commandPath); err != nil {
			return ConfiguredClients{}, err
		}
	}
	if options.ClaudePath != "" {
		if err := ConfigureClaude(options.ClaudePath, commandPath); err != nil {
			return ConfiguredClients{}, err
		}
	}
	if options.CodexPath == "" && options.ClaudePath == "" {
		return ConfiguredClients{}, errors.New("no Codex or Claude Code configuration detected")
	}
	configured := ConfiguredClients{Names: make([]string, 0, 2)}
	if options.CodexPath != "" {
		configured.Names = append(configured.Names, "Codex")
	}
	if options.ClaudePath != "" {
		configured.Names = append(configured.Names, "Claude Code")
	}
	return configured, nil
}

func ConfigureCodex(path, commandPath string) error {
	commandPath, err := absolutePath(commandPath)
	if err != nil {
		return err
	}
	root := make(map[string]any)
	if data, readErr := os.ReadFile(path); readErr == nil {
		if err := toml.Unmarshal(data, &root); err != nil {
			return errors.New("parse Codex configuration")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return errors.New("read Codex configuration")
	}
	servers := table(root, "mcp_servers")
	name, found := existingEntryName(servers, commandPath)
	if found {
		entry, ok := servers[name].(map[string]any)
		if ok && canonicalMCPEntry(entry, commandPath) {
			return nil
		}
	} else {
		name = availableEntryName(servers)
	}
	servers[name] = mcpEntry(commandPath)
	root["mcp_servers"] = servers
	data, err := toml.Marshal(root)
	if err != nil {
		return errors.New("write Codex configuration")
	}
	return atomicConfigWrite(path, data)
}

func ConfigureClaude(path, commandPath string) error {
	commandPath, err := absolutePath(commandPath)
	if err != nil {
		return err
	}
	root := make(map[string]any)
	if data, readErr := os.ReadFile(path); readErr == nil {
		if err := json.Unmarshal(data, &root); err != nil {
			return errors.New("parse Claude Code configuration")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return errors.New("read Claude Code configuration")
	}
	servers := table(root, "mcpServers")
	name, found := existingEntryName(servers, commandPath)
	if found {
		entry, ok := servers[name].(map[string]any)
		if ok && canonicalMCPEntry(entry, commandPath) {
			return nil
		}
	} else {
		name = availableEntryName(servers)
	}
	servers[name] = mcpEntry(commandPath)
	root["mcpServers"] = servers
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return errors.New("write Claude Code configuration")
	}
	data = append(data, '\n')
	return atomicConfigWrite(path, data)
}

func detectCodexPath() string {
	if path := os.Getenv("CODEX_CONFIG_PATH"); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(home, ".codex", "config.toml")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

func detectClaudePath() string {
	if path := os.Getenv("CLAUDE_CONFIG_PATH"); path != "" {
		return path
	}
	if configDir := os.Getenv("CLAUDE_CONFIG_DIR"); configDir != "" {
		path := filepath.Join(configDir, ".claude.json")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(home, ".claude.json")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

func absolutePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty command path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("resolve command path")
	}
	return filepath.Clean(abs), nil
}

func table(root map[string]any, key string) map[string]any {
	if value, ok := root[key].(map[string]any); ok {
		return value
	}
	result := make(map[string]any)
	root[key] = result
	return result
}

func mcpEntry(commandPath string) map[string]any {
	return map[string]any{
		"command": commandPath,
		"args":    []any{},
	}
}

func canonicalMCPEntry(entry map[string]any, commandPath string) bool {
	if len(entry) != 2 || entry["command"] != commandPath {
		return false
	}
	args, ok := entry["args"].([]any)
	return ok && len(args) == 0
}

func existingEntryName(servers map[string]any, commandPath string) (string, bool) {
	for name, value := range servers {
		entry, ok := value.(map[string]any)
		if ok && matchingEntry(entry, commandPath) {
			return name, true
		}
	}
	return "", false
}

func matchingEntry(entry map[string]any, commandPath string) bool {
	command, ok := entry["command"].(string)
	return ok && filepath.Clean(command) == filepath.Clean(commandPath)
}

func availableEntryName(servers map[string]any) string {
	if _, exists := servers[configEntryName]; !exists {
		return configEntryName
	}
	for suffix := 2; ; suffix++ {
		name := configEntryName + "-" + strconv.Itoa(suffix)
		if _, exists := servers[name]; !exists {
			return name
		}
	}
}

func atomicConfigWrite(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.New("create client configuration directory")
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("stat client configuration")
	}
	temporary, err := os.CreateTemp(directory, ".mcpaste-config-*")
	if err != nil {
		return errors.New("create client configuration temporary file")
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return errors.New("secure client configuration temporary file")
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return errors.New("write client configuration")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return errors.New("sync client configuration")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close client configuration")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return errors.New("replace client configuration")
	}
	if err := syncDirectory(directory); err != nil {
		return errors.New("sync client configuration directory")
	}
	removeTemporary = false
	return nil
}
