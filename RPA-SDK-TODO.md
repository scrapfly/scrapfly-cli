# RPA SDK coverage TODO

**Status:** draft, 2026-05-16
**Owner:** SDK / Integrations
**Driving change:** RPA AI Agent is moving from a dashboard-only surface to
a programmable product. The CLI is the canonical SDK; this file tracks the
work to surface the RPA endpoints the dashboard already uses.

## Why this exists

The internal `pkg/agent/bench` package in scrapfly-api exercises the same
public HTTP + WS surfaces a customer SDK would call. Every benchmarked
endpoint is therefore SDK-eligible. This file is the punch list to bring
the CLI (and through it, the agent-rules skill at
`integrations/skills/scrapfly-agent-rules/`) up to parity with the
dashboard's RPA tab.

## Endpoints to expose

| Endpoint | Verb | CLI subcommand (proposed) | Priority |
|----------|------|---------------------------|----------|
| `POST /v1/rpa/agents` | Create an agent | `scrapfly rpa agent create` | P0 |
| `GET /v1/rpa/agents` | List agents | `scrapfly rpa agent list` | P0 |
| `GET /v1/rpa/agents/{id}` | Get one agent | `scrapfly rpa agent get <id>` | P1 |
| `PATCH /v1/rpa/agents/{id}` | Update (prompt, schemas, vision flag) | `scrapfly rpa agent update <id>` | P1 |
| `DELETE /v1/rpa/agents/{id}` | Delete | `scrapfly rpa agent delete <id>` | P1 |
| `POST /v1/rpa/agents/{id}/run` | Launch a run | `scrapfly rpa agent run <id>` | P0 |
| `GET /v1/rpa/runs/{id}` | Read run state | `scrapfly rpa run get <id>` | P0 |
| `GET /v1/rpa/runs/{id}/events?since_seq=N` | Tail historical events | `scrapfly rpa run tail <id>` | P1 |
| `WSS /v1/rpa/runs/{id}/observe?token=...` | Live frame stream | `scrapfly rpa run watch <id>` | P0 |
| `POST /v1/rpa/runs/{id}/interaction` | Reply to interaction\_required | `scrapfly rpa run reply <id>` | P1 |
| `POST /v1/rpa/workflows` | Workflow CRUD | `scrapfly rpa workflow ...` | P2 |
| `POST /v1/rpa/profiles` | Profile CRUD | `scrapfly rpa profile ...` | P2 |
| `GET /v1/rpa/runs/{id}/artifacts/{art_id}/signed-url` | Mint artifact URL | `scrapfly rpa run artifact <id>` | P1 |

P0 = needed to run any agent from the CLI.
P1 = needed for full operator parity with the dashboard.
P2 = lower priority while the Agent surface stabilises.

## Auth and headers

- Standard `X-API-Key` header (the CLI already wires this).
- `X-Vault-Key` header on run launch when the bound Profile carries a vault
  binding. The CLI must NEVER persist the vault key to disk. Read it from
  stdin or an env var (`SCRAPFLY_VAULT_KEY`) and wipe the local copy after
  the run completes. Reuse the secrets/zeroize pattern already in
  `internal/secret`.

## Output envelope

Every RPA verb must return the standard CLI envelope:

```json
{ "success": true, "product": "rpa", "data": { ... } }
```

The `data` shape mirrors the dashboard's API responses verbatim. Failure
shape:

```json
{
  "success": false,
  "product": "rpa",
  "error": { "type": "...", "code": "...", "field": "...", "message": "..." }
}
```

## Streaming watch

`scrapfly rpa run watch <id>` connects to the WS observer and emits one
JSON object per frame on stdout. Each frame:

```json
{ "type": "agent.step", "payload": { ... }, "ts": "2026-05-16T13:00:00Z" }
```

The terminal frame is `run.completed` or `run.failed`; on either the
binary exits 0 or 1 respectively. Add `--until-status completed` for
batch-friendly waits.

## Bench parity

The `pkg/agent/bench` benchmark inside scrapfly-api uses HTTP + WS only
(no in-process imports). Once these CLI subcommands ship, the bench's
`Harness` can be reimplemented as a thin shell over `scrapfly rpa
agent create` + `scrapfly rpa agent run` + `scrapfly rpa run watch`,
which gives customers their own private bench against their own task
catalog. Track that follow-up under "bench-as-a-product" once the
CLI surface lands.

## Doc deliverables

For every CLI verb shipped:

- Update `integrations/cli/docs/reference/go-reference.txt` (auto-generated).
- Add a one-page example under `integrations/cli/examples/`.
- Backfill the customer doc under
  `apps/scrapfly/web-app/src/Template/Docs/cli/` (mirror the existing
  product subpages for shape).

## Internal references

- HTTP server routes: [pkg/agent/server.go](../../apps/scrapfly/api/scrapfly-api/pkg/agent/server.go)
- Agent runtime: [pkg/agent/agent_runtime.go](../../apps/scrapfly/api/scrapfly-api/pkg/agent/agent_runtime.go)
- WS observer protocol: [pkg/agent/ws_observer.go](../../apps/scrapfly/api/scrapfly-api/pkg/agent/ws_observer.go)
- Bench (uses HTTP + WS): [pkg/agent/bench/](../../apps/scrapfly/api/scrapfly-api/pkg/agent/bench/)
- Design doc: [docs/rpa/README.md](../../docs/rpa/README.md), [docs/rpa/bench.md](../../docs/rpa/bench.md)
