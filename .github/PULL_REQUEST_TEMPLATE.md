<!--
Thanks for contributing. Please fill out every section. PRs missing required
sections may be closed without review. See CONTRIBUTING.md for the full flow.
-->

## Motivation

<!-- Why is this change needed? Link the sprint task or issue if applicable. -->

## What changed

<!-- One or two paragraphs. Skip the line-by-line — that's what the diff is for. -->

## How it was tested

<!--
Required. Even doc-only changes need a "no test changes; doc-only" line.

For code changes, list the new/updated test files and what they cover.
TDD discipline: tests should generally precede implementation in the diff history.
-->

## Breaking changes

<!--
"None." or describe the break, the migration path, and the CHANGELOG entry.
On-disk schema changes are always breaking until v1.0.
-->

## Coverage

<!--
Required. Paste or link the relevant `go test -coverprofile` total + per-package
numbers if this PR changes coverage. CI will fail the PR if repo-wide branch
coverage drops below 90%.
-->

## Checklist

- [ ] Tests added/updated and passing locally (`make verify`).
- [ ] Coverage ≥ 90% repo-wide; ≥ 90% on every package this PR touches.
- [ ] Public-API doc comments updated if the surface changed.
- [ ] `CHANGELOG.md` updated for user-visible changes (post-v0.2 only).
- [ ] No new dependencies, OR the new dep is justified in *Motivation* and its license is checked.
- [ ] No `// nocover:` annotations added without reviewer sign-off.
