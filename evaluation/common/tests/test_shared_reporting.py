"""Shared ablation statistics, bootstrap, and promotion gate engine."""

import pytest

from dmem_eval.reporting import (
    RecordSchema,
    ReportingError,
    ablation_request,
    aggregate_repetitions,
    build_end_to_end_report,
    mean,
    paired_bootstrap_ci,
    promotion_gates,
    query_metrics,
    run_metrics,
    stddev,
)

SCHEMA = RecordSchema(
    id_field="qid",
    category_field="cat",
    metrics_layer="m",
    recall_metric="rec",
    recall_at_1_metrics=("rec1", "rec"),
    mrr_metrics=("mrr",),
    evidence_field="gold",
    report_schema="test-ablation-report-v1",
)


def record(qid, cat, rec, rec1, mrr=0.0):
    return {
        "qid": qid,
        "cat": cat,
        "gold": ["x"],
        "m": {
            "@10": {"rec": rec, "mrr": mrr, "ndcg": mrr},
            "@1": {"rec1": rec1, "mrr": mrr},
        },
        "ranked_items": [],
        "duplicate_rate": 0.0,
        "safety_violations": 0,
    }


def test_ablation_request_maps_cells():
    assert ablation_request("S") == {
        "capabilities": ["semantic"], "graph_policy": "off", "diversification": False
    }
    assert ablation_request("SKF")["graph_policy"] == "features_only"
    assert ablation_request("SKPM")["diversification"] is True
    with pytest.raises(ReportingError):
        ablation_request("nope")


def test_sample_statistics_and_bootstrap():
    assert mean([1.0, 2.0, 3.0]) == 2.0
    assert stddev([5.0]) == 0.0
    assert stddev([2.0, 4.0]) == pytest.approx(1.4142135623730951)
    a = [1.0, 2.0, 3.0, 4.0]
    b = [0.5, 1.5, 2.5, 3.5]
    assert paired_bootstrap_ci(a, b, seed="fixed") == paired_bootstrap_ci(a, b, seed="fixed")
    with pytest.raises(ReportingError):
        paired_bootstrap_ci([1.0], [1.0, 2.0])


def test_query_metrics_reads_configured_layer_and_fallbacks():
    metrics = query_metrics(record("q1", "a", rec=0.0, rec1=0.5), SCHEMA)
    assert metrics["recall"] == 0.0
    assert metrics["recall_at_1"] == 0.5
    assert metrics["ranking"] == 0.0


def test_aggregate_requires_three_repetitions():
    runs = [[record("q1", "a", 0.4, 0.4)], [record("q1", "a", 0.6, 0.6)]]
    with pytest.raises(ReportingError, match="at least 3"):
        aggregate_repetitions(runs, SCHEMA)
    aggregate = aggregate_repetitions([*runs, [record("q1", "a", 0.8, 0.8)]], SCHEMA)
    assert aggregate["repetitions"] == 3
    assert aggregate["metrics"]["recall"]["mean"] == pytest.approx(0.6)


def test_run_metrics_exposes_cost_aliases():
    metrics = run_metrics([record("q1", "a", 0.5, 0.5, mrr=0.25)], SCHEMA)
    assert metrics["recall"] == pytest.approx(0.5)
    assert metrics["ranking"] == pytest.approx(0.25)
    assert metrics["latency"] == metrics["latency_ms"]


def test_build_report_and_promotion_gates_round_trip():
    runs = {
        "S": [[record("q1", "a", 0.5, 0.5), record("q2", "b", 0.6, 0.6)]] * 3,
        "SKPM": [[record("q1", "a", 0.55, 0.55), record("q2", "b", 0.6, 0.6)]] * 3,
    }
    report = build_end_to_end_report(runs=runs, schema=SCHEMA, baseline_cell="S")
    assert report["schema"] == "test-ablation-report-v1"
    assert set(report["cells"]) == {"S", "SKPM"}
    gates = promotion_gates(report, SCHEMA)
    assert gates["safety_measured"] is True
    assert isinstance(gates["passes"], bool)
    assert "graph_recall_regression" in gates
