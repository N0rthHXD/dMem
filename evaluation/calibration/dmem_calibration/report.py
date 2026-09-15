"""Versioned calibration reports, cross-benchmark comparisons, and gates."""

from __future__ import annotations

from typing import Dict, List

from .fit import QueryExample, paired_bootstrap_interval, per_group_scores
from .space import CALIBRATION_REPORT_VERSION, default_weights
from .splits import SPLIT_DEVELOPMENT, SPLIT_HOLDOUT, SPLIT_TRAIN

# A promotion is only credible when the paired improvement interval excludes
# zero and no benchmark regresses beyond this tolerance.
DEFAULT_PROMOTION = {
    "minimum_ci_lower": 0.0,
    "maximum_benchmark_regression": 0.01,
    "maximum_query_type_regression": 0.02,
}


def _group_dimensions(examples: List[QueryExample]) -> Dict[str, Dict[str, str]]:
    dimensions: Dict[str, Dict[str, str]] = {}
    for example in examples:
        dimensions[example.group()] = {
            "benchmark": example.benchmark,
            "query_type": example.query_type,
        }
    return dimensions


def _scores_by(examples: List[QueryExample], weights: Dict[str, float], dimension: str) -> Dict[str, float]:
    dimensions = _group_dimensions(examples)
    totals: Dict[str, float] = {}
    counts: Dict[str, int] = {}
    for group_id, score in per_group_scores(examples, weights):
        value = dimensions[group_id][dimension]
        totals[value] = totals.get(value, 0.0) + score
        counts[value] = counts.get(value, 0) + 1
    return {key: round(totals[key] / counts[key], 9) for key in sorted(totals)}


def _split_report(examples: List[QueryExample], weights: Dict[str, float], baseline: Dict[str, float]) -> Dict:
    candidate_scores = dict(per_group_scores(examples, weights))
    baseline_scores = dict(per_group_scores(examples, baseline))
    groups = sorted(candidate_scores)
    paired_baseline = [baseline_scores[group] for group in groups]
    paired_candidate = [candidate_scores[group] for group in groups]
    lower, upper = paired_bootstrap_interval(paired_baseline, paired_candidate)
    return {
        "groups": len(groups),
        "mean_metric": round(sum(paired_candidate) / len(groups), 9) if groups else 0.0,
        "baseline_metric": round(sum(paired_baseline) / len(groups), 9) if groups else 0.0,
        "mean_improvement": round(
            (sum(paired_candidate) - sum(paired_baseline)) / len(groups), 9
        )
        if groups
        else 0.0,
        "paired_ci": [lower, upper],
        "by_benchmark": _scores_by(examples, weights, "benchmark"),
        "baseline_by_benchmark": _scores_by(examples, baseline, "benchmark"),
        "by_query_type": _scores_by(examples, weights, "query_type"),
        "baseline_by_query_type": _scores_by(examples, baseline, "query_type"),
    }


def build_report(space: Dict, splits: Dict[str, List[QueryExample]], weights: Dict[str, float], fit_summary: Dict) -> Dict:
    """Builds a deterministic, versioned calibration report.

    Holdout metrics are reported but never fed back into fitting. The report
    records the holdout paired interval so promotion is gated on it.
    """
    baseline = default_weights()
    report = {
        "report_version": CALIBRATION_REPORT_VERSION,
        "parameter_space": {
            name: {"minimum": spec.minimum, "maximum": spec.maximum, "step": spec.step}
            for name, spec in space.items()
        },
        "selected_weights": dict(sorted(weights.items())),
        "fit": fit_summary,
        "splits": {
            "train": _split_report(splits[SPLIT_TRAIN], weights, baseline),
            "development": _split_report(splits[SPLIT_DEVELOPMENT], weights, baseline),
            "holdout": _split_report(splits[SPLIT_HOLDOUT], weights, baseline),
        },
        "boundaries": {
            "fit_uses": ["train", "development"],
            "selection_uses": ["development"],
            "promotion_uses": ["holdout"],
            "runtime_feedback": "not_written",
        },
    }
    report["promotion"] = promotion_decision(report)
    return report


def promotion_decision(report: Dict, thresholds: Dict = None) -> Dict:
    thresholds = dict(DEFAULT_PROMOTION, **(thresholds or {}))
    holdout = report["splits"]["holdout"]
    ci_lower, ci_upper = holdout["paired_ci"]
    benchmark_regressions = {
        benchmark: round(holdout["by_benchmark"][benchmark] - holdout["baseline_by_benchmark"][benchmark], 9)
        for benchmark in holdout["by_benchmark"]
    }
    query_type_regressions = {
        query_type: round(holdout["by_query_type"][query_type] - holdout["baseline_by_query_type"][query_type], 9)
        for query_type in holdout["by_query_type"]
    }
    worst_benchmark = min(benchmark_regressions.values()) if benchmark_regressions else 0.0
    worst_query_type = min(query_type_regressions.values()) if query_type_regressions else 0.0
    reasons: List[str] = []
    if holdout["groups"] == 0:
        reasons.append("no_holdout_groups")
    if ci_lower <= thresholds["minimum_ci_lower"]:
        reasons.append("paired_ci_does_not_exclude_zero")
    if worst_benchmark < -thresholds["maximum_benchmark_regression"]:
        reasons.append("benchmark_regression")
    if worst_query_type < -thresholds["maximum_query_type_regression"]:
        reasons.append("query_type_regression")
    return {
        "promoted": not reasons,
        "reasons": reasons,
        "thresholds": thresholds,
        "holdout_ci": [ci_lower, ci_upper],
        "benchmark_deltas": benchmark_regressions,
        "query_type_deltas": query_type_regressions,
    }
