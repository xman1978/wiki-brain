# Unit 实现路径

## 职责

Unit 从规范化 Markdown 中提取 KnowledgeUnit 和 KnowledgePoint，并生成 KnowledgePoint 之间的轻量语义关系网络（KPN），构成系统长期记忆的材料侧基础，写入 SQLite 和 Bleve 索引供后续检索使用。

## 核心组件

```text
Outline-segment 切块器（按 source_outlines 划定提取范围）
LLM 联合提取调用（一次调用产出 KnowledgeUnit[] + KnowledgePoint[]）
JSON Schema 校验器（校验 LLM 结构化输出）
单元级重试处理器（失败单元局部重试，不重跑整份材料）
KPN 关系生成器（提取完成后对全 Source KP 做关系分析）
KnowledgeUnit / KnowledgePoint / KPN 存储（SQLite）
Bleve units / points 索引写入
HTTP API
```

## 数据结构

### knowledge_units 表

```sql
CREATE TABLE knowledge_units (
    unit_id        TEXT PRIMARY KEY,
    source_id      TEXT NOT NULL REFERENCES sources(source_id),
    outline_id     TEXT REFERENCES source_outlines(outline_id),
    concept_id     TEXT REFERENCES concepts(concept_id),
    -- 匹配到的知识概念（可为空，批量匹配后写入）
    center         TEXT NOT NULL,
    -- 'center' 是该单元的核心主题或判断，10~40 字，供检索和展示使用
    line_start     INTEGER NOT NULL,
    -- 单元内容在规范化 Markdown 中的起始行号（1-based, inclusive，绝对行号，见 foundation.md 行号约定）
    line_end       INTEGER NOT NULL,
    -- 单元内容在规范化 Markdown 中的结束行号（1-based, inclusive，绝对行号）
    status         TEXT NOT NULL DEFAULT 'pending',
    -- pending / completed / extraction_failed
    error_msg      TEXT,
    prompt_version TEXT NOT NULL,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_knowledge_units_source_id ON knowledge_units(source_id);
CREATE INDEX idx_knowledge_units_outline_id ON knowledge_units(outline_id);
```

### knowledge_points 表

```sql
CREATE TABLE knowledge_points (
    point_id       TEXT PRIMARY KEY,
    unit_id        TEXT NOT NULL REFERENCES knowledge_units(unit_id),
    source_id      TEXT NOT NULL REFERENCES sources(source_id),
    content        TEXT NOT NULL,
    -- 可激活摘要，20~80 字，可在不读完整段落的情况下独立理解该知识面的核心主张
    point_type     TEXT NOT NULL,
    -- definition / rule / method / case / question（共 5 种，与 KP 提取 Prompt 枚举对齐）
    -- definition：定义或概念解释
    -- rule：判断、原则、约束
    -- method：方法、流程、步骤
    -- case：案例、经验
    -- question：悬而未决的问题
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_knowledge_points_unit_id ON knowledge_points(unit_id);
CREATE INDEX idx_knowledge_points_source_id ON knowledge_points(source_id);
```

### knowledge_point_relations 表（KPN）

```sql
CREATE TABLE knowledge_point_relations (
    relation_id      TEXT PRIMARY KEY,
    source_point_id  TEXT NOT NULL REFERENCES knowledge_points(point_id),
    target_point_id  TEXT NOT NULL REFERENCES knowledge_points(point_id),
    relation_type    TEXT NOT NULL,
    -- related / hierarchical / depends / supplements / contradicts
    -- MVP 阶段只保留 KPN Prompt 实际生成的 5 种类型
    direction        TEXT NOT NULL DEFAULT 'directed',
    -- directed（有向）/ bidirectional（双向）
    -- related / contradicts → bidirectional；hierarchical / depends / supplements → directed
    prompt_version   TEXT NOT NULL,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_kp_relations_source ON knowledge_point_relations(source_point_id);
CREATE INDEX idx_kp_relations_target ON knowledge_point_relations(target_point_id);
```

## 任务执行模型

Unit 提取任务以 **source 为粒度**，一个 source 对应一条队列任务，由单个 goroutine 顺序执行全部步骤：

```text
队列任务载荷：{ task_type: "unit_extract", source_id: "..." }

消费者执行顺序（顺序执行，单 goroutine，不并发）：
  Step 1  切块（所有分段在内存中确定）
  Step 2  遍历分段，逐段顺序调用 LLM 提取（Step 2 + Step 3 合并在循环内）
  Step 4  全部分段处理完后（无论部分失败），触发 KPN 关系生成
  Step 5  KPN 完成后触发 Concept 批量匹配
```

**选择顺序执行而非并发分段的原因：**
- 消除分段完成计数器和跨 goroutine 协调，实现最简；
- 单 source 的 LLM 调用已受 `llm.max_concurrency` 全局约束，并发分段不会加速；
- 顺序执行保证 Step 4（KPN）在所有分段结束后自然触发，无需额外信号；
- MVP 阶段 source 数量有限，顺序吞吐足够。

**"全部分段处理完"的判断**：循环结束即完成，无需数据库计数器。失败分段已在循环内标记 `extraction_failed` 并记录 `error_msg`，不影响循环继续。

**任务失败隔离**：LLM 调用失败（含重试后）只标记当前分段为 `extraction_failed`，不中止整个任务，后续分段继续处理。仅当切块本身（Step 1）或数据库读取失败时，整体任务终止并记录 error 日志。

---

## 实现步骤

### 步骤 1：Outline-segment 切块

Unit 提取按 outline 节点划定范围，每个范围作为一次 LLM 联合提取调用的输入：

```text
从 source_outlines 取当前 source 的全部节点（按 line_start 升序）；
取叶子节点（无子节点的节点）作为基础切块单位；
  - 叶子节点行范围内容字符数（rune）≤ segment_max_chars（默认 4000）：直接作为一段；
  - 叶子节点内容 > segment_max_chars 字符（rune）：按 Markdown 元素边界切行，每段字符数不超过 segment_max_chars，
    不可分割元素（表格、代码块等）整体保留，允许略超限；
  - 相邻叶子节点内容合计字符数（rune）< source.min_segment_chars（默认 400）：合并为一段；
每段记录：outline_id（或 null）、绝对 line_start、绝对 line_end。

segment_max_chars 与 source.md 中语义目录生成的 segment_max_chars 为同一配置项。
source.md 的语义目录生成保证叶节点内容 ≤ segment_max_chars，此处切块仅作语义目录生成失败时的安全兜底，正常情况下不触发。
```

切块策略影响提取粒度，通过评估集验证，不在运行时动态调整。

### 步骤 2：LLM 联合提取调用

#### 2.1 提取 Prompt

Prompt 文件：`config/prompts/unit_extract.md`

```
分析以下文本，识别其中可独立引用的知识面，每个知识面生成一个知识单元（unit）和对应知识点（points）。

知识单元是围绕一个稳定主题形成的最小完整知识包。过细则合并，过粗则拆分；过渡文字和格式噪声不生成单元。

每个单元用 unit_id（如 "1"、"2"）标记，points 通过 unit_id 关联对应单元，每个单元 1~3 个知识点。

知识点类型：
- definition（定义/概念）
- rule（判断/原则/约束）
- method（方法/流程）
- case（案例/经验）
- question（问题）

来源目录节点：{{outline_title}}
文本（原文第 {{segment_line_start}} 行到第 {{segment_line_end}} 行，行号从 1 开始）：
{{text_content}}

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

#### 2.2 提取输出格式

units 和 points 平铺为两个独立数组，通过 `unit_id` 关联，避免数组嵌套。`unit_id` 是模型自定义的本地编号（如 "1"、"2"），由程序在写库时替换为系统生成的 UUID；`point_id` 同理，由程序写库时生成。

注入 prompt 的 `{{json_schema}}` 是示例 JSON，不是 JSON Schema DSL：

```json
{
  "units": [
    {"unit_id": "1", "center": "知识单元主题", "line_start": 1, "line_end": 8}
  ],
  "points": [
    {"point_id": "1", "unit_id": "1", "content": "可激活摘要内容", "type": "definition|rule|method|case|question"}
  ]
}
```

程序将模型输出解析并整合后（行号转换、unit_id/point_id 本地编号→UUID、推断归属关系），用 `unit_extract.md` 内 `## Schema` 段的 JSON Schema 校验整合结果，检查：每个 unit 的 `line_start <= line_end`；每个 point 的 `unit_id` 存在于 units 中；每个 unit 至少有 1 个 point；`content` 非空。

#### 2.3 行号转换与字段映射

LLM 输出的 `line_start`/`line_end` 是相对于传入分段首行的**相对行号**（1-based），写入数据库前转换为绝对行号（全局约定见 foundation.md）：

```text
unit.line_start（绝对行号）= segment.line_start - 1 + llm_output.line_start
unit.line_end（绝对行号）  = segment.line_start - 1 + llm_output.line_end

point.point_type       = llm_output.type
point.unit_id          = 系统为对应 unit_id（本地编号）的 unit 分配的 UUID
```

#### 2.4 调用参数

```text
模型：extraction 模型
temperature：0
每次调用记录：source_id、outline_id、prompt_version、模型名、token 用量
```

### 步骤 3：校验和重试

```text
LLM 输出先做 JSON 解析和 JSON Schema 整体校验；

整体 JSON 解析失败或 Schema 校验失败：
  - 对整个 segment 使用 unit_extract_retry.md 重试一次；
  - 重试成功后继续进入逐条 KnowledgeUnit 业务校验；
  - 重试仍失败：记录该 segment 提取失败日志，不写入 KU/KP，继续处理后续 segment。

Schema 校验通过后，逐条校验每个 KnowledgeUnit：
  - line_start <= line_end；
  - center 非空；
  - points 非空且每条 content 非空；
校验通过的单元写入 SQLite，status 设为 completed；
校验失败的单元若仍可从 LLM 输出中定位其本地 unit_id，则带原始文本段单独重试一次，使用重试 Prompt（见步骤 3.1）；
  - 重试成功：status 设为 completed，写入 SQLite；
  - 重试仍失败：写入 extraction_failed 占位 KU（保留 source_id、outline_id、line_start/line_end、error_msg），不写入 KP；
无法定位到具体 unit 的失败项：记录 warn，跳过该项，不阻塞其他单元入库；
不重跑整份材料。
```

#### 3.1 重试 Prompt

Prompt 文件：`config/prompts/unit_extract_retry.md`

```
从以下文本提取知识单元和知识点，严格按 JSON Schema 输出。

要求：
1. 每个 unit 必须有 unit_id（如 "1"）、center（10~40字）、line_start <= line_end（相对行号，1-based）
2. 每个 point 必须有 point_id（如 "1"）和 unit_id，且 unit_id 必须对应一个已存在的 unit unit_id
3. content 不得为空

文本（原文第 {{segment_line_start}} 行到第 {{segment_line_end}} 行，共 {{segment_line_count}} 行）：
{{text_content}}

按以下 JSON Schema 输出：
{{json_schema}}
```

重试使用同一 prompt 的 `## Schema` 段校验，不换版本。

### 步骤 4：KPN 关系生成

KPN 关系在当前 Source 全部 KU/KP 提取完成（无论部分失败）后，对状态为 `completed` 的 KU 下的 KP 统一运行一次关系分析。

#### 4.1 输入准备

```text
查询当前 source_id 下所有 status = completed 的 KU 关联的 KP，取：
  point_id、unit_id（对应 unit.center）、content、point_type

点数 ≤ 60：整个 source 发起一次 KPN 调用；
点数 > 60：按顶层 outline 节点分组，每组独立发起一次 KPN 调用；
  同一顶层节点下所有 KP 为一组，组内点数仍超 60 时硬切为每批 60 个；
  跨组关系不在本次生成，留待后续跨 Source 关系任务处理。
```

#### 4.2 KPN Prompt

Prompt 文件：`config/prompts/kpn_extract.md`

```
分析以下知识点列表，找出知识点之间的语义连接。

关系类型：
- related：主题相关（双向）
- hierarchical：from 是 to 的上位概念或 to 是 from 的细化（有向）
- depends：from 成立需要 to 作为前提（有向）
- supplements：from 为 to 补充细节（有向）
- contradicts：两者存在约束冲突（双向）

原则：
- 只建立有明确依据的关系，不推测
- 同一单元内的知识点不建立关系（unit_id 相同的跳过）
- 关系总数不超过知识点数的 1.5 倍

知识点（格式：point_id TAB unit_center TAB content）：
{{knowledge_points}}

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

#### 4.3 KPN 输出格式

`direction` 由程序从 `type` 推断（`related`/`contradicts` → bidirectional，其余 → directed），不让模型输出。

注入 prompt 的 `{{json_schema}}` 是示例 JSON，不是 JSON Schema DSL：

```json
{
  "relations": [
    {"from": "point_id", "to": "point_id", "type": "related|hierarchical|depends|supplements|contradicts"}
  ]
}
```

程序将模型输出解析整合后，用 `kpn_extract.md` 内 `## Schema` 段的 JSON Schema 校验整合结果，检查：`from` 和 `to` 存在于当前批次 point_id 集合中；`from != to`。

#### 4.4 KPN 校验和写入

```text
校验每条关系：
  - from 和 to 必须存在于当前批次的 point_id 集合中；
  - from != to；

校验通过后写入 knowledge_point_relations：
  - source_point_id = from，target_point_id = to
  - direction 由程序从 type 推断：
      related / contradicts  → bidirectional
      hierarchical / depends / supplements → directed

KPN 生成失败（Schema 校验失败或 LLM 报错）记录 warn 日志，不阻塞 Source 完成；
KPN 生成不改变 KU / KP 的状态。
```

### 步骤 5：Concept 批量匹配

KPN 关系生成（步骤 4）完成后，对当前 Source 下所有状态为 `completed` 的 KU 做批量 Concept 匹配，写入 `knowledge_units.concept_id`。

**输入**：

```text
所有 completed KU 的 unit_id + center；
sources.domain_id（若有，则只取该 domain 下的 concept 列表；若为 null，则取全部 concept 列表）；
concept 列表：concept_id + name + description。
```

**Prompt 文件**：`config/prompts/unit_concept_match.md`

```
以下是一批知识单元，每条包含编号和主题描述：
{{units_list}}

以下是可用的知识概念列表：
{{concept_list}}

请为每个知识单元选择最匹配的概念 concept_id。若没有匹配的概念，concept_id 输出空字符串。

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

`{{units_list}}` 每行格式：`[unit_id] center 描述`
`{{concept_list}}` 每行格式：`[concept_id] name：description`

注入 prompt 的 `{{json_schema}}` 是示例 JSON：

```json
{
  "matches": [
    {"unit_id": "unit_uuid_xxx", "concept_id": "xxx"}
  ]
}
```

程序将模型输出解析整合后，用 `unit_concept_match.md` 内 `## Schema` 段的 JSON Schema 校验整合结果，检查 `unit_id` 存在于当前批次 unit_id 集合中。

**批量策略与结果处理**：

```text
单次 LLM 调用携带不超过 50 个 KU；超出则分批，每批独立调用；
concept_id 非空且存在于 concepts 表：写入 knowledge_units.concept_id；
concept_id 为空或不存在：concept_id 保持 null；
  → concept_id 为 null 的 KU 不受 concept 过滤约束；
LLM 调用失败：记录 warn 日志，当前批次 KU 的 concept_id 保持 null，不阻塞 Source 完成。
```

### 步骤 6：写入 Bleve 索引

KnowledgeUnit 和 KnowledgePoint 入库后同步写入对应 Bleve 索引。KU 正文在写入时按 `sources.markdown_path` + `line_start` / `line_end` 动态切片读取，不单独存文件：

```text
units index 写入字段：
  unit_id、source_id、center、line_start、line_end、content（行号切片正文，供 FTS）

points index 写入字段：
  point_id、unit_id、source_id、content、point_type
```

KPN 关系不写入 Bleve（通过 SQLite 按 point_id 查询）。

索引写入失败记录错误日志，不将 Source 标记为 failed。

### 步骤 7：暴露 HTTP API

```text
POST   /sources/:id/units
  触发对指定 Source 的 Unit 提取（含 KPN 生成）
  仅对 status=completed 的 Source 生效
  响应：{ source_id, triggered_at }

GET    /sources/:id/units
  列出 Source 下的所有 KnowledgeUnit
  响应：[{ unit_id, outline_id, center, line_start, line_end, status }]

GET    /units/:id
  查询单个 KnowledgeUnit 及其 KnowledgePoint
  响应：{
    unit_id, source_id, outline_id, concept_id, center, line_start, line_end, status,
    points: [{ point_id, content, point_type }]
  }

GET    /units/:id/points
  列出 KnowledgeUnit 下的 KnowledgePoint
  响应：[{ point_id, content, point_type }]

GET    /points/:id/relations
  列出指定 KnowledgePoint 的 KPN 关系（双向合并）
  响应：[{
    relation_id, related_point_id, related_point_content,
    relation_type, direction, as_source（bool）
  }]
```

## 依赖

```text
基础设施：SQLite、Bleve 索引、LLM client、结构化日志、HTTP 框架
Source：依赖 Source 完成后产出的规范化 Markdown 和 source_outlines 树
Prompt 文件：config/prompts/ 下，版本号在文件内 frontmatter 中管理，不用文件名前缀
```

## 完成标准

```text
能对已完成的 Source 稳定触发 Unit 提取；
提取结果（KU + KP）写入 SQLite 和 Bleve，可通过 API 查询；
行号转换正确：unit.line_start / line_end 为规范化 Markdown 的绝对行号（1-based, inclusive）；
可通过 strings.Split(markdown, "\n")[line_start-1:line_end] 还原单元原文内容（`markdown_path` 来自 sources 表）；
重试机制正常工作：整体 JSON 失败时 segment 级重试一次；可定位的单元业务校验失败时单元级重试一次，失败单元标记 extraction_failed 并记录 error_msg；
KPN 生成在全 Source KP 提取完成后运行，关系写入 SQLite，可通过 API 按 point_id 查询；
KPN 生成失败不阻塞 Source 完成状态；
Concept 批量匹配在 KPN 完成后运行，concept_id 写入 SQLite，可通过 GET /units/:id 查询；
Concept 匹配失败不阻塞 Source 完成状态；
fake LLM client 下，提取、KPN 和 Concept 匹配路径测试可稳定运行，不依赖真实 LLM 调用。
```
