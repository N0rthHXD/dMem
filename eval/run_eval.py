#!/usr/bin/env python3
"""Command-line entry point for the LoCoMo Chinese-subset evaluation.

Usage:
    python run_eval.py --config config.zh-subset.yaml
    python run_eval.py --config config.zh-subset.yaml --phases retrieval,qa
    python run_eval.py --config config.zh-subset.yaml --phases ablation
    python run_eval.py --config config.zh-subset.yaml --run-id my-run

Everything else (dataset, dMem daemon, LLM, embedding, metrics, ablation modes)
is configured in the YAML file. See README.md for the full reference.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable

from dmem_eval import __version__
from dmem_eval.ablation import build_ablation, graph_corpus_counts
from dmem_eval.config import load_config
from dmem_eval.daemon import ManagedDaemon
from dmem_eval.dataset import load_dataset, validate_dataset
from dmem_eval.dmem_client import DMemClient
from dmem_eval.io_utils import write_json
from dmem_eval.llm import LLMClient
from dmem_eval.phases import (
    EvalContext,
    load_maps,
    run_build,
    run_qa,
    run_retrieval,
)
from dmem_eval.report import build_report

PHASE_ORDER = ("build", "ablation", "retrieval", "qa")


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Evaluate dMem on the LoCoMo Chinese subset.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--config", required=True, help="path to the YAML evaluation config")
    parser.add_argument(
        "--phases",
        default=None,
        help="comma-separated subset of build,ablation,retrieval,qa (default: config value)",
    )
    parser.add_argument("--run-id", default=None, help="override the run id (output directory)")
    return parser.parse_args(argv)


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _daemon_commit(binary: Path) -> str | None:
    try:
        result = subprocess.run(
            ["go", "version", "-m", str(binary)], capture_output=True, text=True, timeout=30
        )
    except (OSError, subprocess.SubprocessError):
        return None
    for line in result.stdout.splitlines():
        line = line.strip()
        if line.startswith("build\tvcs.revision="):
            return line.split("=", 1)[1]
    return None


def _resolve_phases(args: argparse.Namespace, config) -> list[str]:
    requested = args.phases.split(",") if args.phases else config.phases
    phases = [phase.strip() for phase in requested if phase.strip()]
    unknown = set(phases) - set(PHASE_ORDER)
    if unknown:
        raise SystemExit(f"unknown phases: {sorted(unknown)}")
    return [phase for phase in PHASE_ORDER if phase in phases]


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    config = load_config(args.config)
    run_id = args.run_id or str(config.get("run_id") or "zh-subset")
    phases = _resolve_phases(args, config)
    run_dir = config.run_dir(run_id)
    run_dir.mkdir(parents=True, exist_ok=True)

    dataset_path = config.resolve_path(str(config.section("dataset")["path"]))
    samples, qa_items = load_dataset(dataset_path, config.sample_ids, config.categories)
    validation = validate_dataset(samples, qa_items)

    print(f"LoCoMo evaluation {run_id} (harness {__version__})")
    print(f"  dataset: {dataset_path}")
    print(f"  {validation['conversations']} conversations, {validation['sessions']} sessions, "
          f"{validation['turns']} turns, {validation['qa']} QA")
    print(f"  phases: {', '.join(phases)}")

    write_json(run_dir / "config.snapshot.json", config.data)

    llm_cfg = config.section("llm")
    llm = LLMClient(
        base_url=str(llm_cfg["base_url"]),
        model=str(llm_cfg["model"]),
        api_key_env=str(llm_cfg.get("api_key_env") or "DMEM_PROVIDER_KEY"),
        temperature=float(llm_cfg.get("temperature", 0.0)),
        max_tokens=int(llm_cfg.get("max_tokens") or 2048),
        timeout_seconds=float(llm_cfg.get("timeout_seconds") or 180),
        max_retries=int(llm_cfg.get("max_retries") or 4),
        max_concurrent=int(llm_cfg.get("max_concurrent") or 8),
    )

    dmem_cfg = config.section("dmem")
    managed = bool(dmem_cfg.get("manage_daemon", True))
    if "ablation" in phases and not managed:
        raise SystemExit(
            "the ablation phase switches daemon configurations and requires "
            "dmem.manage_daemon: true"
        )
    fallback_endpoint = str(dmem_cfg.get("endpoint") or "http://127.0.0.1:7373")
    binary = config.resolve_path(str(dmem_cfg.get("binary"))) if managed else None

    def with_daemon(
        name: str,
        overlay: dict[str, Any] | None,
        action: Callable[[EvalContext, ManagedDaemon | None], Any],
        *,
        target_dir: Path | None = None,
    ) -> Any:
        """Run ``action`` against one daemon configuration.

        When the harness manages the daemon, every call starts a fresh process
        (so a changed graph policy or embedding provider takes effect) and stops
        it afterwards. All variants of a run share the built data directory.
        """

        target = target_dir or run_dir
        if not managed:
            client = DMemClient(fallback_endpoint)
            context = EvalContext(
                config=config, run_dir=target, samples=samples, qa_items=qa_items,
                dmem=client, llm=llm, log=print,
            )
            try:
                return action(context, None)
            finally:
                client.close()
        daemon = ManagedDaemon(config, run_dir, overlay=overlay, name=name, log=print)
        daemon.start()
        # A restart can enqueue an index rebuild; settle it before any recall.
        daemon.wait_for_indexing()
        client = DMemClient(daemon.endpoint)
        context = EvalContext(
            config=config, run_dir=target, samples=samples, qa_items=qa_items,
            dmem=client, llm=llm, log=print,
        )
        try:
            return action(context, daemon)
        finally:
            client.close()
            daemon.stop()

    manifest = {
        "harness_version": __version__,
        "run_id": run_id,
        "started_at": datetime.now(timezone.utc).isoformat(),
        "phases": phases,
        "endpoint": fallback_endpoint if not managed else None,
        "dataset": {
            "path": str(dataset_path),
            "sha256": _sha256(dataset_path),
            "sample_ids": [sample.sample_id for sample in samples],
            "categories": config.categories,
        },
        "dataset_validation": validation,
        "ablation_modes": config.ablation_modes if "ablation" in phases else [],
        "identities": {
            "llm_model": llm_cfg.get("model"),
            "llm_base_url": llm_cfg.get("base_url"),
            "embedding_model": (dmem_cfg.get("embedding") or {}).get("model"),
            "daemon_commit": _daemon_commit(binary) if binary else None,
        },
    }
    write_json(run_dir / "manifest.json", manifest)

    exit_code = 0
    try:
        if "build" in phases:
            def _build(context: EvalContext, daemon: ManagedDaemon | None) -> dict:
                summary = run_build(context)
                if daemon is not None:
                    daemon.wait_for_indexing()
                return summary

            with_daemon("daemon", None, _build)
        if "ablation" in phases:
            _run_ablation(config, run_id, run_dir, with_daemon)
        if "retrieval" in phases:
            def _retrieval(context: EvalContext, _daemon: ManagedDaemon | None) -> dict:
                load_maps(context)
                return run_retrieval(context)

            with_daemon("daemon", None, _retrieval)
        if "qa" in phases:
            with_daemon("daemon", None, lambda context, _daemon: run_qa(context))
    except BaseException as exc:  # noqa: BLE001 - always tear the daemon down
        print(f"evaluation failed: {exc}", file=sys.stderr)
        exit_code = 1

    summary = build_report(run_dir)
    manifest["finished_at"] = datetime.now(timezone.utc).isoformat()
    manifest["exit_code"] = exit_code
    write_json(run_dir / "manifest.json", manifest)

    print("")
    print(f"artifacts: {run_dir}")
    if summary.get("build"):
        print(f"  build_summary.json     {json.dumps(summary['build'], ensure_ascii=False)}")
    if summary.get("retrieval"):
        metrics = (summary["retrieval"].get("metrics") or {}).get("overall", {}).get("@10", {})
        degraded = summary["retrieval"].get("degradation_counts", {})
        print(f"  retrieval_summary.json @10={json.dumps(metrics, ensure_ascii=False)} degraded={degraded}")
    ablation = summary.get("retrieval_ablation") or {}
    if ablation.get("modes"):
        print(f"  retrieval_ablation.json baseline={ablation.get('baseline')} "
              f"modes={[row['name'] for row in ablation['modes']]}")
    if summary.get("qa"):
        print(f"  qa_summary.json        accuracy={summary['qa'].get('accuracy', {}).get('overall')}")
    print("  report.md / summary.json")
    return exit_code


def _run_ablation(config, run_id: str, run_dir: Path, with_daemon) -> dict[str, Any]:
    """Run the retrieval phase once per configured channel-ablation mode."""

    modes = config.ablation_modes
    cutoffs = [int(value) for value in (config.section("retrieval").get("cutoffs") or [1, 3, 5, 10, 30])]
    print(f"phase 2/3 (ablation): {len(modes)} retrieval-channel modes")
    summaries: list[dict[str, Any]] = []
    for mode in modes:
        mode_name = str(mode["name"])
        mode_dir = run_dir / "ablation" / mode_name
        mode_dir.mkdir(parents=True, exist_ok=True)
        overlay = {
            "graph_policy": mode["graph_policy"],
            "embedding": bool(mode["embedding"]),
        }
        print(f"  mode {mode_name}: graph_policy={mode['graph_policy']} "
              f"embedding={'on' if mode['embedding'] else 'off'}")

        def _action(context: EvalContext, _daemon: ManagedDaemon | None) -> dict:
            # The maps live in the run root; per-mode artifacts live in mode_dir.
            load_maps(context, source_dir=run_dir)
            return run_retrieval(context)

        summary = with_daemon(f"ablation/{mode_name}", overlay, _action, target_dir=mode_dir)
        summaries.append(summary)
        overall = ((summary.get("metrics") or {}).get("overall") or {}).get("@10", {})
        print(f"    @10 hit={overall.get('hit')} coverage={overall.get('coverage')} "
              f"mrr={overall.get('mrr')} ndcg={overall.get('ndcg')} "
              f"degraded={summary.get('degradation_counts')}")

    payload = build_ablation(
        run_dir, run_id=run_id, modes=modes, summaries=summaries, cutoffs=cutoffs,
        graph_corpus=graph_corpus_counts(ManagedDaemon(config, run_dir).database_path),
    )
    print(f"  wrote {run_dir / 'retrieval_ablation.json'} and retrieval_ablation.md")
    return payload


if __name__ == "__main__":
    raise SystemExit(main())
