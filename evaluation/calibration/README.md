# Deterministic Calibration

Evaluation-only tooling for fitting dMem retrieval weights on fixed development
sets. It never writes runtime feedback and never reads benchmark test-set
(holdout) labels while fitting.

## Boundaries

| Split | Percent | Used for |
| --- | --- | --- |
| `train` | 60% | Coordinate search |
| `development` | 20% | Coordinate search and model selection |
| `holdout` | 20% | Paired confidence interval and promotion gate only |

`holdout` is the benchmark test set. Corrupting holdout labels cannot change
selected weights because `fit_weights` only observes `train` and `development`.

## Parameter space

`default_parameter_space()` covers semantic thresholds (`absolute_floor`,
`top_margin`), keyword and feature weights (`keyword`, `exact`, `temporal`,
`quality`), graph gate and boost (`gate_floor`, `boost_cap`), penalties
(`duplicate`, `overbreadth`, `stale`), context utility (`relevance`,
`redundancy`, `minimum_utility`), and bounded feedback weights (`global`,
`query`).

## Usage

```sh
PYTHONPATH=evaluation/calibration ~/miniforge3/envs/agent/bin/python -m dmem_calibration \
  --examples evaluation/calibration/testdata/dev-examples-v1.json \
  --output evaluation/calibration/records/calibration-report-v1.json
```

The report is versioned (`calibration-report-v1`) and records the parameter
space, selected weights, per-split metrics, per-benchmark and per-query-type
deltas, and a paired bootstrap interval. Promotion requires the holdout paired
interval to exclude zero and no benchmark or query-type regression beyond the
configured tolerance.

## Tests

```sh
~/miniforge3/envs/agent/bin/python -m pytest evaluation/calibration/tests
```

## Optional learned reranker

The bounded reranker interface and its safety validation live in
`internal/retrieval/reranker.go`. Learned reranking remains evaluation-only:
`SelectReranker` returns the deterministic fallback unless a caller explicitly
opts into `RerankerModeEvaluation`, and promotion requires gains that exceed
latency, variance, and degradation thresholds on identical candidate windows.
