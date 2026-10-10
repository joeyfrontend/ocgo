"""Recover a findings list from a model's final message.

Models do not reliably return the shape `--output-schema` asks for. Measured
here: one run answered with a markdown-fenced top-level array, used `severity`
and `confidence` instead of the schema's fields, and wrote endpoints as
`GET /v2/orders/{id}` rather than a concrete URL.

Refusing to look inside the fence throws away a correct answer; ignoring the
divergence would hide a genuine failure to follow the contract. So both are
reported: the caller gets the recovered findings and a flag saying whether the
file was the shape that was asked for.
"""
import json
import re

FENCE = re.compile(r"```(?:json)?\s*(.*?)```", re.DOTALL)
METHOD = re.compile(r"^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+", re.IGNORECASE)
URLISH = re.compile(r"^[a-z]+://[^/]+", re.IGNORECASE)

# Words that mean "this is a defect" and words that mean "this is not".
DEFECT_WORDS = {"low", "medium", "high", "critical", "confirmed", "true", "yes"}
CLEAN_WORDS = {"none", "info", "informational", "false", "no", "safe", "not_vulnerable"}


def extract(text):
    """Return (document, conformant).

    conformant is True only when the whole file was one JSON value of the shape
    the schema describes, which is what --output-schema asks for.
    """
    if text is None:
        return None, False
    stripped = text.strip()
    if stripped:
        try:
            doc = json.loads(stripped)
            if isinstance(doc, dict) and "findings" in doc:
                return doc, True
        except ValueError:
            pass

    for candidate in [m.group(1) for m in FENCE.finditer(text)] + [text]:
        candidate = candidate.strip()
        if not candidate:
            continue
        try:
            doc = json.loads(candidate)
        except ValueError:
            continue
        if isinstance(doc, (dict, list)):
            return doc, False
    return None, False


def findings(doc):
    """Return the entries that assert a defect, whatever the document called them."""
    if isinstance(doc, list):
        return [e for e in doc if isinstance(e, dict) and claims_defect(e)]
    if not isinstance(doc, dict):
        return []
    for key in ("findings", "results", "issues", "vulnerabilities"):
        value = doc.get(key)
        if not isinstance(value, list):
            continue
        entries = [e for e in value if isinstance(e, dict)]
        if key == "findings":
            return entries
        # A probe list: keep the probes that assert a defect.
        return [e for e in entries if claims_defect(e)]
    return []


def claims_defect(entry):
    """Whether an entry says the thing it describes is vulnerable."""
    for key in ("severity", "vulnerable", "is_vulnerable", "exploitable", "confirmed"):
        value = entry.get(key)
        if isinstance(value, bool):
            return value
        if isinstance(value, str):
            word = value.strip().lower()
            if word in CLEAN_WORDS:
                return False
            if word in DEFECT_WORDS:
                return True
    confidence = entry.get("confidence")
    if isinstance(confidence, str):
        word = confidence.strip().lower()
        if word in CLEAN_WORDS:
            return False
        if word in DEFECT_WORDS:
            return True
    return False


def normalise_path(endpoint):
    """Reduce an endpoint to a comparable path, dropping method and host."""
    if not isinstance(endpoint, str):
        return ""
    value = endpoint.strip()
    value = METHOD.sub("", value)
    value = URLISH.sub("", value)
    value = value.split("?")[0].split("#")[0]
    if not value.startswith("/"):
        value = "/" + value.lstrip("/")
    return value.rstrip("/").lower()


def _segments(path):
    return [s for s in path.split("/") if s != ""]


def _is_placeholder(segment):
    return (segment.startswith("{") and segment.endswith("}")) or segment in ("*", ":id")


def _segment_matches(a, b):
    """A placeholder matches an identifier; a literal must match itself.

    Literal numbers are deliberately not interchangeable: `/v2/orders/1002` is
    a different object from `/v2/orders/1001`, and treating them as the same
    route would score a model that only tested its own order as correct.
    """
    if a == b:
        return True
    if _is_placeholder(a):
        return b.isdigit()
    if _is_placeholder(b):
        return a.isdigit()
    return False


def matches(endpoint, expected):
    """Whether an endpoint refers to the same route as the expected endpoint.

    Segment-wise, so `GET /v2/orders/{id}` matches `/v2/orders/1001`, and a
    longer path (`/v2/orders/1001/receipt`) does not match a shorter one.
    """
    got = _segments(normalise_path(endpoint))
    want = _segments(normalise_path(expected))
    if not got or not want or len(got) != len(want):
        return False
    return all(_segment_matches(w, g) for w, g in zip(want, got))


def endpoint_of(entry):
    """Best-effort endpoint for a finding or a probe entry."""
    for key in ("endpoint", "url", "path", "asset", "route"):
        value = entry.get(key)
        if isinstance(value, str) and value.strip():
            return value.strip()
    evidence = entry.get("evidence")
    candidates = evidence if isinstance(evidence, list) else [evidence]
    for item in candidates:
        if isinstance(item, dict):
            item = item.get("request") or item.get("url") or ""
        for token in str(item).split():
            if token.startswith("/") or ("://" in token):
                return token
    return ""
