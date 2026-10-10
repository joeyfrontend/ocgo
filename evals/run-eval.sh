#!/usr/bin/env bash
# Run one evaluation: a harness against a seeded target, scored.
#
#   evals/run-eval.sh <harness> <target> [--mock]
#
# harness: bridge | opencode | claude
# target:  a key in evals/targets.json
#
# --mock drives the bridge with the scripted local upstream instead of OpenCode
# Go. That is a harness smoke test, not a measurement of the model: the findings
# are the ones the script asked for. Mock rows are written with
# counts_as_result=false so they can never be mistaken for results.
set -uo pipefail

HARNESS="${1:-}"
TARGET="${2:-}"
MODE="${3:-}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

cd "$ROOT" || exit 1

if [ -z "$HARNESS" ] || [ -z "$TARGET" ]; then
  sed -n '2,14p' "$0"
  exit 2
fi

STAMP="$(date +%Y%m%d-%H%M%S)"
OUT="$ROOT/evals/results/$HARNESS-$TARGET-$STAMP"
mkdir -p "$OUT"

# The prompt is arbitrary text and may contain shell metacharacters, so it is
# written shell-quoted and sourced rather than interpolated into an eval.
python3 - "$ROOT/evals/targets.json" "$TARGET" "$OUT/target.env" <<'PY'
import json, shlex, sys
t = json.load(open(sys.argv[1]))[sys.argv[2]]
with open(sys.argv[3], "w") as fh:
    for key, value in (("TARGET_START", t["start"]), ("TARGET_PROMPT", t["prompt"])):
        fh.write(f"{key}={shlex.quote(value)}\n")
PY
. "$OUT/target.env"

# Scope file for the target, and a matching mock script.
python3 - "$ROOT/evals/targets.json" "$TARGET" "$OUT/scope.json" "$OUT/turns.json" <<'PY'
import json, sys
targets = json.load(open(sys.argv[1]))
t = targets[sys.argv[2]]
json.dump({"program": sys.argv[2], "hosts": t["hosts"], "ports": t["ports"]},
          open(sys.argv[3], "w"), indent=2)
# A scripted run that exercises the target's real endpoints, so the smoke test
# covers the transport rather than only the schema.
host, port = t["hosts"][0], t["ports"][0]
base = f"http://{host}:{port}"
expected = t["expected"][0]["endpoint"]
control = t["must_not_report"][0]["endpoint"]
findings = {"summary": "Smoke-test findings produced by the scripted upstream.",
            "findings": [{"title": "Order readable across accounts (smoke test)",
                          "asset": host, "class": "broken_access_control",
                          "confidence": "high",
                          "endpoint": expected,
                          "evidence": [f"GET {expected} with bob's token returned alice's order"],
                          "repro_steps": [f"curl -H 'Authorization: Bearer bob-token' {base}{expected}"],
                          "impact": "Reads another customer's order.",
                          "caveats": ["Produced by the scripted upstream, not by a model."]}]}
turns = [
    {"kind": "tool", "tool": "exec_command",
     "command": f"curl -s -H 'Authorization: Bearer bob-token' {base}/v2/me && curl -s -H 'Authorization: Bearer bob-token' {base}{expected}"},
    {"kind": "tool", "tool": "exec_command",
     "command": f"curl -s -o /dev/null -w 'control:%{{http_code}}\\n' -H 'Authorization: Bearer bob-token' {base}{control}"},
    {"kind": "text", "text": json.dumps(findings)},
]
json.dump(turns, open(sys.argv[4], "w"), indent=2)
PY

echo "target: $TARGET_START"
# shellcheck disable=SC2086
$TARGET_START >"$OUT/target.log" 2>&1 &
TARGET_PID=$!
sleep 1

cleanup() { kill $TARGET_PID 2>/dev/null; }
trap cleanup EXIT

case "$HARNESS" in
  bridge)
    export OCGO_API_KEY="${OCGO_API_KEY:-unset}"
    MOCK_PID=""
    # A scripted upstream reports fixed usage figures; recording them would put
    # imaginary dollars in the real spend ledger. A mock run gets its own.
    if [ "$MODE" = "--mock" ]; then
      export OCGO_USAGE_FILE="$OUT/usage.json"
    fi
    if [ "$MODE" = "--mock" ]; then
      "$ROOT/bin/mockglm" -addr 127.0.0.1:3457 -turns-file "$OUT/turns.json" \
        >"$OUT/mock.log" 2>&1 &
      MOCK_PID=$!
      sleep 1
      OCGO_API_KEY=dev "$ROOT/bin/ocgo" serve \
        --upstream http://127.0.0.1:3457/v1/chat/completions >"$OUT/serve.log" 2>&1 &
      SRV_PID=$!
      sleep 2
      UPSTREAM=mock
    else
      UPSTREAM="opencode-go"
    fi
    # Real spend and token counts come from the bridge's own ledger, which the
    # run's metadata otherwise has no way to see.
    LEDGER="${OCGO_USAGE_FILE:-$HOME/.config/ocgo/usage.json}"
    python3 - "$LEDGER" >"$OUT/ledger-before.json" <<'PY'
import json, os, sys
try:
    entries = json.load(open(sys.argv[1])).get("entries", [])
except Exception:
    entries = []
print(json.dumps({"count": len(entries),
                  "cost": sum(e.get("cost", 0) for e in entries),
                  "tokens": sum(e.get("input", 0) + e.get("output", 0) for e in entries)}))
PY
    bash "$ROOT/scripts/scoped-run.sh" "$OUT/scope.json" "$OUT" "$TARGET_PROMPT" \
      >"$OUT/scoped-run.log" 2>&1
    HARNESS_RC=$?
    python3 - "$LEDGER" >"$OUT/ledger-after.json" <<'PY'
import json, sys
try:
    entries = json.load(open(sys.argv[1])).get("entries", [])
except Exception:
    entries = []
print(json.dumps({"count": len(entries),
                  "cost": sum(e.get("cost", 0) for e in entries),
                  "tokens": sum(e.get("input", 0) + e.get("output", 0) for e in entries)}))
PY
    [ -n "$MOCK_PID" ] && kill $MOCK_PID 2>/dev/null
    [ -n "${SRV_PID:-}" ] && kill $SRV_PID 2>/dev/null
    ;;
  opencode|claude)
    UPSTREAM="opencode-go"
    HARNESS_RC=3
    echo "no working OpenCode Go credential is configured on this machine; see docs/eval-results.md" \
      >"$OUT/blocked.txt"
    ;;
  *)
    echo "unknown harness: $HARNESS"
    exit 2
    ;;
esac

python3 - "$ROOT/evals/targets.json" "$TARGET" "$OUT" "$HARNESS" "$UPSTREAM" "$HARNESS_RC" "$MODE" <<'PY'
import json, sys
path, harness, upstream, rc, mode = sys.argv[3], sys.argv[4], sys.argv[5], int(sys.argv[6]), sys.argv[7]
meta_path = f"{path}/run.json"
meta = {}
try:
    meta = json.load(open(meta_path))
except Exception:
    pass

try:
    before = json.load(open(f"{path}/ledger-before.json"))
    after = json.load(open(f"{path}/ledger-after.json"))
    if after["count"] >= before["count"]:
        meta["cost_usd"] = round(after["cost"] - before["cost"], 8)
        meta["tokens"] = after["tokens"] - before["tokens"]
except Exception:
    pass

meta["harness"] = harness
meta["upstream"] = upstream
meta["harness_exit"] = rc
codex_exit = meta.get("codex_exit")
# A run counts as a result when it actually reached the model and the agent
# finished. Schema conformance is reported separately: a correct answer in the
# wrong wrapper is a real outcome, not a missing one.
meta["counts_as_result"] = upstream != "mock" and codex_exit == 0
if upstream == "mock":
    meta["blocked_reason"] = "scripted upstream: exercises the harness, does not measure a model"
elif codex_exit is None:
    meta["blocked_reason"] = "the run did not complete"
json.dump(meta, open(meta_path, "w"), indent=2)
PY

python3 "$ROOT/evals/score-eval.py" "$ROOT/evals/targets.json" "$TARGET" "$OUT"
echo "run dir: $OUT"
