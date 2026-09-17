#!/usr/bin/env bash
# Run the LongMemEval retrieval stage, QA stage, or both.
# The shared driver in evaluation/common/evaluate.sh implements the stages.
set -euo pipefail

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export DMEM_EVAL_MODULE=dmem_longmemeval
export DMEM_EVAL_LABEL=longmemeval
export DMEM_EVAL_DIR="$BENCH_DIR"
export DMEM_EVAL_STAGES="corpus indexes retrieval qa all"

exec "$BENCH_DIR/../common/evaluate.sh" "$@"
