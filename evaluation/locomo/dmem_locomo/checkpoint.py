"""Append-only checkpoint for resume support.

LoCoMo records are keyed by ``qa_id`` and its conversations by ``sample_id``.
"""

from __future__ import annotations

from dmem_eval.checkpoint import Checkpoint as _Checkpoint

__all__ = ["Checkpoint"]


class Checkpoint(_Checkpoint):
    id_field = "qa_id"
    ingest_id_field = "sample_id"
    ingest_complete_type = "conversation_complete"
