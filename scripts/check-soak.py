#!/usr/bin/env python3
"""Assert the properties a soak run must hold, from its trace.

    python3 scripts/check-soak.py <trace-dir> [--codex-exit N]

Fails (non-zero exit) when any of these is violated:

  * an upstream request returned an auth/session error
  * the translator reported a malformed tool call
  * the session id changed within a conversation
  * the conversation never compacted, when compaction was requested
  * the Codex process exited non-zero
"""
import json
import pathlib
import sys


def load(trace_dir):
    records = []
    for path in sorted(pathlib.Path(trace_dir).glob("*.jsonl")):
        for line in path.read_text().splitlines():
            if line.strip():
                records.append(json.loads(line))
    return records


def request_kind(body):
    meta = (body or {}).get("client_metadata") or {}
    raw = meta.get("x-codex-turn-metadata")
    if isinstance(raw, str):
        try:
            return json.loads(raw).get("request_kind") or "turn"
        except ValueError:
            pass
    return "turn"


def main():
    args = sys.argv[1:]
    if not args:
        print(__doc__)
        return 2
    trace_dir = args[0]
    codex_exit = None
    if "--codex-exit" in args:
        codex_exit = int(args[args.index("--codex-exit") + 1])

    records = load(trace_dir)
    if not records:
        print("FAIL: no trace records found in", trace_dir)
        return 1

    failures = []
    inbound = [r for r in records if r["kind"] == "inbound_request"]
    kinds = {"turn": 0, "compaction": 0, "other": 0}
    for r in inbound:
        body = r["body"]
        if isinstance(body, str):
            body = json.loads(body)
        kind = request_kind(body)
        kinds[kind if kind in kinds else "other"] += 1

    # Session identity must not change mid-conversation.
    sessions = {}
    for r in records:
        if r["kind"] == "upstream_request":
            value = (r.get("headers") or {}).get("X-Opencode-Session", "")
            sessions.setdefault(r["conv"], set()).add(value)
    for conv, values in sessions.items():
        if len(values) != 1 or "" in values:
            failures.append(f"session id unstable for {conv}: {sorted(values)}")

    # No upstream auth or session failures.
    for r in records:
        if r["kind"] != "error":
            continue
        text = r.get("text", "")
        if "MissingSessionID" in text:
            failures.append("upstream rejected a request with MissingSessionID")
        if "upstream status 401" in text or "upstream status 403" in text:
            failures.append(f"upstream auth failure: {text}")

    # No malformed tool calls.
    for r in records:
        if r["kind"] == "note" and "not valid JSON" in r.get("text", ""):
            failures.append("translator reported malformed tool arguments: " + r["text"])

    # Compaction must actually happen when it was provoked.
    if kinds["compaction"] == 0:
        failures.append("no compaction request observed")

    if codex_exit is not None and codex_exit != 0:
        failures.append(f"codex exited {codex_exit}")

    print(f"trace records:   {len(records)}")
    print(f"inbound:         {len(inbound)}  (turn={kinds['turn']} compaction={kinds['compaction']})")
    print(f"conversations:   {len(sessions)}")
    print(f"upstream calls:  {sum(1 for r in records if r['kind'] == 'upstream_request')}")
    print(f"warnings:        {sum(1 for r in records if r['kind'] == 'note' and 'translation' in r.get('text',''))}")

    if failures:
        print("\nFAIL:")
        for f in failures:
            print("  -", f)
        return 1
    print("\nPASS: no session errors, no malformed calls, compaction observed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
