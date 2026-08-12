# MCPaste Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish a security-conscious MCPaste repository baseline and a tested, containerized Go server skeleton with health endpoints and CI.

**Architecture:** This phase creates one standard-library Go HTTP process with validated environment configuration, metadata-only access logging, and separate liveness/readiness routes. It intentionally excludes PostgreSQL, authentication, paste data, and MCP so the repository and deployment unit can be verified before stateful behavior is introduced.

**Tech Stack:** Go 1.26.5, Go standard library (`net/http`, `log/slog`), Docker, GitHub Actions, Gitleaks, Govulncheck

---

## Preconditions

The implementation worker must stop before Task 3 if `go version` does not report Go 1.26.5. On the current machine Go is not installed. The owner can install it with Homebrew:

```bash
brew install go
go version
```

Expected output contains:

```text
go version go1.26.5 darwin/arm64
```

Docker 28.2.2 is already available. Full Xcode is not required for this phase. Do not create a Droplet, deploy, push, rename a GitHub repository, or add an `origin` remote while executing this plan.

## File map

Files created or modified in this phase have one responsibility each:

- `README.md`: public product status, boundaries, architecture, and developer entry points.
- `SECURITY.md`: contributor and vulnerability-reporting policy for a secret-bearing service.
- `docs/security-and-secrets.md`: concrete local and production secret placement rules.
- `.env.example`: non-sensitive environment variable names used by the foundation server.
- `.gitignore`: excludes local config, credentials, databases, app data, IDE output, and binaries.
- `.go-version`: pins the local and CI Go patch release.
- `go.mod`: declares the MCPaste Go module and language version.
- `internal/config/config.go`: parses and validates process configuration.
- `internal/config/config_test.go`: proves defaults, overrides, and invalid-input behavior.
- `internal/httpserver/health.go`: owns liveness/readiness HTTP behavior.
- `internal/httpserver/health_test.go`: proves health status, method, and error-redaction behavior.
- `internal/httpserver/logging.go`: metadata-only access logging middleware.
- `internal/httpserver/logging_test.go`: proves headers, queries, and request bodies are never logged.
- `cmd/server/main.go`: process startup, signal handling, timeouts, and graceful shutdown.
- `.dockerignore`: limits the container build context.
- `Dockerfile`: builds a static, non-root server container.
- `.github/workflows/ci.yml`: pull-request and `main` quality gates; no deployment.

### Task 1: Rename and reconcile public project documentation

**Files:**

- Modify: `README.md`
- Modify: `SECURITY.md`

- [ ] **Step 1: Replace `README.md` with the approved MCPaste boundary**

```markdown
# MCPaste

MCPaste is a macOS menu bar app for deliberately handing plain text or static images to AI coding tools through Model Context Protocol (MCP).

> Status: early open-source development. The approved design exists, but the app, service, and MCP connector are not released.

## Product boundary

- MCPaste never monitors or automatically saves the system clipboard.
- A full macOS app is the supported interface for creating, editing, and deleting content.
- Codex, Claude Code, and headless Linux companions receive read-only MCP access.
- A central service is the source of truth across connected devices.
- Text and images may contain credentials or personal data and are sensitive by default.

## Trust model

MCPaste is not end-to-end encrypted. The service decrypts authorized content before returning it to a connected device. TLS protects data in transit, and application-level encryption protects stored bodies, but the service operator or an attacker with full production-server access can read stored content.

After MCP returns content, the connected AI client and model provider may process or retain it under their own terms. MCPaste cannot enforce downstream retention.

Read the complete rules in [Security and secrets](docs/security-and-secrets.md) and report vulnerabilities according to [SECURITY.md](SECURITY.md).

## Planned architecture

```text
Full Mac app ──HTTPS write/sync──▶ MCPaste service ──▶ PostgreSQL/files

Codex / Claude Code ──STDIO──▶ mcpaste connector
                                  └──Streamable HTTP MCP──▶ MCPaste service
```

The production MVP targets one cost-conscious DigitalOcean Droplet running Caddy, the Go service, and PostgreSQL. The native app uses SwiftUI; the service and connector use Go.

## Development

The first executable phase contains the Go server skeleton. Its prerequisites are:

- the Go version in `.go-version`;
- Docker for container verification;
- full Xcode only when macOS app implementation begins.

After Go is installed:

```sh
go test ./...
go run ./cmd/server
```

Never put real credentials or paste samples in environment files, fixtures, logs, screenshots, issues, or pull requests. Use visibly fake values such as `example-token-not-real`.

## Design and implementation plans

- [Approved system design](docs/superpowers/specs/2026-08-12-mcpaste-system-design.md)
- [Implementation roadmap](docs/superpowers/plans/2026-08-12-mcpaste-roadmap.md)
```

- [ ] **Step 2: Replace `SECURITY.md` with the remote-capable policy**

```markdown
# Security Policy

MCPaste intentionally transports arbitrary text and static images to MCP clients. Assume every paste may contain credentials, access tokens, private customer data, or other sensitive material.

## Current status

MCPaste is in early development and has no supported public release. Security behavior described in design documents is a requirement, not a guarantee that unimplemented code already provides it.

## Contributor rules

- Never commit real credentials, cookies, private keys, recovery codes, pairing codes, personal data, or unredacted paste content.
- Never include sensitive values in `.env.example`, fixtures, snapshots, test output, logs, screenshots, issues, or pull requests.
- Use deterministic, visibly fake values such as `example-token-not-real`.
- Never log request or response bodies, authorization headers, cookies, QR payloads, or secret-bearing URLs.
- Keep generated Codex, Claude Code, Keychain, and Linux credential-store data outside the repository.
- Treat production encryption keys, database credentials, Apple signing material, and deployment SSH keys as server or CI secrets with the narrowest possible scope.

## Trust boundary

The planned service is not end-to-end encrypted. The production server can decrypt authorized content. TLS and encryption at rest do not protect against a malicious operator or complete production-server compromise.

MCPaste controls access up to the MCP response. It does not control retention by Codex, Claude Code, a model provider, terminal capture, or other software after content is returned.

## If a secret is exposed

1. Revoke or rotate the secret immediately. Deleting a Git line or rewriting history is not sufficient.
2. Do not copy the secret into an issue, pull request, chat, or incident report.
3. Record only the affected file or commit, secret type, exposure window, and rotation status.
4. Use GitHub private vulnerability reporting or contact the maintainer privately.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub private vulnerability reporting when enabled. Include reproduction steps, impact, affected version or commit, and a suggested mitigation without including live secrets or private paste content.
```

- [ ] **Step 3: Verify obsolete branding and local-only claims are gone from public entry documents**

Run:

```bash
rg -n 'Paste Bridge|PASTE_BRIDGE|local-only|local MVP|future opt-in' README.md SECURITY.md
```

Expected: no output and exit status 1.

- [ ] **Step 4: Check the documentation diff**

Run:

```bash
git add -N README.md SECURITY.md
git diff --check -- README.md SECURITY.md
git diff -- README.md SECURITY.md
```

Expected: the intent-to-add command makes the currently untracked files visible to diff without staging their contents; the check prints nothing; the diff contains only the two complete documentation replacements above.

- [ ] **Step 5: Commit the public documentation**

```bash
git add README.md SECURITY.md
git commit -m "docs: rename project to MCPaste"
```

Expected: one commit containing only `README.md` and `SECURITY.md`.

### Task 2: Define local and production secret placement

**Files:**

- Modify: `docs/security-and-secrets.md`
- Modify: `.env.example`
- Modify: `.gitignore`

- [ ] **Step 1: Replace `docs/security-and-secrets.md` with the central-service rules**

```markdown
# Security and secrets management

This document defines where MCPaste data belongs during development and production. Paste content is application data, never configuration.

## Data placement

| Data | Correct location | Forbidden locations |
| --- | --- | --- |
| Paste text and images | App cache; encrypted production database or image volume | Git, environment files, logs, screenshots, issues, pull requests |
| Full Mac credentials | macOS Keychain | App bundle, repository, user defaults, logs |
| Linux connector credential | System credential store or a user-owned `0600` file | Shell history, URL, repository, world-readable config |
| Production encryption key | `/etc/mcpaste/server.env`, root-owned mode `0600` | GitHub Actions, image, app, connector, repository |
| Database/session secrets | `/etc/mcpaste/server.env`, root-owned mode `0600` | Public Compose files, CI logs, client apps |
| Deployment SSH key | Protected GitHub production environment | Repository, Droplet image, pull-request jobs |
| Apple signing material | Keychain and protected release secrets | Repository, ordinary build logs, app resources |

## Local development

Copy the safe template:

```sh
cp .env.example .env.local
```

Foundation development uses only non-sensitive server settings. Later stateful plans must use isolated local credentials that are not reused anywhere else. Never put paste samples in `.env.local`.

Before every commit:

```sh
git diff --cached
rg -n '(sk-[A-Za-z0-9]{12,}|ghp_[A-Za-z0-9]{12,}|AKIA[0-9A-Z]{16}|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY)' .
```

The pattern check is a supplement to CI secret scanning, not proof that a diff is safe.

## Production server

The production Go container receives server-only variables from `/etc/mcpaste/server.env`. The file is created directly on the Droplet, owned by root, and set to mode `0600`. It is never copied into a container image or sent through GitHub Actions.

Caddy exposes only HTTPS application traffic. PostgreSQL has no public port. Authorization values use headers, never URL query strings.

## Logs

Allowed log fields are timestamp, request ID, method, route path, status, duration, object ID, size, and count. Logs must exclude request and response bodies, query strings, authorization headers, cookies, device credentials, recovery codes, pairing codes, QR payloads, and image bytes.

Error handling must avoid body capture by construction. Redaction after logging is not an acceptable primary control.

## Incident response

If a credential reaches Git, logs, screenshots, or an untrusted client, rotate or revoke it first. Then remove stored copies and investigate scope. History cleanup cannot restore secrecy to an exposed value.
```

- [ ] **Step 2: Replace `.env.example` with only active, non-sensitive foundation settings**

```dotenv
# Copy to .env.local for local development.
# Never place paste content or real credentials in this file.

MCPASTE_ENV=development
MCPASTE_HTTP_ADDR=:8080
MCPASTE_LOG_LEVEL=info
```

- [ ] **Step 3: Replace `.gitignore` with the MCPaste repository exclusions**

```gitignore
# macOS and editors
.DS_Store
.idea/
.vscode/

# Environment and credentials
.env
.env.*
!.env.example
*.key
*.pem
*.p12
*.mobileprovision
secrets/
.secrets/

# Application data and databases
*.db
*.db-*
*.sqlite
*.sqlite3
*.sqlite-*
data/
var/

# Go output
bin/
coverage.out
mcpaste-server

# Swift and Xcode output
.build/
.swiftpm/
DerivedData/
xcuserdata/
*.xcuserstate

# General output
*.log
coverage/
dist/
build/
node_modules/
```

- [ ] **Step 4: Verify ignored sensitive files stay untracked**

Run:

```bash
touch .env.local example.key local.sqlite
git status --short --ignored .env.local example.key local.sqlite
```

Expected:

```text
!! .env.local
!! example.key
!! local.sqlite
```

Remove only these three disposable verification files:

```bash
rm .env.local example.key local.sqlite
```

Expected: the files no longer exist. They contained no user data and are not recoverable.

- [ ] **Step 5: Run the lightweight secret and obsolete-name checks**

```bash
rg -n '(sk-[A-Za-z0-9]{12,}|ghp_[A-Za-z0-9]{12,}|AKIA[0-9A-Z]{16}|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY)' .
rg -n 'PASTE_BRIDGE' .env.example .gitignore docs/security-and-secrets.md
```

Expected: both commands produce no output and exit status 1.

- [ ] **Step 6: Commit the security baseline**

```bash
git add docs/security-and-secrets.md .env.example .gitignore
git commit -m "docs: define remote secret handling"
```

Expected: one commit containing exactly those three files.

### Task 3: Add validated server configuration

**Files:**

- Create: `.go-version`
- Create: `go.mod`
- Create: `internal/config/config_test.go`
- Create: `internal/config/config.go`

- [ ] **Step 1: Add the Go toolchain declarations**

Create `.go-version`:

```text
1.26.5
```

Create `go.mod`:

```go
module github.com/1yoouoo/mcpaste

go 1.26.0
```

- [ ] **Step 2: Write the failing configuration tests**

Create `internal/config/config_test.go`:

```go
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
```

- [ ] **Step 3: Run the configuration test to verify it fails**

Run:

```bash
go test ./internal/config
```

Expected: FAIL because `Load`, `LookupEnv`, `Development`, and `Production` are undefined.

- [ ] **Step 4: Implement the minimal validated configuration**

Create `internal/config/config.go`:

```go
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
	LogLevel   slog.Level
}

type LookupEnv func(string) (string, bool)

func LoadOS() (Config, error) {
	return Load(os.LookupEnv)
}

func Load(lookup LookupEnv) (Config, error) {
	cfg := Config{
		Environment: Development,
		HTTPAddr:    ":8080",
		LogLevel:   slog.LevelInfo,
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
```

- [ ] **Step 5: Format and run the focused tests**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go
go test ./internal/config
```

Expected: PASS.

- [ ] **Step 6: Run static checks for the new package**

```bash
go vet ./internal/config
go test -race ./internal/config
```

Expected: both commands pass with exit status 0.

- [ ] **Step 7: Commit the configuration package**

```bash
git add .go-version go.mod internal/config/config.go internal/config/config_test.go
git commit -m "feat: add server configuration"
```

Expected: one commit containing the Go declarations and configuration package.

### Task 4: Add liveness and readiness endpoints

**Files:**

- Create: `internal/httpserver/health_test.go`
- Create: `internal/httpserver/health.go`

- [ ] **Step 1: Write the failing health-handler tests**

Create `internal/httpserver/health_test.go`:

```go
package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLiveness(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/livez", nil)
	response := httptest.NewRecorder()

	NewHandler(nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", response.Header().Get("Content-Type"))
	}
	if response.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("body = %q", response.Body.String())
	}
}

func TestReadiness(t *testing.T) {
	tests := []struct {
		name       string
		readiness  ReadinessFunc
		wantStatus int
		wantBody   string
	}{
		{
			name:       "ready",
			readiness:  func(context.Context) error { return nil },
			wantStatus: http.StatusOK,
			wantBody:   "{\"status\":\"ok\"}\n",
		},
		{
			name: "unavailable",
			readiness: func(context.Context) error {
				return errors.New("database-password-secret")
			},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "{\"status\":\"unavailable\"}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			response := httptest.NewRecorder()

			NewHandler(tt.readiness).ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
			if response.Body.String() != tt.wantBody {
				t.Fatalf("body = %q, want %q", response.Body.String(), tt.wantBody)
			}
			if strings.Contains(response.Body.String(), "database-password-secret") {
				t.Fatal("readiness response leaked internal error")
			}
		})
	}
}

func TestHealthEndpointsRejectNonGETMethods(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/livez", nil)
	response := httptest.NewRecorder()

	NewHandler(nil).ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
	if response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", response.Header().Get("Allow"))
	}
}
```

- [ ] **Step 2: Run the health tests to verify they fail**

```bash
go test ./internal/httpserver
```

Expected: FAIL because `ReadinessFunc` and `NewHandler` are undefined.

- [ ] **Step 3: Implement the minimal health handler**

Create `internal/httpserver/health.go`:

```go
package httpserver

import (
	"context"
	"io"
	"net/http"
)

type ReadinessFunc func(context.Context) error

func NewHandler(readiness ReadinessFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", healthHandler(nil))
	mux.HandleFunc("/readyz", healthHandler(readiness))
	return mux
}

func healthHandler(readiness ReadinessFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if readiness != nil && readiness(r.Context()) != nil {
			writeHealth(w, http.StatusServiceUnavailable, "unavailable")
			return
		}

		writeHealth(w, http.StatusOK, "ok")
	}
}

func writeHealth(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "{\"status\":\""+value+"\"}\n")
}
```

- [ ] **Step 4: Format and run the focused tests**

```bash
gofmt -w internal/httpserver/health.go internal/httpserver/health_test.go
go test ./internal/httpserver
```

Expected: PASS.

- [ ] **Step 5: Run package race and vet checks**

```bash
go test -race ./internal/httpserver
go vet ./internal/httpserver
```

Expected: both pass.

- [ ] **Step 6: Commit the health endpoints**

```bash
git add internal/httpserver/health.go internal/httpserver/health_test.go
git commit -m "feat: add server health endpoints"
```

Expected: one commit containing only the health handler and tests.

### Task 5: Add metadata-only access logging and the server process

**Files:**

- Create: `internal/httpserver/logging_test.go`
- Create: `internal/httpserver/logging.go`
- Create: `cmd/server/main.go`

- [ ] **Step 1: Write the failing access-log redaction test**

Create `internal/httpserver/logging_test.go`:

```go
package httpserver

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessLogContainsMetadataAndExcludesSecrets(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := NewAccessLogMiddleware(logger)(next)

	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/example?token=query-secret",
		strings.NewReader("body-secret"),
	)
	request.Header.Set("Authorization", "Bearer header-secret")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	logLine := output.String()
	for _, want := range []string{`"method":"POST"`, `"path":"/v1/example"`, `"status":204`} {
		if !strings.Contains(logLine, want) {
			t.Fatalf("log = %q, want it to contain %q", logLine, want)
		}
	}
	for _, secret := range []string{"query-secret", "body-secret", "header-secret", "Authorization"} {
		if strings.Contains(logLine, secret) {
			t.Fatalf("log leaked %q: %s", secret, logLine)
		}
	}
}
```

- [ ] **Step 2: Run the logging test to verify it fails**

```bash
go test ./internal/httpserver -run TestAccessLogContainsMetadataAndExcludesSecrets
```

Expected: FAIL because `NewAccessLogMiddleware` is undefined.

- [ ] **Step 3: Implement metadata-only access logging**

Create `internal/httpserver/logging.go`:

```go
package httpserver

import (
	"log/slog"
	"net/http"
	"time"
)

func NewAccessLogMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			wrapped := &statusWriter{
				ResponseWriter: w,
				status:         http.StatusOK,
			}

			next.ServeHTTP(wrapped, r)

			logger.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", wrapped.status,
				"duration_ms", time.Since(started).Milliseconds(),
			)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
```

- [ ] **Step 4: Format and run the logging tests**

```bash
gofmt -w internal/httpserver/logging.go internal/httpserver/logging_test.go
go test ./internal/httpserver
```

Expected: PASS, including the secret-exclusion assertions.

- [ ] **Step 5: Create the graceful server process**

Create `cmd/server/main.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/1yoouoo/mcpaste/internal/config"
	"github.com/1yoouoo/mcpaste/internal/httpserver"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mcpaste server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadOS()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
	handler := httpserver.NewAccessLogMiddleware(logger)(httpserver.NewHandler(nil))
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("server listening",
			"address", cfg.HTTPAddr,
			"environment", cfg.Environment,
		)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
```

- [ ] **Step 6: Format, test, vet, and build the complete Go tree**

```bash
gofmt -w cmd/server/main.go
go test -race ./...
go vet ./...
go build -o /tmp/mcpaste-server ./cmd/server
```

Expected: all commands pass and `/tmp/mcpaste-server` exists.

- [ ] **Step 7: Smoke-test the process without logging sensitive values**

Run:

```bash
MCPASTE_HTTP_ADDR=127.0.0.1:18080 /tmp/mcpaste-server >/tmp/mcpaste-foundation.log 2>&1 &
mcpaste_server_pid=$!
curl --retry 20 --retry-delay 1 --retry-connrefused --fail --silent http://127.0.0.1:18080/livez
kill "$mcpaste_server_pid"
wait "$mcpaste_server_pid" || true
rg -n 'Authorization|query-secret|body-secret|header-secret' /tmp/mcpaste-foundation.log
```

Expected health output:

```json
{"status":"ok"}
```

Expected secret search: no output and exit status 1.

- [ ] **Step 8: Commit the logging and runtime**

```bash
git add internal/httpserver/logging.go internal/httpserver/logging_test.go cmd/server/main.go
git commit -m "feat: add server runtime"
```

Expected: one commit containing access logging, its test, and the process entry point.

### Task 6: Package the server as a non-root container

**Files:**

- Create: `.dockerignore`
- Create: `Dockerfile`

- [ ] **Step 1: Verify the container build currently fails for the expected reason**

Run:

```bash
docker build -t mcpaste-server:foundation .
```

Expected: FAIL because `Dockerfile` does not exist.

- [ ] **Step 2: Add a minimal build context**

Create `.dockerignore`:

```dockerignore
.git
.github
.env
.env.*
!.env.example
.DS_Store
*.db
*.sqlite*
*.log
build
coverage
data
dist
docs
node_modules
secrets
var
```

- [ ] **Step 3: Add the multi-stage non-root image**

Create `Dockerfile`:

```dockerfile
FROM golang:1.26.5-alpine AS build

RUN apk add --no-cache ca-certificates
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/mcpaste-server \
    ./cmd/server

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/mcpaste-server /mcpaste-server

USER 65532:65532
EXPOSE 8080

ENTRYPOINT ["/mcpaste-server"]
```

- [ ] **Step 4: Build the image**

```bash
docker build -t mcpaste-server:foundation .
```

Expected: build succeeds and tags `mcpaste-server:foundation`.

- [ ] **Step 5: Smoke-test liveness inside the container**

```bash
mcpaste_container_id=$(docker run -d --rm -p 127.0.0.1:18081:8080 mcpaste-server:foundation)
curl --retry 20 --retry-delay 1 --retry-connrefused --fail --silent http://127.0.0.1:18081/livez
docker stop "$mcpaste_container_id"
```

Expected health output:

```json
{"status":"ok"}
```

Expected final command: Docker prints the stopped container ID and removes the container.

- [ ] **Step 6: Confirm the container runs as the intended non-root user**

```bash
docker image inspect mcpaste-server:foundation --format '{{.Config.User}}'
```

Expected:

```text
65532:65532
```

- [ ] **Step 7: Commit container packaging**

```bash
git add .dockerignore Dockerfile
git commit -m "build: package server container"
```

Expected: one commit containing only the build-context and image definitions.

### Task 7: Add pull-request and main CI gates

**Files:**

- Create: `.github/workflows/ci.yml`

- [ ] **Step 1: Verify the workflow is absent**

```bash
test ! -e .github/workflows/ci.yml
```

Expected: exit status 0.

- [ ] **Step 2: Add the pinned CI workflow**

Create `.github/workflows/ci.yml`:

```yaml
name: CI

on:
  pull_request:
  push:
    branches:
      - main

permissions:
  contents: read

concurrency:
  group: ci-${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  go:
    name: Go checks
    runs-on: ubuntu-24.04
    steps:
      - name: Check out repository
        uses: actions/checkout@df4cb1c069e1874edd31b4311f1884172cec0e10 # v6.0.3

      - name: Set up Go
        uses: actions/setup-go@4b73464bb391d4059bd26b0524d20df3927bd417 # v6.3.0
        with:
          go-version-file: .go-version
          cache: false

      - name: Check formatting
        run: test -z "$(find cmd internal -type f -name '*.go' -exec gofmt -l {} +)"

      - name: Vet
        run: go vet ./...

      - name: Test with race detector
        run: go test -race -coverprofile=coverage.out ./...

      - name: Build server
        run: go build -o /tmp/mcpaste-server ./cmd/server

      - name: Check known Go vulnerabilities
        run: go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

  container:
    name: Container build
    runs-on: ubuntu-24.04
    steps:
      - name: Check out repository
        uses: actions/checkout@df4cb1c069e1874edd31b4311f1884172cec0e10 # v6.0.3

      - name: Build server image
        run: docker build -t mcpaste-server:ci .

  secrets:
    name: Secret scan
    runs-on: ubuntu-24.04
    steps:
      - name: Check out full history
        uses: actions/checkout@df4cb1c069e1874edd31b4311f1884172cec0e10 # v6.0.3
        with:
          fetch-depth: 0

      - name: Scan repository
        uses: gitleaks/gitleaks-action@ff98106e4c7b2bc287b24eaf42907196329070c7 # v2
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

- [ ] **Step 3: Parse the workflow and inspect permissions**

Run:

```bash
ruby -e 'require "yaml"; YAML.parse_file(".github/workflows/ci.yml"); puts "valid yaml"'
rg -n 'permissions:|contents: read|pull_request:|branches:|main|deploy|production' .github/workflows/ci.yml
```

Expected first output:

```text
valid yaml
```

Expected search: read-only permissions and PR/`main` triggers are present; `deploy` and `production` are absent.

- [ ] **Step 4: Reproduce every local CI check**

```bash
test -z "$(find cmd internal -type f -name '*.go' -exec gofmt -l {} +)"
go vet ./...
go test -race -coverprofile=coverage.out ./...
go build -o /tmp/mcpaste-server ./cmd/server
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
docker build -t mcpaste-server:ci .
```

Expected: every command exits 0. Govulncheck reports no reachable known vulnerability.

- [ ] **Step 5: Commit CI**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: add foundation checks"
```

Expected: one commit containing only the CI workflow.

### Task 8: Verify the foundation acceptance criteria

**Files:**

- Verify only; no file changes expected.

- [ ] **Step 1: Run formatting, tests, vet, build, and vulnerability checks from repository root**

```bash
test -z "$(find cmd internal -type f -name '*.go' -exec gofmt -l {} +)"
go test -race ./...
go vet ./...
go build -o /tmp/mcpaste-server ./cmd/server
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
```

Expected: all commands exit 0.

- [ ] **Step 2: Run the container health smoke test again**

```bash
docker build -t mcpaste-server:foundation .
mcpaste_acceptance_container=$(docker run -d --rm -p 127.0.0.1:18082:8080 mcpaste-server:foundation)
curl --retry 20 --retry-delay 1 --retry-connrefused --fail --silent http://127.0.0.1:18082/livez
curl --fail --silent http://127.0.0.1:18082/readyz
docker stop "$mcpaste_acceptance_container"
```

Expected two responses:

```json
{"status":"ok"}
{"status":"ok"}
```

- [ ] **Step 3: Verify branding, placeholders, accidental secrets, and whitespace**

```bash
rg -n 'Paste Bridge|PASTE_BRIDGE' README.md SECURITY.md docs/security-and-secrets.md .env.example .gitignore
rg -n -i '\b(T[B]D|T[O]DO|F[I]XME|X[X]X)\b' README.md SECURITY.md docs/security-and-secrets.md internal cmd .github Dockerfile
rg -n '(sk-[A-Za-z0-9]{12,}|ghp_[A-Za-z0-9]{12,}|AKIA[0-9A-Z]{16}|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY)' .
git diff --check HEAD
```

Expected: all searches and the whitespace check produce no output. Search commands exit 1; `git diff --check HEAD` exits 0.

- [ ] **Step 4: Verify commit and working-tree boundaries**

```bash
git log --oneline --decorate -8
git status --short --branch
```

Expected: the phase has separate documentation, configuration, health, runtime, container, and CI commits. The working tree has no changes produced by these tasks. Pre-existing plan/spec files are tracked; no environment file, database, credential, binary, or coverage file is staged.

## Phase handoff

After every Task 8 check passes, update the implementation record with:

- installed Go version;
- exact commands and outputs used for acceptance;
- commits created by Tasks 1 through 7;
- any deviation from this plan and its evidence;
- confirmation that no remote, push, deployment, or paid infrastructure change occurred.

Then write or refresh the Phase 2 identity-server plan against the actual repository state. Do not begin PostgreSQL, authentication, pairing, encryption, or MCP work inside this foundation phase.
