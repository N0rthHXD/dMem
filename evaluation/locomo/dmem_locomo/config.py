"""Evaluation configuration loading from TOML.

LoCoMo defaults: answer cutoff 25, cutoffs ``[1, 3, 5, 10, 25, 50]``, and
graph retrieval enabled.
"""

from __future__ import annotations

from dmem_eval.config import (
    AnswerConfig,
    DaemonConfig,
    EvalConfig,
    IngestionConfig,
    JudgeConfig,
    RetrievalConfig,
    resolve_api_key,
    resolve_socket,
    load_config as _load_config,
)
from .runner import DEFAULT_CUTOFFS

RETRIEVAL_MODES = ("keyword", "semantic", "graph", "hybrid")
ANSWER_CUTOFF_DEFAULT = 25

__all__ = [
    "DEFAULT_CUTOFFS",
    "DaemonConfig",
    "IngestionConfig",
    "RetrievalConfig",
    "AnswerConfig",
    "JudgeConfig",
    "EvalConfig",
    "load_config",
    "resolve_api_key",
    "resolve_socket",
]


def load_config(path):
    """Load a TOML config file. Returns LoCoMo defaults if path is None/empty."""
    return _load_config(
        path,
        default_cutoffs=DEFAULT_CUTOFFS,
        answer_cutoff=ANSWER_CUTOFF_DEFAULT,
        retrieval_modes=RETRIEVAL_MODES,
    )
