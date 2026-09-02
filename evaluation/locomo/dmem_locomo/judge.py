"""Semantic QA judging using OpenAI-compatible chat completions.

Provides a fixed-judge semantic correctness signal that is complementary to
the deterministic official F1 score. The judge returns yes/no correctness,
distinguishing answerable questions (type-specific prompts) from adversarial
questions, which are correct only when the model abstains.
"""

from __future__ import annotations

import sys
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Any

import numpy as np
from openai import OpenAI

from .checkpoint import Checkpoint
from .dataset import load_dataset
from .progress import tqdm

PROMPT_ANSWERABLE = (
    "I will give you a question, a correct answer, and a response from a "
    "model. Please answer yes if the response contains the correct answer. "
    "Otherwise, answer no. If the response is equivalent to the correct "
    "answer or contains all the intermediate steps to get the correct answer, "
    "you should also answer yes. If the response only contains a subset of "
    "the information required by the answer, answer no.\n\n"
    "Question: {}\n\nCorrect Answer: {}\n\nModel Response: {}\n\n"
    "Is the model response correct? Answer yes or no only."
)

PROMPT_TEMPORAL = (
    "I will give you a question, a correct answer, and a response from a "
    "model. Please answer yes if the response contains the correct answer. "
    "Otherwise, answer no. If the response is equivalent to the correct "
    "answer or contains all the intermediate steps to get the correct answer, "
    "you should also answer yes. If the response only contains a subset of "
    "the information required by the answer, answer no. In addition, do not "
    "penalize off-by-one errors for the number of days, weeks, or months.\n\n"
    "Question: {}\n\nCorrect Answer: {}\n\nModel Response: {}\n\n"
    "Is the model response correct? Answer yes or no only."
)

PROMPT_ADVERSARIAL = (
    "I will give you an unanswerable or adversarial question, an explanation, "
    "and a response from a model. Please answer yes if the model correctly "
    "identifies that the question cannot be answered from the conversation, "
    "for example by saying 'No information available' or explicitly refusing "
    "to attribute the information. Otherwise, answer no.\n\n"
    "Question: {}\n\nExplanation: {}\n\nModel Response: {}\n\n"
    "Does the model correctly identify the question as unanswerable? "
    "Answer yes or no only."
)


def judge_answers(
    dataset_path: str,
    output_dir: str,
    *,
    model: str,
    api_key: str,
    base_url: str | None = None,
    workers: int = 1,
) -> dict:
    """Judge generated answers and return QA summary."""
    if isinstance(workers, bool) or not isinstance(workers, int) or workers < 1:
        raise ValueError("workers must be a positive integer")
    conversations, _ = load_dataset(dataset_path)
    cp = Checkpoint(output_dir)
    generation_records = cp.load_generation_records()
    done = cp.load_judge_qids()

    client = OpenAI(api_key=api_key, base_url=base_url, max_retries=5)

    qa_map = {qa.qa_id: (conv, qa) for conv in conversations for qa in conv.qa}
    eligible = [qa_map[qid] for qid in generation_records if qid not in done]

    def judge(item: tuple[Any, Any]) -> tuple[dict[str, Any], int]:
        conv, qa = item
        gen_rec = generation_records[qa.qa_id]
        hypothesis = gen_rec["hypothesis"]
        if qa.is_adversarial:
            explanation = qa.adversarial_answer or qa.answer_string or "not mentioned"
            prompt = PROMPT_ADVERSARIAL.format(qa.question, explanation, hypothesis)
        elif qa.category == 2:
            prompt = PROMPT_TEMPORAL.format(qa.question, qa.answer_string, hypothesis)
        else:
            prompt = PROMPT_ANSWERABLE.format(qa.question, qa.answer_string, hypothesis)
        completion = client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": prompt}],
            n=1,
            temperature=0,
            max_tokens=10,
        )
        eval_response = completion.choices[0].message.content.strip()
        label = "yes" in eval_response.lower()
        return (
            {
                "qa_id": qa.qa_id,
                "sample_id": conv.sample_id,
                "category": qa.category,
                "model": model,
                "label": label,
                "raw_response": eval_response,
            },
            1 if label else 0,
        )

    all_acc: list[int] = []
    type2acc: dict[int, list[int]] = {}
    first_error: Exception | None = None
    with tqdm(total=len(eligible), desc="judge", unit="q", file=sys.stderr) as bar:
        with ThreadPoolExecutor(
            max_workers=workers, thread_name_prefix="loc-judge"
        ) as executor:
            futures = {executor.submit(judge, item): item for item in eligible}
            for future in as_completed(futures):
                item = futures[future]
                try:
                    record, acc = future.result()
                except Exception as exc:
                    if first_error is None:
                        first_error = exc
                else:
                    cp.append_judge(record)
                    all_acc.append(acc)
                    type2acc.setdefault(record["category"], []).append(acc)
                    bar.set_postfix_str(
                        f"q={record['qa_id']} correct={sum(all_acc)}/{len(all_acc)}"
                    )
                bar.update(1)

    if first_error is not None:
        raise first_error

    summary = {
        "overall_accuracy": float(np.mean(all_acc)) if all_acc else 0.0,
        "category_averaged_accuracy": (
            float(np.mean([np.mean(v) for v in type2acc.values()]))
            if type2acc
            else 0.0
        ),
        "per_category": {k: float(np.mean(v)) for k, v in sorted(type2acc.items())},
        "total_judged": len(all_acc),
    }

    existing = cp.read_manifest() or {}
    existing["semantic_judge"] = summary
    cp.write_manifest(existing)
    return summary
