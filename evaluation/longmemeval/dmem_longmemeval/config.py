"""Evaluation configuration loading from TOML.

Uses Python 3.11+ ``tomllib`` to parse a TOML config file matching dMem's
configuration convention. API keys are never stored in the file; instead,
``api_key_env`` names the environment variable to read at runtime.

CLI flags override config values, which override built-in defaults.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from .runner import DEFAULT_CUTOFFS


@dataclass
class DaemonConfig:
    socket_path: str = ""


@dataclass
class IngestionConfig:
    confirmation_policy: str = "confirm-all-new"
    job_poll_interval: float = 3.0
    job_timeout: float = 600.0
    invalid_evidence_reference_retries: int = 1
    workers: int = 1


@dataclass
class RetrievalConfig:
    mode: str = "keyword"
    limit: int = 100
    max_chars: int = 100000
    cutoffs: list[int] = field(default_factory=lambda: DEFAULT_CUTOFFS.copy())
    workers: int = 1


@dataclass
class AnswerConfig:
    model: str = ""
    base_url: str = ""
    api_key_env: str = "OPENAI_API_KEY"
    answer_cutoff: int = 30
    gen_length: int = 500
    cot: bool = False
    workers: int = 1


@dataclass
class JudgeConfig:
    model: str = ""
    base_url: str = ""
    api_key_env: str = "OPENAI_API_KEY"
    workers: int = 1


@dataclass
class EvalConfig:
    daemon: DaemonConfig = field(default_factory=DaemonConfig)
    ingestion: IngestionConfig = field(default_factory=IngestionConfig)
    retrieval: RetrievalConfig = field(default_factory=RetrievalConfig)
    answer: AnswerConfig = field(default_factory=AnswerConfig)
    judge: JudgeConfig = field(default_factory=JudgeConfig)
    config_path: str | None = None


def load_config(path: str | Path | None) -> EvalConfig:
    """Load a TOML config file. Returns defaults if path is None or empty."""
    cfg = EvalConfig()
    if path is None or path == "":
        return cfg
    p = Path(path)
    if not p.exists():
        raise FileNotFoundError(f"config file not found: {p}")
    with open(p, "rb") as f:
        data = tomllib.load(f)

    cfg.config_path = str(p)

    if "daemon" in data:
        d = data["daemon"]
        cfg.daemon.socket_path = d.get("socket_path", "")

    if "ingestion" in data:
        i = data["ingestion"]
        cfg.ingestion.confirmation_policy = i.get(
            "confirmation_policy", "confirm-all-new"
        )
        cfg.ingestion.job_poll_interval = i.get("job_poll_interval", 3.0)
        cfg.ingestion.job_timeout = i.get("job_timeout", 600.0)
        cfg.ingestion.invalid_evidence_reference_retries = i.get(
            "invalid_evidence_reference_retries", 1
        )
        cfg.ingestion.workers = i.get("workers", 1)

    if "retrieval" in data:
        r = data["retrieval"]
        cfg.retrieval.mode = r.get("mode", "keyword")
        cfg.retrieval.limit = r.get("limit", 100)
        cfg.retrieval.max_chars = r.get("max_chars", 100000)
        cfg.retrieval.cutoffs = r.get("cutoffs", DEFAULT_CUTOFFS.copy())
        cfg.retrieval.workers = r.get("workers", 1)

    if "answer" in data:
        a = data["answer"]
        cfg.answer.model = a.get("model", "")
        cfg.answer.base_url = a.get("base_url", "")
        cfg.answer.api_key_env = a.get("api_key_env", "OPENAI_API_KEY")
        cfg.answer.answer_cutoff = a.get("answer_cutoff", 30)
        cfg.answer.gen_length = a.get("gen_length", 500)
        cfg.answer.cot = a.get("cot", False)
        cfg.answer.workers = a.get("workers", 1)

    if "judge" in data:
        j = data["judge"]
        cfg.judge.model = j.get("model", "")
        cfg.judge.base_url = j.get("base_url", "")
        cfg.judge.api_key_env = j.get("api_key_env", "OPENAI_API_KEY")
        cfg.judge.workers = j.get("workers", 1)

    _validate(cfg)
    return cfg


def _validate(cfg: EvalConfig) -> None:
    worker_counts = {
        "ingestion.workers": cfg.ingestion.workers,
        "retrieval.workers": cfg.retrieval.workers,
        "answer.workers": cfg.answer.workers,
        "judge.workers": cfg.judge.workers,
    }
    for name, workers in worker_counts.items():
        if isinstance(workers, bool) or not isinstance(workers, int) or workers < 1:
            raise ValueError(f"{name} must be a positive integer")
    if cfg.ingestion.confirmation_policy not in ("confirm-all-new", "none"):
        raise ValueError(
            "invalid ingestion.confirmation_policy: "
            f"{cfg.ingestion.confirmation_policy!r}"
        )
    if cfg.ingestion.job_poll_interval <= 0:
        raise ValueError("ingestion.job_poll_interval must be positive")
    if cfg.ingestion.job_timeout <= 0:
        raise ValueError("ingestion.job_timeout must be positive")
    retries = cfg.ingestion.invalid_evidence_reference_retries
    if isinstance(retries, bool) or not isinstance(retries, int) or retries < 0:
        raise ValueError(
            "ingestion.invalid_evidence_reference_retries must be a non-negative integer"
        )
    if cfg.retrieval.mode not in ("keyword", "semantic", "hybrid"):
        raise ValueError(
            f"invalid retrieval.mode: {cfg.retrieval.mode!r}"
        )
    if not (1 <= cfg.retrieval.limit <= 100):
        raise ValueError(
            f"retrieval.limit must be in [1, 100], got {cfg.retrieval.limit}"
        )
    if not (1 <= cfg.retrieval.max_chars <= 100000):
        raise ValueError(
            f"retrieval.max_chars must be in [1, 100000], "
            f"got {cfg.retrieval.max_chars}"
        )
    for k in cfg.retrieval.cutoffs:
        if not (1 <= k <= cfg.retrieval.limit):
            raise ValueError(
                f"cutoff {k} must be in [1, limit={cfg.retrieval.limit}]"
            )
    if not (1 <= cfg.answer.answer_cutoff <= cfg.retrieval.limit):
        raise ValueError(
            f"answer.answer_cutoff must be in [1, limit={cfg.retrieval.limit}]"
        )


def resolve_api_key(cfg_section: AnswerConfig | JudgeConfig) -> str:
    """Read the API key from the environment variable named in the config."""
    key = os.environ.get(cfg_section.api_key_env, "")
    if not key:
        raise ValueError(
            f"API key not found in environment variable "
            f"{cfg_section.api_key_env!r}"
        )
    return key


def resolve_socket(cfg: EvalConfig, cli_socket: str | None) -> str:
    """Resolve socket path: CLI flag > config > error."""
    if cli_socket:
        return cli_socket
    if cfg.daemon.socket_path:
        return cfg.daemon.socket_path
    raise ValueError(
        "socket path required: provide --socket or set daemon.socket_path "
        "in the config file"
    )
