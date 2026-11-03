#!/usr/bin/env python3
"""Score a needle3 .cact archive on the held-out ritual set, through the engine.

The .cact engine is the deployed runtime and the only one that is usable as a
benchmark: it keeps a KV cache and answers in ~0.4 s, where the pure-JAX decoder
in needle.model.run re-runs the whole prefix for every token (measured 16-26 s
per row on this 2-core sandbox). Both archives are built by
scripts/needle_finetune.sh from the same rung checkpoint, so the only difference
between them is the LoRA adapter.

    python3 scripts/needle_bench.py --eval scripts/needle-data/eval.jsonl \
        --weights scripts/needle-data/rituals-tuned.cact --label tuned

Two properties of the engine decide how this script measures.

The decoding is greedy and it is deterministic *within* a process: asking the
same 160 rows twice in one process gives 160/160 identical answers, and the C
API (``needle_complete``) takes no seed or temperature to change that.

Across processes it is not reproducible, and the cause is not sampling. The
constructor injects ``date: %Y-%m-%d %a %H:%M`` into the system prompt, so two
interpreter starts a minute apart send different prompts; on the same weights
that moved 26 of 160 rows. ``--system-date`` therefore pins the fact to a fixed
stamp (the prompt the engine sends is recorded in the report), and ``--repeats``
runs the whole bench in fresh child processes so any variation that survives the
pin shows up as a measured range rather than as a silent difference between two
runs a day apart.

The report separates what a single accuracy number hides: did it pick the right
tools, did it copy the arguments verbatim, and did it stay silent on requests
that no tool serves.
"""

import argparse
import json
import os
import statistics
import subprocess
import sys
import tempfile
import time

import needle

TOOLS_PER_ROW = "tools"

# Fixed stamp for the prompt's date fact; matches needle's own `date:` key so the
# engine keeps it instead of prefixing the live clock.
PINNED_DATE = "2026-01-01 Thu 00:00"

# Tools the corpus presents as ordered values rather than a keyed object; the
# order is the contract, mirrored from scripts/needle_rituals.py.
ORDERED = {"grep": ("pattern", "text")}


def bind(name, arguments):
    """Map an ordered call onto the tool's keyed contract.

    A positional wire format costs one adapter at the edge, and this is where the
    bench pays it, so an ordered archive is scored against the same gold rows as
    a keyed one. A keyed call for the same tool passes through untouched.
    """
    order = ORDERED.get(name)
    values = arguments.get("values") if order else None
    if isinstance(values, list) and len(values) == len(order) and set(arguments) == {"values"}:
        return dict(zip(order, (str(value) for value in values)))
    return arguments


def calls_of(response):
    """The tool calls in an engine envelope, or None when it reported an error."""
    if not isinstance(response, dict) or response.get("error"):
        return None
    calls = response.get("function_calls") or []
    out = []
    for call in calls:
        if not isinstance(call, dict):
            return None
        arguments = call.get("arguments")
        if isinstance(arguments, str):
            try:
                arguments = json.loads(arguments or "{}")
            except json.JSONDecodeError:
                return None
        if not isinstance(arguments, dict):
            return None
        name = str(call.get("name") or "")
        out.append({"name": name, "arguments": bind(name, arguments)})
    return out


def load_rows(path, limit=0):
    rows = []
    with open(path, encoding="utf-8") as handle:
        for line in handle:
            if line.strip():
                rows.append(json.loads(line))
    return rows[:limit] if limit else rows


def evaluate(rows, weights, system, max_new_tokens, progress=True):
    """One pass over the rows in this process."""
    tools_json = json.dumps(rows[0][TOOLS_PER_ROW], separators=(",", ":"))
    agent = needle.Needle(tools=tools_json, weights=weights, system=system, stateless=True)

    records = []
    for index, row in enumerate(rows, 1):
        started = time.time()
        try:
            response = agent.complete(row["query"], max_new_tokens)
        except RuntimeError as err:
            response = {"error": str(err)}
        elapsed_ms = round((time.time() - started) * 1000)
        predicted = calls_of(response)
        gold = [{"name": call["name"], "arguments": call["arguments"]} for call in row["answers"]]
        records.append({
            "query": row["query"],
            "gold": gold,
            "predicted": predicted,
            "reasoning": (response or {}).get("reasoning"),
            "error": (response or {}).get("error"),
            "latency_ms": elapsed_ms,
            "expected_calls": len(gold),
        })
        if progress and (index % 20 == 0 or index == len(rows)):
            print(f"  {index}/{len(rows)}  last {elapsed_ms} ms", flush=True)

    agent.close()
    return records


def score(records):
    for record in records:
        predicted = record["predicted"]
        gold = record["gold"]
        record["parse_ok"] = predicted is not None
        predicted = predicted or []
        record["name_ok"] = [c["name"] for c in predicted] == [c["name"] for c in gold]
        record["args_ok"] = [c["arguments"] for c in predicted] == [c["arguments"] for c in gold]
        record["exact"] = record["parse_ok"] and record["name_ok"] and record["args_ok"]
        record["abstain_ok"] = (record["parse_ok"] and not predicted) if not gold else None

    answering = [r for r in records if r["gold"]]
    abstaining = [r for r in records if not r["gold"]]

    def rate(items, key):
        return 0.0 if not items else sum(1 for r in items if r[key]) / len(items)

    latencies = sorted(r["latency_ms"] for r in records)
    return {
        "rows": len(records),
        "answering_rows": len(answering),
        "abstain_rows": len(abstaining),
        "parse_rate": rate(records, "parse_ok"),
        "exact_rate": rate(answering, "exact"),
        "tool_name_rate": rate(answering, "name_ok"),
        "argument_rate": rate(answering, "args_ok"),
        "abstain_rate": rate(abstaining, "abstain_ok"),
        "multi_call_exact": rate([r for r in answering if r["expected_calls"] > 1], "exact"),
        "latency_p50_ms": latencies[len(latencies) // 2] if latencies else 0,
        "latency_mean_ms": round(statistics.fmean(latencies)) if latencies else 0,
        "exact_counts": sum(1 for r in answering if r["exact"]),
    }


def band(values):
    """mean and the observed min..max, in the same unit as the input."""
    if not values:
        return 0.0, 0.0, 0.0
    return statistics.fmean(values), min(values), max(values)


def child_command(args, out_path):
    command = [sys.executable, os.path.abspath(__file__),
               "--eval", args.eval, "--weights", args.weights, "--label", args.label,
               "--max-new-tokens", str(args.max_new_tokens), "--repeats", "1",
               "--out", out_path]
    if args.limit:
        command += ["--limit", str(args.limit)]
    command += ["--live-date"] if args.live_date else ["--system-date", args.system_date]
    return command


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--eval", required=True)
    parser.add_argument("--weights", required=True, help="a .cact archive")
    parser.add_argument("--label", default="run")
    parser.add_argument("--limit", type=int, default=0)
    parser.add_argument("--max-new-tokens", type=int, default=192)
    parser.add_argument("--system-date", default=PINNED_DATE,
                        help="the date fact the prompt carries (default: pinned)")
    parser.add_argument("--live-date", action="store_true",
                        help="let the engine prefix the current clock instead")
    parser.add_argument("--repeats", type=int, default=3,
                        help="passes in fresh processes, so variation shows as a range")
    parser.add_argument("--out", default=None, help="per-example predictions")
    parser.add_argument("--report", default=None, help="summary json")
    args = parser.parse_args(argv)
    if args.repeats < 1:
        parser.error("--repeats must be at least 1")

    system = "" if args.live_date else f"date: {args.system_date}"
    rows = load_rows(args.eval, args.limit)
    print(f"{args.label}: {len(rows)} rows, {len(rows[0][TOOLS_PER_ROW]) if rows else 0} tools, "
          f"weights {args.weights}, prompt {'live clock' if args.live_date else system}")

    passes = []
    if args.repeats == 1:
        records = evaluate(rows, args.weights, system, args.max_new_tokens)
        passes.append(score(records))
        if args.out:
            with open(args.out, "w", encoding="utf-8") as handle:
                for record in records:
                    handle.write(json.dumps(record, ensure_ascii=False) + "\n")
            print(f"  predictions          {args.out}")
    else:
        with tempfile.TemporaryDirectory() as scratch:
            for index in range(args.repeats):
                out_path = os.path.join(scratch, f"round-{index}.jsonl")
                print(f"== round {index + 1}/{args.repeats} (fresh process)", flush=True)
                result = subprocess.run(child_command(args, out_path), check=False)
                if result.returncode != 0:
                    print(f"  round {index + 1} failed with {result.returncode}", flush=True)
                    continue
                records = []
                with open(out_path, encoding="utf-8") as handle:
                    for line in handle:
                        if line.strip():
                            records.append(json.loads(line))
                passes.append(score(records))
                if args.out:
                    target = f"{args.out}.r{index + 1}"
                    with open(target, "w", encoding="utf-8") as handle:
                        for record in records:
                            handle.write(json.dumps(record, ensure_ascii=False) + "\n")

    if not passes:
        print("no completed pass")
        return 1

    summary = {"label": args.label, "weights": args.weights, "repeats": len(passes),
               "system_prompt": "live clock" if args.live_date else system, "rounds": passes}
    print(f"\n=== {args.label}  ({len(passes)} pass{'es' if len(passes) > 1 else ''})")
    for key, printed, unit in (("exact_rate", "exact call", True),
                               ("tool_name_rate", "correct tool set", True),
                               ("argument_rate", "exact arguments", True),
                               ("abstain_rate", "refused correctly", True),
                               ("parse_rate", "parsed", True)):
        mean, low, high = band([p[key] for p in passes])
        spread = "" if high == low else f"  [{low:.1%}..{high:.1%}]"
        extra = ""
        if key == "exact_rate":
            counts = [p["exact_counts"] for p in passes]
            extra = f"  ({min(counts)}-{max(counts)} of {passes[0]['answering_rows']})"
        print(f"  {printed:<20} {mean:.1%}{spread}{extra}")
    summary["exact_rate_mean"] = band([p["exact_rate"] for p in passes])[0]
    summary["exact_rate_min"] = band([p["exact_rate"] for p in passes])[1]
    summary["exact_rate_max"] = band([p["exact_rate"] for p in passes])[2]
    summary["exact_counts"] = [p["exact_counts"] for p in passes]
    if len(passes) > 1:
        identical = len({json.dumps(p["exact_counts"]) for p in passes}) == 1 and \
            len({p["exact_rate"] for p in passes}) == 1
        print(f"  reproducible         {'yes - every pass identical' if identical else 'no - see the range'}")
        summary["reproducible"] = identical
    p50 = round(statistics.fmean(p["latency_p50_ms"] for p in passes))
    mean_ms = round(statistics.fmean(p["latency_mean_ms"] for p in passes))
    print(f"  latency              p50 {p50} ms, mean {mean_ms} ms")
    summary["latency_p50_ms"] = p50
    summary["latency_mean_ms"] = mean_ms

    if args.report:
        with open(args.report, "w", encoding="utf-8") as handle:
            json.dump(summary, handle, indent=2)
            handle.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
