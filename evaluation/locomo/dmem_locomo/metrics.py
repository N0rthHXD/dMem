"""Retrieval metrics and official LoCoMo answer scoring.

Two independent concerns live here:

1. Turn- and session-level retrieval metrics over evidence references
   (``recall_any``, ``recall_all``, ``coverage``, ``mrr``, binary ``nDCG``).
2. A faithful port of the official LoCoMo F1 answer scoring in
   ``snap-research/locomo/task_eval/evaluation.py`` (token F1 with Porter
   stemming, multi-answer comma handling, and category-5 abstention checks).
"""

from __future__ import annotations

import math
import re
import string
import unicodedata
from collections import Counter
from typing import Any, Sequence

import numpy as np

from .dataset import CATEGORY_LABELS, Conversation, QA, _session_num_from_dia_id

try:
    from nltk.stem import PorterStemmer
    _stemmer = PorterStemmer()
except ImportError:  # pragma: no cover - optional dependency
    def _stemmer(word: str) -> str:  # type: ignore[misc]
        return word


# --------------------------------------------------------------------------
# retrieval metrics
# --------------------------------------------------------------------------

def recall_any(ranked: Sequence[str], gold: set[str], k: int) -> float:
    return float(any(d in gold for d in ranked[:k]))


def recall_all(ranked: Sequence[str], gold: set[str], k: int) -> float:
    if not gold:
        return 0.0
    return float(all(d in gold for d in ranked[:k]))


def coverage(ranked: Sequence[str], gold: set[str], k: int) -> float:
    if not gold:
        return 1.0
    return float(len(set(ranked[:k]) & gold) / len(gold))


def mrr(ranked: Sequence[str], gold: set[str]) -> float:
    for i, d in enumerate(ranked, start=1):
        if d in gold:
            return 1.0 / i
    return 0.0


def ndcg(ranked: Sequence[str], gold: set[str], k: int) -> float:
    """Binary-relevance normalized DCG over the ranked evidence list."""
    rel = [1 if d in gold else 0 for d in ranked[:k]]
    ideal = sorted(rel, reverse=True)
    idcg = ideal[0] + sum(ideal[i] / math.log2(i + 2) for i in range(1, len(ideal)))
    if idcg == 0:
        return 0.0
    dcg = rel[0] + sum(rel[i] / math.log2(i + 2) for i in range(1, len(rel)))
    return float(dcg / idcg)


def evaluate_retrieval(ranked: Sequence[str], gold: set[str], k: int) -> dict[str, float]:
    return {
        "recall_any": recall_any(ranked, gold, k),
        "recall_all": recall_all(ranked, gold, k),
        "coverage": coverage(ranked, gold, k),
        "ndcg": ndcg(ranked, gold, k),
        "mrr": mrr(ranked, gold),
    }


def promote_to_sessions(dia_ids: Sequence[str]) -> list[str]:
    """Promote turn-level evidence ids (``D<s>:<t>``) to session ids (``S<s>``)."""
    out: list[str] = []
    seen: set[str] = set()
    for dia_id in dia_ids:
        try:
            session = f"S{_session_num_from_dia_id(dia_id)}"
        except ValueError:
            continue
        if session not in seen:
            seen.add(session)
            out.append(session)
    return out


def aggregate_metrics(
    per_case: list[dict[str, float]],
    metric_names: Sequence[str],
) -> dict[str, float]:
    """Macro-average a list of per-case metric dicts."""
    if not per_case:
        return {name: 0.0 for name in metric_names}
    result: dict[str, float] = {}
    for name in metric_names:
        values = [entry[name] for entry in per_case if name in entry]
        result[name] = float(np.mean(values)) if values else 0.0
    return result


# --------------------------------------------------------------------------
# official LoCoMo answer scoring (ported from task_eval/evaluation.py)
# --------------------------------------------------------------------------

_ARTICLE_RE = re.compile(r"\b(a|an|the|and)\b")


def normalize_answer(s: Any) -> str:
    s = str(s) if not isinstance(s, str) else s

    def remove_articles(text: str) -> str:
        return _ARTICLE_RE.sub(" ", text)

    def white_space_fix(text: str) -> str:
        return " ".join(text.split())

    def remove_punc(text: str) -> str:
        exclude = set(string.punctuation)
        return "".join(ch for ch in text if ch not in exclude)

    def lower(text: str) -> str:
        return text.lower()

    return white_space_fix(remove_articles(remove_punc(lower(s))))


def _stem(text: str) -> str:
    return " ".join(_stemmer(w) for w in text.split())


def exact_match_score(prediction: str, ground_truth: str) -> bool:
    prediction = normalize_answer(prediction)
    ground_truth = normalize_answer(ground_truth)
    return set(prediction.split()) == set(ground_truth.split())


def f1_score(prediction: str, ground_truth: str) -> float:
    prediction_tokens = normalize_answer(prediction).split()
    ground_truth_tokens = normalize_answer(ground_truth).split()
    prediction_tokens = [_stemmer(w) for w in prediction_tokens]
    ground_truth_tokens = [_stemmer(w) for w in ground_truth_tokens]
    common = Counter(prediction_tokens) & Counter(ground_truth_tokens)
    num_same = sum(common.values())
    if num_same == 0:
        return 0.0
    precision = 1.0 * num_same / len(prediction_tokens)
    recall = 1.0 * num_same / len(ground_truth_tokens)
    f1 = (2 * precision * recall) / (precision + recall)
    return f1


def f1_multi(prediction: str, ground_truth: str) -> float:
    predictions = [p.strip() for p in prediction.split(",")]
    ground_truths = [g.strip() for g in ground_truth.split(",")]
    return float(np.mean([
        max([f1_score(p, gt) for p in predictions]) for gt in ground_truths
    ]))


def rougel_score(prediction: str, ground_truth: str) -> float:
    prediction = _stem(normalize_answer(prediction))
    ground_truth = _stem(normalize_answer(ground_truth))
    if not prediction or not ground_truth:
        return 0.0
    p_tokens = set(prediction.split())
    g_tokens = set(ground_truth.split())
    common = p_tokens & g_tokens
    if not common:
        return 0.0
    return float(len(common) / len(g_tokens))


def score_qa(prediction: str, qa: QA) -> float:
    """Official per-question score, mirroring ``eval_question_answering``."""
    answer = qa.answer_string
    if qa.category == 3:
        answer = answer.split(";")[0].strip()

    output = prediction if isinstance(prediction, str) else str(prediction)

    if qa.category in (2, 3, 4):
        return f1_score(output, answer)
    elif qa.category == 1:
        return f1_multi(output, answer)
    elif qa.category == 5:
        lowered = output.lower()
        if "no information available" in lowered or "not mentioned" in lowered:
            return 1.0
        return 0.0
    raise ValueError(f"unexpected category {qa.category!r}")


def score_all(
    conversations: list[Conversation],
    predictions: dict[str, str],
) -> dict[str, Any]:
    """Aggregate official F1 over all QA with available predictions.

    ``predictions`` maps ``qa_id`` -> model prediction. Returns overall,
    per-category, per-conversation, and per-evidence-count breakdowns.
    """
    overall: list[float] = []
    per_category: dict[int, list[float]] = {}
    per_conversation: dict[str, list[float]] = {}
    per_evidence_bucket: dict[str, list[float]] = {}

    for conv in conversations:
        for qa in conv.qa:
            if qa.qa_id not in predictions:
                continue
            score = score_qa(predictions[qa.qa_id], qa)
            overall.append(score)
            per_category.setdefault(qa.category, []).append(score)
            per_conversation.setdefault(conv.sample_id, []).append(score)
            n = len(qa.evidence)
            bucket = "1" if n == 1 else ("2" if n == 2 else "3+") if n else "none"
            per_evidence_bucket.setdefault(bucket, []).append(score)

    def _agg(values: list[float]) -> dict[str, float]:
        return {"mean": float(np.mean(values)), "n": len(values)} if values else {"mean": 0.0, "n": 0}

    return {
        "overall": _agg(overall),
        "per_category": {
            CATEGORY_LABELS.get(k, str(k)): _agg(v)
            for k, v in sorted(per_category.items())
        },
        "per_conversation": {k: _agg(v) for k, v in sorted(per_conversation.items())},
        "per_evidence_bucket": {k: _agg(v) for k, v in sorted(per_evidence_bucket.items())},
        "macro_category_mean": float(np.mean([
            np.mean(v) for v in per_category.values() if v
        ])) if per_category else 0.0,
        "macro_conversation_mean": float(np.mean([
            np.mean(v) for v in per_conversation.values() if v
        ])) if per_conversation else 0.0,
    }
