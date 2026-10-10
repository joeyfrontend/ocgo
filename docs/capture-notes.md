# Capture notes — what Codex CLI actually sends

Source of every claim below: traces in `~/ocgo-capture/traces` and
`~/ocgo-compact/traces`, produced by `~/capture.sh` (real `codex exec` →
`ocgo serve --trace` → scripted local upstream). Reproduce with:

```bash
TURNS=12 bash ~/capture.sh ~/ocgo-compact "..." "-prompt-tokens 960000"
```

**Version measured: `codex-cli 0.162.1` (Linux musl), `Originator: codex_exec`.**
Every item below is an observation from those traces, not from documentation.

## Which header carries the session id?

Codex sends the same UUID in four places, so no guessing is needed:

| header | example |
|---|---|
| `Session-Id` | `01a1258c-844d-7431-9343-3ecbf4613871` |
| `Thread-Id` | same value |
| `X-Client-Request-Id` | same value |
| `X-Codex-Window-Id` | `<session>:0` (window suffix increments per compaction) |

Also present: `Originator: codex_exec`, `Accept: text/event-stream`,
`X-Codex-Beta-Features: remote_compaction_v2`, and `X-Codex-Turn-Metadata`
(a JSON blob repeating `session_id`, `thread_id`, `turn_id`, `window_id`,
`sandbox_mode`, `workspaces`).

The body also carries `prompt_cache_key`, and it equals the same UUID — so the
plan's resolution branches 1 and 2 agree in practice.

**Trap found:** `http.Header.Get("session_id")` never matches, because
canonicalisation only rewrites `-`-separated names: an underscore name is
returned unchanged and looked up verbatim. The first capture resolved via
`thread-id` for this reason. `internal/session` now lists canonical names.

## Which tool types appear in `tools`?

Nine tools, three different `type` values — and **no `function`-vs-`custom`
split**, because this Codex version has neither `apply_patch` nor `shell`:

| type | names |
|---|---|
| `function` | `exec_command`, `write_stdin`, `request_user_input`, `view_image`, `get_goal`, `create_goal`, `update_goal` |
| `namespace` | `multi_agent_v1` (a container whose `tools` array holds further `function` tools: `close_agent`, `resume_agent`, …) |
| `web_search` | hosted, no `name`; body `{"type":"web_search","external_web_access":false}` |

This contradicts the plan's assumption that the interesting tools are `shell`
and `apply_patch`. On 0.162.1 the command runner is **`exec_command`** with a
string parameter `cmd` (plus `justification`, `login`, `max_output_tokens`,
`prefix_rule`, `sandbox_permissions`), and file edits happen by running shell
commands. The model catalog ocgo writes still says `shell_type: "shell_command"`
and `apply_patch_tool_type: null`, which matches the observed behaviour.

`namespace` is a tool type the current bridge silently drops. That is survivable
(sub-agent tools simply become unavailable) but must be a deliberate, logged
decision rather than an accident of a `t.Name != "" && t.Type == "function"`
filter.

## Which `input` item types appear?

- `message` — roles `developer` (system instructions, including the skills and
  permissions blocks) and `user`. Content is always a parts array
  (`input_text`, and `input_image` when an image is attached).
- `function_call` — `{id, call_id, name, arguments}`; `id` and `call_id` were
  identical in every observed call.
- `function_call_output` — `{id, call_id, output}`; `output` is a plain string
  wrapping Codex's own `Chunk ID / Wall time / Process exited / Output:` block.

No `reasoning` items were observed. That is expected: the bridge never emitted a
`reasoning` output item, and `store: false` means Codex only echoes back what it
received. Whether Codex round-trips `reasoning` items must be re-measured once
the translator emits them (Phase 3 follow-up).

## Does Codex send `prompt_cache_key`, `store`, `previous_response_id`, `reasoning`?

| field | value | note |
|---|---|---|
| `store` | `false` | always; the client resends full history each turn |
| `prompt_cache_key` | session UUID | |
| `previous_response_id` | absent | never sent |
| `reasoning` | `{"effort":"minimal"}` | mirrors the profile's `model_reasoning_effort` |
| `include` | `["reasoning.encrypted_content"]` | Codex asks for reasoning it can round-trip |
| `parallel_tool_calls` | `true` on turns, `false` on compaction | |
| `tool_choice` | `"auto"` | |
| `instructions` | absent | system text arrives as `developer` messages instead |
| `max_output_tokens`, `temperature`, `text`, `truncation` | absent | |
| `client_metadata` | present | turn/window/installation ids and the raw turn-metadata JSON |

Full top-level key set: `client_metadata`, `include`, `input`, `model`,
`parallel_tool_calls`, `prompt_cache_key`, `reasoning`, `store`, `stream`,
`tool_choice`, `tools`.

## Does Codex call any endpoint other than `/v1/responses`?

**No.** In the 13-request compaction run every request went to `/v1/responses`.
A catch-all handler now records any other path, so this stays observable.

**Compaction is not a separate endpoint.** It is the same `/v1/responses` call
with `request_kind: "compaction"` in `client_metadata.x-codex-turn-metadata`,
`tools: []`, `parallel_tool_calls: false`, and an extra final user message:

> You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary
> for another LLM that will resume the task. …

The model's text reply becomes the new context. This is why reported usage
matters: with usage forced to 960,000 against a 1,000,000-token catalog entry,
Codex compacted on **every** turn and kept working; the bridge reported the
`response.completed` usage and Codex acted on it.

## What does GLM return for reasoning and tool calls?

**Not yet measured — no live credential.** `OPENCODE_API_KEY` in the
environment and the `opencode-go` entry in `~/.local/share/opencode/auth.json`
both return `401 {"type":"error","error":{"type":"AuthError","message":"Invalid
API key."}}` from `https://opencode.ai/zen/go/v1/chat/completions`, across
`Authorization: Bearer`, bare `Authorization`, and `x-api-key`, on both
`/zen/go/v1/chat/completions` and `/zen/v1/chat/completions`. `GET /v1/models`
succeeds unauthenticated and does list `glm-5.3-flash` and `glm-5.3`.

Until a working Go key is supplied, the mock upstream defines the shape:
`reasoning_content` deltas (the Z.ai convention, and what the existing
`parseOpenAIStreamChunk` already reads) and `tool_calls[].index` with arguments
split mid-JSON. Both must be re-confirmed against the real endpoint before the
`reasoning` output item is considered done.

## Event sequence Codex accepted

The existing minimal adapter emitted exactly this and Codex completed multi-turn
tool loops, including compaction, without complaint:

```
response.created
response.output_item.added
response.content_part.added        (text only)
response.output_text.delta         (repeated)
response.output_text.done
response.output_item.added         (per tool call)
response.function_call_arguments.delta (repeated)
response.output_item.done
response.function_call_arguments.done
response.completed
```

Missing versus the Responses API and worth adding in Phase 3:
`response.in_progress`, `response.content_part.done`, `sequence_number`, unique
item ids (the current code hard-codes `msg_ocgo`), and a `reasoning` output item.
