package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/1yoouoo/mcpaste/internal/secure"
)

type Environment string

const (
	Development Environment = "development"
	Test        Environment = "test"
	Production  Environment = "production"
)

type Config struct {
	Environment       Environment
	HTTPAddr          string
	LogLevel          slog.Level
	DatabaseURL       string
	ActiveKeyID       string
	EncryptionKeys    string
	CleanupInterval   time.Duration
	TrustedProxyCIDRs []*net.IPNet
}

type LookupEnv func(string) (string, bool)

func LoadOS() (Config, error) {
	return Load(os.LookupEnv)
}

func Load(lookup LookupEnv) (Config, error) {
	cfg := Config{
		Environment:     Development,
		HTTPAddr:        ":8080",
		LogLevel:        slog.LevelInfo,
		CleanupInterval: 15 * time.Minute,
	}
	if value, ok := nonEmpty(lookup, "MCPASTE_ENV"); ok {
		switch Environment(value) {
		case Development, Test, Production:
			cfg.Environment = Environment(value)
		default:
			return Config{}, fmt.Errorf("MCPASTE_ENV must be development, test, or production")
		}
	}
	if value, ok := nonEmpty(lookup, "MCPASTE_HTTP_ADDR"); ok {
		cfg.HTTPAddr = value
	}
	if err := validateHTTPAddr(cfg.HTTPAddr); err != nil {
		return Config{}, fmt.Errorf("MCPASTE_HTTP_ADDR: %w", err)
	}
	if value, ok := nonEmpty(lookup, "MCPASTE_LOG_LEVEL"); ok {
		level, err := parseLogLevel(value)
		if err != nil {
			return Config{}, fmt.Errorf("MCPASTE_LOG_LEVEL: %w", err)
		}
		cfg.LogLevel = level
	}
	databaseURL, ok := nonEmpty(lookup, "MCPASTE_DATABASE_URL")
	if !ok || !validDatabaseURL(databaseURL) {
		return Config{}, fmt.Errorf("MCPASTE_DATABASE_URL must be a PostgreSQL URL")
	}
	cfg.DatabaseURL = databaseURL
	activeKeyID, ok := nonEmpty(lookup, "MCPASTE_ACTIVE_KEY_ID")
	if !ok {
		return Config{}, fmt.Errorf("MCPASTE_ACTIVE_KEY_ID is required")
	}
	keyring, ok := nonEmpty(lookup, "MCPASTE_ENCRYPTION_KEYS")
	if !ok {
		return Config{}, fmt.Errorf("MCPASTE_ENCRYPTION_KEYS is required")
	}
	if _, err := secure.ParseKeyring(activeKeyID, keyring, secure.SystemRandom{}); err != nil {
		return Config{}, fmt.Errorf("MCPASTE_ENCRYPTION_KEYS or MCPASTE_ACTIVE_KEY_ID is invalid")
	}
	cfg.ActiveKeyID = activeKeyID
	cfg.EncryptionKeys = keyring
	if value, ok := nonEmpty(lookup, "MCPASTE_CLEANUP_INTERVAL"); ok {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < time.Minute || parsed > time.Hour {
			return Config{}, fmt.Errorf("MCPASTE_CLEANUP_INTERVAL must be from 1m through 1h")
		}
		cfg.CleanupInterval = parsed
	}
	if value, ok := nonEmpty(lookup, "MCPASTE_TRUSTED_PROXY_CIDRS"); ok {
		for _, item := range strings.Split(value, ",") {
			_, network, err := net.ParseCIDR(strings.TrimSpace(item))
			if err != nil {
				return Config{}, fmt.Errorf("MCPASTE_TRUSTED_PROXY_CIDRS contains an invalid CIDR")
			}
			cfg.TrustedProxyCIDRs = append(cfg.TrustedProxyCIDRs, network)
		}
	}
	if cfg.Environment == Production && len(cfg.TrustedProxyCIDRs) == 0 {
		return Config{}, fmt.Errorf("MCPASTE_TRUSTED_PROXY_CIDRS is required in production")
	}
	return cfg, nil
}

func nonEmpty(lookup LookupEnv, key string) (string, bool) {
	value, ok := lookup(key)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func validDatabaseURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
		return false
	}
	return strings.Trim(parsed.Path, "/") != ""
}

func validateHTTPAddr(value string) error {
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("must be host:port")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("port must be an integer from 1 to 65535")
	}
	return nil
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("must be debug, info, warn, or error")
	}
}
