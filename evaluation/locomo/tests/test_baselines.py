"""Tests for versioned baseline fixtures and the baseline record schema."""

import json
from pathlib import Path

import pytest

from dmem_locomo.baseline import (
    BASELINE_SCHEMA_VERSION,
    REQUIRED_IDENTITIES,
    REQUIRED_METRICS,
    RETRIEVAL_MODES,
    BaselineError,
    build_baseline_matrix,
    load_spec,
    new_baseline,
    validate_spec,
)
from dmem_locomo.metrics import METRIC_SCHEMA_VERSION
from dmem_locomo.runner import result_report

REPO_ROOT = Path(__file__).resolve().parents[3]
STORAGE_TESTDATA = REPO_ROOT / "internal" / "storage" / "testdata"
CAPTURE_PATH = REPO_ROOT / "evaluation" / "locomo" / "baselines" / "locomo-phase0-rrf-v3.json"


def test_baseline_spec_is_versioned_and_complete():
    spec = load_spec()
    assert spec["version"] == BASELINE_SCHEMA_VERSION
    assert set(REQUIRED_METRICS).issubset(spec["required_metrics"])
    validate_spec(spec)


def test_evaluation_and_storage_baseline_specs_match():
    storage = json.loads(
        (STORAGE_TESTDATA / "evaluation-baseline-v1.json").read_text(encoding="utf-8")
    )
    assert storage == load_spec()


def test_baseline_spec_metric_schema_matches_metrics_module():
    assert load_spec()["metric_schema"] == METRIC_SCHEMA_VERSION


def test_validate_spec_rejects_missing_metric():
    spec = load_spec()
    del spec["required_metrics"]["recall"]
    with pytest.raises(BaselineError):
        validate_spec(spec)


def test_new_baseline_requires_identities_and_metrics():
    spec = load_spec()
    identities = {name: "x" for name in REQUIRED_IDENTITIES}
    identities["metric_schema"] = METRIC_SCHEMA_VERSION
    metrics = {name: 0.0 for name in REQUIRED_METRICS}
    record = new_baseline(identities=identities, metrics=metrics, spec=spec)
    assert record["schema_version"] == BASELINE_SCHEMA_VERSION
    assert set(record["metrics"]) == set(REQUIRED_METRICS)
    with pytest.raises(BaselineError):
        new_baseline(
            identities={k: v for k, v in identities.items() if k != "corpus_id"},
            metrics=metrics,
            spec=spec,
        )
    with pytest.raises(BaselineError):
        new_baseline(
            identities=identities,
            metrics={k: v for k, v in metrics.items() if k != "latency"},
            spec=spec,
        )


def test_build_baseline_matrix_requires_four_modes_on_one_corpus():
    runs = []
    for mode in RETRIEVAL_MODES:
        runs.append(
            {
                "manifest": {
                    "dataset_sha": "dataset-sha",
                    "dataset_subset": ["conv-1", "conv-2"],
                    "corpus_id": "corpus-1",
                    "retrieval_mode": mode,
                    "identities": {
                        "daemon_build_commit": "deadbeef",
                        "schema_compatibility": "schema-v1",
                        "processor_identity": "processor-v1",
                        "configuration_identity": "config-v1",
                        "semantic_index_version": 3,
                        "graph_index_version": 4,
                        "fts_index_version": 5,
                        "ranking_version": "ranking-weights-v1",
                        "metric_schema": METRIC_SCHEMA_VERSION,
                    },
                },
                "metrics": {name: 0.0 for name in REQUIRED_METRICS},
            }
        )

    matrix = build_baseline_matrix(runs)
    assert set(matrix["runs"]) == set(RETRIEVAL_MODES)
    assert {run["identities"]["corpus_id"] for run in matrix["runs"].values()} == {
        "corpus-1"
    }

    runs[-1]["manifest"]["corpus_id"] = "different-corpus"
    with pytest.raises(BaselineError, match="shared corpus"):
        build_baseline_matrix(runs)


def test_build_baseline_matrix_rejects_missing_or_unknown_identity():
    runs = []
    for mode in RETRIEVAL_MODES:
        runs.append(
            {
                "manifest": {
                    "dataset_sha": "dataset-sha",
                    "dataset_subset": ["conv-1"],
                    "corpus_id": "corpus-1",
                    "retrieval_mode": mode,
                    "identities": {
                        "daemon_build_commit": "deadbeef",
                        "schema_compatibility": "schema-v1",
                        "processor_identity": "processor-v1",
                        "configuration_identity": "config-v1",
                        "semantic_index_version": 3,
                        "graph_index_version": 4,
                        "fts_index_version": 5,
                        "ranking_version": "ranking-weights-v1",
                        "metric_schema": METRIC_SCHEMA_VERSION,
                    },
                },
                "metrics": {name: 0.0 for name in REQUIRED_METRICS},
            }
        )
    runs[0]["manifest"]["identities"]["ranking_version"] = "unknown"

    with pytest.raises(BaselineError, match="ranking_version"):
        build_baseline_matrix(runs)


def test_phase0_baseline_capture_has_four_modes_on_one_immutable_corpus():
    capture = json.loads(CAPTURE_PATH.read_text(encoding="utf-8"))

    assert capture["schema_version"] == BASELINE_SCHEMA_VERSION
    assert capture["metric_schema"] == METRIC_SCHEMA_VERSION
    assert tuple(capture["runs"]) == RETRIEVAL_MODES
    shared = {
        (
            run["identities"]["dataset_sha"],
            tuple(run["identities"]["dataset_subset"]),
            run["identities"]["corpus_id"],
        )
        for run in capture["runs"].values()
    }
    assert len(shared) == 1
    for mode, run in capture["runs"].items():
        assert run["identities"]["retrieval_mode"] == mode
        assert all(value not in (None, "", "unknown", 0, []) for value in run["identities"].values())
        assert all(isinstance(value, (int, float)) for value in run["metrics"].values())

    readme = (REPO_ROOT / "evaluation" / "locomo" / "README.md").read_text(
        encoding="utf-8"
    )
    assert "exact baseline run identities were not captured" not in readme


def test_retrieval_report_fixture_lists_required_fields():
    fixture = json.loads(
        (STORAGE_TESTDATA / "retrieval-report-v1.json").read_text(encoding="utf-8")
    )
    assert fixture["version"] == "retrieval-report-v1"
    assert fixture["corpus_version"] == "retrieval-corpus-v1"
    assert set(fixture["required_result_fields"]) == {
        "primary_score",
        "raw_channel_scores",
        "matched_fields",
        "graph_score",
        "signal_family",
        "duplicate_cluster",
        "selection_state",
    }


def test_result_report_emits_every_required_field():
    fixture = json.loads(
        (STORAGE_TESTDATA / "retrieval-report-v1.json").read_text(encoding="utf-8")
    )
    result = {
        "matched_channels": ["keyword", "semantic"],
        "keyword_relevance": 1.5,
        "semantic_relevance": 0.8,
        "selection_reason": "primary",
        "graph_signals": [
            {"kind": "entity_fact", "fact_confidence": 0.9, "entity_confidence": 0.8}
        ],
    }
    report = result_report(result, ["D1:1", "D1:2"])
    for field in fixture["required_result_fields"]:
        assert field in report, field
    assert report["signal_family"] == ["entity_fact", "keyword", "semantic"]
    assert report["raw_channel_scores"] == {"keyword": 1.5, "semantic": 0.8}
    assert report["duplicate_cluster"] == "D1:1"
    assert report["selection_state"] == "primary"
    assert report["graph_score"] == pytest.approx(0.72)


def test_result_report_marks_unmetered_fields_explicitly():
    report = result_report({"matched_channels": []}, [])
    assert report["primary_score"] is None
    assert report["matched_fields"] == []
    assert report["graph_score"] is None
    assert report["duplicate_cluster"] is None
