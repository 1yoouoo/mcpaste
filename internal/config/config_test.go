package config

import (
	"log/slog"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(mapLookup(nil))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Environment != Development {
		t.Fatalf("Environment = %q, want %q", cfg.Environment, Development)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelInfo)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(mapLookup(map[string]string{
		"MCPASTE_ENV":       "production",
		"MCPASTE_HTTP_ADDR": "127.0.0.1:9090",
		"MCPASTE_LOG_LEVEL": "debug",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Environment != Production {
		t.Fatalf("Environment = %q, want %q", cfg.Environment, Production)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" {
		t.Fatalf("HTTPAddr = %q, want 127.0.0.1:9090", cfg.HTTPAddr)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelDebug)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]string
		wantErr string
	}{
		{
			name:    "environment",
			values:  map[string]string{"MCPASTE_ENV": "staging"},
			wantErr: "MCPASTE_ENV",
		},
		{
			name:    "address",
			values:  map[string]string{"MCPASTE_HTTP_ADDR": "8080"},
			wantErr: "MCPASTE_HTTP_ADDR",
		},
		{
			name:    "port",
			values:  map[string]string{"MCPASTE_HTTP_ADDR": ":70000"},
			wantErr: "MCPASTE_HTTP_ADDR",
		},
		{
			name:    "log level",
			values:  map[string]string{"MCPASTE_LOG_LEVEL": "verbose"},
			wantErr: "MCPASTE_LOG_LEVEL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(mapLookup(tt.values))
			if err == nil {
				t.Fatal("Load() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
