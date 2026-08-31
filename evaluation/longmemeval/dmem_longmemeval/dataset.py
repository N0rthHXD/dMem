"""LongMemEval dataset loading and validation.

Parses the official LongMemEval JSON format, validates structural integrity,
sorts haystack sessions by timestamp, and computes a SHA-256 of the raw bytes
for run-identity and resume checks.
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path

QUESTION_TYPES = {
    "single-session-user",
    "single-session-assistant",
    "single-session-preference",
    "multi-session",
    "temporal-reasoning",
    "knowledge-update",
}

DATE_LAYOUT = "%Y/%m/%d (%a) %H:%M"


class DatasetError(ValueError):
    pass


@dataclass
class Turn:
    role: str
    content: str
    has_answer: bool = False


@dataclass
class Session:
    session_id: str
    date: datetime
    turns: list[Turn]


@dataclass
class Case:
    question_id: str
    question_type: str
    question: str
    answer: object  # str | list | dict
    answer_string: str
    question_date: datetime
    haystack_session_ids: list[str]
    haystack_dates: list[datetime]
    haystack_sessions: list[list[Turn]]
    answer_session_ids: list[str]

    @property
    def is_abstention(self) -> bool:
        return self.question_id.endswith("_abs")

    def sessions(self) -> list[Session]:
        """Return haystack sessions sorted by date ascending."""
        pairs = sorted(
            zip(self.haystack_dates, self.haystack_session_ids, self.haystack_sessions),
            key=lambda t: t[0],
        )
        return [
            Session(session_id=sid, date=d, turns=turns)
            for d, sid, turns in pairs
        ]


def parse_date(s: str) -> datetime:
    try:
        return datetime.strptime(s, DATE_LAYOUT)
    except ValueError as exc:
        raise DatasetError(f"invalid date {s!r}: {exc}") from exc


def _answer_to_string(answer: object) -> str:
    if isinstance(answer, str):
        return answer
    return json.dumps(answer, sort_keys=True, ensure_ascii=False)


def load_dataset(path: str | Path) -> tuple[list[Case], str]:
    """Load and validate a LongMemEval JSON file.

    Returns ``(cases, sha256_hex)`` where sha256_hex is the digest of the
    raw file bytes (used for run identity and resume checks).
    """
    raw = Path(path).read_bytes()
    sha = hashlib.sha256(raw).hexdigest()
    try:
        data = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise DatasetError(f"invalid JSON: {exc}") from exc
    if not isinstance(data, list):
        raise DatasetError("expected a JSON array of cases")
    cases: list[Case] = []
    seen_ids: set[str] = set()
    for i, entry in enumerate(data):
        if not isinstance(entry, dict):
            raise DatasetError(f"case {i}: expected object, got {type(entry)}")
        case = _validate_case(entry, i, seen_ids)
        cases.append(case)
    return cases, sha


def _validate_case(entry: dict, index: int, seen_ids: set[str]) -> Case:
    for field_name in (
        "question_id",
        "question_type",
        "question",
        "answer",
        "question_date",
        "haystack_session_ids",
        "haystack_dates",
        "haystack_sessions",
        "answer_session_ids",
    ):
        if field_name not in entry:
            raise DatasetError(
                f"case {index}: missing required field {field_name!r}"
            )

    qid = entry["question_id"]
    if not isinstance(qid, str) or not qid:
        raise DatasetError(f"case {index}: question_id must be non-empty string")
    if qid in seen_ids:
        raise DatasetError(f"case {index}: duplicate question_id {qid!r}")
    seen_ids.add(qid)

    qtype = entry["question_type"]
    if qtype not in QUESTION_TYPES:
        raise DatasetError(
            f"case {index} ({qid}): invalid question_type {qtype!r}"
        )

    question = entry["question"]
    if not isinstance(question, str) or not question:
        raise DatasetError(f"case {index} ({qid}): question must be non-empty string")

    answer = entry["answer"]
    answer_string = _answer_to_string(answer)

    question_date = parse_date(entry["question_date"])

    session_ids = entry["haystack_session_ids"]
    dates_raw = entry["haystack_dates"]
    sessions_raw = entry["haystack_sessions"]
    answer_session_ids = entry["answer_session_ids"]

    if not isinstance(session_ids, list) or not session_ids:
        raise DatasetError(f"case {index} ({qid}): haystack_session_ids must be non-empty list")
    if not isinstance(dates_raw, list) or len(dates_raw) != len(session_ids):
        raise DatasetError(
            f"case {index} ({qid}): haystack_dates length must match haystack_session_ids"
        )
    if not isinstance(sessions_raw, list) or len(sessions_raw) != len(session_ids):
        raise DatasetError(
            f"case {index} ({qid}): haystack_sessions length must match haystack_session_ids"
        )
    if not isinstance(answer_session_ids, list):
        raise DatasetError(f"case {index} ({qid}): answer_session_ids must be a list")

    dates = [parse_date(d) for d in dates_raw]

    sessions: list[list[Turn]] = []
    session_id_set = set(session_ids)
    for j, (sid, sess_turns) in enumerate(zip(session_ids, sessions_raw)):
        if not isinstance(sid, str) or not sid:
            raise DatasetError(f"case {index} ({qid}): session id {j} must be non-empty string")
        if not isinstance(sess_turns, list):
            raise DatasetError(f"case {index} ({qid}): session {j} must be a list of turns")
        turns: list[Turn] = []
        for turn in sess_turns:
            if not isinstance(turn, dict):
                raise DatasetError(f"case {index} ({qid}): session {j} turn must be object")
            role = turn.get("role")
            if role not in ("user", "assistant"):
                raise DatasetError(
                    f"case {index} ({qid}): session {j} turn role must be user/assistant, got {role!r}"
                )
            content = turn.get("content", "")
            if not isinstance(content, str):
                raise DatasetError(f"case {index} ({qid}): session {j} turn content must be string")
            has_answer = bool(turn.get("has_answer", False))
            turns.append(Turn(role=role, content=content, has_answer=has_answer))
        sessions.append(turns)

    for asid in answer_session_ids:
        if asid not in session_id_set:
            raise DatasetError(
                f"case {index} ({qid}): answer_session_id {asid!r} not in haystack"
            )

    return Case(
        question_id=qid,
        question_type=qtype,
        question=question,
        answer=answer,
        answer_string=answer_string,
        question_date=question_date,
        haystack_session_ids=session_ids,
        haystack_dates=dates,
        haystack_sessions=sessions,
        answer_session_ids=answer_session_ids,
    )
