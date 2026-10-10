# codex-opencode

Run Codex CLI against OpenCode Go models.

This is a fork of [ocgo](https://github.com/emanuelcasco/ocgo) by Emanuel Casco.
The original starts a local proxy and lets Claude Code and Codex use an OpenCode
Go subscription. This fork does three extra things:

- The Responses API adapter is a real translator, so Codex gets working tool
  calls, streaming and context compaction.
- The proxy points at the endpoint OpenCode Go serves today, and sends the
  workspace header it requires.
- There is a spend guard and a network scope guard.

## What works

Codex CLI, headless or interactive, against GLM, DeepSeek, Qwen, MiniMax, Kimi,
LongCat, MiMo and others. Long sessions work, including the point where Codex
compacts its context. Cost is tracked per model.

## Requirements

- Go 1.22 or newer
- Codex CLI on your PATH
- An OpenCode Go subscription, which gives you an API key and a workspace id

## Build

```bash
go build -o bin/ocgo ./cmd/ocgo
```

## Setup

You need two values: the API key and the workspace id. The workspace id looks
like `wrk_...` and the proxy cannot work without it. OpenCode Go answers
`403 Workspace selection is required` when it is missing.

```bash
bin/ocgo setup --api-key "your-key" --org-id "wrk_yourworkspace"
```

The key is written to `~/.config/ocgo/config.json` with permissions 0600. You
can also pass both as environment variables, `OCGO_API_KEY` and `OCGO_ORG_ID`.

If the endpoint ever moves again, override it instead of waiting for a release:

```bash
bin/ocgo setup --api-key "..." --org-id "wrk_..." --base-url "https://example/v1"
```

## Run

One command, which starts the proxy and then Codex:

```bash
bin/ocgo launch codex --model glm-5.3-flash
```

Or run the two separately, so the proxy stays up between sessions:

```bash
bin/ocgo serve &
OPENAI_API_KEY=ocgo codex --profile ocgo-launch -m glm-5.3-flash
```

Add `-- exec --sandbox workspace-write "your task"` after the model name to run
headless instead of opening the interactive UI.

## Models

`bin/ocgo list` prints every model the API serves, with its monthly allowance,
and marks the free ones. It also lists separately the models OpenCode Go
advertises but does not actually serve, so you do not pick one by mistake.

Some useful ones:

| model | monthly allowance |
|---|---|
| `glm-5.3-flash` | $60, the default |
| `glm-5.3` | $15 |
| `deepseek-v4.1-flash` | $60 |
| `minimax-m3` | $60 |
| `space-bunny` | $30 |
| `longcat-2.5-preview-free` | free |
| `step-5-preview-free` | free |

## Spending

OpenCode Go meters each model separately, with a five hour window at 20 percent
of that model's monthly limit, a weekly window at 50 percent, and the monthly
limit itself. The proxy keeps a ledger at `~/.config/ocgo/usage.json` and
refuses a request when a window is exhausted, rather than letting it fail
halfway through.

```bash
bin/ocgo usage
```

The free models have no allowance to exhaust, so they are never refused and
never billed.

## Sandboxed runs

`cmd/ocgo-guard` is a small HTTP proxy that only dials hosts you list. Point a
sandboxed agent at it and anything outside the list is refused and logged. This
is for running an agent against systems you are allowed to test, without
trusting it to stay where it was told.

```bash
bin/ocgo-guard -scope scope.json -addr 127.0.0.1:8888 -log decisions.jsonl
```

See `docs/scope-enforcement.md` for the scope file format and a wrapper that
runs Codex through it.

## Documentation

- `docs/codex-requests.md`: what Codex actually sends, measured.
- `docs/scope-enforcement.md`: the network scope guard and the scoped run wrapper.
- `docs/eval-results.md`: how the translator scored on a seeded test target.
- `docs/findings.schema.json`: the JSON shape a scoped run must answer with.

## Tests

```bash
go test ./...
```

The translator tests replay recorded sessions from `testdata/fixtures`, and a
fuzz target splits those recordings at arbitrary byte boundaries and checks that
tool call arguments still reassemble.

## Limitations

- Some models need the Anthropic wire format rather than the OpenAI one. The
  proxy picks per model and falls back automatically when it is told the
  protocol is unsupported.
- Spend for models that use the Anthropic path is not recorded, so `ocgo usage`
  under-counts those.
- Token counting is an estimate. The real figure arrives with every response.

## License

MIT, the same as the original project.
