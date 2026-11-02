#!/usr/bin/env python3
"""Write a ladder slice of a needle3 checkpoint.

The published checkpoint is 20 layers deep and its training compile needs about
4.5 GB of XLA memory, which the 2-core/8 GB sandbox kills (SIGTERM at ~4.5 GB RSS).
needle's own ladder makes a shallower rung a first-class artifact — `needle build
--layers N` exports one — so the experiment trains a rung and compares it against
the same rung of the base weights, which keeps the only difference the adapter.

    python3 scripts/needle_rung.py --depth 12 --out checkpoints/needle3-rung12.safetensors
"""

import argparse
import os

os.environ.setdefault("JAX_PLATFORMS", "cpu")

from needle.model.checkpoints import write_checkpoint
from needle.model.finetune import rung
from needle.model.run import load_checkpoint

FORMAT_VERSION = 2


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--checkpoint", default="checkpoints/needle3.safetensors")
    parser.add_argument("--depth", type=int, required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    params, config, run = load_checkpoint(args.checkpoint, return_run=True)
    sliced_params, sliced_config = rung(params, config, args.depth)
    if sliced_config.num_layers == config.num_layers:
        raise SystemExit(f"--depth {args.depth} is not below the checkpoint's {config.num_layers} layers")

    os.makedirs(os.path.dirname(args.out) or ".", exist_ok=True)
    write_checkpoint(args.out, {
        "format_version": FORMAT_VERSION,
        "params": sliced_params,
        "config": dict(vars(sliced_config)),
        "step": None,
        "run": run,
    })
    print(f"{args.out}: {sliced_config.num_layers} layers "
          f"({config.num_layers} -> {args.depth}), {os.path.getsize(args.out) / 1e6:.2f} MB")


if __name__ == "__main__":
    main()
