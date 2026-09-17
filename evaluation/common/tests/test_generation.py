"""Parallel generation frame: success persistence and fail-closed errors."""

import pytest

from dmem_eval.generation import run_generation


def test_run_generation_persists_successes_and_raises_first_error():
    recorded = []

    def generate(item):
        if item == "bad":
            raise RuntimeError("boom")
        return ({"id": item}, "answer", 1)

    with pytest.raises(RuntimeError, match="boom"):
        run_generation(
            total=2,
            todo=["ok", "bad"],
            generate=generate,
            on_record=lambda item, record, answer, tokens: recorded.append(record),
            id_field="id",
            workers=2,
            thread_name_prefix="test-generation",
        )

    assert recorded == [{"id": "ok"}]


def test_run_generation_uses_initial_for_resumed_units():
    seen = {}

    def generate(item):
        return ({"id": item}, "answer", 1)

    def on_record(item, record, answer, tokens):
        seen[item] = tokens

    count = run_generation(
        total=3,
        todo=["resumed"],
        generate=generate,
        on_record=on_record,
        id_field="id",
        workers=1,
        thread_name_prefix="test-generation",
    )
    assert count == 1
    assert seen == {"resumed": 1}


def test_run_generation_rejects_non_positive_workers():
    with pytest.raises(ValueError, match="workers"):
        run_generation(
            total=0,
            todo=[],
            generate=lambda item: ({}, "", 0),
            on_record=lambda *args: None,
            id_field="id",
            workers=0,
            thread_name_prefix="test-generation",
        )
