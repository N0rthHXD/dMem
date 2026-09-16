"""QA judging using official LongMemEval prompt templates.

Implements the exact judge prompts from ``src/evaluation/evaluate_qa.py`` in
the upstream LongMemEval repository and reports overall, task-averaged,
per-type, and abstention accuracy.
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

JUDGE_PROMPT_VERSION = "longmemeval-upstream-judge-v1"

def get_judge_prompt(
    task: str,
    question: str,
    answer: str,
    response: str,
    *,
    abstention: bool = False,
) -> str:
    if not abstention:
        if task in ("single-session-user", "single-session-assistant", "multi-session"):
            tmpl = (
                "I will give you a question, a correct answer, and a response "
                "from a model. Please answer yes if the response contains the "
                "correct answer. Otherwise, answer no. If the response is "
                "equivalent to the correct answer or contains all the "
                "intermediate steps to get the correct answer, you should also "
                "answer yes. If the response only contains a subset of the "
                "information required by the answer, answer no. \n\n"
                "Question: {}\n\nCorrect Answer: {}\n\n"
                "Model Response: {}\n\n"
                "Is the model response correct? Answer yes or no only."
            )
        elif task == "temporal-reasoning":
            tmpl = (
                "I will give you a question, a correct answer, and a response "
                "from a model. Please answer yes if the response contains the "
                "correct answer. Otherwise, answer no. If the response is "
                "equivalent to the correct answer or contains all the "
                "intermediate steps to get the correct answer, you should also "
                "answer yes. If the response only contains a subset of the "
                "information required by the answer, answer no. In addition, do "
                "not penalize off-by-one errors for the number of days. If the "
                "question asks for the number of days/weeks/months, etc., and "
                "the model makes off-by-one errors (e.g., predicting 19 days "
                "when the answer is 18), the model's response is still "
                "correct. \n\n"
                "Question: {}\n\nCorrect Answer: {}\n\n"
                "Model Response: {}\n\n"
                "Is the model response correct? Answer yes or no only."
            )
        elif task == "knowledge-update":
            tmpl = (
                "I will give you a question, a correct answer, and a response "
                "from a model. Please answer yes if the response contains the "
                "correct answer. Otherwise, answer no. If the response contains "
                "some previous information along with an updated answer, the "
                "response should be considered as correct as long as the "
                "updated answer is the required answer.\n\n"
                "Question: {}\n\nCorrect Answer: {}\n\n"
                "Model Response: {}\n\n"
                "Is the model response correct? Answer yes or no only."
            )
        elif task == "single-session-preference":
            tmpl = (
                "I will give you a question, a rubric for desired personalized "
                "response, and a response from a model. Please answer yes if "
                "the response satisfies the desired response. Otherwise, "
                "answer no. The model does not need to reflect all the points "
                "in the rubric. The response is correct as long as it recalls "
                "and utilizes the user's personal information correctly.\n\n"
                "Question: {}\n\nRubric: {}\n\n"
                "Model Response: {}\n\n"
                "Is the model response correct? Answer yes or no only."
            )
        else:
            raise NotImplementedError(f"unsupported task: {task}")
        return tmpl.format(question, answer, response)
    else:
        tmpl = (
            "I will give you an unanswerable question, an explanation, and a "
            "response from a model. Please answer yes if the model correctly "
            "identifies the question as unanswerable. The model could say that "
            "the information is incomplete, or some other information is given "
            "but the asked information is not.\n\n"
            "Question: {}\n\nExplanation: {}\n\n"
            "Model Response: {}\n\n"
            "Does the model correctly identify the question as unanswerable? "
            "Answer yes or no only."
        )
        return tmpl.format(question, answer, response)


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
    cases, _ = load_dataset(dataset_path)
    cp = Checkpoint(output_dir)
    cp.assert_phase_compatible({
        "judge_model": model,
        "judge_prompt": JUDGE_PROMPT_VERSION,
        "judge_temperature": 0,
        "judge_max_tokens": 10,
    })
    generation_records = cp.load_generation_records()
    done = cp.load_judge_qids()

    client = OpenAI(api_key=api_key, base_url=base_url, max_retries=5)

    existing_records = cp.load_judge_records()
    all_acc: list[int] = []
    type2acc: dict[str, list[int]] = {}
    abstention_acc: list[int] = []
    for case in cases:
        existing = existing_records.get(case.question_id)
        if existing is None:
            continue
        acc = 1 if existing.get("label") else 0
        all_acc.append(acc)
        type2acc.setdefault(case.question_type, []).append(acc)
        if case.is_abstention:
            abstention_acc.append(acc)

    eligible = [case for case in cases if case.question_id in generation_records]
    todo = [case for case in eligible if case.question_id not in done]

    def judge(case: Any) -> tuple[dict[str, Any], int]:
        gen_rec = generation_records[case.question_id]
        prompt = get_judge_prompt(
            case.question_type,
            case.question,
            case.answer_string,
            gen_rec["hypothesis"],
            abstention=case.is_abstention,
        )
        completion = client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": prompt}],
            n=1,
            temperature=0,
            max_tokens=10,
        )
        eval_response = completion.choices[0].message.content.strip()
        usage = completion.usage
        label = "yes" in eval_response.lower()
        return (
            {
                "question_id": case.question_id,
                "model": model,
                "label": label,
                "raw_response": eval_response,
                "prompt_version": JUDGE_PROMPT_VERSION,
                "prompt_tokens": usage.prompt_tokens if usage else 0,
                "completion_tokens": usage.completion_tokens if usage else 0,
            },
            1 if label else 0,
        )

    first_error: Exception | None = None
    with tqdm(
        total=len(eligible),
        initial=len(eligible) - len(todo),
        desc="judge",
        unit="q",
        file=sys.stderr,
    ) as bar:
        with ThreadPoolExecutor(
            max_workers=workers, thread_name_prefix="lme-judge"
        ) as executor:
            futures = {executor.submit(judge, case): case for case in todo}
            for future in as_completed(futures):
                case = futures[future]
                try:
                    record, acc = future.result()
                except Exception as exc:
                    if first_error is None:
                        first_error = exc
                else:
                    cp.append_judge(record)
                    all_acc.append(acc)
                    type2acc.setdefault(case.question_type, []).append(acc)
                    if case.is_abstention:
                        abstention_acc.append(acc)
                    bar.set_postfix_str(
                        f"q={case.question_id} correct={sum(all_acc)}/{len(all_acc)}"
                    )
                bar.update(1)

    if first_error is not None:
        raise first_error

    summary = {
        "overall_accuracy": float(np.mean(all_acc)) if all_acc else 0.0,
        "task_averaged_accuracy": (
            float(np.mean([np.mean(v) for v in type2acc.values()]))
            if type2acc
            else 0.0
        ),
        "abstention_accuracy": (
            float(np.mean(abstention_acc)) if abstention_acc else 0.0
        ),
        "per_type": {k: float(np.mean(v)) for k, v in type2acc.items()},
        "total_judged": len(all_acc),
    }

    existing = cp.read_manifest() or {}
    existing["qa"] = summary
    existing.update({
        "judge_model": model,
        "judge_base_url": base_url or "",
        "judge_prompt": JUDGE_PROMPT_VERSION,
        "judge_temperature": 0,
        "judge_max_tokens": 10,
    })
    cp.write_manifest(existing)
    return summary
