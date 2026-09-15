"""Command line entry point for deterministic calibration."""

from __future__ import annotations

import argparse
import json
import os
import sys
from typing import List

from .fit import QueryExample, fit_weights
from .report import build_report
from .space import default_parameter_space
from .splits import assert_disjoint, assign_split, split_examples

_SPLIT_SEED = "dmem-calibration-split-v1"


def _load_examples(path: str) -> List[QueryExample]:
    with open(path, "r", encoding="utf-8") as handle:
        raw = json.load(handle)
    if not isinstance(raw, list):
        raise ValueError("examples must be a JSON list")
    examples = []
    for item in raw:
        examples.append(
            QueryExample(
                query_id=str(item["query_id"]),
                benchmark=str(item["benchmark"]),
                query_type=str(item["query_type"]),
                features={str(key): float(value) for key, value in item.get("features", {}).items()},
                label=float(item["label"]),
                group_id=str(item.get("group_id", "")),
            )
        )
    return examples


def _write_json(path: str, value) -> None:
    directory = os.path.dirname(os.path.abspath(path))
    temporary = os.path.join(directory, "." + os.path.basename(path) + ".tmp")
    with open(temporary, "w", encoding="utf-8") as handle:
        json.dump(value, handle, indent=2, sort_keys=True)
        handle.write("\n")
    os.replace(temporary, path)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="dmem-calibration")
    parser.add_argument("--examples", required=True, help="JSON list of graded query examples")
    parser.add_argument("--output", required=True, help="destination calibration report JSON")
    parser.add_argument("--passes", type=int, default=2, help="coordinate search passes")
    parser.add_argument("--seed", default=_SPLIT_SEED, help="deterministic split/bootstrap seed")
    return parser


def main(argv: List[str] = None) -> int:
    args = build_parser().parse_args(argv)
    examples = _load_examples(args.examples)
    splits = split_examples(examples, seed=args.seed)
    assert_disjoint(splits)
    space = default_parameter_space()
    weights, fit_summary = fit_weights(space, splits, passes=args.passes)
    report = build_report(space, splits, weights, fit_summary)
    report["split_assignment"] = {example.query_id: assign_split(example.query_id, args.seed) for example in examples}
    _write_json(args.output, report)
    print(json.dumps({"promoted": report["promotion"]["promoted"], "reasons": report["promotion"]["reasons"]}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
