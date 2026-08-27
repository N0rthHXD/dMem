# LongMemEval Evaluation

`dmem-eval longmemeval` evaluates dMem against LongMemEval through dMem's public
HTTP API over its Unix socket. The initial implementation supports a
direct-storage retrieval baseline plus optional answer generation and official-
compatible LLM judging. It does not claim to measure automatic extraction.

## Reference Implementations

The evaluator follows three different LongMemEval usages:

- Upstream LongMemEval separates retrieval, answer generation, and LLM judging.
  Retrieval reports `recall_any`, `recall_all`, and binary nDCG. QA reports
  overall, six question-type, task-macro, and abstention accuracy.
- mem0 isolates each question, stores chronological two-turn chunks, checkpoints
  ingestion, retrieves a large ranked set, and judges generated answers at
  multiple cutoffs. Its additional benchmark-specific answer and judge rules
  mean its QA score is not directly comparable with upstream.
- MemOS splits ingestion, search, response, judging, and aggregation into
  scripts. Its stored search context loses raw result identities and scores,
  making retrieval attribution weaker.

dMem keeps complete recall responses and maps memory evidence event IDs back to
LongMemEval sessions. It does not infer relevance through text or IDs containing
the word `answer`.

## Supported Baseline

The current `direct-turn` mode:

1. Strictly validates the JSON array, six question types, timestamps, roles,
   unique IDs, parallel haystack arrays, and answer-session references.
2. Creates a separate dMem user space for every question.
3. Sorts sessions by timestamp and stores consecutive user/assistant pairs with
   `POST /v1/remember`.
4. Never sends `has_answer`, `answer_session_ids`, or the answer to dMem.
5. Calls `POST /v1/recall` with isolated scope, `include_evidence=true`, and the
   question date as `reference_time`.
6. Computes session `recall_any`, `recall_all`, binary nDCG, evidence coverage,
   and unmapped-result counts.
7. Atomically checkpoints the complete run after each question. Resume validates
   the dataset hash and retrieval configuration.

This mode evaluates storage and retrieval only. Storing each pair as an
authoritative event memory bypasses extraction and candidate review.

Add `--answer-model` to generate answers with an OpenAI-compatible API. Add
`--judge-model` to apply the upstream LongMemEval type-specific and abstention
judge prompts. The rich result reports overall, task-averaged, per-type, and
abstention accuracy. `--hypotheses` writes the upstream-compatible JSONL fields
`question_id` and `hypothesis` for independent evaluation with the official
scripts.

## Run

Download `longmemeval_s_cleaned.json` or another official variant, start dMem
with its config file, and run:

```sh
CGO_ENABLED=1 go run -tags sqlite_fts5 ./cmd/dmem-eval longmemeval \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --config ~/.config/dmem/config.toml \
  --run-id baseline-001 \
  --retrieval-mode hybrid \
  --output results/longmemeval/baseline-001.json
```

The evaluator resolves the dMem Unix socket from the config file's
`runtime_dir` + `socket_filename`, mirroring `dmemd --config <path> daemon`.
`--socket` overrides the config and `DMEM_SOCKET` overrides both; when neither
is set the evaluator falls back to the default config path and then to the
computed default socket.

For QA evaluation:

```sh
export LONGMEMEVAL_ANSWER_API_KEY=...
export LONGMEMEVAL_JUDGE_API_KEY=...

CGO_ENABLED=1 go run -tags sqlite_fts5 ./cmd/dmem-eval longmemeval \
  --dataset /path/to/longmemeval_s_cleaned.json \
  --config ~/.config/dmem/config.toml \
  --run-id qa-001 \
  --answer-model gpt-4o-mini-2024-07-18 \
  --judge-model gpt-4o-2024-08-06 \
  --answer-cutoff 30 \
  --hypotheses results/longmemeval/qa-001.hypotheses.jsonl \
  --output results/longmemeval/qa-001.json
```

The default cutoffs are `1,3,5,10,30,50,100`. dMem currently limits one recall
request to 100 results, so this evaluator does not advertise mem0's top-200
cutoff. Semantic and hybrid runs require a configured embedding provider. The
result records requested/executed mode and degradation details.

Use `--resume` with the same run ID, dataset bytes, cutoffs, mode, limit, and
character budget. Deterministic idempotency keys prevent duplicate acknowledged
memories.

## Metrics

Retrieval macro means exclude `_abs` question IDs and cases without answer
session labels. `recall_all@k` requires every distinct relevant session among the
first `k` distinct mapped sessions. Duplicate memories from one session do not
count multiple times. Unmapped memories remain non-relevant ranked results.

`evidence_coverage` is the fraction of answer-labelled pair events present in
recalled evidence. Pair granularity reflects direct ingestion of a user turn and
its following assistant turn together.

## Planned Profiles

- `processed-events`: ingest through `/v1/events`, wait for jobs, apply an
  explicit candidate confirmation policy, then measure extraction plus retrieval.
- Stage-specific concurrency and bounded retry controls for large live runs.

These stay separate so extraction, retrieval, answer-model, and judge-model
failures are not collapsed into one opaque score.
