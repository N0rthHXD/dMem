from types import SimpleNamespace

import pytest

from dmem_longmemeval.checkpoint import Checkpoint
from dmem_longmemeval.runner import RunConfig, Runner


def make_case(question_id):
    return SimpleNamespace(
        question_id=question_id,
        question_type="single-session-user",
        is_abstention=False,
        answer_session_ids=["session"],
        sessions=lambda: [],
    )


def make_runner(tmp_path):
    checkpoint = Checkpoint(tmp_path)
    config = RunConfig(
        run_id="test",
        dataset_sha="sha",
        socket_path="/tmp/dmem.sock",
        cutoffs=[1],
    )
    return Runner(SimpleNamespace(), config, checkpoint), checkpoint


def append_complete_ingestion(checkpoint, question_id, task_id):
    checkpoint.append_ingestion({
        "record_type": "case_complete",
        "question_id": question_id,
        "task_space_id": task_id,
    })


def append_retrieval(checkpoint, question_id):
    checkpoint.append_retrieval({
        "question_id": question_id,
        "metrics": {
            "@1": {"recall_any": 1.0, "recall_all": 1.0, "ndcg": 1.0}
        },
        "degraded": False,
        "has_answer_session_labels": True,
    })


def test_ingestion_failure_does_not_stop_or_enter_task_map(tmp_path, monkeypatch):
    runner, checkpoint = make_runner(tmp_path)
    cases = [make_case("failed"), make_case("completed")]
    attempted = []
    monkeypatch.setattr(
        runner,
        "_get_or_create_task",
        lambda case, _project_id: "task-" + case.question_id,
    )

    def ingest(case, task_id):
        attempted.append(case.question_id)
        if case.question_id == "failed":
            raise RuntimeError("provider timeout")
        append_complete_ingestion(checkpoint, case.question_id, task_id)

    monkeypatch.setattr(runner, "_ingest_case", ingest)

    task_map = runner._ingest_phase(cases, "project")

    assert attempted == ["failed", "completed"]
    assert task_map == {"completed": "task-completed"}
    assert checkpoint.load_failure_records()[0]["phase"] == "ingestion"


def test_partial_ingestion_checkpoint_is_not_recalled(tmp_path, monkeypatch):
    runner, checkpoint = make_runner(tmp_path)
    cases = [make_case("partial"), make_case("completed")]
    checkpoint.append_ingestion({
        "record_type": "event",
        "question_id": "partial",
        "task_space_id": "task-partial",
        "event_id": "event-partial",
    })
    append_complete_ingestion(checkpoint, "completed", "task-completed")
    recalled = []
    monkeypatch.setattr(
        runner,
        "_recall_case",
        lambda case, *_args: recalled.append(case.question_id),
    )

    runner._recall_phase(cases, {"completed": "task-completed"})

    assert recalled == ["completed"]


def test_retrieval_failure_continues_and_is_excluded_from_metrics(
    tmp_path, monkeypatch
):
    runner, checkpoint = make_runner(tmp_path)
    cases = [make_case("failed"), make_case("completed")]
    for case in cases:
        append_complete_ingestion(
            checkpoint, case.question_id, "task-" + case.question_id
        )

    def recall(case, *_args):
        if case.question_id == "failed":
            raise RuntimeError("recall unavailable")
        append_retrieval(checkpoint, case.question_id)

    monkeypatch.setattr(runner, "_recall_case", recall)
    runner._recall_phase(
        cases,
        {case.question_id: "task-" + case.question_id for case in cases},
    )

    summary = runner._summarize(cases)
    assert summary["complete"] is False
    assert summary["coverage"] == 0.5
    assert summary["failures"]["by_phase"] == {"retrieval": 1}
    assert summary["retrieval"]["completed_cases"] == 1
    assert summary["retrieval"]["evaluated_cases"] == 1
    assert summary["retrieval"]["excluded_errors"] == 1


def test_successful_resume_resolves_historical_failures(tmp_path):
    runner, checkpoint = make_runner(tmp_path)
    case = make_case("resumed")
    checkpoint.append_failure({
        "question_id": case.question_id,
        "phase": "ingestion",
        "error_type": "RuntimeError",
        "error": "timeout",
    })
    checkpoint.append_failure({
        "question_id": case.question_id,
        "phase": "retrieval",
        "error_type": "RuntimeError",
        "error": "unavailable",
    })
    append_complete_ingestion(checkpoint, case.question_id, "task-resumed")
    append_retrieval(checkpoint, case.question_id)

    summary = runner._summarize([case])

    assert summary["complete"] is True
    assert summary["coverage"] == 1.0
    assert summary["failures"] == {
        "total_cases": 0,
        "total_attempts": 2,
        "by_phase": {},
    }


def test_case_isolation_does_not_swallow_keyboard_interrupt(tmp_path, monkeypatch):
    runner, _checkpoint = make_runner(tmp_path)
    case = make_case("interrupted")
    monkeypatch.setattr(
        runner, "_get_or_create_task", lambda *_args: "task-interrupted"
    )

    def interrupt(*_args):
        raise KeyboardInterrupt

    monkeypatch.setattr(runner, "_ingest_case", interrupt)

    with pytest.raises(KeyboardInterrupt):
        runner._ingest_phase([case], "project")
