"""Shared configuration loading and per-benchmark defaults."""

import pytest

from dmem_locomo.config import load_config as locomo_load
from dmem_longmemeval.config import load_config as longmemeval_load


def test_all_model_requests_default_to_dmem_provider_key(tmp_path):
    for load in (locomo_load, longmemeval_load):
        defaults = load(None)
        assert defaults.answer.api_key_env == "DMEM_PROVIDER_KEY"
        assert defaults.judge.api_key_env == "DMEM_PROVIDER_KEY"

        path = tmp_path / "eval.toml"
        path.write_text("[answer]\nmodel = 'answer'\n[judge]\nmodel = 'judge'\n")
        loaded = load(path)
        assert loaded.answer.api_key_env == "DMEM_PROVIDER_KEY"
        assert loaded.judge.api_key_env == "DMEM_PROVIDER_KEY"


def test_benchmarks_keep_distinct_cutoff_defaults():
    locomo = locomo_load(None)
    assert locomo.answer.answer_cutoff == 25
    assert locomo.retrieval.cutoffs == [1, 3, 5, 10, 25, 50]

    longmemeval = longmemeval_load(None)
    assert longmemeval.answer.answer_cutoff == 30
    assert longmemeval.retrieval.cutoffs == [1, 3, 5, 10, 30, 50]


def test_retrieval_mode_is_validated_per_benchmark(tmp_path):
    path = tmp_path / "eval.toml"
    path.write_text("[retrieval]\nmode = 'graph'\n")
    assert locomo_load(path).retrieval.mode == "graph"
    with pytest.raises(ValueError, match="retrieval.mode"):
        longmemeval_load(path)


def test_workers_must_be_positive(tmp_path):
    path = tmp_path / "eval.toml"
    path.write_text("[ingestion]\nworkers = 0\n")
    for load in (locomo_load, longmemeval_load):
        with pytest.raises(ValueError, match="ingestion.workers"):
            load(path)
