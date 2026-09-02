"""LoCoMo dataset loading, validation, and evidence normalization.

Parses the official ``locomo10.json`` format, validates structural integrity,
normalizes evidence references, and computes a SHA-256 of the raw bytes for
run-identity and resume checks. It never mutates the upstream file; all
normalization is applied to in-memory objects.
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Any

SESSION_KEY_RE = re.compile(r"^session_(\d+)$")
SESSION_DATE_KEY_RE = re.compile(r"^session_(\d+)_date_time$")
DIA_ID_RE = re.compile(r"D(\d+):(\d+)")

DATE_TIME_FORMAT = "%I:%M %p on %d %B, %Y"

# Official LoCoMo QA category labels.
CATEGORY_LABELS = {
    1: "multi-hop",
    2: "temporal",
    3: "open-domain",
    4: "single-hop",
    5: "adversarial",
}

# Expected aggregate counts of the official locomo10.json release. These are
# reported (not enforced) so reviewers can detect upstream data drift.
EXPECTED_COUNTS = {
    "conversations": 10,
    "sessions": 272,
    "turns": 5882,
    "qa": 1986,
    "categories": {1: 282, 2: 321, 3: 96, 4: 841, 5: 446},
}


class DatasetError(ValueError):
    pass


@dataclass
class Turn:
    speaker: str
    dia_id: str
    session_num: int
    turn_num: int
    text: str
    has_image: bool = False
    blip_caption: str = ""
    img_urls: list[str] = field(default_factory=list)
    query: str = ""


@dataclass
class Session:
    session_num: int
    date_time: str
    date: datetime
    turns: list[Turn] = field(default_factory=list)


@dataclass
class QA:
    sample_id: str
    index: int
    question: str
    answer: object  # str | int | None
    adversarial_answer: str | None
    evidence_raw: list[str]
    evidence: list[str]  # normalized "D<s>:<t>" references
    evidence_unresolved: list[str]
    category: int

    @property
    def qa_id(self) -> str:
        return f"{self.sample_id}:q{self.index}"

    @property
    def is_adversarial(self) -> bool:
        return self.category == 5

    @property
    def answer_string(self) -> str:
        return _answer_to_string(self.answer)

    @property
    def has_evidence(self) -> bool:
        return len(self.evidence) > 0


@dataclass
class Conversation:
    sample_id: str
    speaker_a: str
    speaker_b: str
    sessions: list[Session]
    qa: list[QA]
    event_summary: dict[str, Any] = field(default_factory=dict)
    observation: dict[str, Any] = field(default_factory=dict)
    session_summary: dict[str, Any] = field(default_factory=dict)

    def sorted_sessions(self) -> list[Session]:
        return sorted(self.sessions, key=lambda s: (s.date, s.session_num))

    def all_turns(self) -> list[Turn]:
        turns: list[Turn] = []
        for session in self.sorted_sessions():
            turns.extend(session.turns)
        return turns

    def dia_id_set(self) -> set[str]:
        return {t.dia_id for t in self.all_turns()}

    def last_session_date(self) -> datetime | None:
        sessions = self.sorted_sessions()
        return sessions[-1].date if sessions else None


def parse_date_time(s: str) -> datetime:
    try:
        return datetime.strptime(s, DATE_TIME_FORMAT)
    except ValueError as exc:
        raise DatasetError(f"invalid session date-time {s!r}: {exc}") from exc


def _answer_to_string(answer: object) -> str:
    if answer is None:
        return ""
    if isinstance(answer, str):
        return answer
    return str(answer)


def _normalize_evidence(raw_evidence: list[str], dia_id_set: set[str]) -> tuple[list[str], list[str]]:
    """Return ``(resolved, unresolved)`` normalized evidence references.

    Splits composite strings (e.g. ``"D8:6; D9:17"`` or
    ``"D9:1 D4:4 D4:6"``), canonicalizes zero-padded ids (``D30:05`` ->
    ``D30:5``), and separates references that do not resolve to a known turn.
    """
    resolved: list[str] = []
    unresolved: list[str] = []
    seen: set[str] = set()
    for raw in raw_evidence:
        if not isinstance(raw, str):
            unresolved.append(repr(raw))
            continue
        matches = DIA_ID_RE.findall(raw)
        if not matches:
            unresolved.append(raw)
            continue
        for session_str, turn_str in matches:
            canonical = f"D{int(session_str)}:{int(turn_str)}"
            if canonical in dia_id_set:
                if canonical not in seen:
                    seen.add(canonical)
                    resolved.append(canonical)
            else:
                if raw not in unresolved:
                    unresolved.append(raw)
    return resolved, unresolved


def _session_num_from_dia_id(dia_id: str) -> int:
    m = DIA_ID_RE.match(dia_id)
    if not m:
        raise ValueError(f"invalid dia_id {dia_id!r}")
    return int(m.group(1))


def load_dataset(path: str | Path) -> tuple[list[Conversation], str]:
    """Load and validate a LoCoMo JSON file.

    Returns ``(conversations, sha256_hex)`` where sha256_hex is the digest of
    the raw file bytes (used for run identity and resume checks).
    """
    raw = Path(path).read_bytes()
    sha = hashlib.sha256(raw).hexdigest()
    try:
        data = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise DatasetError(f"invalid JSON: {exc}") from exc
    if not isinstance(data, list):
        raise DatasetError("expected a JSON array of conversations")
    conversations: list[Conversation] = []
    seen_ids: set[str] = set()
    for i, entry in enumerate(data):
        if not isinstance(entry, dict):
            raise DatasetError(f"conversation {i}: expected object, got {type(entry)}")
        conv = _parse_conversation(entry, i, seen_ids)
        conversations.append(conv)
    return conversations, sha


def _parse_conversation(entry: dict, index: int, seen_ids: set[str]) -> Conversation:
    sample_id = entry.get("sample_id")
    if not isinstance(sample_id, str) or not sample_id:
        raise DatasetError(f"conversation {index}: missing sample_id")
    if sample_id in seen_ids:
        raise DatasetError(f"conversation {index}: duplicate sample_id {sample_id!r}")
    seen_ids.add(sample_id)

    conversation = entry.get("conversation")
    if not isinstance(conversation, dict):
        raise DatasetError(f"{sample_id}: missing conversation object")

    speaker_a = conversation.get("speaker_a", "")
    speaker_b = conversation.get("speaker_b", "")
    if not isinstance(speaker_a, str) or not isinstance(speaker_b, str):
        raise DatasetError(f"{sample_id}: speaker_a/speaker_b must be strings")

    # Only keys whose value is a list count as a real session; orphan
    # `session_N_date_time` keys (as in conv-26) are ignored here.
    session_keys: list[int] = []
    for key in conversation:
        m = SESSION_KEY_RE.match(key)
        if m and isinstance(conversation[key], list):
            session_keys.append(int(m.group(1)))

    sessions: list[Session] = []
    for num in sorted(session_keys):
        date_time_key = f"session_{num}_date_time"
        if date_time_key not in conversation:
            raise DatasetError(f"{sample_id}: missing {date_time_key}")
        date_time = conversation[date_time_key]
        if not isinstance(date_time, str):
            raise DatasetError(f"{sample_id}: {date_time_key} must be string")
        date = parse_date_time(date_time)
        turns = _parse_turns(sample_id, num, conversation[f"session_{num}"])
        sessions.append(Session(session_num=num, date_time=date_time, date=date, turns=turns))

    dia_id_set: set[str] = set()
    for session in sessions:
        for turn in session.turns:
            if turn.dia_id in dia_id_set:
                raise DatasetError(f"{sample_id}: duplicate dia_id {turn.dia_id!r}")
            dia_id_set.add(turn.dia_id)

    qa_raw = entry.get("qa")
    if not isinstance(qa_raw, list):
        raise DatasetError(f"{sample_id}: missing qa list")
    qa_list = _parse_qa(sample_id, qa_raw, dia_id_set)

    return Conversation(
        sample_id=sample_id,
        speaker_a=speaker_a,
        speaker_b=speaker_b,
        sessions=sessions,
        qa=qa_list,
        event_summary=entry.get("event_summary") or {},
        observation=entry.get("observation") or {},
        session_summary=entry.get("session_summary") or {},
    )


def _parse_turns(sample_id: str, session_num: int, turns_raw: Any) -> list[Turn]:
    if not isinstance(turns_raw, list):
        raise DatasetError(f"{sample_id}: session_{session_num} must be a list")
    turns: list[Turn] = []
    for turn in turns_raw:
        if not isinstance(turn, dict):
            raise DatasetError(f"{sample_id}: session_{session_num} turn must be object")
        dia_id = turn.get("dia_id")
        speaker = turn.get("speaker")
        text = turn.get("text")
        if not isinstance(dia_id, str) or not isinstance(speaker, str) or not isinstance(text, str):
            raise DatasetError(
                f"{sample_id}: session_{session_num} turn requires string dia_id/speaker/text"
            )
        m = DIA_ID_RE.fullmatch(dia_id)
        if not m:
            raise DatasetError(f"{sample_id}: malformed dia_id {dia_id!r}")
        turn_num = int(m.group(2))
        if int(m.group(1)) != session_num:
            raise DatasetError(
                f"{sample_id}: dia_id {dia_id!r} session does not match session_{session_num}"
            )
        img_urls = turn.get("img_url")
        if img_urls is not None and not isinstance(img_urls, list):
            raise DatasetError(f"{sample_id}: {dia_id} img_url must be a list")
        img_urls = [u for u in (img_urls or []) if isinstance(u, str)]
        turns.append(Turn(
            speaker=speaker,
            dia_id=dia_id,
            session_num=session_num,
            turn_num=turn_num,
            text=text,
            has_image=len(img_urls) > 0 or bool(turn.get("blip_caption")),
            blip_caption=turn.get("blip_caption") or "",
            img_urls=img_urls,
            query=turn.get("query") or "",
        ))
    return turns


def _parse_qa(sample_id: str, qa_raw: list, dia_id_set: set[str]) -> list[QA]:
    qa_list: list[QA] = []
    for i, item in enumerate(qa_raw):
        if not isinstance(item, dict):
            raise DatasetError(f"{sample_id}: qa {i} must be object")
        question = item.get("question")
        if not isinstance(question, str) or not question:
            raise DatasetError(f"{sample_id}: qa {i} missing question")
        category = item.get("category")
        if not isinstance(category, int) or category not in CATEGORY_LABELS:
            raise DatasetError(f"{sample_id}: qa {i} invalid category {category!r}")

        answer = item.get("answer")
        adversarial_answer = item.get("adversarial_answer")
        if not isinstance(adversarial_answer, (str, type(None))):
            raise DatasetError(f"{sample_id}: qa {i} adversarial_answer must be string")
        if answer is not None and not isinstance(answer, (str, int)):
            raise DatasetError(f"{sample_id}: qa {i} answer must be string/int/None")

        evidence_raw = item.get("evidence")
        if not isinstance(evidence_raw, list):
            raise DatasetError(f"{sample_id}: qa {i} evidence must be a list")
        evidence_raw = [e for e in evidence_raw if isinstance(e, str)]
        resolved, unresolved = _normalize_evidence(evidence_raw, dia_id_set)

        qa_list.append(QA(
            sample_id=sample_id,
            index=i,
            question=question,
            answer=answer,
            adversarial_answer=adversarial_answer,
            evidence_raw=evidence_raw,
            evidence=resolved,
            evidence_unresolved=unresolved,
            category=category,
        ))
    return qa_list


def validate_dataset(conversations: list[Conversation]) -> dict[str, Any]:
    """Compute aggregate validation statistics and flag anomalies."""
    sessions = 0
    turns = 0
    qa = 0
    categories: dict[int, int] = {}
    image_turns = 0
    caption_turns = 0
    empty_evidence = 0
    unresolved_evidence = 0
    adversarial_only_answer = 0
    both_answers = 0

    for conv in conversations:
        sessions += len(conv.sessions)
        for session in conv.sessions:
            turns += len(session.turns)
            for turn in session.turns:
                if turn.has_image:
                    image_turns += 1
                if turn.blip_caption:
                    caption_turns += 1
        for q in conv.qa:
            qa += 1
            categories[q.category] = categories.get(q.category, 0) + 1
            if not q.has_evidence:
                empty_evidence += 1
            if q.evidence_unresolved:
                unresolved_evidence += 1
            if q.is_adversarial and q.answer is None and q.adversarial_answer is not None:
                adversarial_only_answer += 1
            if q.answer is not None and q.adversarial_answer is not None:
                both_answers += 1

    return {
        "conversations": len(conversations),
        "sessions": sessions,
        "turns": turns,
        "qa": qa,
        "categories": categories,
        "image_turns": image_turns,
        "caption_turns": caption_turns,
        "empty_evidence": empty_evidence,
        "qa_with_unresolved_evidence": unresolved_evidence,
        "adversarial_answer_only": adversarial_only_answer,
        "qa_with_both_answers": both_answers,
        "expected_counts": EXPECTED_COUNTS,
    }
