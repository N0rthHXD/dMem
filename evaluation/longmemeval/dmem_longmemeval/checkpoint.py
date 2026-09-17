"""Append-only checkpoint for resume support.

LongMemEval records are keyed by ``question_id`` and its cases by
``question_id``; a completed case is marked ``case_complete``.
"""

from __future__ import annotations

from dmem_eval.checkpoint import Checkpoint as _Checkpoint

__all__ = ["Checkpoint"]


class Checkpoint(_Checkpoint):
    id_field = "question_id"
    ingest_id_field = "question_id"
    ingest_complete_type = "case_complete"
