import copy
import json
import os

from dmem_calibration import (
    CALIBRATION_REPORT_VERSION,
    assign_split,
    build_report,
    default_parameter_space,
    fit_weights,
    paired_bootstrap_interval,
    split_examples,
)
from dmem_calibration.fit import QueryExample
from dmem_calibration.report import promotion_decision
from dmem_calibration.splits import (
    SPLIT_DEVELOPMENT,
    SPLIT_HOLDOUT,
    SPLIT_TRAIN,
    assert_disjoint,
)


def make_examples(count=60, candidates=4):
    examples = []
    for query in range(count):
        for rank in range(candidates):
            examples.append(
                QueryExample(
                    query_id=f"q{query}",
                    benchmark="locomo" if query % 2 == 0 else "longmemeval",
                    query_type=["lookup", "temporal", "multi_hop"][query % 3],
                    features={
                        "semantic": 1.0 - 0.2 * rank,
                        "keyword": 0.9 - 0.25 * rank,
                        "exact": 1.0 if rank == 0 else 0.0,
                        "temporal": 0.8 if query % 3 == 1 and rank == 0 else 0.1,
                        "quality": 0.7,
                        "agreement": 0.5,
                        "graph": 0.4 if rank == 1 else 0.0,
                        "feedback": 0.0,
                        "duplicate_risk": 0.1 * rank,
                        "overbreadth": 0.0,
                        "stale_penalty": 0.0,
                    },
                    label=3.0 - rank,
                )
            )
    return examples


def test_parameter_space_covers_required_categories():
    space = default_parameter_space()
    names = " ".join(space)
    for required in [
        "semantic.absolute_floor",
        "semantic.top_margin",
        "ranking.keyword_weight",
        "ranking.temporal_weight",
        "graph.gate_floor",
        "graph.boost_cap",
        "penalty.duplicate",
        "penalty.overbreadth",
        "penalty.stale",
        "context.relevance",
        "context.redundancy",
        "context.minimum_utility",
    ]:
        assert required in names, required
    for spec in space.values():
        values = spec.values()
        assert values[0] == spec.minimum
        assert values[-1] == spec.maximum
        assert values == sorted(values)


def test_splits_are_deterministic_and_disjoint():
    examples = make_examples()
    first = split_examples(examples)
    second = split_examples(examples)
    assert [e.query_id for e in first[SPLIT_TRAIN]] == [e.query_id for e in second[SPLIT_TRAIN]]
    assert_disjoint(first)
    assigned = {e.query_id: assign_split(e.query_id) for e in examples}
    assert set(assigned.values()) <= {SPLIT_TRAIN, SPLIT_DEVELOPMENT, SPLIT_HOLDOUT}
    assert len(set(assigned.values())) == 3


def test_holdout_labels_never_influence_fitting():
    examples = make_examples()
    splits = split_examples(examples)
    assert splits[SPLIT_HOLDOUT], "expected a non-empty holdout split"
    weights, _ = fit_weights(default_parameter_space(), splits)

    corrupted = copy.deepcopy(splits)
    for example in corrupted[SPLIT_HOLDOUT]:
        example.label = 0.0
        example.features = {key: 0.0 for key in example.features}
    corrupted_weights, _ = fit_weights(default_parameter_space(), corrupted)
    assert weights == corrupted_weights


def test_fitting_is_deterministic():
    examples = make_examples()
    splits = split_examples(examples)
    first, first_summary = fit_weights(default_parameter_space(), splits)
    second, second_summary = fit_weights(default_parameter_space(), splits)
    assert first == second
    assert first_summary == second_summary


def test_paired_bootstrap_interval_is_reproducible():
    baseline = [0.1, 0.2, 0.3, 0.4, 0.35]
    candidate = [0.2, 0.3, 0.35, 0.5, 0.45]
    first = paired_bootstrap_interval(baseline, candidate)
    second = paired_bootstrap_interval(baseline, candidate)
    assert first == second
    assert first[0] <= first[1]
    assert first[0] > 0


def test_report_is_versioned_and_gated_on_holdout_interval():
    examples = make_examples()
    splits = split_examples(examples)
    weights, summary = fit_weights(default_parameter_space(), splits)
    report = build_report(default_parameter_space(), splits, weights, summary)
    assert report["report_version"] == CALIBRATION_REPORT_VERSION
    assert report["boundaries"]["runtime_feedback"] == "not_written"
    assert report["boundaries"]["fit_uses"] == ["train", "development"]
    assert set(report["splits"]) == {"train", "development", "holdout"}
    decision = promotion_decision(
        report,
        {"minimum_ci_lower": 10.0, "maximum_benchmark_regression": 0.0, "maximum_query_type_regression": 0.0},
    )
    assert decision["promoted"] is False
    assert "paired_ci_does_not_exclude_zero" in decision["reasons"]


def test_recorded_report_reproduces_exactly(tmp_path):
    from dmem_calibration.cli import main

    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    output = tmp_path / "report.json"
    assert main(["--examples", os.path.join(root, "testdata", "dev-examples-v1.json"), "--output", str(output)]) == 0
    with open(output, "r", encoding="utf-8") as handle:
        produced = json.load(handle)
    with open(os.path.join(root, "records", "calibration-report-v1.json"), "r", encoding="utf-8") as handle:
        recorded = json.load(handle)
    assert produced == recorded
