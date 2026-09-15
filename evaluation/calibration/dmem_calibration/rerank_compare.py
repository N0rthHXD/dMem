"""Paired comparison of learned and deterministic reranking (evaluation only)."""

from __future__ import annotations

import statistics
from dataclasses import dataclass
from typing import Dict, List, Sequence

DEFAULT_RERANK_THRESHOLDS = {
    "minimum_mean_rank_improvement": 0.02,
    "maximum_latency_ratio": 1.5,
    "maximum_variance_increase": 0.05,
    "minimum_window_count": 50,
    "degradation_tolerance": 0.0,
}


@dataclass
class RerankWindowResult:
    """One candidate window compared under both rerankers."""

    deterministic_order: Sequence[str]
    learned_order: Sequence[str]
    relevant: Sequence[str]
    deterministic_latency_ms: float
    learned_latency_ms: float


def _reciprocal_rank(order: Sequence[str], relevant: Sequence[str]) -> float:
    relevant_set = set(relevant)
    for index, key in enumerate(order):
        if key in relevant_set:
            return 1.0 / (index + 1)
    return 0.0


def compare_reranking(windows: List[RerankWindowResult]) -> Dict:
    """Computes paired evidence for the learned reranker over identical windows."""
    if not windows:
        return {
            "window_count": 0,
            "mean_rank_improvement": 0.0,
            "latency_ratio": 0.0,
            "variance_increase": 0.0,
            "degradation_count": 0,
        }
    deterministic_scores = [_reciprocal_rank(w.deterministic_order, w.relevant) for w in windows]
    learned_scores = [_reciprocal_rank(w.learned_order, w.relevant) for w in windows]
    deltas = [learned_scores[i] - deterministic_scores[i] for i in range(len(windows))]
    deterministic_latency = statistics.mean(w.deterministic_latency_ms for w in windows)
    learned_latency = statistics.mean(w.learned_latency_ms for w in windows)
    deterministic_variance = statistics.pvariance(deterministic_scores) if len(windows) > 1 else 0.0
    learned_variance = statistics.pvariance(learned_scores) if len(windows) > 1 else 0.0
    degradation_count = sum(1 for delta in deltas if delta < 0)
    return {
        "window_count": len(windows),
        "mean_rank_improvement": round(statistics.mean(deltas), 9),
        "latency_ratio": round(learned_latency / deterministic_latency, 6) if deterministic_latency > 0 else 0.0,
        "variance_increase": round(learned_variance - deterministic_variance, 9),
        "degradation_count": degradation_count,
    }


def promotion_allowed(evidence: Dict, thresholds: Dict = None) -> Dict:
    thresholds = dict(DEFAULT_RERANK_THRESHOLDS, **(thresholds or {}))
    reasons: List[str] = []
    if evidence["window_count"] < thresholds["minimum_window_count"]:
        reasons.append("insufficient_windows")
    if evidence["degradation_count"] > thresholds["degradation_tolerance"]:
        reasons.append("degradation")
    if evidence["mean_rank_improvement"] < thresholds["minimum_mean_rank_improvement"]:
        reasons.append("insufficient_gain")
    if thresholds["maximum_latency_ratio"] > 0 and evidence["latency_ratio"] > thresholds["maximum_latency_ratio"]:
        reasons.append("latency")
    if thresholds["maximum_variance_increase"] >= 0 and evidence["variance_increase"] > thresholds["maximum_variance_increase"]:
        reasons.append("variance")
    return {"promoted": not reasons, "reasons": reasons, "thresholds": thresholds}
