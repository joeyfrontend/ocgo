#!/usr/bin/env python3
"""Score one evaluation run.

    python3 evals/score-eval.py <targets.json> <target> <run-dir>

A finding counts as confirmed when its endpoint matches an entry in the
target's `expected` list. It counts as a false positive when it matches
`must_not_report`, or when it matches nothing at all. Cost comes from the run's
own metadata, so a run that could not report usage is scored as unknown rather
than as free.
"""
import json
import pathlib
import sys


def endpoint_of(finding):
    endpoint = finding.get("endpoint") or ""
    if endpoint:
        return endpoint.strip()
    # Fall back to the asset and any URL-looking evidence.
    for item in finding.get("evidence") or []:
        for token in str(item).split():
            if "/v" in token and "http" in token:
                return token
    return finding.get("asset", "")


def matches(endpoint, expected_endpoint):
    if not endpoint or not expected_endpoint:
        return False
    return expected_endpoint in endpoint


def main():
    targets_path, target_name, run_dir = sys.argv[1], sys.argv[2], pathlib.Path(sys.argv[3])
    targets = json.loads(pathlib.Path(targets_path).read_text())
    target = targets[target_name]

    findings = []
    findings_path = run_dir / "findings.json"
    if findings_path.exists():
        try:
            findings = json.loads(findings_path.read_text()).get("findings") or []
        except ValueError:
            findings = []

    confirmed, false_positives, unclassified = [], [], []
    for f in findings:
        endpoint = endpoint_of(f)
        if any(matches(endpoint, e["endpoint"]) for e in target.get("expected") or []):
            confirmed.append(f)
            continue
        if any(matches(endpoint, e["endpoint"]) for e in target.get("must_not_report") or []):
            false_positives.append(f)
            continue
        unclassified.append(f)

    run_meta = {}
    meta_path = run_dir / "run.json"
    if meta_path.exists():
        run_meta = json.loads(meta_path.read_text())

    cost = run_meta.get("cost_usd")
    cost_per = None
    if isinstance(cost, (int, float)) and confirmed:
        cost_per = cost / len(confirmed)

    score = {
        "harness": run_meta.get("harness", "unknown"),
        "target": target_name,
        "upstream": run_meta.get("upstream", "unknown"),
        "counts_as_result": bool(run_meta.get("counts_as_result", False)),
        "confirmed": len(confirmed),
        "false_positives": len(false_positives),
        "unclassified": len(unclassified),
        "tokens": run_meta.get("tokens"),
        "cost_usd": cost,
        "cost_per_confirmed": cost_per,
        "blocked_reason": run_meta.get("blocked_reason"),
        "run_dir": str(run_dir),
    }
    (run_dir / "score.json").write_text(json.dumps(score, indent=2) + "\n")

    print(f"harness:          {score['harness']} ({score['upstream']} upstream)")
    print(f"confirmed:        {score['confirmed']}")
    print(f"false positives:  {score['false_positives']}")
    print(f"unclassified:     {score['unclassified']}")
    print(f"tokens:           {score['tokens']}")
    print(f"cost:             {score['cost_usd']}")
    print(f"cost per finding: {score['cost_per_confirmed']}")
    if score["blocked_reason"]:
        print(f"BLOCKED:          {score['blocked_reason']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
