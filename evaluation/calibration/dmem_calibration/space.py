"""The versioned deterministic calibration parameter space."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Dict, List

CALIBRATION_REPORT_VERSION = "calibration-report-v1"


@dataclass(frozen=True)
class ParameterSpec:
    """A single bounded, deterministically stepped calibration parameter."""

    name: str
    minimum: float
    maximum: float
    step: float

    def values(self) -> List[float]:
        count = int(round((self.maximum - self.minimum) / self.step))
        return [round(self.minimum + i * self.step, 6) for i in range(count + 1)]


def default_parameter_space() -> Dict[str, ParameterSpec]:
    """Returns the full calibration space in a stable parameter order.

    The space covers semantic thresholds, keyword weight, temporal features,
    graph gates and boosts, penalties, context utility, and bounded feedback
    weights.
    """
    specs = [
        ParameterSpec("semantic.absolute_floor", 0.10, 0.60, 0.05),
        ParameterSpec("semantic.top_margin", 0.00, 0.30, 0.05),
        ParameterSpec("ranking.keyword_weight", 0.00, 1.00, 0.05),
        ParameterSpec("ranking.exact_weight", 0.00, 0.40, 0.05),
        ParameterSpec("ranking.temporal_weight", 0.00, 0.50, 0.05),
        ParameterSpec("ranking.quality_weight", 0.00, 0.30, 0.05),
        ParameterSpec("graph.gate_floor", 0.00, 0.50, 0.05),
        ParameterSpec("graph.boost_cap", 0.00, 0.20, 0.02),
        ParameterSpec("penalty.duplicate", 0.00, 0.40, 0.05),
        ParameterSpec("penalty.overbreadth", 0.00, 0.30, 0.05),
        ParameterSpec("penalty.stale", 0.00, 0.30, 0.05),
        ParameterSpec("context.relevance", 0.50, 1.50, 0.25),
        ParameterSpec("context.redundancy", 0.50, 1.50, 0.25),
        ParameterSpec("context.minimum_utility", 0.00, 0.50, 0.05),
        ParameterSpec("feedback.global_weight", 0.00, 0.002, 0.0005),
        ParameterSpec("feedback.query_weight", 0.00, 0.002, 0.0005),
    ]
    return {spec.name: spec for spec in specs}


def default_weights() -> Dict[str, float]:
    """Starting weights mirroring the daemon deterministic defaults."""
    return {
        "semantic.absolute_floor": 0.35,
        "semantic.top_margin": 0.08,
        "ranking.keyword_weight": 0.35,
        "ranking.exact_weight": 0.15,
        "ranking.temporal_weight": 0.10,
        "ranking.quality_weight": 0.10,
        "graph.gate_floor": 0.20,
        "graph.boost_cap": 0.08,
        "penalty.duplicate": 0.15,
        "penalty.overbreadth": 0.10,
        "penalty.stale": 0.10,
        "context.relevance": 1.00,
        "context.redundancy": 0.90,
        "context.minimum_utility": 0.20,
        "feedback.global_weight": 0.001,
        "feedback.query_weight": 0.0005,
    }
