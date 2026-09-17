#!/usr/bin/env bash
# Reproducible end-to-end ablation matrix for LoCoMo.
# The shared driver in evaluation/common/run_ablation_matrix.sh implements it.
set -euo pipefail

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export DMEM_EVAL_MODULE=dmem_locomo
export DMEM_EVAL_LABEL=locomo
export DMEM_EVAL_DIR="$BENCH_DIR"
export DMEM_EVAL_INDEX_STAGE=embeddings

exec "$BENCH_DIR/../common/run_ablation_matrix.sh" "$@"
