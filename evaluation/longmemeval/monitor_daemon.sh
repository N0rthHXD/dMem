#!/usr/bin/env bash
# Monitor a running dMem daemon through its public health API.
#
# Usage:
#   ./monitor_daemon.sh --socket /path/to/runtime/dmem.sock
#   ./monitor_daemon.sh --output /path/to/run-dir       # derive daemon-workspace socket
#   ./monitor_daemon.sh --socket PATH --summary         # summary only
#   ./monitor_daemon.sh --socket PATH --watch 5         # refresh every 5s
set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export PYTHONPATH="$SCRIPT_DIR/../common${PYTHONPATH:+:$PYTHONPATH}"

SOCKET=""
INTERVAL=""
SUMMARY=0
PYTHON_BIN="${PYTHON:-python3}"

usage() {
  sed -n '2,9p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --socket)     SOCKET="$2"; shift 2;;
    --output)     SOCKET="$2/daemon-workspace/runtime/dmem.sock"; shift 2;;
    --watch|-w)   INTERVAL="$2"; shift 2;;
    --summary|-s) SUMMARY=1; shift;;
    -h|--help)    usage 0;;
    *)            usage 1;;
  esac
done

[[ -n "$SOCKET" ]] || { printf 'error: --socket or --output required\n' >&2; usage 1; }
[[ -S "$SOCKET" ]] || { printf 'error: socket not found: %s\n' "$SOCKET" >&2; exit 1; }

run_health() {
  "$PYTHON_BIN" - "$SOCKET" "$SUMMARY" <<'PY'
import json
import sys

from dmem_eval.api import DMemClient

socket_path, summary_only = sys.argv[1], sys.argv[2] == "1"
with DMemClient(socket_path, caller_id="monitor") as client:
    health = client.health()

if summary_only:
    fields = (
        "overall_status", "ready", "query_available", "accepting_writes",
        "storage_state", "schema_compatibility", "queue_state",
        "job_queue_depth", "oldest_job_age_seconds",
        "fts_index_state", "semantic_state", "semantic_index_state",
        "graph_index_state", "processor_state",
    )
    for field in fields:
        print(f"{field}: {health.get(field)}")
else:
    print(json.dumps(health, indent=2, sort_keys=True))
PY
}

if [[ -n "$INTERVAL" ]]; then
  while true; do
    clear
    run_health
    sleep "$INTERVAL"
  done
else
  run_health
fi
