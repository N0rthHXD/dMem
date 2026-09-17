"""dMem HTTP API client over Unix domain socket.

Thin benchmark-local re-export of the shared client in :mod:`dmem_eval.api`.
"""

from __future__ import annotations

from dmem_eval.api import DEFAULT_TIMEOUT, MAX_RETRIES, DMemClient, DMemError

__all__ = ["DEFAULT_TIMEOUT", "MAX_RETRIES", "DMemClient", "DMemError"]
