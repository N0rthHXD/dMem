"""Runner: namespace bootstrap, ingestion profiles, recall, and metrics.

The runner drives three phases against a live dMem daemon:

1. **Ingest** — create a ``user → project → task`` namespace hierarchy, then
   write each LongMemEval haystack session through the selected profile.
2. **Semantic barrier** — for semantic/hybrid modes, trigger one semantic
   rebuild and wait for completion before querying.
3. **Recall** — query each question's task scope, map recalled evidence back to
   LongMemEval sessions, and compute upstream-compatible retrieval metrics.

The runner never imports or starts the daemon; it is a pure HTTP client.
"""

from __future__ import annotations

import hashlib
import logging
import sys
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any

from .api import DMemClient, DMemError
from .checkpoint import Checkpoint
from .dataset import Case
from .metrics import aggregate_metrics, evaluate_retrieval
from .progress import tqdm

logger = logging.getLogger(__name__)

DEFAULT_CUTOFFS = [1, 3, 5, 10, 30, 50]
RETRIEVAL_MODES = {"keyword", "semantic", "hybrid"}
INGESTION_PROFILES = {"direct-turn", "processed-events"}
CONFIRMATION_POLICIES = {"confirm-all-new", "none"}
RESULT_VERSION = "longmemeval-dmem-py-v2"


@dataclass
class RunConfig:
    run_id: str
    dataset_sha: str
    socket_path: str
    ingestion_profile: str = "direct-turn"
    confirmation_policy: str = "confirm-all-new"
    job_poll_interval: float = 3.0
    job_timeout: float = 600.0
    retrieval_mode: str = "keyword"
    limit: int = 100
    max_chars: int = 100000
    cutoffs: list[int] = field(default_factory=lambda: DEFAULT_CUTOFFS.copy())
    answer_cutoff: int = 30
    caller_id: str = "longmemeval"
    output_dir: str = "results/longmemeval"
    request_timeout: float = 120.0
    fail_on_degraded: bool = False


def stable_key(*parts: str) -> str:
    raw = "\x00".join(parts)
    h = hashlib.sha256(raw.encode()).hexdigest()[:32]
    return f"lme-{h}"


def to_rfc3339(dt: datetime) -> str:
    return dt.replace(tzinfo=timezone.utc).isoformat()


class Runner:
    def __init__(self, client: DMemClient, config: RunConfig, cp: Checkpoint):
        self.client = client
        self.config = config
        self.cp = cp
        if config.retrieval_mode not in RETRIEVAL_MODES:
            raise ValueError(f"invalid retrieval_mode: {config.retrieval_mode}")
        if config.ingestion_profile not in INGESTION_PROFILES:
            raise ValueError(f"invalid ingestion_profile: {config.ingestion_profile}")
        if config.confirmation_policy not in CONFIRMATION_POLICIES:
            raise ValueError(
                f"invalid confirmation_policy: {config.confirmation_policy}"
            )
        for k in config.cutoffs:
            if not (1 <= k <= config.limit):
                raise ValueError(f"cutoff {k} must be in [1, limit={config.limit}]")

    # ------------------------------------------------------------------
    # public entry point
    # ------------------------------------------------------------------

    def run(self, cases: list[Case]) -> dict[str, Any]:
        manifest: dict[str, Any] = {
            "format_version": RESULT_VERSION,
            "run_id": self.config.run_id,
            "dataset_sha": self.config.dataset_sha,
            "ingestion_profile": self.config.ingestion_profile,
            "confirmation_policy": self.config.confirmation_policy,
            "retrieval_mode": self.config.retrieval_mode,
            "limit": self.config.limit,
            "max_chars": self.config.max_chars,
            "cutoffs": self.config.cutoffs,
            "answer_cutoff": self.config.answer_cutoff,
            "socket_path": self.config.socket_path,
            "caller_id": self.config.caller_id,
            "created_at": datetime.now(timezone.utc).isoformat(),
        }
        self.cp.write_manifest(manifest)

        user_id, project_id = self._bootstrap_namespace()

        self._task_cache: dict[str, str] = {}
        for s in self.client.list_spaces(
            parent_space_id=project_id, space_type="task"
        ):
            ext = s.get("external_id", "")
            if ext.startswith("q:"):
                qid = ext[2:]
                self._task_cache[qid] = s["space_id"]

        task_map = self._ingest_phase(cases, project_id)

        if self.config.retrieval_mode in ("semantic", "hybrid"):
            self._semantic_barrier()

        self._recall_phase(cases, task_map)

        summary = self._summarize(cases)
        self.cp.write_summary(summary)
        return summary

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
                external_id="longmemeval",
                display_name="LongMemEval",
                idempotency_key=stable_key(
                    "root", self.config.dataset_sha, self.config.run_id
                ),
            )
            user_id = result["space_id"]
            logger.info("created user root: %s", user_id)

        project_ext = (
            f"longmemeval:{self.config.dataset_sha[:16]}:"
            f"{self.config.ingestion_profile}:{self.config.run_id}"
        )
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
                display_name=f"LongMemEval {self.config.run_id}",
                parent_space_id=user_id,
                idempotency_key=stable_key(
                    "project",
                    self.config.dataset_sha,
                    self.config.ingestion_profile,
                    self.config.run_id,
                ),
            )
            project_id = result["space_id"]
            logger.info("created project: %s", project_id)
        else:
            logger.info("reusing project: %s", project_id)

        return user_id, project_id

    def _get_or_create_task(self, case: Case, project_id: str) -> str:
        cached = getattr(self, "_task_cache", {}).get(case.question_id)
        if cached:
            return cached
        task_ext = f"q:{case.question_id}"
        result = self.client.create_space(
            space_type="task",
            external_id=task_ext,
            display_name=f"Q: {case.question_id}",
            parent_space_id=project_id,
            idempotency_key=stable_key(
                "task",
                self.config.dataset_sha,
                self.config.ingestion_profile,
                self.config.run_id,
                case.question_id,
            ),
        )
        space_id = result["space_id"]
        if not hasattr(self, "_task_cache"):
            self._task_cache = {}
        self._task_cache[case.question_id] = space_id
        return space_id

    # ------------------------------------------------------------------
    # ingest phase
    # ------------------------------------------------------------------

    def _ingest_phase(
        self, cases: list[Case], project_id: str
    ) -> dict[str, str]:
        ingested = self.cp.load_ingested_qids()
        ingestion_records = self.cp.load_ingestion_records()
        task_map: dict[str, str] = {}

        for qid, recs in ingestion_records.items():
            if recs:
                task_map[qid] = recs[0]["task_space_id"]

        todo = [c for c in cases if c.question_id not in ingested]
        case_qids = {case.question_id for case in cases}
        completed_events = sum(
            rec.get("record_type", "event") == "event"
            for qid, records in ingestion_records.items()
            if qid in case_qids
            for rec in records
        )
        with tqdm(
            total=sum(self._case_event_count(case) for case in cases),
            initial=completed_events,
            desc="ingest",
            unit="event",
            file=sys.stderr,
        ) as bar:
            self._ingest_bar = bar
            for case in todo:
                task_id = self._get_or_create_task(case, project_id)
                task_map[case.question_id] = task_id
                self._ingest_case(case, task_id)
            self._ingest_bar = None

        return task_map

    @staticmethod
    def _case_event_count(case: Case) -> int:
        count = 0
        for session in case.sessions():
            for start in range(0, len(session.turns), 2):
                if any(turn.content for turn in session.turns[start:start + 2]):
                    count += 1
        return count

    def _ingest_case(self, case: Case, task_space_id: str) -> None:
        existing = {
            (rec.get("session_id"), rec.get("pair_index")): rec
            for rec in self.cp.load_ingestion_records().get(case.question_id, [])
            if rec.get("record_type", "event") == "event"
        }
        sessions = case.sessions()
        for session in sessions:
            turns = session.turns
            for start in range(0, len(turns), 2):
                pair = turns[start : start + 2]
                pair_index = start // 2
                parts = []
                for turn in pair:
                    if turn.content:
                        parts.append(f"{turn.role}: {turn.content}")
                content = "\n".join(parts)
                if not content:
                    continue

                if (session.session_id, pair_index) in existing:
                    continue

                bar = getattr(self, "_ingest_bar", None)
                if bar is not None:
                    bar.set_postfix_str(
                        f"q={case.question_id} session={session.session_id} "
                        f"pair={pair_index + 1} submit"
                    )

                has_answer = any(
                    t.has_answer for t in pair if t.role == "user"
                )
                session_date_key = session.date.isoformat()
                source_id = stable_key(
                    "source",
                    self.config.ingestion_profile,
                    self.config.dataset_sha,
                    self.config.run_id,
                    case.question_id,
                    session.session_id,
                    session_date_key,
                    str(pair_index),
                )
                idem_key = stable_key(
                    self.config.ingestion_profile,
                    self.config.dataset_sha,
                    self.config.run_id,
                    case.question_id,
                    session.session_id,
                    session_date_key,
                    str(pair_index),
                )
                metadata = {
                    "benchmark": "longmemeval",
                    "dataset_sha256": self.config.dataset_sha,
                    "run_id": self.config.run_id,
                    "question_id": case.question_id,
                    "session_id": session.session_id,
                    "pair_index": pair_index,
                    "has_answer": has_answer,
                }

                if self.config.ingestion_profile == "processed-events":
                    record = self._ingest_processed_event(
                        case=case,
                        task_space_id=task_space_id,
                        session_id=session.session_id,
                        pair_index=pair_index,
                        content=content,
                        occurred_at=to_rfc3339(session.date),
                        source_id=source_id,
                        metadata=metadata,
                        idempotency_key=idem_key,
                    )
                else:
                    result = self.client.remember(
                        space_id=task_space_id,
                        content=content,
                        memory_type="event",
                        source_name="longmemeval",
                        source_event_id=source_id,
                        occurred_at=to_rfc3339(session.date),
                        metadata=metadata,
                        reason="longmemeval_direct_turn",
                        idempotency_key=idem_key,
                    )
                    record = {
                        "event_id": result.get("event_id", ""),
                        "memory_id": result.get("memory", {}).get("memory_id", ""),
                    }

                self.cp.append_ingestion({
                    "record_type": "event",
                    "profile": self.config.ingestion_profile,
                    "question_id": case.question_id,
                    "session_id": session.session_id,
                    "pair_index": pair_index,
                    "task_space_id": task_space_id,
                    "has_answer": has_answer,
                    **record,
                })
                if bar is not None:
                    if self.config.ingestion_profile == "processed-events":
                        bar.set_postfix_str(
                            f"q={case.question_id} candidates="
                            f"{len(record.get('candidate_ids', []))} confirmed="
                            f"{len(record.get('confirmed', []))}"
                        )
                    bar.update(1)

        self.cp.append_ingestion({
            "record_type": "case_complete",
            "profile": self.config.ingestion_profile,
            "question_id": case.question_id,
            "task_space_id": task_space_id,
        })

    def _ingest_processed_event(
        self,
        *,
        case: Case,
        task_space_id: str,
        session_id: str,
        pair_index: int,
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
            source_name="longmemeval",
            source_event_id=source_id,
            occurred_at=occurred_at,
            metadata=metadata,
            idempotency_key=idempotency_key,
        )
        event_id = result.get("event_id", "")
        if not event_id:
            raise RuntimeError("POST /v1/events returned no event_id")

        jobs = [
            self.client.wait_for_job(
                job_id,
                poll_interval=self.config.job_poll_interval,
                timeout=self.config.job_timeout,
                on_status=lambda job: self._show_job_progress(
                    case.question_id, pair_index, job
                ),
            )
            for job_id in result.get("job_ids", [])
        ]
        if not jobs:
            raise RuntimeError(
                f"processed-events requires an extraction job for event {event_id}"
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
                "processed-events requires a configured extraction processor"
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
                        reason_code="longmemeval_confirm_all_new",
                        note="Explicit processed-events benchmark policy",
                        idempotency_key=stable_key(
                            "confirm",
                            self.config.dataset_sha,
                            self.config.run_id,
                            case.question_id,
                            session_id,
                            str(pair_index),
                            candidate_id,
                        ),
                    )
                    memory_id = memory.get("memory_id", "")
                else:
                    raise RuntimeError(
                        f"candidate {candidate_id} was rejected outside benchmark policy"
                    )
                confirmed.append({
                    "candidate_id": candidate_id,
                    "memory_id": memory_id,
                })

        return {
            "event_id": event_id,
            "job_ids": result.get("job_ids", []),
            "processing_result_codes": result_codes,
            "candidate_ids": [c["candidate_id"] for c in candidates],
            "confirmed": confirmed,
            "memory_ids": [c["memory_id"] for c in confirmed if c["memory_id"]],
        }

    def _show_job_progress(
        self, question_id: str, pair_index: int, job: dict[str, Any]
    ) -> None:
        bar = getattr(self, "_ingest_bar", None)
        if bar is not None:
            bar.set_postfix_str(
                f"q={question_id} pair={pair_index + 1} "
                f"job={job.get('status', 'unknown')} "
                f"attempt={job.get('attempt_count', 0)}"
            )

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
                    job.get("status"),
                    job.get("attempt_count"),
                    time.monotonic() - started,
                ),
                on_status=lambda health: logger.info(
                    "semantic barrier: state=%s queue=%s elapsed=%.1fs",
                    health.get("semantic_state"),
                    health.get("job_queue_depth"),
                    time.monotonic() - started,
                ),
            )
            logger.info(
                "semantic barrier complete in %.1fs", time.monotonic() - started
            )
        except Exception as exc:
            logger.warning("semantic barrier failed: %s", exc)
            if self.config.fail_on_degraded:
                raise

    # ------------------------------------------------------------------
    # recall phase
    # ------------------------------------------------------------------

    def _recall_phase(
        self, cases: list[Case], task_map: dict[str, str]
    ) -> None:
        done = self.cp.load_retrieval_qids()
        ingestion_records = self.cp.load_ingestion_records()
        todo = [case for case in cases if case.question_id not in done]
        with tqdm(
            todo,
            total=len(cases),
            initial=len(done),
            desc="recall",
            unit="q",
            file=sys.stderr,
        ) as bar:
            for case in bar:
                task_id = task_map.get(case.question_id)
                if not task_id:
                    recs = ingestion_records.get(case.question_id)
                    task_id = recs[0]["task_space_id"] if recs else None
                if not task_id:
                    logger.warning("no task space for %s, skipping", case.question_id)
                    continue
                self._recall_case(case, task_id, ingestion_records)
                bar.set_postfix_str(case.question_id)

    def _recall_case(
        self, case: Case, task_space_id: str,
        ingestion_records: dict[str, list[dict[str, Any]]] | None = None,
    ) -> None:
        if ingestion_records is None:
            ingestion_records = self.cp.load_ingestion_records()
        case_records = ingestion_records.get(case.question_id, [])
        event_labels: dict[str, dict[str, Any]] = {}
        for rec in case_records:
            if rec.get("event_id"):
                event_labels[rec["event_id"]] = rec

        response = self.client.recall(
            query=case.question,
            space_ids=[task_space_id],
            include_ancestors=False,
            reference_time=to_rfc3339(case.question_date),
            mode=self.config.retrieval_mode,
            limit=self.config.limit,
            max_chars=self.config.max_chars,
            include_evidence=True,
            exclude_stale_aggregates=True,
        )

        corpus_ids = case.haystack_session_ids
        correct_docs = set(case.answer_session_ids)

        session_rank_indices: list[int] = []
        seen_sessions: set[str] = set()
        unmapped_count = 0

        for res in response.get("results", []):
            memory = res.get("memory", {})
            revision = memory.get("current_revision", {})
            evidence_list = revision.get("evidence", [])

            mapped_session: str | None = None
            for ev in evidence_list:
                ev_id = ev.get("event_id", "")
                if ev_id in event_labels:
                    mapped_session = event_labels[ev_id]["session_id"]
                    break

            if mapped_session is None:
                unmapped_count += 1
                continue
            if mapped_session in seen_sessions:
                continue
            seen_sessions.add(mapped_session)
            if mapped_session in corpus_ids:
                session_rank_indices.append(corpus_ids.index(mapped_session))

        metrics: dict[str, dict[str, float]] = {}
        for k in self.config.cutoffs:
            recall_any, recall_all, ndcg_score = evaluate_retrieval(
                session_rank_indices, correct_docs, corpus_ids, k=k
            )
            metrics[f"@{k}"] = {
                "recall_any": recall_any,
                "recall_all": recall_all,
                "ndcg": ndcg_score,
            }

        relevant_events = [
            rec for rec in case_records if rec.get("has_answer")
        ]
        recalled_event_ids: set[str] = set()
        for res in response.get("results", []):
            revision = res.get("memory", {}).get("current_revision", {})
            for ev in revision.get("evidence", []):
                recalled_event_ids.add(ev.get("event_id", ""))
        evidence_coverage = 0.0
        if relevant_events:
            covered = sum(
                1 for rec in relevant_events if rec["event_id"] in recalled_event_ids
            )
            evidence_coverage = covered / len(relevant_events)

        ranked_items: list[dict[str, Any]] = []
        for res in response.get("results", []):
            revision = res.get("memory", {}).get("current_revision", {})
            content = revision.get("content", "")
            ms: str | None = None
            for ev in revision.get("evidence", []):
                ev_id = ev.get("event_id", "")
                if ev_id in event_labels:
                    ms = event_labels[ev_id]["session_id"]
                    break
            sd: str | None = None
            if ms and ms in corpus_ids:
                sd = to_rfc3339(case.haystack_dates[corpus_ids.index(ms)])
            ranked_items.append(
                {"content": content, "session_id": ms, "session_date": sd}
            )

        record: dict[str, Any] = {
            "question_id": case.question_id,
            "ranked_items": ranked_items[: self.config.answer_cutoff],
            "question_type": case.question_type,
            "is_abstention": case.is_abstention,
            "requested_mode": response.get("requested_mode"),
            "executed_mode": response.get("executed_mode"),
            "degraded": response.get("degraded", False),
            "degradation_reasons": response.get("degradation_reasons", []),
            "num_results": len(response.get("results", [])),
            "unmapped_count": unmapped_count,
            "session_rank_indices": session_rank_indices,
            "metrics": metrics,
            "evidence_coverage": evidence_coverage,
            "has_answer_session_labels": len(case.answer_session_ids) > 0,
        }
        self.cp.append_retrieval(record)

    # ------------------------------------------------------------------
    # summarize
    # ------------------------------------------------------------------

    def _summarize(self, cases: list[Case]) -> dict[str, Any]:
        retrieval_records = self.cp.load_retrieval_records()

        all_metrics: list[dict[str, dict[str, float]]] = []
        excluded_abstention: list[str] = []
        excluded_no_labels: list[str] = []
        excluded_errors: list[str] = []
        degraded_cases: list[str] = []

        for case in cases:
            rec = retrieval_records.get(case.question_id)
            if rec is None:
                excluded_errors.append(case.question_id)
                continue
            if rec.get("degraded"):
                degraded_cases.append(case.question_id)
            if case.is_abstention:
                excluded_abstention.append(case.question_id)
                continue
            if not rec.get("has_answer_session_labels"):
                excluded_no_labels.append(case.question_id)
                continue
            all_metrics.append({case.question_id: rec["metrics"]})

        summary_metrics = aggregate_metrics(all_metrics, self.config.cutoffs)

        by_type: dict[str, list[dict[str, dict[str, float]]]] = {}
        for case in cases:
            rec = retrieval_records.get(case.question_id)
            if rec is None or case.is_abstention or not rec.get("has_answer_session_labels"):
                continue
            by_type.setdefault(case.question_type, []).append(
                {case.question_id: rec["metrics"]}
            )
        per_type = {
            qt: aggregate_metrics(entries, self.config.cutoffs)
            for qt, entries in by_type.items()
        }

        return {
            "ingestion": self._summarize_ingestion(),
            "retrieval": {
                "metrics": summary_metrics,
                "per_type": per_type,
                "excluded_abstention": len(excluded_abstention),
                "excluded_no_labels": len(excluded_no_labels),
                "excluded_errors": len(excluded_errors),
                "degraded_cases": len(degraded_cases),
                "total_cases": len(cases),
                "evaluated_cases": len(all_metrics),
            },
        }

    def _summarize_ingestion(self) -> dict[str, Any]:
        events = [
            rec
            for records in self.cp.load_ingestion_records().values()
            for rec in records
            if rec.get("record_type", "event") == "event"
        ]
        return {
            "profile": self.config.ingestion_profile,
            "confirmation_policy": self.config.confirmation_policy,
            "events": len(events),
            "candidates": sum(len(rec.get("candidate_ids", [])) for rec in events),
            "confirmed": sum(len(rec.get("confirmed", [])) for rec in events),
            "no_memory": sum(
                "no_memory" in rec.get("processing_result_codes", [])
                for rec in events
            ),
        }

    # ------------------------------------------------------------------
    # cleanup (logical forget)
    # ------------------------------------------------------------------

    def cleanup(self, cases: list[Case] | None = None) -> dict[str, Any]:
        records = self.cp.load_ingestion_records()
        if cases:
            qids = {c.question_id for c in cases}
            records = {qid: recs for qid, recs in records.items() if qid in qids}

        forgotten = 0
        errors = 0
        for qid, recs in records.items():
            for rec in recs:
                memory_ids = rec.get("memory_ids", [])
                if rec.get("memory_id"):
                    memory_ids = [rec["memory_id"], *memory_ids]
                for memory_id in memory_ids:
                    idem_key = stable_key(
                        "forget",
                        self.config.dataset_sha,
                        self.config.run_id,
                        memory_id,
                    )
                    try:
                        self.client.forget(
                            memory_id=memory_id,
                            reason="longmemeval run reset",
                            idempotency_key=idem_key,
                        )
                        forgotten += 1
                    except DMemError as exc:
                        logger.warning("forget %s failed: %s", memory_id, exc)
                        errors += 1

        return {"forgotten": forgotten, "errors": errors}
