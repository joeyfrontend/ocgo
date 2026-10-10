#!/usr/bin/env python3
"""Validate a headless run's findings and its egress log.

    python3 scripts/check-findings.py <scope.json> <outdir> <codex-exit>

Checks:
  * findings.json parses and every finding satisfies the schema's contract
  * no finding names a host outside the scope file
  * every egress decision in the guard log is consistent with the scope file
  * an out-of-scope connection attempt was refused, when one was made

Writes <outdir>/run.json with the run's metadata, and <outdir>/escalation.json
with the findings whose confidence meets the threshold.
"""
import json
import pathlib
import sys

REQUIRED = ("title", "asset", "class", "confidence", "evidence", "repro_steps")
CONFIDENCE = ("low", "medium", "high")
ESCALATE_AT = ("high",)


def fail(problems, message):
    problems.append(message)


def matches(entry, host):
    entry = entry.lower().strip()
    host = host.lower().strip().rstrip(".")
    if entry.startswith("*."):
        suffix = entry[1:]
        return host.endswith(suffix) and len(host) > len(suffix)
    return entry == host


def in_scope(scope, host):
    if any(matches(d, host) for d in scope.get("deny") or []):
        return False
    return any(matches(h, host) for h in scope.get("hosts") or [])


def main():
    scope_path, outdir, codex_exit = sys.argv[1], pathlib.Path(sys.argv[2]), int(sys.argv[3])
    scope = json.loads(pathlib.Path(scope_path).read_text())
    problems = []

    findings_path = outdir / "findings.json"
    findings = []
    if not findings_path.exists():
        fail(problems, "findings.json was not produced")
    else:
        try:
            doc = json.loads(findings_path.read_text())
        except ValueError as exc:
            fail(problems, f"findings.json is not valid JSON: {exc}")
            doc = {}
        items = doc.get("findings")
        if not isinstance(items, list):
            fail(problems, "findings.json has no findings array")
        else:
            for i, f in enumerate(items):
                if not isinstance(f, dict):
                    fail(problems, f"finding {i} is not an object")
                    continue
                for key in REQUIRED:
                    if key not in f:
                        fail(problems, f"finding {i} is missing {key}")
                if f.get("confidence") not in CONFIDENCE:
                    fail(problems, f"finding {i} has confidence {f.get('confidence')!r}")
                for key in ("evidence", "repro_steps"):
                    value = f.get(key)
                    if not isinstance(value, list) or not value:
                        fail(problems, f"finding {i} has an empty {key}")
                asset = f.get("asset")
                if isinstance(asset, str) and asset and not in_scope(scope, asset):
                    fail(problems, f"finding {i} names out-of-scope asset {asset!r}")
                findings.append(f)

    decisions = []
    log_path = outdir / "egress.jsonl"
    if log_path.exists():
        for line in log_path.read_text().splitlines():
            if line.strip():
                decisions.append(json.loads(line))
    refused = [d for d in decisions if not d.get("allowed")]
    allowed = [d for d in decisions if d.get("allowed")]
    for d in allowed:
        if not in_scope(scope, d["host"]):
            fail(problems, f"guard allowed out-of-scope host {d['host']}")

    escalation = [f for f in findings if f.get("confidence") in ESCALATE_AT]
    (outdir / "escalation.json").write_text(json.dumps({
        "threshold": list(ESCALATE_AT),
        "program": scope.get("program"),
        "findings": escalation,
    }, indent=2) + "\n")

    (outdir / "run.json").write_text(json.dumps({
        "program": scope.get("program"),
        "codex_exit": codex_exit,
        "scope_file": str(scope_path),
        "scope_hosts": scope.get("hosts"),
        "egress_allowed": len(allowed),
        "egress_refused": len(refused),
        "refused_hosts": sorted({d["host"] for d in refused}),
        "findings": len(findings),
        "escalations": len(escalation),
    }, indent=2) + "\n")

    print(f"findings:        {len(findings)}")
    print(f"escalations:     {len(escalation)} (confidence in {list(ESCALATE_AT)})")
    print(f"egress allowed:  {len(allowed)}")
    print(f"egress refused:  {len(refused)} {sorted({d['host'] for d in refused})}")
    if codex_exit != 0:
        fail(problems, f"codex exited {codex_exit}")

    if problems:
        print("\nFAIL:")
        for p in problems:
            print("  -", p)
        return 1
    print("\nPASS: findings valid, no out-of-scope asset named, egress consistent with scope")
    return 0


if __name__ == "__main__":
    sys.exit(main())
