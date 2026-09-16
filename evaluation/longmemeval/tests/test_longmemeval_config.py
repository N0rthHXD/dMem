from dmem_longmemeval.config import load_config


def test_all_model_requests_default_to_dmem_provider_key(tmp_path):
    defaults = load_config(None)
    assert defaults.answer.api_key_env == "DMEM_PROVIDER_KEY"
    assert defaults.judge.api_key_env == "DMEM_PROVIDER_KEY"

    path = tmp_path / "eval.toml"
    path.write_text("[answer]\nmodel = 'answer'\n[judge]\nmodel = 'judge'\n")
    loaded = load_config(path)
    assert loaded.answer.api_key_env == "DMEM_PROVIDER_KEY"
    assert loaded.judge.api_key_env == "DMEM_PROVIDER_KEY"
