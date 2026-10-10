# What Codex sends

Notes from recording real Codex CLI sessions through the proxy, so the
translator can be written against the traffic rather than against the API
documentation. Everything here was measured on `codex-cli 0.162.1` on Linux.

To take your own recording, see `scripts/capture.sh`. It runs a session against
a scripted local upstream, so it needs no OpenCode Go credential and spends
nothing.

## Session identity

Codex sends the same identifier in four headers:

| header | example |
|---|---|
| `Session-Id` | `01a1258c-844d-7431-9343-3ecbf4613871` |
| `Thread-Id` | the same value |
| `X-Client-Request-Id` | the same value |
| `X-Codex-Window-Id` | the same value with `:0` appended |

The body also carries `prompt_cache_key` with the same value, so either source
gives the same answer.

One trap worth knowing: Go's `http.Header.Get` only canonicalises names
separated by hyphens. A lookup for `session_id` is passed through unchanged and
never matches the stored `Session-Id` key, so that spelling silently never
works. The resolver uses the canonical spellings for this reason.

## The tools it offers

Nine tools, in three shapes, and no `apply_patch` or `shell`:

| type | names |
|---|---|
| `function` | `exec_command`, `write_stdin`, `request_user_input`, `view_image`, `get_goal`, `create_goal`, `update_goal` |
| `namespace` | `multi_agent_v1`, which contains further function tools |
| `web_search` | hosted by the API provider, no function schema |

This matters because the obvious design, mapping `apply_patch` and `shell`, does
not apply to this version. Commands run through `exec_command`, and file edits
happen by running shell commands. The model catalogue this project writes says
`shell_type: "shell_command"`, which matches what is observed.

`namespace` is a type the translator drops on purpose. Flattening the inner
functions into top level tools would produce names the client cannot map back to
the namespace, so a call would return as an unknown tool. Dropping them makes the
sub-agent tools unavailable instead of broken, and logs one warning per session.

## The items in `input`

- `message` with role `developer` or `user`. Content is always a list of parts,
  each `input_text` or, with an image attached, `input_image`.
- `function_call` with `id`, `call_id`, `name` and `arguments`. In every call
  observed, `id` and `call_id` were the same string.
- `function_call_output` with `id`, `call_id` and `output`. The output is a
  plain string wrapping Codex's own block of exit code, timing and stdout.

No `reasoning` items appear, because the bridge never sent any back. With
`store: false` the client sends its whole history each turn, so it can only
return what it was given.

## Other request fields

| field | value |
|---|---|
| `store` | `false`, always |
| `prompt_cache_key` | the session identifier |
| `previous_response_id` | never sent |
| `reasoning` | `{"effort": "minimal"}` unless the profile says otherwise |
| `include` | `["reasoning.encrypted_content"]` |
| `parallel_tool_calls` | `true` on a normal turn, `false` on a compaction |
| `tool_choice` | `"auto"` |
| `instructions` | absent. Instructions arrive as `developer` messages |

`max_output_tokens`, `temperature`, `text` and `truncation` are all absent.

## Compaction is not a separate endpoint

In a run long enough to need compaction, every request still went to
`/v1/responses`. The compaction request carries `request_kind: "compaction"` in
the turn metadata, sends an empty `tools` array, and appends one user message:

> You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary
> for another LLM that will resume the task.

The model's text reply becomes the new context. This is why the usage figures on
`response.completed` matter: they are what Codex uses to decide that compaction
is due. A bridge that reports zero tokens never compacts, and a long session
quietly fills the window instead.

## What the model side returns

Measured against the real endpoint with GLM-5.3-Flash:

- Reasoning arrives in `reasoning_content`, both as stream deltas and on the
  non-streaming message. This is the field the translator reads.
- `usage` is present on every stream chunk, not only the last one.
- Tool call arguments arrived as a single chunk in every sample taken here. The
  translator still accumulates them by index and handles splits, and the fuzzer
  covers splits, but a real split has not been observed on this model.

## The endpoint

The proxy calls `https://opencode.ai/inference/go`, with `/openai/v1/chat/completions`
for the OpenAI shape and `/anthropic/v1/messages` for the Anthropic shape. Both
need two headers beyond the credential, and both are load bearing:

```
without x-opencode-session            400 MissingSessionID
with it, without x-opencode-org-id    403 Workspace selection is required
with both                             200
```

The older `https://opencode.ai/zen/go` inference path rejects these credentials
with `401 Invalid API key`. Only the model listing still answers there, which is
why the catalogue is fetched from the old path while inference uses the new one.
