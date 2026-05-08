# Branch protection

`main` is the only protected branch. Feature branches are unprotected and may be force-pushed by their author until the PR opens.

## Required status checks

Every job below must pass before a PR can merge to `main`. The names match the GitHub UI exactly so the apply-script doesn't drift.

| Check | Workflow | Job name |
|---|---|---|
| Lint + tests + coverage (Go 1.24) | `ci` | `lint + test + coverage (go1.24.x)` |
| Lint + tests + coverage (Go 1.25) | `ci` | `lint + test + coverage (go1.25.x)` |
| `gosec` + `govulncheck` | `ci` | `gosec + govulncheck` |
| License policy | `ci` | `license policy (block GPL/AGPL/SSPL/BUSL/Elastic/Commons-Clause)` |
| Trivy filesystem scan | `ci` | `trivy (filesystem)` |
| Web typecheck + build | `ci` | `web (typecheck + build)` |
| Binary build sanity | `ci` | `binary build (sanity)` |
| CodeQL (Go) | `codeql` | `analyze (go)` |
| CodeQL (TypeScript) | `codeql` | `analyze (javascript-typescript)` |
| Gitleaks | `gitleaks` | `gitleaks` |

## Other rules

- **Require PR before merging** — yes; require approving review from at least one CODEOWNER.
- **Dismiss stale reviews on new commits** — yes.
- **Require linear history** — yes; merge strategy is squash-only.
- **Require signed commits** — yes.
- **Block force pushes** — yes.
- **Block deletions** — yes.
- **Allow administrators to bypass** — no. Maintainers must follow the same flow.

## Applying with `gh`

The script below is idempotent. Run it once when the public repo is created, then again whenever the required checks list changes.

```bash
#!/usr/bin/env bash
# docs/scripts/apply-branch-protection.sh
# Requires: gh auth login. Adjust REPO if the slug changes.
set -euo pipefail

REPO="ryan-evans-git/signalwatch"
BRANCH="main"

REQUIRED_CHECKS='[
  "lint + test + coverage (go1.24.x)",
  "lint + test + coverage (go1.25.x)",
  "gosec + govulncheck",
  "license policy (block GPL/AGPL/SSPL/BUSL/Elastic/Commons-Clause)",
  "trivy (filesystem)",
  "web (typecheck + build)",
  "binary build (sanity)",
  "analyze (go)",
  "analyze (javascript-typescript)",
  "gitleaks"
]'

gh api \
  --method PUT \
  -H "Accept: application/vnd.github+json" \
  "repos/${REPO}/branches/${BRANCH}/protection" \
  --input - <<EOF
{
  "required_status_checks": {
    "strict": true,
    "contexts": ${REQUIRED_CHECKS}
  },
  "enforce_admins": true,
  "required_pull_request_reviews": {
    "dismiss_stale_reviews": true,
    "require_code_owner_reviews": true,
    "required_approving_review_count": 1
  },
  "restrictions": null,
  "required_linear_history": true,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "required_conversation_resolution": true,
  "required_signatures": true
}
EOF

echo "Branch protection applied to ${REPO}@${BRANCH}."
```

## Bootstrap order (first push to GitHub)

`required_signatures` and required-status checks both reference state that has to exist *before* protection is applied. Order matters:

1. Create the empty public repo on GitHub (via UI or `gh repo create`).
2. Push the initial branch (e.g. `feature/sprint-1-foundations`); do **not** merge to `main` yet.
3. Open a sentinel PR against `main`. Watch every workflow run at least once so the check names register with GitHub.
4. Run `apply-branch-protection.sh`. The PUT requires that every named context has been seen on the branch at least once.
5. From here on, every PR must pass the gates before squash-merging.
