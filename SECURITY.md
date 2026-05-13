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

## Handling third-party credentials

A few channel types need provider credentials. **None go in config files**; they are read from the environment at startup.

| Channel | Env vars |
| --- | --- |
| Twilio SMS (`sms`) | `SIGNALWATCH_TWILIO_ACCOUNT_SID`, `SIGNALWATCH_TWILIO_AUTH_TOKEN` |

Operational guidance:

- Inject these via your process supervisor (systemd `EnvironmentFile=`, Kubernetes `Secret`, AWS SSM, etc.). Never commit them.
- Rotate routinely. signalwatch reads each env var once at process startup; restart after rotation.
- The `sms` channel's `from_number` is non-sensitive (it's a phone number bound to your Twilio account) and lives in YAML; only the API credentials are env-only.

Other channel types (SMTP, Slack/Discord/Teams webhooks, PagerDuty routing keys) accept their credentials in YAML today. That's a deliberate trade-off for v0.3 — those credentials are scoped to a single posting target, not full-account API access. Moving them to env-var indirection is on the v0.4 roadmap.

## Disclosure timeline

We aim for coordinated disclosure within 90 days of a confirmed report, sooner if a fix is available. Reporters who want public credit are listed in the advisory; otherwise reports are anonymous.
