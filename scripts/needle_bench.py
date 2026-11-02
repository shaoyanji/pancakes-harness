#!/usr/bin/env python3
"""Score a needle3 .cact archive on the held-out ritual set, through the engine.

The .cact engine is the deployed runtime and the only one that is usable as a
benchmark: it keeps a KV cache and answers in ~0.5 s, where the pure-JAX decoder
in needle.model.run re-runs the whole prefix for every token (measured 16-26 s
per row on this 2-core sandbox). Both archives are built by
scripts/needle_finetune.sh from the same rung checkpoint, so the only difference
between them is the LoRA adapter.

    python3 scripts/needle_bench.py --eval scripts/needle-data/eval.jsonl \
        --weights scripts/needle-data/rituals-base.cact --label base
    python3 scripts/needle_bench.py --eval scripts/needle-data/eval.jsonl \
        --weights scripts/needle-data/rituals-tuned.cact --label tuned

The report separates what a single accuracy number hides: did it pick the right
tools, did it copy the arguments verbatim, and did it stay silent on requests
that no tool serves.
"""

import argparse
import json
import os
import statistics
import sys
import time

import needle

TOOLS_PER_ROW = "tools"


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
        out.append({"name": str(call.get("name") or ""), "arguments": arguments})
    return out


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--eval", required=True)
    parser.add_argument("--weights", required=True, help="a .cact archive")
    parser.add_argument("--label", default="run")
    parser.add_argument("--limit", type=int, default=0)
    parser.add_argument("--max-new-tokens", type=int, default=192)
    parser.add_argument("--out", default=None, help="per-example predictions")
    parser.add_argument("--report", default=None, help="summary json")
    args = parser.parse_args(argv)

    rows = []
    with open(args.eval, encoding="utf-8") as handle:
        for line in handle:
            if line.strip():
                rows.append(json.loads(line))
    if args.limit:
        rows = rows[: args.limit]

    # The archive carries its own tool list on every call, and needle takes it
    # pre-serialized so the bytes are the same ones the training rows carried.
    tools = rows[0][TOOLS_PER_ROW] if rows else []
    tools_json = json.dumps(tools, separators=(",", ":"))
    agent = needle.Needle(tools=tools_json, weights=args.weights, stateless=True)
    print(f"{args.label}: {len(rows)} rows, {len(tools)} tools, weights {args.weights}")

    records = []
    for index, row in enumerate(rows, 1):
        started = time.time()
        try:
            response = agent.complete(row["query"], args.max_new_tokens)
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
        if index % 20 == 0 or index == len(rows):
            print(f"  {index}/{len(rows)}  last {elapsed_ms} ms", flush=True)

    agent.close()

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
    summary = {
        "label": args.label,
        "weights": args.weights,
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
    }

    print(f"\n=== {args.label}")
    print(f"  rows                 {summary['rows']} ({summary['answering_rows']} callable, "
          f"{summary['abstain_rows']} refusal)")
    print(f"  parsed               {summary['parse_rate']:.1%}")
    print(f"  correct tool set     {summary['tool_name_rate']:.1%}")
    print(f"  exact arguments      {summary['argument_rate']:.1%}")
    print(f"  exact call           {summary['exact_rate']:.1%}  (multi-call {summary['multi_call_exact']:.1%})")
    print(f"  refused correctly    {summary['abstain_rate']:.1%}")
    print(f"  latency              p50 {summary['latency_p50_ms']} ms, mean {summary['latency_mean_ms']} ms")

    if args.out:
        with open(args.out, "w", encoding="utf-8") as handle:
            for record in records:
                handle.write(json.dumps(record, ensure_ascii=False) + "\n")
        print(f"  predictions          {args.out}")
    if args.report:
        with open(args.report, "w", encoding="utf-8") as handle:
            json.dump(summary, handle, indent=2)
            handle.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
