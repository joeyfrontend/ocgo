# Headless, scope-enforced runs

Phase 8 of the plan. What this is: a way to run Codex CLI non-interactively
against an authorised programme, with the programme's scope enforced at the
network layer rather than documented and hoped for.

## The pieces

| piece | what it does |
|---|---|
| `internal/scopeguard` + `cmd/ocgo-guard` | an HTTP forward proxy that only dials hosts named in a scope file; everything else is refused with 403 and logged |
| `docs/findings.schema.json` | the JSON Schema the agent's final answer must satisfy |
| `scripts/hunt7-run.sh` | wires the two together and runs `codex exec` |
| `scripts/check-findings.py` | validates the findings, the egress log, and the scope's consistency |

## Scope file

```json
{
  "program": "example",
  "hosts": ["api.example.com", "*.example.com"],
  "ports": [443],
  "deny": ["admin.example.com"],
  "local_exempt": []
}
```

Matching is exact, or one-or-more subdomain levels for an entry written as
`*.example.com`. **Nothing is ever inferred from a parent hostname**: an entry
for `example.com` does not admit `api.example.com`. `deny` removes hosts a
wildcard would otherwise cover. Ports default to 443 and 80 when the file does
not list any.

`local_exempt` is separate on purpose. The model bridge runs on loopback and the
agent's own model calls must reach it, but pointing an agent at a bridge is not
programme scope, and folding the two together would silently widen the surface.
`scripts/hunt7-run.sh` passes the bridge address with `-exempt` and leaves the
scope file meaning exactly one thing.

## Why the enforcement is real

Codex honours `HTTP_PROXY` / `HTTPS_PROXY`, and it routes *all* of its traffic
through them — its own update and telemetry endpoints included. Measured on
0.162.1: with the proxy set, a run produced `DENY github.com:443`,
`DENY chatgpt.com:443`, `DENY api.github.com:443` and `DENY ab.chatgpt.com:443`
alongside the allowed requests. The agent cannot quietly reach something the
scope file does not name.

Two settings are required, and both were measured rather than assumed:

```bash
--sandbox workspace-write
-c sandbox_workspace_write.network_access=true
```

`workspace-write` alone denies the agent *all* network, loopback included, so it
cannot probe anything at all — the tool call returns curl exit 000. Enabling
network access keeps the filesystem sandbox and lets the agent reach the
network, at which point the guard is the thing deciding what it reaches.

Verified end to end on this machine: through the guard, the agent's request to
an in-scope host returned `200` and its request to `http://example.com/`
returned `403` with the guard logging `not listed in scope`.

## Running

```bash
scripts/hunt7-run.sh scope.json outdir "Probe the in-scope API for IDOR on /v2/orders."
```

Outputs, all under `outdir`:

- `findings.json` — the agent's answer, constrained by the schema
- `egress.jsonl` — every guard decision, allowed and refused, at 0600
- `guard.log`, `codex.log` — the two processes' output
- `run.json` — the run's metadata: scope, exit code, egress counts, findings and escalation counts
- `escalation.json` — findings at or above the confidence threshold

## Escalation

`scripts/check-findings.py` writes every `high`-confidence finding to
`escalation.json`, which is the queue for independent validation by a second
harness under the owner's access. It prepares the queue and stops there: **it
never submits anything.** Human review comes before any report leaves the
machine, and the same rule applies to the escalation step.
