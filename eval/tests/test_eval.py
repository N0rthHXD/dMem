"""Unit tests for the LoCoMo evaluation harness.

Run with:
    /home/ut005426@uos/miniforge3/envs/agent/bin/python -m unittest discover -s eval/tests
"""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

EVAL_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(EVAL_ROOT))

from dmem_eval.config import load_config  # noqa: E402
from dmem_eval.ablation import (  # noqa: E402
    build_ablation,
    graph_corpus_counts,
    requested_channels,
    summarize_mode,
)
from dmem_eval.daemon import (  # noqa: E402
    ManagedDaemon,
    effective_dmem,
    render_daemon_config,
)
from dmem_eval.dataset import load_dataset, validate_dataset  # noqa: E402
from dmem_eval.metrics import evaluate_retrieval  # noqa: E402
from dmem_eval.phases import _build_context, _normalize_memories, load_maps  # noqa: E402

CONFIG_PATH = EVAL_ROOT / "config.zh-subset.yaml"


class ConfigTests(unittest.TestCase):
    def test_load_and_defaults(self) -> None:
        config = load_config(CONFIG_PATH)
        self.assertEqual(config.get("run_id"), "zh-subset")
        self.assertEqual(config.section("retrieval")["cutoffs"], [1, 3, 5, 10, 30])
        self.assertTrue(config.section("dmem")["manage_daemon"])

    def test_daemon_config_allows_tool_call_and_user_scope(self) -> None:
        config = load_config(CONFIG_PATH)
        rendered = render_daemon_config(config, Path("/tmp/dmem-eval-test"))
        self.assertIn('source_types = ["tool_call"', rendered)
        self.assertIn('space_type = "project"', rendered)
        self.assertIn('space_type = "user"', rendered)
        self.assertIn("enabled = false", rendered)  # processing stays off

    def test_ablation_modes_parse(self) -> None:
        config = load_config(CONFIG_PATH)
        modes = config.ablation_modes
        self.assertEqual([mode["name"] for mode in modes],
                         ["hybrid", "graph-off", "semantic-off", "keyword-only"])
        self.assertEqual(requested_channels(modes[0]), ["keyword", "semantic", "graph"])
        self.assertEqual(requested_channels(modes[1]), ["keyword", "semantic"])
        self.assertEqual(requested_channels(modes[2]), ["keyword", "semantic", "graph"])
        # semantic is always requested; without a provider it is degraded.
        self.assertEqual(requested_channels(modes[3]), ["keyword", "semantic"])

    def test_ablation_rejects_duplicate_names(self) -> None:
        with self.assertRaises(ValueError):
            load_config(CONFIG_PATH, overrides={
                "retrieval": {"ablation": {"modes": [
                    {"name": "dup", "graph_policy": "off", "embedding": False},
                    {"name": "dup", "graph_policy": "off", "embedding": False},
                ]}}
            })

    def test_ablation_rejects_unknown_graph_policy(self) -> None:
        with self.assertRaises(ValueError):
            load_config(CONFIG_PATH, overrides={
                "retrieval": {"ablation": {"modes": [
                    {"name": "bad", "graph_policy": "everything", "embedding": False},
                ]}}
            })


class DaemonOverlayTests(unittest.TestCase):
    def test_overlay_disables_graph_and_embedding(self) -> None:
        config = load_config(CONFIG_PATH)
        dmem = effective_dmem(config, {"graph_policy": "off", "embedding": False})
        self.assertEqual(dmem["graph_policy"], "off")
        self.assertFalse(dmem["embedding"]["enabled"])
        # Other embedding settings survive so the provider can be rebuilt.
        self.assertEqual(dmem["embedding"]["model"],
                         config.section("dmem")["embedding"]["model"])
        rendered = render_daemon_config(config, Path("/tmp/dmem-eval"), {"embedding": False})
        self.assertIn("enabled = false", rendered)
        self.assertNotIn("[embedding.provider]", rendered)

    def test_mode_daemons_share_one_data_dir(self) -> None:
        config = load_config(CONFIG_PATH)
        run_dir = Path("/tmp/dmem-run")
        base = ManagedDaemon(config, run_dir, name="daemon")
        mode = ManagedDaemon(config, run_dir, name="ablation/hybrid")
        self.assertEqual(base.data_dir, mode.data_dir)
        self.assertEqual(mode.daemon_dir, run_dir / "ablation" / "hybrid")


class AblationAggregationTests(unittest.TestCase):
    def _metrics(self, hit: float) -> dict:
        return {"overall": {"@10": {"hit": hit, "all": hit, "coverage": hit,
                                    "mrr": hit, "ndcg": hit}}}

    def test_summarize_and_channel_effects(self) -> None:
        modes = [
            {"name": "hybrid", "graph_policy": "features_only", "embedding": True},
            {"name": "graph-off", "graph_policy": "off", "embedding": True},
            {"name": "keyword-only", "graph_policy": "off", "embedding": False},
        ]
        summaries = [
            {"queries": 3, "failed_queries": 0, "degradation_counts": {},
             "metrics": self._metrics(0.9)},
            {"queries": 3, "failed_queries": 0, "degradation_counts": {},
             "metrics": self._metrics(0.7)},
            {"queries": 3, "failed_queries": 0, "degradation_counts": {"semantic": 3},
             "metrics": self._metrics(0.4)},
        ]
        row = summarize_mode(modes[2], summaries[2])
        self.assertEqual(row["channels_requested"], ["keyword", "semantic"])
        self.assertEqual(row["channels_executed"], ["keyword"])
        self.assertEqual(row["disabled_channels"], ["graph", "semantic"])
        self.assertEqual(row["degradation_counts"], {"semantic": 3})
        self.assertEqual(row["unexpected_degradation_counts"], {})

        payload = build_ablation(
            Path("/tmp/dmem-ablation-test"), run_id="t", modes=modes,
            summaries=summaries, cutoffs=[10],
        )
        self.assertEqual(payload["baseline"], "hybrid")
        self.assertAlmostEqual(
            payload["channel_effects"]["semantic"]["delta"]["@10"]["hit"], 0.3
        )
        self.assertAlmostEqual(
            payload["deltas_vs_baseline"]["keyword-only"]["@10"]["hit"], -0.5
        )


class GraphCorpusTests(unittest.TestCase):
    def test_graph_corpus_counts_reads_sqlite(self) -> None:
        import sqlite3
        import tempfile

        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "dmem.db"
            connection = sqlite3.connect(path)
            connection.executescript(
                "CREATE TABLE entities(state TEXT);"
                "CREATE TABLE entity_facts(state TEXT);"
                "CREATE TABLE memory_relations(state TEXT);"
                "CREATE TABLE graph_index_metadata(singleton INTEGER, state TEXT);"
                "INSERT INTO entities VALUES ('active'),('archived');"
                "INSERT INTO graph_index_metadata VALUES (1,'ready');"
            )
            connection.commit()
            connection.close()
            counts = graph_corpus_counts(path)
            self.assertEqual(counts["active_entities"], 1)
            self.assertEqual(counts["active_facts"], 0)
            self.assertEqual(counts["graph_index_state"], "ready")
            self.assertIsNone(graph_corpus_counts(Path(tmp) / "missing.db"))


class MapLoadingTests(unittest.TestCase):
    def test_load_maps_reads_from_source_dir(self) -> None:
        import tempfile

        with tempfile.TemporaryDirectory() as tmp:
            source = Path(tmp) / "base"
            target = Path(tmp) / "mode"
            source.mkdir()
            target.mkdir()
            (source / "scopes.json").write_text('{"conv-1": "scope-1"}', encoding="utf-8")
            (source / "build_memories.jsonl").write_text(
                '{"memory_id": "m1", "status": "authority", "dia_ids": ["D1:1"]}\n',
                encoding="utf-8",
            )
            (source / "build_commits.jsonl").write_text(
                '{"source_event_id": "e1", "dia_ids": ["D1:1", "D1:2"]}\n',
                encoding="utf-8",
            )
            context = type("Ctx", (), {
                "run_dir": target,
                "scope_map": {},
                "memory_map": {},
                "event_map": {},
            })()
            load_maps(context, source_dir=source)
            self.assertEqual(context.scope_map, {"conv-1": "scope-1"})
            self.assertEqual(context.memory_map, {"m1": {"D1:1"}})
            self.assertEqual(context.event_map, {"e1": {"D1:1", "D1:2"}})


class DatasetTests(unittest.TestCase):
    def test_subset_shape(self) -> None:
        config = load_config(CONFIG_PATH)
        samples, qa = load_dataset(config.resolve_path(config.section("dataset")["path"]))
        stats = validate_dataset(samples, qa)
        self.assertEqual(stats["conversations"], 2)
        self.assertEqual(stats["qa"], 304)
        self.assertEqual(stats["sessions_with_unparsed_date"], 0)
        self.assertEqual(len(samples[0].sessions[0].turns[0].dia_id) > 0, True)
        self.assertTrue(samples[0].sessions[0].occurred_at.startswith("2023-05-08T13:56"))


class NormalizeMemoryTests(unittest.TestCase):
    def test_normalization_and_guards(self) -> None:
        raw = [
            {"kind": "fact", "content": " 内容 ", "dia_ids": "d1:3", "search_hints": "提示",
             "sensitivity": "weird", "confidence": 2},
            {"kind": "scene", "content": "聚合类型应被拒绝", "dia_ids": ["D1:1"]},
            {"kind": "event", "content": "无有效证据", "dia_ids": ["D99:1"]},
        ]
        memories = _normalize_memories(raw, {"D1:3"}, {"max_content_runes": 100, "max_search_hints": 4})
        self.assertEqual(len(memories), 1)
        memory = memories[0]
        self.assertEqual(memory["dia_ids"], ["D1:3"])
        self.assertEqual(memory["search_hints"], ["提示"])
        self.assertEqual(memory["sensitivity"], "normal")
        self.assertEqual(memory["confidence"], 1.0)


class MetricsTests(unittest.TestCase):
    def test_hit_coverage_mrr_ndcg(self) -> None:
        records = [
            {
                "qa_id": "a",
                "category": 4,
                "gold": ["D1:1", "D1:2"],
                "ranked": [
                    {"dia_ids": ["D9:9"]},
                    {"dia_ids": ["D1:1"]},
                    {"dia_ids": ["D1:2"]},
                ],
            }
        ]
        metrics = evaluate_retrieval(records, [1, 2, 3])
        self.assertEqual(metrics["usable_queries"], 1)
        self.assertAlmostEqual(metrics["overall"]["@1"]["hit"], 0.0)
        self.assertAlmostEqual(metrics["overall"]["@2"]["hit"], 1.0)
        self.assertAlmostEqual(metrics["overall"]["@2"]["coverage"], 0.5)
        self.assertAlmostEqual(metrics["overall"]["@3"]["coverage"], 1.0)
        self.assertAlmostEqual(metrics["overall"]["@2"]["mrr"], 0.5)
        self.assertLessEqual(metrics["overall"]["@3"]["ndcg"], 1.0)

    def test_queries_without_gold_are_skipped(self) -> None:
        metrics = evaluate_retrieval([{"qa_id": "x", "category": 5, "gold": [], "ranked": []}], [1])
        self.assertEqual(metrics["usable_queries"], 0)
        self.assertEqual(metrics["skipped_queries"], 1)


class ContextTests(unittest.TestCase):
    def test_context_respects_budget(self) -> None:
        record = {"ranked": [{"content": "a" * 50}, {"content": "b" * 50}]}
        context = _build_context(record, cutoff=10, max_chars=60)
        self.assertIn("a" * 50, context)
        self.assertNotIn("b" * 50, context)


if __name__ == "__main__":
    unittest.main()
