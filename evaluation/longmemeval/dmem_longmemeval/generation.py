"""Answer generation using an OpenAI-compatible chat completions API.

Reads retrieval results from the checkpoint, reconstructs chat history from
recalled memories sorted by session date, and generates answers that are
written to ``hypotheses.jsonl`` in the official LongMemEval format.
"""

from __future__ import annotations

import logging

from openai import OpenAI

from .checkpoint import Checkpoint
from .dataset import load_dataset

logger = logging.getLogger(__name__)

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
) -> int:
    """Generate answers and append to checkpoint. Returns count generated."""
    cases, _ = load_dataset(dataset_path)
    cp = Checkpoint(output_dir)
    retrieval_records = cp.load_retrieval_records()
    done = cp.load_generation_qids()

    client = OpenAI(api_key=api_key, base_url=base_url, max_retries=5)
    template = PROMPT_COT if cot else PROMPT_PLAIN
    count = 0

    for case in cases:
        if case.question_id in done:
            continue
        rec = retrieval_records.get(case.question_id)
        if rec is None:
            logger.warning("no retrieval for %s, skipping", case.question_id)
            continue

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

        cp.append_generation(
            {
                "question_id": case.question_id,
                "model": model,
                "hypothesis": answer,
                "prompt_tokens": pt,
                "completion_tokens": ct,
            }
        )
        cp.append_hypothesis(case.question_id, answer)
        count += 1
        logger.info("generated: %s", case.question_id)

    return count
