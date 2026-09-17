"""Shared checkpoint storage semantics, parameterized by identity fields."""

import json

import pytest

from dmem_eval.checkpoint import Checkpoint


def test_partial_final_line_is_ignored_on_read(tmp_path):
    cp = Checkpoint(tmp_path)
    cp.retrieval_path.write_text(
        json.dumps({"record_id": "complete"}) + "\n" + '{"record_id": "interrupted"',
        encoding="utf-8",
    )
    assert cp.load_retrieval_qids() == {"complete"}


def test_invalid_nonfinal_line_is_rejected(tmp_path):
    cp = Checkpoint(tmp_path)
    cp.retrieval_path.write_text(
        '{"record_id": "invalid"\n'
        + json.dumps({"record_id": "complete"})
        + "\n",
        encoding="utf-8",
    )
    with pytest.raises(json.JSONDecodeError):
        cp.load_retrieval_records()


def test_ingested_ids_require_completion_record(tmp_path):
    cp = Checkpoint(tmp_path)
    cp.append_ingestion({"record_type": "unit_complete", "unit_id": "done"})
    cp.append_ingestion({"record_type": "event", "unit_id": "partial"})
    assert cp.load_ingested_ids() == {"done"}
    assert cp.load_ingested_sample_ids() == {"done"}
    assert cp.load_ingested_qids() == {"done"}


def test_compatibility_requires_every_expected_key(tmp_path):
    cp = Checkpoint(tmp_path)
    cp.write_manifest({"dataset_sha": "sha"})
    ok, reason = cp.is_compatible({"dataset_sha": "sha", "metric_schema": "m2"})
    assert ok is False
    assert "metric_schema" in reason


def test_phase_compatibility_allows_unset_and_rejects_conflict(tmp_path):
    cp = Checkpoint(tmp_path)
    cp.write_manifest({})
    cp.assert_phase_compatible({"answer_model": "gpt-b"})
    cp.write_manifest({"answer_model": "gpt-a"})
    with pytest.raises(ValueError):
        cp.assert_phase_compatible({"answer_model": "gpt-b"})
