"""Progress display with a no-op fallback when tqdm is unavailable."""

from __future__ import annotations

from dmem_eval.progress import tqdm

__all__ = ["tqdm"]
