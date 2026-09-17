#!/usr/bin/env bash
# Run LongMemEval's fixed retrieval matrix as repeated end-to-end evaluations.
# The shared driver in evaluation/common/run_ablation_matrix.sh implements it.
set -euo pipefail

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export DMEM_EVAL_MODULE=dmem_longmemeval
export DMEM_EVAL_LABEL=longmemeval
export DMEM_EVAL_DIR="$BENCH_DIR"
export DMEM_EVAL_INDEX_STAGE=indexes

exec "$BENCH_DIR/../common/run_ablation_matrix.sh" "$@"
