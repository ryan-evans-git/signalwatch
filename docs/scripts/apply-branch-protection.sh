#!/usr/bin/env bash
# Idempotent branch-protection apply script. See docs/BRANCH-PROTECTION.md.
# Requires: gh auth login. Adjust REPO via env or by editing.
set -euo pipefail

REPO="${REPO:-ryan-evans-git/signalwatch}"
BRANCH="${BRANCH:-main}"

REQUIRED_CHECKS='[
  "lint + test + coverage (go1.24.x)",
  "lint + test + coverage (go1.25.x)",
  "test (postgres integration)",
  "gosec + govulncheck",
  "license policy (block GPL/AGPL/SSPL/BUSL/Elastic/Commons-Clause)",
  "trivy (filesystem)",
  "web (typecheck + build)",
  "binary build (sanity)",
  "analyze (go)",
  "analyze (javascript-typescript)",
  "gitleaks"
]'

#
# required_pull_request_reviews is intentionally null. CODEOWNERS lists
# only the solo maintainer, and require_code_owner_reviews + enforce_admins
# would block self-merges entirely. Flip this on once a second
# maintainer/collaborator joins.
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
  "required_pull_request_reviews": null,
  "restrictions": null,
  "required_linear_history": true,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "required_conversation_resolution": true,
  "required_signatures": true
}
EOF

echo "Branch protection applied to ${REPO}@${BRANCH}."
