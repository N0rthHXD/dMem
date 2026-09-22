# LoCoMo 中文子集评估模块

针对 dMem 的中文记忆评估工具，使用 LoCoMo 中文评估子集
（`locomo10.zh-subset.json`），通过 dMem 的 HTTP 接口完成
**记忆构建 → 检索评估 → 端到端 QA 评估** 三个阶段。

- Python 实现，复用 `openai` / `requests` / `PyYAML` 等三方库；
- 只调用 dMem 的 HTTP 接口（`/v1/scopes/ensure`、`/v1/memory-commits`、`/v1/recall`）；
- LLM 与 embedding 全部在评估配置文件中配置；
- 命令行参数只有 `--config`、`--phases`、`--run-id`。

## 1. 快速开始

```sh
# 1) 构建当前提交的 dMem 二进制（仓库根目录）
CGO_ENABLED=1 go build -tags sqlite_fts5 -o dmemd ./cmd/dmemd

# 2) 导出 LLM / embedding 的 API Key（配置里用 api_key_env 引用变量名）
export DMEM_PROVIDER_KEY=...

# 3) 运行评估（使用 agent 环境）
/home/ut005426@uos/miniforge3/envs/agent/bin/python eval/run_eval.py \
    --config eval/config.zh-subset.yaml
```

产物写入 `eval/results/<run_id>/`。默认 `run_id = zh-subset`。

只跑部分阶段（复用同一 `run_id` 的已有产物与 daemon 数据目录）：

```sh
python eval/run_eval.py --config eval/config.zh-subset.yaml --phases retrieval,qa
python eval/run_eval.py --config eval/config.zh-subset.yaml --phases qa --run-id zh-subset

# 检索通道消融：复用已构建索引，只重启 daemon 切换通道配置
python eval/run_eval.py --config eval/config.zh-subset.yaml --phases ablation
```

## 2. 四个阶段

| 阶段 | 做什么 | 主要产物 |
| --- | --- | --- |
| `build` | 用配置的 LLM 逐 session 抽取结构化记忆（含 `dia_ids` 证据），通过 `POST /v1/memory-commits`（Agent Tool 路线）写入；随后等待 embedding 索引就绪 | `build_memories.jsonl`、`build_commits.jsonl`、`build_summary.json`、`scopes.json` |
| `ablation` | 对每个通道消融模式重启 daemon（切换 `graph_policy` / embedding provider），各跑一遍 `recall` 并计算检索指标，最后汇总通道增益 | `ablation/<mode>/retrieval.jsonl`、`ablation/<mode>/retrieval_summary.json`、`retrieval_ablation.json`、`retrieval_ablation.md` |
| `retrieval` | 对每条 QA 用 `POST /v1/recall` 召回，把记忆映射回 `dia_id`，计算检索指标 | `retrieval.jsonl`、`retrieval_summary.json` |
| `qa` | 用召回上下文让 LLM 作答，再用 LLM 评审（adversarial 题单独判定） | `qa.jsonl`、`judge.jsonl`、`qa_summary.json` |

最后汇总为 `summary.json` 与 `report.md`。

## 3. 配置说明（`config.zh-subset.yaml`）

所有设置都在 YAML 中，支持 `${ENV_VAR}` / `${ENV_VAR:-default}` 插值，相对路径以配置文件目录为基准。

```yaml
run_id: zh-subset          # 运行标识，同时是输出目录名
output_dir: results        # 相对 eval/ 的产物根目录
phases: [build, retrieval, qa]

dataset:
  path: /home/ut005426@uos/data/locomo/zh-subset/locomo10.zh-subset.json
  sample_ids: []           # 空 = 全部会话
  categories: []           # 空 = 全部问题类型

dmem:
  manage_daemon: true      # 评估自管一个独立 daemon（独立 data_dir，不碰现有实例）
  binary: ../dmemd         # dmemd 二进制
  listen_address: 127.0.0.1:17373
  data_dir: ""             # 空 = <output_dir>/<run_id>/daemon/data
  worker_count: 8
  indexing_timeout_seconds: 1800
  embedding:               # 语义召回所需的 embedding provider
    enabled: true
    endpoint: https://ark.cn-beijing.volces.com/api/v3/embeddings/multimodal
    model: doubao-embedding-vision-251215
    api_key_env: DMEM_PROVIDER_KEY
    dimensions: 1024
    query_instructions: "Target_modality: text.\nInstruction:Retrieve semantically similar text\nQuery:"

llm:                       # 记忆抽取、答案生成、评审共用
  base_url: https://ark.cn-beijing.volces.com/api/v3
  model: deepseek-v4-flash-ga-260731
  api_key_env: DMEM_PROVIDER_KEY
  temperature: 0.0
  max_tokens: 8192
  max_concurrent: 8

build:
  max_memories_per_commit: 24
  include_image_captions: true

retrieval:
  limit: 30
  cutoffs: [1, 3, 5, 10, 30]
  intent_by_category: {1: multi_hop, 2: temporal, 3: auto, 4: lookup, 5: verification}
  workers: 8
  ablation:                 # --phases ablation 使用的通道模式
    modes:
      - {name: hybrid, graph_policy: features_only, embedding: true}
      - {name: graph-off, graph_policy: "off", embedding: true}
      - {name: semantic-off, graph_policy: features_only, embedding: false}
      - {name: keyword-only, graph_policy: "off", embedding: false}

qa:
  answer_cutoff: 10
  max_context_chars: 12000
  workers: 8
  judge: true
```

要点：

- **`manage_daemon: true`**：评估模块根据配置生成 `daemon/config.toml`，执行
  `dmemd init`、启动 `dmemd daemon`，等待就绪，跑完自动关闭。`data_dir` 是评估专用的，
  不会影响 `127.0.0.1:7373` 上可能正在运行的用户实例。
- **embedding provider 的 egress 策略**：Agent Tool 提交的原始事件 `source_type` 是
  `tool_call`，配置会生成允许 `tool_call` 的 source type 白名单。
- **scope 层级**：dMem 要求 `project` 挂在 `user` 之下，且全局只允许一个 user 根。
  评估自动创建根 user scope 与每个会话一个 project scope；召回会解析祖先链，
  因此 embedding 策略同时允许 `project` 与 `user` 两种 scope 类型。
- **索引就绪**：构建阶段结束后，评估以只读方式轮询 daemon 的 SQLite，直到
  `embed_memory` / `rebuild_semantic` 作业清空且投影全部为 `current`，保证语义召回可用。

### 检索通道消融

dMem 有 `keyword`、`semantic`、`graph` 三个独立召回通道，但 Agent Tool 的
`POST /v1/recall` 契约**不暴露通道选择**（只接受 `intent` / `entities` / `limit` /
`cursor`）。因此本模块不改动核心契约，而是让每个消融模式使用一份不同的 daemon 配置：

- `graph_policy: off` 关闭图谱通道（`features_only` 保留图谱特征但不新增候选）；
- `embedding: false` 不配置 embedding provider，`semantic` 通道被显式报告为降级，
  `keyword` 始终可用；
- 每个模式复用 `build` 阶段同一份 `data_dir` 与索引，评估只重启 daemon；
- 模式顺序建议先 `embedding: true` 再 `embedding: false`：关闭 embedding 后再重新开启会
  触发整库重嵌入（daemon 每次启动都会开启新的 activation generation）。

默认四个模式覆盖 hybrid（k+s+g）、graph-off（k+s）、semantic-off（k+g）、
keyword-only（k）。`--phases ablation` 只做检索，不跑 QA。注意：`ablation` 阶段要求
`dmem.manage_daemon: true`（需要评估自己控制 daemon 生命周期）。

报告还会读取 daemon SQLite 中 `entities` / `entity_facts` / `memory_relations` 的活跃计数。
当前 Agent Tool 提交路线**不产生** entity / fact / relation，因此 graph 索引虽然
`ready`、graph 通道也会执行，但没有可加入的候选：`semantic-off` 与 `keyword-only`
的指标完全相同就说明 graph 的边际贡献为 0。报告中 `graph` 的差异应视为噪声，而非真实增益。

## 4. 产物与指标

```
eval/results/<run_id>/
├── manifest.json            # 数据集 SHA256、模型身份、dMem 提交、阶段与耗时
├── config.snapshot.json     # 本次运行的实际配置
├── scopes.json              # sample_id -> project scope_id
├── build_memories.jsonl     # 每条抽取记忆：kind/content/dia_ids/提交状态/memory_id
├── build_commits.jsonl      # 每次 commit：source_event_id/dia_ids
├── retrieval.jsonl          # 每条 QA 的召回列表（含映射后的 dia_ids）
├── qa.jsonl                 # 问题、参考答案、模型回答
├── judge.jsonl              # 评审标签与理由
├── ablation/<mode>/         # 每个消融模式：retrieval.jsonl、retrieval_summary.json、daemon/
├── retrieval_ablation.json  # 各模式指标、相对基线差异、通道增益
├── retrieval_ablation.md    # 通道消融可读报告
├── summary.json             # 各阶段汇总指标
└── report.md                # 人类可读报告
```

检索指标对每个 cutoff 同时给出：

- `hit`：top-k 中至少命中一个 gold `dia_id` 的查询比例；
- `all`：top-k 覆盖全部 gold `dia_id` 的查询比例；
- `coverage`：gold `dia_id` 的平均覆盖率（turn-level recall）；
- `mrr` / `ndcg`：首个相关记忆的排名与归一化折损累计增益。

QA 指标为 LLM 评审准确率（总体与按 category）。LoCoMo category 5 为对抗题，
参考答案为空且带 `adversarial_answer`，只有模型明确表示“无法回答”才判正确。

## 5. 为什么用 Agent Tool 路线

dMem 的 Provider Route（`POST /v1/events`）只把抽取结果写成 **pending candidate**，
必须经 `ConfirmCandidate` 审核才成为可召回的权威记忆，而当前 HTTP 契约没有候选审核
入口（README 亦说明“审核与冲突解决当前没有协议入口”）。因此纯 HTTP 的 Provider Route
跑完 `recall` 会为空。Agent Tool 路线对 normal、非聚合、非重复的记忆直接创建权威记忆，
且 evidence 指向本次 commit 的原始事件，记忆与 `dia_id` 的映射精确可控，是纯 HTTP 闭环
下唯一可行的记忆构建方式。

## 6. 代码结构

```
eval/
├── run_eval.py                 # CLI 入口（--config / --phases / --run-id）
├── config.zh-subset.yaml       # 示例配置
└── dmem_eval/
    ├── config.py               # YAML 加载、env 插值、校验、消融模式解析
    ├── dataset.py              # LoCoMo 子集加载、日期解析、统计
    ├── dmem_client.py          # dMem HTTP 客户端（身份头、幂等键、重试）
    ├── llm.py                  # OpenAI 兼容 LLM 客户端（并发、重试、JSON）
    ├── daemon.py               # daemon 配置生成与生命周期、消融 overlay、索引就绪轮询
    ├── prompts.py              # 抽取 / 作答 / 评审提示词
    ├── metrics.py              # 检索指标
    ├── phases.py               # build / retrieval / qa 三个阶段
    ├── ablation.py             # 通道消融汇总与报告
    ├── report.py               # summary.json 与 report.md
    └── io_utils.py             # JSON/JSONL 读写
```

## 7. 测试

```sh
/home/ut005426@uos/miniforge3/envs/agent/bin/python -m unittest discover -s eval/tests -v
```

覆盖配置加载、数据集解析、daemon 配置生成、记忆归一化、检索指标与上下文预算。

## 8. 依赖

- Python 3.10+（已在 `/home/ut005426@uos/miniforge3/envs/agent` 的 Python 3.12 验证）；
- `openai`、`requests`、`PyYAML`、`httpx`（openai 依赖）；
- 仓库根目录可执行的 `dmemd`（`CGO_ENABLED=1 go build -tags sqlite_fts5 -o dmemd ./cmd/dmemd`）。
