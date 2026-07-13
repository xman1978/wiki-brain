# Retrieval 实现路径

## 职责

Retrieval 负责根据用户问题，通过两级检索找到相关证据，经 Rerank 分类后输出可追溯的 EvidenceSet 供 Answer 使用。

第一级定位相关 Source，第二级在 Source 内通过目录结构和 FTS 多路召回 KU/KP，最终由 LLM Rerank 做质量判断。

## 核心组件

```text
Source 语义过滤器（LLM 判断哪些 Source 与问题相关）
目录结构召回器（FTS 优先，评分低于阈值时 LLM 语义 fallback）
FTS 召回器（Bleve units + points 全文检索）
多路召回合并器（RRF 融合，去重）
LLM Rerank（逐条分类：直接证据 / 支持性证据 / 无关）
直接回答充分性判断器（短路径 vs 深路径决策）
EvidenceSet 构建器（含 fact_id 和来源位置）
HTTP API
```

## 检索总流程

```text
问题
  → 步骤 2：Domain 预过滤（LLM 匹配 domain，缩小候选 source 范围）
  → 步骤 3：Source 语义过滤（LLM）→ 相关 source 列表
  → 步骤 4：目录结构召回（FTS → LLM fallback）→ KU 候选
  → 步骤 5：FTS 召回（units + points index）→ KU/KP 候选
  → 步骤 6：多路合并（RRF）→ 统一候选集
  → 步骤 7：LLM Rerank → 直接证据 / 支持性证据 / 无关
  → 步骤 8：KPN 扩展（对直接证据 KU 查 KPN 邻居，补充 supporting）
  → 步骤 9：充分性判断 → 短路径 / 深路径
  → 步骤 10：构建 EvidenceSet
```

## 实现步骤

### 步骤 1：定义 EvidenceSet 输出格式

在实现检索逻辑之前，先确定 EvidenceSet 的结构，作为 Retrieval 与 Answer 之间的契约：

```text
EvidenceSet：
  question           原始问题
  subject            核心主题（Session 解析产出，缩小 Rerank 判断范围）
  audience           对象/受益角色（Session 解析产出，如"实施人员"，为空表示问题不涉及角色差异化待遇）
  constraint         约束（Session 解析产出，问题中的专有名称/地点/时间等限定词）
  path               short / deep（由步骤 9 充分性判断写入，Answer 层按此分发路径）
  direct_evidence[]  直接证据列表
  supporting[]       支持性证据列表
  conflicts[]        冲突证据列表（KPN contradicts 扩展产出，见步骤 8），无 fact_id，
                     不参与 Answer 引用，仅作为提示信息随 EvidenceSet 传递给 Answer

每条 Evidence：
  fact_id            全局唯一标识，由步骤 10 统一分配，供 Answer 层引用
  candidate_id       Rerank 阶段使用的临时标识（如 "c1"），步骤 10 完成后不再使用
  unit_id            所属 KnowledgeUnit
  point_id           所属 KnowledgePoint，必填。Evidence 是 Trace / Study 的学习信号来源，
                     每条证据都必须绑定到一个 KP：
                       - points index 命中：使用命中的 point_id；
                       - units index / outline 命中：取该 KU 下的代表 KP；
                       - KPN 扩展补充：使用触发扩展的邻居 KP point_id；
                     若某 KU 没有任何 KP，则不进入 EvidenceSet，记录 warn 日志。
  content            证据文本，取所属 KnowledgeUnit 的完整正文：
                     通过 knowledge_units.line_start / line_end 从规范化 Markdown 切片，
                     即 strings.Join(lines[lineStart-1:lineEnd], "\n")；
                     所有命中路径（points index / units index / outline / KPN 扩展）均统一取 KU 正文，
                     不取 knowledge_points.content（KP 摘要过短，信息量不足以支撑 Rerank 和 Answer）
  source_ref         来源位置，JSON 对象：
                       {"source_id": "xxx", "line_start": 10, "line_end": 25}
                     字段来自 knowledge_units.source_id / line_start / line_end；
                     证据始终携带 point_id，先通过
                       SELECT unit_id FROM knowledge_points WHERE point_id = ?
                     反查到所属 KnowledgeUnit，再取该 KU 的 source_id / line_start / line_end；
                     序列化后存入 answers.evidence_snapshot，供 Trace 和调试反查；
                     不需要对 source_ref 做额外数据库查询，仅用于展示和追溯。
  role               direct / supporting
  origin             rerank / kpn_expansion，标记该证据是 Rerank 直接分类产出还是步骤 8 的 KPN
                     邻居扩展补充；direct 恒为 rerank（KPN 扩展只补充 supporting）；
                     随 EvidenceSet 序列化进 evidence_snapshot，供 Trace 计算 KPN 引用采纳率
                     （见 trace.md / study.md summary.kpn_citation_rate）

每条 ConflictEvidence（conflicts[] 中的元素，字段少于 Evidence，无 fact_id / role）：
  unit_id / point_id / content / source_ref   含义同 Evidence
  source_title       冲突材料所属 Source 标题，供 Answer 提示用户注意来源差异
```

fact_id 是 Answer 层引用证据的唯一标识，Answer 只能使用 EvidenceSet 中已存在的 fact_id。candidate_id 仅在步骤 6～9 的内存处理阶段使用，不对外暴露。conflicts[] 不分配 fact_id，Answer 不能引用它作为证据，只能在回答中提示"存在冲突信息"。

### 步骤 2：Domain 预过滤

在 Source 语义过滤之前，先根据问题匹配相关 Domain，将候选 source 范围缩小到匹配 domain 下的 source。

**输入**：domains 表中所有 domain（domain_id + name + description）+ 问题文本。

**Prompt 文件**：`config/prompts/question_domain_match.md`

```
以下是一个问题：
{{question}}

以下是可用的知识领域列表：
{{domain_list}}

请选择与问题相关的知识领域（可多选，宁多勿漏）。若不确定，返回空列表。

按以下格式输出，不输出任何其他内容：
{"domain_ids": ["id1", "id2"]}
```

`{{domain_list}}` 每行格式：`[domain_id] name：description`

**结果处理**：

```text
domain_ids 非空：后续步骤仅在以下 source 范围内检索：
  - sources.domain_id 在 domain_ids 中的 source；
  - sources.domain_id 为 null 的 source（未分配领域，始终参与召回）；
domain_ids 为空：不做 domain 过滤，所有 source 进入步骤 3；
LLM 调用失败：记录 warn 日志，不做 domain 过滤，所有 source 进入步骤 3。
```

### 步骤 3：Source 语义过滤

在 Domain 预过滤（步骤 2）确定候选 source 范围后，进一步用 LLM 判断哪些 Source 与当前问题相关。

文件级过滤是整个检索链的最窄漏斗，遗漏意味着答案永久丢失，因此使用 LLM 语义判断而非 FTS 或向量搜索。

**输入**：步骤 2 确定的候选 source 列表（title + summary，summary 为空时只用 title）。

**Prompt 文件**：`config/prompts/source_filter.md`

```
以下是知识库中的所有文档，每条包含编号、标题和内容概述。

文档列表：
{{source_list}}

问题：{{question}}

请列出与问题可能相关的文档编号（source_id）。只选择有可能包含答案的文档，宁多勿漏。若无相关文档，返回空列表。

按以下格式输出：
{"source_ids": ["id1", "id2"]}
```

`{{source_list}}` 每行格式：`[source_id] 标题：xxx / 概述：xxx`

**结果处理**：
```text
过滤结果非空：后续步骤只在这些 source 内检索；
过滤结果为空：扩大为 Domain 预过滤后的候选 source 范围，避免因摘要质量问题造成漏召回；
若 Domain 预过滤未生效，则候选范围即为全部 source。
```

### 步骤 4：目录结构召回

在 Source 语义过滤（步骤 3）确定的范围内，通过 outline 节点定位相关知识区域，取命中节点下的所有 KnowledgeUnit。

#### 4.1 FTS 召回（优先路径）

对过滤后 source 的 outline 节点按 `title + summary` 做 FTS：

```text
从问题提取关键词，对 outlines index 发起 Bleve 查询，限定 source_id 范围；
记录每个 source 命中的最高 BM25 分数（无结果则为 0）；
最高分 ≥ outline_fts_min_score（默认 0.5，可通过 config.yml: retrieval.outline_fts_min_score 配置）：
  取命中节点及其所有子节点覆盖的 KnowledgeUnit，进入步骤 6 合并；
最高分 < 0.5：触发步骤 4.2 LLM 语义匹配。
```

#### 4.2 LLM 语义匹配（FTS 评分低于阈值时触发）

收集所有 FTS 最高分低于阈值的 source（包含零结果），对它们**并发**发起 LLM 调用，而非逐一串行。

**Prompt 文件**：`config/prompts/outline_filter.md`

```
以下是一篇文档的目录节点列表，每条包含编号和标题关键词。

目录节点：
{{outline_list}}

问题：{{question}}

请列出与问题相关的目录节点编号（outline_id）。若整篇文档都不相关，返回空列表。

按以下格式输出：
{"outline_ids": ["id1", "id2"]}
```

`{{outline_list}}` 每行格式：`[outline_id] ${"  " × (level-1)}标题 / 关键词：xxx`

```text
并发策略：对所有 FTS 最高分低于阈值（< 0.5）的 source 同时发起 goroutine，等待全部完成后合并结果；
          并发数受 LLM client 的最大并发配置约束（config.yml: llm.max_concurrency，默认 5）；
有结果：取命中节点及其所有子节点覆盖的 KnowledgeUnit，进入步骤 6 合并；
无结果：该 source 的目录结构路径不贡献候选，由 FTS 路径（步骤 5）兜底。
```

### 步骤 5：FTS 召回

对过滤后 source 范围内的 units index 和 points index 分别发起 Bleve 查询：

```text
对问题分词后构建 Bleve 查询，限定 source_id 范围；
units index 返回 unit_id + BM25 分，直接作为候选；
points index 返回 point_id + BM25 分，需转换为 unit_id：
  SELECT unit_id FROM knowledge_points WHERE point_id = ?
  同一 unit 被多个 point 命中时，取各 point 中最高的 BM25 分作为该 unit 的 FTS 分；
units 和 points 两路在 unit_id 粒度归并后，作为"FTS 召回"一路进入步骤 6 RRF 融合；
归并后若同一 unit_id 来自 units 路和 points 路，取两路得分中较高者。
同时保留候选的代表 point_id：
  - points 路命中时，使用得分最高的命中 point_id；
  - units 路命中但没有 points 路命中时，查询该 unit 下第一条 KnowledgePoint 作为代表 point_id；
  - 若该 unit 下不存在 KnowledgePoint，丢弃该候选并记录 warn。
```

### 步骤 6：多路召回合并（RRF）

将目录结构召回（步骤 4）和 FTS 召回（步骤 5）的结果用 RRF（Reciprocal Rank Fusion）融合：

```text
各路候选按来源赋予排名；
RRF 公式：score = Σ 1 / (k + rank_i)，k 默认取 60；
按 unit_id 去重，同一 unit 来自多路时累加各路 RRF 分；
保留每条候选的来源路径（目录 / FTS / 两者都有），用于 Trace 记录；
按 RRF 分数降序排列，截取 Top N（默认 20，可通过 config.yml: retrieval.rerank_top_n 配置）；
截断后进入 Rerank，避免候选过多导致 Rerank prompt token 超限。
```

### 步骤 7：LLM Rerank

调用 `classification` 模型，对每条候选证据独立分类：

```text
RRF 合并（步骤 6）完成后，为每条候选分配临时 candidate_id（如 "c1"、"c2"，按 RRF 排名顺序）；
候选 content 来源：JOIN knowledge_units 与 sources，按 sources.markdown_path 读取规范化 Markdown，
  用 knowledge_units.line_start / line_end 切片（见 foundation.md 行号约定），不依赖独立正文文件；
候选证据按 sourceID 分组聚合、标注来源文档标题（`sources.title`，通过 `Store.GetSourceTitle`
  查询）后再拼入 prompt（格式：`【来源文档：标题】` 后接该文档下各条 `[candidate_id] content`），
  而非逐条打散罗列，使 LLM 能够感知对象/约束等限定信息（如"仅适用于渠道伙伴"）可能只
  出现在文档标题中、不会逐条重复在每条证据正文里；分组顺序按候选首次出现的 RRF 排名；
输入：问题 + 核心主题（subject）+ 意图（intent）+ 对象（audience）+ 约束（constraint，均来自
  Session 解析，缺失时分别填充占位文案"（未提取）"/"（无）"）+ 按来源文档分组的候选
  证据列表（每条附 candidate_id 和 content）；
模型输出：每条证据的结构化语义抽取结果，包括来源主题、内容主题、意图、对象、范围和关键事实；
模型判断：将所有候选的结构化语义交给 `rerank_judge.md` 判断
  direct / supporting / irrelevant；
程序校验：只校验 candidate_id 必须来自当前候选批次，role 必须是 direct / supporting / irrelevant；
direct 和 supporting 证据进入后续环节，irrelevant 排除；
Rerank 是逐条分类器，不是 Top-K 选择器，不截断数量；
程序校验两个模型输出中的 candidate_id 必须来自当前候选批次，缺失的 candidate_id 不进入后续证据集。
```

**第一阶段 Prompt 文件**：`config/prompts/rerank_extract.md`

```
你是证据语义抽取助手。只抽取每条候选证据本身真正表达的语义事实，不判断相关性。

问题：{{question}}
核心主题：{{subject}}
意图：{{intent}}
对象：{{audience}}
约束：{{constraint}}

证据列表（按来源文档分组，格式：【来源文档：标题】后接该文档下的 [candidate_id] 证据内容）：
{{candidates}}

每条证据独立抽取，不遗漏任何 candidate_id，只输出 JSON。
```

模型输出示例：

```json
{
  "results": [
    {
      "candidate_id": "c1",
      "source_theme": "来源文档主题",
      "content_theme": "证据内容主题",
      "intent": "证据提供的信息类型",
      "object": "证据适用对象",
      "scope": "证据适用范围",
      "key_facts": ["关键事实"]
    }
  ]
}
```

`subject` / `intent` / `audience` / `constraint` 均来自 Session 模块的上下文感知解析
（设计见 session.md "SessionParser" 章节，prompt 见 `config/prompts/session_parse.md`），
通过 `QueryContext` 传入 Retrieval，再经 `EvidenceSet.Subject` / `EvidenceSet.Intent` /
`EvidenceSet.Audience` / `EvidenceSet.Constraint` 传给 Answer 层。

程序将模型抽取结果解析后，调用 `config/prompts/rerank_judge.md` 对所有候选统一判断。
程序不再使用业务词表规则提前判 direct / supporting / irrelevant，避免因规则覆盖面不足误杀相关证据。

**第二阶段判断 Prompt 文件**：`config/prompts/rerank_judge.md`

```
你是证据关系判断助手。根据问题语义和已抽取的证据语义，判断每条候选证据的角色。
只使用结构化字段判断，不重新解释原始证据，不补充输入之外的事实。
对象明确冲突或主题场景明确不同且无依赖关系时判 irrelevant。
```

**Rerank 输出示例格式**（使用 candidate_id，此时 fact_id 尚未分配）：
```json
{"results": [{"candidate_id": "c1", "role": "direct|supporting|irrelevant"}]}
```

### 步骤 8：KPN 上下文扩展

Rerank 完成后，对标记为直接证据的 KU 做 KPN 扩展，补充变量、边界、前提等上下文；`contradicts` 关系单独查询，产出 conflicts 而非 supporting。

KPN 关系类型 MVP 阶段只有 2 种（related / contradicts，见 unit.md 设计决策），均写入为 `direction = 'bidirectional'`。查询按用途拆成两条，而非按 type 统一处理：

```text
取所有 direct 证据的 unit_id；
查询这些 unit 下的全部 KnowledgePoint，得到 seed_point_ids；

邻居查询（GetKPNNeighbors，排除 contradicts）：
  target_point_id WHERE source_point_id IN seed_point_ids AND relation_type != 'contradicts'
  UNION
  source_point_id WHERE target_point_id IN seed_point_ids
    AND direction = 'bidirectional' AND relation_type != 'contradicts'
  去除 seed_point_ids 本身，得到 neighbor_point_ids；
  取 neighbor_point_ids 所属的 KnowledgeUnit（unit_id 去重），排除已存在于候选集中的 KU；
  新增 KU 加入候选集，role 标记为 supporting，point_id 填写触发该扩展的邻居 KP 的 point_id。

冲突查询（GetKPNConflicts，仅 contradicts）：
  target_point_id WHERE source_point_id IN seed_point_ids AND relation_type = 'contradicts'
  UNION
  source_point_id WHERE target_point_id IN seed_point_ids AND relation_type = 'contradicts'
  （contradicts 恒按双向处理，不依赖 direction 列）
  排除 seed_point_ids 本身；查到对应 KnowledgeUnit 后写入 EvidenceSet.conflicts（见步骤 1），
  不加入候选集、不参与 Rerank、不分配 fact_id。
```

邻居查询的 `direction = 'bidirectional'` 条件是为未来可能引入的有向关系类型（如原设计中的 hierarchical / depends）保留的通用机制：MVP 当前只产生 bidirectional 数据，所以该条件目前恒为真，仅在未来又出现 directed 类型时才会生效收窄邻居范围。
direct_evidence 为空时跳过此步骤。

### 步骤 9：直接回答充分性判断

Rerank 和 KPN 扩展完成后，根据以下规则写入 EvidenceSet.path：

```text
short 路径条件（满足以下全部）：
  - direct_evidence 非空（Rerank 分类出至少一条 direct）

否则（direct_evidence 为空）→ deep 路径。

说明：
- MVP 阶段不做"内容覆盖度"语义判断，只判断 direct 证据是否存在
- Rerank 已对每条候选做过语义质量过滤，进入 direct_evidence 的条目均为 LLM
  判定"直接包含问题答案"的证据，无需再做二次判断
- short/deep 的本质区别体现在 Answer 层使用不同 Prompt 结构，此处判断保持最简
```

充分性判断结果写入 EvidenceSet.path（short / deep），与证据列表一起传递给 Answer。

### 步骤 10：构建 EvidenceSet

按步骤 1 定义的结构，将证据组装为 EvidenceSet：

```text
对 Rerank 保留（direct + supporting）及 KPN 扩展的所有候选，统一分配全局唯一 fact_id；
建立内存映射：candidate_id → fact_id（用于 Rerank 结果与 fact_id 对应）；
填入每条证据的 unit_id、point_id（所有证据均有 point_id，KPN 扩展来源的条目填写触发扩展的邻居 KP point_id）、
  content（同上，按 markdown_path + line_start/line_end 动态读取）、source_ref 和 role；
写入 path（来自步骤 9）；
EvidenceSet 作为 Retrieval 的最终输出传递给 Answer，candidate_id 不出库。
```

### 步骤 11：暴露 HTTP API 和内部接口

```text
POST   /retrieval      接收问题，返回 EvidenceSet（HTTP API，供外部测试）
内部接口              供 Answer 模块直接调用，不经过 HTTP
```

## 依赖

```text
基础设施：SQLite、Bleve 索引、LLM client、结构化日志、HTTP 框架
Foundation：依赖 domains 表中已加载的预制 Domain 数据（Domain 预过滤使用）
Source：依赖 sources.summary、sources.domain_id 和 source_outlines（title + summary）供过滤和目录召回使用
Unit：依赖 KnowledgeUnit / KnowledgePoint 已写入 SQLite 和 Bleve 索引，以及 knowledge_point_relations 表（KPN 扩展使用）
```

## 完成标准

```text
Domain 预过滤能正确匹配问题相关 domain，domain_id 为 null 的 source 始终参与召回；
Domain 匹配失败时自动跳过预过滤，不影响后续检索；
Source 语义过滤在 Domain 过滤后的候选范围内正常工作，过滤结果为空时自动扩大到候选范围全部 source；
目录结构召回 FTS 路径正常工作；LLM fallback 对所有 FTS 最高分低于阈值（< 0.5）的 source 并发发起，受 llm.max_concurrency 约束；
FTS 召回命中 units index 和 points index；
RRF 合并正确去重、多路得分累加并保留来源路径，按 RRF 分截取 Top N（默认 20）后进入 Rerank；
LLM Rerank 对每条候选正确分类，输出结构通过 JSON Schema 校验；
KPN 扩展能正确查询直接证据 KU 的邻居 KP，新增 KU 以 supporting 角色加入 EvidenceSet；
KPN contradicts 邻居正确写入 EvidenceSet.conflicts（不加入候选集、不分配 fact_id）；
Evidence.origin 正确标记（KPN 扩展补充的 supporting 为 kpn_expansion，其余为 rerank）；
短路径和深路径分叉逻辑正常工作；
EvidenceSet 中每条证据可回溯到 KnowledgeUnit 和来源位置；
fake LLM client 下测试可稳定运行。
```
