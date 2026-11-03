#!/usr/bin/env python3
"""Generate the held-out ritual dataset for the needle3 finetuning experiment.

The tool schemas are the harness's own (internal/tooling, dumped to
scripts/needle_tools.json). Queries, values and file paths are split between
train and eval by a sha1 of the value rather than by a seeded shuffle, so the
split survives edits to the repo: a path that was in train yesterday is still in
train after a file is added.

    python3 scripts/needle_rituals.py gen --out-dir scripts/needle-data

Writes {split}.jsonl in the format needle finetune reads: one object per line
with query, reasoning, answers and tools.

The split is doubly held out. Values (paths, patterns, texts, commit lines) are
split by sha1 as described, and every verb family keeps two disjoint template
lists below, so the eval rows are renderings the corpus never shows. That is the
point of the experiment: it scores paraphrase generalization, not recall of a
sentence. Editing a family's "train" list is fair game; editing an "eval" list
invalidates every number reported against it.
"""

import argparse
import hashlib
import json
import os
import sys

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

READ = "read"
GLOB = "glob"
GREP = "grep"
WRITE = "write"

PATTERNS = [
    "context.Context", "schema_version", "call_id", "vendorHash", "ladder_sample",
    "tool_call", "max_seq_len", "ErrToolNotFound", "MergeQueue", "postMessage",
    "Tombstone", "readReplica", "objective_fn", "egress_policy", "blob_ref",
    "intent_confidence", "sentiment_confidence", "SourceLength", "dedup_key",
    "goroutine", "xkcd", "sourdough", "mechanical keyboard", "tide table",
    "kiln schedule", "espresso grind", "cable ferrule", "salmon ladder",
    "moon phase", "bicycle torque", "needle3", "kv_window",
]

TEXTS = [
    "the vendor hash is pinned, so a dependency bump rewrites it",
    "every tool result carries a call id that must match the request",
    "the harness routes reads in parallel and writes one at a time",
    "a stale generation is reported before any rebuild is attempted",
    "the inbox sweep only deletes when confidence clears eight tenths",
    "needle emits a tool call and nothing else when it is sure",
    "the sentinel writes a ledger line every fifteen minutes",
    "compaction drops the oldest turns before the context window overflows",
    "the bridge speaks one JSON record per line on stdin and stdout",
    "a tool that cannot be found returns a typed error, never a panic",
]

COMMIT_MSGS = [
    "tie the manifest order to the request order",
    "drop the sample path from the ledger",
    "keep the retry inside the socket timeout",
    "rename the field to match the schema",
    "record the flag before the gate reads it",
]


# Tools whose arguments are emitted as ordered values instead of a keyed object.
# The order is the contract: values[0] is the first name listed here.
ORDERED = {"grep": ("pattern", "text")}


def compact(tools):
    """Keep the parameter schema, drop the prose.

    The full dump in scripts/needle_tools.json is ~500 tokens of prompt, which is
    the whole context of a 512-token training window, so descriptions and the
    "type":"function" wrappers are dropped. The *structure* stays: which keys a
    tool takes, and that they are strings. That structure is the only signal that
    binds a value to its key - a flatter {name, args} list was tried first and the
    model selected tools correctly while inventing keys like `pattern`, `file_name`
    or `file` in place of `ref` (0.0% exact arguments).

    grep is the exception, and deliberately so. It is the only tool that binds two
    values to two keys with no reason for either to come first, and it scored 0/37
    exact across fifteen phrasings while still being named correctly: the adapter
    fills one field with the whole request and drops the other. An ordered pair
    leaves it one field to fill, and the position does the binding.
    """
    out = []
    for entry in tools:
        function = entry.get("function", entry)
        properties = function.get("parameters", {}).get("properties", {})
        name = function["name"]
        if name in ORDERED:
            out.append({"name": name, "parameters": {"properties": {"values": {
                "type": "array",
                "items": {"type": "string"},
                "minItems": len(ORDERED[name]),
                "maxItems": len(ORDERED[name]),
            }}}})
            continue
        out.append({"name": name,
                    "parameters": {"properties": {
                        key: {"type": value.get("type", "string")}
                        for key, value in properties.items()}}})
    return sorted(out, key=lambda tool: tool["name"])


def split_of(value, train_percent=55):
    """Stable train/eval split: the same value always lands in the same half."""
    digest = hashlib.sha1(value.encode("utf-8")).hexdigest()
    return "train" if int(digest[:8], 16) % 100 < train_percent else "eval"


CORPUS = os.path.join(REPO_ROOT, "scripts", "needle-data", "corpus.json")


def repo_paths(refresh=False):
    """The corpus the tools operate on, pinned to scripts/needle-data/corpus.json.

    The split is stable per value, but the sample was drawn from a pool that grew
    with the tree: adding one .py file shifted three eval rows, so two runs a day
    apart were not scored on the same set. The pool is pinned on first use and
    reused; --refresh-corpus re-pins it deliberately.
    """
    if os.path.exists(CORPUS) and not refresh:
        with open(CORPUS, encoding="utf-8") as handle:
            return sorted(json.load(handle)["paths"])
    skip = {".git", "node_modules", "__pycache__", "tmp_dumptools", "needle-data"}
    out = []
    for dirpath, dirnames, filenames in os.walk(REPO_ROOT):
        dirnames[:] = sorted(d for d in dirnames if d not in skip and not d.startswith("."))
        for name in sorted(filenames):
            if not name.endswith((".go", ".md", ".json", ".sh", ".yml", ".py")):
                continue
            if name.endswith("_test.go"):
                continue
            out.append(os.path.relpath(os.path.join(dirpath, name), REPO_ROOT))
    out.sort()
    os.makedirs(os.path.dirname(CORPUS), exist_ok=True)
    with open(CORPUS, "w", encoding="utf-8") as handle:
        json.dump({"paths": out}, handle, indent=2)
        handle.write("\n")
    return out


def bucket(values, split):
    return sorted(v for v in values if split_of(v) == split)


def render(split, rng_seed):
    import random

    rng = random.Random(rng_seed)
    paths = bucket(repo_paths(), split)
    dirs = bucket(sorted({os.path.dirname(p) or "." for p in repo_paths()}), split)
    patterns = bucket(PATTERNS, split)
    texts = bucket(TEXTS, split)
    commits = bucket(COMMIT_MSGS, split)
    if not paths or not dirs or not patterns or not texts:
        raise SystemExit(f"split {split!r} produced an empty pool; the checkout looks wrong")

    def pick(pool):
        return rng.choice(pool)

    def one(name, arguments, reasoning, query):
        return {"query": query, "reasoning": reasoning,
                "answers": [{"name": name, "arguments": arguments}]}

    def read():
        path = pick(paths)
        return one(READ, {"ref": path},
                   f'ref="{path}" is the file named in the request.',
                   rng.choice(READ_TEMPLATES[split]).format(path=path))

    def glob():
        directory = pick(dirs)
        pattern = f"{directory}/*.go"
        return one(GLOB, {"pattern": pattern},
                   f'pattern="{pattern}" is the glob the request asks for.',
                   rng.choice(GLOB_TEMPLATES[split]).format(pattern=pattern, dir=directory))

    def grep():
        pattern, text = pick(patterns), pick(texts)
        return one(GREP, {"values": [pattern, text]},
                   f'the request quotes "{pattern}" first and "{text}" second, in that order.',
                   rng.choice(GREP_TEMPLATES[split]).format(pattern=pattern, text=text))

    def write():
        path, content = pick(paths), pick(commits)
        return one(WRITE, {"ref": path, "content": content},
                   f'ref="{path}" and content="{content}" are both quoted in the request.',
                   rng.choice(WRITE_TEMPLATES[split]).format(path=path, content=content))

    def ritual():
        first, second = pick(paths), pick(dirs)
        pattern = f"{second}/*.go"
        return {"query": rng.choice(RITUAL_TEMPLATES[split]).format(path=first, pattern=pattern),
                "reasoning": f'the request names {first} first and the glob {pattern} second.',
                "answers": [{"name": READ, "arguments": {"ref": first}},
                            {"name": GLOB, "arguments": {"pattern": pattern}}]}

    def refusal():
        return {"query": pick(REFUSALS[split]), "reasoning": "no tool in the set serves this request.",
                "answers": []}

    builders = [(read, 0.35), (glob, 0.20), (grep, 0.23), (write, 0.10), (ritual, 0.07), (refusal, 0.05)]
    rows = []
    for build, share in builders:
        for _ in range(max(1, round(share * COUNT[split]))):
            rows.append(build())
    rng.shuffle(rows)
    return rows


READ_TEMPLATES = {
    "train": ["open {path}", "show me {path}", "read the file at {path}", "pull up {path}"],
    "eval": ["cat the contents of {path}", "I want to look at {path}",
             "print out {path} for me", "fetch the file {path}"],
}

GLOB_TEMPLATES = {
    "train": ["list {pattern}", "which files match {pattern}", "enumerate the .go files in {dir}"],
    "eval": ["give me every file matching {pattern}", "what .go files live in {dir}",
             "glob for {pattern}"],
}

GREP_TEMPLATES = {
    "train": ["find {pattern} in the text \"{text}\"",
              "where does {pattern} appear in \"{text}\"",
              "does \"{text}\" mention {pattern}",
              "which line of \"{text}\" contains {pattern}",
              "is {pattern} present in \"{text}\"",
              "in \"{text}\", is there any mention of {pattern}",
              "check whether \"{text}\" says {pattern}",
              "quote the line in \"{text}\" that has {pattern}",
              "every occurrence of {pattern} in \"{text}\", one per line",
              "scan \"{text}\" and point out {pattern}",
              "count the occurrences of {pattern} in \"{text}\"",
              "what context surrounds {pattern} in \"{text}\"",
              "look at \"{text}\" and tell me if {pattern} turns up",
              "highlight {pattern} wherever it occurs in \"{text}\"",
              "does {pattern} show up anywhere in \"{text}\""],
    "eval": ["locate {pattern} inside \"{text}\"", "search \"{text}\" for {pattern}"],
}

WRITE_TEMPLATES = {
    "train": ["write \"{content}\" to {path}", "save \"{content}\" at {path}"],
    "eval": ["put \"{content}\" into {path}", "create {path} containing \"{content}\""],
}

RITUAL_TEMPLATES = {
    "train": ["read {path} and then list {pattern}", "enumerate {pattern} after opening {path}"],
    "eval": ["first open {path}, then give me {pattern}",
             "open {path}; once done, list every file matching {pattern}"],
}

REFUSALS = {
    "train": ["what is the weather in Lisbon this weekend?",
              "explain monads to me in one paragraph.",
              "tell me a joke about compilers."],
    "eval": ["summarise the history of the Byzantine empire.",
             "what should I have for dinner tonight?",
             "how do I get better at chess?"],
}

COUNT = {"train": 600, "eval": 160}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("command", choices=["gen"])
    parser.add_argument("--out-dir", default=os.path.join(REPO_ROOT, "scripts", "needle-data"))
    parser.add_argument("--seed", type=int, default=7)
    parser.add_argument("--refresh-corpus", action="store_true",
                        help="re-pin the corpus from the current tree")
    args = parser.parse_args(argv)

    tools_path = os.path.join(REPO_ROOT, "scripts", "needle_tools.json")
    with open(tools_path, encoding="utf-8") as handle:
        tools = compact(json.load(handle))

    repo_paths(refresh=args.refresh_corpus)  # pin/refresh the corpus before sampling
    os.makedirs(args.out_dir, exist_ok=True)
    with open(os.path.join(args.out_dir, "tools.json"), "w", encoding="utf-8") as handle:
        json.dump(tools, handle, indent=2)
        handle.write("\n")

    for split in ("train", "eval"):
        rows = render(split, args.seed)
        for row in rows:
            row["tools"] = tools
        out = os.path.join(args.out_dir, f"{split}.jsonl")
        with open(out, "w", encoding="utf-8") as handle:
            for row in rows:
                handle.write(json.dumps(row, ensure_ascii=False) + "\n")
        answers = sum(1 for row in rows if row["answers"])
        multi = sum(1 for row in rows if len(row["answers"]) > 1)
        print(f"{split}: {len(rows)} rows ({answers} with calls, {multi} multi-call) -> {out}")

    train_refs = set()
    eval_refs = set()
    for split, bucket_set in (("train", train_refs), ("eval", eval_refs)):
        with open(os.path.join(args.out_dir, f"{split}.jsonl"), encoding="utf-8") as handle:
            for line in handle:
                for call in json.loads(line)["answers"]:
                    bucket_set.add(json.dumps(call["arguments"], sort_keys=True))
    overlap = train_refs & eval_refs
    if overlap:
        print(f"WARNING: {len(overlap)} identical call(s) appear in both splits", file=sys.stderr)
    else:
        print("splits are disjoint on call arguments")


if __name__ == "__main__":
    main()
