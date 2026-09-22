"""Loading and normalizing the LoCoMo Chinese evaluation subset."""

from __future__ import annotations

import json
import re
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

_SESSION_KEY = re.compile(r"^session_(\d+)$")
_DATETIME_PATTERNS = (
    "%I:%M %p on %d %B, %Y",
    "%I:%M%p on %d %B, %Y",
    "%H:%M on %d %B, %Y",
    "%I:%M %p on %d %b, %Y",
)


@dataclass
class Turn:
    dia_id: str
    speaker: str
    text: str
    caption: str = ""
    image_urls: list[str] = field(default_factory=list)

    def transcript_line(self, include_captions: bool = True) -> str:
        line = f"[{self.dia_id}] {self.speaker}: {self.text}"
        if include_captions and self.caption:
            line += f"（图片描述：{self.caption}）"
        return line


@dataclass
class Session:
    index: int
    key: str
    date_time_raw: str
    occurred_at: str
    turns: list[Turn]


@dataclass
class Sample:
    sample_id: str
    speaker_a: str
    speaker_b: str
    sessions: list[Session]
    qa: list[dict]

    def dia_index(self) -> dict[str, Turn]:
        index: dict[str, Turn] = {}
        for session in self.sessions:
            for turn in session.turns:
                index[turn.dia_id] = turn
        return index

    @property
    def turn_count(self) -> int:
        return sum(len(session.turns) for session in self.sessions)


def _parse_session_datetime(value: str) -> str:
    text = (value or "").strip()
    for pattern in _DATETIME_PATTERNS:
        try:
            parsed = datetime.strptime(text, pattern)
        except ValueError:
            continue
        return parsed.replace(tzinfo=timezone.utc).isoformat()
    return ""


def _normalize_qa(sample_id: str, raw_qa: list[dict], categories: list[int]) -> list[dict]:
    items: list[dict] = []
    for position, entry in enumerate(raw_qa):
        category = int(entry.get("category", 0))
        if categories and category not in categories:
            continue
        answer = entry.get("answer")
        if answer is not None and not isinstance(answer, str):
            answer = str(answer)
        evidence = [str(item) for item in (entry.get("evidence") or []) if str(item).strip()]
        items.append(
            {
                "qa_id": f"{sample_id}:q{position}",
                "sample_id": sample_id,
                "index": position,
                "question": str(entry.get("question", "")).strip(),
                "answer": answer,
                "adversarial_answer": entry.get("adversarial_answer"),
                "evidence": evidence,
                "category": category,
            }
        )
    return items


def load_dataset(
    path: str | Path,
    sample_ids: list[str] | None = None,
    categories: list[int] | None = None,
) -> tuple[list[Sample], list[dict]]:
    """Load the dataset and return (samples, qa_items)."""

    dataset_path = Path(path).expanduser()
    with dataset_path.open("r", encoding="utf-8") as handle:
        raw = json.load(handle)
    if not isinstance(raw, list):
        raise ValueError(f"dataset {dataset_path} must be a JSON array of conversations")

    wanted = set(sample_ids or [])
    samples: list[Sample] = []
    qa_items: list[dict] = []
    for entry in raw:
        sample_id = str(entry.get("sample_id") or "").strip()
        if not sample_id or (wanted and sample_id not in wanted):
            continue
        conversation = entry.get("conversation") or {}
        session_indices = sorted(
            int(match.group(1))
            for key in conversation
            if (match := _SESSION_KEY.match(key))
        )
        sessions: list[Session] = []
        for index in session_indices:
            key = f"session_{index}"
            turns: list[Turn] = []
            for raw_turn in conversation.get(key) or []:
                dia_id = str(raw_turn.get("dia_id") or "").strip()
                if not dia_id:
                    continue
                turns.append(
                    Turn(
                        dia_id=dia_id,
                        speaker=str(raw_turn.get("speaker") or "").strip(),
                        text=str(raw_turn.get("text") or "").strip(),
                        caption=str(raw_turn.get("blip_caption") or "").strip(),
                        image_urls=[str(url) for url in (raw_turn.get("img_url") or [])],
                    )
                )
            date_time_raw = str(conversation.get(f"{key}_date_time") or "").strip()
            sessions.append(
                Session(
                    index=index,
                    key=key,
                    date_time_raw=date_time_raw,
                    occurred_at=_parse_session_datetime(date_time_raw),
                    turns=turns,
                )
            )
        sample = Sample(
            sample_id=sample_id,
            speaker_a=str(conversation.get("speaker_a") or "").strip(),
            speaker_b=str(conversation.get("speaker_b") or "").strip(),
            sessions=sessions,
            qa=_normalize_qa(sample_id, entry.get("qa") or [], list(categories or [])),
        )
        samples.append(sample)
        qa_items.extend(sample.qa)

    if not samples:
        raise ValueError("dataset selection produced no conversations")
    return samples, qa_items


def validate_dataset(samples: list[Sample], qa_items: list[dict]) -> dict:
    """Return deterministic dataset statistics used in the run manifest."""

    categories: dict[str, int] = {}
    empty_evidence = 0
    unresolved_evidence = 0
    image_turns = 0
    caption_turns = 0
    unparsed_dates = 0
    total_turns = 0
    total_sessions = 0
    for sample in samples:
        dia_index = sample.dia_index()
        total_sessions += len(sample.sessions)
        for session in sample.sessions:
            if not session.occurred_at:
                unparsed_dates += 1
            for turn in session.turns:
                total_turns += 1
                if turn.image_urls:
                    image_turns += 1
                if turn.caption:
                    caption_turns += 1
        for item in sample.qa:
            categories[str(item["category"])] = categories.get(str(item["category"]), 0) + 1
            if not item["evidence"]:
                empty_evidence += 1
                continue
            if any(dia_id not in dia_index for dia_id in item["evidence"]):
                unresolved_evidence += 1
    return {
        "conversations": len(samples),
        "sessions": total_sessions,
        "turns": total_turns,
        "qa": len(qa_items),
        "categories": dict(sorted(categories.items())),
        "image_turns": image_turns,
        "caption_turns": caption_turns,
        "qa_with_empty_evidence": empty_evidence,
        "qa_with_unresolved_evidence": unresolved_evidence,
        "sessions_with_unparsed_date": unparsed_dates,
    }
