import json
import threading
from concurrent.futures import ThreadPoolExecutor
from types import SimpleNamespace

import pytest

from dmem_longmemeval.checkpoint import Checkpoint
from dmem_longmemeval.config import load_config
from dmem_longmemeval.runner import RunConfig, Runner


def make_case(question_id):
    return SimpleNamespace(
        question_id=question_id,
        question_type="single-session-user",
        is_abstention=False,
        answer_session_ids=["session"],
        sessions=lambda: [],
    )


def make_runner(tmp_path, *, ingestion_workers=1, retrieval_workers=1):
    checkpoint = Checkpoint(tmp_path)
    config = RunConfig(
        run_id="test",
        dataset_sha="sha",
        socket_path="/tmp/dmem.sock",
        cutoffs=[1],
        ingestion_workers=ingestion_workers,
        retrieval_workers=retrieval_workers,
    )
    return Runner(SimpleNamespace(), config, checkpoint), checkpoint


def test_config_loads_and_validates_workers(tmp_path):
    config_path = tmp_path / "eval.toml"
    config_path.write_text(
        """
[ingestion]
workers = 2
[retrieval]
workers = 3
[answer]
workers = 4
[judge]
workers = 5
""",
        encoding="utf-8",
    )

    config = load_config(config_path)

    assert config.ingestion.workers == 2
    assert config.retrieval.workers == 3
    assert config.answer.workers == 4
    assert config.judge.workers == 5

    config_path.write_text("[ingestion]\nworkers = 0\n", encoding="utf-8")
    with pytest.raises(ValueError, match="ingestion.workers"):
        load_config(config_path)


def test_checkpoint_concurrent_appends_are_complete(tmp_path):
    checkpoint = Checkpoint(tmp_path)

    def append(index):
        checkpoint.append_retrieval({"question_id": f"q-{index}"})

    with ThreadPoolExecutor(max_workers=8) as executor:
        list(executor.map(append, range(100)))

    lines = checkpoint.retrieval_path.read_text(encoding="utf-8").splitlines()
    records = [json.loads(line) for line in lines]
    assert len(records) == 100
    assert {record["question_id"] for record in records} == {
        f"q-{index}" for index in range(100)
    }


def test_ingestion_cases_run_in_parallel(tmp_path, monkeypatch):
    runner, checkpoint = make_runner(tmp_path, ingestion_workers=2)
    cases = [make_case("first"), make_case("second")]
    barrier = threading.Barrier(2)
    active = 0
    peak_active = 0
    lock = threading.Lock()

    monkeypatch.setattr(
        runner,
        "_get_or_create_task",
        lambda case, _project_id: "task-" + case.question_id,
    )

    def ingest(case, task_id):
        nonlocal active, peak_active
        with lock:
            active += 1
            peak_active = max(peak_active, active)
        barrier.wait(timeout=2)
        checkpoint.append_ingestion({
            "record_type": "case_complete",
            "question_id": case.question_id,
            "task_space_id": task_id,
        })
        with lock:
            active -= 1

    monkeypatch.setattr(runner, "_ingest_case", ingest)

    task_map = runner._ingest_phase(cases, "project")

    assert peak_active == 2
    assert task_map == {"first": "task-first", "second": "task-second"}


def test_recall_cases_run_in_parallel_and_isolate_failure(tmp_path, monkeypatch):
    runner, checkpoint = make_runner(tmp_path, retrieval_workers=2)
    cases = [make_case("failed"), make_case("completed")]
    barrier = threading.Barrier(2)

    def recall(case, *_args):
        barrier.wait(timeout=2)
        if case.question_id == "failed":
            raise RuntimeError("recall unavailable")
        checkpoint.append_retrieval({"question_id": case.question_id})

    monkeypatch.setattr(runner, "_recall_case", recall)
    runner._recall_phase(
        cases,
        {case.question_id: "task-" + case.question_id for case in cases},
    )

    assert checkpoint.load_retrieval_qids() == {"completed"}
    assert checkpoint.load_failure_records()[0]["question_id"] == "failed"


def test_recall_maps_all_evidence_sessions_not_only_first(tmp_path):
    runner, checkpoint = make_runner(tmp_path)
    runner.client = SimpleNamespace(recall=lambda **_kwargs: {
        "execution": {},
        "context": {"items": [{
            "memory_id": "memory",
            "display_content": "combined",
            "evidence": [{"event_id": "event-a"}, {"event_id": "event-b"}],
        }]},
    })
    case = SimpleNamespace(
        question_id="q", question="question", question_type="multi-session",
        question_date=__import__("datetime").datetime(2024, 1, 3),
        haystack_session_ids=["session-a", "session-b"],
        haystack_dates=[__import__("datetime").datetime(2024, 1, 1), __import__("datetime").datetime(2024, 1, 2)],
        answer_session_ids=["session-a", "session-b"], is_abstention=False,
    )
    ingestion = {"q": [
        {"event_id": "event-a", "session_id": "session-a", "has_answer": True},
        {"event_id": "event-b", "session_id": "session-b", "has_answer": True},
    ]}

    runner._recall_case(case, "task", ingestion)

    result = checkpoint.load_retrieval_records()["q"]
    assert result["metric_schema"] == "longmemeval-retrieval-metrics-v2"
    assert result["ranked_session_ids"] == ["session-a", "session-b"]
    assert result["ranked_items"][0]["session_ids"] == ["session-a", "session-b"]


def test_recall_phase_uses_shared_corpus_ingestion_records(tmp_path, monkeypatch):
    runner, _ = make_runner(tmp_path)
    case = make_case("shared")
    shared = {"shared": [{"event_id": "event", "session_id": "session"}]}
    observed = []
    monkeypatch.setattr(
        runner,
        "_recall_case",
        lambda _case, _task, records: observed.append(records),
    )

    runner._recall_phase([case], {"shared": "task"}, shared)

    assert observed == [shared]
