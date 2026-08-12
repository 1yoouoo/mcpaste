# MCPaste Foundation implementation record

- Date: 2026-08-12
- Phase: 1 — Foundation
- Recorded repository state: `main` at `4084a5f`

## Goal and scope

Phase 1 established a security-conscious repository baseline and a tested, containerized Go HTTP server skeleton. Its scope was public project and security documentation, secret-placement rules, validated process configuration, liveness and readiness routes, metadata-only request logging, graceful server runtime behavior, a non-root container image, pull-request and `main` CI gates, and local acceptance evidence.

### Out of scope

PostgreSQL, authentication, sessions, device identity, pairing, encryption of paste bodies, paste or image persistence, the MCP connector and protocol surface, the macOS app, Caddy, production deployment, DigitalOcean resources, paid infrastructure, and production secret changes were not part of this phase. No Phase 2 implementation was started.

## Environment

- Repository: repository root
- Branch: `main`
- Go: `go version go1.26.5 darwin/arm64`
- Docker: `Docker version 28.2.2, build e6534b4`

## Implemented

- Public documentation now identifies MCPaste, states the trust boundary, and directs contributors to concrete security and secret-handling rules.
- Foundation configuration validates environment, HTTP address, and log level values, with tests for defaults, overrides, and rejected input.
- The Go server exposes `GET /livez` and `GET /readyz`, returns redacted readiness failures, rejects unsupported methods, applies bounded HTTP timeouts, handles process signals, and shuts down gracefully.
- The server binds its configured address before logging `server listening`; an occupied-address regression test proves bind failure cannot emit a false success log.
- Access and recovery logs contain metadata only. They use the matched route pattern, or `<unmatched>`, rather than the raw URL path; tests cover path, query, body, authorization-header, and panic-value exclusion.
- The multi-stage image builds a static server and runs it as `65532:65532` from `scratch`. The Docker context is default-deny and admits only `go.mod`, `cmd/**`, and `internal/**`.
- CI covers pull requests and pushes to `main` with read-only repository permission, pinned actions, disabled checkout credential persistence, 15-minute job timeouts, Go formatting/vet/race/build/vulnerability checks, container build verification, and a redacting Gitleaks v8.24.3 CLI scan with a pinned archive checksum.

## Verified

All commands below ran from the repository root during Task 8 acceptance or the evidence review of this record.

### Go acceptance

- `test -z "$(find cmd internal -type f -name '*.go' -exec gofmt -l {} +)"` — exit 0; no files required formatting.
- `go test -race ./...` — exit 0; `cmd/server` had no test files, and `internal/config` plus `internal/httpserver` passed.
- Post-acceptance correction: `go test ./cmd/server -run TestRunDoesNotLogListeningWhenAddressCannotBind -count=1` failed before the fix because `server listening` was logged for an occupied address, then passed after the bind-before-log change. A fresh `go test -race ./...` also passed with the new `cmd/server` regression test.
- `go vet ./...` — exit 0 with no output.
- `go build -o /tmp/mcpaste-server ./cmd/server` — exit 0.
- `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...` — exit 0 with `No vulnerabilities found.`

### Container acceptance

- `docker build -t mcpaste-server:foundation-record-evidence .` — exit 0.
- `docker run -d --rm --name mcpaste-foundation-record-evidence -p 127.0.0.1:18082:8080 mcpaste-server:foundation-record-evidence` — exit 0 and returned an ephemeral container identifier.
- `curl --retry 20 --retry-delay 1 --retry-connrefused --silent --show-error --output /tmp/mcpaste-foundation-livez.body --write-out '%{http_code}' http://127.0.0.1:18082/livez > /tmp/mcpaste-foundation-livez.status` — exit 0.
- `test "$(cat /tmp/mcpaste-foundation-livez.status)" = 200` — exit 0, independently asserting the `/livez` status.
- `cmp -s /tmp/mcpaste-foundation-livez.body <(printf '{"status":"ok"}\n')` — exit 0, asserting the exact `/livez` body including its trailing newline. `od -An -tx1 -v /tmp/mcpaste-foundation-livez.body` exited 0 and ended with bytes `7d 0a`.
- `curl --silent --show-error --output /tmp/mcpaste-foundation-readyz.body --write-out '%{http_code}' http://127.0.0.1:18082/readyz > /tmp/mcpaste-foundation-readyz.status` — exit 0.
- `test "$(cat /tmp/mcpaste-foundation-readyz.status)" = 200` — exit 0, independently asserting the `/readyz` status.
- `cmp -s /tmp/mcpaste-foundation-readyz.body <(printf '{"status":"ok"}\n')` — exit 0, asserting the exact `/readyz` body including its trailing newline. `od -An -tx1 -v /tmp/mcpaste-foundation-readyz.body` exited 0 and ended with bytes `7d 0a`.
- `docker stop mcpaste-foundation-record-evidence` and `docker image rm mcpaste-server:foundation-record-evidence` each exited 0.
- The following exact cleanup and absence assertion exited 0:

```sh
for capture in /tmp/mcpaste-foundation-livez.status /tmp/mcpaste-foundation-livez.body /tmp/mcpaste-foundation-readyz.status /tmp/mcpaste-foundation-readyz.body
do
  if test -e "$capture"
  then
    unlink "$capture"
  fi
done
test ! -e /tmp/mcpaste-foundation-livez.status \
  -a ! -e /tmp/mcpaste-foundation-livez.body \
  -a ! -e /tmp/mcpaste-foundation-readyz.status \
  -a ! -e /tmp/mcpaste-foundation-readyz.body
```

- `docker ps -a --filter name='^/mcpaste-foundation-record-evidence$' --format '{{.ID}} {{.Names}} {{.Status}}'` — exit 0 with no output after cleanup.
- `docker image inspect mcpaste-server:foundation-record-evidence` — exit 1 with `No such image` after cleanup.
- `lsof -nP -iTCP:18082 -sTCP:LISTEN` — exit 1 with no output after cleanup, confirming no listener remained.

### Repository and CI acceptance

- `rg -n 'Paste Bridge|PASTE_BRIDGE' README.md SECURITY.md docs/security-and-secrets.md .env.example .gitignore` — exit 1 with no matches.
- `rg -n -i '\b(T[B]D|T[O]DO|F[I]XME|X[X]X)\b' README.md SECURITY.md docs/security-and-secrets.md internal cmd .github Dockerfile` — exit 1 with no matches.
- `rg -n '(sk-[A-Za-z0-9]{12,}|ghp_[A-Za-z0-9]{12,}|AKIA[0-9A-Z]{16}|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY)' .` — exit 1 with no matches.
- `git diff --check HEAD` — exit 0 with no output.
- `ruby -e 'require "yaml"; YAML.parse_file(".github/workflows/ci.yml"); puts "valid yaml"'` — exit 0 and printed `valid yaml`.
- `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 -version` reported `v1.7.7`; `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/ci.yml` exited 0 with no findings.
- This exact disposable Ubuntu 24.04 Linux/amd64 command exited 0. The bind mount was read-only, Git was installed in the runner, and the downloaded Gitleaks archive and extracted Gitleaks binary stayed under the runner's `/tmp`:

```sh
docker run --rm --platform linux/amd64 --mount type=bind,src="$PWD",dst=/repo,readonly --workdir /repo ubuntu:24.04 bash -ceu '
export DEBIAN_FRONTEND=noninteractive
apt-get update >/dev/null
apt-get install -y --no-install-recommends ca-certificates curl git >/dev/null
curl --fail --location --silent --show-error \
  --output /tmp/gitleaks.tar.gz \
  https://github.com/gitleaks/gitleaks/releases/download/v8.24.3/gitleaks_8.24.3_linux_x64.tar.gz
echo "9991e0b2903da4c8f6122b5c3186448b927a5da4deef1fe45271c3793f4ee29c  /tmp/gitleaks.tar.gz" | sha256sum --check
tar -xzf /tmp/gitleaks.tar.gz -C /tmp gitleaks
/tmp/gitleaks detect --source . --redact --verbose
rm /tmp/gitleaks /tmp/gitleaks.tar.gz
'
```

- The command printed `/tmp/gitleaks.tar.gz: OK`, `16 commits scanned.`, and `no leaks found`. `git rev-list --count --all` separately exited 0 with `16`, confirming the scan count covered the repository's full reachable history. Docker `--rm` removed the runner, and the read-only repository mount prevented writes to tracked files.

## Committed

Tasks 1–7 produced these commits in repository order, with their existing subjects:

1. Task 1 — `4d54b0b` — `docs: rename project to MCPaste`
2. Task 1 — `546cc16` — `docs: fix design document links`
3. Task 1 — `357bd65` — `docs: clarify security boundaries`
4. Task 2 — `26da490` — `docs: define remote secret handling`
5. Task 2 — `97e23d9` — `docs: tighten logging boundaries`
6. Task 2 — `b411541` — `docs: correct security workflows`
7. Task 3 — `2e9e590` — `feat: add server configuration`
8. Task 4 — `beac5c3` — `feat: add server health endpoints`
9. Task 5 — `67262c6` — `feat: add server runtime`
10. Task 5 — `7edec5e` — `fix: harden server request logging`
11. Task 6 — `7892c06` — `build: package server container`
12. Task 6 — `f06d4ca` — `build: restrict Docker build context`
13. Task 7 — `a915db1` — `ci: add foundation checks`
14. Task 7 — `4084a5f` — `ci: harden secret scanning`

The list above is limited to Tasks 1–7 implementation commits. This record and the Phase 2 plan are committed afterward as the documentation handoff and are intentionally excluded from that task list.

Post-acceptance review produced these additional local commits before the final handoff update:

1. `215de3b` — `docs: record foundation and plan identity server`
2. `61d7b7e` — `docs: keep identity plan secret-scan clean`
3. `a99b40d` — `fix: log server listening after bind`

The updated roadmap, Phase 2 baseline assertions, and this record are included together in the final documentation commit with subject `docs: finalize foundation handoff`.

## Handoff status

At the Task 8 evidence checkpoint, before the final documentation commit, `git status --short --branch --untracked-files=all` reported no tracked changes and exactly these two untracked handoff files:

- `docs/superpowers/plans/2026-08-12-mcpaste-identity-server.md`
- `docs/superpowers/records/2026-08-12-mcpaste-foundation.md`

## Pushed-deployed

Local Git evidence is limited to local configuration: `git remote get-url origin` returned `https://github.com/1yoouoo/mcpaste.git`; `git for-each-ref --format='%(upstream:short)' refs/heads/main` returned no upstream; and `git for-each-ref --format='%(refname:short)' refs/remotes` returned no remote-tracking ref. These facts do not establish remote activity. The repository rename is recorded as owner direction.

Author/session attestation: based on actions observed during this session, the author attests that no push, deployment, Droplet, paid infrastructure, or production secret change occurred during this session. This is a session record, not a durable fact inferable from local Git or the repository after the session ends.

## Decisions

### Owner direction

- The owner directed Foundation work to use the current `main` branch rather than a worktree.
- The owner recorded that the GitHub repository was renamed; the local `origin` value above is separate supporting evidence only for local configuration.

### Security decisions

- Request logging uses the matched route pattern, or `<unmatched>`, instead of the raw URL path because the design forbids secret-bearing URLs in logs. Commit `7edec5e` implements and tests this boundary.
- The Docker context uses a default-deny allowlist because a denylist would admit sensitive unlisted files. Commit `f06d4ca` admits only the build inputs.
- The pinned `gitleaks-action` integration was replaced by the official Gitleaks v8.24.3 CLI with SHA-256 verification because source review found no pull-request API pagination and an unverified binary download. Commit `4084a5f` also disables checkout credential persistence and adds job timeouts.

### Harness corrections

These were transient evidence-harness corrections, not product failures, and did not alter tracked product files:

- An unsafe cleanup command was blocked before execution, so it caused no state change. Cleanup was rerun against explicit disposable paths only.
- The first health body assertion omitted the server's trailing newline. The expected value was corrected to include byte `0a`; both endpoints passed exact comparisons without a server change.
- A harness variable named `path` conflicted with zsh's special variable. It was renamed and the affected check passed without a product change.
- The first disposable Gitleaks runner verified the archive checksum but lacked Git, so Gitleaks exited before scanning. Its `--rm` container was removed; the runner was recreated with Git, and the full-history scan then passed as recorded above.

## Deviations from plan

1. The owner-directed use of current `main` replaced the plan workflow's worktree expectation.
2. The logging route pattern, Docker default-deny context, and checksum-verified Gitleaks CLI are security-driven deviations described under Decisions.
3. The plan expected one commit per task. Two-stage review produced these focused corrective follow-up commits while preserving task boundaries:
   - Task 1: `546cc16` — `docs: fix design document links`; `357bd65` — `docs: clarify security boundaries`.
   - Task 2: `97e23d9` — `docs: tighten logging boundaries`; `b411541` — `docs: correct security workflows`.
   - Task 5: `7edec5e` — `fix: harden server request logging`.
   - Task 6: `f06d4ca` — `build: restrict Docker build context`.
   - Task 7: `4084a5f` — `ci: harden secret scanning`.

## Temporary artifacts

The disposable ignore-test files `.env.local`, `example.key`, and `local.sqlite`; `coverage.out`; the `/tmp/mcpaste-server` build; the four exact health status/body captures; and the Gitleaks archive and binary were removed. The evidence health container and image tag were removed, the reproducibility runner used `--rm` with a read-only repository mount, and no process remained listening on port 18082. At the Task 8 evidence checkpoint, Git status contained only the two then-untracked handoff files listed above.

## Remaining risks

- This is only the stateless foundation. Authentication, pairing, storage, encryption, paste authorization, MCP behavior, and client behavior remain unimplemented and unverified.
- Readiness currently has no stateful dependency to probe, so it establishes process readiness only.
- No hosted GitHub Actions run was verified; CI syntax and its component commands were verified locally.
- Vulnerability and secret-scan results are point-in-time evidence, not a guarantee against future or undisclosed issues.

## Next safe step

The Foundation handoff and reviewed Phase 2 plan are committed locally. The next safe step is to execute only `docs/superpowers/plans/2026-08-12-mcpaste-identity-server.md` as a separate phase. No Phase 2 implementation was started during Foundation work or while updating this record.
