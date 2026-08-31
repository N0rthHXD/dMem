"""Command-line interface for dMem LongMemEval evaluation.

Subcommands:

    run        Ingest haystack sessions, trigger semantic barrier, recall,
               and compute retrieval metrics.
    generate   Generate answers from stored retrieval results.
    judge      Judge generated answers with official LongMemEval prompts.
    summarize  Recompute and print aggregated metrics from checkpoint.
    cleanup    Logically forget all ingested memories for this run.
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys

import numpy as np

from .api import DMemClient
from .checkpoint import Checkpoint
from .dataset import load_dataset
from .generation import generate_answers
from .judge import judge_answers
from .metrics import aggregate_metrics
from .runner import DEFAULT_CUTOFFS, RunConfig, Runner

logger = logging.getLogger(__name__)


def parse_cutoffs(s: str) -> list[int]:
    parts = [int(x) for x in s.split(",")]
    seen: set[int] = set()
    for p in parts:
        if p <= 0:
            raise ValueError(f"cutoff must be positive: {p}")
        if p in seen:
            raise ValueError(f"duplicate cutoff: {p}")
        seen.add(p)
    return parts


# ------------------------------------------------------------------ run --


def cmd_run(args: argparse.Namespace) -> None:
    cases, sha = load_dataset(args.dataset)
    cutoffs = parse_cutoffs(args.cutoffs)

    config = RunConfig(
        run_id=args.run_id,
        dataset_sha=sha,
        socket_path=args.socket,
        retrieval_mode=args.retrieval_mode,
        limit=args.limit,
        max_chars=args.max_chars,
        cutoffs=cutoffs,
        answer_cutoff=args.answer_cutoff,
        answer_model=args.answer_model,
        judge_model=args.judge_model,
        caller_id=args.caller_id,
        output_dir=args.output,
        request_timeout=args.request_timeout,
        fail_on_degraded=args.fail_on_degraded,
    )

    cp = Checkpoint(args.output)

    if args.resume:
        compat = {
            "dataset_sha": sha,
            "run_id": args.run_id,
            "retrieval_mode": args.retrieval_mode,
            "limit": args.limit,
            "max_chars": args.max_chars,
            "cutoffs": cutoffs,
            "answer_cutoff": args.answer_cutoff,
            "answer_model": args.answer_model,
            "judge_model": args.judge_model,
        }
        ok, msg = cp.is_compatible(compat)
        if not ok:
            print(f"resume incompatible: {msg}", file=sys.stderr)
            sys.exit(1)
        logger.info("resume compatible: %s", msg)

    client = DMemClient(
        args.socket, caller_id=args.caller_id, timeout=args.request_timeout
    )
    try:
        health = client.health()
        if not health.get("ready"):
            print("daemon not ready", file=sys.stderr)
            sys.exit(1)
        logger.info("daemon ready: mode=%s", health.get("mode"))

        runner = Runner(client, config, cp)
        summary = runner.run(cases)
        print(json.dumps(summary, indent=2))
    finally:
        client.close()


# ------------------------------------------------------------ generate --


def cmd_generate(args: argparse.Namespace) -> None:
    api_key = args.api_key or os.environ.get("OPENAI_API_KEY", "")
    if not api_key:
        print("API key required (--api-key or OPENAI_API_KEY)", file=sys.stderr)
        sys.exit(1)
    base_url = args.base_url or os.environ.get("OPENAI_BASE_URL")

    count = generate_answers(
        args.dataset,
        args.output,
        model=args.model,
        api_key=api_key,
        base_url=base_url,
        answer_cutoff=args.answer_cutoff,
        gen_length=args.gen_length,
        cot=args.cot,
    )
    print(f"generated {count} answers")


# -------------------------------------------------------------- judge --


def cmd_judge(args: argparse.Namespace) -> None:
    api_key = args.api_key or os.environ.get("OPENAI_API_KEY", "")
    if not api_key:
        print("API key required (--api-key or OPENAI_API_KEY)", file=sys.stderr)
        sys.exit(1)
    base_url = args.base_url or os.environ.get("OPENAI_BASE_URL")

    summary = judge_answers(
        args.dataset,
        args.output,
        model=args.model,
        api_key=api_key,
        base_url=base_url,
    )
    print(json.dumps(summary, indent=2))


# ---------------------------------------------------------- summarize --


def cmd_summarize(args: argparse.Namespace) -> None:
    cases, _ = load_dataset(args.dataset)
    cp = Checkpoint(args.output)

    manifest = cp.read_manifest()
    cutoffs = manifest.get("cutoffs", DEFAULT_CUTOFFS) if manifest else DEFAULT_CUTOFFS

    retrieval_records = cp.load_retrieval_records()
    judge_records = cp.load_judge_records()

    all_metrics: list[dict[str, dict[str, float]]] = []
    excluded_abstention = 0
    excluded_no_labels = 0
    excluded_errors = 0
    degraded_cases = 0
    per_type: dict[str, list[dict[str, dict[str, float]]]] = {}

    for case in cases:
        rec = retrieval_records.get(case.question_id)
        if rec is None:
            excluded_errors += 1
            continue
        if rec.get("degraded"):
            degraded_cases += 1
        if case.is_abstention:
            excluded_abstention += 1
            continue
        if not rec.get("has_answer_session_labels"):
            excluded_no_labels += 1
            continue
        all_metrics.append({case.question_id: rec["metrics"]})
        per_type.setdefault(case.question_type, []).append(
            {case.question_id: rec["metrics"]}
        )

    summary_metrics = aggregate_metrics(all_metrics, cutoffs)
    per_type_metrics = {
        qt: aggregate_metrics(entries, cutoffs) for qt, entries in per_type.items()
    }

    result: dict = {
        "retrieval": {
            "metrics": summary_metrics,
            "per_type": per_type_metrics,
            "excluded_abstention": excluded_abstention,
            "excluded_no_labels": excluded_no_labels,
            "excluded_errors": excluded_errors,
            "degraded_cases": degraded_cases,
            "total_cases": len(cases),
            "evaluated_cases": len(all_metrics),
        },
    }

    if judge_records:
        case_map = {c.question_id: c for c in cases}
        all_acc: list[int] = []
        type2acc: dict[str, list[int]] = {}
        abstention_acc: list[int] = []

        for qid, jrec in judge_records.items():
            case = case_map.get(qid)
            if case is None:
                continue
            acc = 1 if jrec.get("label") else 0
            all_acc.append(acc)
            type2acc.setdefault(case.question_type, []).append(acc)
            if case.is_abstention:
                abstention_acc.append(acc)

        result["qa"] = {
            "overall_accuracy": float(np.mean(all_acc)) if all_acc else 0.0,
            "task_averaged_accuracy": (
                float(np.mean([np.mean(v) for v in type2acc.values()]))
                if type2acc
                else 0.0
            ),
            "abstention_accuracy": (
                float(np.mean(abstention_acc)) if abstention_acc else 0.0
            ),
            "per_type": {k: float(np.mean(v)) for k, v in type2acc.items()},
            "total_judged": len(all_acc),
        }

    cp.write_summary(result)
    print(json.dumps(result, indent=2))


# ------------------------------------------------------------ cleanup --


def cmd_cleanup(args: argparse.Namespace) -> None:
    cases, sha = load_dataset(args.dataset)
    cp = Checkpoint(args.output)
    manifest = cp.read_manifest()
    if not manifest:
        print("no manifest found", file=sys.stderr)
        sys.exit(1)

    config = RunConfig(
        run_id=manifest["run_id"],
        dataset_sha=manifest["dataset_sha"],
        socket_path=args.socket,
        caller_id=manifest.get("caller_id", "longmemeval"),
        output_dir=args.output,
    )

    client = DMemClient(
        args.socket, caller_id=config.caller_id, timeout=args.request_timeout
    )
    try:
        runner = Runner(client, config, cp)
        result = runner.cleanup(cases)
        print(json.dumps(result, indent=2))
    finally:
        client.close()


# ---------------------------------------------------------- argparse --


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="dmem-longmemeval",
        description="Evaluate dMem against the LongMemEval benchmark.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    # -- run --
    p = sub.add_parser("run", help="Ingest + recall + retrieval metrics")
    p.add_argument("--dataset", required=True, help="Path to LongMemEval JSON")
    p.add_argument("--socket", required=True, help="dMem Unix socket path")
    p.add_argument("--run-id", required=True, help="Unique run identifier")
    p.add_argument(
        "--retrieval-mode",
        default="keyword",
        choices=["keyword", "semantic", "hybrid"],
    )
    p.add_argument("--limit", type=int, default=100)
    p.add_argument("--max-chars", type=int, default=100000)
    p.add_argument("--cutoffs", default="1,3,5,10,30,50")
    p.add_argument("--answer-cutoff", type=int, default=30)
    p.add_argument("--answer-model", default=None)
    p.add_argument("--judge-model", default=None)
    p.add_argument("--caller-id", default="longmemeval")
    p.add_argument("--output", default="results/longmemeval")
    p.add_argument("--request-timeout", type=float, default=120.0)
    p.add_argument("--resume", action="store_true")
    p.add_argument("--fail-on-degraded", action="store_true")
    p.set_defaults(func=cmd_run)

    # -- generate --
    p = sub.add_parser("generate", help="Generate answers from retrieval results")
    p.add_argument("--dataset", required=True)
    p.add_argument("--output", default="results/longmemeval")
    p.add_argument("--model", required=True)
    p.add_argument("--api-key", default=None)
    p.add_argument("--base-url", default=None)
    p.add_argument("--answer-cutoff", type=int, default=30)
    p.add_argument("--gen-length", type=int, default=500)
    p.add_argument("--cot", action="store_true")
    p.set_defaults(func=cmd_generate)

    # -- judge --
    p = sub.add_parser("judge", help="Judge generated answers")
    p.add_argument("--dataset", required=True)
    p.add_argument("--output", default="results/longmemeval")
    p.add_argument("--model", required=True)
    p.add_argument("--api-key", default=None)
    p.add_argument("--base-url", default=None)
    p.set_defaults(func=cmd_judge)

    # -- summarize --
    p = sub.add_parser("summarize", help="Print aggregated metrics")
    p.add_argument("--dataset", required=True)
    p.add_argument("--output", default="results/longmemeval")
    p.set_defaults(func=cmd_summarize)

    # -- cleanup --
    p = sub.add_parser("cleanup", help="Logically delete ingested memories")
    p.add_argument("--dataset", required=True)
    p.add_argument("--socket", required=True)
    p.add_argument("--output", default="results/longmemeval")
    p.add_argument("--request-timeout", type=float, default=120.0)
    p.set_defaults(func=cmd_cleanup)

    return parser


def main() -> None:
    parser = build_parser()
    args = parser.parse_args()
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    args.func(args)
