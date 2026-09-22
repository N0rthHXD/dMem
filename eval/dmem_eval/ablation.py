"""Retrieval-channel ablation: per-mode summaries and channel-effect deltas.

dMem's Agent Tool recall contract does not let a caller choose retrieval
channels. The harness therefore realises each ablation mode as a *daemon
configuration*: ``graph_policy = off`` removes the graph channel, and disabling
the embedding provider removes the semantic channel (recall then reports it in
``degraded_capabilities``). ``keyword`` is always available, so the four default
modes cover hybrid, graph-off, semantic-off and keyword-only.

Every mode runs against the same built index; the harness only restarts the
daemon between modes.
"""

from __future__ import annotations

import sqlite3
from pathlib import Path
from typing import Any

from .io_utils import write_json

_METRICS = ("hit", "all", "coverage", "mrr", "ndcg")
_BASE_CHANNELS = ("keyword", "semantic")


def graph_corpus_counts(database_path: Path) -> dict[str, Any] | None:
    """Read the graph corpus size so a flat graph ablation is interpretable.

    The Agent Tool commit route does not create entity/fact/relation proposals,
    so a graph channel can execute while contributing nothing. Reporting the
    corpus size makes that explicit instead of presenting noise as a result.
    """

    if not database_path.exists():
        return None
    try:
        connection = sqlite3.connect(
            f"file:{database_path}?mode=ro", uri=True, timeout=5
        )
    except sqlite3.Error:
        return None
    try:
        def scalar(sql: str) -> int | None:
            try:
                row = connection.execute(sql).fetchone()
            except sqlite3.Error:
                return None
            return int(row[0]) if row and row[0] is not None else None

        state: str | None = None
        try:
            row = connection.execute(
                "SELECT state FROM graph_index_metadata WHERE singleton=1"
            ).fetchone()
            state = str(row[0]) if row else None
        except sqlite3.Error:
            state = None
        return {
            "active_entities": scalar("SELECT count(*) FROM entities WHERE state='active'"),
            "active_facts": scalar("SELECT count(*) FROM entity_facts WHERE state='active'"),
            "active_relations": scalar("SELECT count(*) FROM memory_relations WHERE state='active'"),
            "graph_index_state": state,
        }
    finally:
        connection.close()


def requested_channels(mode: dict[str, Any]) -> list[str]:
    """The channels a daemon configured for ``mode`` will ask recall for."""

    channels = list(_BASE_CHANNELS)
    if str(mode.get("graph_policy") or "features_only") != "off":
        channels.append("graph")
    return channels


def intentional_disables(mode: dict[str, Any]) -> list[str]:
    """Channels the mode turns off on purpose.

    The core reports a policy-disabled graph channel and a provider-less semantic
    channel in ``degraded_capabilities``; for the ablation those are expected, so
    they are separated from genuine degradation.
    """

    disabled: list[str] = []
    if str(mode.get("graph_policy") or "features_only") == "off":
        disabled.append("graph")
    if not bool(mode.get("embedding", True)):
        disabled.append("semantic")
    return disabled


def summarize_mode(
    mode: dict[str, Any], summary: dict[str, Any]
) -> dict[str, Any]:
    """Fold one mode's ``retrieval_summary.json`` into an ablation row."""

    degradation_counts = dict(summary.get("degradation_counts") or {})
    disabled = intentional_disables(mode)
    degraded = set(degradation_counts)
    requested = requested_channels(mode)
    return {
        "name": str(mode["name"]),
        "graph_policy": str(mode.get("graph_policy") or "features_only"),
        "embedding_enabled": bool(mode.get("embedding", True)),
        "channels_requested": requested,
        "channels_executed": [channel for channel in requested if channel not in degraded],
        "disabled_channels": disabled,
        "degradation_counts": degradation_counts,
        "unexpected_degradation_counts": {
            channel: count
            for channel, count in degradation_counts.items()
            if channel not in disabled
        },
        "queries": summary.get("queries"),
        "failed_queries": summary.get("failed_queries"),
        "metrics": summary.get("metrics") or {},
    }


def _metric(metrics: dict[str, Any], cutoff: int, name: str) -> float | None:
    value = ((metrics.get("overall") or {}).get(f"@{cutoff}") or {}).get(name)
    return float(value) if isinstance(value, (int, float)) else None


def _row_delta(
    metrics: dict[str, Any], baseline: dict[str, Any], cutoffs: list[int]
) -> dict[str, dict[str, float | None]]:
    result: dict[str, dict[str, float | None]] = {}
    for cutoff in cutoffs:
        bucket: dict[str, float | None] = {}
        for name in _METRICS:
            after = _metric(metrics, cutoff, name)
            before = _metric(baseline, cutoff, name)
            bucket[name] = (
                round(after - before, 6)
                if after is not None and before is not None
                else None
            )
        result[f"@{cutoff}"] = bucket
    return result


def build_ablation(
    run_dir: Path,
    *,
    run_id: str,
    modes: list[dict[str, Any]],
    summaries: list[dict[str, Any]],
    cutoffs: list[int],
    graph_corpus: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """Aggregate per-mode retrieval summaries and write the ablation report."""

    cutoffs = sorted({int(cutoff) for cutoff in cutoffs})
    rows = [
        summarize_mode(mode, summary) for mode, summary in zip(modes, summaries)
    ]
    by_name = {row["name"]: row for row in rows}
    baseline = by_name.get("hybrid") or (rows[0] if rows else None)

    deltas: dict[str, Any] = {}
    if baseline is not None:
        for row in rows:
            if row is baseline:
                continue
            deltas[row["name"]] = _row_delta(
                row["metrics"], baseline["metrics"], cutoffs
            )

    effects: dict[str, Any] = {}

    def add_effect(label: str, after: str, before: str, channels: list[str]) -> None:
        if after not in by_name or before not in by_name:
            return
        effects[label] = {
            "after": after,
            "before": before,
            "channels": channels,
            "delta": _row_delta(
                by_name[after]["metrics"], by_name[before]["metrics"], cutoffs
            ),
        }

    add_effect("semantic", "graph-off", "keyword-only", ["semantic"])
    add_effect("graph_with_semantic", "hybrid", "graph-off", ["graph"])
    add_effect("graph_without_semantic", "semantic-off", "keyword-only", ["graph"])

    payload = {
        "run_id": run_id,
        "baseline": baseline["name"] if baseline else None,
        "cutoffs": cutoffs,
        "graph_corpus": graph_corpus,
        "modes": rows,
        "deltas_vs_baseline": deltas,
        "channel_effects": effects,
    }
    write_json(run_dir / "retrieval_ablation.json", payload)
    (run_dir / "retrieval_ablation.md").write_text(
        render_markdown(payload), encoding="utf-8"
    )
    return payload


def _pct(value: Any) -> str:
    if value is None:
        return "-"
    try:
        return f"{float(value) * 100:.2f}%"
    except (TypeError, ValueError):
        return str(value)


def _signed_pct(value: Any) -> str:
    if value is None:
        return "-"
    try:
        number = float(value) * 100
    except (TypeError, ValueError):
        return str(value)
    return f"{number:+.2f}pp"


def render_markdown(payload: dict[str, Any]) -> str:
    lines: list[str] = []
    lines.append(f"# 检索通道消融：{payload.get('run_id')}")
    lines.append("")
    lines.append(
        "dMem 的 Agent Tool 召回契约不暴露通道选择，因此每个模式通过 daemon 配置实现："
        "`graph_policy=off` 关闭图谱通道，关闭 embedding provider 使语义通道降级，"
        "`keyword` 始终可用。所有模式复用同一份已构建索引。"
    )
    lines.append("")

    lines.append("## 模式与通道")
    lines.append("")
    lines.append("| 模式 | graph_policy | embedding | 请求通道 | 实际执行通道 | 预期禁用 | 意外降级 | 查询数 |")
    lines.append("| --- | --- | --- | --- | --- | --- | --- | --- |")
    for row in payload.get("modes") or []:
        lines.append(
            f"| {row['name']} | `{row['graph_policy']}` | "
            f"{'on' if row['embedding_enabled'] else 'off'} | "
            f"{', '.join(row['channels_requested'])} | "
            f"{', '.join(row['channels_executed']) or '-'} | "
            f"{', '.join(row.get('disabled_channels') or []) or '-'} | "
            f"`{row.get('unexpected_degradation_counts') or {}}` | {row.get('queries')} |"
        )
    lines.append("")
    lines.append(
        "> `预期禁用` 是模式刻意关闭的通道（核心仍会把它列入 `degraded_capabilities`）；"
        "`意外降级` 为空表示消融配置按预期生效。"
    )
    lines.append("")

    graph = payload.get("graph_corpus") or {}
    if graph:
        lines.append("## 图谱语料")
        lines.append("")
        lines.append(
            f"- 活跃实体 {graph.get('active_entities')}，活跃事实 "
            f"{graph.get('active_facts')}，活跃关系 {graph.get('active_relations')}，"
            f"graph index：`{graph.get('graph_index_state')}`"
        )
        if not any(
            graph.get(key)
            for key in ("active_entities", "active_facts", "active_relations")
        ):
            lines.append(
                "- 图谱语料为空：Agent Tool 提交路线不产生 entity/fact/relation，"
                "graph 通道虽然执行但没有可加入的候选，因此 graph 的消融差异应视为"
                "零贡献（噪声），而不是真实增益。"
            )
        lines.append("")

    modes = payload.get("modes") or []
    cutoffs = payload.get("cutoffs") or []
    if modes and cutoffs:
        lines.append("## 总体检索指标")
        lines.append("")
        header = "| 模式 | cutoff | hit | all | turn coverage | MRR | nDCG |"
        lines.append(header)
        lines.append("| --- | --- | --- | --- | --- | --- | --- |")
        for row in modes:
            overall = (row.get("metrics") or {}).get("overall") or {}
            for cutoff in cutoffs:
                values = overall.get(f"@{cutoff}") or {}
                lines.append(
                    f"| {row['name']} | @{cutoff} | {_pct(values.get('hit'))} | "
                    f"{_pct(values.get('all'))} | {_pct(values.get('coverage'))} | "
                    f"{_pct(values.get('mrr'))} | {_pct(values.get('ndcg'))} |"
                )
        lines.append("")

        effects = payload.get("channel_effects") or {}
        if effects:
            lines.append("## 通道增益（相对差异，单位：百分点）")
            lines.append("")
            lines.append("| 通道 | 对比 | cutoff | Δhit | Δcoverage | ΔMRR | ΔnDCG |")
            lines.append("| --- | --- | --- | --- | --- | --- | --- |")
            for label, effect in effects.items():
                for cutoff in cutoffs:
                    values = (effect.get("delta") or {}).get(f"@{cutoff}") or {}
                    lines.append(
                        f"| {label} | {effect['after']} − {effect['before']} | @{cutoff} | "
                        f"{_signed_pct(values.get('hit'))} | "
                        f"{_signed_pct(values.get('coverage'))} | "
                        f"{_signed_pct(values.get('mrr'))} | "
                        f"{_signed_pct(values.get('ndcg'))} |"
                    )
            lines.append("")

        baseline = payload.get("baseline")
        deltas = payload.get("deltas_vs_baseline") or {}
        if baseline and deltas:
            lines.append(f"## 相对基线 `{baseline}` 的差异（单位：百分点）")
            lines.append("")
            lines.append("| 模式 | cutoff | Δhit | Δcoverage | ΔMRR | ΔnDCG |")
            lines.append("| --- | --- | --- | --- | --- | --- |")
            for name, per_cutoff in deltas.items():
                for cutoff in cutoffs:
                    values = per_cutoff.get(f"@{cutoff}") or {}
                    lines.append(
                        f"| {name} | @{cutoff} | {_signed_pct(values.get('hit'))} | "
                        f"{_signed_pct(values.get('coverage'))} | "
                        f"{_signed_pct(values.get('mrr'))} | "
                        f"{_signed_pct(values.get('ndcg'))} |"
                    )
            lines.append("")

        lines.append("## 分问题类型（@10）")
        lines.append("")
        lines.append("| 模式 | category | hit | turn coverage | MRR | nDCG |")
        lines.append("| --- | --- | --- | --- | --- | --- |")
        for row in modes:
            by_category = (row.get("metrics") or {}).get("by_category") or {}
            for category, buckets in by_category.items():
                values = buckets.get("@10") or {}
                lines.append(
                    f"| {row['name']} | {category} | {_pct(values.get('hit'))} | "
                    f"{_pct(values.get('coverage'))} | {_pct(values.get('mrr'))} | "
                    f"{_pct(values.get('ndcg'))} |"
                )
        lines.append("")
    return "\n".join(lines)
