"""Unit tests for LoCoMo retrieval metrics.

These tests pin the corrected metric semantics required by Phase 0:

* ``recall_all@k`` is ``gold <= set(top_k)``.
* nDCG uses an ideal DCG built from ``min(k, len(gold))`` relevant items.
* MRR is available both as a full-list metric and an explicitly truncated
  ``MRR@k`` without reporting ambiguous repeated values.
"""

from dmem_locomo.metrics import (
    METRIC_SCHEMA_VERSION,
    coverage,
    evaluate_retrieval,
    mrr,
    mrr_at_k,
    ndcg,
    promote_to_sessions,
    recall_all,
    recall_any,
)


# --------------------------------------------------------------- recall_all --


def test_recall_all_true_when_all_gold_in_top_k():
    assert recall_all(["D1:1", "D1:2", "x"], {"D1:1", "D1:2"}, 2) == 1.0


def test_recall_all_false_for_empty_ranking_with_gold():
    assert recall_all([], {"D1:1"}, 5) == 0.0


def test_recall_all_requires_every_gold_item():
    assert recall_all(["D1:1", "D1:3"], {"D1:1", "D1:2"}, 5) == 0.0


def test_recall_all_ignores_irrelevant_additions():
    baseline = recall_all(["D1:1", "D1:2"], {"D1:1", "D1:2"}, 2)
    with_noise = recall_all(["junk", "D1:1", "D1:2", "more"], {"D1:1", "D1:2"}, 4)
    assert baseline == 1.0
    assert with_noise == 1.0


def test_recall_all_is_monotonic_in_k():
    ranked = ["D1:1", "noise", "D1:2", "D1:3"]
    gold = {"D1:1", "D1:2", "D1:3"}
    scores = [recall_all(ranked, gold, k) for k in range(1, len(ranked) + 1)]
    assert scores == sorted(scores)
    assert scores[-1] == 1.0


def test_recall_all_not_confused_with_precision():
    # Every retrieved item is gold, but the complete gold set is not retrieved.
    assert recall_all(["D1:1"], {"D1:1", "D1:2"}, 1) == 0.0


def test_recall_any_empty_ranking_is_zero():
    assert recall_any([], {"D1:1"}, 5) == 0.0


def test_coverage_partial_gold():
    assert coverage(["D1:1", "junk"], {"D1:1", "D1:2"}, 2) == 0.5


# --------------------------------------------------------------------- nDCG --


def test_ndcg_perfect_when_all_gold_ranked_first():
    assert ndcg(["D1:1", "D1:2"], {"D1:1", "D1:2"}, 2) == 1.0


def test_ndcg_missing_gold_lowers_score():
    perfect = ndcg(["D1:1", "D1:2"], {"D1:1", "D1:2"}, 2)
    missing = ndcg(["D1:1"], {"D1:1", "D1:2"}, 2)
    assert missing < perfect


def test_ndcg_ideal_uses_min_k_gold_count():
    # Two relevant items are perfect at k=2 because len(gold) >= k.
    assert ndcg(["D1:1", "D1:2"], {"D1:1", "D1:2", "D1:3"}, 2) == 1.0
    # One relevant item retrieved out of three gold at k=2 is not perfect.
    assert ndcg(["D1:1", "x"], {"D1:1", "D1:2", "D1:3"}, 2) < 1.0


def test_ndcg_empty_gold_or_ranking_is_zero():
    assert ndcg([], {"D1:1"}, 5) == 0.0
    assert ndcg(["D1:1"], set(), 5) == 0.0


def test_ndcg_never_exceeds_one_with_duplicates():
    assert ndcg(["D1:1", "D1:1"], {"D1:1"}, 2) <= 1.0


# ---------------------------------------------------------------------- MRR --


def test_mrr_full_list_uses_whole_ranking():
    assert mrr(["x", "y", "D1:1"], {"D1:1"}) == 1.0 / 3.0


def test_mrr_at_k_truncates():
    ranked = ["x", "D1:1"]
    assert mrr_at_k(ranked, {"D1:1"}, 1) == 0.0
    assert mrr_at_k(ranked, {"D1:1"}, 2) == 0.5


def test_evaluate_retrieval_reports_distinct_cutoff_mrr():
    ranked = ["x", "D1:1"]
    at_one = evaluate_retrieval(ranked, {"D1:1"}, 1)
    at_two = evaluate_retrieval(ranked, {"D1:1"}, 2)
    assert at_one["mrr_at_k"] != at_two["mrr_at_k"]
    assert "mrr_at_k" in at_one
    assert "mrr" not in at_one


def test_evaluate_retrieval_uses_corrected_recall_all():
    # Every retrieved entry is gold, but gold is incomplete: old precision-like
    # semantics returned 1.0 here.
    assert evaluate_retrieval(["D1:1"], {"D1:1", "D1:2"}, 1)["recall_all"] == 0.0


def test_metric_schema_identity_is_explicit():
    assert isinstance(METRIC_SCHEMA_VERSION, str)
    assert METRIC_SCHEMA_VERSION


# ------------------------------------------------------ session promotion --


def test_promote_to_sessions_dedupes_and_preserves_order():
    assert promote_to_sessions(["D2:1", "D10:3", "D2:2"]) == ["S2", "S10"]
