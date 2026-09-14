"""Versioned evaluation baselines.

A baseline is an immutable record of one retrieval configuration's measured
metrics together with the dataset, corpus, and algorithm identities that
produced them. Baseline records are never edited in place: a changed algorithm,
corpus, or metric schema produces a new ``schema_version`` file.
"""

from __future__ import annotations

import json
import math
from pathlib import Path
from typing import Any

BASELINE_SCHEMA_VERSION = "evaluation-baseline-v1"
BASELINE_SPEC_PATH = Path(__file__).resolve().parents[1] / "baselines" / "evaluation-baseline-v1.json"
RETRIEVAL_MODES = ("keyword", "semantic", "graph", "hybrid")

REQUIRED_METRICS = (
    "extraction_count",
    "duplicate_density",
    "recall",
    "ranking",
    "graph_promotion_harm",
    "context_utilization",
    "latency",
    "allocation",
    "storage",
    "provider_cost",
)

REQUIRED_IDENTITIES = (
    "dataset_sha",
    "dataset_subset",
    "corpus_id",
    "retrieval_mode",
    "daemon_build_commit",
    "schema_compatibility",
    "processor_identity",
    "configuration_identity",
    "ranking_version",
    "fts_index_version",
    "semantic_index_version",
    "graph_index_version",
    "metric_schema",
)


class BaselineError(ValueError):
    pass


def load_spec(path: str | Path = BASELINE_SPEC_PATH) -> dict[str, Any]:
    return json.loads(Path(path).read_text(encoding="utf-8"))


def validate_spec(spec: dict[str, Any]) -> None:
    if spec.get("version") != BASELINE_SCHEMA_VERSION:
        raise BaselineError(
            f"expected baseline schema {BASELINE_SCHEMA_VERSION!r}, "
            f"got {spec.get('version')!r}"
        )
    metrics = spec.get("required_metrics") or {}
    missing = [name for name in REQUIRED_METRICS if name not in metrics]
    if missing:
        raise BaselineError(f"baseline spec is missing metrics: {', '.join(missing)}")
    for name, meta in metrics.items():
        if meta.get("direction") not in ("higher", "lower", "neutral"):
            raise BaselineError(f"baseline metric {name!r} has invalid direction")
        if not meta.get("unit"):
            raise BaselineError(f"baseline metric {name!r} has no unit")
    identities = set(spec.get("required_identities") or [])
    missing_ids = [name for name in REQUIRED_IDENTITIES if name not in identities]
    if missing_ids:
        raise BaselineError(
            f"baseline spec is missing identities: {', '.join(missing_ids)}"
        )


def new_baseline(
    *,
    identities: dict[str, Any],
    metrics: dict[str, float],
    spec: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """Build a validated baseline record for one retrieval configuration."""
    spec = spec or load_spec()
    validate_spec(spec)
    missing_ids = [name for name in REQUIRED_IDENTITIES if name not in identities]
    if missing_ids:
        raise BaselineError(
            f"baseline identities missing: {', '.join(missing_ids)}"
        )
    missing_metrics = [name for name in REQUIRED_METRICS if name not in metrics]
    if missing_metrics:
        raise BaselineError(
            f"baseline metrics missing: {', '.join(missing_metrics)}"
        )
    for name in REQUIRED_IDENTITIES:
        value = identities[name]
        if value in (None, "", "unknown", 0, []):
            raise BaselineError(f"baseline identity {name!r} is not immutable: {value!r}")
    for name in REQUIRED_METRICS:
        value = metrics[name]
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise BaselineError(f"baseline metric {name!r} must be numeric")
        if not math.isfinite(float(value)):
            raise BaselineError(f"baseline metric {name!r} must be finite")
    if identities["metric_schema"] != spec.get("metric_schema"):
        raise BaselineError("baseline metric schema identity does not match the spec")
    return {
        "schema_version": spec["version"],
        "metric_schema": spec.get("metric_schema"),
        "identities": {name: identities[name] for name in REQUIRED_IDENTITIES},
        "metrics": {name: metrics[name] for name in REQUIRED_METRICS},
    }


def build_baseline_matrix(
    runs: list[dict[str, Any]],
    *,
    spec: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """Validate and combine the four retrieval modes from one shared corpus."""
    spec = spec or load_spec()
    records: dict[str, dict[str, Any]] = {}
    shared_keys = ("dataset_sha", "dataset_subset", "corpus_id")
    shared: tuple[Any, ...] | None = None
    for run in runs:
        manifest = run.get("manifest") or {}
        mode = manifest.get("retrieval_mode")
        if mode not in RETRIEVAL_MODES:
            raise BaselineError(f"invalid retrieval mode: {mode!r}")
        if mode in records:
            raise BaselineError(f"duplicate retrieval mode: {mode}")
        current_shared = tuple(manifest.get(key) for key in shared_keys)
        if shared is None:
            shared = current_shared
        elif current_shared != shared:
            raise BaselineError("baseline runs do not use one shared corpus")
        identities = dict(manifest.get("identities") or {})
        identities.update({key: manifest.get(key) for key in shared_keys})
        identities["retrieval_mode"] = mode
        records[mode] = new_baseline(
            identities=identities,
            metrics=run.get("metrics") or {},
            spec=spec,
        )
    missing = [mode for mode in RETRIEVAL_MODES if mode not in records]
    if missing:
        raise BaselineError(f"baseline matrix missing modes: {', '.join(missing)}")
    return {
        "schema_version": spec["version"],
        "metric_schema": spec["metric_schema"],
        "runs": {mode: records[mode] for mode in RETRIEVAL_MODES},
    }
