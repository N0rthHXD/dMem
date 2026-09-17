#!/usr/bin/env bash
# Shared dMem benchmark evaluation driver.
#
# Benchmark wrappers set the environment below, then exec this script:
#   DMEM_EVAL_MODULE   python module to run (e.g. dmem_locomo)
#   DMEM_EVAL_LABEL    results/<label>/ directory segment (e.g. locomo)
#   DMEM_EVAL_DIR      benchmark directory to run the module from
#   DMEM_EVAL_STAGES   space-separated stages this benchmark supports
#
# Stages: corpus, embeddings, indexes, retrieval, qa, all
#
# Common options:
#   --stage STAGE          stage to run (default: all)
#   --dataset PATH         benchmark dataset (required)
#   --run-id ID            run identifier (required for corpus/retrieval/all)
#   --output DIR           result directory (default: <repo>/results/<label>/<run-id>)
#   --corpus-output DIR    existing shared corpus for embeddings/indexes/retrieval
#   --retrieval-mode MODE  retrieval mode (default: config/default)
#   --eval-config PATH     evaluation TOML (required for QA)
#   --daemon-config PATH   dmemd TOML (default: $DMEM_CONFIG or ~/.config/dmem/config.toml)
#   --dmemd PATH           dmemd binary (default: $DMEMD or <repo>/dmemd)
#   --python PATH          Python interpreter (default: $PYTHON or python3)
#   --resume               resume retrieval with the saved daemon workspace
#   --                     extra arguments passed to the retrieval command

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

MODULE="${DMEM_EVAL_MODULE:?DMEM_EVAL_MODULE is required}"
LABEL="${DMEM_EVAL_LABEL:?DMEM_EVAL_LABEL is required}"
BENCH_DIR="${DMEM_EVAL_DIR:?DMEM_EVAL_DIR is required}"
SUPPORTED_STAGES="${DMEM_EVAL_STAGES:?DMEM_EVAL_STAGES is required}"
export PYTHONPATH="$SCRIPT_DIR${PYTHONPATH:+:$PYTHONPATH}"

STAGE="all"
DATASET=""
RUN_ID=""
OUTPUT=""
RETRIEVAL_MODE=""
CORPUS_OUTPUT=""
EVAL_CONFIG="${DMEM_EVAL_CONFIG:-}"
DAEMON_CONFIG="${DMEM_CONFIG:-${XDG_CONFIG_HOME:-$HOME/.config}/dmem/config.toml}"
DMEMD_BIN="${DMEMD:-}"
PYTHON_BIN="${PYTHON:-python3}"
RESUME=false
RETRIEVAL_ARGS=()

usage() {
  sed -n '2,26p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --stage) STAGE="$2"; shift 2;;
    --dataset) DATASET="$2"; shift 2;;
    --run-id) RUN_ID="$2"; shift 2;;
    --output) OUTPUT="$2"; shift 2;;
    --corpus-output) CORPUS_OUTPUT="$2"; shift 2;;
    --retrieval-mode) RETRIEVAL_MODE="$2"; shift 2;;
    --eval-config) EVAL_CONFIG="$2"; shift 2;;
    --daemon-config) DAEMON_CONFIG="$2"; shift 2;;
    --dmemd) DMEMD_BIN="$2"; shift 2;;
    --python) PYTHON_BIN="$2"; shift 2;;
    --resume) RESUME=true; shift;;
    --)
      shift
      while [[ $# -gt 0 ]]; do
        RETRIEVAL_ARGS+=("$1")
        shift
      done
      ;;
    -h|--help) usage 0;;
    *) printf 'unknown flag: %s\n' "$1" >&2; usage 1;;
  esac
done

stage_supported() {
  local wanted="$1" candidate
  for candidate in $SUPPORTED_STAGES; do
    [[ "$candidate" == "$wanted" ]] && return 0
  done
  return 1
}

if ! stage_supported "$STAGE"; then
  printf 'error: --stage %s not supported by %s (supported: %s)\n' \
    "$STAGE" "$LABEL" "$SUPPORTED_STAGES" >&2
  exit 1
fi

[[ -n "$DATASET" ]] || { printf 'error: --dataset is required\n' >&2; exit 1; }
[[ -f "$DATASET" ]] || { printf 'error: dataset not found: %s\n' "$DATASET" >&2; exit 1; }

NEEDS_DAEMON=false
case "$STAGE" in
  corpus|embeddings|indexes|retrieval|all) NEEDS_DAEMON=true;;
esac

if [[ "$NEEDS_DAEMON" == true ]]; then
  [[ -n "$RUN_ID" || "$STAGE" == "embeddings" || "$STAGE" == "indexes" ]] || {
    printf 'error: --run-id is required for %s\n' "$STAGE" >&2
    exit 1
  }
  [[ "$STAGE" != "corpus" || -n "$RUN_ID" ]] || {
    printf 'error: --run-id is required for corpus\n' >&2
    exit 1
  }
  if [[ "$STAGE" == "embeddings" || "$STAGE" == "indexes" ]]; then
    [[ -n "$CORPUS_OUTPUT" ]] || {
      printf 'error: --corpus-output is required for %s\n' "$STAGE" >&2
      exit 1
    }
  fi
fi

if [[ -z "$OUTPUT" && ( "$STAGE" == "embeddings" || "$STAGE" == "indexes" ) ]]; then
  OUTPUT="$CORPUS_OUTPUT"
fi
if [[ -z "$OUTPUT" ]]; then
  [[ -n "$RUN_ID" ]] || {
    printf 'error: --output is required for a standalone QA stage\n' >&2
    exit 1
  }
  OUTPUT="$REPO_ROOT/results/$LABEL/$RUN_ID"
fi

if [[ "$STAGE" == "corpus" && -e "$OUTPUT" && "$RESUME" == false ]]; then
  printf 'error: corpus output already exists: %s\n' "$OUTPUT" >&2
  exit 1
fi

if [[ "$STAGE" == "qa" || "$STAGE" == "all" ]]; then
  [[ -n "$EVAL_CONFIG" ]] || {
    printf 'error: --eval-config or DMEM_EVAL_CONFIG is required for QA\n' >&2
    exit 1
  }
  [[ -f "$EVAL_CONFIG" ]] || {
    printf 'error: eval config not found: %s\n' "$EVAL_CONFIG" >&2
    exit 1
  }
fi

run_daemon_stage() {
  [[ -f "$DAEMON_CONFIG" ]] || {
    printf 'error: daemon config not found: %s\n' "$DAEMON_CONFIG" >&2
    exit 1
  }
  command -v "$PYTHON_BIN" >/dev/null 2>&1 || {
    printf 'error: python not found: %s\n' "$PYTHON_BIN" >&2
    exit 1
  }

  if [[ -z "$DMEMD_BIN" ]]; then
    DMEMD_BIN="$("$PYTHON_BIN" -m dmem_eval.daemon_workspace build --repo-root "$REPO_ROOT")"
  fi
  [[ -x "$DMEMD_BIN" ]] || {
    printf 'error: dmemd not executable: %s\n' "$DMEMD_BIN" >&2
    exit 1
  }
  export DMEM_DAEMON_BUILD_SHA256
  DMEM_DAEMON_BUILD_SHA256="$(sha256sum "$DMEMD_BIN" | cut -d' ' -f1)"

  install -d "$OUTPUT"
  if [[ -n "$CORPUS_OUTPUT" ]]; then
    TMP_ROOT="$CORPUS_OUTPUT/daemon-workspace"
  else
    TMP_ROOT="$OUTPUT/daemon-workspace"
  fi
  DATA_DIR="$TMP_ROOT/data"
  DAEMON_CONFIG_PATH="$TMP_ROOT/dmemd.toml"

  if [[ -n "$CORPUS_OUTPUT" ]]; then
    [[ -f "$DATA_DIR/dmem.db" ]] || {
      printf 'error: corpus daemon workspace not found: %s\n' "$TMP_ROOT" >&2
      exit 1
    }
    printf 'reusing corpus daemon workspace: %s\n' "$TMP_ROOT" >&2
    INIT_DB=false
  elif [[ "$RESUME" == true ]]; then
    [[ -f "$DAEMON_CONFIG_PATH" && -f "$DATA_DIR/dmem.db" ]] || {
      printf 'error: resumable daemon workspace not found: %s\n' "$TMP_ROOT" >&2
      exit 1
    }
    printf 'reusing daemon workspace: %s\n' "$TMP_ROOT" >&2
    INIT_DB=false
  else
    [[ ! -e "$TMP_ROOT" ]] || {
      printf 'error: daemon workspace already exists; use --resume: %s\n' "$TMP_ROOT" >&2
      exit 1
    }
    INIT_DB=true
  fi

  "$PYTHON_BIN" -m dmem_eval.daemon_workspace write-config \
    --workspace "$TMP_ROOT" --source "$DAEMON_CONFIG" >/dev/null

  local start_args=(start --workspace "$TMP_ROOT" --dmemd "$DMEMD_BIN")
  [[ "$INIT_DB" == true ]] && start_args+=(--init)
  printf 'starting daemon\n' >&2
  SOCKET_PATH="$TMP_ROOT/runtime/dmem.sock"
  "$PYTHON_BIN" -m dmem_eval.daemon_workspace "${start_args[@]}" >/dev/null

  cleanup() {
    local status=$?
    trap - EXIT INT TERM
    "$PYTHON_BIN" -m dmem_eval.daemon_workspace stop --workspace "$TMP_ROOT" >/dev/null 2>&1 || true
    exit "$status"
  }
  trap cleanup EXIT INT TERM

  local cmd=("$PYTHON_BIN" -m "$MODULE")
  [[ -n "$EVAL_CONFIG" ]] && cmd+=(--config "$EVAL_CONFIG")

  if [[ "$STAGE" == "corpus" || "$STAGE" == "embeddings" || "$STAGE" == "indexes" ]]; then
    printf 'running %s stage\n' "$STAGE" >&2
    cmd+=("$STAGE" --dataset "$DATASET" --socket "$SOCKET_PATH")
    if [[ "$STAGE" == "corpus" ]]; then
      cmd+=(--run-id "$RUN_ID" --output "$OUTPUT")
      [[ "$RESUME" == true ]] && cmd+=(--resume)
    else
      cmd+=(--corpus-output "$CORPUS_OUTPUT")
    fi
  else
    cmd+=(run --dataset "$DATASET" --socket "$SOCKET_PATH" --run-id "$RUN_ID" --output "$OUTPUT")
    [[ "$RESUME" == true ]] && cmd+=(--resume)
    [[ -n "$RETRIEVAL_MODE" ]] && cmd+=(--retrieval-mode "$RETRIEVAL_MODE")
    [[ -n "$CORPUS_OUTPUT" ]] && cmd+=(--corpus-output "$CORPUS_OUTPUT")
    [[ ${#RETRIEVAL_ARGS[@]} -gt 0 ]] && cmd+=("${RETRIEVAL_ARGS[@]}")
    if [[ "$STAGE" == "all" ]]; then
      printf '[1/3] running retrieval stage\n' >&2
    else
      printf '[1/1] running retrieval stage\n' >&2
    fi
  fi

  (
    cd "$BENCH_DIR"
    "${cmd[@]}"
  )
}

if [[ "$NEEDS_DAEMON" == true ]]; then
  run_daemon_stage
fi

if [[ "$STAGE" == "qa" || "$STAGE" == "all" ]]; then
  [[ -f "$OUTPUT/retrieval.jsonl" ]] || {
    printf 'error: retrieval checkpoint not found: %s/retrieval.jsonl\n' "$OUTPUT" >&2
    exit 1
  }
  command -v "$PYTHON_BIN" >/dev/null 2>&1 || {
    printf 'error: python not found: %s\n' "$PYTHON_BIN" >&2
    exit 1
  }

  if [[ "$STAGE" == "all" ]]; then
    printf '[2/3] running QA generation stage\n' >&2
  else
    printf '[1/2] running QA generation stage\n' >&2
  fi
  (
    cd "$BENCH_DIR"
    "$PYTHON_BIN" -m "$MODULE" --config "$EVAL_CONFIG" generate \
      --dataset "$DATASET" --output "$OUTPUT"
  )

  if [[ "$STAGE" == "all" ]]; then
    printf '[3/3] running QA judge stage\n' >&2
  else
    printf '[2/2] running QA judge stage\n' >&2
  fi
  (
    cd "$BENCH_DIR"
    "$PYTHON_BIN" -m "$MODULE" --config "$EVAL_CONFIG" judge \
      --dataset "$DATASET" --output "$OUTPUT"
  )

  printf 'summarizing retrieval and QA results\n' >&2
  (
    cd "$BENCH_DIR"
    "$PYTHON_BIN" -m "$MODULE" summarize --dataset "$DATASET" --output "$OUTPUT"
  )
fi

printf 'results: %s\n' "$OUTPUT" >&2
