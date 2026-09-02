# dMem LoCoMo Evaluation

A standalone benchmark runner that evaluates [dMem](../../README.md) against the
[LoCoMo](https://github.com/snap-research/locomo) very-long-term conversational
memory benchmark. It communicates with a running `dmemd` instance exclusively
through dMem's public HTTP API over its Unix socket — it never imports or starts
the daemon, and does not touch SQLite directly.

## Architecture

```
locomo10.json
      │
      ▼
namespace bootstrap + turn ingestion + candidate confirmation
      │  POST /v1/events + jobs + candidate confirm
      ▼
dMem daemon (public API over Unix socket)
      │  POST /v1/recall
      ▼
turn/session retrieval metrics + evidence→turn attribution
      │
      ▼
optional generation + semantic judge + official LoCoMo F1
```

The runner creates a `user → project → task` namespace hierarchy with one task
per conversation:

```
unique user root
└── project: locomo:<dataset-sha>:<run-id>
    ├── task: c:conv-26
    ├── task: c:conv-30
    └── ...
```

Each dialogue turn is submitted as one raw event (`source_type="conversation"`)
with metadata carrying `sample_id`, `session_id`, `dia_id`, `speaker`, and
`has_image`. A turn that shares an image appends its BLIP caption to the text
(`[shared image: <caption>]`). Retrieval is scoped to the conversation's task
space with `include_ancestors=false` and a `reference_time` equal to the
conversation's last session date, ensuring strict inter-conversation isolation.

`confirm-all-new` is an evaluator policy, not daemon auto-confirmation. The
daemon still requires an explicit `POST /v1/candidates/{id}/confirm` for every
candidate. `none` leaves all candidates pending and is useful for extraction
inspection, but normally produces no recallable memories on a fresh instance.

## Setup

```sh
pip install -r evaluation/locomo/requirements.txt
```

Requires Python 3.11+ (uses `tomllib`). NLTK is optional (used only for the
official Porter-stemmed F1 scorer); without it the scorer falls back to an
identity stemmer.

## Configuration

All settings are loaded from a TOML config file (`--config`). CLI flags
override config values, which override built-in defaults. API keys are never
stored in the config file — they are read from environment variables named
by `api_key_env`.

```sh
cp evaluation/locomo/eval_config.example.toml eval_config.toml
# Edit eval_config.toml for your environment
export OPENAI_API_KEY=sk-...
```

| Section | Key | Description |
|---|---|---|
| `[daemon]` | `socket_path` | dMem Unix socket (or use `--socket`) |
| `[ingestion]` | `confirmation_policy` | `confirm-all-new` or `none` |
| `[ingestion]` | `job_poll_interval`, `job_timeout` | Extraction job polling |
| `[ingestion]` | `invalid_evidence_reference_retries` | Bounded retry count for model-output contract violations; `0` disables it |
| `[ingestion]` | `workers` | Conversations ingested concurrently |
| `[retrieval]` | `mode` | `keyword`, `semantic`, or `hybrid` |
| `[retrieval]` | `limit`, `max_chars`, `cutoffs` | Retrieval parameters |
| `[retrieval]` | `workers` | Recall requests issued concurrently |
| `[answer]` | `model` | OpenAI-compatible model for answer generation |
| `[answer]` | `base_url`, `api_key_env` | API endpoint and key environment variable |
| `[answer]` | `answer_cutoff`, `gen_length`, `cot` | Generation parameters |
| `[judge]` | `model`, `base_url`, `api_key_env` | Judge model configuration |

## Usage

### Stage runner

`evaluate.sh` is the standalone evaluation entry point:

```sh
# Extraction/ingestion plus retrieval only.
./evaluate.sh --stage retrieval \
  --dataset /path/to/locomo10.json \
  --run-id hybrid-001 \
  --eval-config eval_config.toml

# QA only, consuming an existing retrieval checkpoint.
./evaluate.sh --stage qa \
  --dataset /path/to/locomo10.json \
  --output results/locomo/hybrid-001 \
  --eval-config eval_config.toml

# Both stages.
./evaluate.sh --stage all \
  --dataset /path/to/locomo10.json \
  --run-id hybrid-001 \
  --eval-config eval_config.toml
```

Arguments after `--` are passed to the retrieval `run` command, for example
`-- --confirmation-policy none --max-conversations 1`.

### Event Processing Retrieval

The daemon must have an extraction processor and a processing policy that
allows the benchmark's task spaces, `conversation` source type, and
`text/plain` content. For semantic or hybrid retrieval, the embedding provider
policy must also allow the `conversation` source type, or every memory's
embedding job will be blocked with `embedding_policy_denied`.

Failures are isolated at the conversation boundary. If any turn in a
conversation cannot be processed, that conversation is recorded in
`failures.jsonl`, excluded from recall and metric denominators, and the runner
continues with later conversations. The final summary reports `coverage`,
`complete`, and unresolved failure counts by phase. A partial run writes its
summary and then exits non-zero; resume retries incomplete conversations.

### Resume

`evaluate.sh` stores the daemon data directory at `<output>/daemon-workspace`,
so use its `--resume` option to restart the daemon against the same data and
continue an interrupted retrieval stage:

```sh
./evaluate.sh --stage retrieval \
  --dataset /path/to/locomo10.json \
  --run-id hybrid-001 \
  --output results/locomo/hybrid-001 \
  --resume
```

The Python runner validates that the dataset SHA-256, run ID, confirmation
policy, retrieval mode, limit, max-chars, cutoffs, and answer cutoff all match
the existing checkpoint.

### Answer generation and judging

```sh
python -m dmem_locomo --config eval_config.toml generate \
  --dataset /path/to/locomo10.json --output results/locomo/hybrid-001

python -m dmem_locomo --config eval_config.toml judge \
  --dataset /path/to/locomo10.json --output results/locomo/hybrid-001

python -m dmem_locomo summarize \
  --dataset /path/to/locomo10.json --output results/locomo/hybrid-001
```

### Cleanup

```sh
python -m dmem_locomo --config eval_config.toml cleanup \
  --dataset /path/to/locomo10.json --output results/locomo/hybrid-001
```

Logically forgets all ingested memories for this run via
`POST /v1/memories/{memory_id}/forget`. Spaces, raw events, revisions,
evidence, and audit history are retained — this is a logical reset, not a
physical one. For a fully clean run, use a fresh daemon instance with a new
data directory.

## Metrics

### Retrieval

Two granularities are reported over the annotated `evidence` references:

- **Turn level** — `recall_any`, `recall_all`, `coverage`, `nDCG` (binary
  relevance), and `MRR` over recalled `dia_id` evidence.
- **Session level** — the same metrics after promoting turns to sessions.

Macro-averages exclude adversarial (category 5) questions and questions with
empty or unresolved evidence, whose gold relevance is not defined for recall.
Default cutoffs: `1, 3, 5, 10, 25, 50`. `unmapped_count` records recalled
results that could not be attributed to a LoCoMo turn.

### QA

- **Official F1** — a faithful port of LoCoMo's `task_eval/evaluation.py`
  token-F1 scorer: Porter-stemmed token F1 for categories 1–4 (with comma
  multi-answer handling for category 1), and category-5 abstention checking.
- **Semantic judge** — fixed-prompt LLM correctness with type-specific prompts.

## Output Files

```
output_dir/
├── manifest.json          # run configuration and identity
├── validation.json        # dataset validation statistics
├── ingestion.jsonl        # turn records plus per-conversation completion markers
├── retrieval.jsonl        # one record per recalled question
├── failures.jsonl         # append-only conversation failures by phase
├── generation.jsonl       # one record per generated answer
├── judge.jsonl            # one record per judged answer
├── summary.json           # aggregated metrics and coverage
└── daemon-workspace/      # daemon data retained for retrieval resume
```

All JSONL files are append-only with `fsync` after each record, enabling safe
resume after crashes.
