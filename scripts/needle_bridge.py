#!/usr/bin/env python3
"""Needle 3 bridge: the local fast adapter behind internal/preprocess.

One JSON object per line in, one JSON object per line out, in request order.
The Go side (internal/preprocess/needle.go) owns the process and the timeout;
this file owns the two local models:

  extraction  needle3 (Cactus Compute, 8-29 MB, 2-bit) via cactus-needle
              -> the golden Extraction schema from internal/preprocess/types.go
  gate        laya (System One decision engine, scripts/vendor/systemone_engine.py)
              -> act or escalate, mapped onto the schema's escape-hatch flags

No API key, no network at inference time: the weights are a .cact file that the
needle python package maps in place.

Two measured properties of needle3 shape this file and are worth keeping in
mind before "improving" it:

  * It is a tool caller. It emits no call when the message is not a request for
    an action, so an ingress turn routinely comes back empty; the empty case is
    recorded (uncertain flag + a deterministic summary) instead of being retried.
  * Its constrained decoder guarantees the *shape* of a record, not the values.
    Under the enum-typed schema below every field parses, but a free numeric
    field comes back on the model's own scale (95.0 for "very sure"), so
    confidences are normalized here rather than trusted.

Usage:
    NEEDLE_BRIDGE=scripts/needle_bridge.py python3 ...
    echo '{"id":"1","text":"run the tests"}' | python3 scripts/needle_bridge.py
"""

from __future__ import annotations

import json
import os
import sys
import time
from dataclasses import dataclass
from typing import Any, Dict, List, Literal

INTENTS = ("question", "command", "status_update", "artifact_share",
           "conversation", "correction", "unknown")
TOPICS = ("code", "debugging", "planning", "infra", "data", "review", "meta",
          "general")
SENTIMENTS = ("neutral", "positive", "frustrated", "urgent")
FLAGS = ("uncertain", "multi_intent", "ambiguous_entity", "low_confidence",
         "needs_review")

SUMMARY_MAX = 200
GATE_THRESHOLD = 0.75  # laya's own min_confidence_threshold
GATE_OPTIONS = ["act", "escalate"]


def _log(**fields: Any) -> None:
    """Diagnostics go to stderr, one JSON line, so stdout stays the protocol."""
    fields["ts"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    print(json.dumps(fields, default=str), file=sys.stderr, flush=True)


@dataclass
class Extraction:
    """One ingress record for a coding harness: intent, topics, tone, summary."""

    intent: Literal["question", "command", "status_update", "artifact_share",
                    "conversation", "correction", "unknown"]
    intent_confidence: float
    topics: List[Literal["code", "debugging", "planning", "infra", "data",
                         "review", "meta", "general"]]
    sentiment: Literal["neutral", "positive", "frustrated", "urgent"]
    sentiment_confidence: float
    summary: str


def normalize_confidence(value: Any, default: float = 0.0) -> float:
    """Map a model-reported score onto 0.0-1.0.

    Measured: needle3 answers 95.0 where the schema means 0.95 and 6.5 where it
    means "barely". Anything above 1 is read as a percentage.
    """
    try:
        number = float(value)
    except (TypeError, ValueError):
        return default
    if number > 1.0:
        number = number / 100.0
    return max(0.0, min(1.0, number))


def pick(value: Any, allowed: tuple, default: str) -> str:
    if isinstance(value, str) and value.strip().lower() in allowed:
        return value.strip().lower()
    return default


def pick_topics(value: Any, limit: int = 3) -> List[str]:
    """Keep the first few distinct topics: the model otherwise lists all eight."""
    seen: List[str] = []
    if isinstance(value, str):
        value = [value]
    if isinstance(value, list):
        for item in value:
            topic = pick(item, TOPICS, "")
            if topic and topic not in seen:
                seen.append(topic)
            if len(seen) >= limit:
                break
    return seen or ["general"]


def clip(text: str) -> str:
    text = " ".join(str(text).split())
    return text[:SUMMARY_MAX]


class Bridge:
    """Lazily loads needle3 and laya; either can fail independently."""

    def __init__(self) -> None:
        self._needle_module = None
        self._needle_error = None
        self._engine = None
        self._engine_error = None

    # -- needle3 ---------------------------------------------------------

    def needle(self):
        """Import the package once; each call builds its own stateless engine.

        Measured: needle.extract is the better extraction path than run() with
        the record exposed as a tool. run() treats the message as a *request for
        an action* and refuses anything that is not one (0 of 1 command messages
        produced a record), while extract() records the message itself (3 of 3).
        """
        if self._needle_module is not None or self._needle_error is not None:
            return self._needle_module
        try:
            import needle  # noqa: PLC0415 - optional at runtime, imported once

            self._needle_module = needle
        except Exception as exc:  # noqa: BLE001 - reported to the caller
            self._needle_error = f"{type(exc).__name__}: {exc}"
            _log(event="needle_load_failed", error=self._needle_error)
        return self._needle_module

    def extract(self, text: str) -> Dict[str, Any]:
        needle = self.needle()
        if needle is None:
            raise RuntimeError(self._needle_error or "needle unavailable")

        weights = os.environ.get("NEEDLE_WEIGHTS") or None
        started = time.time()
        # strict=False because the grounding validator rejects every derived
        # field (a confidence is not a substring of the message). The enum-typed
        # schema still constrains the shape, so the record parses.
        raw = needle.extract(text, Extraction, strict=False, weights=weights)
        arguments = raw if isinstance(raw, dict) else {}

        record = {
            "intent": pick(arguments.get("intent"), INTENTS, "conversation"),
            "intent_confidence": normalize_confidence(arguments.get("intent_confidence")),
            "topics": pick_topics(arguments.get("topics")),
            "sentiment": pick(arguments.get("sentiment"), SENTIMENTS, "neutral"),
            "sentiment_confidence": normalize_confidence(arguments.get("sentiment_confidence")),
            "summary": clip(arguments.get("summary") or text),
        }
        return {
            "record": record,
            "emitted_call": bool(arguments),
            "confidence": record["intent_confidence"],
            "latency_ms": int((time.time() - started) * 1000),
        }

    # -- laya ------------------------------------------------------------

    def engine(self):
        if self._engine is not None or self._engine_error is not None:
            return self._engine
        try:
            sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "vendor"))
            from systemone_engine import DecisionEngine  # noqa: PLC0415

            self._engine = DecisionEngine("laya")
        except Exception as exc:  # noqa: BLE001
            self._engine_error = f"{type(exc).__name__}: {exc}"
            _log(event="laya_load_failed", error=self._engine_error)
        return self._engine

    def gate(self, text: str, record: Dict[str, Any]) -> Dict[str, Any]:
        """Second opinion from laya: act on this extraction, or escalate."""
        engine = self.engine()
        if engine is None:
            return {"decision": "act", "confidence": 0.0,
                    "error": self._engine_error}
        state = (
            f"intent: {record['intent']}\n"
            f"topics: {', '.join(record['topics'])}\n"
            f"confidence: {record['intent_confidence']}\n"
            f"message: {text}"
        )
        decision = engine.choice(
            state,
            "Act on this extraction locally, or escalate to the strong model?",
            GATE_OPTIONS,
        )
        return {
            "decision": decision["decision"],
            "confidence": decision["confidence"],
            "entropy": decision.get("entropy"),
            "model": decision.get("model"),
        }


def flags_for(record: Dict[str, Any], emitted: bool, gate: Dict[str, Any]) -> List[str]:
    flags: List[str] = []
    if not emitted:
        flags.append("uncertain")
    if record["intent_confidence"] < 0.5:
        flags.append("low_confidence")
    if gate.get("decision") == "escalate" and gate.get("confidence", 0) >= GATE_THRESHOLD:
        flags.append("needs_review")
    return [flag for flag in flags if flag in FLAGS]


def handle(bridge: Bridge, request: Dict[str, Any]) -> Dict[str, Any]:
    text = str(request.get("text") or "").strip()
    if not text:
        return {"ok": False, "error": "empty text"}

    extracted = bridge.extract(text)
    record = extracted["record"]
    gate = bridge.gate(text, record)
    record["flags"] = flags_for(record, extracted["emitted_call"], gate)

    _log(event="preprocess", id=request.get("id"),
         needle_ms=extracted["latency_ms"], needle_confidence=extracted["confidence"],
         emitted_call=extracted["emitted_call"], gate=gate, flags=record["flags"])
    return {"ok": True, "extraction": record, "gate": gate}


def main() -> int:
    bridge = Bridge()
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        request_id = None
        try:
            request = json.loads(line)
            request_id = request.get("id")
            result = handle(bridge, request)
        except Exception as exc:  # noqa: BLE001 - one bad request must not kill the worker
            result = {"ok": False, "error": f"{type(exc).__name__}: {exc}"}
        if request_id is not None:
            result["id"] = request_id
        print(json.dumps(result, default=str), flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
