import threading
from datetime import datetime
from types import SimpleNamespace

from dmem_longmemeval.checkpoint import Checkpoint
from dmem_longmemeval import generation, judge


class FakeCompletions:
    def __init__(self, barrier, response):
        self.barrier = barrier
        self.response = response

    def create(self, **_kwargs):
        self.barrier.wait(timeout=2)
        return SimpleNamespace(
            choices=[SimpleNamespace(message=SimpleNamespace(content=self.response))],
            usage=SimpleNamespace(prompt_tokens=2, completion_tokens=1),
        )


class FakeOpenAI:
    barrier = None
    response = ""

    def __init__(self, **_kwargs):
        self.chat = SimpleNamespace(
            completions=FakeCompletions(self.barrier, self.response)
        )


def make_cases():
    return [
        SimpleNamespace(
            question_id=qid,
            question=f"question {qid}",
            question_date=datetime(2024, 1, 1),
            question_type="single-session-user",
            answer_string="answer",
            is_abstention=False,
        )
        for qid in ("first", "second")
    ]


def test_generation_calls_model_in_parallel(tmp_path, monkeypatch):
    checkpoint = Checkpoint(tmp_path)
    cases = make_cases()
    for case in cases:
        checkpoint.append_retrieval({
            "question_id": case.question_id,
            "ranked_items": [],
        })
    FakeOpenAI.barrier = threading.Barrier(2)
    FakeOpenAI.response = "generated answer"
    monkeypatch.setattr(generation, "OpenAI", FakeOpenAI)
    monkeypatch.setattr(generation, "load_dataset", lambda _path: (cases, "sha"))

    count = generation.generate_answers(
        "dataset.json", str(tmp_path), model="model", api_key="key", workers=2
    )

    assert count == 2
    assert checkpoint.load_generation_qids() == {"first", "second"}
    hypotheses = checkpoint._read_jsonl(checkpoint.hypotheses_path)
    assert {record["question_id"] for record in hypotheses} == {"first", "second"}


def test_judge_calls_model_in_parallel(tmp_path, monkeypatch):
    checkpoint = Checkpoint(tmp_path)
    cases = make_cases()
    for case in cases:
        checkpoint.append_generation({
            "question_id": case.question_id,
            "hypothesis": "generated answer",
        })
    FakeOpenAI.barrier = threading.Barrier(2)
    FakeOpenAI.response = "yes"
    monkeypatch.setattr(judge, "OpenAI", FakeOpenAI)
    monkeypatch.setattr(judge, "load_dataset", lambda _path: (cases, "sha"))

    summary = judge.judge_answers(
        "dataset.json", str(tmp_path), model="model", api_key="key", workers=2
    )

    assert summary["overall_accuracy"] == 1.0
    assert summary["total_judged"] == 2
    assert checkpoint.load_judge_qids() == {"first", "second"}
