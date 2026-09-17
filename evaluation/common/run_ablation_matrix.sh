#!/usr/bin/env bash
# Shared reproducible end-to-end ablation matrix driver.
#
# Benchmark wrappers set DMEM_EVAL_MODULE, DMEM_EVAL_LABEL, DMEM_EVAL_DIR, and
# DMEM_EVAL_INDEX_STAGE, then exec this script.
#
# Usage:
#   ./run_ablation_matrix.sh \
#     --dataset PATH --eval-config PATH --daemon-config PATH \
#     --output-root DIR [--repetitions N] [--resume] [--dmemd PATH] [--python PATH]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MODULE="${DMEM_EVAL_MODULE:?DMEM_EVAL_MODULE is required}"
LABEL="${DMEM_EVAL_LABEL:?DMEM_EVAL_LABEL is required}"
BENCH_DIR="${DMEM_EVAL_DIR:?DMEM_EVAL_DIR is required}"
INDEX_STAGE="${DMEM_EVAL_INDEX_STAGE:?DMEM_EVAL_INDEX_STAGE is required}"
export PYTHONPATH="$SCRIPT_DIR${PYTHONPATH:+:$PYTHONPATH}"

DATASET=""
EVAL_CONFIG=""
DAEMON_CONFIG=""
OUTPUT_ROOT=""
REPETITIONS=3
RESUME=0
PYTHON_BIN="${DMEM_PYTHON:-python3}"
DMEMD_BIN=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dataset) DATASET="$2"; shift 2 ;;
    --eval-config) EVAL_CONFIG="$2"; shift 2 ;;
    --daemon-config) DAEMON_CONFIG="$2"; shift 2 ;;
    --output-root) OUTPUT_ROOT="$2"; shift 2 ;;
    --repetitions) REPETITIONS="$2"; shift 2 ;;
    --dmemd) DMEMD_BIN="$2"; shift 2 ;;
    --python) PYTHON_BIN="$2"; shift 2 ;;
    --resume) RESUME=1; shift ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done

for required in DATASET EVAL_CONFIG DAEMON_CONFIG OUTPUT_ROOT; do
  if [[ -z "${!required}" ]]; then
    printf 'missing required --%s\n' "$(echo "$required" | tr '[:upper:]' '[:lower:]' | tr '_' '-')" >&2
    exit 2
  fi
done

(( REPETITIONS >= 3 )) || { printf '%s\n' '--repetitions must be at least 3' >&2; exit 2; }

mkdir -p "$OUTPUT_ROOT"
CORPUS_OUTPUT="$OUTPUT_ROOT/corpus"
COMMON=(--dataset "$DATASET" --eval-config "$EVAL_CONFIG" --daemon-config "$DAEMON_CONFIG" --python "$PYTHON_BIN")
[[ -z "$DMEMD_BIN" ]] || COMMON+=(--dmemd "$DMEMD_BIN")

echo "[1/4] fresh shared corpus"
if [[ ! -f "$CORPUS_OUTPUT/daemon-workspace/data/dmem.db" ]]; then
  "$BENCH_DIR/evaluate.sh" --stage corpus --run-id ablation-corpus --output "$CORPUS_OUTPUT" "${COMMON[@]}"
fi

echo "[2/4] index build ($INDEX_STAGE)"
"$BENCH_DIR/evaluate.sh" --stage "$INDEX_STAGE" --corpus-output "$CORPUS_OUTPUT" "${COMMON[@]}"

# cell|capabilities|graph_policy|disable_diversification
CELLS=(
  "S|semantic|off|1"
  "SK|semantic,keyword|off|1"
  "SKF|semantic,keyword,graph|features_only|1"
  "SKD|semantic,keyword,graph|direct|1"
  "SKP|semantic,keyword,graph|bounded_path|1"
  "SKPM|semantic,keyword,graph|bounded_path|0"
)

echo "[3/4] repeated end-to-end runs"
RUN_SPECS=()
for ((repeat=1; repeat<=REPETITIONS; repeat++)); do
  for cell_spec in "${CELLS[@]}"; do
    IFS='|' read -r cell capabilities graph_policy fixed_cutoff <<<"$cell_spec"
    run_dir="$OUTPUT_ROOT/${cell}-r${repeat}"
    if [[ "$RESUME" != "1" || ! -s "$run_dir/retrieval.jsonl" ]]; then
      extra=(-- --capabilities "$capabilities" --graph-policy "$graph_policy")
      [[ "$fixed_cutoff" == "1" ]] && extra+=(--disable-diversification)
      "$BENCH_DIR/evaluate.sh" --stage all \
        --run-id "${cell}-r${repeat}" --output "$run_dir" \
        --corpus-output "$CORPUS_OUTPUT" --retrieval-mode hybrid \
        "${COMMON[@]}" "${extra[@]}"
    fi
    RUN_SPECS+=("${cell}@r${repeat}=${run_dir}")
  done
done

echo "[4/4] ablation report"
"$PYTHON_BIN" "$BENCH_DIR/ablation_report.py" --check \
  --output "$OUTPUT_ROOT/ablation-report.json" "${RUN_SPECS[@]}"
echo "report written to $OUTPUT_ROOT/ablation-report.json"
