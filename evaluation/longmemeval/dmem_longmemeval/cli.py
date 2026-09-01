"""Command-line interface for dMem LongMemEval evaluation.

Subcommands:

    run        Ingest haystack sessions, trigger semantic barrier, recall,
               and compute retrieval metrics.
    generate   Generate answers from stored retrieval results.
    judge      Judge generated answers with official LongMemEval prompts.
    summarize  Recompute and print aggregated metrics from checkpoint.
    cleanup    Logically forget all ingested memories for this run.

Configuration is loaded from a TOML file (``--config``). CLI flags override
config-file values, which override built-in defaults. API keys are never
stored in the config file; they are read from environment variables named
by ``api_key_env`` in the config.
"""

from __future__ import annotations

import argparse
import json
import logging
import sys

import numpy as np

from .api import DMemClient
from .checkpoint import Checkpoint
from .config import (
    EvalConfig,
    load_config,
    resolve_api_key,
    resolve_socket,
)
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


def positive_int(value: str) -> int:
    parsed = int(value)
    if parsed < 1:
        raise argparse.ArgumentTypeError("must be a positive integer")
    return parsed


# ------------------------------------------------------------------ run --


def cmd_run(args: argparse.Namespace) -> None:
    cfg = load_config(args.config)
    cases, sha = load_dataset(args.dataset)

    if args.max_cases is not None and args.max_cases > 0:
        cases = cases[: args.max_cases]
        logger.info("subset run: first %d cases", len(cases))

    retrieval_mode = args.retrieval_mode or cfg.retrieval.mode
    confirmation_policy = (
        args.confirmation_policy or cfg.ingestion.confirmation_policy
    )
    invalid_evidence_reference_retries = (
        args.invalid_evidence_reference_retries
        if args.invalid_evidence_reference_retries is not None
        else cfg.ingestion.invalid_evidence_reference_retries
    )
    limit = args.limit if args.limit is not None else cfg.retrieval.limit
    max_chars = args.max_chars if args.max_chars is not None else cfg.retrieval.max_chars
    cutoffs = parse_cutoffs(args.cutoffs) if args.cutoffs else cfg.retrieval.cutoffs
    answer_cutoff = (
        args.answer_cutoff if args.answer_cutoff is not None
        else cfg.answer.answer_cutoff
    )
    socket_path = resolve_socket(cfg, args.socket)

    config = RunConfig(
        run_id=args.run_id,
        dataset_sha=sha,
        socket_path=socket_path,
        confirmation_policy=confirmation_policy,
        job_poll_interval=cfg.ingestion.job_poll_interval,
        job_timeout=cfg.ingestion.job_timeout,
        invalid_evidence_reference_retries=invalid_evidence_reference_retries,
        retrieval_mode=retrieval_mode,
        limit=limit,
        max_chars=max_chars,
        cutoffs=cutoffs,
        answer_cutoff=answer_cutoff,
        caller_id=args.caller_id,
        output_dir=args.output,
        request_timeout=args.request_timeout,
        fail_on_degraded=args.fail_on_degraded,
        ingestion_workers=(
            args.ingestion_workers
            if args.ingestion_workers is not None
            else cfg.ingestion.workers
        ),
        retrieval_workers=(
            args.retrieval_workers
            if args.retrieval_workers is not None
            else cfg.retrieval.workers
        ),
    )

    cp = Checkpoint(args.output)

    if args.resume:
        compat = {
            "dataset_sha": sha,
            "run_id": args.run_id,
            "confirmation_policy": confirmation_policy,
            "invalid_evidence_reference_retries": invalid_evidence_reference_retries,
            "retrieval_mode": retrieval_mode,
            "limit": limit,
            "max_chars": max_chars,
            "cutoffs": cutoffs,
            "answer_cutoff": answer_cutoff,
        }
        ok, msg = cp.is_compatible(compat)
        if not ok:
            print(f"resume incompatible: {msg}", file=sys.stderr)
            sys.exit(1)
        logger.info("resume compatible: %s", msg)

    client = DMemClient(
        socket_path, caller_id=args.caller_id, timeout=args.request_timeout
    )
    try:
        health = client.health()
        if not health.get("ready"):
            print("daemon not ready", file=sys.stderr)
            sys.exit(1)
        if health.get("processor_state") == "not_configured":
            print(
                "LongMemEval evaluation requires an enabled processing provider; "
                "use a daemon config with [processing.provider].enabled=true",
                file=sys.stderr,
            )
            sys.exit(1)
        logger.info("daemon ready: mode=%s", health.get("mode"))

        runner = Runner(client, config, cp)
        summary = runner.run(cases)
        print(json.dumps(summary, indent=2))
        if not summary.get("complete", False):
            raise SystemExit(1)
    finally:
        client.close()


# ------------------------------------------------------------ generate --


def cmd_generate(args: argparse.Namespace) -> None:
    cfg = load_config(args.config)

    model = args.model or cfg.answer.model
    if not model:
        print(
            "answer model required: set [answer].model in config or use --model",
            file=sys.stderr,
        )
        sys.exit(1)
    base_url = args.base_url or cfg.answer.base_url or None
    api_key = resolve_api_key(cfg.answer)
    answer_cutoff = (
        args.answer_cutoff if args.answer_cutoff is not None
        else cfg.answer.answer_cutoff
    )
    gen_length = args.gen_length if args.gen_length is not None else cfg.answer.gen_length
    cot = args.cot if args.cot is not None else cfg.answer.cot

    count = generate_answers(
        args.dataset,
        args.output,
        model=model,
        api_key=api_key,
        base_url=base_url,
        answer_cutoff=answer_cutoff,
        gen_length=gen_length,
        cot=cot,
        workers=args.workers if args.workers is not None else cfg.answer.workers,
    )
    print(f"generated {count} answers")


# -------------------------------------------------------------- judge --


def cmd_judge(args: argparse.Namespace) -> None:
    cfg = load_config(args.config)

    model = args.model or cfg.judge.model
    if not model:
        print(
            "judge model required: set [judge].model in config or use --model",
            file=sys.stderr,
        )
        sys.exit(1)
    base_url = args.base_url or cfg.judge.base_url or None
    api_key = resolve_api_key(cfg.judge)

    summary = judge_answers(
        args.dataset,
        args.output,
        model=model,
        api_key=api_key,
        base_url=base_url,
        workers=args.workers if args.workers is not None else cfg.judge.workers,
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
    ingested = cp.load_ingested_qids()
    case_qids = {case.question_id for case in cases}
    failure_records = cp.load_failure_records()
    failures = {"ingestion": set(), "retrieval": set()}
    for rec in failure_records:
        qid = rec.get("question_id")
        phase = rec.get("phase")
        if qid not in case_qids or phase not in failures:
            continue
        if phase == "ingestion" and qid not in ingested:
            failures[phase].add(qid)
        elif phase == "retrieval" and qid not in retrieval_records:
            failures[phase].add(qid)
    failed_qids = set().union(*failures.values())

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

    completed_retrieval = sum(qid in retrieval_records for qid in case_qids)
    result: dict = {
        "complete": not failed_qids and completed_retrieval == len(cases),
        "coverage": completed_retrieval / len(cases) if cases else 1.0,
        "failures": {
            "total_cases": len(failed_qids),
            "total_attempts": sum(
                rec.get("question_id") in case_qids for rec in failure_records
            ),
            "by_phase": {
                phase: len(qids) for phase, qids in failures.items() if qids
            },
        },
        "retrieval": {
            "metrics": summary_metrics,
            "per_type": per_type_metrics,
            "excluded_abstention": excluded_abstention,
            "excluded_no_labels": excluded_no_labels,
            "excluded_errors": excluded_errors,
            "degraded_cases": degraded_cases,
            "total_cases": len(cases),
            "completed_cases": completed_retrieval,
            "evaluated_cases": len(all_metrics),
            "failed_cases": len(failures["retrieval"]),
            "skipped_ingestion_failures": len(failures["ingestion"]),
        },
    }

    ingestion_records = cp.load_ingestion_records()
    ingestion_events = [
        rec
        for records in ingestion_records.values()
        for rec in records
        if rec.get("record_type", "event") == "event"
    ]
    if manifest:
        result["ingestion"] = {
            "confirmation_policy": manifest.get(
                "confirmation_policy", "confirm-all-new"
            ),
            "completed_cases": len(ingested & case_qids),
            "failed_cases": len(failures["ingestion"]),
            "events": len(ingestion_events),
            "candidates": sum(
                len(rec.get("candidate_ids", [])) for rec in ingestion_events
            ),
            "confirmed": sum(
                len(rec.get("confirmed", [])) for rec in ingestion_events
            ),
            "no_memory": sum(
                "no_memory" in rec.get("processing_result_codes", [])
                for rec in ingestion_events
            ),
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
    cfg = load_config(args.config)
    cases, sha = load_dataset(args.dataset)
    cp = Checkpoint(args.output)
    manifest = cp.read_manifest()
    if not manifest:
        print("no manifest found", file=sys.stderr)
        sys.exit(1)

    socket_path = resolve_socket(cfg, args.socket)

    config = RunConfig(
        run_id=manifest["run_id"],
        dataset_sha=manifest["dataset_sha"],
        socket_path=socket_path,
        caller_id=manifest.get("caller_id", "longmemeval"),
        output_dir=args.output,
        request_timeout=args.request_timeout,
    )

    client = DMemClient(
        socket_path, caller_id=config.caller_id, timeout=args.request_timeout
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
    parser.add_argument(
        "--config",
        default=None,
        help="Path to TOML config file (e.g. eval_config.toml)",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    # -- run --
    p = sub.add_parser("run", help="Ingest + recall + retrieval metrics")
    p.add_argument("--dataset", required=True, help="Path to LongMemEval JSON")
    p.add_argument("--socket", default=None, help="dMem Unix socket path")
    p.add_argument("--run-id", required=True, help="Unique run identifier")
    p.add_argument(
        "--confirmation-policy",
        default=None,
        choices=["confirm-all-new", "none"],
        help="Explicit candidate confirmation policy",
    )
    p.add_argument(
        "--retrieval-mode",
        default=None,
        choices=["keyword", "semantic", "hybrid"],
    )
    p.add_argument("--limit", type=int, default=None)
    p.add_argument("--max-chars", type=int, default=None)
    p.add_argument("--cutoffs", default=None)
    p.add_argument("--answer-cutoff", type=int, default=None)
    p.add_argument(
        "--max-cases",
        type=int,
        default=None,
        help="Only run the first N cases (subset/smoke-test). Default: all.",
    )
    p.add_argument("--caller-id", default="longmemeval")
    p.add_argument("--output", default="results/longmemeval")
    p.add_argument("--request-timeout", type=float, default=120.0)
    p.add_argument("--ingestion-workers", type=positive_int, default=None)
    p.add_argument("--retrieval-workers", type=positive_int, default=None)
    p.add_argument(
        "--invalid-evidence-reference-retries",
        type=int,
        default=None,
        help="Retry failed extraction jobs with model output contract errors N times.",
    )
    p.add_argument("--resume", action="store_true")
    p.add_argument("--fail-on-degraded", action="store_true")
    p.set_defaults(func=cmd_run)

    # -- generate --
    p = sub.add_parser("generate", help="Generate answers from retrieval results")
    p.add_argument("--dataset", required=True)
    p.add_argument("--output", default="results/longmemeval")
    p.add_argument("--model", default=None, help="Override [answer].model")
    p.add_argument("--base-url", default=None, help="Override [answer].base_url")
    p.add_argument("--answer-cutoff", type=int, default=None)
    p.add_argument("--gen-length", type=int, default=None)
    p.add_argument("--cot", action=argparse.BooleanOptionalAction, default=None)
    p.add_argument("--workers", type=positive_int, default=None)
    p.set_defaults(func=cmd_generate)

    # -- judge --
    p = sub.add_parser("judge", help="Judge generated answers")
    p.add_argument("--dataset", required=True)
    p.add_argument("--output", default="results/longmemeval")
    p.add_argument("--model", default=None, help="Override [judge].model")
    p.add_argument("--base-url", default=None, help="Override [judge].base_url")
    p.add_argument("--workers", type=positive_int, default=None)
    p.set_defaults(func=cmd_judge)

    # -- summarize --
    p = sub.add_parser("summarize", help="Print aggregated metrics")
    p.add_argument("--dataset", required=True)
    p.add_argument("--output", default="results/longmemeval")
    p.set_defaults(func=cmd_summarize)

    # -- cleanup --
    p = sub.add_parser("cleanup", help="Logically delete ingested memories")
    p.add_argument("--dataset", required=True)
    p.add_argument("--socket", default=None)
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
    logging.getLogger("httpx").setLevel(logging.WARNING)
    args.func(args)
