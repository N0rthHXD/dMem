"""Answer generation using an OpenAI-compatible chat completions API.

Reads retrieval results from the checkpoint, reconstructs chat history from
recalled memories sorted by evidence order, and generates answers that are
written to ``generation.jsonl``.
"""

from __future__ import annotations

from typing import Any

from dmem_eval.generation import run_generation

from .checkpoint import Checkpoint
from .dataset import load_dataset
from .llm import chat_with_retry, make_client

PROMPT = (
    "Below is a conversation between two people: {speaker_a} and {speaker_b}. "
    "The conversation takes place over multiple days and the date of each "
    "conversation is written at the beginning of the conversation.\n\n"
    "Relevant conversation history:\n{history}\n\n"
    "Based on the above context, write an answer in the form of a short "
    "phrase for the following question. Answer with exact words from the "
    "context whenever possible.\n\n"
    "Question: {question}\n\nShort answer:"
)

PROMPT_ADVERSARIAL = (
    "Below is a conversation between two people: {speaker_a} and {speaker_b}. "
    "The conversation takes place over multiple days and the date of each "
    "conversation is written at the beginning of the conversation.\n\n"
    "Relevant conversation history:\n{history}\n\n"
    "Based on the above context, answer the following question. If the "
    "information is not available in the conversation, answer exactly "
    "'No information available'.\n\n"
    "Question: {question}\n\nShort answer:"
)

GENERATION_PROMPT_VERSION = "locomo-answer-v1"


def _history_string(ranked_items: list[dict[str, Any]]) -> str:
    parts: list[str] = []
    for i, item in enumerate(ranked_items, start=1):
        content = item.get("content", "")
        if not content:
            continue
        parts.append(f"### Memory {i}:\n{content}")
    return "\n\n".join(parts)


def generate_answers(
    dataset_path: str,
    output_dir: str,
    *,
    model: str,
    api_key: str,
    base_url: str | None = None,
    answer_cutoff: int = 25,
    gen_length: int = 500,
    cot: bool = False,
    workers: int = 1,
) -> int:
    """Generate answers and append to checkpoint. Returns count generated."""
    conversations, _ = load_dataset(dataset_path)
    cp = Checkpoint(output_dir)
    cp.assert_phase_compatible({
        "answer_model": model,
        "generation_prompt": GENERATION_PROMPT_VERSION,
        "generation_temperature": 0,
        "generation_max_tokens": gen_length,
    })
    retrieval_records = cp.load_retrieval_records()
    done = cp.load_generation_qids()

    client = make_client(api_key, base_url)

    eligible: list[tuple[Any, Any]] = []
    for conv in conversations:
        for qa in conv.qa:
            if qa.qa_id in retrieval_records and qa.qa_id not in done:
                eligible.append((conv, qa))

    def generate(item: tuple[Any, Any]) -> tuple[dict[str, Any], str, int]:
        conv, qa = item
        rec = retrieval_records[qa.qa_id]
        ranked_items = rec.get("ranked_items", [])[:answer_cutoff]
        history = _history_string(ranked_items)
        template = PROMPT_ADVERSARIAL if qa.is_adversarial else PROMPT
        prompt = template.format(
            speaker_a=conv.speaker_a,
            speaker_b=conv.speaker_b,
            history=history or "(no relevant history retrieved)",
            question=qa.question,
        )
        completion = chat_with_retry(
            client, model=model, prompt=prompt, max_tokens=gen_length
        )
        answer = completion.choices[0].message.content.strip()
        usage = completion.usage
        pt = usage.prompt_tokens if usage else 0
        ct = usage.completion_tokens if usage else 0
        return (
            {
                "qa_id": qa.qa_id,
                "sample_id": conv.sample_id,
                "category": qa.category,
                "model": model,
                "prompt_version": GENERATION_PROMPT_VERSION,
                "hypothesis": answer,
                "prompt_tokens": pt,
                "completion_tokens": ct,
            },
            answer,
            pt + ct,
        )

    def on_record(
        _item: tuple[Any, Any], record: dict[str, Any], _answer: str, _tokens: int
    ) -> None:
        cp.append_generation(record)

    count = run_generation(
        total=len(eligible),
        todo=eligible,
        generate=generate,
        on_record=on_record,
        id_field="qa_id",
        workers=workers,
        thread_name_prefix="loc-generate",
    )

    manifest = cp.read_manifest() or {}
    manifest["answer_model"] = model
    manifest["generation_prompt"] = GENERATION_PROMPT_VERSION
    manifest["generation_temperature"] = 0
    manifest["generation_max_tokens"] = gen_length
    manifest["answer_base_url"] = base_url or ""
    cp.write_manifest(manifest)

    return count
