"""The three evaluation phases: memory build, retrieval and end-to-end QA."""

from __future__ import annotations

import json
import re
import threading
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable, Iterable, Sequence

from . import prompts
from .dmem_client import DMemClient
from .io_utils import read_json, read_jsonl, write_json, write_jsonl
from .llm import LLMClient
from .metrics import evaluate_retrieval

ALLOWED_KINDS = {"fact", "event", "preference", "plan", "procedure"}
FALLBACK_OCCURRED_AT = "2023-01-01T00:00:00+00:00"


@dataclass
class EvalContext:
    config: Any
    run_dir: Path
    samples: list[Any]
    qa_items: list[dict]
    dmem: DMemClient
    llm: LLMClient
    log: Callable[[str], None] = print
    scope_map: dict[str, str] = field(default_factory=dict)
    memory_map: dict[str, set[str]] = field(default_factory=dict)
    event_map: dict[str, set[str]] = field(default_factory=dict)

    def path(self, name: str) -> Path:
        return self.run_dir / name


# --------------------------------------------------------------------------
# tolerant concurrency helper
# --------------------------------------------------------------------------
def _map_concurrent(
    func: Callable[[Any], Any],
    items: Sequence[Any],
    *,
    workers: int,
    description: str,
    log: Callable[[str], None],
) -> tuple[list[Any], list[tuple[int, str]]]:
    results: list[Any | None] = [None] * len(items)
    failures: list[tuple[int, str]] = []
    lock = threading.Lock()
    completed = 0

    def worker(position: int, item: Any) -> None:
        nonlocal completed
        try:
            value = func(item)
        except BaseException as exc:  # noqa: BLE001 - one bad item must not kill the run
            with lock:
                failures.append((position, str(exc)))
        else:
            results[position] = value
        with lock:
            completed += 1
            if completed % 25 == 0:
                log(f"  {description}: {completed}/{len(items)}")

    with ThreadPoolExecutor(max_workers=max(1, int(workers))) as pool:
        for position, item in enumerate(items):
            pool.submit(worker, position, item)
    return [value for value in results if value is not None], failures


# --------------------------------------------------------------------------
# phase 1: memory construction (Agent Tool route)
# --------------------------------------------------------------------------
def _truncate(text: str, limit: int) -> str:
    return text if len(text) <= limit else text[:limit]


def _as_list(value: Any) -> list[Any]:
    """Models occasionally emit a bare string where a list is expected."""

    if value is None:
        return []
    if isinstance(value, (list, tuple)):
        return list(value)
    return [value]


def _normalize_dia_id(value: Any) -> str:
    text = str(value or "").strip()
    match = re.fullmatch(r"[dD]\s*(\d+)\s*[:：]\s*(\d+)", text)
    if match:
        return f"D{int(match.group(1))}:{int(match.group(2))}"
    return text


def _normalize_memories(
    raw: Any, valid_dia_ids: set[str], build_cfg: dict
) -> list[dict]:
    if not isinstance(raw, list):
        return []
    max_content = int(build_cfg.get("max_content_runes") or 4096)
    max_hints = int(build_cfg.get("max_search_hints") or 8)
    memories: list[dict] = []
    for entry in raw:
        if not isinstance(entry, dict):
            continue
        kind = str(entry.get("kind") or "").strip().lower()
        if kind not in ALLOWED_KINDS:
            continue
        content = _truncate(str(entry.get("content") or "").strip(), max_content)
        if not content:
            continue
        dia_ids = []
        for value in _as_list(entry.get("dia_ids")):
            dia_id = _normalize_dia_id(value)
            if dia_id in valid_dia_ids and dia_id not in dia_ids:
                dia_ids.append(dia_id)
        if not dia_ids:
            continue
        hints: list[str] = []
        for value in _as_list(entry.get("search_hints")):
            hint = _truncate(str(value).strip(), 256)
            if hint and hint not in hints:
                hints.append(hint)
            if len(hints) >= max_hints:
                break
        sensitivity = str(entry.get("sensitivity") or "normal").strip().lower()
        if sensitivity not in {"normal", "sensitive"}:
            sensitivity = "normal"
        try:
            confidence = float(entry.get("confidence", 0.9))
        except (TypeError, ValueError):
            confidence = 0.9
        confidence = min(1.0, max(0.0, confidence))
        memories.append(
            {
                "kind": kind,
                "content": content,
                "search_hints": hints,
                "sensitivity": sensitivity,
                "confidence": confidence,
                "dia_ids": dia_ids,
            }
        )
    return memories


def _ensure_scopes(ctx: EvalContext) -> None:
    build_cfg = ctx.config.section("build")
    root_external_id = str(build_cfg.get("scope_root_external_id") or "locomo-zh-eval")
    root = ctx.dmem.ensure_scope(
        scope_type="user",
        external_id=root_external_id,
        display_name="LoCoMo evaluation root",
    )
    root_id = root.get("scope_id") or root.get("space_id")
    ctx.scope_map = {}
    for sample in ctx.samples:
        scope = ctx.dmem.ensure_scope(
            scope_type="project",
            external_id=sample.sample_id,
            display_name=f"LoCoMo {sample.sample_id}",
            parent_scope_id=root_id,
        )
        ctx.scope_map[sample.sample_id] = scope.get("scope_id") or scope.get("space_id")
    write_json(ctx.path("scopes.json"), ctx.scope_map)
    ctx.log(f"  ensured {len(ctx.scope_map)} project scopes under root {root_external_id}")


def _extract_session(ctx: EvalContext, sample: Any, session: Any) -> tuple[Any, list[dict]]:
    build_cfg = ctx.config.section("build")
    include_captions = bool(build_cfg.get("include_image_captions", True))
    transcript = "\n".join(
        turn.transcript_line(include_captions=include_captions) for turn in session.turns
    )
    payload = ctx.llm.chat_json(
        [
            {"role": "system", "content": prompts.EXTRACTION_SYSTEM},
            {
                "role": "user",
                "content": prompts.extraction_user(
                    sample.sample_id,
                    sample.speaker_a,
                    sample.speaker_b,
                    session.key,
                    session.date_time_raw,
                    transcript,
                ),
            },
        ]
    )
    valid_dia_ids = {turn.dia_id for turn in session.turns}
    memories = _normalize_memories(payload.get("memories"), valid_dia_ids, build_cfg)
    return session, memories


def _commit_session(
    ctx: EvalContext, sample: Any, session: Any, memories: list[dict]
) -> tuple[list[dict], list[dict]]:
    if not memories:
        return [], []
    build_cfg = ctx.config.section("build")
    chunk_size = max(1, int(build_cfg.get("max_memories_per_commit") or 24))
    scope_id = ctx.scope_map[sample.sample_id]
    occurred_at = session.occurred_at or FALLBACK_OCCURRED_AT
    memory_records: list[dict] = []
    commit_records: list[dict] = []
    for chunk_index in range(0, len(memories), chunk_size):
        chunk = memories[chunk_index : chunk_index + chunk_size]
        suggestions = []
        for position, memory in enumerate(chunk):
            suggestions.append(
                {
                    "local_id": f"m{position}",
                    "kind": memory["kind"],
                    "content": memory["content"],
                    "sensitivity": memory["sensitivity"],
                    "confidence": memory["confidence"],
                    "search_hints": memory["search_hints"],
                }
            )
        source = {
            "type": "agent_tool",
            "name": "locomo-zh-eval",
            "conversation_id": sample.sample_id,
            "turn_id": session.key,
            "occurred_at": occurred_at,
            "metadata": {
                "sample_id": sample.sample_id,
                "session": session.key,
                "session_date": session.date_time_raw,
            },
        }
        response = ctx.dmem.commit(scope_id=scope_id, source=source, memories=suggestions)
        commit_records.append(
            {
                "sample_id": sample.sample_id,
                "session": session.key,
                "scope_id": scope_id,
                "source_event_id": response.get("source_event_id"),
                "dia_ids": [turn.dia_id for turn in session.turns],
            }
        )
        outcomes = {item.get("local_id"): item for item in response.get("memories", [])}
        for position, memory in enumerate(chunk):
            outcome = outcomes.get(f"m{position}") or {}
            memory_records.append(
                {
                    "sample_id": sample.sample_id,
                    "session": session.key,
                    "scope_id": scope_id,
                    "local_id": f"m{position}",
                    "status": outcome.get("status"),
                    "memory_id": outcome.get("memory_id"),
                    "revision_id": outcome.get("revision_id"),
                    "reason_code": outcome.get("reason_code"),
                    "kind": memory["kind"],
                    "content": memory["content"],
                    "dia_ids": memory["dia_ids"],
                }
            )
    return memory_records, commit_records


def run_build(ctx: EvalContext) -> dict[str, Any]:
    ctx.log("phase 1/3: memory construction (Agent Tool route)")
    _ensure_scopes(ctx)
    tasks = [
        (sample, session) for sample in ctx.samples for session in sample.sessions
    ]
    workers = int(ctx.config.section("llm").get("max_concurrent") or 8)

    def extract(task: tuple[Any, Any]) -> tuple[Any, Any, list[dict]]:
        sample, session = task
        _, memories = _extract_session(ctx, sample, session)
        return sample, session, memories

    extracted, failures = _map_concurrent(
        extract, tasks, workers=workers, description="sessions extracted", log=ctx.log
    )
    for position, error in failures:
        sample, session = tasks[position]
        ctx.log(f"  ! extraction failed for {sample.sample_id}/{session.key}: {error}")

    def commit(task: tuple[Any, Any, list[dict]]) -> tuple[list[dict], list[dict]]:
        sample, session, memories = task
        return _commit_session(ctx, sample, session, memories)

    committed, commit_failures = _map_concurrent(
        commit, extracted, workers=workers, description="sessions committed", log=ctx.log
    )
    for position, error in commit_failures:
        ctx.log(f"  ! commit failed for task {position}: {error}")

    memory_records: list[dict] = []
    commit_records: list[dict] = []
    for records, commits in committed:
        memory_records.extend(records)
        commit_records.extend(commits)

    write_jsonl(ctx.path("build_memories.jsonl"), memory_records)
    write_jsonl(ctx.path("build_commits.jsonl"), commit_records)

    status_counts: dict[str, int] = {}
    for record in memory_records:
        status = str(record.get("status"))
        status_counts[status] = status_counts.get(status, 0) + 1
    summary = {
        "sessions_total": len(tasks),
        "sessions_extracted": len(extracted),
        "sessions_failed": len(failures) + len(commit_failures),
        "memories_extracted": len(memory_records),
        "commits": len(commit_records),
        "status_counts": dict(sorted(status_counts.items())),
        "authority_memories": len(
            {
                record["memory_id"]
                for record in memory_records
                if record.get("memory_id") and record.get("status") in {"authority", "deduplicated"}
            }
        ),
    }
    write_json(ctx.path("build_summary.json"), summary)
    ctx.log(
        f"  extracted {summary['memories_extracted']} memories over {len(commit_records)} commits; "
        f"statuses={summary['status_counts']}"
    )
    return summary


# --------------------------------------------------------------------------
# shared map loading
# --------------------------------------------------------------------------
def load_maps(ctx: EvalContext, source_dir: Path | None = None) -> None:
    base = source_dir or ctx.run_dir
    if not ctx.scope_map:
        ctx.scope_map = read_json(base / "scopes.json", {}) or {}
    if not ctx.memory_map:
        for record in read_jsonl(base / "build_memories.jsonl"):
            memory_id = record.get("memory_id")
            if memory_id and record.get("status") in {"authority", "deduplicated"}:
                ctx.memory_map.setdefault(memory_id, set()).update(record.get("dia_ids") or [])
    if not ctx.event_map:
        for record in read_jsonl(base / "build_commits.jsonl"):
            event_id = record.get("source_event_id")
            if event_id:
                ctx.event_map.setdefault(event_id, set()).update(record.get("dia_ids") or [])
    if not ctx.scope_map:
        raise RuntimeError(
            "no scope map found; run the build phase first or point --run-id at a completed run"
        )


def _intent_for(config: Any, category: int) -> str:
    mapping = config.section("retrieval").get("intent_by_category") or {}
    value = mapping.get(category, mapping.get(str(category), "auto"))
    return str(value or "auto")


# --------------------------------------------------------------------------
# phase 2: retrieval evaluation
# --------------------------------------------------------------------------
def _retrieve_one(ctx: EvalContext, qa: dict) -> dict[str, Any]:
    retrieval_cfg = ctx.config.section("retrieval")
    scope_id = ctx.scope_map.get(qa["sample_id"])
    if not scope_id:
        raise RuntimeError(f"no scope for sample {qa['sample_id']}")
    intent = _intent_for(ctx.config, qa["category"])
    response = ctx.dmem.recall(
        query=qa["question"],
        scope_ids=[scope_id],
        intent=intent,
        limit=int(retrieval_cfg.get("limit") or 30),
    )
    ranked = []
    for memory in response.get("memories", []):
        memory_id = memory.get("memory_id")
        # Prefer the precise dialogue ids the extractor attributed to this
        # memory; only fall back to the commit's source event (which covers a
        # whole session) when no precise mapping exists.
        dia_ids = set(ctx.memory_map.get(memory_id) or ())
        if not dia_ids:
            for evidence in memory.get("evidence") or []:
                dia_ids |= ctx.event_map.get(evidence.get("event_id"), set())
        ranked.append(
            {
                "memory_id": memory_id,
                "revision_id": memory.get("revision_id"),
                "kind": memory.get("kind"),
                "content": memory.get("content"),
                "confidence": memory.get("confidence"),
                "dia_ids": sorted(dia_ids),
            }
        )
    return {
        "qa_id": qa["qa_id"],
        "sample_id": qa["sample_id"],
        "category": qa["category"],
        "question": qa["question"],
        "answer": qa["answer"],
        "adversarial_answer": qa.get("adversarial_answer"),
        "gold": qa["evidence"],
        "intent": intent,
        "degraded_capabilities": response.get("degraded_capabilities") or [],
        "ranked": ranked,
    }


def run_retrieval(ctx: EvalContext) -> dict[str, Any]:
    ctx.log("phase 2/3: retrieval evaluation")
    load_maps(ctx)
    retrieval_cfg = ctx.config.section("retrieval")
    records, failures = _map_concurrent(
        lambda qa: _retrieve_one(ctx, qa),
        ctx.qa_items,
        workers=int(retrieval_cfg.get("workers") or 8),
        description="queries recalled",
        log=ctx.log,
    )
    for position, error in failures:
        ctx.log(f"  ! recall failed for {ctx.qa_items[position]['qa_id']}: {error}")
    records.sort(key=lambda item: (item["sample_id"], item["qa_id"]))
    write_jsonl(ctx.path("retrieval.jsonl"), records)
    metrics = evaluate_retrieval(records, retrieval_cfg.get("cutoffs") or [1, 3, 5, 10, 30])
    degraded: dict[str, int] = {}
    for record in records:
        for capability in record.get("degraded_capabilities") or []:
            degraded[capability] = degraded.get(capability, 0) + 1
    summary = {
        "queries": len(records),
        "failed_queries": len(failures),
        "degradation_counts": dict(sorted(degraded.items())),
        "metrics": metrics,
    }
    write_json(ctx.path("retrieval_summary.json"), summary)
    overall = (metrics.get("overall") or {}).get("@10", {})
    ctx.log(
        f"  retrieval @10: hit={overall.get('hit')} coverage={overall.get('coverage')} "
        f"mrr={overall.get('mrr')} ndcg={overall.get('ndcg')}"
    )
    return summary


def load_retrieval(ctx: EvalContext) -> list[dict]:
    records = read_jsonl(ctx.path("retrieval.jsonl"))
    if not records:
        raise RuntimeError("no retrieval.jsonl found; run the retrieval phase first")
    return records


# --------------------------------------------------------------------------
# phase 3: end-to-end QA evaluation
# --------------------------------------------------------------------------
def _build_context(record: dict, cutoff: int, max_chars: int) -> str:
    lines: list[str] = []
    used = 0
    for item in (record.get("ranked") or [])[:cutoff]:
        content = str(item.get("content") or "").strip()
        if not content:
            continue
        line = f"- {content}"
        if used + len(line) > max_chars:
            break
        lines.append(line)
        used += len(line)
    return "\n".join(lines)


def _judge(ctx: EvalContext, qa: dict, hypothesis: str) -> tuple[bool, str, str]:
    if qa["category"] == 5 or (qa.get("answer") is None and qa.get("adversarial_answer")):
        adversarial = str(qa.get("adversarial_answer") or "（无）")
        payload = ctx.llm.chat_json(
            [
                {"role": "system", "content": prompts.ADVERSARIAL_JUDGE_SYSTEM},
                {
                    "role": "user",
                    "content": prompts.adversarial_judge_user(
                        qa["question"], adversarial, hypothesis
                    ),
                },
            ]
        )
    else:
        payload = ctx.llm.chat_json(
            [
                {"role": "system", "content": prompts.JUDGE_SYSTEM},
                {
                    "role": "user",
                    "content": prompts.judge_user(
                        qa["question"], str(qa.get("answer") or ""), hypothesis
                    ),
                },
            ]
        )
    label = bool(payload.get("correct"))
    reason = str(payload.get("reason") or "")
    return label, reason, json.dumps(payload, ensure_ascii=False)


def _answer_one(ctx: EvalContext, record: dict) -> tuple[dict, dict]:
    qa_cfg = ctx.config.section("qa")
    cutoff = int(qa_cfg.get("answer_cutoff") or 10)
    max_chars = int(qa_cfg.get("max_context_chars") or 12000)
    context = _build_context(record, cutoff, max_chars)
    hypothesis = ctx.llm.chat(
        [
            {"role": "system", "content": prompts.ANSWER_SYSTEM},
            {"role": "user", "content": prompts.answer_user(context, record["question"])},
        ]
    ).strip()
    generation = {
        "qa_id": record["qa_id"],
        "sample_id": record["sample_id"],
        "category": record["category"],
        "question": record["question"],
        "answer": record.get("answer"),
        "adversarial_answer": record.get("adversarial_answer"),
        "hypothesis": hypothesis,
        "context_memory_ids": [
            item.get("memory_id") for item in (record.get("ranked") or [])[:cutoff]
        ],
    }
    if not qa_cfg.get("judge", True):
        return generation, {}
    label, reason, raw = _judge(ctx, record, hypothesis)
    judgement = {
        "qa_id": record["qa_id"],
        "sample_id": record["sample_id"],
        "category": record["category"],
        "label": label,
        "reason": reason,
        "raw_response": raw,
    }
    return generation, judgement


def run_qa(ctx: EvalContext) -> dict[str, Any]:
    ctx.log("phase 3/3: end-to-end QA evaluation")
    records = load_retrieval(ctx)
    qa_cfg = ctx.config.section("qa")
    results, failures = _map_concurrent(
        lambda record: _answer_one(ctx, record),
        records,
        workers=int(qa_cfg.get("workers") or 8),
        description="answers generated",
        log=ctx.log,
    )
    for position, error in failures:
        ctx.log(f"  ! QA failed for {records[position]['qa_id']}: {error}")
    generations = [item[0] for item in results]
    judgements = [item[1] for item in results if item[1]]
    generations.sort(key=lambda item: (item["sample_id"], item["qa_id"]))
    judgements.sort(key=lambda item: (item["sample_id"], item["qa_id"]))
    write_jsonl(ctx.path("qa.jsonl"), generations)
    write_jsonl(ctx.path("judge.jsonl"), judgements)
    accuracy = _accuracy(judgements)
    summary = {
        "questions": len(generations),
        "judged": len(judgements),
        "failed_questions": len(failures),
        "accuracy": accuracy,
    }
    write_json(ctx.path("qa_summary.json"), summary)
    ctx.log(
        f"  QA accuracy: overall={accuracy.get('overall')} by_category={accuracy.get('by_category')}"
    )
    return summary


def _accuracy(judgements: Iterable[dict]) -> dict[str, Any]:
    overall: list[int] = []
    by_category: dict[str, list[int]] = {}
    for item in judgements:
        value = 1 if item.get("label") else 0
        overall.append(value)
        by_category.setdefault(str(item.get("category")), []).append(value)
    if not overall:
        return {"overall": None, "by_category": {}, "count": 0}
    return {
        "overall": round(sum(overall) / len(overall), 6),
        "count": len(overall),
        "by_category": {
            category: {"accuracy": round(sum(values) / len(values), 6), "count": len(values)}
            for category, values in sorted(by_category.items())
        },
    }
