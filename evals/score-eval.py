#!/usr/bin/env python3
"""Score one evaluation run.

    python3 evals/score-eval.py <targets.json> <target> <run-dir>

A finding counts as confirmed when its endpoint matches an entry in the
target's `expected` list. It counts as a false positive when it matches
`must_not_report`, or when it matches nothing at all.

Two things are reported separately and must not be conflated:

  * how much of the target the run got right (confirmed, false positives)
  * whether the answer was shaped the way `--output-schema` asked

A run that found the defect inside a markdown fence is a scoring success and a
schema failure, and saying so is more useful than collapsing the two.
"""
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).parent))
import jsonout  # noqa: E402


def main():
    targets_path, target_name, run_dir = sys.argv[1], sys.argv[2], pathlib.Path(sys.argv[3])
    targets = json.loads(pathlib.Path(targets_path).read_text())
    target = targets[target_name]

    raw = ""
    findings_path = run_dir / "findings.json"
    if findings_path.exists():
        raw = findings_path.read_text()

    doc, conformant = jsonout.extract(raw)
    items = jsonout.findings(doc)

    confirmed, false_positives, unclassified = [], [], []
    for f in items:
        endpoint = jsonout.endpoint_of(f)
        if any(jsonout.matches(endpoint, e["endpoint"]) for e in target.get("expected") or []):
            confirmed.append(f)
        elif any(jsonout.matches(endpoint, e["endpoint"]) for e in target.get("must_not_report") or []):
            false_positives.append(f)
        else:
            unclassified.append(f)

    run_meta = {}
    meta_path = run_dir / "run.json"
    if meta_path.exists():
        run_meta = json.loads(meta_path.read_text())

    cost = run_meta.get("cost_usd")
    cost_per = cost / len(confirmed) if isinstance(cost, (int, float)) and confirmed else None

    score = {
        "harness": run_meta.get("harness", "unknown"),
        "target": target_name,
        "upstream": run_meta.get("upstream", "unknown"),
        "counts_as_result": bool(run_meta.get("counts_as_result", False)),
        "schema_conformant": conformant,
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

    print(f"harness:           {score['harness']} ({score['upstream']} upstream)")
    print(f"schema conformant: {conformant}" + ("" if conformant else "  (recovered from surrounding text)"))
    print(f"confirmed:         {score['confirmed']}")
    print(f"false positives:   {score['false_positives']}")
    print(f"unclassified:      {score['unclassified']}")
    print(f"tokens:            {score['tokens']}")
    print(f"cost:              {score['cost_usd']}")
    print(f"cost per finding:  {score['cost_per_confirmed']}")
    if score["blocked_reason"]:
        print(f"BLOCKED:           {score['blocked_reason']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
