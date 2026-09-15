"""Runner tests for complete evidence mapping and layered retrieval metrics.

The runner must map every evidence event on every recalled revision (not just
the first), preserve stable chronological turn order, count unmapped results and
duplicate source turns, and report memory-level, unique-evidence-level, and
session-level metrics separately.
"""

from datetime import datetime

from dmem_locomo.checkpoint import Checkpoint
from dmem_locomo.dataset import Conversation, QA, Session, Turn
from dmem_locomo.runner import RunConfig, Runner, dia_id_sort_key


class FakeClient:
    def __init__(self, response):
        self._response = response
        self.calls = []

    def recall(self, **kwargs):
        self.calls.append(kwargs)
        return self._response


def make_conversation(sample_id="conv-1"):
    sessions = [
        Session(
            session_num=1,
            date_time="1:00 PM on 1 January, 2024",
            date=datetime(2024, 1, 1, 13, 0),
            turns=[
                Turn("A", "D1:1", 1, 1, "hello"),
                Turn("B", "D1:2", 1, 2, "world"),
            ],
        ),
        Session(
            session_num=2,
            date_time="1:00 PM on 2 January, 2024",
            date=datetime(2024, 1, 2, 13, 0),
            turns=[Turn("A", "D2:1", 2, 1, "later")],
        ),
    ]
    qa = QA(
        sample_id=sample_id,
        index=0,
        question="what happened?",
        answer="x",
        adversarial_answer=None,
        evidence_raw=["D1:1", "D2:1"],
        evidence=["D1:1", "D2:1"],
        evidence_unresolved=[],
        category=4,
    )
    return Conversation(
        sample_id=sample_id,
        speaker_a="A",
        speaker_b="B",
        sessions=sessions,
        qa=[qa],
    )


def make_runner(tmp_path, client, fail_on_degraded=False):
    config = RunConfig(
        run_id="test",
        dataset_sha="sha",
        socket_path="/tmp/dmem.sock",
        cutoffs=[1, 2, 3],
        limit=10,
        fail_on_degraded=fail_on_degraded,
    )
    cp = Checkpoint(tmp_path)
    return Runner(client, config, cp), cp


def result(memory_id, evidence_ids, channels=None):
    return {
        "memory": {
            "memory_id": memory_id,
            "current_revision": {
                "revision_id": memory_id + "-r",
                "content": "content " + memory_id,
                "evidence": [{"event_id": ev} for ev in evidence_ids],
            },
        },
        "matched_channels": channels or ["keyword"],
    }


def ingestion_records(*, unmapped_event=True):
    records = [
        {
            "record_type": "event",
            "event_id": "e1",
            "dia_id": "D1:1",
            "session_id": 1,
            "memory_ids": ["m1"],
            "confirmed": [{"candidate_id": "c1", "memory_id": "m1"}],
        },
        {
            "record_type": "event",
            "event_id": "e2",
            "dia_id": "D2:1",
            "session_id": 2,
            "memory_ids": ["m2"],
            "confirmed": [{"candidate_id": "c2", "memory_id": "m2"}],
        },
        {
            "record_type": "event",
            "event_id": "e3",
            "dia_id": "D1:2",
            "session_id": 1,
            "memory_ids": [],
            "confirmed": [],
        },
    ]
    return {"conv-1": records}


def test_dia_id_sort_key_orders_session_then_turn():
    assert sorted(["D10:1", "D2:10", "D2:2"], key=dia_id_sort_key) == [
        "D2:2",
        "D2:10",
        "D10:1",
    ]


def test_recall_maps_all_evidence_events_in_chronological_order(tmp_path):
    response = {
        "results": [result("m1", ["e2", "e1"])],
        "degraded": False,
        "degradation_reasons": [],
    }
    runner, cp = make_runner(tmp_path, FakeClient(response))
    conv = make_conversation()
    runner._recall_qa(conv, conv.qa[0], "task-1", ingestion_records())

    record = cp.load_retrieval_records()[conv.qa[0].qa_id]
    assert record["ranked_items"][0]["dia_ids"] == ["D1:1", "D2:1"]
    assert record["ranked_dia_ids"] == ["D1:1", "D2:1"]


def test_recall_counts_unmapped_results(tmp_path):
    response = {
        "results": [
            result("m1", ["unknown-event"]),
            result("m2", ["e1"]),
        ],
        "degraded": False,
        "degradation_reasons": [],
    }
    runner, cp = make_runner(tmp_path, FakeClient(response))
    conv = make_conversation()
    runner._recall_qa(conv, conv.qa[0], "task-1", ingestion_records())

    record = cp.load_retrieval_records()[conv.qa[0].qa_id]
    assert record["unmapped_count"] == 1
    assert record["unmapped_rate"] == 0.5


def test_recall_deduplicates_repeated_source_turns(tmp_path):
    response = {
        "results": [
            result("m1", ["e1"]),
            result("m2", ["e1"]),
            result("m3", ["e2"]),
        ],
        "degraded": False,
        "degradation_reasons": [],
    }
    runner, cp = make_runner(tmp_path, FakeClient(response))
    conv = make_conversation()
    runner._recall_qa(conv, conv.qa[0], "task-1", ingestion_records())

    record = cp.load_retrieval_records()[conv.qa[0].qa_id]
    assert record["ranked_dia_ids"] == ["D1:1", "D2:1"]
    assert record["duplicate_count"] >= 1


def test_recall_reports_memory_turn_and_session_layers(tmp_path):
    response = {
        "results": [result("m1", ["e1"]), result("m2", ["e2"])],
        "degraded": False,
        "degradation_reasons": [],
    }
    runner, cp = make_runner(tmp_path, FakeClient(response))
    conv = make_conversation()
    runner._recall_qa(conv, conv.qa[0], "task-1", ingestion_records())

    record = cp.load_retrieval_records()[conv.qa[0].qa_id]
    assert set(record["memory_metrics"]["@3"]) >= {
        "recall_any",
        "recall_all",
        "coverage",
        "ndcg",
        "mrr_at_k",
    }
    assert record["turn_metrics"]["@2"]["recall_all"] == 1.0
    assert record["session_metrics"]["@2"]["recall_all"] == 1.0
    assert record["ranked_sessions"] == ["S1", "S2"]


def test_recall_reports_extraction_coverage(tmp_path):
    response = {
        "results": [result("m1", ["e1"])],
        "degraded": False,
        "degradation_reasons": [],
    }
    runner, cp = make_runner(tmp_path, FakeClient(response))
    conv = make_conversation()
    runner._recall_qa(conv, conv.qa[0], "task-1", ingestion_records())

    record = cp.load_retrieval_records()[conv.qa[0].qa_id]
    assert record["extraction"]["gold_total"] == 2
    assert record["extraction"]["gold_indexed"] == 2
    assert record["extraction"]["coverage"] == 1.0
    assert record["recall_latency_ms"] >= 0
    assert record["response_bytes"] > 0


def test_recall_surfaces_gold_source_missing_from_index(tmp_path):
    response = {
        "results": [result("m1", ["e1"])],
        "degraded": False,
        "degradation_reasons": [],
    }
    runner, cp = make_runner(tmp_path, FakeClient(response))
    conv = make_conversation()
    # D2:1 event produced no confirmed memory, so the gold source is absent.
    records = ingestion_records()
    records["conv-1"][1]["memory_ids"] = []
    records["conv-1"][1]["confirmed"] = []
    runner._recall_qa(conv, conv.qa[0], "task-1", records)

    record = cp.load_retrieval_records()[conv.qa[0].qa_id]
    assert record["extraction"]["gold_indexed"] == 1
    assert record["extraction"]["coverage"] == 0.5
    assert record["extraction"]["missed"] is True


def test_finalize_manifest_uses_observed_scalar_ranking_identities(tmp_path):
    runner, cp = make_runner(tmp_path, FakeClient({}))
    cp.write_manifest(
        {
            "identities": {
                "ranking_version": "unknown",
                "fts_index_version": 0,
            }
        }
    )
    cp.append_retrieval(
        {
            "qa_id": "q1",
            "ranking_version": "ranking-weights-v1",
            "fts_index_version": 7,
            "semantic_index_version": 8,
            "graph_index_version": 9,
        }
    )

    runner._finalize_manifest_identities()

    identities = cp.read_manifest()["identities"]
    assert identities["ranking_version"] == "ranking-weights-v1"
    assert identities["fts_index_version"] == 7
    assert identities["semantic_index_version"] == 8
    assert identities["graph_index_version"] == 9
    assert "ranking_versions" not in identities
    assert "fts_index_versions" not in identities


def test_summarize_recomputes_turn_metrics_from_raw_records():
    from dmem_locomo.cli import _recompute_flat

    conv = make_conversation()
    qa = conv.qa[0]
    stale = {"@2": {"recall_all": 0.0, "recall_any": 0.0, "coverage": 0.0,
                    "ndcg": 0.0, "mrr_at_k": 0.0}}
    rec = {
        "qa_id": qa.qa_id,
        "ranked_dia_ids": ["D1:1", "D2:1"],
        "turn_metrics": stale,
    }
    flat = _recompute_flat(rec, qa, [1, 2], layer="turn")
    assert flat["recall_all@2"] == 1.0


def test_summarize_recomputes_memory_layer_from_raw_records():
    from dmem_locomo.cli import _recompute_flat

    conv = make_conversation()
    qa = conv.qa[0]
    rec = {
        "qa_id": qa.qa_id,
        "ranked_memory_ids": ["m1"],
        "gold_memory_ids": ["m1"],
        "memory_metrics": {"@1": {"recall_all": 0.0}},
    }
    flat = _recompute_flat(rec, qa, [1], layer="memory")
    assert flat["recall_all@1"] == 1.0


class BarrierClient(FakeClient):
    def __init__(self, response=None, error=None):
        super().__init__(response or {"results": [], "degraded": False})
        self.error = error
        self.trigger_calls = 0

    def trigger_and_wait_graph(self, **kwargs):
        self.trigger_calls += 1
        if self.error is not None:
            raise self.error
        return {"job_id": "graph-job"}


class IndexBuildClient(BarrierClient):
    def __init__(self):
        super().__init__()
        self.semantic_calls = 0

    def trigger_and_wait_semantic(self, **kwargs):
        self.semantic_calls += 1
        return {"job_id": "semantic-job"}


def test_shared_index_build_rebuilds_semantic_then_graph(tmp_path):
    client = IndexBuildClient()
    runner, cp = make_runner(tmp_path, client)
    cp.write_manifest({"run_type": "corpus"})

    result = runner.build_embeddings()

    assert client.semantic_calls == 1
    assert client.trigger_calls == 1
    assert result["embedding_state"] == "ready"
    assert result["graph_state"] == "ready"


def test_graph_barrier_triggers_rebuild_when_required(tmp_path):
    client = BarrierClient()
    runner, _cp = make_runner(tmp_path, client, fail_on_degraded=True)
    runner._graph_barrier()
    assert client.trigger_calls == 1


def test_graph_barrier_swallows_failure_without_fail_on_degraded(tmp_path):
    client = BarrierClient(error=RuntimeError("graph rebuild failed"))
    runner, _cp = make_runner(tmp_path, client, fail_on_degraded=False)
    runner._graph_barrier()


def test_graph_barrier_raises_failure_with_fail_on_degraded(tmp_path):
    client = BarrierClient(error=RuntimeError("graph rebuild failed"))
    runner, _cp = make_runner(tmp_path, client, fail_on_degraded=True)
    try:
        runner._graph_barrier()
    except RuntimeError as exc:
        assert "graph rebuild failed" in str(exc)
    else:
        raise AssertionError("fail_on_degraded must propagate graph barrier failure")


def test_degraded_recall_is_recorded(tmp_path):
    response = {
        "results": [result("m1", ["e1"])],
        "degraded": True,
        "degradation_reasons": ["semantic_unavailable"],
        "ranking_version": "ranking-weights-v1",
        "fts_index_version": 2,
    }
    runner, cp = make_runner(tmp_path, FakeClient(response))
    conv = make_conversation()
    runner._recall_qa(conv, conv.qa[0], "task-1", ingestion_records())

    record = cp.load_retrieval_records()[conv.qa[0].qa_id]
    assert record["degraded"] is True
    assert record["degradation_reasons"] == ["semantic_unavailable"]
    assert record["ranking_version"] == "ranking-weights-v1"
    assert record["fts_index_version"] == 2


def test_degraded_recall_raises_when_fail_on_degraded(tmp_path):
    response = {
        "results": [result("m1", ["e1"])],
        "degraded": True,
        "degradation_reasons": ["graph_unavailable"],
    }
    runner, _cp = make_runner(tmp_path, FakeClient(response), fail_on_degraded=True)
    conv = make_conversation()
    try:
        runner._recall_qa(conv, conv.qa[0], "task-1", ingestion_records())
    except RuntimeError as exc:
        assert "graph_unavailable" in str(exc)
    else:
        raise AssertionError("fail_on_degraded must reject degraded recall responses")
