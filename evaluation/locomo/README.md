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
| `[retrieval]` | `mode` | `keyword`, `semantic`, `graph`, or `hybrid` |
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

### Shared Corpus

Ingestion is a shared prerequisite for retrieval comparisons. Create it once,
build embeddings from the ingested memories, and then run each retrieval mode
against the same daemon database:

```sh
# 1. Ingest and confirm the common corpus.
./evaluate.sh --stage corpus \
  --dataset /path/to/locomo10.json \
  --run-id locomo-corpus-001 \
  --output /path/to/results/locomo-corpus-001 \
  --eval-config eval_config.toml \
  --daemon-config dmemd-embedding.toml

# 2. Enable the embedding provider in dmemd-embedding.toml and build vectors.
./evaluate.sh --stage embeddings \
  --dataset /path/to/locomo10.json \
  --corpus-output /path/to/results/locomo-corpus-001 \
  --eval-config eval_config.toml \
  --daemon-config dmemd-embedding.toml

# 3. Evaluate any number of retrieval modes without re-ingestion.
./evaluate.sh --stage retrieval \
  --dataset /path/to/locomo10.json \
  --run-id full-hybrid-001 \
  --output /path/to/results/full-hybrid-001 \
  --corpus-output /path/to/results/locomo-corpus-001 \
  --retrieval-mode hybrid \
  --eval-config eval_config.toml \
  --daemon-config dmemd-embedding.toml
```

The corpus directory owns the daemon workspace and `ingestion.jsonl`; each
retrieval directory owns its own manifest, `retrieval.jsonl`, and summary. The
retrieval stage validates the dataset SHA, complete ingestion, and embedding
readiness before querying. Do not run cleanup against the shared corpus while
retrieval runs still depend on it.

`graph` requests only the graph channel. `hybrid` requests keyword, semantic,
and graph together. Only `semantic` and `hybrid` require the embedding build
stage.

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

## Baselines

Phase 0 records fresh current-algorithm baselines on one immutable shared
corpus before any ranking change. A baseline is immutable: a changed dataset
SHA, corpus, metric schema, or daemon algorithm identity requires a new file.
The baseline schema is `evaluation-baseline-v1` under
`evaluation/locomo/baselines/` and `internal/storage/testdata/`; the retrieval
metric schema is `locomo-retrieval-metrics-v2`.

Capture one shared corpus once, build embeddings, then run each retrieval mode
against the identical database:

```sh
# 1. Fresh shared corpus (ingestion + candidate confirmation).
./evaluate.sh --stage corpus \
  --dataset /path/to/locomo10.json \
  --run-id locomo-baseline-corpus-001 \
  --output results/locomo/baseline-corpus-001 \
  --daemon-config dmemd-embedding.toml

# 2. Build embeddings for the shared corpus.
./evaluate.sh --stage embeddings \
  --dataset /path/to/locomo10.json \
  --corpus-output results/locomo/baseline-corpus-001 \
  --daemon-config dmemd-embedding.toml

# 3. Keyword, semantic, graph, and hybrid on the same corpus.
for mode in keyword semantic graph hybrid; do
  ./evaluate.sh --stage retrieval \
    --dataset /path/to/locomo10.json \
    --run-id baseline-$mode-001 \
    --output results/locomo/baseline-$mode-001 \
    --corpus-output results/locomo/baseline-corpus-001 \
    --retrieval-mode "$mode" \
    --daemon-config dmemd-embedding.toml
done
```

Each run records its `identities` (daemon build/commit, schema compatibility,
processing, semantic/graph versions, FTS and ranking versions, metric schema)
and `degradation_counts` in `manifest.json`, plus per-result report fields
(`primary_score`, `raw_channel_scores`, `matched_fields`, `graph_score`,
`signal_family`, `duplicate_cluster`, `selection_state`) in `retrieval.jsonl`.
Use `summarize` (which recomputes metrics from raw records and refuses a metric
schema mismatch) to produce the immutable baseline records.

The accepted current-algorithm capture is
`baselines/locomo-current-v1.json`. It was recorded on 2026-09-14 from all 10
conversations and 1,986 questions with no retrieval failures or degraded
responses. All modes use dataset SHA
`79fa87e90f04081343b8c8debecb80a9a6842b76a7aa537dc9fdf651ea698ff4`
and shared corpus `ingestion.jsonl` SHA
`a4225c775f1e6f2c91e332a17b4db1731b07065e3d052e587fcf81863d792165`.
The daemon build identity is
`9723af424262df7731b41298b6e7c6022e3769575894813b2ffe61cb940f3bec`,
the ranking identity is `rrf-v3`, the FTS version is `1`, and the graph
version is `4046`. Exact per-run semantic versions and checkpoint SHA-256
values are retained in the capture file.

The four run IDs are `phase0-current-keyword-v1`,
`phase0-current-semantic-v1`, `phase0-current-graph-v1`, and
`phase0-current-hybrid-v1`. Each command used `--fail-on-degraded`; the exact
retrieval command was the following, with `$mode` replaced by each mode:

```sh
./evaluation/locomo/evaluate.sh --stage retrieval \
  --dataset /home/ut005426@uos/data/locomo/locomo10.json \
  --run-id "phase0-current-$mode-v1" \
  --output "/home/ut005426@uos/data/locomo/results/phase0-current-$mode-v1" \
  --corpus-output /home/ut005426@uos/data/locomo/results/full-keyword-001 \
  --retrieval-mode "$mode" \
  --eval-config /home/ut005426@uos/data/locomo/eval_config.locomo-full.toml \
  --daemon-config /home/ut005426@uos/data/locomo/dmemd.locomo-full.toml \
  --dmemd /tmp/opencode/dmemd-phase0 \
  --python /home/ut005426@uos/miniforge3/envs/agent/bin/python \
  -- --fail-on-degraded
```

The capture uses unique-evidence coverage@10 as `recall` and unique-evidence
nDCG@10 as `ranking`. Per-request daemon allocation and provider pricing are
not exposed, so the recorded `allocation` is the mean serialized response-byte
proxy and `provider_cost` is the query-embedding request-count proxy. These
definitions are embedded in the capture and must not be compared with a future
capture that uses different measurement units.

## Metrics

### Retrieval

Metrics are reported at three separate granularities over the annotated
`evidence` references:

- **Memory level** — the ranked list of recalled memories. A memory is
  relevant when it maps to at least one gold turn; the gold set is the set of
  memories confirmed from gold turns.
- **Turn level (unique evidence)** — recalled evidence promoted to unique
  `dia_id` turns in stable chronological order.
- **Session level** — unique evidence turns promoted to sessions.

Each layer reports `recall_any`, `recall_all`, `coverage`, binary-relevance
`nDCG`, and `MRR@k`. Definitions:

- `recall_all@k` is `gold <= set(top_k)`: the complete gold set must appear in
  the top-k ranking. It is not a precision check that every retrieved item is
  relevant, and adding irrelevant results does not change it.
- `nDCG@k` uses an ideal DCG built from `min(k, len(gold))` relevant items, so
  omitting gold evidence always lowers the score. Duplicate ids add no gain.
- `MRR@k` is the reciprocal rank of the first gold item inside the top-k
  window. It is explicitly cutoff-specific; a full-list `mrr` is also defined
  but is not reported per cutoff.

The summary also reports macro-average `duplicate_rate` (mapped source turns
seen more than once) and `unmapped_rate` (recalled results that could not be
attributed to a LoCoMo turn). `unmapped_count` records the absolute unmapped
count per question.

The extraction layer distinguishes an indexed miss from a retrieval failure:
`extraction.coverage` is the fraction of gold turns that appear in at least one
confirmed memory, `missing_gold_cases` counts questions whose gold source never
became an indexed memory, and `retrieval_failure_cases` counts questions where
the gold source was indexed but never retrieved.

Macro-averages exclude adversarial (category 5) questions and any question with
empty, malformed, or partially unresolved evidence, whose gold relevance is not
defined for recall. `excluded_unscorable` counts those cases. Default cutoffs:
`1, 3, 5, 10, 25, 50`. The retrieval metric contract is versioned as
`locomo-retrieval-metrics-v2` and recorded in every manifest and retrieval
record.

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
