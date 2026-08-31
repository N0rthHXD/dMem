# dMem LongMemEval Evaluation

A standalone benchmark runner that evaluates [dMem](../../README.md) against the
[LongMemEval](https://github.com/xiaowu0162/LongMemEval) benchmark. It communicates
with a running `dmemd` instance exclusively through dMem's public HTTP API over its
Unix socket — it never imports or starts the daemon, and does not touch SQLite
directly.

## Architecture

```
LongMemEval dataset
      │
      ▼
namespace bootstrap + direct-turn ingest
      │  POST /v1/spaces, POST /v1/remember
      ▼
dMem daemon (public API over Unix socket)
      │  POST /v1/recall
      ▼
retrieval metrics + evidence→session attribution
      │
      ▼
optional generation + official-compatible QA judge
```

The runner creates a `user → project → task` namespace hierarchy:

```
unique user root
└── project: longmemeval:<dataset-sha>:<run-id>
    ├── task: q:<question-id-1>
    ├── task: q:<question-id-2>
    └── task: q:<question-id-N>
```

Each question's haystack sessions are ingested as paired user/assistant event
memories via `POST /v1/remember`. Retrieval is scoped to the question's task
space with `include_ancestors=false`, ensuring strict inter-question isolation.

This mode measures **storage and retrieval** quality only. Direct ingestion
via `/v1/remember` bypasses extraction and candidate review.

## Setup

```sh
pip install -r evaluation/longmemeval/requirements.txt
```

Requires Python 3.10+.

## Usage

### Retrieval baseline

```sh
python -m dmem_longmemeval run \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --socket /run/dmem/dmem.sock \
  --run-id baseline-001 \
  --retrieval-mode keyword \
  --output results/longmemeval/baseline-001
```

For semantic or hybrid retrieval:

```sh
python -m dmem_longmemeval run \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --socket /run/dmem/dmem.sock \
  --run-id baseline-002 \
  --retrieval-mode hybrid \
  --output results/longmemeval/baseline-002
```

Semantic and hybrid modes require a configured embedding provider in the daemon.
The runner triggers a single semantic rebuild after all ingestion is complete,
then waits for readiness before querying.

### Resume

Add `--resume` to continue a previously interrupted run. The runner validates that
the dataset SHA-256, run ID, retrieval mode, limit, max-chars, cutoffs, and model
configuration all match the existing checkpoint.

### Answer generation

```sh
export OPENAI_API_KEY=...
python -m dmem_longmemeval generate \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --output results/longmemeval/baseline-001 \
  --model gpt-4o-mini-2024-07-18 \
  --answer-cutoff 30
```

Writes `hypotheses.jsonl` in the official format (`question_id` + `hypothesis`),
which can also be evaluated independently with the upstream script:

```sh
python LongMemEval/src/evaluation/evaluate_qa.py \
  gpt-4o results/longmemeval/baseline-001/hypotheses.jsonl \
  /path/to/longmemeval_s_cleaned.json
```

### QA judging

```sh
export OPENAI_API_KEY=...
python -m dmem_longmemeval judge \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --output results/longmemeval/baseline-001 \
  --model gpt-4o-2024-08-06
```

Uses the exact type-specific and abstention judge prompts from the upstream
LongMemEval `evaluate_qa.py`. Reports overall, task-averaged, per-type, and
abstention accuracy.

### Summarize

```sh
python -m dmem_longmemeval summarize \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --output results/longmemeval/baseline-001
```

Recomputes and prints aggregated metrics from stored checkpoint files.

### Cleanup

```sh
python -m dmem_longmemeval cleanup \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --socket /run/dmem/dmem.sock \
  --output results/longmemeval/baseline-001
```

Logically forgets all ingested memories for this run via `POST /v1/forget`.
Spaces, raw events, revisions, evidence, and audit history are retained —
this is a logical reset, not a physical one. For a fully clean run, use a
fresh daemon instance with a new data directory.

## Metrics

Retrieval metrics follow the upstream `eval_utils.py` formulas exactly:

- **recall_any@k**: at least one relevant session in the top-k retrieved sessions
- **recall_all@k**: all relevant sessions in the top-k
- **ndcg@k**: binary-relevance nDCG

Macro-averages exclude:
- Abstention questions (`_abs` suffix)
- Cases without answer-session labels
- Cases with execution errors

Default cutoffs: `1, 3, 5, 10, 30, 50`.

Evidence coverage and unmapped-result counts are also reported per case.

## Output Files

```
output_dir/
├── manifest.json          # run configuration and identity
├── ingestion.jsonl        # one record per ingested turn-pair
├── retrieval.jsonl        # one record per recalled question
├── generation.jsonl       # one record per generated answer
├── judge.jsonl            # one record per judged answer
├── hypotheses.jsonl       # official format for upstream evaluation
└── summary.json           # aggregated metrics
```

All JSONL files are append-only with `fsync` after each record, enabling safe
resume after crashes.
