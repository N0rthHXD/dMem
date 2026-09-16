import importlib.util
import json
from pathlib import Path

import pytest

from dmem_locomo.reporting import (
    ABLATION_MATRIX,
    GRAPH_FAMILY_ABLATIONS,
    ReportingError,
    ablation_request,
    aggregate_repetitions,
    assert_identical_query_coverage,
    build_end_to_end_report,
    category_deltas,
    evidence_buckets,
    graph_promotion_report,
    mean,
    paired_bootstrap_ci,
    promotion_gates,
    run_metrics,
    stage_reports,
    stddev,
)


def _load_ablation_report():
    path = Path(__file__).resolve().parents[1] / "ablation_report.py"
    spec = importlib.util.spec_from_file_location("locomo_ablation_report", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


ablation_report = _load_ablation_report()


def record(
    qa_id,
    category,
    recall,
    ndcg,
    *,
    gold=("d1",),
    gold_memory_ids=("m1",),
    ranked=(("m1", ["d1"], ["keyword"], ["keyword_match"]),),
    latency=10.0,
    response_bytes=100.0,
):
    items = []
    for memory_id, dia_ids, channels, families in ranked:
        items.append(
            {
                "memory_id": memory_id,
                "dia_ids": list(dia_ids),
                "matched_channels": list(channels),
                "report": {"signal_family": list(families)},
            }
        )
    return {
        "qa_id": qa_id,
        "category": category,
        "gold_evidence": list(gold),
        "gold_memory_ids": list(gold_memory_ids),
        "duplicate_rate": 0.2,
        "recall_latency_ms": latency,
        "response_bytes": response_bytes,
        "ranked_items": items,
        "turn_metrics": {"@10": {"coverage": recall, "ndcg": ndcg}},
    }


def write_run(directory, records, **manifest_overrides):
    directory.mkdir(parents=True, exist_ok=True)
    (directory / "retrieval.jsonl").write_text(
        "\n".join(json.dumps(item) for item in records) + "\n", encoding="utf-8"
    )
    for stage in ("generation", "judge"):
        (directory / f"{stage}.jsonl").write_text(
            "\n".join(json.dumps({"qa_id": item["qa_id"], "prompt_tokens": 3, "completion_tokens": 2}) for item in records) + "\n",
            encoding="utf-8",
        )
    manifest = {
        "corpus_id": "fresh-1", "dataset_sha": "sha", "answer_model": "m",
        "generation_prompt": "p", "generation_temperature": 0.0, "generation_max_tokens": 100,
        "judge_model": "j", "judge_prompt": "jp", "judge_temperature": 0.0, "judge_max_tokens": 100,
    }
    manifest.update(manifest_overrides)
    (directory / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")


def test_ablation_report_cli_requires_all_cells_and_qa_checkpoints(tmp_path):
    specs = []
    for cell in ABLATION_MATRIX:
        for repetition in range(3):
            directory = tmp_path / f"{cell}-r{repetition}"
            write_run(directory, [record("q1", 1, 0.9, 0.9), record("q2", 1, 0.8, 0.8)])
            specs.append(f"{cell}@r{repetition}={directory}")
    output = tmp_path / "report.json"
    assert ablation_report.main([*specs, "--output", str(output)]) == 0
    report = json.loads(output.read_text(encoding="utf-8"))
    assert set(report["cells"]) == set(ABLATION_MATRIX)
    assert "promotion_gates" in report

    with pytest.raises(SystemExit, match="missing fixed ablation cells"):
        ablation_report.main([f"S={tmp_path / 'S-r0'}", "--output", str(tmp_path / "missing.json")])

    retrieval_only = tmp_path / "retrieval-only"
    retrieval_only.mkdir()
    (retrieval_only / "retrieval.jsonl").write_text(json.dumps(record("q1", 1, 1.0, 1.0)) + "\n", encoding="utf-8")
    with pytest.raises(SystemExit, match="generation.jsonl"):
        ablation_report.main([f"S={retrieval_only}", "--output", str(tmp_path / "nope.json")])


def test_matrix_is_fixed_and_covers_plan_cells():
    assert tuple(ABLATION_MATRIX) == ("S", "SK", "SKF", "SKD", "SKP", "SKPM")
    assert "memory_relation" in GRAPH_FAMILY_ABLATIONS


def test_ablation_request_maps_cells_to_recall_controls():
    assert ablation_request("S") == {"capabilities": ["semantic"], "graph_policy": "off", "diversification": False}
    assert ablation_request("SK")["graph_policy"] == "off"
    assert ablation_request("SKF")["graph_policy"] == "features_only"
    assert ablation_request("SKD")["graph_policy"] == "direct"
    assert ablation_request("SKP")["graph_policy"] == "bounded_path"
    assert ablation_request("SKPM")["diversification"] is True
    with pytest.raises(ReportingError):
        ablation_request("nope")


def test_mean_and_stddev_are_sample_statistics():
    assert mean([1.0, 2.0, 3.0]) == 2.0
    assert stddev([5.0]) == 0.0
    assert stddev([2.0, 4.0]) == pytest.approx(1.4142135623730951)


def test_paired_bootstrap_ci_is_deterministic_and_requires_pairing():
    a = [1.0, 2.0, 3.0, 4.0]
    b = [0.5, 1.5, 2.5, 3.5]
    first = paired_bootstrap_ci(a, b, seed="fixed")
    second = paired_bootstrap_ci(a, b, seed="fixed")
    assert first == second
    assert first[0] <= first[1]
    with pytest.raises(ReportingError):
        paired_bootstrap_ci([1.0], [1.0, 2.0])


def test_run_metrics_reads_turn_metrics_and_costs():
    metrics = run_metrics([record("q1", 1, 0.5, 0.25), record("q2", 2, 1.0, 0.75)])
    assert metrics["recall"] == pytest.approx(0.75)
    assert metrics["ranking"] == pytest.approx(0.5)
    assert metrics["duplicate_rate"] == pytest.approx(0.2)
    assert metrics["latency"] == pytest.approx(10.0)


def test_aggregate_repetitions_reports_mean_std_and_paired_ci():
    runs = [
        [record("q1", 1, 0.5, 0.4)],
        [record("q1", 1, 0.6, 0.5)],
        [record("q1", 1, 0.7, 0.6)],
    ]
    aggregate = aggregate_repetitions(runs)
    assert aggregate["repetitions"] == 3
    assert aggregate["metrics"]["recall"]["mean"] == pytest.approx(0.6)
    assert aggregate["metrics"]["recall"]["std"] == pytest.approx(0.1)
    assert "paired_ci" not in aggregate["metrics"]["recall"]


def test_repetitions_are_end_to_end_and_at_least_three():
    with pytest.raises(ReportingError, match="at least 3"):
        aggregate_repetitions([[record("q1", 1, 1.0, 1.0)]])


def test_graph_promotion_precision_and_harm():
    records = [
        record(
            "q1",
            1,
            0.0,
            0.0,
            gold_memory_ids=("m1",),
            ranked=(
                ("m1", ["d1"], ["graph"], ["direct_entity_mention"]),
                ("m2", ["d1"], ["keyword"], ["keyword_match"]),
            ),
        ),
        record(
            "q2",
            1,
            0.0,
            0.0,
            gold_memory_ids=("gold",),
            ranked=(
                ("graph-harm", [], ["graph"], ["one_hop_fact"]),
                ("gold", ["d1"], ["keyword"], ["keyword_match"]),
            ),
        ),
    ]
    report = graph_promotion_report(records)
    assert report["promoted"] == 2
    assert report["precision"] == pytest.approx(0.5)
    assert report["harm_rate"] == pytest.approx(0.5)


def test_category_deltas_pair_by_question_id():
    baseline = [record("q1", 1, 0.4, 0.4), record("q2", 2, 0.6, 0.6)]
    candidate = [record("q1", 1, 0.5, 0.5), record("q2", 2, 0.8, 0.8)]
    deltas = category_deltas(candidate, baseline)
    assert deltas["1"] == pytest.approx(0.1)
    assert deltas["2"] == pytest.approx(0.2)


def test_evidence_buckets_group_by_gold_count():
    records = [
        record("q1", 1, 1.0, 1.0, gold=("d1",)),
        record("q2", 1, 0.5, 0.5, gold=("d1", "d2")),
        record("q3", 1, 0.0, 0.0, gold=("d1", "d2", "d3")),
    ]
    buckets = evidence_buckets(records)
    assert buckets["1"]["count"] == 1
    assert buckets["2"]["recall"] == pytest.approx(0.5)
    assert buckets["3+"]["count"] == 1


def test_stage_reports_and_identical_coverage():
    records = [record("q1", 1, 0.9, 0.8), record("q2", 2, 0.5, 0.4)]
    for item, extraction in zip(records, ({"gold_total": 2, "gold_indexed": 2}, {"gold_total": 2, "gold_indexed": 1})):
        item["extraction"] = extraction
    stages = stage_reports(records)
    assert stages["extraction"]["indexed_coverage"] == pytest.approx(0.75)
    assert stages["recall"]["mean_recall"] == pytest.approx(0.7)
    assert "graph_promotion" in stages["end_to_end"]

    assert_identical_query_coverage({"S": [records], "SK": [records]})
    with pytest.raises(ReportingError):
        assert_identical_query_coverage({"S": [records], "SK": [[record("q3", 1, 1.0, 1.0)]]})


def test_build_report_and_promotion_gates():
    recall_gold = run_metrics([record("q1", 1, 0.9, 0.9), record("q2", 1, 0.8, 0.8)])
    graph_records = [
        record(
            "q1",
            1,
            0.9,
            0.9,
            ranked=(
                ("m1", ["d1"], ["keyword"], ["keyword_match"]),
                ("m2", ["d1"], ["graph"], ["direct_entity_mention"]),
            ),
        ),
        record(
            "q2",
            1,
            0.8,
            0.8,
            ranked=(("m1", ["d1"], ["keyword"], ["keyword_match"]),),
        ),
    ]
    baseline = [record("q1", 1, recall_gold["recall"], recall_gold["ranking"]), record("q2", 1, 0.8, 0.8)]
    hybrid = [record("q1", 1, 0.9, 0.9), record("q2", 1, 0.8, 0.8)]
    graph = [
            record("q1", 1, 0.9, 0.9, gold_memory_ids=("m1", "m2"), ranked=(
                ("m1", ["d1"], ["keyword"], ["keyword_match"]),
                ("m2", ["d1"], ["graph"], ["direct_entity_mention"]),
            )),
            record("q2", 1, 0.8, 0.8),
        ]
    runs = {
        "S": [baseline, baseline, baseline],
        "SK": [hybrid, hybrid, hybrid],
        "SKPM": [graph, graph, graph],
    }
    report = build_end_to_end_report(runs=runs)
    assert set(report["cells"]) == {"S", "SK", "SKPM"}
    assert report["cells"]["SKPM"]["graph_promotion"]["precision"] >= 0.5
    assert set(report["cells"]["SKPM"]["graph_families"]) == set(GRAPH_FAMILY_ABLATIONS)
    assert report["cells"]["SKPM"]["paired_vs_baseline"]["recall_at_1"]["ci"]
    gates = promotion_gates(report)
    assert gates["graph_recall_within_limit"] is True
    assert gates["graph_promotion_precision"] >= 0.5


def test_report_uses_per_query_pairing_and_reports_recall_at_1_mrr_and_costs():
    baseline = [record("q1", 1, 0.0, 0.0), record("q2", 2, 1.0, 1.0)]
    candidate = [record("q2", 2, 0.0, 0.0), record("q1", 1, 1.0, 1.0)]
    for records in (baseline, candidate):
        for rec in records:
            rec["turn_metrics"]["@1"] = {
                "coverage": rec["turn_metrics"]["@10"]["coverage"],
                "mrr_at_k": rec["turn_metrics"]["@10"]["ndcg"],
            }
            rec["prompt_tokens"] = 10
            rec["completion_tokens"] = 4
            rec["provider_cost_usd"] = 0.01
    report = build_end_to_end_report(
        runs={"S": [baseline] * 3, "SK": [candidate] * 3}
    )
    metrics = report["cells"]["SK"]["aggregate"]["metrics"]
    assert {"recall_at_1", "mrr", "prompt_tokens", "completion_tokens", "provider_cost_usd"} <= set(metrics)
    assert report["cells"]["SK"]["paired_vs_baseline"]["recall_at_1"]["mean_delta"] == 0.0


def gate_report(
    *,
    baseline_duplicate=0.4,
    candidate_duplicate=0.0,
    recall_at_1_delta=0.0,
    mrr_delta=0.0,
    recall_delta=0.0,
    precision=0.6,
    category_delta=None,
    safety_measured=True,
    baseline_safety=0.0,
    candidate_safety=0.0,
):
    category_delta = {"1": 0.1} if category_delta is None else category_delta

    def entry(pairing, duplicate, safety, promotion_precision, delta):
        return {
            "paired_vs_baseline": {
                "recall_at_1": {"mean_delta": pairing[0]},
                "mrr": {"mean_delta": pairing[1]},
                "recall": {"mean_delta": pairing[2]},
            },
            "graph_promotion": {"precision": promotion_precision},
            "aggregate": {
                "metrics": {
                    "duplicate_rate": {"mean": duplicate},
                    "safety_violations": {"mean": safety},
                }
            },
            "category_delta": delta,
        }

    pairing = (recall_at_1_delta, mrr_delta, recall_delta)
    return {
        "baseline_cell": "S",
        "safety_measured": safety_measured,
        "cells": {
            "S": entry((0.0, 0.0, 0.0), baseline_duplicate, baseline_safety, 0.0, {}),
            "SKD": entry(pairing, candidate_duplicate, candidate_safety, precision, category_delta),
            "SKP": entry(pairing, candidate_duplicate, candidate_safety, precision, category_delta),
            "SKPM": entry(pairing, candidate_duplicate, candidate_safety, precision, category_delta),
        },
    }


def test_promotion_gates_verdict_has_no_failures_when_all_pass():
    gates = promotion_gates(gate_report())
    assert gates["passes"] is True
    assert gates["failures"] == []


@pytest.mark.parametrize(
    "overrides, expected",
    [
        ({"recall_at_1_delta": -0.02}, "graph_recall_regression"),
        ({"mrr_delta": -0.02}, "graph_mrr_regression"),
        ({"precision": 0.2}, "graph_promotion_precision"),
        ({"category_delta": {}}, "intended_gain_without_larger_harm"),
        ({"category_delta": {"1": 0.01}, "recall_delta": -0.5}, "intended_gain_without_larger_harm"),
        ({"candidate_duplicate": 0.4}, "duplicate_rate_reduction"),
        ({"safety_measured": False}, "safety_not_measured"),
        ({"candidate_safety": 1.0}, "safety_regression"),
    ],
)
def test_promotion_gates_reports_each_failure(overrides, expected):
    gates = promotion_gates(gate_report(**overrides))
    assert gates["passes"] is False
    assert expected in gates["failures"]


def _passing_gate_records(duplicate, recall, graph):
    ranked = (
        (("m1", ["d1"], ["graph"], ["direct_entity_mention"]),)
        if graph
        else (("m1", ["d1"], ["keyword"], ["keyword_match"]),)
    )
    item = record("q1", 1, recall, recall, ranked=ranked)
    item["duplicate_rate"] = duplicate
    item["safety_violations"] = 0
    return [item]


def test_ablation_report_check_mode_enforces_promotion_gates(tmp_path):
    specs = []
    for cell in ABLATION_MATRIX:
        for repetition in range(3):
            directory = tmp_path / f"{cell}-r{repetition}"
            graph = cell in ("SKD", "SKP", "SKPM")
            write_run(
                directory,
                _passing_gate_records(0.0 if graph else 0.4, 0.9 if graph else 0.8, graph),
            )
            specs.append(f"{cell}@r{repetition}={directory}")
    output = tmp_path / "report.json"
    assert ablation_report.main([*specs, "--output", str(output), "--check"]) == 0
    report = json.loads(output.read_text(encoding="utf-8"))
    assert report["promotion_gates"]["passes"] is True


def test_ablation_report_check_mode_fails_closed_on_unmet_gates(tmp_path):
    specs = []
    for cell in ABLATION_MATRIX:
        for repetition in range(3):
            directory = tmp_path / f"{cell}-fail-r{repetition}"
            write_run(directory, [record("q1", 1, 0.9, 0.9)])
            specs.append(f"{cell}@r{repetition}={directory}")
    output = tmp_path / "report.json"
    assert ablation_report.main([*specs, "--output", str(output)]) == 0
    report = json.loads(output.read_text(encoding="utf-8"))
    assert report["promotion_gates"]["passes"] is False
    assert "safety_not_measured" in report["promotion_gates"]["failures"]
    with pytest.raises(SystemExit, match="promotion gates failed"):
        ablation_report.main([*specs, "--output", str(output), "--check"])


def test_report_rejects_mixed_corpus_and_generation_judge_identities():
    records = [record("q1", 1, 1.0, 1.0)]
    identities = {
        "S": [
            {"corpus_id": "fresh-1", "dataset_sha": "sha", "generation": "g", "judge": "j"},
            {"corpus_id": "fresh-1", "dataset_sha": "sha", "generation": "g", "judge": "j"},
            {"corpus_id": "fresh-1", "dataset_sha": "sha", "generation": "other", "judge": "j"},
        ]
    }
    with pytest.raises(ReportingError, match="identit"):
        build_end_to_end_report(runs={"S": [records] * 3}, identities=identities)
