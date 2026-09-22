"""Configuration loading for the LoCoMo Chinese-subset evaluation.

The YAML file is the single source of evaluation settings. Only a handful of
values are overridable on the command line (config path, phase selection and
run id); everything else is read here.
"""

from __future__ import annotations

import copy
import os
import re
from pathlib import Path
from typing import Any

import yaml

_ENV_PATTERN = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}")

DEFAULT_INTENT_BY_CATEGORY = {
    1: "multi_hop",
    2: "temporal",
    3: "auto",
    4: "lookup",
    5: "verification",
}

# Retrieval-channel ablation modes. dMem does not expose channel selection on
# the Agent Tool recall contract, so each mode is realised by a daemon
# configuration: ``graph_policy`` switches the graph channel off, and
# ``embedding`` (false) removes the embedding provider so semantic recall is
# reported as degraded. ``keyword`` is always available.
DEFAULT_ABLATION_MODES = [
    {"name": "hybrid", "graph_policy": "features_only", "embedding": True},
    {"name": "graph-off", "graph_policy": "off", "embedding": True},
    {"name": "semantic-off", "graph_policy": "features_only", "embedding": False},
    {"name": "keyword-only", "graph_policy": "off", "embedding": False},
]

GRAPH_POLICIES = ("off", "features_only", "direct", "bounded_path")
PHASES = ("build", "ablation", "retrieval", "qa")
_MODE_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]*")


def _expand_env(value: Any) -> Any:
    if isinstance(value, str):
        def replace(match: re.Match[str]) -> str:
            name, default = match.group(1), match.group(2)
            return os.environ.get(name, default if default is not None else "")

        return _ENV_PATTERN.sub(replace, value)
    if isinstance(value, list):
        return [_expand_env(item) for item in value]
    if isinstance(value, dict):
        return {key: _expand_env(item) for key, item in value.items()}
    return value


def _deep_merge(base: dict[str, Any], override: dict[str, Any]) -> dict[str, Any]:
    result = copy.deepcopy(base)
    for key, value in override.items():
        if isinstance(value, dict) and isinstance(result.get(key), dict):
            result[key] = _deep_merge(result[key], value)
        else:
            result[key] = copy.deepcopy(value)
    return result


def _default_config() -> dict[str, Any]:
    return {
        "run_id": "zh-subset",
        "output_dir": "results",
        "phases": ["build", "retrieval", "qa"],
        "dataset": {
            "path": "",
            "sample_ids": [],
            "categories": [],
        },
        "dmem": {
            "manage_daemon": True,
            "endpoint": "http://127.0.0.1:17373",
            "binary": "",
            "listen_address": "127.0.0.1:17373",
            "data_dir": "",
            "startup_timeout_seconds": 90,
            "indexing_timeout_seconds": 1800,
            "settle_seconds": 2.0,
            "worker_count": 8,
            "graph_policy": "features_only",
            "embedding": {
                "enabled": True,
                "endpoint": "",
                "model": "",
                "api_key_env": "DMEM_PROVIDER_KEY",
                "dimensions": 1024,
                "query_instructions": "",
                "max_egress_input_chars": 30000,
                "max_response_bytes": 1048576,
                "request_timeout": "60s",
                "max_concurrent": 16,
                "requests_per_minute": 300,
            },
        },
        "llm": {
            "base_url": "",
            "model": "",
            "api_key_env": "DMEM_PROVIDER_KEY",
            "temperature": 0.0,
            "max_tokens": 2048,
            "timeout_seconds": 180,
            "max_concurrent": 8,
            "max_retries": 4,
        },
        "build": {
            "scope_root_external_id": "locomo-zh-eval",
            "max_memories_per_commit": 24,
            "max_content_runes": 4096,
            "max_search_hints": 8,
            "include_image_captions": True,
        },
        "retrieval": {
            "limit": 30,
            "cutoffs": [1, 3, 5, 10, 30],
            "intent_by_category": DEFAULT_INTENT_BY_CATEGORY,
            "workers": 8,
            "ablation": {
                "modes": [dict(mode) for mode in DEFAULT_ABLATION_MODES],
            },
        },
        "qa": {
            "answer_cutoff": 10,
            "max_context_chars": 12000,
            "workers": 8,
            "judge": True,
        },
    }


class EvalConfig:
    """Resolved evaluation configuration."""

    def __init__(self, data: dict[str, Any], path: Path) -> None:
        self.data = data
        self.path = path

    def __getitem__(self, key: str) -> Any:
        return self.data[key]

    def get(self, key: str, default: Any = None) -> Any:
        return self.data.get(key, default)

    def section(self, name: str) -> dict[str, Any]:
        value = self.data.get(name, {})
        if not isinstance(value, dict):
            raise ValueError(f"config section {name!r} must be a mapping")
        return value

    def resolve_path(self, value: str, base: Path | None = None) -> Path:
        path = Path(value).expanduser()
        if path.is_absolute():
            return path
        return ((base or self.path.parent) / path).resolve()

    def run_dir(self, run_id: str) -> Path:
        return self.resolve_path(str(self.get("output_dir", "results"))) / run_id

    @property
    def phases(self) -> list[str]:
        phases = self.get("phases") or ["build", "retrieval", "qa"]
        return [str(item) for item in phases]

    @property
    def sample_ids(self) -> list[str]:
        return [str(item) for item in (self.section("dataset").get("sample_ids") or [])]

    @property
    def categories(self) -> list[int]:
        return [int(item) for item in (self.section("dataset").get("categories") or [])]

    @property
    def ablation_modes(self) -> list[dict[str, Any]]:
        modes = self.section("retrieval").get("ablation", {}).get("modes")
        if not modes:
            modes = DEFAULT_ABLATION_MODES
        return [
            {
                "name": str(mode["name"]),
                "graph_policy": _graph_policy(mode.get("graph_policy")),
                "embedding": bool(mode.get("embedding", True)),
            }
            for mode in modes
        ]


def _graph_policy(value: Any) -> str:
    """Normalize a graph policy, tolerating YAML's boolean ``off``."""

    if value is None:
        return "features_only"
    if value is False:
        return "off"
    if value is True:
        return "features_only"
    return str(value)


def load_config(path: str | Path, overrides: dict[str, Any] | None = None) -> EvalConfig:
    config_path = Path(path).expanduser().resolve()
    with config_path.open("r", encoding="utf-8") as handle:
        raw = yaml.safe_load(handle) or {}
    if not isinstance(raw, dict):
        raise ValueError(f"config file {config_path} must contain a YAML mapping")
    merged = _deep_merge(_default_config(), _expand_env(raw))
    if overrides:
        merged = _deep_merge(merged, overrides)
    config = EvalConfig(merged, config_path)
    _validate(config)
    return config


def _validate(config: EvalConfig) -> None:
    dataset_path = str(config.section("dataset").get("path") or "").strip()
    if not dataset_path:
        raise ValueError("dataset.path is required")
    if not config.resolve_path(dataset_path).is_file():
        raise ValueError(f"dataset.path does not exist: {dataset_path}")
    if not str(config.section("llm").get("model") or "").strip():
        raise ValueError("llm.model is required")
    if not str(config.section("llm").get("base_url") or "").strip():
        raise ValueError("llm.base_url is required")
    dmem = config.section("dmem")
    if dmem.get("manage_daemon"):
        if not str(dmem.get("binary") or "").strip():
            raise ValueError("dmem.binary is required when dmem.manage_daemon is true")
    embedding = dmem.get("embedding") or {}
    if embedding.get("enabled"):
        if not str(embedding.get("endpoint") or "").strip():
            raise ValueError("dmem.embedding.endpoint is required when embedding is enabled")
        if not str(embedding.get("model") or "").strip():
            raise ValueError("dmem.embedding.model is required when embedding is enabled")
    unknown = set(config.phases) - set(PHASES)
    if unknown:
        raise ValueError(f"unknown phases: {sorted(unknown)}")
    _validate_ablation(config)


def _validate_ablation(config: EvalConfig) -> None:
    modes = config.ablation_modes
    if not modes:
        raise ValueError("retrieval.ablation.modes must not be empty")
    names: set[str] = set()
    for mode in modes:
        name = mode["name"]
        if not _MODE_NAME.fullmatch(name):
            raise ValueError(
                f"retrieval.ablation mode name must be filesystem-safe: {name!r}"
            )
        if name in names:
            raise ValueError(f"duplicate retrieval.ablation mode name: {name!r}")
        names.add(name)
        if mode["graph_policy"] not in GRAPH_POLICIES:
            raise ValueError(
                f"retrieval.ablation mode {name!r} graph_policy must be one of "
                f"{list(GRAPH_POLICIES)}"
            )
        if mode["embedding"]:
            embedding = config.section("dmem").get("embedding") or {}
            if not embedding.get("enabled"):
                raise ValueError(
                    f"retrieval.ablation mode {name!r} needs dmem.embedding.enabled "
                    "so the semantic channel can execute; enable embedding or drop "
                    "that mode from retrieval.ablation.modes"
                )
