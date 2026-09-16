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
namespace bootstrap + event ingestion + candidate confirmation
      │  POST /v1/events + jobs + candidate confirm
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

Retrieval is scoped to the question's task space with
`include_ancestors=false`, ensuring strict inter-question isolation. The runner
submits paired user/assistant input units as raw conversations through
`POST /v1/events`, waits for every extraction job, discovers candidates belonging
to the exact raw event, and applies an explicit candidate confirmation policy.
It measures the complete extraction, review orchestration, retrieval, and QA chain.

`confirm-all-new` is an evaluator policy, not daemon auto-confirmation. The
daemon still requires an explicit `POST /v1/candidates/{id}/confirm` for every
candidate. `none` leaves all candidates pending and is useful for extraction
inspection, but normally produces no recallable memories on a fresh instance.

## Setup

```sh
pip install -r evaluation/longmemeval/requirements.txt
```

Requires Python 3.11+ (uses `tomllib`).

## Configuration

All settings are loaded from a TOML config file (`--config`). CLI flags
override config values, which override built-in defaults. API keys are never
stored in the config file — they are read from environment variables named
by `api_key_env`.

```sh
cp evaluation/longmemeval/eval_config.example.toml eval_config.toml
# Edit eval_config.toml for your environment
export DMEM_PROVIDER_KEY=...
```

The config file defines:

| Section | Key | Description |
|---|---|---|
| `[daemon]` | `socket_path` | dMem Unix socket (or use `--socket`) |
| `[ingestion]` | `confirmation_policy` | `confirm-all-new` or `none` |
| `[ingestion]` | `job_poll_interval`, `job_timeout` | Extraction job polling |
| `[ingestion]` | `invalid_evidence_reference_retries` | Bounded retry count for this model-output violation; default `1`, `0` disables it |
| `[ingestion]` | `workers` | Cases ingested concurrently; built-in default `1` |
| `[retrieval]` | `mode` | `keyword`, `semantic`, or `hybrid` |
| `[retrieval]` | `limit`, `max_chars`, `cutoffs` | Retrieval parameters |
| `[retrieval]` | `workers` | Recall requests issued concurrently; built-in default `1` |
| `[answer]` | `model` | OpenAI-compatible model for answer generation |
| `[answer]` | `base_url` | API endpoint (empty = OpenAI default) |
| `[answer]` | `api_key_env` | Environment variable name for API key |
| `[answer]` | `answer_cutoff`, `gen_length`, `cot` | Generation parameters |
| `[answer]` | `workers` | Answer model requests issued concurrently; built-in default `1` |
| `[judge]` | `model`, `base_url`, `api_key_env` | Judge model configuration |
| `[judge]` | `workers` | Judge model requests issued concurrently; built-in default `1` |

## Usage

### Stage runner

`evaluate.sh` is the standalone evaluation entry point. Use `--stage` to run
retrieval, QA, or both stages:

```sh
# Extraction/ingestion plus retrieval only.
./evaluate.sh --stage retrieval \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --run-id processed-001 \
  --eval-config eval_config.toml

# QA only, consuming an existing retrieval checkpoint.
./evaluate.sh --stage qa \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --output results/longmemeval/processed-001 \
  --eval-config eval_config.toml

# Both stages.
./evaluate.sh --stage all \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --run-id processed-001 \
  --eval-config eval_config.toml
```

Arguments after `--` are passed to the retrieval `run` command, for example
`-- --confirmation-policy none --max-cases 10`.

The four stages support bounded case-level concurrency. Configure `workers` in
their TOML sections, or override retrieval-stage values with
`--ingestion-workers` and `--retrieval-workers`; `generate` and `judge` each
accept `--workers`. Ingestion keeps events within each case ordered, and recall
still starts only after all ingestion and the optional semantic barrier finish.
Keep ingestion workers aligned with the daemon job worker count and provider
`max_concurrent`/rate limits. Checkpoints are thread-safe within one evaluator,
but multiple evaluator processes must not share an output directory.

For semantic or hybrid retrieval, set `mode = "hybrid"` in the config or pass
`--retrieval-mode hybrid` on the command line. Semantic and hybrid modes
require a configured embedding provider in the daemon. The runner triggers a
single semantic rebuild after all ingestion is complete, then waits for
readiness before querying.

### Event Processing Retrieval

The daemon must have an extraction processor and a processing policy that
allows the benchmark's task spaces, `conversation` source type, and
`text/plain` content. For semantic or hybrid retrieval, the embedding provider
policy must also allow the `conversation` source type, or every memory's
embedding job will be blocked with `embedding_policy_denied`. Run the extraction
plus retrieval stage with:

```sh
python -m dmem_longmemeval --config eval_config.toml run \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --run-id processed-001 \
  --confirmation-policy confirm-all-new \
  --output results/longmemeval/processed-001
```

The runner retries a failed extraction job on the same job ID when its error
is a model output contract error, up to
`invalid_evidence_reference_retries` times. This
includes malformed processor JSON, invalid candidates, evidence references,
related-memory references, and the generic `invalid_output` fallback. It does
not re-ingest the event. All other failed job states fail the current case if an
event has no extraction job, a job does not succeed, the processor reports
`no_processor`, or the event does not reach `processed`. `no_memory` is a valid
extraction result and is reported in the ingestion summary.

Failures are isolated at the case boundary. If any event in a case cannot be
processed, that case is recorded in `failures.jsonl`, excluded from recall and
metric denominators, and the runner continues with later cases. Retrieval
failures are handled the same way. The final summary reports `coverage`,
`complete`, and unresolved failure counts by phase. A partial run writes its
summary and then exits non-zero; resume retries incomplete cases, and a later
successful checkpoint resolves the earlier failure for summary purposes.

`evaluate.sh` creates and manages an isolated daemon workspace for the retrieval stage. It
defaults to `${DMEM_CONFIG:-~/.config/dmem/config.toml}` as the base daemon
configuration. If that file has `[processing.provider].enabled = false`, pass
an enabled provider configuration explicitly:

```sh
./evaluate.sh --stage all ... \
  --daemon-config /path/to/processing-enabled.toml
```

### Resume

`evaluate.sh` stores the daemon data directory at
`<output>/daemon-workspace`, so use its `--resume` option to restart the daemon
against the same data and continue an interrupted retrieval stage:

```sh
./evaluate.sh --stage retrieval \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --run-id processed-001 \
  --output results/longmemeval/processed-001 \
  --resume
```

The Python runner uses its per-case checkpoints to skip completed ingestion and
recall records. It validates
that the dataset SHA-256, run ID, confirmation policy, retrieval mode, limit,
max-chars, cutoffs, and answer cutoff all match the existing checkpoint.

### Answer generation

```sh
python -m dmem_longmemeval --config eval_config.toml generate \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --output results/longmemeval/baseline-001
```

Reads `[answer]` section from the config. Writes `hypotheses.jsonl` in the
official format (`question_id` + `hypothesis`), which can also be evaluated
independently with the upstream script:

```sh
python LongMemEval/src/evaluation/evaluate_qa.py \
  gpt-4o results/longmemeval/baseline-001/hypotheses.jsonl \
  /path/to/longmemeval_s_cleaned.json
```

### QA judging

```sh
python -m dmem_longmemeval --config eval_config.toml judge \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --output results/longmemeval/baseline-001
```

Reads `[judge]` section from the config. Uses the exact type-specific and
abstention judge prompts from the upstream LongMemEval `evaluate_qa.py`.
Reports overall, task-averaged, per-type, and abstention accuracy.

Together, `generate` and `judge` are the QA stage. They consume the retrieval
checkpoint produced by the event-processing retrieval stage.
`evaluate.sh --stage all` runs both stages and manages a fresh daemon for retrieval.

### Summarize

```sh
python -m dmem_longmemeval summarize \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --output results/longmemeval/baseline-001
```

Recomputes and prints aggregated metrics from stored checkpoint files.

### Cleanup

```sh
python -m dmem_longmemeval --config eval_config.toml cleanup \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --output results/longmemeval/baseline-001
```

Logically forgets all ingested memories for this run via
`POST /v1/memories/{memory_id}/forget`.
Spaces, raw events, revisions, evidence, and audit history are retained —
this is a logical reset, not a physical one. For a fully clean run, use a
fresh daemon instance with a new data directory.

## Metrics

Retrieval metrics follow the upstream `eval_utils.py` formulas exactly:

- **recall_any@k**: at least one relevant session in the top-k retrieved sessions
- **recall_all@k**: all relevant sessions in the top-k
- **ndcg@k**: binary-relevance nDCG
- **mrr@k**: reciprocal rank of the first relevant session within top-k

Macro-averages exclude:
- Abstention questions (`_abs` suffix)
- Cases without answer-session labels
- Cases with execution errors

Default cutoffs: `1, 3, 5, 10, 30, 50`.

Evidence coverage and unmapped-result counts are also reported per case.

### Fresh Corpus And Repetitions

Every ablation cell must be evaluated on one immutable dataset SHA with a fresh
daemon workspace and fixed generation/judge parameters, and end-to-end runs must
be repeated at least three times. A changed dataset SHA, corpus, metric schema,
or algorithm identity requires a new output directory; do not compare cells with
different query coverage, and never reuse an output directory across cells or
repetitions.

### Ablation Matrix

The fixed retrieval matrix maps to the debug override contract:

| Cell | Semantic | Keyword | Graph features | Graph candidates | Diversification |
| --- | --- | --- | --- | --- | --- |
| `S` | yes | no | no | no | no |
| `SK` | yes | yes | no | no | no |
| `SKF` | yes | yes | yes | no | no |
| `SKD` | yes | yes | yes | direct/fact | no |
| `SKP` | yes | yes | yes | bounded path | no |
| `SKPM` | yes | yes | yes | bounded path | MMR |

Graph-family reports further isolate `direct_entity_mention`,
`exact_predicate_fact`, `one_hop_fact`, `two_hop_path`, and `memory_relation`
from per-result telemetry. Run the executable matrix against one newly ingested
shared corpus:

```sh
./evaluation/longmemeval/run_ablation_matrix.sh \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --eval-config /path/to/eval_config.toml \
  --daemon-config /path/to/dmemd-embedding.toml \
  --output-root /path/to/results/longmemeval-ablation \
  --repetitions 3
```

The script maps each cell to `--capabilities`, `--graph-policy`, and
`--disable-diversification`, creates the corpus and semantic index once, and
runs retrieval, answer generation, and judging for every repetition. It reports
with `ablation_report.py --check`, so the matrix fails closed with the failed
gate names when the promotion gates do not pass.

### Reproducible Report

`ablation_report.py` requires all six cells, at least three end-to-end
repetitions per cell, and each run's `manifest.json`, `retrieval.jsonl`,
`generation.jsonl`, and `judge.jsonl`. It rejects differing question coverage,
corpus/dataset identities, generation identities, or judge identities. The
report contains run-level means/sample standard deviations and
candidate-minus-`S` percentile bootstrap CIs paired by question ID after
averaging repetitions, plus query-type/evidence-count buckets, graph-family
promotion precision/harm, duplicates, recall latency, serialized response bytes,
and generation/judge token use.

```sh
~/miniforge3/envs/agent/bin/python evaluation/longmemeval/ablation_report.py \
  --output results/longmemeval/ablation-report.json \
  S@r1=results/S-r1 S@r2=results/S-r2 S@r3=results/S-r3 \
  SK@r1=results/SK-r1 SK@r2=results/SK-r2 SK@r3=results/SK-r3 \
  SKF@r1=results/SKF-r1 SKF@r2=results/SKF-r2 SKF@r3=results/SKF-r3 \
  SKD@r1=results/SKD-r1 SKD@r2=results/SKD-r2 SKD@r3=results/SKD-r3 \
  SKP@r1=results/SKP-r1 SKP@r2=results/SKP-r2 SKP@r3=results/SKP-r3 \
  SKPM@r1=results/SKPM-r1 SKPM@r2=results/SKPM-r2 SKPM@r3=results/SKPM-r3
```

Response bytes are a storage proxy, not database growth. Priced provider cost
and safety results are used only when present in source records; they are never
estimated. The safety gate fails closed when safety was not measured. The full
promotion gate also enforces no more than 0.5-point Recall@1/MRR regression,
at least 50% graph-promoted top-10 precision, positive intended query-type gain
without larger aggregate harm, and at least 50% duplicate-rate reduction.

## Output Files

```
output_dir/
├── manifest.json          # run configuration and identity
├── ingestion.jsonl        # event records plus per-case completion markers
├── retrieval.jsonl        # one record per recalled question
├── failures.jsonl         # append-only case failures by phase
├── generation.jsonl       # one record per generated answer
├── judge.jsonl            # one record per judged answer
├── hypotheses.jsonl       # official format for upstream evaluation
├── summary.json           # aggregated metrics and coverage
└── daemon-workspace/      # daemon data retained for retrieval resume
```

All JSONL files are append-only with `fsync` after each record, enabling safe
resume after crashes.
