# Security Policy

## Reporting a vulnerability

Please report security issues privately via GitHub's **Security Advisories** ("Security" tab → "Report a vulnerability") rather than opening a public issue. Reports are acknowledged within 5 business days.

If you can't use GitHub Advisories, email the address listed on the maintainer's GitHub profile. PGP-encrypted reports are accepted on request.

## Supported versions

Pre-alpha. Only `main` is supported. Once v0.2 is released, only the latest minor version receives security fixes.

## Scope

In scope:

- Code in this repository (Go modules under `engine/`, `internal/`, `cmd/`).
- The bundled React UI in `web/`.
- Container images built from this repository's `Dockerfile` (when one is added in PI 2).

Out of scope:

- Vulnerabilities in upstream Go modules — please report those to the respective maintainers. We track their advisories via Dependabot and `govulncheck` and ship updated pins promptly.
- Datasources or message queues you connect signalwatch to (Postgres, Kafka, etc.) — those have their own security boundaries.
- The CICD workflows of forks of this repository.

## What we run on every commit

A failing security check blocks merge to `main`. The full list of gates is documented in [`CONTRIBUTING.md`](./CONTRIBUTING.md). Security-relevant gates:

- **`golangci-lint`** — lint + static checks
- **`gosec`** — Go static security analysis (medium+ severity gates merge)
- **`govulncheck`** — Go-module CVE scan; resolves against the live Go vulnerability database
- **`google/go-licenses`** — block GPL/AGPL/SSPL/BUSL/Elastic/Commons-Clause
- **Trivy** — filesystem scan for OS-package + lockfile CVEs
- **CodeQL** — `security-extended` query pack on push, PR, and weekly cron
- **Gitleaks** — full-history committed-secret detection on every PR
- **Dependabot** — weekly grouped dep PRs; security advisories open immediately

## Branch protection

`main` is protected: all CI checks above are required, PR review is required, force-push and deletion are blocked, and signed commits are required. The applied configuration lives in [`docs/BRANCH-PROTECTION.md`](./docs/BRANCH-PROTECTION.md) and is reproducible via the `gh` script in that document.

## Disclosure timeline

We aim for coordinated disclosure within 90 days of a confirmed report, sooner if a fix is available. Reporters who want public credit are listed in the advisory; otherwise reports are anonymous.
