"""Deterministic train/development/holdout boundaries.

The holdout split is the benchmark test set. Its labels must never be used for
weight fitting or model selection; only the train and development splits are.
"""

from __future__ import annotations

import hashlib
from typing import Dict, Iterable, List

SPLIT_TRAIN = "train"
SPLIT_DEVELOPMENT = "development"
SPLIT_HOLDOUT = "holdout"

_DEFAULT_SEED = "dmem-calibration-split-v1"
# 60% train, 20% development, 20% holdout.
_BUCKETS = [(60, SPLIT_TRAIN), (80, SPLIT_DEVELOPMENT), (100, SPLIT_HOLDOUT)]


def assign_split(query_id: str, seed: str = _DEFAULT_SEED) -> str:
    """Assigns one query to a split deterministically and reproducibly."""
    if not query_id:
        raise ValueError("query_id is required")
    digest = hashlib.sha256(f"{seed}\x00{query_id}".encode("utf-8")).hexdigest()
    bucket = int(digest[:8], 16) % 100
    for upper, name in _BUCKETS:
        if bucket < upper:
            return name
    return SPLIT_HOLDOUT


def split_examples(examples: Iterable, seed: str = _DEFAULT_SEED) -> Dict[str, List]:
    """Groups examples by split without changing their relative order."""
    splits: Dict[str, List] = {SPLIT_TRAIN: [], SPLIT_DEVELOPMENT: [], SPLIT_HOLDOUT: []}
    for example in examples:
        splits[assign_split(example.query_id, seed)].append(example)
    return splits


def assert_disjoint(splits: Dict[str, List]) -> None:
    """Raises when a query appears in more than one split."""
    seen: Dict[str, str] = {}
    for name, examples in splits.items():
        for example in examples:
            previous = seen.get(example.query_id)
            if previous is not None and previous != name:
                raise ValueError(f"query {example.query_id} appears in {previous} and {name}")
            seen[example.query_id] = name


def fit_examples(splits: Dict[str, List]) -> List:
    """Returns only the examples that weight fitting may observe."""
    return list(splits[SPLIT_TRAIN]) + list(splits[SPLIT_DEVELOPMENT])
