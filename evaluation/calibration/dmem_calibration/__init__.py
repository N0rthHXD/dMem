"""Deterministic calibration tooling for dMem retrieval weights.

This package is evaluation-only. It never writes runtime feedback, never reads
benchmark test-set (holdout) labels while fitting, and emits versioned reports
that a promotion step can gate on paired confidence intervals.

Pipeline:
    parameter space -> train/development/holdout splits -> deterministic fit
    -> cross-benchmark and per-query-type report -> paired CI promotion gate
"""

from .space import (
    CALIBRATION_REPORT_VERSION,
    ParameterSpec,
    default_parameter_space,
)
from .splits import (
    SPLIT_DEVELOPMENT,
    SPLIT_HOLDOUT,
    SPLIT_TRAIN,
    assign_split,
    split_examples,
)
from .fit import (
    QueryExample,
    fit_weights,
    paired_bootstrap_interval,
    rank_score,
)
from .report import build_report, promotion_decision
from .rerank_compare import RerankWindowResult, compare_reranking, promotion_allowed

__all__ = [
    "CALIBRATION_REPORT_VERSION",
    "ParameterSpec",
    "default_parameter_space",
    "SPLIT_TRAIN",
    "SPLIT_DEVELOPMENT",
    "SPLIT_HOLDOUT",
    "assign_split",
    "split_examples",
    "QueryExample",
    "fit_weights",
    "paired_bootstrap_interval",
    "rank_score",
    "build_report",
    "promotion_decision",
    "RerankWindowResult",
    "compare_reranking",
    "promotion_allowed",
]
