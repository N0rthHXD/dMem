from dmem_calibration import RerankWindowResult, compare_reranking, promotion_allowed


def windows(count=60, learned_better=True):
    result = []
    for index in range(count):
        deterministic = ["a", "b", "c"]
        learned = ["b", "a", "c"] if learned_better else ["c", "a", "b"]
        result.append(
            RerankWindowResult(
                deterministic_order=deterministic,
                learned_order=learned,
                relevant=["b"],
                deterministic_latency_ms=10.0,
                learned_latency_ms=11.0,
            )
        )
    return result


def test_comparison_is_paired_and_promotion_blocks_regression():
    evidence = compare_reranking(windows(learned_better=True))
    assert evidence["window_count"] == 60
    assert evidence["mean_rank_improvement"] > 0
    assert evidence["degradation_count"] == 0
    decision = promotion_allowed(evidence)
    assert decision["promoted"] is True

    regressing = compare_reranking(windows(learned_better=False))
    assert regressing["degradation_count"] > 0
    blocked = promotion_allowed(regressing)
    assert blocked["promoted"] is False
    assert "degradation" in blocked["reasons"]


def test_promotion_requires_enough_windows_and_bounded_latency():
    evidence = compare_reranking(windows(count=5, learned_better=True))
    decision = promotion_allowed(evidence)
    assert decision["promoted"] is False
    assert "insufficient_windows" in decision["reasons"]

    slow = compare_reranking(
        [
            RerankWindowResult(["a", "b"], ["b", "a"], ["b"], 10.0, 100.0)
            for _ in range(60)
        ]
    )
    slow_decision = promotion_allowed(slow)
    assert slow_decision["promoted"] is False
    assert "latency" in slow_decision["reasons"]
