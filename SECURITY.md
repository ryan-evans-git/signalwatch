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

## API authentication

The HTTP API supports two bearer-token mechanisms; either or both can be active simultaneously.

### Shared token (`SIGNALWATCH_API_TOKEN`)

The legacy v0.1 single-tenant model: any request bearing `Authorization: Bearer ${SIGNALWATCH_API_TOKEN}` is treated as **admin scope**. Empty env var (and no per-user tokens) leaves `/v1/*` open — appropriate only for `127.0.0.1`-bound, single-developer deployments. Comparison is constant-time (`crypto/subtle`).

### Per-user tokens (DB-backed, v0.4+)

Tokens live in the `api_tokens` table with these fields: `id`, `name`, `token_hash` (sha256 of the raw secret — the secret itself is **never persisted**), `scopes`, `created_at`, `expires_at`, `last_used_at`, `revoked`. Issue / list / revoke via the API:

```bash
# Issue (returns the raw secret exactly once)
curl -sX POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"ci-deploybot","scopes":["admin"],"expires_in":"720h"}' \
  $URL/v1/auth/tokens
# {"token":{"id":"...","name":"ci-deploybot",...},"secret":"sw_..."}

# List (never returns the secret or the hash)
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" $URL/v1/auth/tokens

# Revoke
curl -sX DELETE -H "Authorization: Bearer $ADMIN_TOKEN" \
  $URL/v1/auth/tokens/$TOKEN_ID
```

Scopes are coarse for v0.4:

- `admin` — every route (mutating + read)
- `read` — GET-only; mutating verbs return 403

`expires_in` accepts any `time.ParseDuration` value (`24h`, `720h` = 30d). Omit for non-expiring tokens.

Tokens are stored only as `sha256(raw_secret)`. If the DB is dumped, the rows can't be replayed against the API without finding a sha256 preimage. The secret is returned to the caller exactly once at issuance; the API has no recovery path. `last_used_at` is updated best-effort in a 2-second background timeout so an unresponsive DB doesn't slow auth.

Operational guidance:

- Issue one token per automated caller (CI, alert-relay, etc.); rotate by issuing a fresh one and revoking the old.
- Set `expires_in` whenever possible; long-lived tokens accumulate risk.
- The `last_used_at` column makes inactive-token cleanup easy: `SELECT id FROM api_tokens WHERE last_used_at < ?`.

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
