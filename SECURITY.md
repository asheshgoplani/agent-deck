# Security Policy

## Reporting a vulnerability

Please report security issues privately via one of:

- GitHub Security Advisory: https://github.com/asheshgoplani/agent-deck/security/advisories/new (preferred)
- Email: ashesh.goplani96@gmail.com

Do not file public issues for security reports. We will respond within 7 days and coordinate a fix + disclosure timeline.

## Scope

In scope: code in this repository, official release artifacts (GitHub releases + brew tap).
Out of scope: third-party Claude / agent CLI tools agent-deck wraps, third-party MCP servers.

## Supply chain

- We use Dependabot (weekly) + govulncheck on PRs for Go module CVEs.
- We use CodeQL + golangci-lint (gosec, staticcheck) for static analysis.
- GitHub Actions are pinned by SHA where third-party.
