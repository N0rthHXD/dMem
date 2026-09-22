"""Aggregate per-phase artifacts into summary.json and a Markdown report."""

from __future__ import annotations

from pathlib import Path
from typing import Any

from .io_utils import read_json, read_jsonl, write_json


def build_report(run_dir: Path) -> dict[str, Any]:
    build = read_json(run_dir / "build_summary.json", {}) or {}
    retrieval = read_json(run_dir / "retrieval_summary.json", {}) or {}
    ablation = read_json(run_dir / "retrieval_ablation.json", {}) or {}
    qa = read_json(run_dir / "qa_summary.json", {}) or {}
    manifest = read_json(run_dir / "manifest.json", {}) or {}
    judgements = read_jsonl(run_dir / "judge.jsonl")

    summary = {
        "run_id": manifest.get("run_id"),
        "dataset": manifest.get("dataset", {}),
        "dataset_validation": manifest.get("dataset_validation", {}),
        "identities": manifest.get("identities", {}),
        "build": build,
        "retrieval": retrieval,
        "retrieval_ablation": ablation,
        "qa": qa,
    }
    write_json(run_dir / "summary.json", summary)
    (run_dir / "report.md").write_text(
        _render_markdown(summary, judgements), encoding="utf-8"
    )
    return summary


def _render_markdown(summary: dict[str, Any], judgements: list[dict]) -> str:
    lines: list[str] = []
    lines.append(f"# LoCoMo 中文子集评估报告：{summary.get('run_id')}")
    lines.append("")
    dataset = summary.get("dataset", {})
    validation = summary.get("dataset_validation", {})
    lines.append("## 运行信息")
    lines.append("")
    lines.append(f"- 数据集：`{dataset.get('path')}`")
    lines.append(f"- 数据集 SHA256：`{dataset.get('sha256')}`")
    lines.append(f"- 样本：{validation.get('conversations')} 个会话，{validation.get('sessions')} 个 session，"
                 f"{validation.get('turns')} 轮对话，{validation.get('qa')} 条 QA")
    identities = summary.get("identities", {})
    if identities:
        lines.append(f"- 抽取/回答模型：`{identities.get('llm_model')}`")
        lines.append(f"- embedding 模型：`{identities.get('embedding_model')}`")
        lines.append(f"- dMem 二进制提交：`{identities.get('daemon_commit')}`")
    lines.append("")

    build = summary.get("build", {})
    if build:
        lines.append("## 阶段一：记忆构建")
        lines.append("")
        lines.append(f"- 抽取 session：{build.get('sessions_extracted')}/{build.get('sessions_total')}"
                     f"（失败 {build.get('sessions_failed')}）")
        lines.append(f"- 记忆候选：{build.get('memories_extracted')} 条，commit {build.get('commits')} 次")
        lines.append(f"- 可召回权威记忆：{build.get('authority_memories')} 条")
        lines.append(f"- 提交状态分布：`{build.get('status_counts')}`")
        lines.append("")

    metrics = (summary.get("retrieval") or {}).get("metrics", {})
    if metrics:
        lines.append("## 阶段二：检索评估")
        lines.append("")
        lines.append(f"- 参与评测的查询：{metrics.get('usable_queries')}（跳过无证据 {metrics.get('skipped_queries')}）")
        degraded = (summary.get("retrieval") or {}).get("degradation_counts") or {}
        lines.append(f"- 降级统计：`{degraded}`")
        lines.append("")
        lines.append("| cutoff | hit | all | turn coverage | MRR | nDCG |")
        lines.append("| --- | --- | --- | --- | --- | --- |")
        for cutoff, values in (metrics.get("overall") or {}).items():
            lines.append(
                f"| {cutoff} | {_pct(values.get('hit'))} | {_pct(values.get('all'))} | "
                f"{_pct(values.get('coverage'))} | {_pct(values.get('mrr'))} | {_pct(values.get('ndcg'))} |"
            )
        lines.append("")
        by_category = metrics.get("by_category") or {}
        if by_category:
            lines.append("### 分问题类型（@10）")
            lines.append("")
            lines.append("| category | hit | all | turn coverage | MRR | nDCG |")
            lines.append("| --- | --- | --- | --- | --- | --- |")
            for category, buckets in by_category.items():
                values = buckets.get("@10", {})
                lines.append(
                    f"| {category} | {_pct(values.get('hit'))} | {_pct(values.get('all'))} | "
                    f"{_pct(values.get('coverage'))} | {_pct(values.get('mrr'))} | {_pct(values.get('ndcg'))} |"
                )
            lines.append("")

    ablation = summary.get("retrieval_ablation") or {}
    ablation_modes = ablation.get("modes") or []
    if ablation_modes:
        lines.append("## 阶段二·附：检索通道消融")
        lines.append("")
        lines.append("| 模式 | 实际执行通道 | 预期禁用 | 意外降级 | @10 hit | @10 coverage | @10 MRR | @10 nDCG |")
        lines.append("| --- | --- | --- | --- | --- | --- | --- | --- |")
        for row in ablation_modes:
            values = ((row.get("metrics") or {}).get("overall") or {}).get("@10") or {}
            lines.append(
                f"| {row['name']} | {', '.join(row['channels_executed']) or '-'} | "
                f"{', '.join(row.get('disabled_channels') or []) or '-'} | "
                f"`{row.get('unexpected_degradation_counts') or {}}` | {_pct(values.get('hit'))} | "
                f"{_pct(values.get('coverage'))} | {_pct(values.get('mrr'))} | "
                f"{_pct(values.get('ndcg'))} |"
            )
        lines.append("")
        effects = ablation.get("channel_effects") or {}
        if effects:
            lines.append("通道增益（@10，相对差异）：")
            lines.append("")
            for label, effect in effects.items():
                delta = ((effect.get("delta") or {}).get("@10")) or {}
                lines.append(
                    f"- `{label}`（{effect['after']} − {effect['before']}）："
                    f"Δhit={_signed_pct(delta.get('hit'))}，"
                    f"Δcoverage={_signed_pct(delta.get('coverage'))}，"
                    f"ΔMRR={_signed_pct(delta.get('mrr'))}，"
                    f"ΔnDCG={_signed_pct(delta.get('ndcg'))}"
                )
            lines.append("")
        lines.append("完整结果见 `retrieval_ablation.md` 与 `retrieval_ablation.json`。")
        lines.append("")

    qa = summary.get("qa", {})
    accuracy = (qa or {}).get("accuracy") or {}
    if accuracy:
        lines.append("## 阶段三：端到端 QA")
        lines.append("")
        lines.append(f"- 题目数：{accuracy.get('count')}（失败 {qa.get('failed_questions')}）")
        lines.append(f"- 总体准确率：**{_pct(accuracy.get('overall'))}**")
        lines.append("")
        by_category = accuracy.get("by_category") or {}
        if by_category:
            lines.append("| category | accuracy | count |")
            lines.append("| --- | --- | --- |")
            for category, values in by_category.items():
                lines.append(f"| {category} | {_pct(values.get('accuracy'))} | {values.get('count')} |")
            lines.append("")

    if judgements:
        wrong = [item for item in judgements if not item.get("label")]
        lines.append("## 错误样例")
        lines.append("")
        lines.append(f"共 {len(wrong)} 条错误（最多展示 15 条）。")
        lines.append("")
        for item in wrong[:15]:
            lines.append(f"- `{item.get('qa_id')}`（category {item.get('category')}）：{item.get('reason')}")
        lines.append("")

    return "\n".join(lines)


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
        return f"{float(value) * 100:+.2f}pp"
    except (TypeError, ValueError):
        return str(value)
