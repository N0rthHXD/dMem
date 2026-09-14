"""Checkpoint and manifest identity tests.

Checkpoints must reject non-resume runs over non-empty results, persist all
effective identities, and refuse resume/summarize when identities are
incompatible. No legacy or partial-compatibility path is allowed.
"""

import json

import pytest

from dmem_locomo.checkpoint import Checkpoint
from dmem_locomo.runner import capture_identities


def test_is_compatible_accepts_matching_identity(tmp_path):
    cp = Checkpoint(tmp_path)
    cp.write_manifest({"dataset_sha": "sha", "run_id": "run", "metric_schema": "m2"})
    ok, _msg = cp.is_compatible(
        {"dataset_sha": "sha", "run_id": "run", "metric_schema": "m2"}
    )
    assert ok is True


def test_is_compatible_rejects_metric_schema_mismatch(tmp_path):
    cp = Checkpoint(tmp_path)
    cp.write_manifest({"dataset_sha": "sha", "run_id": "run", "metric_schema": "m1"})
    ok, msg = cp.is_compatible(
        {"dataset_sha": "sha", "run_id": "run", "metric_schema": "m2"}
    )
    assert ok is False
    assert "metric_schema" in msg


def test_is_compatible_rejects_key_missing_from_manifest(tmp_path):
    cp = Checkpoint(tmp_path)
    cp.write_manifest({"dataset_sha": "sha"})
    ok, _msg = cp.is_compatible({"dataset_sha": "sha", "metric_schema": "m2"})
    assert ok is False


def test_has_results_is_false_for_empty_checkpoint(tmp_path):
    cp = Checkpoint(tmp_path)
    assert cp.has_results() is False


def test_has_results_detects_retrieval_records(tmp_path):
    cp = Checkpoint(tmp_path)
    cp.append_retrieval({"qa_id": "q1"})
    assert cp.has_results() is True


def test_phase_compatibility_raises_on_model_mismatch(tmp_path):
    cp = Checkpoint(tmp_path)
    cp.write_manifest({"answer_model": "gpt-a"})
    with pytest.raises(ValueError):
        cp.assert_phase_compatible({"answer_model": "gpt-b"})


def test_phase_compatibility_allows_unset_manifest_value(tmp_path):
    cp = Checkpoint(tmp_path)
    cp.write_manifest({})
    cp.assert_phase_compatible({"answer_model": "gpt-b", "generation_prompt": "v1"})


def test_phase_compatibility_raises_on_prompt_mismatch(tmp_path):
    cp = Checkpoint(tmp_path)
    cp.write_manifest({"generation_prompt": "v0"})
    with pytest.raises(ValueError):
        cp.assert_phase_compatible({"generation_prompt": "v1"})


def test_manifest_roundtrips_identity_block(tmp_path):
    cp = Checkpoint(tmp_path)
    identities = {
        "metric_schema": "m2",
        "daemon_build_commit": "abc",
        "schema_compatibility": "compatible",
        "processor_identity": "proc-v1",
        "semantic_index_version": 3,
        "graph_index_version": 4,
        "fts_index_version": 2,
        "ranking_version": "rrf-v3",
        "context_selection": "none",
    }
    cp.write_manifest({"identities": identities})
    assert cp.read_manifest()["identities"] == identities


def test_partial_final_line_is_ignored_on_read(tmp_path):
    cp = Checkpoint(tmp_path)
    cp.retrieval_path.write_text(
        json.dumps({"qa_id": "complete"}) + "\n" + '{"qa_id": "interrupted"',
        encoding="utf-8",
    )
    assert cp.load_retrieval_qids() == {"complete"}


def test_capture_identities_includes_all_required_identities():
    health = {
        "build_version": "1.2.3",
        "build_commit": "deadbeef",
        "schema_compatibility": "compatible",
        "processor_identity": "proc-v1",
        "configuration_identity": "cfg-1",
        "semantic_index_version": 3,
        "graph_index_version": 4,
        "graph_index_state": "ready",
        "fts_index_state": "ready",
    }
    identities = capture_identities(health, {"fts_index_version": 7, "ranking_version": "rrf-v3"})
    for key in (
        "metric_schema",
        "daemon_build_version",
        "daemon_build_commit",
        "schema_compatibility",
        "processor_identity",
        "semantic_index_version",
        "graph_index_version",
        "fts_index_version",
        "ranking_version",
        "context_selection",
    ):
        assert key in identities
    assert identities["daemon_build_commit"] == "deadbeef"
    assert identities["fts_index_version"] == 7
    assert identities["ranking_version"] == "rrf-v3"


def test_capture_identities_uses_daemon_binary_sha_when_health_omits_build(monkeypatch):
    monkeypatch.setenv("DMEM_DAEMON_BUILD_SHA256", "binary-sha")
    identities = capture_identities({})
    assert identities["daemon_build_version"] == "binary-sha"
    assert identities["daemon_build_commit"] == "binary-sha"
