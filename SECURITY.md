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
- Release artifacts carry [SLSA build provenance](https://slsa.dev/) via GitHub's
  artifact attestation. The release workflow verifies every artifact (count +
  SHA-256 against `checksums.txt`) before signing and fails closed otherwise, so
  an attestation only ever covers the complete, intact release. Consumers can
  verify a binary was built by this repo's CI from the tagged source:
  ```bash
  gh attestation verify agent-deck_*_linux_amd64.tar.gz --repo asheshgoplani/agent-deck
  ```
  **Known gap: v1.10.9, v1.10.10 and v1.10.11 have no attestation.** A failure
  publishing the Homebrew tap aborted the release job before the signing step, so
  `gh attestation verify` reports "no attestations found" for those three tags —
  a broken pipeline, not a tampered artifact. Their assets were checked by hand
  against `checksums.txt`; a hand check is not a signature, so treat those three
  tags as unattested. Fixed in [#1760](https://github.com/asheshgoplani/agent-deck/issues/1760):
  provenance is now signed *before* a release is published, and the tap can no
  longer fail the release job. Releases after v1.10.11 are attested again.
