# Security Policy

MCPaste handles arbitrary text and images. Treat them as sensitive by default.

MCPaste is in early development and has no supported release. Design requirements in this document and the approved project design are not implementation guarantees.

## Contributor rules

- Never commit real credentials, cookies, private keys, recovery codes, pairing codes, personal data, or paste data.
- Never put secrets in examples, fixtures, logs, screenshots, issues, or pull requests.
- Use fake, deterministic examples that are visibly non-functional.
- Do not log request or response bodies, authentication headers, cookies, QR codes, or secret-bearing URLs.
- Keep generated Codex and Claude Code connection files, macOS Keychain values, and Linux credential-store data outside the repository.
- Scope production encryption keys, database credentials, Apple credentials, and deployment keys narrowly.

## Trust boundary

The server can decrypt MCPaste data. TLS in transit and encryption at rest do not protect data from a malicious operator or a full server compromise. Retention by downstream AI tools and providers is outside MCPaste's control.

## Incident response

1. Rotate or revoke the exposed credential first.
2. Do not recopy the secret into an issue, pull request, log, or report.
3. Record only metadata needed to identify the incident, such as the affected file, commit, time, and rotation status.
4. Report the incident privately.

## Reporting a vulnerability

Use GitHub private vulnerability reporting when it is enabled. If it is unavailable, contact the maintainer using the contact details on their GitHub profile; do not send vulnerability details in a public issue. Include reproduction steps, impact, affected version or commit, and mitigation details where available. Do not include a live secret or other sensitive payload; use a fake deterministic example instead.
