# Scope enforcement

A small proxy that only dials hosts you list, so an agent can be pointed at
systems you are allowed to test without relying on it to stay where it was
told.

Two parts:

| part | what it does |
|---|---|
| `cmd/ocgo-guard` | an HTTP forward proxy that refuses any host not in a scope file, and logs the decision |
| `scripts/scoped-run.sh` | runs Codex headless through that proxy, with a required output shape |

## The scope file

```json
{
  "program": "example",
  "hosts": ["api.example.com", "*.example.com"],
  "ports": [443],
  "deny": ["admin.example.com"],
  "local_exempt": []
}
```

Matching is exact, except that `*.example.com` matches any depth of subdomain
below it. Nothing is inferred from a parent name: an entry for `example.com`
does not admit `api.example.com`. Entries in `deny` win over a wildcard. When
`ports` is absent, 443 and 80 are allowed.

`local_exempt` is separate on purpose. The model bridge runs on loopback and the
agent has to reach it, but pointing an agent at a bridge is not part of a
target's scope, and folding the two together would quietly widen what is
reachable. `scripts/scoped-run.sh` passes the bridge address here and leaves the
scope file meaning one thing.

## Running it

```bash
bin/ocgo-guard -scope scope.json -addr 127.0.0.1:8888 -log decisions.jsonl
```

Every request is written to the log as JSON, allowed or refused, with the host,
the port and the reason. Then point an agent at it:

```bash
HTTP_PROXY=http://127.0.0.1:8888 HTTPS_PROXY=http://127.0.0.1:8888 \
  your-agent
```

Or use the wrapper on a target defined in `evals/targets.json`:

```bash
scripts/scoped-run.sh scope.json outdir "Probe the API for order access control."
```

It writes the agent's answer to `findings.json`, every egress decision to
`egress.jsonl`, and a summary to `run.json`, then checks the answer against
`docs/findings.schema.json` and reports what failed.

## Why this is enforcement and not advice

Codex honours `HTTP_PROXY` and sends all of its traffic through it, including
its own update and telemetry requests. In a test run it refused `github.com`,
`chatgpt.com`, `api.github.com` and `ab.chatgpt.com` alongside the target, which
is what you want: the agent cannot quietly reach something the scope file does
not name.

Two settings are needed, and both were found by testing rather than by reading:

```
--sandbox workspace-write
-c sandbox_workspace_write.network_access=true
```

`workspace-write` on its own denies the agent all network access, loopback
included, so it cannot probe anything at all and its commands fail with a
connection error. Turning network access on keeps the filesystem sandbox and
lets the guard decide what the agent reaches.

## What the guard does not do

It decides where traffic may go. It does not read the traffic, and it does not
know anything about the target. A run against an in-scope host can still be
harmful if the request itself is, which is why the wrapper only prepares
evidence and never submits anything.
