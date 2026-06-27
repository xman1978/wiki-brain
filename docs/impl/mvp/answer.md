# Answer 实现路径

## 职责

Answer 接收 Retrieval 产出的 EvidenceSet，根据充分性判断结果选择短路径或深路径生成回答，强制要求所有引用只能来自 EvidenceSet 中已存在的 fact_id，输出带有可追溯 citation 的 AnswerResult，供 Trace 模块记录。

MVP 阶段 Answer 只实现 Evidence Answer：基于已检索证据直接生成回答，不实现 Working Model、冲突检测或复杂推理路径。

## 核心组件

```text
路径分发器（根据 EvidenceSet.path 和证据是否为空，分发到短路径 / 深路径 / 兜底）
短路径生成器（simple 问题，直接+补充证据完整输入，直接生成答案）
深路径生成器（复杂问题，结构化组织全部证据，让模型推理生成答案）
无证据兜底处理器（直接返回固定文本，不调用 LLM）
Citation 合法性校验器（过滤幻构 fact_id）
回答存储（SQLite answers 表）
HTTP API
```

## 数据结构

### answers 表

```sql
CREATE TABLE answers (
    answer_id         TEXT PRIMARY KEY,
    question          TEXT NOT NULL,
    content           TEXT NOT NULL,
    -- 回答正文
    citations         TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组，存储引用的 fact_id 列表
    evidence_snapshot TEXT NOT NULL DEFAULT '{}',
    -- 完整 EvidenceSet 的 JSON 快照，保留 fact_id → unit_id / point_id / source_ref 映射
    -- 使 citations 中每个 fact_id 在请求结束后仍可追溯到来源位置
    has_answer        INTEGER NOT NULL DEFAULT 1,
    -- 1=有答案，0=无证据返回固定文本或生成失败
    path              TEXT NOT NULL,
    -- short / deep / none / error
    prompt_version    TEXT NOT NULL,
    model_name        TEXT NOT NULL,
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### AnswerResult（与 Trace 的输出契约）

```text
AnswerResult：
  answer_id      已写入 SQLite 的回答 ID
  question       原始问题
  content        回答正文
  citations[]    引用的 fact_id 列表（均在当前 EvidenceSet 中）
  has_answer     bool
  path           short / deep / none / error
  evidence_set   原样传递的完整 EvidenceSet（供 Trace 记录）
```

`citations` 中每个 fact_id 通过 `evidence_snapshot`（持久化的 EvidenceSet JSON）反查 unit_id / point_id / source_ref，保证请求结束后仍可追溯到具体 KnowledgeUnit 和来源位置。

**关于 EvidenceSet.supporting 的内容**：supporting[] 包含两类证据：Rerank 分类为"支持性"的 KU，以及 KPN 扩展步骤补充的邻居 KU（变量、边界、前提等上下文）。Answer 层不区分这两类，统一作为补充证据处理。

## 实现步骤

### 步骤 1：定义 AnswerResult 并实现路径分发

Answer 模块对外提供两个入口：

```text
HTTP 接口：POST /answer，接收 question，完整执行 Retrieval → Answer 链路；
内部接口：Answer(evidenceSet EvidenceSet) AnswerResult，供测试直接注入 EvidenceSet。
```

HTTP 请求中的 `deep` 字段表示"优先使用深路径"：

```text
deep=false 或未传：按 retrieval.md 步骤 9 的默认 path 分发；
deep=true：只要 direct_evidence 或 supporting 非空，强制使用 deep 路径；
direct_evidence 和 supporting 均为空时，无论 deep 取值如何都进入 none 路径。
```

路径分发逻辑：

```text
path == "short"（direct_evidence 非空）        → 步骤 2（短路径）
path == "deep" 且 direct 或 supporting 非空    → 步骤 3（深路径）
direct 和 supporting 均为空（path == "deep"）  → 步骤 4（无证据兜底）
```

说明：Retrieval 充分性判断规则——direct_evidence 非空则 path="short"，否则 path="deep"。
因此 short 路径时 direct_evidence 必然非空；默认 deep 路径时 direct_evidence 为空，supporting 可能非空或为空；
deep=true 覆盖 path 时，deep 路径也可能包含 direct_evidence。direct 和 supporting 均为空时进入无证据兜底，不调 LLM。

### 步骤 2：短路径生成

问题简单直接，direct_evidence 已充分，supporting 提供完整上下文。两类证据均传入 LLM，生成简洁完整的答案。

**输入构建**：

```text
{{question}}：原始问题；
{{direct_evidence_list}}：direct_evidence[] 列表，每条格式：[fact_id] content；
{{supporting_evidence_list}}：supporting[] 列表，每条格式：[fact_id] content；
  supporting 为空时传空字符串 ""，prompt 中该段显示为空，LLM 按无补充证据处理。
```

**Prompt 文件**：`config/prompts/answer_short.md`

```
根据以下证据回答问题。只使用提供的证据，不添加证据中没有的信息。若证据不足以回答，如实说明缺少什么。

问题：{{question}}

直接证据：
{{direct_evidence_list}}

补充证据：
{{supporting_evidence_list}}

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

注入 prompt 的 `{{json_schema}}` 是示例 JSON，不是 JSON Schema DSL：

```json
{
  "content": "回答正文",
  "citations": ["fact_id_1", "fact_id_2"]
}
```

程序将模型输出解析整合（将 citations 中的 fact_id 映射回完整 EvidenceItem、拼装 answer_text 和 references 列表）后，用 `answer_short.md` 内 `## Schema` 段校验整合结果，检查 `content` 非空、`citations` 为字符串数组。

**调用参数**：

```text
模型：default 模型
temperature：0
记录：prompt_version、model_name、token 用量
```

### 步骤 3：深路径生成

问题复杂，需要多变量或多步骤推理。将全部证据按结构提供给 LLM，要求模型识别关键变量、梳理推理路径，生成有依据的结论。

**输入构建**：与短路径相同（direct + supporting 均传入）。

**Prompt 文件**：`config/prompts/answer_deep.md`

```
基于以下证据对问题进行推理并给出答案。直接证据是主要依据，补充证据提供背景和关联上下文。

要求：
1. 识别回答问题所需的关键变量
2. 梳理推理路径，说明各步骤依据哪条证据
3. 给出当前证据支持下的最优结论
4. 若证据存在矛盾或缺口，明确指出

问题：{{question}}

直接证据：
{{direct_evidence_list}}

补充证据（含上下文关联）：
{{supporting_evidence_list}}

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

使用与短路径相同的输出结构，用 `answer_deep.md` 内 `## Schema` 段校验整合结果，不依赖独立 schema 文件。

### 步骤 4：无证据兜底

direct_evidence 和 supporting 均为空，程序直接构造结果，不调用 LLM：

```text
has_answer = false
content    = "知识库中暂无相关材料，无法回答该问题。"
citations  = []
path       = "none"
```

此情况仍将 EvidenceSet 完整传递给 Trace。knowledge gap 是有价值的学习信号，供 Study 判断是否触发 Learning Event。

### 步骤 5：Citation 合法性校验

LLM 输出的 citations 中可能包含幻构的 fact_id，校验逻辑：

```text
取 EvidenceSet 中所有 fact_id 的集合（direct_evidence + supporting）；
遍历 LLM 输出的 citations，过滤掉不在集合中的 fact_id；
过滤后 citations 为空但 content 非空：
  保留 content，citations = []，记录 warn 日志（含 question、幻构的 fact_id）；
过滤后 citations 非空：正常写入。
```

### 步骤 6：LLM 调用失败处理

步骤 2 或步骤 3 调用 LLM 失败（超时、Schema 校验失败）时：

```text
重试一次（相同 prompt 和参数）；
重试仍失败：构造降级结果：
  has_answer = false
  content    = "回答生成失败，请稍后重试。"
  citations  = []
  path       = "error"
记录 error 日志（含 question、path、错误详情）。
```

降级结果写入 answers 表并传递给 Trace，不静默丢弃。

### 步骤 7：写入 answers 表并异步触发 Trace

步骤 2~6 完成后，将结果写入 SQLite，再将 AnswerResult 投入异步队列通知 Trace：

```text
生成 answer_id（UUID）；
将 EvidenceSet 序列化为 JSON 作为 evidence_snapshot；
写入 question、content、citations（JSON 序列化）、evidence_snapshot、
      has_answer、path、prompt_version、model_name；
若请求携带 session_id：
      UPDATE session_turns SET answer_id=? WHERE session_id=? AND answer_id IS NULL
      ORDER BY turn_index DESC LIMIT 1
      （补填本次问答对应的轮次记录，不阻塞主流程，失败只记录 warn 日志）；
写入成功后，将 AnswerResult（含 answer_id + evidence_set）投入 foundation 异步任务队列，
      由 Trace 模块的消费者异步处理，不阻塞当前请求返回；
组装 AnswerResult 返回给 HTTP 调用方（不等待 Trace 完成）。
```

**Trace 入队失败处理**：

```text
入队失败（队列已满）：记录 error 日志（含 answer_id），不重试，不影响 Answer 返回；
Trace 消费失败：由 Trace 模块自身处理，Answer 不感知。
```

### 步骤 8：暴露 HTTP API 和内部接口

```text
POST   /answer
  请求：{ "question": "...", "deep": false, "session_id": "..." }
  session_id 可选；携带时写库后补填对应 session_turns.answer_id
  流程：调用 Retrieval 内部接口 → 获得 EvidenceSet → 生成 AnswerResult → 写库 → 返回
        deep=true 时按步骤 1 规则覆盖 EvidenceSet.path
  响应：{
    "answer_id": "...",
    "question": "...",
    "content": "...",
    "citations": ["fact_id_1", "fact_id_2"],
    "has_answer": true,
    "path": "short|deep|none|error",
    "evidence_snapshot": { ... }
  }
  说明：Trace 异步写入，POST /answer 不保证立即返回 trace_id；
        调用方可用 GET /traces?answer_id=... 查询 Trace 是否已生成。

GET    /answers/:id
  响应：与 POST 响应格式相同，补充 created_at 字段；
        包含 evidence_snapshot（完整 EvidenceSet JSON），供调用方按 fact_id 反查来源
  用途：Trace 回查、调试追溯
```

内部接口供测试直接注入 EvidenceSet：

```text
Answer(evidenceSet EvidenceSet) → AnswerResult
```

## 依赖

```text
基础设施：SQLite、LLM client、结构化日志、HTTP 框架
Retrieval：依赖 EvidenceSet（含 path、direct_evidence[]、supporting[]、每条证据的 fact_id / content / source_ref）
           supporting[] 包含 Rerank 分类的支持性证据和 KPN 扩展的邻居 KU，Answer 层统一消费
Trace：AnswerResult 是 Trace 的输入契约，Trace 通过内部调用获取并持久化
```

## 完成标准

```text
路径分发正确：默认 short 仅在 direct_evidence 非空时触发，deep 在 direct 为空且 supporting 非空时触发；
deep=true 时有任意证据即强制 deep；none 仅在 direct+supporting 均为空时触发；
短路径和深路径均将 direct+supporting 传入 LLM，用 prompt 文件 `## Schema` 段校验整合结果；
深路径 prompt 要求模型识别变量、梳理推理路径、指出证据缺口；
Citation 校验能过滤幻构 fact_id，过滤结果记录 warn 日志；
无证据时程序直接返回固定文本，不调用 LLM；
LLM 失败重试一次，重试仍失败返回 path=error 的降级结果；
回答写入 answers 表（含 evidence_snapshot），POST /answer 和 GET /answers/:id 均返回 evidence_snapshot；
citations 中每个 fact_id 可经 evidence_snapshot 反查 unit_id / point_id / source_ref；
AnswerResult 写库成功后异步投入队列通知 Trace，投入失败记录 error 日志不阻塞返回；
fake LLM client 下，四条路径（short / deep / none / error）测试均可稳定运行。
```
