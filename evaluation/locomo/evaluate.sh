#!/usr/bin/env bash
# Run the LoCoMo retrieval stage, QA stage, or both.
#
# Usage:
#   ./evaluate.sh --stage corpus --dataset DATASET --run-id RUN_ID [options]
#   ./evaluate.sh --stage embeddings --dataset DATASET --corpus-output DIR [options]
#   ./evaluate.sh --stage retrieval --dataset DATASET --run-id RUN_ID [options]
#   ./evaluate.sh --stage retrieval --dataset DATASET --run-id RUN_ID --resume [options]
#   ./evaluate.sh --stage qa --dataset DATASET --output OUTPUT [options]
#   ./evaluate.sh --stage all --dataset DATASET --run-id RUN_ID [options]
#
# Options:
#   --stage STAGE          corpus|embeddings|retrieval|qa|all (default: all)
#   --dataset PATH        locomo10.json dataset (required)
#   --run-id ID           Run identifier (required for retrieval/all)
#   --output DIR          Result directory (default: <repo>/results/locomo/<run-id>)
#   --corpus-output DIR   Existing shared corpus directory for embeddings/retrieval
#   --retrieval-mode MODE keyword|semantic|graph|hybrid (default: config/default)
#   --eval-config PATH    Evaluation TOML (required for QA)
#   --daemon-config PATH  dmemd TOML (default: $DMEM_CONFIG or ~/.config/dmem/config.toml)
#   --dmemd PATH          dmemd binary (default: $DMEMD or <repo>/dmemd)
#   --python PATH         Python interpreter (default: python3)
#   --resume              Resume retrieval with the saved daemon workspace
#   --                    Extra arguments passed to the retrieval run command

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

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
TMP_ROOT=""
DAEMON_PID=""

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ -n "$DAEMON_PID" ]] && kill -0 "$DAEMON_PID" 2>/dev/null; then
    kill -TERM "$DAEMON_PID" 2>/dev/null || true
    for _ in $(seq 1 50); do
      kill -0 "$DAEMON_PID" 2>/dev/null || break
      sleep 0.1
    done
    kill -KILL "$DAEMON_PID" 2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
  fi
  exit "$status"
}

usage() {
  sed -n '2,22p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
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

case "$STAGE" in
  corpus|embeddings|retrieval|qa|all) ;;
  *) printf 'error: --stage must be corpus, embeddings, retrieval, qa, or all\n' >&2; exit 1;;
esac

[[ -n "$DATASET" ]] || { printf 'error: --dataset is required\n' >&2; exit 1; }
[[ -f "$DATASET" ]] || { printf 'error: dataset not found: %s\n' "$DATASET" >&2; exit 1; }

if [[ "$STAGE" == "retrieval" || "$STAGE" == "all" ]]; then
  [[ -n "$RUN_ID" ]] || { printf 'error: --run-id is required for retrieval\n' >&2; exit 1; }
fi

if [[ "$STAGE" == "corpus" ]]; then
  [[ -n "$RUN_ID" ]] || { printf 'error: --run-id is required for corpus\n' >&2; exit 1; }
fi
if [[ "$STAGE" == "embeddings" ]]; then
  [[ -n "$CORPUS_OUTPUT" ]] || { printf 'error: --corpus-output is required for embeddings\n' >&2; exit 1; }
fi

if [[ -z "$OUTPUT" && "$STAGE" == "embeddings" ]]; then
  OUTPUT="$CORPUS_OUTPUT"
fi
if [[ -z "$OUTPUT" ]]; then
  [[ -n "$RUN_ID" ]] || {
    printf 'error: --output is required for a standalone QA stage\n' >&2
    exit 1
  }
  OUTPUT="$REPO_ROOT/results/locomo/$RUN_ID"
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

if [[ "$STAGE" == "corpus" || "$STAGE" == "embeddings" || "$STAGE" == "retrieval" || "$STAGE" == "all" ]]; then
  [[ -f "$DAEMON_CONFIG" ]] || {
    printf 'error: daemon config not found: %s\n' "$DAEMON_CONFIG" >&2
    exit 1
  }
  command -v "$PYTHON_BIN" >/dev/null 2>&1 || {
    printf 'error: python not found: %s\n' "$PYTHON_BIN" >&2
    exit 1
  }

  if [[ -z "$DMEMD_BIN" ]]; then
    DMEMD_BIN="$REPO_ROOT/dmemd"
  fi
  if [[ ! -x "$DMEMD_BIN" ]]; then
    printf 'building dmemd\n' >&2
    (
      cd "$REPO_ROOT"
      CGO_ENABLED=1 go build -tags sqlite_fts5 -o dmemd ./cmd/dmemd
    )
  fi
  [[ -x "$DMEMD_BIN" ]] || {
    printf 'error: dmemd not executable: %s\n' "$DMEMD_BIN" >&2
    exit 1
  }

  install -d "$OUTPUT"
  if [[ -n "$CORPUS_OUTPUT" ]]; then
    TMP_ROOT="$CORPUS_OUTPUT/daemon-workspace"
  else
    TMP_ROOT="$OUTPUT/daemon-workspace"
  fi
  DATA_DIR="$TMP_ROOT/data"
  RUNTIME_DIR="$TMP_ROOT/runtime"
  SOCKET_PATH="$RUNTIME_DIR/dmem.sock"
  DAEMON_CONFIG_PATH="$TMP_ROOT/dmemd.toml"
  DAEMON_LOG="$TMP_ROOT/daemon.log"
  trap cleanup EXIT INT TERM

  if [[ -n "$CORPUS_OUTPUT" ]]; then
    [[ -f "$DATA_DIR/dmem.db" ]] || {
      printf 'error: corpus daemon workspace not found: %s\n' "$TMP_ROOT" >&2
      exit 1
    }
    awk -v data_dir="$DATA_DIR" -v runtime_dir="$RUNTIME_DIR" '
    /^\[instance\]$/ {
      found_instance = 1
      print
      print "data_dir = \"" data_dir "\""
      print "runtime_dir = \"" runtime_dir "\""
      print "database_filename = \"dmem.db\""
      print "socket_filename = \"dmem.sock\""
      in_instance = 1
      next
    }
    in_instance && /^\[/ { in_instance = 0 }
    !in_instance { print }
    END { if (!found_instance) exit 1 }
    ' "$DAEMON_CONFIG" > "$DAEMON_CONFIG_PATH" || {
      printf 'error: daemon config has no [instance] section: %s\n' "$DAEMON_CONFIG" >&2
      exit 1
    }
    printf 'reusing corpus daemon workspace: %s\n' "$TMP_ROOT" >&2
  elif [[ "$RESUME" == true ]]; then
    [[ -f "$DAEMON_CONFIG_PATH" && -f "$DATA_DIR/dmem.db" ]] || {
      printf 'error: resumable daemon workspace not found: %s\n' "$TMP_ROOT" >&2
      exit 1
    }
    printf 'reusing daemon workspace: %s\n' "$TMP_ROOT" >&2
  else
    [[ ! -e "$TMP_ROOT" ]] || {
      printf 'error: daemon workspace already exists; use --resume: %s\n' "$TMP_ROOT" >&2
      exit 1
    }
    install -d -m 0700 "$DATA_DIR" "$RUNTIME_DIR"
    awk -v data_dir="$DATA_DIR" -v runtime_dir="$RUNTIME_DIR" '
    /^\[instance\]$/ {
      found_instance = 1
      print
      print "data_dir = \"" data_dir "\""
      print "runtime_dir = \"" runtime_dir "\""
      print "database_filename = \"dmem.db\""
      print "socket_filename = \"dmem.sock\""
      in_instance = 1
      next
    }
    in_instance && /^\[/ { in_instance = 0 }
    !in_instance { print }
    END { if (!found_instance) exit 1 }
    ' "$DAEMON_CONFIG" > "$DAEMON_CONFIG_PATH" || {
      printf 'error: daemon config has no [instance] section: %s\n' "$DAEMON_CONFIG" >&2
      exit 1
    }
  fi

  printf 'validating daemon config\n' >&2
  "$DMEMD_BIN" --config "$DAEMON_CONFIG_PATH" config validate >/dev/null
  if [[ -n "$CORPUS_OUTPUT" ]]; then
    :
  elif [[ "$RESUME" == false ]]; then
    printf 'initializing fresh database\n' >&2
    "$DMEMD_BIN" --config "$DAEMON_CONFIG_PATH" init >/dev/null
  fi
  printf 'starting daemon\n' >&2
  "$DMEMD_BIN" --config "$DAEMON_CONFIG_PATH" daemon >"$DAEMON_LOG" 2>&1 &
  DAEMON_PID=$!

  for _ in $(seq 1 100); do
    [[ -S "$SOCKET_PATH" ]] && break
    if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
      printf 'error: daemon exited early; log follows\n' >&2
      cat "$DAEMON_LOG" >&2
      exit 1
    fi
    sleep 0.1
  done
  if [[ ! -S "$SOCKET_PATH" ]]; then
    printf 'error: daemon did not create socket; log follows\n' >&2
    cat "$DAEMON_LOG" >&2
    exit 1
  fi

  RETRIEVAL_CMD=(
    "$PYTHON_BIN" -m dmem_locomo
  )
  [[ -n "$EVAL_CONFIG" ]] && RETRIEVAL_CMD+=(--config "$EVAL_CONFIG")
  RETRIEVAL_CMD+=(
    run
    --dataset "$DATASET"
    --socket "$SOCKET_PATH"
    --run-id "$RUN_ID"
    --output "$OUTPUT"
  )
  [[ "$RESUME" == true ]] && RETRIEVAL_CMD+=(--resume)
  [[ -n "$RETRIEVAL_MODE" ]] && RETRIEVAL_CMD+=(--retrieval-mode "$RETRIEVAL_MODE")
  [[ -n "$CORPUS_OUTPUT" ]] && RETRIEVAL_CMD+=(--corpus-output "$CORPUS_OUTPUT")
  [[ ${#RETRIEVAL_ARGS[@]} -gt 0 ]] && RETRIEVAL_CMD+=("${RETRIEVAL_ARGS[@]}")

  if [[ "$STAGE" == "corpus" ]]; then
    printf 'running corpus ingestion stage\n' >&2
    RETRIEVAL_CMD=("$PYTHON_BIN" -m dmem_locomo)
    [[ -n "$EVAL_CONFIG" ]] && RETRIEVAL_CMD+=(--config "$EVAL_CONFIG")
    RETRIEVAL_CMD+=(corpus --dataset "$DATASET" --socket "$SOCKET_PATH" --run-id "$RUN_ID" --output "$OUTPUT")
    [[ "$RESUME" == true ]] && RETRIEVAL_CMD+=(--resume)
  elif [[ "$STAGE" == "embeddings" ]]; then
    printf 'running embedding build stage\n' >&2
    RETRIEVAL_CMD=("$PYTHON_BIN" -m dmem_locomo)
    [[ -n "$EVAL_CONFIG" ]] && RETRIEVAL_CMD+=(--config "$EVAL_CONFIG")
    RETRIEVAL_CMD+=(embeddings --dataset "$DATASET" --corpus-output "$CORPUS_OUTPUT" --socket "$SOCKET_PATH")
  elif [[ "$STAGE" == "all" ]]; then
    printf '[1/3] running retrieval stage\n' >&2
  else
    printf '[1/1] running retrieval stage\n' >&2
  fi
  (
    cd "$SCRIPT_DIR"
    "${RETRIEVAL_CMD[@]}"
  )
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
    cd "$SCRIPT_DIR"
    "$PYTHON_BIN" -m dmem_locomo --config "$EVAL_CONFIG" generate \
      --dataset "$DATASET" --output "$OUTPUT"
  )

  if [[ "$STAGE" == "all" ]]; then
    printf '[3/3] running QA judge stage\n' >&2
  else
    printf '[2/2] running QA judge stage\n' >&2
  fi
  (
    cd "$SCRIPT_DIR"
    "$PYTHON_BIN" -m dmem_locomo --config "$EVAL_CONFIG" judge \
      --dataset "$DATASET" --output "$OUTPUT"
  )

  printf 'summarizing retrieval and QA results\n' >&2
  (
    cd "$SCRIPT_DIR"
    "$PYTHON_BIN" -m dmem_locomo summarize \
      --dataset "$DATASET" --output "$OUTPUT"
  )
fi

printf 'results: %s\n' "$OUTPUT" >&2
