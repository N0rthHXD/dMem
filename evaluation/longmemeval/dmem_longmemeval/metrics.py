"""Retrieval metrics — strict port of upstream ``eval_utils.py``.

Implements ``recall_any``, ``recall_all``, and binary ``nDCG`` at arbitrary
cutoffs, plus the turn-to-session promotion helper, exactly as in the official
LongMemEval repository (``src/retrieval/eval_utils.py``).
"""

from __future__ import annotations

import math
from typing import Sequence

import numpy as np

METRIC_SCHEMA_VERSION = "longmemeval-retrieval-metrics-v2"


def dcg(relevances: Sequence[float], k: int) -> float:
    """Discounted Cumulative Gain at k (upstream formula)."""
    rel = np.asarray(relevances, dtype=float)[:k]
    if rel.size == 0:
        return 0.0
    score = float(rel[0])
    for i in range(1, rel.size):
        score += float(rel[i]) / math.log2(i + 1)
    return score


def ndcg(
    rankings: Sequence[int],
    correct_docs: set[str],
    corpus_ids: Sequence[str],
    k: int = 10,
) -> float:
    """Normalized DCG at k with binary relevance (upstream formula)."""
    relevances = [1 if doc_id in correct_docs else 0 for doc_id in corpus_ids]
    sorted_relevances = [relevances[idx] for idx in rankings[:k]]
    ideal_relevance = sorted(relevances, reverse=True)
    ideal_dcg = dcg(ideal_relevance, k)
    actual_dcg = dcg(sorted_relevances, k)
    if ideal_dcg == 0:
        return 0.0
    return actual_dcg / ideal_dcg


def evaluate_retrieval(
    rankings: Sequence[int],
    correct_docs: set[str],
    corpus_ids: Sequence[str],
    k: int = 10,
) -> tuple[float, float, float, float]:
    """Return (recall_any, recall_all, ndcg, MRR) at cutoff k.

    ``rankings`` is a list of corpus indices ordered by retrieval score
    descending. ``correct_docs`` is the set of relevant corpus IDs.
    ``corpus_ids`` is the full ordered list of corpus IDs.
    """
    recalled = {corpus_ids[idx] for idx in rankings[:k]}
    recall_any = float(any(doc in recalled for doc in correct_docs))
    recall_all = float(all(doc in recalled for doc in correct_docs))
    ndcg_score = ndcg(rankings, correct_docs, corpus_ids, k)
    reciprocal_rank = 0.0
    for rank, index in enumerate(rankings[:k], 1):
        if corpus_ids[index] in correct_docs:
            reciprocal_rank = 1.0 / rank
            break
    return recall_any, recall_all, ndcg_score, reciprocal_rank


def evaluate_retrieval_turn2session(
    rankings: Sequence[int],
    correct_docs: set[str],
    corpus_ids: Sequence[str],
    k: int = 10,
) -> tuple[float, float, float]:
    """Promote turn-level results to session-level, then evaluate.

    Strips the trailing ``_N`` turn index from each corpus_id, deduplicates
    sessions, and extends the effective cutoff until ``k`` unique sessions
    are found (matching upstream behaviour exactly).
    """

    def strip_turn_id(doc_id: str) -> str:
        return "_".join(doc_id.split("_")[:-1])

    correct_sessions = {strip_turn_id(x) for x in correct_docs}
    promoted_corpus = [strip_turn_id(x) for x in corpus_ids]

    effective_k = k
    unique_sessions: set[str] = set()
    for idx in rankings[:effective_k]:
        if idx < len(promoted_corpus):
            unique_sessions.add(promoted_corpus[idx])
    while effective_k < len(promoted_corpus) and len(unique_sessions) < k:
        effective_k += 1
        idx = rankings[effective_k - 1] if effective_k - 1 < len(rankings) else effective_k - 1
        if idx < len(promoted_corpus):
            unique_sessions.add(promoted_corpus[idx])

    return evaluate_retrieval(rankings, correct_sessions, promoted_corpus, k=effective_k)


def aggregate_metrics(
    per_case: list[dict[str, dict[str, float]]],
    cutoffs: list[int],
    *,
    skip_qids: set[str] | None = None,
) -> dict[str, dict[str, float]]:
    """Macro-average retrieval metrics across cases at each cutoff.

    ``per_case`` is a list of ``{qid: {cutoff_str: {metric: value}}}``.
    Returns ``{cutoff_str: {metric: macro_mean}}``.
    """
    skip = skip_qids or set()
    result: dict[str, dict[str, float]] = {}
    if not per_case:
        return result
    metric_names = ["recall_any", "recall_all", "ndcg", "mrr"]
    for k in cutoffs:
        key = f"@{k}"
        result[key] = {}
        for metric in metric_names:
            values = []
            for entry in per_case:
                for qid, metrics in entry.items():
                    if qid in skip:
                        continue
                    if key in metrics and metric in metrics[key]:
                        values.append(metrics[key][metric])
            if values:
                result[key][metric] = float(np.mean(values))
            else:
                result[key][metric] = 0.0
    return result
