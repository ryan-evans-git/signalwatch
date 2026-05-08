#!/usr/bin/env bash
# Idempotent branch-protection apply script. See docs/BRANCH-PROTECTION.md.
# Requires: gh auth login. Adjust REPO via env or by editing.
set -euo pipefail

REPO="${REPO:-ryan-evans-git/signalwatch}"
BRANCH="${BRANCH:-main}"

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
