# Evaluation results

Whether this bridge is worth using comes down to one number: cost per confirmed
finding, compared with running the same models without it.

Running `scripts/render-eval.py` regenerates this file from whatever is in
`evals/results/`. The table below is from a real run, kept here because the run
output itself was removed.

## The test target

`evals/idor-app` is a local order API built for this purpose. It has one
deliberate access control bug: `GET /v2/orders/{id}` returns any order to any
authenticated caller. Next to it sits a control, `GET /v2/orders/{id}/receipt`,
which does check who is asking. A run that reports the control as vulnerable is
producing a false positive, and the scorer counts it as one.

The agent is given one user's credentials and asked to establish whether it can
read another user's order.

## Result

| harness | upstream | answer shape | confirmed | false positives | tokens | cost | cost per finding |
|---|---|---|---|---|---|---|---|
| Codex CLI through this bridge | OpenCode Go | valid | 1 | 0 | 45,700 | $0.0060 | $0.0060 |

The run found the planted bug, correctly cleared the control, and returned an
answer that satisfied the required JSON shape.

## Answer shape is reported separately

The first real run produced the right answer wrapped in a markdown code fence,
with its own field names. That is a security success and a contract failure at
the same time, so the scorer reports both rather than picking one. Once the
prompt stated the required fields explicitly, the answer came back in shape.

## What is not measured

This is one harness on one small target. Two comparisons the plan called for are
still missing:

- Native OpenCode running the same models, for cost per confirmed finding.
- Claude Code running a different model, as a second reference point.

Both need those command line tools wired to the same subscription, which is not
done yet.

The other targets the plan mentions, a sanitised past finding and a local
Foundry fork, are not present and have not been invented here. Add them to
`evals/targets.json` when they are available.

## Reproducing

```bash
evals/run-eval.sh bridge idor-app
python3 scripts/render-eval.py
```

Add `--mock` to drive the run against the scripted upstream. That exercises the
harness end to end and measures nothing about a model, because the answers are
the ones the script asked for. Such runs are recorded with
`counts_as_result: false` so they cannot be mistaken for results.
