#!/usr/bin/env python3
"""Build a reproducible ablation and end-to-end report from checkpoints.

Each positional argument names one ablation cell and the output directory that
holds its ``retrieval.jsonl`` checkpoint::

    python ablation_report.py --output report.json \
        S=results/semantic-001 SK=results/hybrid-001 SKPM=results/mmr-001

Repeating a cell with ``CELL@RUN=DIR`` adds a repeated run for the paired
bootstrap confidence interval. The report refuses cells whose query coverage
differs from the first cell.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from dmem_locomo.reporting import (
    ABLATION_MATRIX,
    ReportingError,
    assert_identical_query_coverage,
    build_end_to_end_report,
    promotion_gates,
)


def _load_run(directory: Path) -> tuple[list[dict], dict]:
    path = directory / "retrieval.jsonl"
    if not path.exists():
        raise SystemExit(f"retrieval.jsonl not found under {directory}")
    records = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line:
            records.append(json.loads(line))
    generation = {
        record["qa_id"]: record
        for record in _load_jsonl(directory / "generation.jsonl")
    }
    judge = {
        record["qa_id"]: record
        for record in _load_jsonl(directory / "judge.jsonl")
    }
    for record in records:
        generated = generation.get(record["qa_id"], {})
        judged = judge.get(record["qa_id"], {})
        record["prompt_tokens"] = generated.get("prompt_tokens", 0) + judged.get("prompt_tokens", 0)
        record["completion_tokens"] = generated.get("completion_tokens", 0) + judged.get("completion_tokens", 0)
    manifest_path = directory / "manifest.json"
    if not manifest_path.exists():
        raise SystemExit(f"manifest.json not found under {directory}")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    required = (
        "corpus_id", "dataset_sha", "answer_model", "generation_prompt",
        "generation_temperature", "generation_max_tokens", "judge_model",
        "judge_prompt", "judge_temperature", "judge_max_tokens",
    )
    missing = [key for key in required if manifest.get(key) in (None, "")]
    if missing:
        raise SystemExit(f"incomplete end-to-end identity under {directory}: {', '.join(missing)}")
    identity = {
        "corpus_id": manifest.get("corpus_id"), "dataset_sha": manifest.get("dataset_sha"),
        "generation": ":".join(str(manifest.get(key, "")) for key in ("answer_model", "generation_prompt", "generation_temperature", "generation_max_tokens")),
        "judge": ":".join(str(manifest.get(key, "")) for key in ("judge_model", "judge_prompt", "judge_temperature", "judge_max_tokens")),
    }
    return records, identity


def _load_jsonl(path: Path) -> list[dict]:
    if not path.exists():
        raise SystemExit(f"{path.name} not found under {path.parent}; end-to-end report requires QA data")
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def _parse_spec(spec: str) -> tuple[str, str]:
    cell, sep, directory = spec.partition("=")
    if not sep or not cell or not directory:
        raise SystemExit(f"invalid run spec {spec!r}; expected CELL=DIR or CELL@RUN=DIR")
    cell = cell.split("@", 1)[0]
    if cell not in ABLATION_MATRIX:
        raise SystemExit(f"unknown ablation cell {cell!r}")
    return cell, directory


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("runs", nargs="+", help="CELL=DIR or CELL@RUN=DIR")
    parser.add_argument("--output", "-o", required=True, help="report JSON path")
    parser.add_argument("--baseline-cell", default="S")
    parser.add_argument("--check", action="store_true", help="exit non-zero when promotion gates fail")
    args = parser.parse_args(argv)

    runs: dict[str, list[list[dict]]] = {}
    identities: dict[str, list[dict]] = {}
    for spec in args.runs:
        cell, directory = _parse_spec(spec)
        records, identity = _load_run(Path(directory))
        runs.setdefault(cell, []).append(records)
        identities.setdefault(cell, []).append(identity)
    missing_cells = sorted(set(ABLATION_MATRIX) - set(runs))
    if missing_cells:
        raise SystemExit(f"missing fixed ablation cells: {', '.join(missing_cells)}")
    assert_identical_query_coverage(runs)
    report = build_end_to_end_report(runs=runs, baseline_cell=args.baseline_cell, identities=identities)
    gates = promotion_gates(report, baseline_cell=args.baseline_cell)
    report["promotion_gates"] = gates
    Path(args.output).write_text(json.dumps(report, indent=2, sort_keys=True), encoding="utf-8")
    json.dump(gates, sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")
    if args.check and not gates["passes"]:
        raise SystemExit("promotion gates failed: " + ", ".join(gates["failures"]))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
