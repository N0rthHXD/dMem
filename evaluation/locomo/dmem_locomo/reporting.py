"""Reproducible LoCoMo ablation reporting and promotion gates.

Binds the shared :mod:`dmem_eval.reporting` engine to the LoCoMo record layout:
``qa_id`` identity, ``category`` grouping, ``turn_metrics`` layer, ``coverage``
recall, and memory-id gold labels.
"""

from __future__ import annotations

from typing import Any, Sequence

from dmem_eval.reporting import (
    ABLATION_MATRIX,
    DEFAULT_BOOTSTRAP_ITERATIONS,
    DEFAULT_BOOTSTRAP_SEED,
    DUPLICATE_RATE_REDUCTION_MINIMUM,
    GRAPH_FAMILY_ABLATIONS,
    GRAPH_PROMOTION_PRECISION_MINIMUM,
    GRAPH_RECALL_REGRESSION_LIMIT,
    GRAPH_SIGNAL_FAMILIES,
    MIN_REPETITIONS,
    RecordSchema,
    ReportingError,
    ablation_request,
    matrix_cells,
    mean,
    paired_bootstrap_ci,
    stddev,
)
from dmem_eval import reporting as _reporting

LOCOMO_SCHEMA = RecordSchema(
    id_field="qa_id",
    category_field="category",
    metrics_layer="turn_metrics",
    recall_metric="coverage",
    recall_at_1_metrics=("recall_any", "coverage"),
    mrr_metrics=("mrr_at_k",),
    evidence_field="gold_evidence",
    report_schema="locomo-ablation-report-v1",
    gold_memory_field="gold_memory_ids",
    gold_session_field=None,
    item_session_field=None,
    delta_keys=("category_delta",),
    gain_source_key="category_delta",
    gain_key="intended_category_gain",
)

__all__ = [
    "ABLATION_MATRIX",
    "DEFAULT_BOOTSTRAP_ITERATIONS",
    "DEFAULT_BOOTSTRAP_SEED",
    "DUPLICATE_RATE_REDUCTION_MINIMUM",
    "GRAPH_FAMILY_ABLATIONS",
    "GRAPH_PROMOTION_PRECISION_MINIMUM",
    "GRAPH_RECALL_REGRESSION_LIMIT",
    "GRAPH_SIGNAL_FAMILIES",
    "MIN_REPETITIONS",
    "ReportingError",
    "ablation_request",
    "aggregate_repetitions",
    "assert_identical_query_coverage",
    "build_end_to_end_report",
    "category_deltas",
    "evidence_buckets",
    "graph_family_reports",
    "graph_promotion_report",
    "matrix_cells",
    "mean",
    "paired_bootstrap_ci",
    "promotion_gates",
    "query_metrics",
    "run_metrics",
    "stage_reports",
    "stddev",
]


def query_metrics(record: dict[str, Any]) -> dict[str, float]:
    return _reporting.query_metrics(record, LOCOMO_SCHEMA)


def run_metrics(records: Sequence[dict[str, Any]]) -> dict[str, float]:
    return _reporting.run_metrics(records, LOCOMO_SCHEMA)


def aggregate_repetitions(runs: Sequence[Sequence[dict[str, Any]]]) -> dict[str, Any]:
    return _reporting.aggregate_repetitions(runs, LOCOMO_SCHEMA)


def graph_promotion_report(records: Sequence[dict[str, Any]]) -> dict[str, float]:
    return _reporting.graph_promotion_report(records, LOCOMO_SCHEMA)


def graph_family_reports(records: Sequence[dict[str, Any]]) -> dict[str, dict[str, float]]:
    return _reporting.graph_family_reports(records, LOCOMO_SCHEMA)


def category_deltas(
    candidate: Sequence[dict[str, Any]],
    baseline: Sequence[dict[str, Any]],
    *,
    layer: str = "turn_metrics",
    name: str | None = None,
) -> dict[str, float]:
    return _reporting.category_deltas(candidate, baseline, LOCOMO_SCHEMA, name=name)


def evidence_buckets(records: Sequence[dict[str, Any]]) -> dict[str, dict[str, float]]:
    return _reporting.evidence_buckets(records, LOCOMO_SCHEMA)


def assert_identical_query_coverage(
    runs: dict[str, Sequence[Sequence[dict[str, Any]]]]
) -> list[str]:
    return _reporting.assert_identical_query_coverage(runs, LOCOMO_SCHEMA)


def build_end_to_end_report(
    *,
    runs: dict[str, Sequence[Sequence[dict[str, Any]]]],
    baseline_cell: str = "S",
    identities: dict[str, Sequence[dict[str, Any]]] | None = None,
) -> dict[str, Any]:
    return _reporting.build_end_to_end_report(
        runs=runs,
        schema=LOCOMO_SCHEMA,
        baseline_cell=baseline_cell,
        identities=identities,
    )


def stage_reports(records: Sequence[dict[str, Any]]) -> dict[str, Any]:
    return _reporting.stage_reports(records, LOCOMO_SCHEMA)


def promotion_gates(
    report: dict[str, Any], *, baseline_cell: str | None = None
) -> dict[str, Any]:
    return _reporting.promotion_gates(
        report, LOCOMO_SCHEMA, baseline_cell=baseline_cell
    )
