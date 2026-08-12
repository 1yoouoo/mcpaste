package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
)

type Environment string

const (
	Development Environment = "development"
	Test        Environment = "test"
	Production  Environment = "production"
)

type Config struct {
	Environment Environment
	HTTPAddr    string
	LogLevel    slog.Level
}

type LookupEnv func(string) (string, bool)

func LoadOS() (Config, error) {
	return Load(os.LookupEnv)
}

func Load(lookup LookupEnv) (Config, error) {
	cfg := Config{
		Environment: Development,
		HTTPAddr:    ":8080",
		LogLevel:    slog.LevelInfo,
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

	return cfg, nil
}

func nonEmpty(lookup LookupEnv, key string) (string, bool) {
	value, ok := lookup(key)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func validateHTTPAddr(value string) error {
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("must be host:port: %w", err)
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
