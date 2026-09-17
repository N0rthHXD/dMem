#!/usr/bin/env bash
# Run the LoCoMo retrieval stage, QA stage, or both.
# The shared driver in evaluation/common/evaluate.sh implements the stages.
set -euo pipefail

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export DMEM_EVAL_MODULE=dmem_locomo
export DMEM_EVAL_LABEL=locomo
export DMEM_EVAL_DIR="$BENCH_DIR"
export DMEM_EVAL_STAGES="corpus embeddings retrieval qa all"

exec "$BENCH_DIR/../common/evaluate.sh" "$@"
