"""Dataset validation tests.

Strict validation must enforce expected corpus counts and reject malformed or
partially unresolved evidence; lenient loading must still record what could not
be resolved so metrics can exclude it explicitly.
"""

import json
from datetime import datetime

import pytest

from dmem_locomo.dataset import (
    Conversation,
    DatasetError,
    QA,
    Session,
    Turn,
    load_dataset,
    validate_dataset,
)


def make_turn(session_num, turn_num):
    return Turn("A", f"D{session_num}:{turn_num}", session_num, turn_num, "text")


def make_conversation(sample_id="conv-x", evidence=("D1:1",), category=4):
    sessions = [
        Session(
            session_num=1,
            date_time="1:00 PM on 1 January, 2024",
            date=datetime(2024, 1, 1, 13, 0),
            turns=[make_turn(1, 1), make_turn(1, 2)],
        )
    ]
    qa = QA(
        sample_id=sample_id,
        index=0,
        question="q",
        answer="a",
        adversarial_answer=None,
        evidence_raw=list(evidence),
        evidence=list(evidence),
        evidence_unresolved=[],
        category=category,
    )
    return Conversation(
        sample_id=sample_id,
        speaker_a="A",
        speaker_b="B",
        sessions=sessions,
        qa=[qa],
    )


def write_dataset(tmp_path, evidence):
    payload = [
        {
            "sample_id": "conv-1",
            "conversation": {
                "speaker_a": "A",
                "speaker_b": "B",
                "session_1": [
                    {"dia_id": "D1:1", "speaker": "A", "text": "hi"},
                    {"dia_id": "D1:2", "speaker": "B", "text": "there"},
                ],
                "session_1_date_time": "1:00 PM on 1 January, 2024",
            },
            "qa": [
                {
                    "question": "q",
                    "answer": "a",
                    "evidence": evidence,
                    "category": 4,
                }
            ],
        }
    ]
    path = tmp_path / "locomo.json"
    path.write_text(json.dumps(payload), encoding="utf-8")
    return path


def test_non_strict_reports_counts_without_enforcement():
    stats = validate_dataset([make_conversation()])
    assert stats["conversations"] == 1
    assert stats["sessions"] == 1
    assert stats["turns"] == 2
    assert stats["qa"] == 1
    assert stats["expected_counts"]


def test_strict_enforces_expected_counts():
    with pytest.raises(DatasetError):
        validate_dataset(
            [make_conversation()],
            strict=True,
            expected_counts={
                "conversations": 10,
                "sessions": 272,
                "turns": 5882,
                "qa": 1986,
                "categories": {1: 282, 2: 321, 3: 96, 4: 841, 5: 446},
            },
        )


def test_strict_accepts_matching_counts():
    stats = validate_dataset(
        [make_conversation()],
        strict=True,
        expected_counts={
            "conversations": 1,
            "sessions": 1,
            "turns": 2,
            "qa": 1,
            "categories": {4: 1},
        },
    )
    assert stats["conversations"] == 1


def test_load_dataset_strict_rejects_malformed_evidence(tmp_path):
    path = write_dataset(tmp_path, ["D"])
    with pytest.raises(DatasetError):
        load_dataset(
            path,
            strict=True,
            expected_counts={
                "conversations": 1,
                "sessions": 1,
                "turns": 2,
                "qa": 1,
                "categories": {4: 1},
            },
        )


def test_load_dataset_strict_rejects_partially_unresolved_evidence(tmp_path):
    path = write_dataset(tmp_path, ["D1:1", "D9:9"])
    with pytest.raises(DatasetError):
        load_dataset(
            path,
            strict=True,
            expected_counts={
                "conversations": 1,
                "sessions": 1,
                "turns": 2,
                "qa": 1,
                "categories": {4: 1},
            },
        )


def test_load_dataset_lenient_records_unresolved_evidence(tmp_path):
    path = write_dataset(tmp_path, ["D1:1", "D9:9", "D"])
    conversations, _sha = load_dataset(path)
    qa = conversations[0].qa[0]
    assert qa.evidence == ["D1:1"]
    assert "D9:9" in qa.evidence_unresolved
    assert "D" in qa.evidence_unresolved
    assert qa.has_unresolved_evidence is True
    assert qa.is_scorable is False


def test_strict_rejects_when_any_qa_has_unresolved_evidence():
    conv = make_conversation(evidence=("D1:1",))
    conv.qa[0].evidence_unresolved = ["D9:9"]
    with pytest.raises(DatasetError):
        validate_dataset(
            [conv],
            strict=True,
            expected_counts={
                "conversations": 1,
                "sessions": 1,
                "turns": 2,
                "qa": 1,
                "categories": {4: 1},
            },
        )
