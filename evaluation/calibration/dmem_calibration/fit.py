"""Deterministic weight fitting and paired confidence intervals."""

from __future__ import annotations

import math
import random
from dataclasses import dataclass, field
from typing import Dict, Iterable, List, Sequence, Tuple

from .space import default_weights
from .splits import fit_examples

_FEATURE_KEYS = (
    "semantic",
    "keyword",
    "exact",
    "temporal",
    "quality",
    "agreement",
    "graph",
    "feedback",
    "duplicate_risk",
    "overbreadth",
    "stale_penalty",
)


@dataclass
class QueryExample:
    """One graded candidate in one benchmark query."""

    query_id: str
    benchmark: str
    query_type: str
    features: Dict[str, float]
    label: float
    group_id: str = ""

    def group(self) -> str:
        return self.group_id or self.query_id


def rank_score(weights: Dict[str, float], features: Dict[str, float]) -> float:
    """Mirrors the deterministic Go ranker's bounded feature formula."""
    primary = (
        weights["ranking.keyword_weight"] * features.get("keyword", 0.0)
        + weights["ranking.exact_weight"] * features.get("exact", 0.0)
        + weights["ranking.temporal_weight"] * features.get("temporal", 0.0)
        + weights["ranking.quality_weight"] * features.get("quality", 0.0)
    )
    # semantic is always the backbone; a fixed unit weight keeps it dominant.
    primary += features.get("semantic", 0.0)
    gate = features.get("graph", 0.0) >= weights["graph.gate_floor"]
    boost = 0.0
    if gate:
        boost = min(0.10 * primary, weights["graph.boost_cap"] * features.get("graph", 0.0))
    penalty = (
        weights["penalty.duplicate"] * features.get("duplicate_risk", 0.0)
        + weights["penalty.overbreadth"] * features.get("overbreadth", 0.0)
        + weights["penalty.stale"] * features.get("stale_penalty", 0.0)
    )
    feedback = weights["feedback.global_weight"] * features.get("feedback", 0.0)
    return max(0.0, primary + boost + feedback - penalty)


def _dcg(gains: Sequence[float], k: int) -> float:
    total = 0.0
    for index, gain in enumerate(gains[:k]):
        total += gain / math.log2(index + 2)
    return total


def _ndcg(group: List[QueryExample], weights: Dict[str, float], k: int = 10) -> float:
    ranked = sorted(
        group,
        key=lambda example: (-rank_score(weights, example.features), example.query_id),
    )
    gains = [(2 ** example.label) - 1 for example in ranked]
    ideal = sorted(((2 ** example.label) - 1 for example in group), reverse=True)
    ideal_dcg = _dcg(ideal, k)
    if ideal_dcg <= 0:
        return 1.0
    return _dcg(gains, k) / ideal_dcg


def _group_metric(examples: List[QueryExample], weights: Dict[str, float]) -> float:
    groups: Dict[str, List[QueryExample]] = {}
    for example in examples:
        groups.setdefault(example.group(), []).append(example)
    if not groups:
        return 0.0
    return sum(_ndcg(group, weights) for group in groups.values()) / len(groups)


def per_group_scores(examples: List[QueryExample], weights: Dict[str, float]) -> List[Tuple[str, float]]:
    """Returns the metric for each group, ordered by group id."""
    groups: Dict[str, List[QueryExample]] = {}
    for example in examples:
        groups.setdefault(example.group(), []).append(example)
    return [(group_id, _ndcg(groups[group_id], weights)) for group_id in sorted(groups)]


def fit_weights(
    space: Dict,
    splits: Dict[str, List[QueryExample]],
    passes: int = 2,
) -> Tuple[Dict[str, float], Dict[str, float]]:
    """Deterministic coordinate search over the calibration space.

    Only train and development examples are observable. The holdout split is
    never read here, so benchmark test-set labels cannot influence weights.
    """
    examples = fit_examples(splits)
    weights = default_weights()
    for name, spec in space.items():
        weights.setdefault(name, spec.minimum)
    best = _group_metric(examples, weights)
    current = dict(weights)
    for _ in range(max(1, passes)):
        improved = False
        for name, spec in space.items():
            candidate_value = current[name]
            candidate_score = best
            for value in spec.values():
                trial = dict(current)
                trial[name] = value
                score = _group_metric(examples, trial)
                if score > candidate_score + 1e-12:
                    candidate_score = score
                    candidate_value = value
            if candidate_value != current[name]:
                current[name] = candidate_value
                best = candidate_score
                improved = True
        if not improved:
            break
    return current, {"fit_metric": round(best, 9), "fit_query_groups": len({e.group() for e in examples})}


def paired_bootstrap_interval(
    baseline: Sequence[float],
    candidate: Sequence[float],
    seed: str = "dmem-calibration-bootstrap-v1",
    iterations: int = 2000,
    alpha: float = 0.05,
) -> Tuple[float, float]:
    """Paired percentile bootstrap interval for the mean improvement.

    Inputs must be aligned per query group and are compared as pairs. The RNG is
    seeded from a fixed string so the interval is reproducible.
    """
    if len(baseline) != len(candidate):
        raise ValueError("baseline and candidate must be paired")
    if not baseline:
        return (0.0, 0.0)
    deltas = [candidate[i] - baseline[i] for i in range(len(baseline))]
    rng = random.Random(seed)
    means: List[float] = []
    count = len(deltas)
    for _ in range(max(1, iterations)):
        total = 0.0
        for _ in range(count):
            total += deltas[rng.randrange(count)]
        means.append(total / count)
    means.sort()
    lower_index = max(0, int((alpha / 2) * len(means)) - 1)
    upper_index = min(len(means) - 1, int((1 - alpha / 2) * len(means)))
    return (round(means[lower_index], 9), round(means[upper_index], 9))
