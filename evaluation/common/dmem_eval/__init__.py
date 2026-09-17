"""Shared evaluation infrastructure for dMem benchmark runners.

This package holds the logic that is identical or near-identical across the
LoCoMo and LongMemEval evaluation suites: the Unix-socket HTTP client, the
append-only checkpoint, TOML configuration loading, progress reporting,
ablation statistics/promotion gates, the parallel answer-generation frame,
and daemon workspace management. Benchmark packages keep only their dataset
readers, metric semantics, judge prompts, and runner adapters.
"""

__version__ = "1.0.0"
