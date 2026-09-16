#!/usr/bin/env bash
# Run LongMemEval's fixed retrieval matrix as repeated end-to-end evaluations.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DATASET=""
EVAL_CONFIG=""
DAEMON_CONFIG=""
OUTPUT_ROOT=""
REPETITIONS=3
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
    *) printf 'unknown argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done

[[ -n "$DATASET" && -n "$EVAL_CONFIG" && -n "$DAEMON_CONFIG" && -n "$OUTPUT_ROOT" ]] || {
  printf 'required: --dataset --eval-config --daemon-config --output-root\n' >&2
  exit 2
}
(( REPETITIONS >= 3 )) || { printf '%s\n' '--repetitions must be at least 3' >&2; exit 2; }

mkdir -p "$OUTPUT_ROOT"
CORPUS_OUTPUT="$OUTPUT_ROOT/corpus"
COMMON=(--dataset "$DATASET" --eval-config "$EVAL_CONFIG" --daemon-config "$DAEMON_CONFIG" --python "$PYTHON_BIN")
[[ -z "$DMEMD_BIN" ]] || COMMON+=(--dmemd "$DMEMD_BIN")

"$SCRIPT_DIR/evaluate.sh" --stage corpus --run-id ablation-corpus --output "$CORPUS_OUTPUT" "${COMMON[@]}"
"$SCRIPT_DIR/evaluate.sh" --stage indexes --corpus-output "$CORPUS_OUTPUT" "${COMMON[@]}"

CELLS=(
  "S|semantic|off|1"
  "SK|semantic,keyword|off|1"
  "SKF|semantic,keyword,graph|features_only|1"
  "SKD|semantic,keyword,graph|direct|1"
  "SKP|semantic,keyword,graph|bounded_path|1"
  "SKPM|semantic,keyword,graph|bounded_path|0"
)
RUN_SPECS=()
for ((repeat=1; repeat<=REPETITIONS; repeat++)); do
  for spec in "${CELLS[@]}"; do
    IFS='|' read -r cell capabilities graph_policy fixed_cutoff <<<"$spec"
    run_dir="$OUTPUT_ROOT/${cell}-r${repeat}"
    extra=(-- --capabilities "$capabilities" --graph-policy "$graph_policy")
    [[ "$fixed_cutoff" == 0 ]] || extra+=(--disable-diversification)
    "$SCRIPT_DIR/evaluate.sh" --stage all --run-id "${cell}-r${repeat}" \
      --output "$run_dir" --corpus-output "$CORPUS_OUTPUT" --retrieval-mode hybrid \
      "${COMMON[@]}" "${extra[@]}"
    RUN_SPECS+=("${cell}@r${repeat}=${run_dir}")
  done
done

"$PYTHON_BIN" "$SCRIPT_DIR/ablation_report.py" --check \
  --output "$OUTPUT_ROOT/ablation-report.json" "${RUN_SPECS[@]}"
