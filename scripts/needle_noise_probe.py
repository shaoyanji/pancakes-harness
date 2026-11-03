#!/usr/bin/env python3
"""Repeat the same rows through the .cact engine and report how much they move.

The engine's C API is the whole decoding surface: ``needle_complete(text, audio,
audio_len, max_new_tokens, buffer, buffer_len)``. It takes no seed and no
temperature, and the shared object exports nothing that sets one, so
``scripts/needle_bench.py`` cannot pin decoding the way the JAX reference
decoder in ``needle.model.run`` can (that path has ``--seed`` and
``--temperature``, and is 16-26 s per row).

What is left is to measure the noise and pay for it with repetitions. This probe
answers two questions a single bench run cannot:

  * within one process, does the same row asked twice give the same answer?
  * across two processes, does the same row give the same answer?

    python3 scripts/needle_noise_probe.py --weights rituals-tuned.cact \
        --eval scripts/needle-data/eval.jsonl --limit 20 --rounds 2

The last line is a digest of every round-0 prediction, so two invocations can be
compared with a diff of that one line.
"""

import argparse
import hashlib
import json
import sys

import needle


def digest(records):
    body = json.dumps([[r["query"], r["predicted"]] for r in records], sort_keys=True)
    return hashlib.sha1(body.encode("utf-8")).hexdigest()[:12]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--eval", required=True)
    parser.add_argument("--weights", required=True)
    parser.add_argument("--limit", type=int, default=20)
    parser.add_argument("--rounds", type=int, default=2)
    parser.add_argument("--max-new-tokens", type=int, default=192)
    args = parser.parse_args(argv)

    rows = []
    with open(args.eval, encoding="utf-8") as handle:
        for line in handle:
            if line.strip():
                rows.append(json.loads(line))
    rows = rows[: args.limit]

    tools_json = json.dumps(rows[0]["tools"], separators=(",", ":"))
    agent = needle.Needle(tools=tools_json, weights=args.weights, stateless=True)

    rounds = []
    for index in range(args.rounds):
        records = []
        for row in rows:
            response = agent.complete(row["query"], args.max_new_tokens)
            calls = response.get("function_calls") or []
            predicted = [{"name": c.get("name"), "arguments": c.get("arguments")}
                         for c in calls]
            records.append({"query": row["query"], "predicted": predicted})
        rounds.append(records)
        print(f"round {index}: {digest(records)}", flush=True)
    agent.close()

    baseline = rounds[0]
    for index, records in enumerate(rounds[1:], 1):
        same = sum(1 for a, b in zip(baseline, records) if a["predicted"] == b["predicted"])
        print(f"round 0 vs round {index}: {same}/{len(rows)} identical")

    for a, rest in zip(baseline, zip(*rounds[1:])):
        answers = {json.dumps(r["predicted"], sort_keys=True) for r in (a, *rest)}
        if len(answers) > 1:
            print(f"  diverged: {a['query'][:66]}")
    print(f"DIGEST {digest(baseline)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
