"""Parallel answer-generation frame shared by benchmark runners.

Benchmarks own their dataset readers and prompt construction; this module owns
the thread pool, progress bar, failure aggregation, and result persistence so
that the concurrency contract is written and tested once.
"""

from __future__ import annotations

import sys
from collections.abc import Callable, Sequence
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Any

from .progress import tqdm

GenerateResult = tuple[dict[str, Any], str, int]


def run_generation(
    *,
    total: int,
    todo: Sequence[Any],
    generate: Callable[[Any], GenerateResult],
    on_record: Callable[[Any, dict[str, Any], str, int], None],
    id_field: str,
    workers: int,
    thread_name_prefix: str,
    desc: str = "generate",
) -> int:
    """Run ``generate`` for each item in ``todo`` in parallel.

    Returns the number of successful records. The first error encountered is
    re-raised after every in-flight item settles, preserving the prior
    fail-closed behaviour while still writing the successes that completed.
    """
    if isinstance(workers, bool) or not isinstance(workers, int) or workers < 1:
        raise ValueError("workers must be a positive integer")

    count = 0
    initial = max(0, total - len(todo))
    first_error: Exception | None = None
    with tqdm(
        total=total,
        initial=initial,
        desc=desc,
        unit="q",
        file=sys.stderr,
    ) as bar:
        with ThreadPoolExecutor(
            max_workers=workers, thread_name_prefix=thread_name_prefix
        ) as executor:
            futures = {executor.submit(generate, item): item for item in todo}
            for future in as_completed(futures):
                try:
                    record, answer, tokens = future.result()
                except Exception as exc:
                    if first_error is None:
                        first_error = exc
                else:
                    on_record(
                        futures[future], record, answer, tokens
                    )
                    count += 1
                    bar.set_postfix_str(
                        f"q={record[id_field]} tokens={tokens} generated={count}"
                    )
                bar.update(1)

    if first_error is not None:
        raise first_error
    return count
