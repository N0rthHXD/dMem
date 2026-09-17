"""Answer generation using an OpenAI-compatible chat completions API.

Reads retrieval results from the checkpoint, reconstructs chat history from
recalled memories sorted by session date, and generates answers that are
written to ``hypotheses.jsonl`` in the official LongMemEval format.
"""

from __future__ import annotations

from typing import Any

from openai import OpenAI

from dmem_eval.generation import run_generation

from .checkpoint import Checkpoint
from .dataset import load_dataset

PROMPT_PLAIN = (
    "I will give you several history chats between you and a user. "
    "Please answer the question based on the relevant chat history.\n\n\n"
    "History Chats:\n\n{}\n\n"
    "Current Date: {}\n"
    "Question: {}\n"
    "Answer:"
)

PROMPT_COT = (
    "I will give you several history chats between you and a user. "
    "Please answer the question based on the relevant chat history. "
    "Answer the question step by step: first extract all the relevant "
    "information, and then reason over the information to get the answer.\n\n\n"
    "History Chats:\n\n{}\n\n"
    "Current Date: {}\n"
    "Question: {}\n"
    "Answer (step by step):"
)
GENERATION_PROMPT_VERSION = "longmemeval-upstream-generation-v1"


def generate_answers(
    dataset_path: str,
    output_dir: str,
    *,
    model: str,
    api_key: str,
    base_url: str | None = None,
    answer_cutoff: int = 30,
    gen_length: int = 500,
    cot: bool = False,
    workers: int = 1,
) -> int:
    """Generate answers and append to checkpoint. Returns count generated."""
    cases, _ = load_dataset(dataset_path)
    cp = Checkpoint(output_dir)
    cp.assert_phase_compatible({
        "answer_model": model,
        "generation_prompt": GENERATION_PROMPT_VERSION,
        "generation_temperature": 0,
        "generation_max_tokens": gen_length,
    })
    retrieval_records = cp.load_retrieval_records()
    done = cp.load_generation_qids()

    client = OpenAI(api_key=api_key, base_url=base_url, max_retries=5)
    template = PROMPT_COT if cot else PROMPT_PLAIN

    eligible = [case for case in cases if case.question_id in retrieval_records]
    todo = [case for case in eligible if case.question_id not in done]

    def generate(case: Any) -> tuple[dict[str, Any], str, int]:
        rec = retrieval_records[case.question_id]
        ranked_items = rec.get("ranked_items", [])[:answer_cutoff]
        ranked_items.sort(
            key=lambda x: x.get("session_date") or "0000-01-01T00:00:00+00:00"
        )

        history_parts = []
        for i, item in enumerate(ranked_items):
            content = item.get("content", "")
            date = item.get("session_date") or ""
            history_parts.append(
                f"### Session {i + 1}:\nSession Date: {date}\n"
                f"Session Content:\n{content}"
            )
        history_string = "\n".join(history_parts)

        qdate = case.question_date.strftime("%Y/%m/%d (%a) %H:%M")
        prompt = template.format(history_string, qdate, case.question)
        completion = client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": prompt}],
            n=1,
            temperature=0,
            max_tokens=gen_length,
        )
        answer = completion.choices[0].message.content.strip()
        usage = completion.usage
        pt = usage.prompt_tokens if usage else 0
        ct = usage.completion_tokens if usage else 0
        return (
            {
                "question_id": case.question_id,
                "model": model,
                "hypothesis": answer,
                "prompt_tokens": pt,
                "completion_tokens": ct,
                "prompt_version": GENERATION_PROMPT_VERSION,
            },
            answer,
            pt + ct,
        )

    def on_record(
        case: Any, record: dict[str, Any], answer: str, _tokens: int
    ) -> None:
        cp.append_generation(record)
        cp.append_hypothesis(case.question_id, answer)

    count = run_generation(
        total=len(eligible),
        todo=todo,
        generate=generate,
        on_record=on_record,
        id_field="question_id",
        workers=workers,
        thread_name_prefix="lme-generate",
    )

    manifest = cp.read_manifest() or {}
    manifest.update({
        "answer_model": model,
        "answer_base_url": base_url or "",
        "generation_prompt": GENERATION_PROMPT_VERSION,
        "generation_temperature": 0,
        "generation_max_tokens": gen_length,
    })
    cp.write_manifest(manifest)

    return count
