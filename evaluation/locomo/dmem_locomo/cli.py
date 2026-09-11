"""Command-line interface for dMem LoCoMo evaluation.

Subcommands:

    run        Ingest conversations, trigger semantic barrier, recall, and
               compute turn/session-level retrieval metrics.
    generate   Generate answers from stored retrieval results.
    judge      Judge generated answers with semantic correctness prompts.
    summarize  Recompute retrieval + official F1 + judge metrics.
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

from .api import DMemClient
from .checkpoint import Checkpoint
from .config import (
    DEFAULT_CUTOFFS,
    EvalConfig,
    load_config,
    resolve_api_key,
    resolve_socket,
)
from .dataset import load_dataset, validate_dataset
from .generation import generate_answers
from .judge import judge_answers
from .metrics import score_all
from .runner import RunConfig, Runner

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


def parse_conversations(s: str) -> list[str]:
    ids = [x.strip() for x in s.split(",") if x.strip()]
    if not ids:
        raise ValueError("--conversations requires at least one sample_id")
    seen: set[str] = set()
    ordered: list[str] = []
    for sid in ids:
        if sid not in seen:
            seen.add(sid)
            ordered.append(sid)
    return ordered


def positive_int(value: str) -> int:
    parsed = int(value)
    if parsed < 1:
        raise argparse.ArgumentTypeError("must be a positive integer")
    return parsed


# ------------------------------------------------------------------ run --


def cmd_run(args: argparse.Namespace) -> None:
    cfg = load_config(args.config)
    conversations, sha = load_dataset(args.dataset)

    if args.conversations:
        wanted = set(parse_conversations(args.conversations))
        known = {c.sample_id for c in conversations}
        missing = sorted(wanted - known)
        if missing:
            print(
                "unknown conversation(s): " + ", ".join(missing),
                file=sys.stderr,
            )
            sys.exit(1)
        conversations = [c for c in conversations if c.sample_id in wanted]
        logger.info(
            "subset run: conversations %s",
            ",".join(c.sample_id for c in conversations),
        )

    if args.max_conversations is not None and args.max_conversations > 0:
        conversations = conversations[: args.max_conversations]
        logger.info("subset run: first %d conversations", len(conversations))

    retrieval_mode = args.retrieval_mode or cfg.retrieval.mode
    confirmation_policy = args.confirmation_policy or cfg.ingestion.confirmation_policy
    invalid_evidence_reference_retries = (
        args.invalid_evidence_reference_retries
        if args.invalid_evidence_reference_retries is not None
        else cfg.ingestion.invalid_evidence_reference_retries
    )
    limit = args.limit if args.limit is not None else cfg.retrieval.limit
    max_chars = args.max_chars if args.max_chars is not None else cfg.retrieval.max_chars
    cutoffs = parse_cutoffs(args.cutoffs) if args.cutoffs else cfg.retrieval.cutoffs
    answer_cutoff = (
        args.answer_cutoff if args.answer_cutoff is not None else cfg.answer.answer_cutoff
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
            args.ingestion_workers if args.ingestion_workers is not None
            else cfg.ingestion.workers
        ),
        retrieval_workers=(
            args.retrieval_workers if args.retrieval_workers is not None
            else cfg.retrieval.workers
        ),
        corpus_dir=args.corpus_output,
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
        if not args.corpus_output and health.get("processor_state") == "not_configured":
            print(
                "LoCoMo evaluation requires an enabled processing provider; "
                "use a daemon config with [processing.provider].enabled=true",
                file=sys.stderr,
            )
            sys.exit(1)
        logger.info("daemon ready: mode=%s", health.get("mode"))

        runner = Runner(client, config, cp)
        summary = runner.run(conversations)
        print(json.dumps(summary, indent=2))
        if not summary.get("complete", False):
            raise SystemExit(1)
    finally:
        client.close()


def _client_for_stage(cfg: EvalConfig, args: argparse.Namespace) -> DMemClient:
    socket_path = resolve_socket(cfg, args.socket)
    client = DMemClient(socket_path, caller_id=args.caller_id, timeout=args.request_timeout)
    health = client.health()
    if not health.get("ready"):
        client.close()
        raise RuntimeError("daemon not ready")
    return client


def cmd_corpus(args: argparse.Namespace) -> None:
    cfg = load_config(args.config)
    conversations, sha = load_dataset(args.dataset)
    client = _client_for_stage(cfg, args)
    try:
        if client.health().get("processor_state") == "not_configured":
            raise RuntimeError(
                "corpus ingestion requires an enabled processing provider"
            )
        config = RunConfig(
            run_id=args.run_id,
            dataset_sha=sha,
            socket_path=resolve_socket(cfg, args.socket),
            confirmation_policy=cfg.ingestion.confirmation_policy,
            job_poll_interval=cfg.ingestion.job_poll_interval,
            job_timeout=cfg.ingestion.job_timeout,
            invalid_evidence_reference_retries=cfg.ingestion.invalid_evidence_reference_retries,
            caller_id=args.caller_id,
            output_dir=args.output,
            ingestion_workers=cfg.ingestion.workers,
        )
        if args.resume:
            existing = Checkpoint(args.output).read_manifest()
            if existing:
                for key, value in {
                    "dataset_sha": sha,
                    "run_id": args.run_id,
                    "confirmation_policy": cfg.ingestion.confirmation_policy,
                }.items():
                    if existing.get(key) != value:
                        raise ValueError(f"corpus resume mismatch on {key}")
        result = Runner(client, config, Checkpoint(args.output)).ingest_corpus(conversations)
        print(json.dumps(result, indent=2))
    finally:
        client.close()


def cmd_embeddings(args: argparse.Namespace) -> None:
    cfg = load_config(args.config)
    _, dataset_sha = load_dataset(args.dataset)
    cp = Checkpoint(args.corpus_output)
    manifest = cp.read_manifest()
    if not manifest or manifest.get("run_type") != "corpus":
        raise ValueError("corpus manifest is missing or is not a corpus run")
    if manifest.get("dataset_sha") != dataset_sha:
        raise ValueError("corpus dataset SHA does not match evaluation dataset")
    client = _client_for_stage(cfg, args)
    try:
        config = RunConfig(
            run_id=manifest["run_id"],
            dataset_sha=manifest["dataset_sha"],
            socket_path=resolve_socket(cfg, args.socket),
            caller_id=args.caller_id,
            output_dir=args.corpus_output,
            job_poll_interval=cfg.ingestion.job_poll_interval,
            job_timeout=cfg.ingestion.job_timeout,
        )
        result = Runner(client, config, cp).build_embeddings()
        print(json.dumps(result, indent=2))
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
        args.answer_cutoff if args.answer_cutoff is not None else cfg.answer.answer_cutoff
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
    conversations, _ = load_dataset(args.dataset)
    cp = Checkpoint(args.output)

    manifest = cp.read_manifest()
    cutoffs = manifest.get("cutoffs", DEFAULT_CUTOFFS) if manifest else DEFAULT_CUTOFFS

    retrieval_records = cp.load_retrieval_records()
    generation_records = cp.load_generation_records()
    judge_records = cp.load_judge_records()
    ingested = cp.load_ingested_sample_ids()

    sample_ids = {c.sample_id for c in conversations}
    failure_records = cp.load_failure_records()
    failures = {"ingestion": set(), "retrieval": set()}
    for rec in failure_records:
        key = rec.get("sample_id") or rec.get("qa_id")
        phase = rec.get("phase")
        if phase not in failures:
            continue
        if phase == "ingestion" and key in sample_ids and key not in ingested:
            failures[phase].add(key)
        elif phase == "retrieval" and key not in retrieval_records:
            failures[phase].add(key)
    failed_keys = set().union(*failures.values())

    turn_cases: list[dict[str, float]] = []
    session_cases: list[dict[str, float]] = []
    per_category: dict[int, list[dict[str, float]]] = {}
    excluded_no_evidence = 0
    excluded_errors = 0

    for conv in conversations:
        for qa in conv.qa:
            rec = retrieval_records.get(qa.qa_id)
            if rec is None:
                excluded_errors += 1
                continue
            if qa.is_adversarial or not qa.has_evidence:
                excluded_no_evidence += 1
                continue
            turn_cases.append(_flat(rec["turn_metrics"]))
            session_cases.append(_flat(rec["session_metrics"]))
            per_category.setdefault(qa.category, []).append(_flat(rec["turn_metrics"]))

    turn_summary = _aggregate(turn_cases, cutoffs)
    session_summary = _aggregate(session_cases, cutoffs)
    per_category_summary = {
        str(k): _aggregate(v, cutoffs) for k, v in sorted(per_category.items())
    }

    completed_retrieval = sum(
        1 for conv in conversations for qa in conv.qa if qa.qa_id in retrieval_records
    )
    total_qa = sum(len(c.qa) for c in conversations)

    result: dict = {
        "complete": not failed_keys and completed_retrieval == total_qa,
        "coverage": completed_retrieval / total_qa if total_qa else 1.0,
        "failures": {
            "total_cases": len(failed_keys),
            "total_attempts": len(failure_records),
            "by_phase": {
                phase: len(keys) for phase, keys in failures.items() if keys
            },
        },
        "retrieval": {
            "turn_level": turn_summary,
            "session_level": session_summary,
            "per_category": per_category_summary,
            "excluded_no_evidence_or_adversarial": excluded_no_evidence,
            "excluded_errors": excluded_errors,
            "total_qa": total_qa,
            "completed_qa": completed_retrieval,
            "evaluated_qa": len(turn_cases),
            "failed_qa": len(failures["retrieval"]),
            "skipped_ingestion_failures": len(failures["ingestion"]),
        },
    }

    if generation_records:
        predictions = {qid: rec["hypothesis"] for qid, rec in generation_records.items()}
        result["official_f1"] = score_all(conversations, predictions)

    if judge_records:
        all_acc = [1 if rec.get("label") else 0 for rec in judge_records.values()]
        import numpy as np
        type2acc: dict[int, list[int]] = {}
        for rec in judge_records.values():
            type2acc.setdefault(rec.get("category", 0), []).append(
                1 if rec.get("label") else 0
            )
        result["semantic_judge"] = {
            "overall_accuracy": float(np.mean(all_acc)) if all_acc else 0.0,
            "category_averaged_accuracy": (
                float(np.mean([np.mean(v) for v in type2acc.values()]))
                if type2acc else 0.0
            ),
            "per_category": {k: float(np.mean(v)) for k, v in sorted(type2acc.items())},
            "total_judged": len(all_acc),
        }

    cp.write_summary(result)
    print(json.dumps(result, indent=2))


def _flat(metrics: dict[str, dict[str, float]]) -> dict[str, float]:
    out: dict[str, float] = {}
    for cutoff, values in metrics.items():
        for name, val in values.items():
            out[f"{name}{cutoff}"] = val
    return out


def _aggregate(cases: list[dict[str, float]], cutoffs: list[int]) -> dict[str, dict[str, float]]:
    if not cases:
        return {}
    import numpy as np
    metric_names = ["recall_any", "recall_all", "coverage", "ndcg", "mrr"]
    result: dict[str, dict[str, float]] = {}
    for k in cutoffs:
        result[f"@{k}"] = {}
        for name in metric_names:
            values = [case[f"{name}@{k}"] for case in cases if f"{name}@{k}" in case]
            result[f"@{k}"][name] = float(np.mean(values)) if values else 0.0
    return result


# ------------------------------------------------------------ cleanup --


def cmd_cleanup(args: argparse.Namespace) -> None:
    cfg = load_config(args.config)
    conversations, sha = load_dataset(args.dataset)
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
        caller_id=manifest.get("caller_id", "locomo"),
        output_dir=args.output,
        request_timeout=args.request_timeout,
    )

    client = DMemClient(
        socket_path, caller_id=config.caller_id, timeout=args.request_timeout
    )
    try:
        runner = Runner(client, config, cp)
        result = runner.cleanup(conversations)
        print(json.dumps(result, indent=2))
    finally:
        client.close()


# ---------------------------------------------------------- argparse --


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="dmem-locomo",
        description="Evaluate dMem against the LoCoMo benchmark.",
    )
    parser.add_argument(
        "--config", default=None, help="Path to TOML config file (e.g. eval_config.toml)"
    )
    sub = parser.add_subparsers(dest="command", required=True)

    # -- run --
    p = sub.add_parser("run", help="Ingest + recall + retrieval metrics")
    p.add_argument("--dataset", required=True, help="Path to locomo10.json")
    p.add_argument("--socket", default=None, help="dMem Unix socket path")
    p.add_argument("--run-id", required=True, help="Unique run identifier")
    p.add_argument(
        "--confirmation-policy", default=None,
        choices=["confirm-all-new", "none"],
        help="Explicit candidate confirmation policy",
    )
    p.add_argument(
        "--retrieval-mode", default=None,
        choices=["keyword", "semantic", "graph", "hybrid"],
    )
    p.add_argument("--limit", type=int, default=None)
    p.add_argument("--max-chars", type=int, default=None)
    p.add_argument("--cutoffs", default=None)
    p.add_argument("--answer-cutoff", type=int, default=None)
    p.add_argument(
        "--max-conversations", type=int, default=None,
        help="Only run the first N conversations (subset/smoke-test).",
    )
    p.add_argument(
        "--conversations", default=None,
        help="Comma-separated sample_ids to run (e.g. conv-26,conv-30).",
    )
    p.add_argument("--caller-id", default="locomo")
    p.add_argument("--output", default="results/locomo")
    p.add_argument(
        "--corpus-output", default=None,
        help="Use an existing corpus output and skip ingestion",
    )
    p.add_argument("--request-timeout", type=float, default=120.0)
    p.add_argument("--ingestion-workers", type=positive_int, default=None)
    p.add_argument("--retrieval-workers", type=positive_int, default=None)
    p.add_argument(
        "--invalid-evidence-reference-retries", type=int, default=None,
        help="Retry failed extraction jobs with model output contract errors N times.",
    )
    p.add_argument("--resume", action="store_true")
    p.add_argument("--fail-on-degraded", action="store_true")
    p.set_defaults(func=cmd_run)

    # -- generate --
    p = sub.add_parser("generate", help="Generate answers from retrieval results")
    p.add_argument("--dataset", required=True)
    p.add_argument("--output", default="results/locomo")
    p.add_argument("--model", default=None, help="Override [answer].model")
    p.add_argument("--base-url", default=None, help="Override [answer].base_url")
    p.add_argument("--answer-cutoff", type=int, default=None)
    p.add_argument("--gen-length", type=int, default=None)
    p.add_argument("--cot", action=argparse.BooleanOptionalAction, default=None)
    p.add_argument("--workers", type=positive_int, default=None)
    p.set_defaults(func=cmd_generate)

    # -- corpus --
    p = sub.add_parser("corpus", help="Ingest the shared evaluation corpus")
    p.add_argument("--dataset", required=True)
    p.add_argument("--socket", default=None)
    p.add_argument("--run-id", required=True)
    p.add_argument("--output", required=True)
    p.add_argument("--caller-id", default="locomo")
    p.add_argument("--request-timeout", type=float, default=120.0)
    p.add_argument("--resume", action="store_true")
    p.set_defaults(func=cmd_corpus)

    # -- embeddings --
    p = sub.add_parser("embeddings", help="Build embeddings for a shared corpus")
    p.add_argument("--dataset", required=True)
    p.add_argument("--corpus-output", required=True)
    p.add_argument("--socket", default=None)
    p.add_argument("--caller-id", default="locomo")
    p.add_argument("--request-timeout", type=float, default=120.0)
    p.set_defaults(func=cmd_embeddings)

    # -- judge --
    p = sub.add_parser("judge", help="Judge generated answers")
    p.add_argument("--dataset", required=True)
    p.add_argument("--output", default="results/locomo")
    p.add_argument("--model", default=None, help="Override [judge].model")
    p.add_argument("--base-url", default=None, help="Override [judge].base_url")
    p.add_argument("--workers", type=positive_int, default=None)
    p.set_defaults(func=cmd_judge)

    # -- summarize --
    p = sub.add_parser("summarize", help="Print aggregated metrics")
    p.add_argument("--dataset", required=True)
    p.add_argument("--output", default="results/locomo")
    p.set_defaults(func=cmd_summarize)

    # -- cleanup --
    p = sub.add_parser("cleanup", help="Logically delete ingested memories")
    p.add_argument("--dataset", required=True)
    p.add_argument("--socket", default=None)
    p.add_argument("--output", default="results/locomo")
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
