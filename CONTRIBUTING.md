# Contributing

Thanks for your interest in signalwatch. This document is the source of truth for how work gets in. Read it before opening your first PR — the workflow is opinionated and the CI gates are strict.

> Status: pre-alpha. APIs and on-disk schema are not yet stable. Expect breaking changes before v0.2.0.

## Process

- **TDD.** Every non-trivial change starts with a failing test. PRs without new or updated tests need an explicit reviewer waiver in the PR body.
- **Active sprint.** Work tracks against [`docs/PI-PLAN.md`](./docs/PI-PLAN.md). If your change is outside the active sprint, mention it in the PR — it's still welcome, but reviewers may ask you to defer it to the backlog.
- **Issue first for non-trivial changes.** Large refactors, new dependencies, new public-API surface — open an issue first so the design conversation happens before the code does.

## Local development

Required:

- Go 1.24+
- Node.js 18+ (for the `web/` UI)
- `make`

Setup:

```bash
git clone https://github.com/ryan-evans-git/signalwatch
cd signalwatch
make build         # builds the React UI then the Go binaries
./bin/signalwatch  # localhost:8080
```

Run the full local CI suite (matches what the `ci.yml` workflow does):

```bash
make verify
```

This runs lint, tests with the 90% coverage gate, gosec, govulncheck, license check, and trivy fs scan. Every step must be green before you push.

## Branch + PR flow

1. **Branch.** Off `main` only. Naming: `feature/<short-slug>`, `fix/<short-slug>`, `chore/<short-slug>`.
2. **Commits.** [Conventional commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `test:`, `chore:`, `docs:`, `refactor:`). Squash-merge is the merge strategy on `main`.
3. **Pre-push checklist.**
   - `make verify` is green.
   - New code has tests; coverage didn't drop.
   - Public-API changes have a doc-comment update and, if behavior changed, a `CHANGELOG.md` entry.
4. **PR.** Use the PR template. Required sections: motivation, what changed, how it was tested, breaking-change disclosure.
5. **Review.** At least one maintainer approval. CODEOWNERS is enforced for `engine/`, `internal/dispatcher/`, and `internal/store/`.
6. **Merge.** Squash. The branch is deleted after merge.

## CI gates

All of these must pass before merge to `main`:

| Gate | Tool | What it catches |
|---|---|---|
| Lint | `golangci-lint` | Style + obvious bugs (errcheck, ineffassign, gocyclo, etc.) |
| Tests | `go test` | Functionality |
| Coverage | `vladopajic/go-test-coverage` | Branch coverage < 90% repo-wide, < 90% per package, < 70% per file |
| Static security | `gosec` | Common security smells in Go code |
| Dependency CVEs | `govulncheck` | Known-vulnerable deps |
| License policy | `google/go-licenses` | Block GPL/AGPL/SSPL/BUSL/Elastic/Commons-Clause |
| Filesystem scan | `trivy` | OS package CVEs + lockfile entries |
| SAST | `CodeQL` (`security-extended`) | Cross-cutting security queries |
| Secrets | `gitleaks` | Committed credentials |
| Builds | `make build` | Binaries still build cleanly |

Pinned-by-SHA actions, not by `@v3` floating tags — Dependabot rotates them.

## Test conventions

- One `_test.go` file per source file when practical.
- Table-driven tests for branch-heavy logic (condition compilation, state transitions).
- Use `t.Helper()` in test helpers so failures point at the calling test.
- Time: inject a `now func() time.Time` rather than calling `time.Now` directly. See `internal/dispatcher/dispatcher_test.go` for the pattern.
- HTTP: use `net/http/httptest`. Don't bind real ports.
- DB: `file::memory:?cache=shared` for SQLite tests. `testcontainers-go` for Postgres/MySQL/Kafka in PI 2.
- Coverage exemptions are tracked in `.testcoverage.yml.exclude` (file-level) or `// nocover:reason` annotations (block-level). New exemptions require reviewer sign-off.

## Style

- Go style: `gofmt`, `goimports`, `golangci-lint`. The CI is the source of truth.
- Comments: don't write comments that explain WHAT the code does — names should. Comments explain WHY when the why isn't obvious.
- No emojis in source files.
- TypeScript / React: Tailwind for styles, `tsc --strict` is on, `noUnusedLocals` is on.

## Reporting bugs

For non-security bugs: open a GitHub issue with reproduction steps.

For security bugs: see [`SECURITY.md`](./SECURITY.md). Do not open a public issue.

## License of contributions

By submitting a contribution, you agree it is licensed under the [Apache-2.0](./LICENSE) license that covers this project.
