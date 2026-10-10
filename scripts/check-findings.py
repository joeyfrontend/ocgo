#!/usr/bin/env python3
"""Validate a headless run's findings and its egress log.

    python3 scripts/check-findings.py <scope.json> <outdir> <codex-exit>

Checks:
  * findings.json parses as the schema's shape — the whole file, one object
  * every finding carries the required fields and a valid confidence
  * no finding names a host outside the scope file
  * every egress decision in the guard log is consistent with the scope file

Writes <outdir>/run.json with the run's metadata and <outdir>/escalation.json
with the findings whose confidence meets the threshold.

A model that answers correctly but wraps the answer in prose or a markdown fence
fails the shape check. That is reported as a failure, because following
--output-schema is part of the contract — but the surrounding prose is still
parsed for scoring, so a correct answer is never scored as an empty one.
"""
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).parent.parent / "evals"))
import jsonout  # noqa: E402

REQUIRED = ("title", "asset", "class", "confidence", "evidence", "repro_steps")
CONFIDENCE = ("low", "medium", "high")
ESCALATE_AT = ("high",)


def in_scope(scope, host):
    def matches(entry, host):
        entry = entry.lower().strip()
        host = host.lower().strip().rstrip(".")
        if entry.startswith("*."):
            suffix = entry[1:]
            return host.endswith(suffix) and len(host) > len(suffix)
        return entry == host

    if any(matches(d, host) for d in scope.get("deny") or []):
        return False
    return any(matches(h, host) for h in scope.get("hosts") or [])


def main():
    scope_path, outdir, codex_exit = sys.argv[1], pathlib.Path(sys.argv[2]), int(sys.argv[3])
    scope = json.loads(pathlib.Path(scope_path).read_text())
    problems = []

    raw = ""
    findings_path = outdir / "findings.json"
    if findings_path.exists():
        raw = findings_path.read_text()
    else:
        problems.append("findings.json was not produced")

    doc, conformant = jsonout.extract(raw)
    if raw and not conformant:
        problems.append(
            "findings.json is not a single JSON object as --output-schema requires; "
            "the answer was recovered from surrounding text for scoring")

    items = []
    if doc is None:
        if raw:
            problems.append("no JSON value could be found in findings.json")
    else:
        recovered = jsonout.findings(doc)
        if isinstance(doc, dict) and isinstance(doc.get("findings"), list):
            recovered = [e for e in doc["findings"] if isinstance(e, dict)]
        if not recovered:
            problems.append("findings.json contains no findings")
        for i, f in enumerate(recovered):
            for key in REQUIRED:
                if key not in f:
                    problems.append(f"finding {i} is missing {key}")
            if f.get("confidence") not in CONFIDENCE:
                problems.append(f"finding {i} has confidence {f.get('confidence')!r}")
            for key in ("evidence", "repro_steps"):
                value = f.get(key)
                if not isinstance(value, list) or not value:
                    problems.append(f"finding {i} has an empty {key}")
            asset = f.get("asset")
            if isinstance(asset, str) and asset and not in_scope(scope, asset):
                problems.append(f"finding {i} names out-of-scope asset {asset!r}")
            items.append(f)

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
            problems.append(f"guard allowed out-of-scope host {d['host']}")

    # Scored findings come from whatever shape the model used, so a correct
    # answer inside a fence is still escalated rather than lost.
    scored = jsonout.findings(doc) or items
    escalation = [f for f in scored if f.get("confidence") in ESCALATE_AT]
    (outdir / "escalation.json").write_text(json.dumps({
        "threshold": list(ESCALATE_AT),
        "program": scope.get("program"),
        "findings": escalation,
    }, indent=2) + "\n")

    (outdir / "run.json").write_text(json.dumps({
        "program": scope.get("program"),
        "codex_exit": codex_exit,
        "schema_conformant": conformant,
        "scope_file": str(scope_path),
        "scope_hosts": scope.get("hosts"),
        "egress_allowed": len(allowed),
        "egress_refused": len(refused),
        "refused_hosts": sorted({d["host"] for d in refused}),
        "findings": len(items),
        "recovered_findings": len(scored),
        "escalations": len(escalation),
    }, indent=2) + "\n")

    print(f"schema conformant: {conformant}")
    print(f"findings:          {len(items)} ({len(scored)} recovered for scoring)")
    print(f"escalations:       {len(escalation)} (confidence in {list(ESCALATE_AT)})")
    print(f"egress allowed:    {len(allowed)}")
    print(f"egress refused:    {len(refused)} {sorted({d['host'] for d in refused})}")
    if codex_exit != 0:
        problems.append(f"codex exited {codex_exit}")

    if problems:
        print("\nFAIL:")
        for p in problems:
            print("  -", p)
        return 1
    print("\nPASS: findings valid, no out-of-scope asset named, egress consistent with scope")
    return 0


if __name__ == "__main__":
    sys.exit(main())
