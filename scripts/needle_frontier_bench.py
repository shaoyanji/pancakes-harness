#!/usr/bin/env python3
"""Score a frontier model on the same held-out ritual rows as the local adapter.

The point of the sidecar is intelligence per byte, so the accuracy table alone says
nothing: what matters is what one *successful* call costs in envelope bytes, prompt
tokens and wall-clock latency. This runs the same 160 queries through a hosted model
with native function calling and prints those numbers next to the adapter's, which
it reads from a needle predictions file.

    python3 scripts/needle_frontier_bench.py \
        --eval scripts/needle-data/eval.pinned.jsonl \
        --model gemini-3-flash-preview \
        --adapter-preds scripts/needle-data/pred-pinned-ep3.jsonl \
        --out scripts/needle-data/pred-frontier.jsonl

GEMINI_API_KEY must be set. Nothing is retried past three attempts, and a row that
fails is recorded rather than dropped, so the denominator stays honest.
"""

import argparse
import json
import os
import statistics
import sys
import time
import urllib.error
import urllib.request

ENDPOINT = "https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent?key={key}"
GROQ_ENDPOINT = "https://api.groq.com/openai/v1/chat/completions"


def openai_tools(tools):
    """The same four tools in the OpenAI function-calling shape Groq accepts."""
    return [{"type": "function", "function": {
        "name": tool["name"],
        "parameters": {
            "type": "object",
            "properties": {name: {"type": "string"}
                           for name in tool.get("parameters", {}).get("properties", {})},
        },
    }} for tool in tools]


def openai_calls(message):
    calls = []
    for call in message.get("tool_calls") or []:
        function = call.get("function") or {}
        arguments = function.get("arguments")
        if isinstance(arguments, str):
            try:
                arguments = json.loads(arguments or "{}")
            except json.JSONDecodeError:
                arguments = {}
        calls.append({"name": function.get("name", ""), "arguments": arguments or {}})
    return calls


def gemini_tools(tools):
    """The same four tools, in the shape Gemini's function-calling API wants."""
    declarations = []
    for tool in tools:
        properties = tool.get("parameters", {}).get("properties", {})
        declarations.append({
            "name": tool["name"],
            "parameters": {
                "type": "OBJECT",
                "properties": {name: {"type": "STRING"} for name in properties},
            },
        })
    return [{"functionDeclarations": declarations}]


def payload_bytes(tools, query):
    """The bytes that describe a request, counted the same way for both runtimes."""
    return len(json.dumps(tools, separators=(",", ":"))) + len(query)


def calls_of(candidate):
    calls = []
    for part in candidate.get("content", {}).get("parts", []):
        call = part.get("functionCall")
        if call:
            calls.append({"name": call.get("name", ""),
                          "arguments": call.get("args", {}) or {}})
    return calls


def post(url, key, body, attempts=3):
    data = json.dumps(body).encode("utf-8")
    headers = {"Content-Type": "application/json", "Authorization": f"Bearer {key}",
               "User-Agent": "needle-frontier-bench/1.0"}
    delay = 2.0
    for attempt in range(1, attempts + 1):
        request = urllib.request.Request(url, data=data, headers=headers)
        try:
            with urllib.request.urlopen(request, timeout=90) as response:
                return json.loads(response.read().decode("utf-8"))
        except urllib.error.HTTPError as err:
            detail = err.read().decode("utf-8", "replace")[:300]
            if err.code not in (429, 500, 503) or attempt == attempts:
                raise RuntimeError(f"HTTP {err.code}: {detail}") from err
            time.sleep(delay)
            delay *= 2
        except urllib.error.URLError as err:
            if attempt == attempts:
                raise RuntimeError(f"network: {err}") from err
            time.sleep(delay)
            delay *= 2
    raise RuntimeError("unreachable")


def score(records):
    answering = [r for r in records if r["gold"]]
    refusals = [r for r in records if not r["gold"]]
    exact = sum(1 for r in answering if r["exact"])
    abstained = sum(1 for r in refusals if not r["predicted"])
    return {
        "rows": len(records),
        "answering_rows": len(answering),
        "refusal_rows": len(refusals),
        "exact_rate": exact / len(answering) if answering else 0.0,
        "tool_name_rate": (sum(1 for r in answering if r["name_ok"]) / len(answering)
                           if answering else 0.0),
        "abstain_rate": abstained / len(refusals) if refusals else 0.0,
        "exact": exact,
    }


def summarize(records, label, adapter=False):
    stats = score(records)
    latencies = sorted(r["latency_ms"] for r in records)
    prompt_tokens = sum(r.get("prompt_tokens") or 0 for r in records)
    output_tokens = sum(r.get("output_tokens") or 0 for r in records)
    payload = sum(r["payload_bytes"] for r in records)
    exact = stats["exact"] or 1
    return {
        "label": label,
        "rows": stats["rows"],
        "answering_rows": stats["answering_rows"],
        "exact": stats["exact"],
        "exact_rate": stats["exact_rate"],
        "tool_name_rate": stats["tool_name_rate"],
        "abstain_rate": stats["abstain_rate"],
        "bytes_per_row": round(payload / stats["rows"]),
        "bytes_per_exact": round(payload / exact),
        "prompt_tokens_per_row": (round(prompt_tokens / stats["rows"])
                                  if prompt_tokens else None),
        "tokens_per_exact": round((prompt_tokens + output_tokens) / exact)
        if prompt_tokens else None,
        "output_tokens_per_row": (round(output_tokens / stats["rows"])
                                  if output_tokens else None),
        "latency_p50_ms": latencies[len(latencies) // 2] if latencies else 0,
        "ms_per_exact": round(sum(latencies) / exact),
        "adapter": adapter,
    }


def adapter_records(path, eval_rows):
    """Read a needle predictions file into the same record shape.

    The predictions carry no byte count: needle builds its prompt internally, so the
    payload it is charged for is the same tools-plus-query the eval row describes.
    """
    with open(path, encoding="utf-8") as handle:
        rows = [json.loads(line) for line in handle if line.strip()]
    if len(rows) != len(eval_rows):
        raise SystemExit(f"{path}: {len(rows)} predictions for {len(eval_rows)} eval rows")
    records = []
    for row, eval_row in zip(rows, eval_rows):
        predicted = row["predicted"]
        gold = row["gold"]
        records.append({
            "query": row["query"],
            "gold": gold,
            "predicted": predicted,
            "latency_ms": row.get("latency_ms") or 0,
            "payload_bytes": payload_bytes(eval_row["tools"], eval_row["query"]),
            "prompt_tokens": None,
            "output_tokens": None,
            "exact": bool(row.get("exact")),
            "name_ok": [c["name"] for c in (predicted or [])] == [c["name"] for c in gold],
        })
    return records


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--eval", required=True)
    parser.add_argument("--provider", choices=("gemini", "groq"), default="gemini")
    parser.add_argument("--model", default=os.environ.get("GEMINI_MODEL", "gemini-3-flash-preview"))
    parser.add_argument("--limit", type=int, default=0)
    parser.add_argument("--pace", type=float, default=0.0,
                        help="seconds to wait between requests, for a free-tier quota")
    parser.add_argument("--out", default=None, help="per-row predictions")
    parser.add_argument("--report", default=None, help="summary json")
    parser.add_argument("--adapter-preds", default=None,
                        help="needle predictions to compare against (needs payload_bytes)")
    parser.add_argument("--adapter-label", default="needle")
    args = parser.parse_args(argv)

    key = os.environ.get("GROQ_API_KEY" if args.provider == "groq" else "GEMINI_API_KEY")
    if not key:
        raise SystemExit(f"{args.provider.upper()}_API_KEY is not set")

    with open(args.eval, encoding="utf-8") as handle:
        rows = [json.loads(line) for line in handle if line.strip()]
    if args.limit:
        rows = rows[: args.limit]

    records = []
    for index, row in enumerate(rows, 1):
        tools = row["tools"]
        if args.provider == "gemini":
            url = ENDPOINT.format(model=args.model, key=key)
            body = {
                "contents": [{"role": "user", "parts": [{"text": row["query"]}]}],
                "tools": gemini_tools(tools),
                "toolConfig": {"functionCallingConfig": {"mode": "AUTO"}},
                "generationConfig": {"temperature": 0},
            }
        else:
            url = GROQ_ENDPOINT
            body = {
                "model": args.model,
                "messages": [{"role": "user", "content": row["query"]}],
                "tools": openai_tools(tools),
                "tool_choice": "auto",
                "temperature": 0,
                "max_completion_tokens": 256,
            }
        if args.pace and index > 1:
            time.sleep(args.pace)
        started = time.time()
        prompt_tokens, output_tokens, total_tokens = None, None, None
        try:
            response = post(url, key, body)
            if args.provider == "gemini":
                usage = response.get("usageMetadata", {})
                predicted = calls_of((response.get("candidates") or [{}])[0])
                prompt_tokens = usage.get("promptTokenCount")
                output_tokens = (usage.get("candidatesTokenCount") or 0) + (
                    usage.get("thoughtsTokenCount") or 0)
                total_tokens = usage.get("totalTokenCount")
            else:
                usage = response.get("usage", {})
                message = (response.get("choices") or [{}])[0].get("message", {})
                predicted = openai_calls(message)
                prompt_tokens = usage.get("prompt_tokens")
                output_tokens = usage.get("completion_tokens") or 0
                total_tokens = usage.get("total_tokens")
            error = None
        except RuntimeError as err:
            predicted, error = None, str(err)
        elapsed_ms = round((time.time() - started) * 1000)

        gold = [{"name": call["name"], "arguments": call["arguments"]} for call in row["answers"]]
        record = {
            "query": row["query"],
            "gold": gold,
            "predicted": predicted,
            "error": error,
            "latency_ms": elapsed_ms,
            "payload_bytes": payload_bytes(tools, row["query"]),
            "request_bytes": len(json.dumps(body).encode("utf-8")),
            "prompt_tokens": prompt_tokens,
            "output_tokens": output_tokens,
            "total_tokens": total_tokens,
        }
        predicted_calls = predicted or []
        record["name_ok"] = [c["name"] for c in predicted_calls] == [c["name"] for c in gold]
        record["args_ok"] = [c["arguments"] for c in predicted_calls] == [c["arguments"] for c in gold]
        record["exact"] = predicted is not None and record["name_ok"] and record["args_ok"]
        records.append(record)

        if index % 20 == 0 or index == len(rows):
            print(f"  {index}/{len(rows)}  {elapsed_ms} ms  " +
                  (f"{record['prompt_tokens']}+{record['output_tokens']} tokens"
                   if record["prompt_tokens"] else (error or "")), flush=True)

    summary = summarize(records, args.model)
    print(f"\n=== {args.model}")
    print(f"  exact calls          {summary['exact']}/{summary['answering_rows']} "
          f"({summary['exact_rate']:.1%})")
    print(f"  correct tool set     {summary['tool_name_rate']:.1%}")
    print(f"  refused correctly    {summary['abstain_rate']:.1%}")
    print(f"  payload              {summary['bytes_per_row']} B/row, "
          f"{summary['prompt_tokens_per_row']} prompt tokens/row")
    print(f"  per exact call       {summary['bytes_per_exact']} B, "
          f"{summary['tokens_per_exact']} tokens, {summary['ms_per_exact']} ms")

    table = [summary]
    if args.adapter_preds:
        adapter = summarize(adapter_records(args.adapter_preds, rows), args.adapter_label,
                            adapter=True)
        table.insert(0, adapter)
        print(f"\n=== {args.adapter_label}")
        print(f"  exact calls          {adapter['exact']}/{adapter['answering_rows']} "
              f"({adapter['exact_rate']:.1%})")
        print(f"  payload              {adapter['bytes_per_row']} B/row")
        print(f"  per exact call       {adapter['bytes_per_exact']} B, "
              f"{adapter['ms_per_exact']} ms")

    if args.out:
        with open(args.out, "w", encoding="utf-8") as handle:
            for record in records:
                handle.write(json.dumps(record, ensure_ascii=False) + "\n")
        print(f"  predictions          {args.out}")
    if args.report:
        with open(args.report, "w", encoding="utf-8") as handle:
            json.dump(table, handle, indent=2)
            handle.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
