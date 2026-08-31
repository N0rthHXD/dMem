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
namespace bootstrap + selected ingestion profile
      │  POST /v1/remember, or /v1/events + jobs + candidate confirm
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
└── project: longmemeval:<dataset-sha>:<profile>:<run-id>
    ├── task: q:<question-id-1>
    ├── task: q:<question-id-2>
    └── task: q:<question-id-N>
```

Retrieval is scoped to the question's task space with
`include_ancestors=false`, ensuring strict inter-question isolation. Two
ingestion profiles use the same paired user/assistant input units:

- `direct-turn` writes authoritative event memories through `POST /v1/remember`.
  It measures storage, retrieval, and QA while bypassing extraction and review.
- `processed-events` submits raw conversations through `POST /v1/events`, waits
  for every extraction job, discovers candidates belonging to the exact raw
  event, and applies an explicit candidate confirmation policy. It measures the
  complete extraction, review orchestration, retrieval, and QA chain.

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
export OPENAI_API_KEY=sk-...
```

The config file defines:

| Section | Key | Description |
|---|---|---|
| `[daemon]` | `socket_path` | dMem Unix socket (or use `--socket`) |
| `[ingestion]` | `profile` | `direct-turn` or `processed-events` |
| `[ingestion]` | `confirmation_policy` | `confirm-all-new` or `none` |
| `[ingestion]` | `job_poll_interval`, `job_timeout` | Extraction job polling |
| `[retrieval]` | `mode` | `keyword`, `semantic`, or `hybrid` |
| `[retrieval]` | `limit`, `max_chars`, `cutoffs` | Retrieval parameters |
| `[answer]` | `model` | OpenAI-compatible model for answer generation |
| `[answer]` | `base_url` | API endpoint (empty = OpenAI default) |
| `[answer]` | `api_key_env` | Environment variable name for API key |
| `[answer]` | `answer_cutoff`, `gen_length`, `cot` | Generation parameters |
| `[judge]` | `model`, `base_url`, `api_key_env` | Judge model configuration |

## Usage

### Stage runner

`evaluate.sh` is the standalone evaluation entry point. Use `--stage` to run
retrieval, QA, or both stages:

```sh
# Extraction/ingestion plus retrieval only.
./evaluate.sh --stage retrieval \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --run-id processed-001 \
  --profile processed-events \
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
  --profile processed-events \
  --eval-config eval_config.toml
```

Arguments after `--` are passed to the retrieval `run` command, for example
`-- --confirmation-policy none --max-cases 10`.

### Retrieval baseline

```sh
python -m dmem_longmemeval --config eval_config.toml run \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --run-id baseline-001 \
  --output results/longmemeval/baseline-001
```

For semantic or hybrid retrieval, set `mode = "hybrid"` in the config or pass
`--retrieval-mode hybrid` on the command line. Semantic and hybrid modes
require a configured embedding provider in the daemon. The runner triggers a
single semantic rebuild after all ingestion is complete, then waits for
readiness before querying.

### Processed-events retrieval

The daemon must have an extraction processor and a processing policy that
allows the benchmark's task spaces, `conversation` source type, and
`text/plain` content. Run the extraction plus retrieval stage with:

```sh
python -m dmem_longmemeval --config eval_config.toml run \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --run-id processed-001 \
  --profile processed-events \
  --confirmation-policy confirm-all-new \
  --output results/longmemeval/processed-001
```

The run fails if an event has no extraction job, a job does not succeed, the
processor reports `no_processor`, or the event does not reach `processed`.
`no_memory` is a valid extraction result and is reported in the ingestion
summary.

`evaluate.sh` creates and manages a fresh daemon for the retrieval stage. It
defaults to `${DMEM_CONFIG:-~/.config/dmem/config.toml}` as the base daemon
configuration. If that file has `[processing.provider].enabled = false`, pass
an enabled provider configuration explicitly:

```sh
./evaluate.sh --stage all ... \
  --profile processed-events \
  --daemon-config /path/to/processing-enabled.toml
```

### Resume

Add `--resume` to continue a previously interrupted run. The runner validates
that the dataset SHA-256, run ID, ingestion profile, confirmation policy,
retrieval mode, limit, max-chars, cutoffs, and answer cutoff all match the
existing checkpoint.

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
checkpoint produced by either ingestion profile, so the commands are unchanged
for `processed-events`. `evaluate.sh --stage all --profile processed-events`
runs both stages and manages a fresh daemon for retrieval.

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
├── ingestion.jsonl        # event records plus per-case completion markers
├── retrieval.jsonl        # one record per recalled question
├── generation.jsonl       # one record per generated answer
├── judge.jsonl            # one record per judged answer
├── hypotheses.jsonl       # official format for upstream evaluation
└── summary.json           # aggregated metrics
```

All JSONL files are append-only with `fsync` after each record, enabling safe
resume after crashes.
