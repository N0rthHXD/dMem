"""Reproducible ablation reporting and promotion gates.

The statistics, bootstrap confidence intervals, graph-promotion accounting,
identity validation, and promotion gate logic are shared here. Benchmarks
supply a :class:`RecordSchema` that maps the generic metric names onto their
record layout; there are no benchmark-name checks in this module.
"""

from __future__ import annotations

import math
import random
from dataclasses import dataclass
from typing import Any, Sequence

ABLATION_MATRIX: dict[str, dict[str, Any]] = {
    "S": {"semantic": True, "keyword": False, "graph_features": False, "graph_candidates": None, "diversification": False},
    "SK": {"semantic": True, "keyword": True, "graph_features": False, "graph_candidates": None, "diversification": False},
    "SKF": {"semantic": True, "keyword": True, "graph_features": True, "graph_candidates": None, "diversification": False},
    "SKD": {"semantic": True, "keyword": True, "graph_features": True, "graph_candidates": "direct", "diversification": False},
    "SKP": {"semantic": True, "keyword": True, "graph_features": True, "graph_candidates": "bounded_path", "diversification": False},
    "SKPM": {"semantic": True, "keyword": True, "graph_features": True, "graph_candidates": "bounded_path", "diversification": True},
}
GRAPH_FAMILY_ABLATIONS = (
    "direct_entity_mention", "exact_predicate_fact", "one_hop_fact",
    "two_hop_path", "memory_relation",
)
GRAPH_SIGNAL_FAMILIES = frozenset((*GRAPH_FAMILY_ABLATIONS, "graph"))
DEFAULT_BOOTSTRAP_ITERATIONS = 2000
DEFAULT_BOOTSTRAP_SEED = "dmem-evaluation-bootstrap-v1"
MIN_REPETITIONS = 3
GRAPH_RECALL_REGRESSION_LIMIT = 0.005
GRAPH_PROMOTION_PRECISION_MINIMUM = 0.5
DUPLICATE_RATE_REDUCTION_MINIMUM = 0.5


class ReportingError(ValueError):
    pass


@dataclass(frozen=True)
class RecordSchema:
    """Maps generic report calculations onto a benchmark record layout."""

    id_field: str
    category_field: str
    metrics_layer: str
    recall_metric: str
    recall_at_1_metrics: tuple[str, ...]
    mrr_metrics: tuple[str, ...]
    evidence_field: str
    report_schema: str
    gold_memory_field: str = "gold_memory_ids"
    gold_session_field: str | None = None
    item_session_field: str | None = None
    delta_keys: tuple[str, ...] = ("category_delta",)
    gain_source_key: str = "category_delta"
    gain_key: str = "intended_category_gain"


def ablation_request(cell: str) -> dict[str, Any]:
    if cell not in ABLATION_MATRIX:
        raise ReportingError(f"unknown ablation cell: {cell!r}")
    definition = ABLATION_MATRIX[cell]
    capabilities = [name for name in ("semantic", "keyword") if definition[name]]
    candidates = definition["graph_candidates"]
    graph_policy = candidates or ("features_only" if definition["graph_features"] else "off")
    if graph_policy != "off":
        capabilities.append("graph")
    return {
        "capabilities": capabilities,
        "graph_policy": graph_policy,
        "diversification": bool(definition["diversification"]),
    }


def matrix_cells() -> tuple[str, ...]:
    return tuple(ABLATION_MATRIX)


def mean(values: Sequence[float]) -> float:
    return sum(values) / len(values) if values else 0.0


def stddev(values: Sequence[float]) -> float:
    if len(values) < 2:
        return 0.0
    average = mean(values)
    return math.sqrt(sum((value - average) ** 2 for value in values) / (len(values) - 1))


def paired_bootstrap_ci(
    candidate: Sequence[float], baseline: Sequence[float], *,
    iterations: int = DEFAULT_BOOTSTRAP_ITERATIONS,
    seed: str = DEFAULT_BOOTSTRAP_SEED,
) -> tuple[float, float]:
    if len(candidate) != len(baseline):
        raise ReportingError("paired bootstrap requires equal-length samples")
    if not candidate:
        return (0.0, 0.0)
    differences = [float(a) - float(b) for a, b in zip(candidate, baseline)]
    rng = random.Random(seed)
    samples = [mean([differences[rng.randrange(len(differences))] for _ in differences]) for _ in range(iterations)]
    samples.sort()
    return samples[int(0.025 * (len(samples) - 1))], samples[int(0.975 * (len(samples) - 1))]


def _metric(record: dict[str, Any], cutoff: str, name: str, schema: RecordSchema) -> float:
    value = ((record.get(schema.metrics_layer) or {}).get(cutoff) or {}).get(name, 0.0)
    return float(value) if isinstance(value, (int, float)) and not isinstance(value, bool) else 0.0


def _first_metric(
    record: dict[str, Any], cutoff: str, names: Sequence[str], schema: RecordSchema
) -> float:
    for name in names:
        value = _metric(record, cutoff, name, schema)
        if value:
            return value
    return 0.0


def _number(record: dict[str, Any], name: str) -> float:
    value = record.get(name, 0.0)
    return float(value) if isinstance(value, (int, float)) and not isinstance(value, bool) else 0.0


def query_metrics(record: dict[str, Any], schema: RecordSchema) -> dict[str, float]:
    return {
        "recall": _metric(record, "@10", schema.recall_metric, schema),
        "ranking": _metric(record, "@10", "ndcg", schema),
        "recall_at_1": _first_metric(record, "@1", schema.recall_at_1_metrics, schema),
        "mrr": _first_metric(record, "@10", schema.mrr_metrics, schema)
        or _first_metric(record, "@1", schema.mrr_metrics, schema),
        "duplicate_rate": _number(record, "duplicate_rate"),
        "latency_ms": _number(record, "recall_latency_ms"),
        "storage_bytes": _number(record, "storage_bytes") or _number(record, "response_bytes"),
        "prompt_tokens": _number(record, "prompt_tokens"),
        "completion_tokens": _number(record, "completion_tokens"),
        "provider_requests": _number(record, "provider_requests") or _number(record, "embedding_requests"),
        "provider_cost_usd": _number(record, "provider_cost_usd"),
        "safety_violations": _number(record, "safety_violations") or float(bool(record.get("safety_violation"))),
    }


def run_metrics(records: Sequence[dict[str, Any]], schema: RecordSchema) -> dict[str, float]:
    names = query_metrics({}, schema).keys()
    result = {
        name: mean([query_metrics(record, schema)[name] for record in records])
        for name in names
    }
    result["latency"] = result["latency_ms"]
    result["storage"] = result["storage_bytes"]
    result["provider_cost"] = result["provider_requests"]
    return result


def aggregate_repetitions(
    runs: Sequence[Sequence[dict[str, Any]]], schema: RecordSchema
) -> dict[str, Any]:
    if len(runs) < MIN_REPETITIONS:
        raise ReportingError(f"end-to-end reports require at least {MIN_REPETITIONS} repetitions")
    per_run = [run_metrics(run, schema) for run in runs]
    return {
        "repetitions": len(runs),
        "metrics": {
            name: {"mean": mean(values), "std": stddev(values), "values": values}
            for name in per_run[0]
            for values in [[run[name] for run in per_run]]
        },
    }


def _ranked_items(record: dict[str, Any]) -> list[dict[str, Any]]:
    value = record.get("ranked_items")
    return value if isinstance(value, list) else []


def _is_graph_promoted(item: dict[str, Any]) -> bool:
    if "graph" not in set(item.get("matched_channels") or []):
        return False
    return bool(set((item.get("report") or {}).get("signal_family") or []) & GRAPH_SIGNAL_FAMILIES)


def _gold(record: dict[str, Any], schema: RecordSchema) -> tuple[set[str], set[str]]:
    memories = set(record.get(schema.gold_memory_field) or [])
    sessions = (
        set(record.get(schema.gold_session_field) or [])
        if schema.gold_session_field
        else set()
    )
    return memories, sessions


def _relevant_item(
    item: dict[str, Any], gold_memories: set[str], gold_sessions: set[str], schema: RecordSchema
) -> bool:
    if item.get("memory_id") in gold_memories:
        return True
    if schema.item_session_field and gold_sessions:
        return bool(set(item.get(schema.item_session_field) or []) & gold_sessions)
    return False


def _promotion_counts(
    records: Sequence[dict[str, Any]],
    schema: RecordSchema,
    family: str | None = None,
) -> dict[str, float]:
    promoted = relevant = harmful = considered = 0
    for record in records:
        gold_memories, gold_sessions = _gold(record, schema)
        if not gold_memories and not gold_sessions:
            continue
        considered += 1
        items = _ranked_items(record)[:10]
        ranks = [
            index
            for index, item in enumerate(items)
            if _relevant_item(item, gold_memories, gold_sessions, schema)
        ]
        best = min(ranks) if ranks else None
        for index, item in enumerate(items):
            if family is not None:
                families = set((item.get("report") or {}).get("signal_family") or [])
                if family not in families:
                    continue
            if not _is_graph_promoted(item):
                continue
            promoted += 1
            if _relevant_item(item, gold_memories, gold_sessions, schema):
                relevant += 1
            elif best is None or index <= best:
                harmful += 1
    return {
        "promoted": promoted,
        "precision": relevant / promoted if promoted else 0.0,
        "harm_rate": harmful / considered if considered else 0.0,
    }


def graph_promotion_report(
    records: Sequence[dict[str, Any]], schema: RecordSchema
) -> dict[str, float]:
    return _promotion_counts(records, schema)


def graph_family_reports(
    records: Sequence[dict[str, Any]], schema: RecordSchema
) -> dict[str, dict[str, float]]:
    return {
        family: _promotion_counts(records, schema, family)
        for family in GRAPH_FAMILY_ABLATIONS
    }


def category_deltas(
    candidate: Sequence[dict[str, Any]],
    baseline: Sequence[dict[str, Any]],
    schema: RecordSchema,
    *,
    name: str | None = None,
) -> dict[str, float]:
    metric_name = name or schema.recall_metric
    base = {record.get(schema.id_field): record for record in baseline}
    grouped: dict[str, list[float]] = {}
    for record in candidate:
        other = base.get(record.get(schema.id_field))
        if other is not None:
            grouped.setdefault(str(record.get(schema.category_field)), []).append(
                _metric(record, "@10", metric_name, schema)
                - _metric(other, "@10", metric_name, schema)
            )
    return {key: mean(values) for key, values in sorted(grouped.items())}


def evidence_buckets(
    records: Sequence[dict[str, Any]], schema: RecordSchema
) -> dict[str, dict[str, float]]:
    grouped: dict[str, list[float]] = {}
    for record in records:
        count = len(record.get(schema.evidence_field) or [])
        key = "0" if count == 0 else str(count) if count < 3 else "3+"
        grouped.setdefault(key, []).append(query_metrics(record, schema)["recall"])
    return {key: {"count": len(values), "recall": mean(values)} for key, values in sorted(grouped.items())}


def assert_identical_query_coverage(
    runs: dict[str, Sequence[Sequence[dict[str, Any]]]], schema: RecordSchema
) -> list[str]:
    reference: set[str] | None = None
    reference_name = ""
    for cell, repetitions in runs.items():
        for index, repetition in enumerate(repetitions, 1):
            raw_ids = [record.get(schema.id_field) for record in repetition]
            if any(qid is None for qid in raw_ids):
                raise ReportingError(
                    f"{cell}@{index} has a record without {schema.id_field}"
                )
            ids = {str(qid) for qid in raw_ids}
            if reference is None:
                reference, reference_name = ids, f"{cell}@{index}"
            elif ids != reference:
                raise ReportingError(f"{cell}@{index} query coverage differs from {reference_name}")
    return sorted(reference or [])


def _validate_identities(runs: dict[str, Sequence[Sequence[dict[str, Any]]]], identities: dict[str, Sequence[dict[str, Any]]] | None) -> dict[str, Any]:
    if identities is None:
        return {}
    expected: dict[str, Any] | None = None
    required = ("corpus_id", "dataset_sha", "generation", "judge")
    for cell, repetitions in runs.items():
        values = identities.get(cell, [])
        if len(values) != len(repetitions):
            raise ReportingError(f"identity count differs for {cell}")
        for identity in values:
            if any(not identity.get(key) for key in required):
                raise ReportingError(f"incomplete end-to-end identities for {cell}")
            selected = {key: identity[key] for key in required}
            if expected is None:
                expected = selected
            elif selected != expected:
                raise ReportingError(f"mixed corpus/generation/judge identities for {cell}")
    return expected or {}


def _per_query(
    runs: Sequence[Sequence[dict[str, Any]]], metric: str, schema: RecordSchema
) -> dict[str, float]:
    grouped: dict[str, list[float]] = {}
    for run in runs:
        for record in run:
            grouped.setdefault(str(record[schema.id_field]), []).append(
                query_metrics(record, schema)[metric]
            )
    return {qid: mean(values) for qid, values in grouped.items()}


def _paired_comparison(
    candidate: Sequence[Sequence[dict[str, Any]]],
    baseline: Sequence[Sequence[dict[str, Any]]],
    schema: RecordSchema,
) -> dict[str, Any]:
    result = {}
    for metric in query_metrics({}, schema):
        candidate_by_id = _per_query(candidate, metric, schema)
        baseline_by_id = _per_query(baseline, metric, schema)
        ids = sorted(candidate_by_id)
        candidate_values = [candidate_by_id[qid] for qid in ids]
        baseline_values = [baseline_by_id[qid] for qid in ids]
        result[metric] = {
            "mean_delta": mean([a - b for a, b in zip(candidate_values, baseline_values)]),
            "ci": list(paired_bootstrap_ci(candidate_values, baseline_values, seed=f"{DEFAULT_BOOTSTRAP_SEED}:{metric}")),
            "queries": len(ids),
        }
    return result


def build_end_to_end_report(
    *,
    runs: dict[str, Sequence[Sequence[dict[str, Any]]]],
    schema: RecordSchema,
    baseline_cell: str = "S",
    identities: dict[str, Sequence[dict[str, Any]]] | None = None,
) -> dict[str, Any]:
    if baseline_cell not in runs:
        raise ReportingError(f"baseline cell {baseline_cell!r} is missing")
    for cell, repetitions in runs.items():
        if cell not in ABLATION_MATRIX:
            raise ReportingError(f"report cell {cell!r} is not in the ablation matrix")
        if len(repetitions) < MIN_REPETITIONS:
            raise ReportingError(f"{cell} requires at least {MIN_REPETITIONS} repetitions")
    coverage = assert_identical_query_coverage(runs, schema)
    identity = _validate_identities(runs, identities)
    safety_measured = all(
        "safety_violations" in record or "safety_violation" in record
        for repetitions in runs.values() for run in repetitions for record in run
    )
    report: dict[str, Any] = {
        "schema": schema.report_schema,
        "baseline_cell": baseline_cell,
        "query_ids": coverage,
        "identities": identity,
        "safety_measured": safety_measured,
        "cells": {},
    }
    baseline = runs[baseline_cell]
    for cell, repetitions in runs.items():
        flat = [record for run in repetitions for record in run]
        first = repetitions[0]
        delta = category_deltas(first, baseline[0], schema)
        cell_report: dict[str, Any] = {
            "request": ablation_request(cell),
            "aggregate": aggregate_repetitions(repetitions, schema),
            "paired_vs_baseline": _paired_comparison(repetitions, baseline, schema),
            "graph_promotion": graph_promotion_report(flat, schema),
            "graph_families": graph_family_reports(flat, schema),
            "evidence_buckets": evidence_buckets(first, schema),
        }
        for key in schema.delta_keys:
            cell_report[key] = delta
        report["cells"][cell] = cell_report
    return report


def stage_reports(records: Sequence[dict[str, Any]], schema: RecordSchema) -> dict[str, Any]:
    total = sum(float((record.get("extraction") or {}).get("gold_total", 0) or 0) for record in records)
    indexed = sum(float((record.get("extraction") or {}).get("gold_indexed", 0) or 0) for record in records)
    metrics = run_metrics(records, schema)
    counts = [len(_ranked_items(record)) for record in records]
    return {
        "extraction": {"gold_total": total, "gold_indexed": indexed, "indexed_coverage": indexed / total if total else 0.0},
        "consolidation": {"memory_per_question": mean(counts)},
        "indexing": {"eligible_coverage": indexed / total if total else 0.0},
        "recall": {"mean_recall": metrics["recall"], "recall_at_1": metrics["recall_at_1"]},
        "ranking": {"mean_ndcg": metrics["ranking"], "mrr": metrics["mrr"]},
        "selection": {"duplicate_rate": metrics["duplicate_rate"], "context_items": mean(counts)},
        "end_to_end": {**metrics, "graph_promotion": graph_promotion_report(records, schema), "evidence_buckets": evidence_buckets(records, schema)},
    }


def promotion_gates(
    report: dict[str, Any], schema: RecordSchema, *, baseline_cell: str | None = None
) -> dict[str, Any]:
    baseline_cell = baseline_cell or report.get("baseline_cell", "S")
    graph_cells = [report["cells"][cell] for cell in ("SKD", "SKP", "SKPM") if cell in report.get("cells", {})]
    regressions = []
    mrr_regressions = []
    for entry in graph_cells:
        paired = entry["paired_vs_baseline"]
        regressions.append(max(0.0, -paired["recall_at_1"]["mean_delta"]))
        mrr_regressions.append(max(0.0, -paired["mrr"]["mean_delta"]))
    precision = max((entry["graph_promotion"]["precision"] for entry in graph_cells), default=0.0)
    baseline_duplicate = report["cells"][baseline_cell]["aggregate"]["metrics"]["duplicate_rate"]["mean"]
    candidate = report.get("cells", {}).get("SKPM")
    candidate_duplicate = candidate["aggregate"]["metrics"]["duplicate_rate"]["mean"] if candidate else baseline_duplicate
    reduction = (baseline_duplicate - candidate_duplicate) / baseline_duplicate if baseline_duplicate else 0.0
    deltas = [
        delta
        for entry in graph_cells
        for delta in entry.get(schema.gain_source_key, {}).values()
    ]
    intended_gain = max(deltas, default=0.0)
    aggregate_delta = max((entry["paired_vs_baseline"]["recall"]["mean_delta"] for entry in graph_cells), default=0.0)
    baseline_safety = report["cells"][baseline_cell]["aggregate"]["metrics"]["safety_violations"]["mean"]
    candidate_safety = max((entry["aggregate"]["metrics"]["safety_violations"]["mean"] for entry in graph_cells), default=baseline_safety)
    safety_measured = bool(report.get("safety_measured"))
    result = {
        "graph_recall_regression": max(regressions, default=0.0),
        "graph_mrr_regression": max(mrr_regressions, default=0.0),
        "graph_recall_within_limit": max(regressions, default=0.0) <= GRAPH_RECALL_REGRESSION_LIMIT,
        "graph_mrr_within_limit": max(mrr_regressions, default=0.0) <= GRAPH_RECALL_REGRESSION_LIMIT,
        "graph_promotion_precision": precision,
        "graph_promotion_precision_passes": precision >= GRAPH_PROMOTION_PRECISION_MINIMUM,
        schema.gain_key: intended_gain,
        "aggregate_delta": aggregate_delta,
        "intended_gain_without_larger_harm": intended_gain > 0 and aggregate_delta >= -intended_gain,
        "duplicate_rate_reduction": reduction,
        "duplicate_rate_reduction_passes": reduction >= DUPLICATE_RATE_REDUCTION_MINIMUM,
        "safety_regression": candidate_safety - baseline_safety,
        "safety_measured": safety_measured,
        "safety_passes": safety_measured and candidate_safety <= baseline_safety,
    }
    failures = []
    if not result["graph_recall_within_limit"]:
        failures.append("graph_recall_regression")
    if not result["graph_mrr_within_limit"]:
        failures.append("graph_mrr_regression")
    if not result["graph_promotion_precision_passes"]:
        failures.append("graph_promotion_precision")
    if not result["intended_gain_without_larger_harm"]:
        failures.append("intended_gain_without_larger_harm")
    if not result["duplicate_rate_reduction_passes"]:
        failures.append("duplicate_rate_reduction")
    if not safety_measured:
        failures.append("safety_not_measured")
    elif not result["safety_passes"]:
        failures.append("safety_regression")
    result["failures"] = failures
    result["passes"] = not failures
    return result
