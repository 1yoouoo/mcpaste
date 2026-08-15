package config

import (
	"encoding/base64"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsWithRequiredSecrets(t *testing.T) {
	cfg, err := Load(mapLookup(requiredValues()))
	if err != nil {
		t.Fatal("Load() returned an error for valid defaults")
	}
	if cfg.Environment != Development {
		t.Fatalf("environment = %q", cfg.Environment)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTP address = %q", cfg.HTTPAddr)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("log level = %v", cfg.LogLevel)
	}
	if cfg.DatabaseURL == "" {
		t.Fatal("database URL is empty")
	}
	if cfg.ActiveKeyID != "test-key" {
		t.Fatalf("active key ID = %q", cfg.ActiveKeyID)
	}
	if cfg.EncryptionKeys == "" {
		t.Fatal("encryption keyring is empty")
	}
	if cfg.CleanupInterval != time.Minute || cfg.DataDir == "" || len(cfg.TrustedProxyCIDRs) != 0 {
		t.Fatalf("cleanup/data-dir/proxies = %v/%q/%d", cfg.CleanupInterval, cfg.DataDir, len(cfg.TrustedProxyCIDRs))
	}
}

func TestLoadOverrides(t *testing.T) {
	values := requiredValues()
	values["MCPASTE_ENV"] = "production"
	values["MCPASTE_HTTP_ADDR"] = "127.0.0.1:9090"
	values["MCPASTE_LOG_LEVEL"] = "debug"
	values["MCPASTE_CLEANUP_INTERVAL"] = "10m"
	values["MCPASTE_DATA_DIR"] = "/tmp/mcpaste-config-test"
	values["MCPASTE_TRUSTED_PROXY_CIDRS"] = "127.0.0.1/32,10.0.0.0/8"
	cfg, err := Load(mapLookup(values))
	if err != nil {
		t.Fatal("Load() returned an error for valid overrides")
	}
	if cfg.Environment != Production {
		t.Fatalf("environment = %q", cfg.Environment)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" {
		t.Fatalf("HTTP address = %q", cfg.HTTPAddr)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("log level = %v", cfg.LogLevel)
	}
	if cfg.CleanupInterval != 10*time.Minute || len(cfg.TrustedProxyCIDRs) != 2 {
		t.Fatalf("cleanup/proxies = %v/%d", cfg.CleanupInterval, len(cfg.TrustedProxyCIDRs))
	}
}

func TestLoadRejectsInvalidValuesWithoutEchoingSecrets(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "environment", key: "MCPASTE_ENV", value: "staging"},
		{name: "address", key: "MCPASTE_HTTP_ADDR", value: "8080"},
		{name: "log level", key: "MCPASTE_LOG_LEVEL", value: "verbose"},
		{name: "database", key: "MCPASTE_DATABASE_URL", value: "database-secret-marker"},
		{name: "active key", key: "MCPASTE_ACTIVE_KEY_ID", value: "bad key"},
		{name: "keyring", key: "MCPASTE_ENCRYPTION_KEYS", value: "keyring-secret-marker"},
		{name: "cleanup", key: "MCPASTE_CLEANUP_INTERVAL", value: "2s"},
		{name: "proxy", key: "MCPASTE_TRUSTED_PROXY_CIDRS", value: "proxy-secret-marker"},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			values := requiredValues()
			values[item.key] = item.value
			_, err := Load(mapLookup(values))
			if err == nil {
				t.Fatal("Load() error = nil")
			}
			if strings.Contains(err.Error(), item.value) {
				t.Fatal("configuration error echoed the rejected value")
			}
		})
	}
}

func TestLoadRequiresTrustedProxyInProduction(t *testing.T) {
	values := requiredValues()
	values["MCPASTE_ENV"] = "production"
	if _, err := Load(mapLookup(values)); err == nil {
		t.Fatal("production without trusted proxy accepted")
	}
}

func requiredValues() map[string]string {
	key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	return map[string]string{
		"MCPASTE_DATABASE_URL":    "postgres://mcpaste:mcpaste-local-only-not-production@127.0.0.1:55439/mcpaste?sslmode=disable",
		"MCPASTE_ACTIVE_KEY_ID":   "test-key",
		"MCPASTE_ENCRYPTION_KEYS": "test-key:" + key,
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadReadsObjectStorageSettings(t *testing.T) {
	values := requiredValues()
	values["MCPASTE_S3_ENDPOINT"] = "https://mcpaste.sgp1.digitaloceanspaces.com"
	values["MCPASTE_S3_REGION"] = "sgp1"
	values["MCPASTE_S3_BUCKET"] = "mcpaste"
	values["MCPASTE_S3_PREFIX"] = "prod"
	values["MCPASTE_S3_ACCESS_KEY_ID"] = "access"
	values["MCPASTE_S3_SECRET_ACCESS_KEY"] = "secret"

	cfg, err := Load(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ObjectStorageEnabled() {
		t.Fatal("object storage should be enabled when the bucket and credentials are set")
	}
	if cfg.S3.Endpoint != "https://mcpaste.sgp1.digitaloceanspaces.com" || cfg.S3.Region != "sgp1" {
		t.Fatalf("endpoint/region = %q/%q", cfg.S3.Endpoint, cfg.S3.Region)
	}
	if cfg.S3.Bucket != "mcpaste" || cfg.S3.Prefix != "prod" {
		t.Fatalf("bucket/prefix = %q/%q", cfg.S3.Bucket, cfg.S3.Prefix)
	}
}

func TestLoadKeepsObjectStorageDisabledByDefault(t *testing.T) {
	cfg, err := Load(mapLookup(requiredValues()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ObjectStorageEnabled() {
		t.Fatal("object storage must stay disabled without configuration")
	}
}

func TestLoadRejectsPartialObjectStorageSettings(t *testing.T) {
	for _, missing := range []string{
		"MCPASTE_S3_ENDPOINT",
		"MCPASTE_S3_REGION",
		"MCPASTE_S3_BUCKET",
		"MCPASTE_S3_ACCESS_KEY_ID",
		"MCPASTE_S3_SECRET_ACCESS_KEY",
	} {
		values := requiredValues()
		values["MCPASTE_S3_ENDPOINT"] = "https://mcpaste.sgp1.digitaloceanspaces.com"
		values["MCPASTE_S3_REGION"] = "sgp1"
		values["MCPASTE_S3_BUCKET"] = "mcpaste"
		values["MCPASTE_S3_ACCESS_KEY_ID"] = "access"
		values["MCPASTE_S3_SECRET_ACCESS_KEY"] = "secret"
		delete(values, missing)

		if _, err := Load(mapLookup(values)); err == nil {
			t.Fatalf("expected an error when %s is missing", missing)
		}
	}
}

func TestLoadRejectsInsecureObjectStorageEndpointInProduction(t *testing.T) {
	values := requiredValues()
	values["MCPASTE_ENV"] = "production"
	values["MCPASTE_DATA_DIR"] = "/var/lib/mcpaste/data"
	values["MCPASTE_TRUSTED_PROXY_CIDRS"] = "10.0.0.0/8"
	values["MCPASTE_S3_ENDPOINT"] = "http://mcpaste.sgp1.digitaloceanspaces.com"
	values["MCPASTE_S3_REGION"] = "sgp1"
	values["MCPASTE_S3_BUCKET"] = "mcpaste"
	values["MCPASTE_S3_ACCESS_KEY_ID"] = "access"
	values["MCPASTE_S3_SECRET_ACCESS_KEY"] = "secret"

	if _, err := Load(mapLookup(values)); err == nil {
		t.Fatal("expected an error for a plaintext object storage endpoint in production")
	}
}
