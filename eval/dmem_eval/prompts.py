"""Prompt templates for memory extraction, answering and judging."""

from __future__ import annotations

EXTRACTION_SYSTEM = """你是一个记忆抽取器。阅读一段中文多人对话，抽取出值得长期记住的原子记忆。

只输出严格 JSON，不要输出 markdown 代码块或任何解释。输出格式：
{"memories":[{"kind":"fact|event|preference|plan|procedure","content":"...","search_hints":["..."],"sensitivity":"normal|sensitive","confidence":0.9,"dia_ids":["D1:3"]}]}

抽取规则：
1. content 必须使用中文，自包含：写清人物全名、绝对时间（把“昨天”“上周”等换算成对话日期对应的日期）和关键地点；一句话只表达一个事实。
2. kind 取值：fact（事实、身份、属性、关系）、event（发生过的具体事件）、preference（喜好/厌恶）、plan（计划/打算）、procedure（步骤/流程）。
3. dia_ids 必须是给定对话中出现过的 [Dx:y] 编号，且是这条记忆的直接依据，至少一个，可以多个。
4. search_hints 提供 1-8 个中文关键词或短语，帮助后续检索这条记忆，不要重复 content 全文。
5. sensitivity 默认 normal；涉及健康、身份认同、财务等敏感个人信息时用 sensitive。
6. confidence 是 0 到 1 之间的数字，表示你对该记忆正确性的把握。
7. 不要编造对话中没有的信息，不要把多个事实合并成一条。如果没有值得记住的内容，返回 {"memories":[]}。
8. 每个会话最多抽取 24 条最有价值、最可能有用的记忆，优先保留具体的人名、时间、地点、偏好、计划和事件。"""


def extraction_user(sample_id: str, speaker_a: str, speaker_b: str, session_key: str,
                    date_time: str, transcript: str) -> str:
    return (
        f"对话编号：{sample_id}\n"
        f"说话人：{speaker_a}、{speaker_b}\n"
        f"会话：{session_key}，时间：{date_time}\n\n"
        f"对话内容：\n{transcript}\n\n"
        "请抽取记忆并按要求输出 JSON。"
    )


ANSWER_SYSTEM = """你是记忆问答助手。请只依据【记忆】回答【问题】。
【记忆】是从两人聊天中抽取的事实。

规则：
1. 先找出与问题最相关的记忆，再据此直接作答。
2. 只要【记忆】中存在与问题相关的信息，就必须作答，即使信息不完整也不要拒绝回答。
3. 只有当所有【记忆】都与问题完全无关时，才回答“根据已知信息无法回答该问题。”。
4. 回答要具体、简洁，只输出答案本身，通常一句话，不要解释推理过程。

示例：
【记忆】
- 小明通过跑步来减压和理清思绪。
【问题】小明为什么跑步？
回答：为了减压和理清思绪。"""


def answer_user(context: str, question: str) -> str:
    return f"【记忆】\n{context}\n\n【问题】\n{question}\n\n请给出答案。"


JUDGE_SYSTEM = """你是一个严格但公正的评分员。给定问题、参考答案和模型回答，判断模型回答是否与参考答案语义一致。

判定规则：
1. 允许措辞不同、语言不同、顺序不同；只要关键信息一致就算正确。
2. 时间、数量、人物、地点等关键信息缺失或错误，算错误。
3. 模型回答包含正确答案但也包含错误信息时，算错误。
4. 参考答案为空或表示无法回答时，模型回答表示“无法回答/没有相关信息/不确定”才算正确。

只输出严格 JSON：{"correct": true, "reason": "简短理由"}"""


def judge_user(question: str, gold: str, hypothesis: str) -> str:
    return (
        f"问题：{question}\n"
        f"参考答案：{gold}\n"
        f"模型回答：{hypothesis}\n\n"
        "请判断模型回答是否正确，并输出 JSON。"
    )


ADVERSARIAL_JUDGE_SYSTEM = """你是一个严格但公正的评分员。这个问题在给定对话中其实没有答案，下面提供的“错误答案”是看似合理但实际错误的答案。

判定规则：
1. 如果模型回答表示“无法回答/没有相关信息/不确定/对话中未提及”，判为 correct=true。
2. 如果模型回答给出了“错误答案”或与其实质相同的断言，判为 correct=false。
3. 模型回答含糊其辞但没有明确断言错误答案时，判为 correct=false。

只输出严格 JSON：{"correct": true, "reason": "简短理由"}"""


def adversarial_judge_user(question: str, adversarial_answer: str, hypothesis: str) -> str:
    return (
        f"问题：{question}\n"
        f"错误答案：{adversarial_answer}\n"
        f"模型回答：{hypothesis}\n\n"
        "请判断模型是否正确识别出该问题无法回答，并输出 JSON。"
    )
