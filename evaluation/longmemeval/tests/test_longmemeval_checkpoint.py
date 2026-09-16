import json

from dmem_longmemeval.checkpoint import Checkpoint


def test_load_retrieval_records_ignores_partial_final_line(tmp_path):
    checkpoint = Checkpoint(tmp_path)
    checkpoint.retrieval_path.write_text(
        json.dumps({"question_id": "completed"})
        + "\n"
        + '{"question_id": "interrupted"',
        encoding="utf-8",
    )

    assert checkpoint.load_retrieval_qids() == {"completed"}


def test_load_retrieval_records_rejects_invalid_nonfinal_line(tmp_path):
    checkpoint = Checkpoint(tmp_path)
    checkpoint.retrieval_path.write_text(
        '{"question_id": "invalid"\n'
        + json.dumps({"question_id": "completed"})
        + "\n",
        encoding="utf-8",
    )

    try:
        checkpoint.load_retrieval_records()
    except json.JSONDecodeError:
        pass
    else:
        raise AssertionError("invalid non-final JSONL record must not be ignored")


def test_failure_records_are_append_only_and_do_not_complete_ingestion(tmp_path):
    checkpoint = Checkpoint(tmp_path)
    checkpoint.append_failure({
        "question_id": "failed",
        "phase": "ingestion",
        "error_type": "RuntimeError",
        "error": "provider timeout",
    })

    assert checkpoint.load_failure_records() == [{
        "question_id": "failed",
        "phase": "ingestion",
        "error_type": "RuntimeError",
        "error": "provider timeout",
    }]
    assert checkpoint.load_ingested_qids() == set()


def test_checkpoint_compatibility_has_no_legacy_missing_key_exception(tmp_path):
    checkpoint = Checkpoint(tmp_path)
    checkpoint.write_manifest({"dataset_sha": "sha"})

    compatible, reason = checkpoint.is_compatible({
        "dataset_sha": "sha",
        "invalid_evidence_reference_retries": 1,
    })

    assert compatible is False
    assert "invalid_evidence_reference_retries" in reason
