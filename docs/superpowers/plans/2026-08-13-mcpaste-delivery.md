# MCPaste production delivery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Add production Compose/Caddy operations, immutable CI/CD workflows, Linux/macOS release automation, rollback behavior, and an operator runbook without embedding production secrets.

**Architecture:** Production runs Caddy, the Go server, and PostgreSQL on one host with PostgreSQL private and the server data directory on a persistent local volume. CI validates all code; deployment builds/pushes an image by digest and performs expand-compatible migration/readiness checks. Release workflows produce checksummed Linux binaries and signed/notarized macOS artifacts only when owner-provided signing credentials exist.

**Tech Stack:** Docker Compose, Caddy, GitHub Actions with pinned actions, GHCR, SSH, Go, Swift/Xcode, `codesign`, `notarytool`, SHA-256 checksums, shell scripts.

**Spec:** `docs/superpowers/specs/2026-08-12-mcpaste-system-design.md`

## Global Constraints

- No production secret, master encryption key, SSH key, Apple signing identity, or notarization credential is committed or printed.
- Only ports 80 and 443 are public; PostgreSQL has no public port.
- Deployment must stop before replacing a healthy application if migrations or readiness fail.
- Application rollback must not run destructive database down migrations.
- The container runs as non-root and keeps the server master key outside the image.
- Workflows use least privilege, pinned action revisions, and protected environment secrets.
- This local execution must not push, deploy, tag, or create a release.

---

## Task 1: Production Compose and Caddy topology

**Files:**

- Create `deploy/compose.production.yaml`.
- Create `deploy/Caddyfile`.
- Create `deploy/server.env.example`.
- Modify `Dockerfile`, `.dockerignore`, and `.gitignore`.
- Add `deploy/compose.production.test.sh`.

- [ ] Step 1: Write shell/config tests for non-root server, private PostgreSQL, public 80/443 only, persistent database/data volumes, healthchecks, and required env names.
- [ ] Step 2: Run checks red because deploy files do not exist.
- [ ] Step 3: Implement pinned images, Caddy reverse proxy, HTTP-to-HTTPS redirect, request body limits, server health routing, and persistent mounts.
- [ ] Step 4: Ensure `server.env.example` contains names and safe placeholders only, with mode instructions for `/etc/mcpaste/server.env`.
- [ ] Step 5: Run `docker compose -f deploy/compose.production.yaml config` with a temporary non-secret env file and commit `ops: add production compose topology`.

## Task 2: Add safe bootstrap, migration, deploy, and rollback scripts

**Files:**

- Create `deploy/bootstrap-host.sh`.
- Create `deploy/deploy-image.sh`.
- Create `deploy/rollback-image.sh`.
- Create `deploy/health-smoke.sh`.
- Create `deploy/tests/deploy_scripts_test.sh`.

- [ ] Step 1: Write shell tests using temporary directories and fake Docker commands for permissions, migration order, health failure, and rollback image selection.
- [ ] Step 2: Run tests red.
- [ ] Step 3: Implement bootstrap with Ubuntu/Docker prerequisites, root-owned 0600 env file checks, firewall/port assertions, and no secret output.
- [ ] Step 4: Implement deployment as pull immutable digest, run `mcpaste-migrate up`, start new container, run readiness and authenticated smoke checks, and retain old image until success.
- [ ] Step 5: Implement rollback as application image restoration only; never call migration down.
- [ ] Step 6: Run shellcheck if installed, `bash -n`, and script tests; commit `ops: add deployment and rollback scripts`.

## Task 3: Harden CI and add deployment workflow

**Files:**

- Modify `.github/workflows/ci.yml`.
- Create `.github/workflows/deploy.yml`.
- Create `.github/workflows/release.yml`.
- Create `.github/workflows/macos-release.yml`.

- [ ] Step 1: Write workflow contract checks for pinned action SHAs, least-privilege permissions, no secret values, Go/Swift/image tests, and architecture builds.
- [ ] Step 2: Run checks red.
- [ ] Step 3: Extend CI with Swift tests, image integration tests, vulnerability scan, container build, and artifact checks.
- [ ] Step 4: Add `deploy.yml` gated on successful main CI, GHCR digest publishing, protected `production` environment, SSH known-host verification, forward migrations, readiness smoke, and rollback on application failure.
- [ ] Step 5: Add Linux release workflow for tags with static amd64/arm64 binaries, SHA-256 checksums, and generated release notes.
- [ ] Step 6: Add macOS release workflow with Xcode archive, Developer ID signing, notarization, stapling, DMG creation, and checksum publication; all credentials come from protected secrets and are never echoed.
- [ ] Step 7: Run YAML parsing, action SHA checks, and local dry-run checks; commit `ci: add delivery and release workflows`.

## Task 4: Add operator runbook and release documentation

**Files:**

- Create `docs/operations.md`.
- Create `docs/releases.md`.
- Modify `README.md`, `SECURITY.md`, and `docs/security-and-secrets.md`.

- [ ] Step 1: Write documentation checks for first boot, DNS/TLS, env permissions, migration, health, rollback, key rotation, retention, log redaction, release verification, and accepted no-backup risk.
- [ ] Step 2: Add exact commands using placeholders that cannot be mistaken for credentials.
- [ ] Step 3: Document Linux checksum install and macOS signature/notarization verification.
- [ ] Step 4: Run secret scan and Markdown link/path checks; commit `docs: add production runbook`.

## Task 5: Final Phase 6 acceptance

- [ ] Step 1: Run Go/Swift tests, race, vet, builds, image build, Compose config, shell tests, workflow checks, and secret scans from a clean tree.
- [ ] Step 2: Run a local deployment simulation with fake registry/SSH commands; verify failed migration and failed health leave the old image selected.
- [ ] Step 3: Verify no push, tag, release, deployment, or signing command ran in this environment.
- [ ] Step 4: Create `docs/superpowers/records/2026-08-13-mcpaste-delivery.md` with evidence and owner checkpoints.
- [ ] Step 5: Commit `docs: record production delivery phase handoff`.

