# MCP integration

signalwatch ships a strict OpenAPI 3.1 description of its HTTP API and serves it from the running binary. Any of the open-source **OpenAPI → MCP** bridges can wrap the API and expose every endpoint as an MCP tool.

## What the server gives you

- **`GET /openapi.yaml`** — the canonical, version-controlled spec, served verbatim from the binary. Unauthenticated (like `/healthz`) so an MCP server can discover the schema before it has a credential.
- **`GET /openapi.json`** — the same document, JSON-encoded.
- The Go test suite (`internal/api/openapi_test.go`) enforces a few MCP-friendly invariants on every build:
  - Every operation has a unique `operationId` — bridges use this as the tool name.
  - Every operation has a `summary` and `tags` — bridges use these for tool documentation and grouping.
  - The set of routes in the spec matches the set of routes `api.Mount` registers — no drift between code and schema.

The spec also lives at `docs/openapi.yaml` (a symlink to the embed source) so it's visible without running the binary.

## Recommended adapter shape

Run an OpenAPI-aware MCP server alongside signalwatch. The adapter fetches `/openapi.yaml`, maps each operation to an MCP tool, and forwards calls — supplying the bearer token from its own configuration.

```
┌───────────┐      MCP        ┌──────────────────┐    HTTP+bearer   ┌──────────────┐
│  agent /  │ <─────────────> │ openapi-mcp      │ <──────────────> │ signalwatch  │
│  Claude   │  (stdio/sse)    │ bridge           │   /openapi.yaml  │  service     │
└───────────┘                 └──────────────────┘   /v1/*          └──────────────┘
```

Concrete bridges that work out of the box:

- [`mcp-openapi-proxy`](https://github.com/matthewhand/mcp-openapi-proxy) — Python; reads an OpenAPI URL, exposes each op as a tool.
- [`openapi-mcp-server`](https://github.com/snaggle-ai/openapi-mcp-server) — TypeScript; reads an OpenAPI URL + a bearer token, exposes ops as tools.
- [`openapi-to-mcp`](https://github.com/janwilmake/openapi-to-mcp) — codegen path: produces a static MCP server from the spec.

The exact CLI varies by bridge, but the common shape is:

```bash
# Issue a token (admin scope, 90-day expiry) via the API.
SECRET=$(curl -sX POST -H "Authorization: Bearer $SIGNALWATCH_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"mcp-bridge","scopes":["admin"],"expires_in":"2160h"}' \
  http://localhost:8080/v1/auth/tokens | jq -r .secret)

# Spawn the bridge pointed at signalwatch.
SPEC_URL=http://localhost:8080/openapi.yaml \
AUTH_BEARER="$SECRET" \
  npx openapi-mcp-server
```

The bridge then registers MCP tools named after our `operationId`s:

| MCP tool name | What it does |
|---|---|
| `listRules` | List every rule (enabled and disabled). |
| `createRule` | Create a rule from a JSON body. |
| `validateRule` | Compile-test a rule without persisting it. |
| `listSubscriptions` | List subscriptions, including `one_shot` flag. |
| `createSubscription` | Bind a subscriber to a rule; set `one_shot:true` for one-time-event delivery. |
| `postEvent` | Push an event for evaluation. |
| `listIncidents` | Read the incident timeline. |
| `exportIncidents` | Export incidents as CSV or JSON. |
| `listNotifications` | Read the notification audit log. |
| `listLiveStates` | Snapshot every rule's FIRING / OK state. |
| `issueToken` / `listTokens` / `revokeToken` | Manage per-user tokens. |
| `getAuthStatus` / `getHealth` | Discovery probes (no auth). |

Each tool's parameters mirror the operation's request body / path parameters / query parameters exactly. An agent reading the descriptions can choose between, for example, `createSubscription` with `one_shot:true` (single notification, ever) vs. `one_shot:false` (recurring notifications across new incidents).

## Scope guidance

The bearer token you hand the bridge controls what the agent can do:

- **Issue a `read`-scoped token** if the agent is a monitoring/reporting layer that should never mutate anything. Mutating operations will return `403` and the agent will know to fall back.
- **Issue an `admin`-scoped token** if the agent needs to create rules, manage subscriptions, or revoke tokens.

Per-user tokens are revocable individually (`DELETE /v1/auth/tokens/{id}`), so giving each MCP bridge its own token lets you cut access at agent granularity without rotating everyone else.

## Keeping the spec honest

If you add a new endpoint:

1. Add a row to `gatedRoutes()` or `authRoutes()` in `internal/api/api.go`, plus the matching handler.
2. Add a corresponding operation to `internal/api/openapi.yaml` with an `operationId`, a `summary`, `tags`, and request/response schemas.
3. Run `go test ./internal/api/...`. The drift checks fail loudly if either side is out of sync.

The spec is the contract MCP adapters depend on — the drift tests are how we keep that contract honest.
