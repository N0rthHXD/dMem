#!/usr/bin/env bash
# UNSUPPORTED: monitors dMem internal SQLite tables directly.
#
# This developer-only script reads the private storage schema and is not part
# of any supported public contract. It may break without notice. Use
# evaluation/longmemeval/monitor_daemon.sh (public health API) instead.
#
# Usage:
#   ./monitor_daemon.sh --db /tmp/dmtest7/daemon-workspace/data/dmem.db
#   ./monitor_daemon.sh --output /tmp/dmtest7                 # summary + details
#   ./monitor_daemon.sh --output /tmp/dmtest7 --summary       # summary only
#   ./monitor_daemon.sh --output /tmp/dmtest7 --watch 5       # summary, refresh 5s
set -u

DB=""
INTERVAL=""
SUMMARY=0

usage() {
  sed -n '2,9p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --db)          DB="$2"; shift 2;;
    --output)      DB="$2/daemon-workspace/data/dmem.db"; shift 2;;
    --watch|-w)    INTERVAL="$2"; shift 2;;
    --summary|-s)  SUMMARY=1; shift;;
    -h|--help)     usage 0;;
    *)             usage 1;;
  esac
done

[[ -n "$DB" ]] || { echo "error: --db or --output required" >&2; usage 1; }
[[ -f "$DB" ]] || { echo "error: db not found: $DB" >&2; exit 1; }

q()   { sqlite3 -header -column "$DB" "$1"; }
n()   { sqlite3 "$DB" "SELECT count(*) FROM $1;"; }
one() { sqlite3 "$DB" "SELECT $1;"; }

summary() {
  local ev_ok ev_proc ev_store ev_fail
  local ex_ok ex_retry em_ok sem_ok
  local blocked failed running waiting
  local mem search cand_conf cand_pend
  local kw_state kw_ver fts
  local sem_state sem_emb sem_elig sem_dim

  ev_ok=$(one "coalesce(sum(processing_status='processed'),0) FROM raw_events")
  ev_proc=$(one "coalesce(sum(processing_status='processing'),0) FROM raw_events")
  ev_store=$(one "coalesce(sum(processing_status='stored'),0) FROM raw_events")
  ev_fail=$(one "coalesce(sum(processing_status='failed'),0) FROM raw_events")

  ex_ok=$(one "coalesce(sum(status='succeeded'),0) FROM jobs WHERE job_type='extract_memory'")
  ex_retry=$(one "coalesce(sum(status='retrying'),0) FROM jobs WHERE job_type='extract_memory'")
  em_ok=$(one "coalesce(sum(status='succeeded'),0) FROM jobs WHERE job_type='embed_memory'")
  sem_ok=$(one "coalesce(sum(status='succeeded'),0) FROM jobs WHERE job_type='rebuild_semantic'")

  blocked=$(n "jobs WHERE status='blocked_policy'")
  failed=$(n "jobs WHERE status='failed'")
  running=$(n "jobs WHERE status='running'")
  waiting=$(n "jobs WHERE status IN ('pending','waiting')")

  mem=$(n "memories")
  search=$(n "memory_projections WHERE searchable=1")
  cand_conf=$(one "coalesce(sum(review_state='confirmed'),0) FROM candidates")
  cand_pend=$(one "coalesce(sum(review_state='pending'),0) FROM candidates")

  kw_state=$(one "state FROM index_metadata WHERE index_name='memory_fts'")
  kw_state=${kw_state:--}
  kw_ver=$(one "index_version FROM index_metadata WHERE index_name='memory_fts'")
  kw_ver=${kw_ver:--}
  fts=$(sqlite3 "$DB" "SELECT count(*) FROM memory_fts;" 2>/dev/null || echo -1)

  sem_state=$(one "state FROM semantic_index_metadata WHERE singleton=1")
  sem_state=${sem_state:--}
  sem_emb=$(one "coalesce(embedded_revision_count,0) FROM semantic_index_metadata WHERE singleton=1")
  sem_emb=${sem_emb:-0}
  sem_elig=$(one "coalesce(eligible_revision_count,0) FROM semantic_index_metadata WHERE singleton=1")
  sem_elig=${sem_elig:-0}
  sem_dim=$(one "coalesce(dimension,0) FROM semantic_index_metadata WHERE singleton=1")
  sem_dim=${sem_dim:-0}

  echo "──────────────────────────────────────────────────────────────"
  echo " dMem eval   $(basename "$DB")   $(date '+%F %T')"
  echo "──────────────────────────────────────────────────────────────"
  printf " events     processed %s · processing %s · stored %s · failed %s\n" \
         "$ev_ok" "$ev_proc" "$ev_store" "$ev_fail"
  printf " jobs       extract %s ok / %s retry · embed %s ok · semantic %s ok\n" \
         "$ex_ok" "$ex_retry" "$em_ok" "$sem_ok"
  printf "            blocked %s · failed %s · running %s · waiting %s\n" \
         "$blocked" "$failed" "$running" "$waiting"
  printf " memories   %s (%s searchable) · candidates %s confirmed / %s pending\n" \
         "$mem" "$search" "$cand_conf" "$cand_pend"
  if [[ "$fts" -ge 0 ]]; then
    printf " index      keyword %s v%s %s/%s · semantic %s %s/%s (dim %s)\n" \
           "$kw_state" "$kw_ver" "$fts" "$search" "$sem_state" "$sem_emb" "$sem_elig" "$sem_dim"
  else
    printf " index      keyword %s v%s · semantic %s %s/%s (dim %s)\n" \
           "$kw_state" "$kw_ver" "$sem_state" "$sem_emb" "$sem_elig" "$sem_dim"
  fi
}

alerts() {
  local blocked failed_ev fts searchable degraded
  blocked=$(n "jobs WHERE status IN ('blocked_policy','failed')")
  failed_ev=$(n "raw_events WHERE processing_status='failed'")
  fts=$(sqlite3 "$DB" "SELECT count(*) FROM memory_fts;" 2>/dev/null || echo -1)
  searchable=$(n "memory_projections WHERE searchable=1")
  degraded=$(n "index_metadata WHERE index_name='memory_fts' AND state='degraded'")

  echo; echo "[alerts]"
  [[ "$blocked" == "0" ]]   && echo "  ok: no blocked/failed jobs"    || echo "  WARN: $blocked blocked/failed job(s)"
  [[ "$failed_ev" == "0" ]] && echo "  ok: no failed events"           || echo "  WARN: $failed_ev failed event(s)"
  [[ "$degraded" == "0" ]]  && echo "  ok: keyword index ready"        || echo "  WARN: keyword index degraded"
  if [[ "$fts" -ge 0 && "$fts" != "$searchable" ]]; then
    echo "  WARN: fts_rows($fts) != searchable($searchable)"
  elif [[ "$fts" -ge 0 ]]; then
    echo "  ok: keyword index complete ($fts rows)"
  else
    echo "  note: memory_fts unreadable by this sqlite3 (trigram?), skipped completeness check"
  fi
}

details() {
  echo; echo "[1] jobs by status"
  q "SELECT status, count(*) AS n FROM jobs GROUP BY status ORDER BY n DESC;"

  echo; echo "[2] jobs by type x status"
  q "SELECT job_type, status, count(*) AS n FROM jobs GROUP BY job_type, status ORDER BY job_type, status;"

  echo; echo "[3] running jobs (live concurrency)"
  q "SELECT job_id, job_type, target_id, attempt_count, lease_owner, started_at FROM jobs WHERE status='running';"

  echo; echo "[4] worker attempts (parallelism)"
  q "SELECT worker_id, count(*) AS attempts, sum(outcome='succeeded') AS ok FROM job_attempts GROUP BY worker_id ORDER BY attempts DESC;"

  echo; echo "[5] blocked / failed"
  q "SELECT job_type, status, coalesce(blocked_reason_code,last_error_code,'') AS reason, count(*) AS n
     FROM jobs WHERE status IN ('blocked_policy','failed')
     GROUP BY job_type, status, reason ORDER BY job_type;"

  echo; echo "[6] events pipeline"
  q "SELECT processing_status, count(*) AS n FROM raw_events GROUP BY processing_status;"

  echo; echo "[7] candidates"
  q "SELECT review_state, count(*) AS n FROM candidates GROUP BY review_state;"

  echo; echo "[8] spaces / memories"
  q "SELECT (SELECT count(*) FROM memory_spaces WHERE space_type='task') AS task_spaces,
             (SELECT count(*) FROM memories) AS memories,
             (SELECT count(*) FROM memory_projections WHERE searchable=1) AS searchable;"

  echo; echo "[9] keyword (FTS) index"
  q "SELECT index_name, index_version, state, source_revision_count, updated_at FROM index_metadata WHERE index_name='memory_fts';"
  q "SELECT (SELECT count(*) FROM memory_fts) AS fts_rows,
             (SELECT count(*) FROM memory_projections WHERE searchable=1) AS searchable_rows;"

  echo; echo "[10] semantic (embedding) index"
  q "SELECT state, active_version, source_revision_count, eligible_revision_count, embedded_revision_count, excluded_revision_count, dimension
     FROM semantic_index_metadata WHERE singleton=1;"
  q "SELECT embedding_state, count(*) AS n FROM memory_projections WHERE searchable=1 GROUP BY embedding_state;"
}

if [[ -n "$INTERVAL" ]]; then
  while true; do
    clear
    summary
    alerts
    sleep "$INTERVAL"
  done
elif [[ "$SUMMARY" == "1" ]]; then
  summary
else
  summary
  alerts
  details
fi
