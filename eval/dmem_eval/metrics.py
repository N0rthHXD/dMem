"""Retrieval metrics for the LoCoMo evaluation.

Two granularities are reported for every cutoff:

* ``memory`` - a retrieved memory counts as relevant when any of the dialogue
  turns it cites is a gold evidence turn. This measures ranking quality.
* ``turn``   - how much of the gold evidence set the top-k memories cover. This
  measures whether the retrieved context actually contains the answer turns.
"""

from __future__ import annotations

import math
from collections import defaultdict
from typing import Any, Iterable


def _dcg(relevances: list[int]) -> float:
    return sum(rel / math.log2(position + 2) for position, rel in enumerate(relevances))


def evaluate_retrieval(records: list[dict[str, Any]], cutoffs: Iterable[int]) -> dict[str, Any]:
    usable = [record for record in records if record.get("gold")]
    cutoffs = sorted({int(cutoff) for cutoff in cutoffs})
    if not usable:
        return {"usable_queries": 0, "skipped_queries": len(records), "cutoffs": cutoffs}

    per_cutoff: dict[int, dict[str, list[float]]] = {
        cutoff: defaultdict(list) for cutoff in cutoffs
    }
    by_category: dict[str, dict[int, dict[str, list[float]]]] = defaultdict(
        lambda: {cutoff: defaultdict(list) for cutoff in cutoffs}
    )

    for record in usable:
        gold = set(record["gold"])
        ranked = record.get("ranked") or []
        ranked_dia_sets = [set(item.get("dia_ids") or []) for item in ranked]
        relevance = [1 if dia_set & gold else 0 for dia_set in ranked_dia_sets]
        category = str(record.get("category"))
        for cutoff in cutoffs:
            window = ranked_dia_sets[:cutoff]
            covered: set[str] = set()
            for dia_set in window:
                covered |= dia_set & gold
            hit = 1.0 if covered else 0.0
            complete = 1.0 if gold <= covered else 0.0
            coverage = len(covered) / len(gold)
            mrr = 0.0
            for position, value in enumerate(relevance[:cutoff], start=1):
                if value:
                    mrr = 1.0 / position
                    break
            ideal_hits = min(sum(relevance), cutoff)
            idcg = _dcg([1] * ideal_hits)
            ndcg = (_dcg(relevance[:cutoff]) / idcg) if idcg > 0 else 0.0
            values = {
                "hit": hit,
                "all": complete,
                "coverage": coverage,
                "mrr": mrr,
                "ndcg": ndcg,
            }
            for name, value in values.items():
                per_cutoff[cutoff][name].append(value)
                by_category[category][cutoff][name].append(value)

    def summarize(bucket: dict[str, list[float]]) -> dict[str, float]:
        return {
            name: round(sum(values) / len(values), 6)
            for name, values in bucket.items()
            if values
        }

    result: dict[str, Any] = {
        "usable_queries": len(usable),
        "skipped_queries": len(records) - len(usable),
        "cutoffs": cutoffs,
        "overall": {
            f"@{cutoff}": summarize(per_cutoff[cutoff]) for cutoff in cutoffs
        },
        "by_category": {
            category: {f"@{cutoff}": summarize(buckets[cutoff]) for cutoff in cutoffs}
            for category, buckets in sorted(by_category.items())
        },
    }
    return result
