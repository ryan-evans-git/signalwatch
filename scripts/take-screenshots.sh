#!/usr/bin/env bash
# Drive the screenshot pipeline end-to-end:
#   1. Build the bundled web app + the Go binary.
#   2. Start signalwatch on a high port (no auth) backed by a temp
#      sqlite file.
#   3. Seed enough fixture data for each tab to render something
#      non-empty.
#   4. Run scripts/take-screenshots.mjs via Playwright.
#   5. Tear everything down.
#
# Idempotent. Safe to re-run; replaces docs/screenshots/*.png in place.
# Requires: go, node, npm, npx. Playwright + its chromium binary are
# installed on demand to scripts/.playwright-cache so the system Node
# install isn't polluted.

set -euo pipefail

cd "$(dirname "$0")/.."

PORT=18080
DB=$(mktemp -t sw-screenshots-XXXXXX.db)
PID_FILE=$(mktemp -t sw-screenshots-pid-XXXXXX)
PLAYWRIGHT_BROWSERS_PATH="$(pwd)/scripts/.playwright-cache"
export PLAYWRIGHT_BROWSERS_PATH

cleanup() {
  if [[ -s "$PID_FILE" ]]; then
    PID=$(cat "$PID_FILE")
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  rm -f "$PID_FILE" "$DB"
}
trap cleanup EXIT

echo "→ building web + binary"
(cd web && npm ci --silent && npm run build --silent)
go build -o bin/signalwatch ./cmd/signalwatch

echo "→ starting signalwatch on 127.0.0.1:${PORT}"
cat > "$DB.config" <<EOF
http:
  addr: 127.0.0.1:${PORT}
store:
  driver: sqlite
  dsn: file:${DB}?_pragma=journal_mode(WAL)
EOF
# As of sprint 17, the cmd/signalwatch wiring always mounts the
# per-user token store, which forces auth on every /v1/* request even
# when SIGNALWATCH_API_TOKEN is unset. We mint a fixed shared token
# for this ephemeral process so the seed + UI can authenticate, and
# inject the same token into the UI's localStorage so screenshots
# capture the data tabs (not the login gate).
SW_TOKEN="screenshot-only-token-not-a-real-secret"
export SIGNALWATCH_API_TOKEN="$SW_TOKEN"
export SW_TOKEN  # consumed by the Playwright script below
./bin/signalwatch --config "$DB.config" >/tmp/sw-screenshots.log 2>&1 &
echo $! > "$PID_FILE"

# Wait for /healthz to respond (server takes ~100ms to bind).
for _ in $(seq 1 50); do
  if curl -sf "http://127.0.0.1:${PORT}/healthz" >/dev/null; then
    break
  fi
  sleep 0.1
done
if ! curl -sf "http://127.0.0.1:${PORT}/healthz" >/dev/null; then
  echo "signalwatch failed to start:" >&2
  cat /tmp/sw-screenshots.log >&2
  exit 1
fi

echo "→ seeding fixture data"
post() {
  curl -sf \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer ${SW_TOKEN}" \
    -X POST -d "$2" "http://127.0.0.1:${PORT}$1" >/dev/null
}

post /v1/rules '{
  "id":"r-cpu","name":"CPU high","enabled":true,"severity":"warning",
  "input_ref":"events",
  "condition":{"type":"pattern_match","spec":{"field":"level","kind":"contains","pattern":"ERROR"}}
}'
post /v1/rules '{
  "id":"r-orders","name":"Order failures","enabled":true,"severity":"critical",
  "input_ref":"orders",
  "condition":{"type":"pattern_match","spec":{"field":"status","kind":"contains","pattern":"failed"}}
}'
post /v1/rules '{
  "id":"r-mpg","name":"30d avg MPG","enabled":false,"severity":"info",
  "input_ref":"vehicles",
  "schedule_seconds":3600,
  "condition":{"type":"window_aggregate","spec":{"field":"mpg","agg":"avg","window":2592000000000000,"op":"<","value":5}}
}'

post /v1/subscribers '{"id":"s-oncall","name":"On-call","channels":[{"channel":"ops-slack","address":"#ops"},{"channel":"ops-email","address":"oncall@example.com"}]}'
post /v1/subscribers '{"id":"s-billing","name":"Billing team","channels":[{"channel":"ops-email","address":"billing@example.com"}]}'

post /v1/subscriptions '{"id":"subscr-oncall-cpu","subscriber_id":"s-oncall","rule_id":"r-cpu","dwell_seconds":120,"repeat_interval_seconds":900,"notify_on_resolve":true}'
post /v1/subscriptions '{"id":"subscr-billing-orders","subscriber_id":"s-billing","rule_id":"r-orders","dwell_seconds":0,"repeat_interval_seconds":0,"notify_on_resolve":true}'
# A one-shot subscription so the Subscriptions screenshot visually
# distinguishes "one-shot" from "recurring" via the Mode pill.
post /v1/subscriptions '{"id":"subscr-oncall-orders-once","subscriber_id":"s-oncall","rule_id":"r-orders","dwell_seconds":0,"repeat_interval_seconds":0,"notify_on_resolve":false,"one_shot":true}'

# Push enough events to open + close an incident so the live-state
# and incidents tabs show data.
post /v1/events '{"input_ref":"events","record":{"level":"ERROR","host":"web-1"}}'
sleep 0.3
post /v1/events '{"input_ref":"events","record":{"level":"INFO","host":"web-1"}}'
post /v1/events '{"input_ref":"orders","record":{"status":"failed","order_id":"o-42"}}'
sleep 0.3

echo "→ installing playwright + chromium (cached)"
mkdir -p "$PLAYWRIGHT_BROWSERS_PATH"
(cd scripts && npm install --silent --no-audit --no-fund)
(cd scripts && npx playwright install chromium >/dev/null)

echo "→ taking screenshots"
SW_BASE_URL="http://127.0.0.1:${PORT}" node scripts/take-screenshots.mjs

echo "✓ done — see docs/screenshots/"
