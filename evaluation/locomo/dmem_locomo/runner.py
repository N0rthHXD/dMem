"""Runner: namespace bootstrap, turn ingestion, recall, and metrics.

The runner drives three phases against a live dMem daemon:

1. **Ingest** — create a ``user → project → task`` namespace hierarchy (one
   task per conversation), submit each dialogue turn as a raw event, wait for
   extraction, and explicitly confirm new candidates.
2. **Semantic barrier** — for semantic/hybrid modes, trigger one semantic
   rebuild and wait for completion before querying.
3. **Recall** — query each QA in its conversation's task scope, map recalled
   evidence back to LoCoMo turn/session ids, and compute retrieval metrics.

The runner never imports or starts the daemon; it is a pure HTTP client.
"""

from __future__ import annotations

import hashlib
import json
import logging
import os
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any

import numpy as np

from .api import DMemClient, DMemError
from .checkpoint import Checkpoint
from .dataset import Conversation, QA, dia_id_sort_key, validate_dataset
from .metrics import METRIC_SCHEMA_VERSION, evaluate_retrieval, promote_to_sessions
from .progress import tqdm

logger = logging.getLogger(__name__)

DEFAULT_CUTOFFS = [1, 3, 5, 10, 25, 50]
RETRIEVAL_MODES = {"keyword", "semantic", "graph", "hybrid"}
CONFIRMATION_POLICIES = {"confirm-all-new", "none"}
RETRYABLE_OUTPUT_ERRORS = {
    "invalid_output",
    "malformed_processor_output",
    "invalid_candidate",
    "candidate_out_of_bounds",
    "invalid_result_category",
    "invalid_usage",
    "invalid_evidence_reference",
    "invalid_related_memory_reference",
}
RESULT_VERSION = "locomo-dmem-py-v1"


@dataclass
class RunConfig:
    run_id: str
    dataset_sha: str
    socket_path: str
    confirmation_policy: str = "confirm-all-new"
    job_poll_interval: float = 3.0
    job_timeout: float = 600.0
    invalid_evidence_reference_retries: int = 1
    retrieval_mode: str = "keyword"
    limit: int = 100
    max_chars: int = 100000
    cutoffs: list[int] = field(default_factory=lambda: DEFAULT_CUTOFFS.copy())
    answer_cutoff: int = 25
    caller_id: str = "locomo"
    output_dir: str = "results/locomo"
    request_timeout: float = 120.0
    fail_on_degraded: bool = False
    ingestion_workers: int = 1
    retrieval_workers: int = 1
    corpus_dir: str | None = None
    dataset_subset: list[str] = field(default_factory=list)


def stable_key(*parts: str) -> str:
    raw = "\x00".join(parts)
    h = hashlib.sha256(raw.encode()).hexdigest()[:32]
    return f"loc-{h}"


def to_rfc3339(dt: datetime) -> str:
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.isoformat()


def capture_identities(
    health: dict[str, Any] | None,
    recall_meta: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """Capture the effective daemon and evaluation identities for a manifest."""
    health = health or {}
    recall_meta = recall_meta or {}
    return {
        "metric_schema": METRIC_SCHEMA_VERSION,
        "runner_version": RESULT_VERSION,
        "daemon_build_version": health.get("build_version") or os.environ.get(
            "DMEM_DAEMON_BUILD_SHA256", "unknown"
        ),
        "daemon_build_commit": health.get("build_commit") or os.environ.get(
            "DMEM_DAEMON_BUILD_SHA256", "unknown"
        ),
        "schema_compatibility": health.get("schema_compatibility", "unknown"),
        "processor_identity": health.get("processor_identity", "unknown"),
        "configuration_identity": health.get("configuration_identity", "unknown"),
        "semantic_index_version": health.get("semantic_index_version", 0),
        "graph_index_version": health.get("graph_index_version", 0),
        "graph_index_state": health.get("graph_index_state", "unknown"),
        "fts_index_version": recall_meta.get("fts_index_version", 0),
        "ranking_version": recall_meta.get("ranking_version", "unknown"),
        "context_selection": "none",
    }


def result_report(
    result: dict[str, Any], mapped_dia_ids: list[str]
) -> dict[str, Any]:
    """Normalize the observable telemetry for one recalled result.

    Field names are fixed by ``testdata/retrieval-report-v1.json``. Fields the
    daemon does not yet expose are reported explicitly as null/empty rather than
    being omitted, so a baseline can tell "not measured" from "zero".
    """
    signals = result.get("graph_signals") or []
    graph_scores = [
        float(signal["fact_confidence"]) * float(signal["entity_confidence"])
        for signal in signals
        if signal.get("fact_confidence") and signal.get("entity_confidence")
    ]
    families = set(result.get("matched_channels") or [])
    for signal in signals:
        if signal.get("kind"):
            families.add(signal["kind"])
    return {
        "primary_score": result.get("primary_score"),
        "raw_channel_scores": {
            "keyword": result.get("keyword_relevance"),
            "semantic": result.get("semantic_relevance"),
        },
        "matched_fields": result.get("matched_fields") or [],
        "graph_score": max(graph_scores) if graph_scores else None,
        "signal_family": sorted(families),
        "duplicate_cluster": mapped_dia_ids[0] if mapped_dia_ids else None,
        "selection_state": result.get("selection_reason", ""),
    }


class Runner:
    def __init__(self, client: DMemClient, config: RunConfig, cp: Checkpoint):
        self.client = client
        self.config = config
        self.cp = cp
        if config.retrieval_mode not in RETRIEVAL_MODES:
            raise ValueError(f"invalid retrieval_mode: {config.retrieval_mode}")
        if config.confirmation_policy not in CONFIRMATION_POLICIES:
            raise ValueError(
                f"invalid confirmation_policy: {config.confirmation_policy}"
            )
        if config.invalid_evidence_reference_retries < 0:
            raise ValueError(
                "invalid_evidence_reference_retries must be a non-negative integer"
            )
        for name, workers in (
            ("ingestion_workers", config.ingestion_workers),
            ("retrieval_workers", config.retrieval_workers),
        ):
            if isinstance(workers, bool) or not isinstance(workers, int) or workers < 1:
                raise ValueError(f"{name} must be a positive integer")
        self._progress_lock = threading.Lock()
        for k in config.cutoffs:
            if not (1 <= k <= config.limit):
                raise ValueError(f"cutoff {k} must be in [1, limit={config.limit}]")

    # ------------------------------------------------------------------
    # public entry point
    # ------------------------------------------------------------------

    def run(self, conversations: list[Conversation]) -> dict[str, Any]:
        health: dict[str, Any] = {}
        try:
            health = self.client.health()
        except Exception as exc:  # pragma: no cover - daemon transport failure
            logger.warning("health unavailable while capturing identities: %s", exc)
        manifest: dict[str, Any] = {
            "format_version": RESULT_VERSION,
            "metric_schema": METRIC_SCHEMA_VERSION,
            "run_id": self.config.run_id,
            "dataset_sha": self.config.dataset_sha,
            "dataset_subset": sorted(self.config.dataset_subset),
            "confirmation_policy": self.config.confirmation_policy,
            "invalid_evidence_reference_retries": (
                self.config.invalid_evidence_reference_retries
            ),
            "retrieval_mode": self.config.retrieval_mode,
            "limit": self.config.limit,
            "max_chars": self.config.max_chars,
            "cutoffs": self.config.cutoffs,
            "answer_cutoff": self.config.answer_cutoff,
            "ingestion_workers": self.config.ingestion_workers,
            "retrieval_workers": self.config.retrieval_workers,
            "socket_path": self.config.socket_path,
            "caller_id": self.config.caller_id,
            "created_at": datetime.now(timezone.utc).isoformat(),
        }
        if self.config.corpus_dir:
            corpus_manifest = Checkpoint(self.config.corpus_dir).read_manifest()
            manifest["corpus_id"] = corpus_manifest.get("corpus_id", corpus_manifest.get("run_id")) if corpus_manifest else None
            manifest["corpus_dir"] = self.config.corpus_dir
            manifest["source_ingestion_run_id"] = corpus_manifest.get("run_id") if corpus_manifest else None
        manifest["identities"] = capture_identities(health)
        self.cp.write_manifest(manifest)
        self.cp.write_validation(validate_dataset(conversations))

        if self.config.corpus_dir:
            task_map, ingestion_records = self._load_corpus(conversations)
        else:
            user_id, project_id = self._bootstrap_namespace()
            self._task_cache = {}
            for s in self.client.list_spaces(
                parent_space_id=project_id, space_type="task"
            ):
                ext = s.get("external_id", "")
                if ext.startswith("c:"):
                    self._task_cache[ext[2:]] = s["space_id"]
            task_map = self._ingest_phase(conversations, project_id)
            ingestion_records = self.cp.load_ingestion_records()

        if self.config.retrieval_mode in ("semantic", "hybrid"):
            if self.config.corpus_dir:
                self.client.wait_for_semantic_ready(
                    poll_interval=self.config.job_poll_interval,
                    timeout=self.config.job_timeout,
                )
            else:
                self._semantic_barrier()

        if self.config.retrieval_mode in ("graph", "hybrid"):
            if self.config.corpus_dir:
                self.client.wait_for_graph_ready(
                    poll_interval=self.config.job_poll_interval,
                    timeout=self.config.job_timeout,
                )
            else:
                self._graph_barrier()

        if self.config.corpus_dir:
            self._recall_phase(conversations, task_map, ingestion_records)
        else:
            self._recall_phase(conversations, task_map)

        self._finalize_manifest_identities()
        summary = self._summarize(conversations, ingestion_records)
        self.cp.write_summary(summary)
        return summary

    def _finalize_manifest_identities(self) -> None:
        """Persist observed ranking/index identities and degradation counts."""
        manifest = self.cp.read_manifest() or {}
        records = self.cp.load_retrieval_records()
        identities = dict(manifest.get("identities") or {})

        def distinct(key: str) -> list[Any]:
            return sorted({
                rec[key] for rec in records.values()
                if rec.get(key) not in (None, "", 0)
            })

        ranking_versions = distinct("ranking_version")
        if len(ranking_versions) == 1:
            identities["ranking_version"] = ranking_versions[0]
        elif ranking_versions:
            identities["ranking_version"] = ranking_versions
        fts_versions = distinct("fts_index_version")
        if len(fts_versions) == 1:
            identities["fts_index_version"] = fts_versions[0]
        elif fts_versions:
            identities["fts_index_version"] = fts_versions
        semantic_versions = distinct("semantic_index_version")
        if len(semantic_versions) == 1:
            identities["semantic_index_version"] = semantic_versions[0]
        elif semantic_versions:
            identities["semantic_index_version"] = semantic_versions
        graph_versions = distinct("graph_index_version")
        if len(graph_versions) == 1:
            identities["graph_index_version"] = graph_versions[0]
        elif graph_versions:
            identities["graph_index_version"] = graph_versions

        degradation_counts: dict[str, int] = {}
        degraded_cases = 0
        for rec in records.values():
            if rec.get("degraded"):
                degraded_cases += 1
            for reason in rec.get("degradation_reasons") or []:
                degradation_counts[reason] = degradation_counts.get(reason, 0) + 1

        manifest["identities"] = identities
        manifest["degradation_counts"] = {
            "degraded_cases": degraded_cases,
            "by_reason": degradation_counts,
        }
        self.cp.write_manifest(manifest)

    def ingest_corpus(self, conversations: list[Conversation]) -> dict[str, Any]:
        user_id, project_id = self._bootstrap_namespace()
        self._task_cache = {}
        for s in self.client.list_spaces(parent_space_id=project_id, space_type="task"):
            ext = s.get("external_id", "")
            if ext.startswith("c:"):
                self._task_cache[ext[2:]] = s["space_id"]
        manifest = {
            "format_version": RESULT_VERSION,
            "run_type": "corpus",
            "corpus_id": self.config.run_id,
            "run_id": self.config.run_id,
            "dataset_sha": self.config.dataset_sha,
            "confirmation_policy": self.config.confirmation_policy,
            "invalid_evidence_reference_retries": self.config.invalid_evidence_reference_retries,
            "ingestion_workers": self.config.ingestion_workers,
            "created_at": datetime.now(timezone.utc).isoformat(),
        }
        self.cp.write_manifest(manifest)
        self.cp.write_validation(validate_dataset(conversations))
        self._ingest_phase(conversations, project_id)
        summary = {"complete": not self._unresolved_failures(
            {c.sample_id for c in conversations}, self.cp.load_ingested_sample_ids(), {}
        )["ingestion"], "ingestion": self._summarize_ingestion(conversations, None, {"ingestion": set(), "retrieval": set()})}
        self.cp.write_summary(summary)
        return summary

    def build_embeddings(self) -> dict[str, Any]:
        previous = self.config.fail_on_degraded
        self.config.fail_on_degraded = True
        try:
            self._semantic_barrier()
            self._graph_barrier()
        finally:
            self.config.fail_on_degraded = previous
        result = self.cp.read_manifest() or {}
        result["embedding_state"] = "ready"
        result["embedding_built_at"] = datetime.now(timezone.utc).isoformat()
        result["graph_state"] = "ready"
        result["graph_built_at"] = datetime.now(timezone.utc).isoformat()
        self.cp.write_manifest(result)
        return result

    def _load_corpus(self, conversations: list[Conversation]) -> tuple[dict[str, str], dict[str, list[dict[str, Any]]]]:
        source = Checkpoint(self.config.corpus_dir or "")
        manifest = source.read_manifest()
        if not manifest or manifest.get("run_type") != "corpus":
            raise ValueError("corpus manifest is missing or is not a corpus run")
        if manifest.get("dataset_sha") != self.config.dataset_sha:
            raise ValueError("corpus dataset SHA does not match evaluation dataset")
        if self.config.retrieval_mode in ("semantic", "hybrid") and manifest.get("embedding_state") != "ready":
            raise ValueError("semantic index is not ready; run the embeddings stage first")
        records = source.load_ingestion_records()
        completed = source.load_ingested_sample_ids()
        expected = {c.sample_id for c in conversations}
        if not expected.issubset(completed):
            missing = sorted(expected - completed)
            raise ValueError(f"corpus ingestion is incomplete; missing: {', '.join(missing)}")
        task_map = {}
        for sid in expected:
            task_records = records.get(sid, [])
            complete = next((r for r in task_records if r.get("record_type") == "conversation_complete"), None)
            if not complete or not complete.get("task_space_id"):
                raise ValueError(f"corpus has no task space for {sid}")
            task_map[sid] = complete["task_space_id"]
        return task_map, records

    # ------------------------------------------------------------------
    # namespace
    # ------------------------------------------------------------------

    def _bootstrap_namespace(self) -> tuple[str, str]:
        spaces = self.client.list_spaces(space_type="user")
        if spaces:
            user_id = spaces[0]["space_id"]
            logger.info("reusing existing user root: %s", user_id)
        else:
            result = self.client.create_space(
                space_type="user",
                external_id="locomo",
                display_name="LoCoMo",
                idempotency_key=stable_key(
                    "root", self.config.dataset_sha, self.config.run_id
                ),
            )
            user_id = result["space_id"]
            logger.info("created user root: %s", user_id)

        project_ext = f"locomo:{self.config.dataset_sha[:16]}:{self.config.run_id}"
        children = self.client.list_spaces(
            parent_space_id=user_id, space_type="project"
        )
        project_id = next(
            (c["space_id"] for c in children if c.get("external_id") == project_ext),
            None,
        )
        if project_id is None:
            result = self.client.create_space(
                space_type="project",
                external_id=project_ext,
                display_name=f"LoCoMo {self.config.run_id}",
                parent_space_id=user_id,
                idempotency_key=stable_key(
                    "project", self.config.dataset_sha, self.config.run_id
                ),
            )
            project_id = result["space_id"]
            logger.info("created project: %s", project_id)
        else:
            logger.info("reusing project: %s", project_id)

        return user_id, project_id

    def _get_or_create_task(self, conv: Conversation, project_id: str) -> str:
        cached = getattr(self, "_task_cache", {}).get(conv.sample_id)
        if cached:
            return cached
        task_ext = f"c:{conv.sample_id}"
        result = self.client.create_space(
            space_type="task",
            external_id=task_ext,
            display_name=f"Conversation {conv.sample_id}",
            parent_space_id=project_id,
            idempotency_key=stable_key(
                "task", self.config.dataset_sha, self.config.run_id, conv.sample_id
            ),
        )
        space_id = result["space_id"]
        if not hasattr(self, "_task_cache"):
            self._task_cache = {}
        self._task_cache[conv.sample_id] = space_id
        return space_id

    # ------------------------------------------------------------------
    # ingest phase
    # ------------------------------------------------------------------

    def _ingest_phase(
        self, conversations: list[Conversation], project_id: str
    ) -> dict[str, str]:
        ingested = self.cp.load_ingested_sample_ids()
        ingestion_records = self.cp.load_ingestion_records()
        task_map: dict[str, str] = {}

        for sid, recs in ingestion_records.items():
            if sid in ingested and recs:
                task_map[sid] = recs[0]["task_space_id"]

        todo = [c for c in conversations if c.sample_id not in ingested]
        total_events = sum(self._conversation_turn_count(c) for c in conversations)
        completed_events = sum(
            rec.get("record_type", "event") == "event"
            for sid, records in ingestion_records.items()
            if sid in {c.sample_id for c in conversations}
            for rec in records
        )
        with tqdm(
            total=total_events,
            initial=completed_events,
            desc="ingest",
            unit="turn",
            file=sys.stderr,
        ) as bar:
            self._ingest_bar = bar
            try:
                def ingest(conv: Conversation) -> tuple[str, str, Exception | None]:
                    try:
                        task_id = self._get_or_create_task(conv, project_id)
                        self._ingest_conversation(conv, task_id)
                        return conv.sample_id, task_id, None
                    except Exception as exc:
                        return conv.sample_id, "", exc

                with ThreadPoolExecutor(
                    max_workers=self.config.ingestion_workers,
                    thread_name_prefix="loc-ingest",
                ) as executor:
                    futures = [executor.submit(ingest, conv) for conv in todo]
                    for future in as_completed(futures):
                        sid, task_id, exc = future.result()
                        if exc is None:
                            task_map[sid] = task_id
                        else:
                            self._record_failure(sid, "ingestion", exc)
            finally:
                self._ingest_bar = None

        return task_map

    @staticmethod
    def _conversation_turn_count(conv: Conversation) -> int:
        return len(conv.all_turns())

    def _ingest_conversation(self, conv: Conversation, task_space_id: str) -> None:
        existing = {
            rec.get("dia_id"): rec
            for rec in self.cp.load_ingestion_records().get(conv.sample_id, [])
            if rec.get("record_type", "event") == "event"
        }
        for session in conv.sorted_sessions():
            for turn in session.turns:
                if turn.dia_id in existing:
                    continue
                self._ingest_turn(conv, session.session_num, turn, task_space_id)
        self.cp.append_ingestion({
            "record_type": "conversation_complete",
            "sample_id": conv.sample_id,
            "task_space_id": task_space_id,
        })

    def _turn_content(self, turn: Any) -> str:
        content = f"{turn.speaker}: {turn.text}"
        if turn.blip_caption:
            content += f" [shared image: {turn.blip_caption}]"
        return content

    def _ingest_turn(
        self,
        conv: Conversation,
        session_num: int,
        turn: Any,
        task_space_id: str,
    ) -> None:
        bar = getattr(self, "_ingest_bar", None)
        if bar is not None:
            with self._progress_lock:
                bar.set_postfix_str(
                    f"c={conv.sample_id} session={session_num} turn={turn.dia_id}"
                )

        source_id = stable_key(
            "source", self.config.dataset_sha, self.config.run_id,
            conv.sample_id, turn.dia_id,
        )
        idem_key = stable_key(
            self.config.dataset_sha, self.config.run_id,
            conv.sample_id, turn.dia_id,
        )
        metadata = {
            "benchmark": "locomo",
            "dataset_sha256": self.config.dataset_sha,
            "run_id": self.config.run_id,
            "sample_id": conv.sample_id,
            "session_id": session_num,
            "dia_id": turn.dia_id,
            "speaker": turn.speaker,
            "has_image": turn.has_image,
        }
        session = next(s for s in conv.sessions if s.session_num == session_num)
        record = self._ingest_event(
            conv=conv,
            task_space_id=task_space_id,
            turn=turn,
            content=self._turn_content(turn),
            occurred_at=to_rfc3339(session.date),
            source_id=source_id,
            metadata=metadata,
            idempotency_key=idem_key,
        )
        self.cp.append_ingestion({
            "record_type": "event",
            "sample_id": conv.sample_id,
            "session_id": session_num,
            "dia_id": turn.dia_id,
            "task_space_id": task_space_id,
            **record,
        })
        if bar is not None:
            with self._progress_lock:
                bar.set_postfix_str(
                    f"c={conv.sample_id} candidates="
                    f"{len(record.get('candidate_ids', []))} confirmed="
                    f"{len(record.get('confirmed', []))}"
                )
                bar.update(1)

    def _ingest_event(
        self,
        *,
        conv: Conversation,
        task_space_id: str,
        turn: Any,
        content: str,
        occurred_at: str,
        source_id: str,
        metadata: dict[str, Any],
        idempotency_key: str,
    ) -> dict[str, Any]:
        result = self.client.ingest_event(
            space_id=task_space_id,
            content=content,
            source_type="conversation",
            source_name="locomo",
            source_event_id=source_id,
            occurred_at=occurred_at,
            metadata=metadata,
            idempotency_key=idempotency_key,
        )
        event_id = result.get("event_id", "")
        if not event_id:
            raise RuntimeError("POST /v1/events returned no event_id")

        jobs: list[dict[str, Any]] = []
        retry_count = 0
        first_error_codes: list[str] = []
        for job_id in result.get("job_ids", []):
            job, retries, first_error_code = self._wait_for_extraction_job(
                event_id=event_id, job_id=job_id, sample_id=conv.sample_id,
                dia_id=turn.dia_id,
            )
            jobs.append(job)
            retry_count += retries
            if first_error_code:
                first_error_codes.append(first_error_code)
        if not jobs:
            raise RuntimeError(
                f"evaluation requires an extraction job for event {event_id}"
            )
        failed = [job for job in jobs if job.get("status") != "succeeded"]
        if failed:
            job = failed[0]
            reason = job.get("blocked_reason_code") or job.get("last_error_code")
            if job.get("status") == "blocked_policy" and not reason:
                reason = (
                    "processing policy must allow task spaces, conversation "
                    "source type, text/plain content, and normal sensitivity"
                )
            raise RuntimeError(
                f"event {event_id} processing job {job.get('job_id')} ended as "
                f"{job.get('status')}: {reason}"
            )
        result_codes = [job.get("result_code") for job in jobs]
        if "no_processor" in result_codes:
            raise RuntimeError(
                "evaluation requires a configured extraction processor"
            )

        raw_event = self.client.get_event(event_id)
        if raw_event.get("processing_status") != "processed":
            raise RuntimeError(
                f"event {event_id} processing status is "
                f"{raw_event.get('processing_status')!r}, expected 'processed'"
            )

        candidates = [
            candidate
            for candidate in self.client.list_candidates(space_id=task_space_id)
            if candidate.get("event_id") == event_id
        ]
        confirmed: list[dict[str, str]] = []
        if self.config.confirmation_policy == "confirm-all-new":
            for candidate in candidates:
                candidate_id = candidate["candidate_id"]
                if candidate.get("review_state") == "confirmed":
                    memory_id = candidate.get("confirmed_memory_id", "")
                elif candidate.get("review_state") == "pending":
                    memory = self.client.confirm_candidate(
                        candidate_id=candidate_id,
                        reason_code="locomo_confirm_all_new",
                        note="Explicit LoCoMo benchmark policy",
                        idempotency_key=stable_key(
                            "confirm", self.config.dataset_sha,
                            self.config.run_id, conv.sample_id,
                            turn.dia_id, candidate_id,
                        ),
                    )
                    memory_id = memory.get("memory_id", "")
                else:
                    raise RuntimeError(
                        f"candidate {candidate_id} was rejected outside benchmark policy"
                    )
                confirmed.append({"candidate_id": candidate_id, "memory_id": memory_id})

        return {
            "event_id": event_id,
            "job_ids": result.get("job_ids", []),
            "processing_result_codes": result_codes,
            "processing_retry_count": retry_count,
            "processing_first_error_codes": first_error_codes,
            "processing_attempt_count": max(
                (job.get("attempt_count", 0) for job in jobs), default=0
            ),
            "candidate_ids": [c["candidate_id"] for c in candidates],
            "confirmed": confirmed,
            "memory_ids": [c["memory_id"] for c in confirmed if c["memory_id"]],
        }

    def _wait_for_extraction_job(
        self, *, event_id: str, job_id: str, sample_id: str, dia_id: str
    ) -> tuple[dict[str, Any], int, str]:
        retries = 0
        first_error_code = ""
        while True:
            job = self.client.wait_for_job(
                job_id,
                poll_interval=self.config.job_poll_interval,
                timeout=self.config.job_timeout,
                on_status=lambda current: self._show_job_progress(
                    sample_id, dia_id, current
                ),
            )
            error_code = job.get("last_error_code", "")
            if (
                job.get("status") != "failed"
                or error_code not in RETRYABLE_OUTPUT_ERRORS
                or retries >= self.config.invalid_evidence_reference_retries
            ):
                return job, retries, first_error_code

            if not first_error_code:
                first_error_code = error_code
            retries += 1
            logger.warning(
                "event %s extraction job %s failed with %s; retrying %d/%d",
                event_id, job_id, error_code, retries,
                self.config.invalid_evidence_reference_retries,
            )
            self.client.retry_job(
                job_id,
                idempotency_key=stable_key(
                    "retry-invalid-output", self.config.dataset_sha,
                    self.config.run_id, event_id, job_id, str(retries),
                ),
            )

    def _show_job_progress(
        self, sample_id: str, dia_id: str, job: dict[str, Any]
    ) -> None:
        bar = getattr(self, "_ingest_bar", None)
        if bar is not None:
            with self._progress_lock:
                bar.set_postfix_str(
                    f"c={sample_id} turn={dia_id} job={job.get('status', 'unknown')} "
                    f"attempt={job.get('attempt_count', 0)}"
                )

    def _record_failure(self, sample_id: str, phase: str, exc: Exception) -> None:
        logger.error("%s failed for %s: %s", phase, sample_id, exc)
        self.cp.append_failure({
            "sample_id": sample_id,
            "phase": phase,
            "error_type": type(exc).__name__,
            "error": str(exc),
        })

    # ------------------------------------------------------------------
    # semantic barrier
    # ------------------------------------------------------------------

    def _semantic_barrier(self) -> None:
        idem_key = stable_key("rebuild", self.config.dataset_sha, self.config.run_id)
        started = time.monotonic()
        logger.info("semantic barrier started")
        try:
            self.client.trigger_and_wait_semantic(
                idempotency_key=idem_key,
                on_job_status=lambda job: logger.info(
                    "semantic rebuild job: status=%s attempt=%s elapsed=%.1fs",
                    job.get("status"), job.get("attempt_count"),
                    time.monotonic() - started,
                ),
                on_status=lambda health: logger.info(
                    "semantic barrier: state=%s queue=%s elapsed=%.1fs",
                    health.get("semantic_state"), health.get("job_queue_depth"),
                    time.monotonic() - started,
                ),
            )
            logger.info("semantic barrier complete in %.1fs", time.monotonic() - started)
        except Exception as exc:
            logger.warning("semantic barrier failed: %s", exc)
            if self.config.fail_on_degraded:
                raise

    def _graph_barrier(self) -> None:
        idem_key = stable_key(
            "rebuild-graph", self.config.dataset_sha, self.config.run_id
        )
        started = time.monotonic()
        logger.info("graph barrier started")
        try:
            self.client.trigger_and_wait_graph(
                idempotency_key=idem_key,
                poll_interval=self.config.job_poll_interval,
                timeout=self.config.job_timeout,
                on_job_status=lambda job: logger.info(
                    "graph rebuild job: status=%s attempt=%s elapsed=%.1fs",
                    job.get("status"), job.get("attempt_count"),
                    time.monotonic() - started,
                ),
                on_status=lambda health: logger.info(
                    "graph barrier: state=%s queue=%s elapsed=%.1fs",
                    health.get("graph_index_state"), health.get("job_queue_depth"),
                    time.monotonic() - started,
                ),
            )
            logger.info("graph barrier complete in %.1fs", time.monotonic() - started)
        except Exception as exc:
            logger.warning("graph barrier failed: %s", exc)
            if self.config.fail_on_degraded:
                raise

    # ------------------------------------------------------------------
    # recall phase
    # ------------------------------------------------------------------

    def _recall_phase(
        self, conversations: list[Conversation], task_map: dict[str, str],
        ingestion_records: dict[str, list[dict[str, Any]]] | None = None,
    ) -> None:
        done = self.cp.load_retrieval_qids()
        ingestion_records = ingestion_records or self.cp.load_ingestion_records()

        qa_by_sample: dict[str, list[QA]] = {}
        for conv in conversations:
            qa_by_sample[conv.sample_id] = conv.qa

        all_qa = [
            (conv, qa)
            for conv in conversations
            if conv.sample_id in task_map
            for qa in conv.qa
            if qa.qa_id not in done
        ]
        total_qa = sum(len(conv.qa) for conv in conversations)
        completed = sum(qa.qa_id in done for conv in conversations for qa in conv.qa)

        with tqdm(
            total=total_qa, initial=completed, desc="recall", unit="q",
            file=sys.stderr,
        ) as bar:
            def recall(item: tuple[Conversation, QA]) -> tuple[str, Exception | None]:
                conv, qa = item
                try:
                    self._recall_qa(
                        conv, qa, task_map[conv.sample_id], ingestion_records
                    )
                    return qa.qa_id, None
                except Exception as exc:
                    return qa.qa_id, exc

            with ThreadPoolExecutor(
                max_workers=self.config.retrieval_workers,
                thread_name_prefix="loc-recall",
            ) as executor:
                futures = [executor.submit(recall, item) for item in all_qa]
                for future in as_completed(futures):
                    qid, exc = future.result()
                    if exc is not None:
                        self._record_failure(qid, "retrieval", exc)
                    bar.set_postfix_str(qid)
                    bar.update(1)

    def _recall_qa(
        self,
        conv: Conversation,
        qa: QA,
        task_space_id: str,
        ingestion_records: dict[str, list[dict[str, Any]]],
    ) -> None:
        event_labels: dict[str, dict[str, Any]] = {}
        indexed_dia_ids: dict[str, set[str]] = {}
        for rec in ingestion_records.get(conv.sample_id, []):
            dia_id = rec.get("dia_id")
            memory_ids = [m for m in (rec.get("memory_ids") or []) if m]
            if rec.get("memory_id"):
                memory_ids.append(rec["memory_id"])
            if rec.get("event_id"):
                event_labels[rec["event_id"]] = rec
            if dia_id and memory_ids:
                indexed_dia_ids.setdefault(dia_id, set()).update(memory_ids)

        reference_time = conv.last_session_date()
        recall_started = time.perf_counter()
        response = self.client.recall(
            query=qa.question,
            space_ids=[task_space_id],
            include_ancestors=False,
            reference_time=to_rfc3339(reference_time) if reference_time else None,
            mode=self.config.retrieval_mode,
            limit=self.config.limit,
            max_chars=self.config.max_chars,
            include_evidence=True,
            exclude_stale_aggregates=True,
        )
        recall_latency_ms = (time.perf_counter() - recall_started) * 1000
        response_bytes = len(
            json.dumps(response, ensure_ascii=False, separators=(",", ":")).encode()
        )

        if response.get("degraded") and self.config.fail_on_degraded:
            reasons = response.get("degradation_reasons") or ["unknown"]
            raise RuntimeError(
                f"recall degraded for {qa.qa_id}: {', '.join(reasons)}"
            )

        gold_turn = set(qa.evidence)
        gold_session = set(promote_to_sessions(qa.evidence))
        gold_memory_ids = {
            memory_id
            for dia_id, memory_ids in indexed_dia_ids.items()
            if dia_id in gold_turn
            for memory_id in memory_ids
        }

        ranked_items: list[dict[str, Any]] = []
        ranked_memory_ids: list[str] = []
        ranked_dia_ids: list[str] = []
        seen_dias: set[str] = set()
        duplicate_count = 0
        unmapped_count = 0

        for res in response.get("results", []):
            memory = res.get("memory", {})
            revision = memory.get("current_revision", {})
            memory_id = memory.get("memory_id", "")

            mapped: list[str] = []
            for ev in revision.get("evidence", []):
                rec = event_labels.get(ev.get("event_id", ""))
                dia_id = rec.get("dia_id") if rec else None
                if dia_id and dia_id not in mapped:
                    mapped.append(dia_id)
            mapped.sort(key=dia_id_sort_key)

            if not mapped:
                unmapped_count += 1
            for dia_id in mapped:
                if dia_id in seen_dias:
                    duplicate_count += 1
                else:
                    seen_dias.add(dia_id)
                    ranked_dia_ids.append(dia_id)

            ranked_memory_ids.append(memory_id)
            ranked_items.append({
                "content": revision.get("content", ""),
                "memory_id": memory_id,
                "revision_id": revision.get("revision_id", ""),
                "dia_id": mapped[0] if mapped else None,
                "dia_ids": mapped,
                "matched_channels": res.get("matched_channels", []),
                "report": result_report(res, mapped),
            })

        ranked_sessions = promote_to_sessions(ranked_dia_ids)
        num_results = len(response.get("results", []))
        total_mapped = sum(len(item["dia_ids"]) for item in ranked_items)

        memory_metrics: dict[str, dict[str, float]] = {}
        turn_metrics: dict[str, dict[str, float]] = {}
        session_metrics: dict[str, dict[str, float]] = {}
        for k in self.config.cutoffs:
            memory_metrics[f"@{k}"] = evaluate_retrieval(
                ranked_memory_ids, gold_memory_ids, k
            )
            turn_metrics[f"@{k}"] = evaluate_retrieval(ranked_dia_ids, gold_turn, k)
            session_metrics[f"@{k}"] = evaluate_retrieval(
                ranked_sessions, gold_session, k
            )

        gold_indexed = gold_turn & set(indexed_dia_ids)
        extraction = {
            "gold_total": len(gold_turn),
            "gold_indexed": len(gold_indexed),
            "coverage": (
                len(gold_indexed) / len(gold_turn) if gold_turn else 1.0
            ),
            "missed": bool(gold_turn - gold_indexed),
        }

        record: dict[str, Any] = {
            "metric_schema": METRIC_SCHEMA_VERSION,
            "qa_id": qa.qa_id,
            "sample_id": conv.sample_id,
            "category": qa.category,
            "question": qa.question,
            "ranked_items": ranked_items[: self.config.answer_cutoff],
            "ranked_dia_ids": ranked_dia_ids,
            "ranked_sessions": ranked_sessions,
            "ranked_memory_ids": ranked_memory_ids,
            "gold_evidence": qa.evidence,
            "gold_sessions": sorted(gold_session, key=lambda s: int(s[1:])),
            "gold_memory_ids": sorted(gold_memory_ids),
            "requested_channels": response.get("requested_channels"),
            "executed_channels": response.get("executed_channels"),
            "degraded": response.get("degraded", False),
            "degradation_reasons": response.get("degradation_reasons", []),
            "ranking_version": response.get("ranking_version"),
            "fts_index_version": response.get("fts_index_version"),
            "semantic_index_version": response.get("semantic_index_version"),
            "graph_index_version": response.get("graph_index_version"),
            "graph_index_state": response.get("graph_index_state"),
            "recall_latency_ms": recall_latency_ms,
            "response_bytes": response_bytes,
            "num_results": num_results,
            "unmapped_count": unmapped_count,
            "unmapped_rate": unmapped_count / num_results if num_results else 0.0,
            "duplicate_count": duplicate_count,
            "duplicate_rate": (
                duplicate_count / total_mapped if total_mapped else 0.0
            ),
            "memory_metrics": memory_metrics,
            "turn_metrics": turn_metrics,
            "session_metrics": session_metrics,
            "extraction": extraction,
            "has_evidence": qa.has_evidence,
            "is_adversarial": qa.is_adversarial,
        }
        self.cp.append_retrieval(record)

    # ------------------------------------------------------------------
    # summarize
    # ------------------------------------------------------------------

    def _summarize(
        self,
        conversations: list[Conversation],
        ingestion_records: dict[str, list[dict[str, Any]]] | None = None,
    ) -> dict[str, Any]:
        retrieval_records = self.cp.load_retrieval_records()
        ingestion_records = ingestion_records or self.cp.load_ingestion_records()
        ingested = {
            sid for sid, records in ingestion_records.items()
            if any(rec.get("record_type") == "conversation_complete" for rec in records)
        }
        sample_ids = {conv.sample_id for conv in conversations}
        failures = self._unresolved_failures(sample_ids, ingested, retrieval_records)
        failed_by_phase = {
            phase: len(qids) for phase, qids in failures.items() if qids
        }
        failed_qids = set().union(*failures.values()) if failures else set()

        # Layered retrieval metrics, excluding adversarial, no-evidence, and
        # partially unresolved QA whose gold relevance is not well defined.
        memory_cases: list[dict[str, float]] = []
        turn_cases: list[dict[str, float]] = []
        session_cases: list[dict[str, float]] = []
        per_category: dict[int, list[dict[str, float]]] = {}
        duplicate_rates: list[float] = []
        unmapped_rates: list[float] = []
        extraction_gold_total = 0
        extraction_gold_indexed = 0
        extraction_missing_cases = 0
        retrieval_failure_cases = 0
        degraded_cases: list[str] = []
        excluded_unscorable = 0
        excluded_errors = 0
        max_cutoff = max(self.config.cutoffs)

        for conv in conversations:
            for qa in conv.qa:
                rec = retrieval_records.get(qa.qa_id)
                if rec is None:
                    excluded_errors += 1
                    continue
                if rec.get("degraded"):
                    degraded_cases.append(qa.qa_id)
                if not qa.is_scorable:
                    excluded_unscorable += 1
                    continue
                memory_cases.append(self._flat_metrics(rec["memory_metrics"]))
                turn_cases.append(self._flat_metrics(rec["turn_metrics"]))
                session_cases.append(self._flat_metrics(rec["session_metrics"]))
                per_category.setdefault(qa.category, []).append(
                    self._flat_metrics(rec["turn_metrics"])
                )
                duplicate_rates.append(float(rec.get("duplicate_rate", 0.0)))
                unmapped_rates.append(float(rec.get("unmapped_rate", 0.0)))
                extraction = rec.get("extraction", {})
                extraction_gold_total += int(extraction.get("gold_total", 0))
                extraction_gold_indexed += int(extraction.get("gold_indexed", 0))
                if extraction.get("missed"):
                    extraction_missing_cases += 1
                if (
                    int(extraction.get("gold_indexed", 0)) > 0
                    and rec["turn_metrics"].get(f"@{max_cutoff}", {}).get("recall_any", 0.0)
                    == 0.0
                ):
                    retrieval_failure_cases += 1

        memory_summary = self._aggregate_cutoffs(memory_cases)
        turn_summary = self._aggregate_cutoffs(turn_cases)
        session_summary = self._aggregate_cutoffs(session_cases)
        per_category_summary = {
            str(k): self._aggregate_cutoffs(v) for k, v in sorted(per_category.items())
        }

        completed_retrieval = sum(
            1 for conv in conversations for qa in conv.qa if qa.qa_id in retrieval_records
        )
        total_qa = sum(len(conv.qa) for conv in conversations)
        extraction_coverage = (
            extraction_gold_indexed / extraction_gold_total
            if extraction_gold_total
            else 1.0
        )

        return {
            "complete": not failed_qids and completed_retrieval == total_qa,
            "coverage": completed_retrieval / total_qa if total_qa else 1.0,
            "metric_schema": METRIC_SCHEMA_VERSION,
            "failures": {
                "total_cases": len(failed_qids),
                "total_attempts": sum(
                    rec.get("sample_id") in sample_ids or rec.get("qa_id") in retrieval_records
                    for rec in self.cp.load_failure_records()
                ),
                "by_phase": failed_by_phase,
            },
            "ingestion": self._summarize_ingestion(
                conversations, ingestion_records, failures
            ),
            "retrieval": {
                "memory_level": memory_summary,
                "turn_level": turn_summary,
                "session_level": session_summary,
                "per_category": per_category_summary,
                "duplicate_rate": (
                    float(np.mean(duplicate_rates)) if duplicate_rates else 0.0
                ),
                "unmapped_rate": (
                    float(np.mean(unmapped_rates)) if unmapped_rates else 0.0
                ),
                "extraction": {
                    "coverage": extraction_coverage,
                    "gold_total": extraction_gold_total,
                    "gold_indexed": extraction_gold_indexed,
                    "missing_gold_cases": extraction_missing_cases,
                    "retrieval_failure_cases": retrieval_failure_cases,
                },
                "excluded_unscorable": excluded_unscorable,
                "excluded_errors": excluded_errors,
                "degraded_cases": len(degraded_cases),
                "total_qa": total_qa,
                "completed_qa": completed_retrieval,
                "evaluated_qa": len(turn_cases),
                "failed_qa": len(failures["retrieval"]),
                "skipped_ingestion_failures": len(failures["ingestion"]),
            },
        }

    @staticmethod
    def _flat_metrics(metrics: dict[str, dict[str, float]]) -> dict[str, float]:
        out: dict[str, float] = {}
        for cutoff, values in metrics.items():
            for name, val in values.items():
                out[f"{name}{cutoff}"] = val
        return out

    def _aggregate_cutoffs(self, cases: list[dict[str, float]]) -> dict[str, dict[str, float]]:
        if not cases:
            return {}
        metric_names = ["recall_any", "recall_all", "coverage", "ndcg", "mrr_at_k"]
        result: dict[str, dict[str, float]] = {}
        for k in self.config.cutoffs:
            result[f"@{k}"] = {}
            for name in metric_names:
                values = [case[f"{name}@{k}"] for case in cases if f"{name}@{k}" in case]
                result[f"@{k}"][name] = float(np.mean(values)) if values else 0.0
        return result

    def _unresolved_failures(
        self,
        sample_ids: set[str],
        ingested: set[str],
        retrieval_records: dict[str, dict[str, Any]],
    ) -> dict[str, set[str]]:
        failures = {"ingestion": set(), "retrieval": set()}
        for rec in self.cp.load_failure_records():
            key = rec.get("sample_id") or rec.get("qa_id")
            phase = rec.get("phase")
            if phase not in failures:
                continue
            if phase == "ingestion" and key in sample_ids and key not in ingested:
                failures[phase].add(key)
            elif phase == "retrieval" and key not in retrieval_records:
                failures[phase].add(key)
        return failures

    def _summarize_ingestion(
        self,
        conversations: list[Conversation],
        ingestion_records: dict[str, list[dict[str, Any]]] | None,
        failures: dict[str, set[str]],
    ) -> dict[str, Any]:
        if ingestion_records is None:
            ingestion_records = self.cp.load_ingestion_records()
        ingested = {
            sid
            for sid, records in ingestion_records.items()
            if any(rec.get("record_type") == "conversation_complete" for rec in records)
        }
        sample_ids = {conv.sample_id for conv in conversations}
        events = [
            rec
            for records in ingestion_records.values()
            for rec in records
            if rec.get("record_type", "event") == "event"
        ]
        return {
            "confirmation_policy": self.config.confirmation_policy,
            "completed_conversations": len(ingested & sample_ids),
            "failed_conversations": len(failures["ingestion"]),
            "events": len(events),
            "candidates": sum(len(rec.get("candidate_ids", [])) for rec in events),
            "confirmed": sum(len(rec.get("confirmed", [])) for rec in events),
            "no_memory": sum(
                "no_memory" in rec.get("processing_result_codes", []) for rec in events
            ),
        }

    # ------------------------------------------------------------------
    # cleanup (logical forget)
    # ------------------------------------------------------------------

    def cleanup(self, conversations: list[Conversation] | None = None) -> dict[str, Any]:
        records = self.cp.load_ingestion_records()
        if conversations:
            sids = {c.sample_id for c in conversations}
            records = {sid: recs for sid, recs in records.items() if sid in sids}

        forgotten = 0
        errors = 0
        for sid, recs in records.items():
            for rec in recs:
                memory_ids = rec.get("memory_ids", [])
                if rec.get("memory_id"):
                    memory_ids = [rec["memory_id"], *memory_ids]
                for memory_id in memory_ids:
                    idem_key = stable_key(
                        "forget", self.config.dataset_sha, self.config.run_id, memory_id
                    )
                    try:
                        self.client.forget(
                            memory_id=memory_id,
                            reason="locomo run reset",
                            idempotency_key=idem_key,
                        )
                        forgotten += 1
                    except DMemError as exc:
                        logger.warning("forget %s failed: %s", memory_id, exc)
                        errors += 1

        return {"forgotten": forgotten, "errors": errors}
