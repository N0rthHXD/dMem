"""Append-only checkpoint for resume support.

Each phase (ingestion, retrieval, generation, judge) writes to its own JSONL
file. Records are appended and flushed after each operation, so a crashed run
can resume from the last completed record without re-ingesting or re-querying.
"""

from __future__ import annotations

import json
import os
import tempfile
import threading
from pathlib import Path
from typing import Any


class Checkpoint:
    def __init__(self, output_dir: str | Path):
        self.dir = Path(output_dir)
        self.dir.mkdir(parents=True, exist_ok=True)
        self.manifest_path = self.dir / "manifest.json"
        self.validation_path = self.dir / "validation.json"
        self.ingestion_path = self.dir / "ingestion.jsonl"
        self.retrieval_path = self.dir / "retrieval.jsonl"
        self.generation_path = self.dir / "generation.jsonl"
        self.judge_path = self.dir / "judge.jsonl"
        self.failure_path = self.dir / "failures.jsonl"
        self.summary_path = self.dir / "summary.json"
        self._lock = threading.RLock()

    # -- manifest --

    def write_manifest(self, manifest: dict[str, Any]) -> None:
        self._atomic_write_json(self.manifest_path, manifest)

    def read_manifest(self) -> dict[str, Any] | None:
        with self._lock:
            if not self.manifest_path.exists():
                return None
            return json.loads(self.manifest_path.read_text(encoding="utf-8"))

    def is_compatible(self, config: dict[str, Any]) -> tuple[bool, str]:
        manifest = self.read_manifest()
        if manifest is None:
            return True, "no existing manifest"
        for key in (
            "dataset_sha", "run_id", "confirmation_policy", "retrieval_mode",
            "limit", "max_chars", "cutoffs", "answer_cutoff",
            "invalid_evidence_reference_retries",
        ):
            if key == "invalid_evidence_reference_retries" and key not in manifest:
                continue
            if manifest.get(key) != config.get(key):
                return False, f"config mismatch on {key}"
        return True, "compatible"

    # -- append --

    def _append_line(self, path: Path, record: dict[str, Any]) -> None:
        payload = json.dumps(record, ensure_ascii=False) + "\n"
        with self._lock:
            with open(path, "a", encoding="utf-8") as f:
                f.write(payload)
                f.flush()
                os.fsync(f.fileno())

    def append_ingestion(self, record: dict[str, Any]) -> None:
        self._append_line(self.ingestion_path, record)

    def append_retrieval(self, record: dict[str, Any]) -> None:
        self._append_line(self.retrieval_path, record)

    def append_generation(self, record: dict[str, Any]) -> None:
        self._append_line(self.generation_path, record)

    def append_judge(self, record: dict[str, Any]) -> None:
        self._append_line(self.judge_path, record)

    def append_failure(self, record: dict[str, Any]) -> None:
        self._append_line(self.failure_path, record)

    # -- read-back --

    def _read_jsonl(self, path: Path) -> list[dict[str, Any]]:
        with self._lock:
            if not path.exists():
                return []
            lines = path.read_text(encoding="utf-8").splitlines()
        records = []
        for index, line in enumerate(lines):
            if line.strip():
                try:
                    records.append(json.loads(line))
                except json.JSONDecodeError:
                    if index == len(lines) - 1:
                        break
                    raise
        return records

    def load_ingestion_records(self) -> dict[str, list[dict[str, Any]]]:
        result: dict[str, list[dict[str, Any]]] = {}
        for rec in self._read_jsonl(self.ingestion_path):
            result.setdefault(rec["sample_id"], []).append(rec)
        return result

    def load_ingested_sample_ids(self) -> set[str]:
        return {
            rec["sample_id"]
            for rec in self._read_jsonl(self.ingestion_path)
            if rec.get("record_type") == "conversation_complete"
        }

    def load_retrieval_records(self) -> dict[str, dict[str, Any]]:
        return {rec["qa_id"]: rec for rec in self._read_jsonl(self.retrieval_path)}

    def load_retrieval_qids(self) -> set[str]:
        return set(self.load_retrieval_records().keys())

    def load_generation_records(self) -> dict[str, dict[str, Any]]:
        return {rec["qa_id"]: rec for rec in self._read_jsonl(self.generation_path)}

    def load_generation_qids(self) -> set[str]:
        return set(self.load_generation_records().keys())

    def load_judge_records(self) -> dict[str, dict[str, Any]]:
        return {rec["qa_id"]: rec for rec in self._read_jsonl(self.judge_path)}

    def load_judge_qids(self) -> set[str]:
        return set(self.load_judge_records().keys())

    def load_failure_records(self) -> list[dict[str, Any]]:
        return self._read_jsonl(self.failure_path)

    # -- summary / validation --

    def write_summary(self, summary: dict[str, Any]) -> None:
        self._atomic_write_json(self.summary_path, summary)

    def write_validation(self, validation: dict[str, Any]) -> None:
        self._atomic_write_json(self.validation_path, validation)

    # -- helpers --

    def _atomic_write_json(self, path: Path, data: dict[str, Any]) -> None:
        with self._lock:
            path.parent.mkdir(parents=True, exist_ok=True)
            fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=".tmp-", suffix=".json")
            try:
                with os.fdopen(fd, "w", encoding="utf-8") as f:
                    json.dump(data, f, ensure_ascii=False, indent=2)
                    f.flush()
                    os.fsync(f.fileno())
                os.replace(tmp, path)
            except Exception:
                if os.path.exists(tmp):
                    os.unlink(tmp)
                raise
