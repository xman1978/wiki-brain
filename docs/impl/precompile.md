# Precompile 实现方案

本文档描述知识大脑第一版中 `precompile` 的实现方案。

`precompile` 不是把导入材料生成完整知识体系。

它负责把 `unit` 阶段联合形成的知识单元和知识点，预编译成两类可用结构：

```text
材料侧召回索引；
Domain / Concept 静态匹配候选。
```

材料侧召回索引用于多路召回，保证知识即使没有被当前领域和概念覆盖，也仍然可以被发现。

静态匹配候选用于缩小后续学习范围，不是正式激活路径，也不直接参与认知结构召回。

## 重构说明（2026-06）

旧 per-point precompile 流程已移除，不再保留以下运行时 artifact：

```text
prompts/precompile/knowledge_point.md
prompts/precompile/domain_match.md
prompts/precompile/concept_match.md
schemas/precompile/knowledge_point.schema.json
schemas/precompile/domain_match.schema.json
schemas/precompile/concept_match.schema.json
ServiceName: precompile.knowledge_point / precompile.domain_match / precompile.concept_match
KnowledgePointBuilder / ActivationLinkBuilder
```

当前运行时路径：

```text
unit.knowledge_compile (+ unit.knowledge_compile_repair) -> 持久化 KnowledgePoint
precompile.domain_concept_match -> 写入 concept_match_candidates（每 KnowledgeUnit 一次联合调用）
Study -> 从真实使用创建 candidate ActivationLink
```

历史数据库表/字段（`activation_links`、`concept_match_candidates` 等）保留，旧 trace/event 值仍可读取。

## 第一版主链路

第一版 `precompile` 只完成以下主链路：

```text
1. 创建默认 Perspective
2. 导入预制 Domain / Concept
3. 接收 unit 阶段联合生成的 KnowledgeUnit / KnowledgePoint
4. 构建多路召回索引
5. 按 KnowledgeUnit 批量联合匹配已有 Domain / Concept
6. 保存静态匹配候选，供 retrieval / trace / study 验证
```

这条链路的目标是让知识同时具备两种可用性：

```text
可以通过材料侧多路召回被发现；
可以在真实使用中形成待验证的认知结构路径。
```

第一版不做：

```text
自动创建正式 Domain / Concept；
自动晋升候选 Domain / Concept；
复杂概念合并；
开放知识图谱关系；
跨领域推理；
多 Perspective 管理；
向量库；
预制文件热重载。
```

第一版 `precompile` 可以把同一 KnowledgeUnit 下的多个 KnowledgePoint 批量匹配到已有 active Domain / Concept，但不得建立 active 或 candidate ActivationLink。

Domain 和 Concept 是父子约束，应在同一个模型提示词中联合判定。默认调用粒度为“一个 KnowledgeUnit 的全部 KnowledgePoint 一次调用”；只有上下文和结构化输出经过评测确认稳定时，才允许整份 Source 一次调用。调用前应使用规则、关键词或向量召回预筛 Concept，避免随知识体系增长把全部 Concept 重复发送给模型。

它不能因为一次导入、一次模型匹配或单篇材料内容，自动创建正式 Domain / Concept。

候选 Domain / Concept 的形成和晋升必须来自后续真实问题处理中的 `retrieval -> trace -> study / review` 流程。

## 核心原则

```text
知识单元来自材料；
知识点来自知识单元；
领域和概念来自预制框架和反复使用；
ActivationLink 只表示激活路径，不表示知识图谱关系；
多路召回索引保证知识可发现；
认知结构激活索引提升知识利用质量。
```

`precompile` 不重新读取原始 Markdown 来理解材料。

它消费 `unit` 输出的结构化知识单元：

```text
KnowledgeUnit
  id
  source_document_id
  title
  center
  unit_type
  content
  internal_points
  primary_heading_path
  covered_heading_paths
  outline_paths
  outline_refs
  source_spans
  status
  confidence
  warnings
```

只有 `active` 且可回溯的 `KnowledgeUnit` 进入第一版预编译链路。

## 组件

第一版建议拆成以下组件：

```text
PerspectiveStore
PresetImporter
MultiRouteIndexBuilder
DomainCandidateBuilder
ConceptCandidateBuilder
ConceptMatcher
PrecompileEventStore
```

实现方必须校验 `ConceptMatcher` 输出：

```text
concept_id 必须存在于 ConceptCandidateBuilder 输出中；
domain_id 必须存在于 DomainCandidateBuilder 输出中；
concept.domain_id 必须等于返回的 domain_id；
否则按 concept_match_output_invalid 处理。
```

### PerspectiveStore

负责默认认知视角。

第一版只需要一个默认视角：

```text
id: default
name: 默认视角
description: 系统默认认知视角，用于承载第一版的领域、概念和激活路径。
status: active
```

启动时和执行 `precompile` 前都应执行：

```text
EnsureDefaultPerspective
```

该操作必须幂等：

```text
如果 default perspective 不存在，则创建；
如果已存在且 active，则直接返回；
如果已存在但 disabled，则返回错误，precompile 不继续写入正式结构。
```

`KnowledgePoint` 不绑定 `perspective_id`。

`Domain`、`Concept`、`ActivationLink` 必须绑定 `perspective_id`。

### PresetImporter

负责导入预制 `Domain / Concept`。

预制结构以 JSON 文件存储，系统启动时导入数据库。

启动导入发生在 SQLite 初始化和 migration 完成之后。

启动阶段只导入 Perspective / Domain / Concept，不生成 KnowledgePoint 或 ActivationLink。

`PrecompileJob` 同样不得写入 ActivationLink。它只保存静态 Domain / Concept 匹配候选；首次 candidate ActivationLink 由后续 `retrieval -> trace -> study` 基于真实问题和实际采用证据创建。

建议目录：

```text
presets/precompile/*.json
```

单个 JSON 文件结构：

```json
{
  "perspective_id": "default",
  "version": "1.0.0",
  "domains": [
    {
      "id": "system_architecture",
      "name": "系统架构",
      "description": "用于理解系统组成、边界、接口、部署和质量属性。",
      "status": "active",
      "concepts": [
        {
          "id": "module_decomposition",
          "name": "模块划分",
          "aliases": ["模块设计", "功能模块"],
          "description": "关注系统如何拆分为功能模块或服务。",
          "boundary": "关注模块职责、边界和协作关系，不直接描述具体页面交互。",
          "status": "active"
        }
      ]
    }
  ]
}
```

导入顺序：

```text
EnsureDefaultPerspective
LoadPresetFiles
ValidatePresetFiles
ImportDomains
ImportConcepts
```

导入规则：

```text
perspective_id + domain.id 唯一；
perspective_id + concept.id 唯一；
重复启动不重复创建；
JSON 中存在且数据库中存在，则更新预制字段；
JSON 中不存在但数据库中存在，不自动删除；
status = disabled 的对象不参与后续匹配。
```

JSON 可以覆盖的字段：

```text
name
description
aliases
boundary
status
preset_version
```

JSON 不应覆盖运行时统计字段：

```text
activation_count
used_count
positive_feedback_count
negative_feedback_count
last_used_at
```

校验失败时，第一版应启动失败。

预制结构是系统基础认知框架，坏数据不应静默进入运行状态。

### KnowledgePoint 生成（已迁移至 Unit）

`KnowledgePoint` 现由 `unit.knowledge_compile` 在 Unit 阶段联合生成并持久化，Precompile 只读取已落库的知识点。

提示词和输出 schema：

```text
prompts/unit/knowledge_compile.md
schemas/unit/knowledge_compile.schema.json
```

输出结构：

```json
{
  "knowledge_points": [
    {
      "text": "知识单元是围绕稳定主题或判断形成的最小完整知识包。",
      "point_type": "definition",
      "evidence_text": "知识单元不是知识点，而是围绕稳定主题或判断形成的最小完整知识包。",
      "confidence": 0.92
    }
  ]
}
```

每个知识点至少保存：

```text
id
unit_id
text
normalized_text
point_type
evidence_text
source_spans
confidence
status
created_at
updated_at
```

如果无法精确定位到更小范围：

```text
knowledge_point.source_spans = unit.source_spans
```

不要伪造精确来源位置。

幂等规则：

```text
unit_id + normalized_text 唯一；
重复运行时 upsert；
不重复插入；
不物理删除旧知识点；
只让 active KnowledgePoint 进入后续匹配和索引。
```

### MultiRouteIndexBuilder

负责构建材料侧多路召回索引。

这是第一版必须补齐的能力。

如果只有 `ActivationLink`，系统只能通过已有认知结构找到知识。

当前领域和概念没有覆盖的知识，就无法被补充查找发现，也无法暴露认知结构缺口。

因此，`precompile` 必须同时产出：

```text
材料侧召回索引；
认知结构激活索引。
```

第一版多路召回索引包括：

```text
source document 索引；
unit title / center / content 索引；
knowledge point 索引；
outline node 索引；
outline tree match 素材（semantic_outlines 树结构 + outline_search_index 节点到单元映射）。
```

建议表：

```text
source_search_index
unit_search_index
knowledge_point_search_index
outline_search_index
```

`source_search_index`：

```text
id
source_document_id
title
description
top_outline_summary
status
updated_at
```

`source_search_index` 用于文档级树搜索的第一步。

它表达一篇 SourceDocument 是否可能回答 query，而不是表达某个具体知识单元。

```text
title 来自 source_documents.title；
description 是文档级检索匹配短语（一句话或关键词），用于判断 query 是否进入该文档，不是给人阅读的摘要；
top_outline_summary 聚合该 source 下一级或二级目录节点的检索匹配短语，控制总长度；
source_document_id 是后续 outline tree search 的文档边界。
```

`unit_search_index`：

```text
unit_id
source_document_id
title
center
content
source_spans
status
updated_at
```

`knowledge_point_search_index`：

```text
knowledge_point_id
unit_id
source_document_id
text
point_type
source_spans
status
updated_at
```

`outline_search_index`：

```text
id
unit_id
source_document_id
outline_type
outline_path
outline_level
outline_summary
source_spans
status
updated_at
```

`outline_search_index` 不是单纯的路径字符串索引。

它应表达一个可检索目录节点：

```text
outline_type + outline_path 标识目录节点；
outline_summary 描述该节点覆盖范围；
unit_id 表示该节点下可展开到的知识单元；
source_spans 用于回到原文证据。
```

第一版词法检索使用 **Bleve sidecar + gse 分词**，详见 `docs/impl/fts.md`。

SQLite 四张 `*_search_index` 表保存元数据与 rebuild 源；倒排索引与 BM25 打分由 Bleve 负责，不使用 SQLite FTS5。

向量索引先不作为必要能力。

索引写入规则：

```text
ready source_document 写入 source_search_index；
active KnowledgeUnit 写入 unit_search_index；
active KnowledgePoint 写入 knowledge_point_search_index；
可回溯 outline_path 写入 outline_search_index；
每次 SQLite upsert 成功后同步写 Bleve sidecar 索引（见 docs/impl/fts.md）；
source_search_index.description 应优先使用明确的文档级说明；缺失时由 source 标题、intake_purpose 和 top-level outline_summary 聚合生成；
source_search_index.top_outline_summary 聚合该 source 下一级或二级目录节点说明，不拼接全文；
semantic outline 应通过 outline_refs 回查 semantic_outlines.summary 并写入 outline_summary；
source / recovered outline 应优先写入该 heading section 的目录说明；
source / recovered outline 如果没有 summary，可使用 heading path + unit.center + original_excerpt 作为退化摘要；
unit 或 point 失效时，将对应索引 status 置为 disabled，并同步清理 Bleve 文档；
不物理删除 SQLite 索引记录。
```

目录说明生成规则：

```text
semantic outline:
  通过 KnowledgeUnit.outline_refs 回查 semantic_outlines.summary；

source / recovered outline:
  优先使用 unit 阶段保存的 OutlineNodeSummary；
  如果没有持久化说明，则用该目录下 active KnowledgeUnit 的 title / center / original_excerpt 聚合生成；
  长目录范围可调用 extraction 模型生成一到两句 summary；

manual outline:
  优先使用人工说明；
  缺失时按 source outline 退化规则生成。
```

`outline_summary` 必须是面向模型路由的**检索匹配短语**。

它帮助后续模型判断 query 是否应进入该目录节点，而不是向用户解释节点内容。

要求：

```text
一句话以内，最好 8–40 字；
可以只是主题关键词，如“无合同立项 四层审批 总经办决策”；
不是给人阅读的段落摘要；
不应只复述标题。
```

质量约束：

```text
outline_summary 为空的 outline_search_index 行不得写入 active；
只能生成退化摘要时，应记录 warning 或降低该 outline route 的置信度；
semantic outline 的 summary 与 path 不一致时，应标记 needs_review，不参与高置信 outline_tree_match。
```

当 SourceDocument / KnowledgeUnit / KnowledgePoint 进入 `disabled / discarded / deleted` 时，材料侧索引必须同步标记为相同状态。

retrieval 必须按状态过滤，不得依赖物理删除索引项保证正确性。

幂等规则：

```text
source_search_index 按 source_document_id upsert；
unit_search_index 按 unit_id upsert；
knowledge_point_search_index 按 knowledge_point_id upsert；
outline_search_index 按 source_document_id + outline_type + outline_path + unit_id upsert。
```

#### Outline-guided Center Match

全文检索可能漏掉表达不同但语义相关的知识单元。

第一版可以支持一条高质量补充召回路线：

```text
文档选择
  -> 单文档目录树检索
  -> 缩小候选 KnowledgeUnit 范围
  -> 使用 LLM 匹配 query 与 KnowledgeUnit.center
  -> 返回高相关 KnowledgeUnit
```

这条路线借鉴 PageIndex 的文档级树搜索思想：先选择可能相关的 SourceDocument，再在该文档的目录树内定位范围，最后用模型判断中心句相关性。

`precompile` 不执行模型匹配。

`precompile` 只需要准备素材：

```text
outline_path
outline_summary
unit_id
source_document_id
unit.title
unit.center
unit.source_spans
```

其中 `outline_summary` 来自目录节点说明。

semantic outline 使用 `semantic_outlines.summary`。

source / recovered / manual outline 使用对应目录节点说明。

它用于让 `retrieval` 先判断目录节点和问题的相关性，再展开节点下的知识单元做中心句匹配。

真正的中心句匹配由 `retrieval` 执行。

执行时应受模型上下文限制：

```text
阶段一：Bleve 宽召回 top source documents，再让模型联合 Domain 信号与文档摘要排序；
阶段二：对每个候选 source 加载去正文树结构，让模型返回合法 source_id + node_id；
节点无直接 unit 时，展开子节点或查 outline_search_index；
如果单文档树过大，先 Bleve 预筛节点或继续下钻；
将结果作为 route = outline_tree_match 的一路召回信号。
```

这条路线的价值是：

```text
全文检索擅长词面命中；
知识点检索擅长明确判断；
目录树词法回退擅长结构定位；
outline_tree_match 擅长按文档理解范围语义。
```

### DomainCandidateBuilder

负责先为 `KnowledgePoint` 预筛候选 `Domain`。

它只筛选已有 active Domain，不创建正式 Domain。

输入：

```text
Perspective
active Domain
active KnowledgePoint
KnowledgeUnit 上下文
```

候选来源：

```text
domain.name 命中；
domain.description 关键词命中；
domain.source / preset_version 可用于限制预制范围；
knowledge_point.text 与 domain.description 语义相关；
unit.title / center 与 domain.name 或 description 语义相关；
unit.outline_paths 与 domain.name 或 description 语义相关。
```

第一版采用：

```text
规则预筛 + LLM 判定
```

LLM 只接收规则预筛后的少量候选 Domain。

如果规则预筛没有候选，不应把全部 Domain 交给模型判断。

提示词和输出 schema：

```text
prompts/precompile/domain_concept_match.md
schemas/precompile/domain_concept_match.schema.json
ServiceName: precompile.domain_concept_match
```

Prompt 约束：

```text
只能从候选 Domain 中选择；
不能创造新的正式 Domain；
如果没有合适 Domain，返回 unmatched；
判断依据是知识点是否应该通过该 Domain 下的 Concept 被进一步激活；
不要仅因为词面相似就匹配；
必须考虑 domain.description；
必须返回 confidence 和 reason；
confidence < 0.55 不返回 match。
```

输出结构：

```json
{
  "matches": [
    {
      "domain_id": "knowledge_organization",
      "confidence": 0.83,
      "reason": "知识点描述知识组织对象，符合该领域的使用边界。"
    }
  ],
  "unmatched_reason": null
}
```

如果没有合适领域：

```json
{
  "matches": [],
  "unmatched_reason": "候选领域中没有覆盖该知识点所表达的业务或认知方向。"
}
```

每个 `KnowledgePoint` 第一版最多保留 5 个候选 `Domain`。

`DomainCandidateBuilder` 只产生候选范围，不写入 `ActivationLink`。

### ConceptCandidateBuilder

负责在候选 `Domain` 下继续预筛候选 `Concept`。

它只筛选已有 active Concept，不创建正式 Concept。

输入：

```text
Perspective
DomainCandidate
active Concept where concept.domain_id in candidate_domain_ids
active KnowledgePoint
KnowledgeUnit 上下文
```

候选来源：

```text
concept.name 命中；
concept.aliases 命中；
concept.description 关键词命中；
concept.boundary 关键词命中；
unit.title / center 命中；
unit.outline_paths 命中；
DomainCandidate.reason 提供的领域依据。
```

不要把所有概念一次性丢给模型。

`ConceptCandidateBuilder` 必须只在候选 Domain 下选 Concept。

如果 `DomainCandidateBuilder` 没有返回候选 Domain，则不执行 `ConceptCandidateBuilder`，直接记录 unmatched 事件。

### ConceptMatcher

负责把 `KnowledgePoint` 匹配到候选 `Domain / Concept`。

匹配已有结构，不创建正式结构。

匹配输入：

```text
Perspective
DomainCandidate
ConceptCandidate
active KnowledgePoint
KnowledgeUnit 上下文
```

匹配时不能只看知识点文本。

应同时提供：

```text
knowledge_point.text
knowledge_point.point_type
unit.title
unit.center
unit.outline_paths
unit.internal_points
```

匹配目标不是判断词面出现了哪个概念，而是判断：

```text
在当前认知视角下，这个知识点最应该通过哪个 Domain / Concept 被激活？
```

第一版采用：

```text
DomainCandidateBuilder 预筛 Domain；
ConceptCandidateBuilder 在候选 Domain 下预筛 Concept；
ConceptMatcher 用 LLM 做最终判定。
```

最终判定仍需考虑：

```text
domain.name；
domain.description；
DomainCandidate.reason；
concept.name 命中；
concept.aliases 命中；
concept.description 关键词命中；
concept.boundary 关键词命中；
unit.title / center 命中；
unit.outline_paths 命中。
```

LLM 只能在 `ConceptCandidateBuilder` 输出的候选概念中选择。

提示词和输出 schema（与 Domain 联合判定，见上文 `domain_concept_match`）：

```text
prompts/precompile/domain_concept_match.md
schemas/precompile/domain_concept_match.schema.json
ServiceName: precompile.domain_concept_match
```

Prompt 约束：

```text
只能从候选 Concept 中选择；
返回的 domain_id 必须等于该 Concept 所属的候选 Domain；
不能创造新的正式 Concept；
如果没有合适 Concept，返回 unmatched；
判断依据是知识点是否应该通过该概念被激活；
不要仅因为词面相似就匹配；
必须考虑 concept.boundary；
必须区分 active 挂载和 candidate 挂载。
```

输出结构：

```json
{
  "matches": [
    {
      "domain_id": "knowledge_organization",
      "concept_id": "knowledge_unit",
      "activation_status": "active",
      "confidence": 0.91,
      "reason": "知识点直接定义知识单元边界，符合该概念的边界。"
    }
  ],
  "unmatched_reason": null
}
```

如果没有合适概念：

```json
{
  "matches": [],
  "unmatched_reason": "候选概念中没有覆盖该知识点表达的语义目录生成条件。"
}
```

分层规则：

```text
confidence >= 0.80 -> active ActivationLink
0.55 <= confidence < 0.80 -> candidate ActivationLink
confidence < 0.55 -> unmatched
```

### 前置问题区分

候选挂载、检索采用和固定晋升处理的是不同问题。

不能把同一个模型置信度同时用于三类判断。

第一版必须先区分当前阶段要回答的问题：

```text
precompile 阶段回答：
  这个 KnowledgePoint 是否可能通过某个已有 Domain / Concept 被激活？

retrieval / trace 阶段回答：
  在一次具体用户问题中，这条候选路径是否实际帮助找到并采用了有用知识？

study / review 阶段回答：
  经过多次真实问题验证后，这条候选路径是否应固定为 active ActivationLink？
```

因此：

```text
precompile 的 confidence 只决定 active / candidate / unmatched 的初始写入；
retrieval 的命中和采用只形成使用证据；
study / review 才能基于累计证据执行固定、降级或丢弃。
```

禁止用单次导入匹配直接证明长期固定。

也禁止用单次用户问题的采用结果直接证明 Domain / Concept 归属稳定。

### 候选挂载规则

对预制 `Domain / Concept`，`precompile` 可以先建立候选挂载。

候选挂载不是正式分类关系，而是 `candidate ActivationLink`。

它表示：

```text
当前知识点和知识单元可能应通过该 Domain / Concept 激活；
但证据不足以直接固定为 active 激活路径。
```

候选挂载的触发条件：

```text
Concept 来自 active 预制结构；
Concept 所属 Domain 来自 active 预制结构；
KnowledgePoint 和 KnowledgeUnit 均为 active 且可回溯；
候选 Domain 通过 DomainCandidateBuilder；
候选 Concept 通过 ConceptCandidateBuilder；
LLM 返回 confidence >= 0.55 且 < 0.80；
reason 明确说明匹配依据和不确定性；
未违反 concept.boundary。
```

以下情况不得建立候选挂载：

```text
候选 Domain 不在输入候选集内；
候选 Concept 不在输入候选集内；
Concept 不属于候选 Domain；
需要创建新的 Domain / Concept 才能解释该知识点；
只是词面相似，语义边界不匹配；
KnowledgeUnit.status = needs_review；
KnowledgePoint 不能回到 source_spans；
confidence < 0.55。
```

候选挂载写入规则：

```text
link_type = candidate
status = candidate
confidence = LLM 返回置信度
match_reason = LLM 返回 reason
perspective_id + concept_id + knowledge_point_id 幂等 upsert
```

候选挂载验证规则：

```text
retrieval 默认只把 active ActivationLink 作为正式认知结构召回；
retrieval 不读取 candidate ActivationLink 作为激活信号；
补充检索独立命中且实际采用知识后，study 才可用该结果验证 candidate；
trace 应记录候选挂载是否被独立材料、EvidenceRef 和实际采用支持；
used_count、positive_feedback_count、negative_feedback_count 用于后续判断；
candidate 不因单次命中自动晋升为 active；
晋升应由 study / review 基于多次使用、正反馈、边界一致性和去重问题簇完成；
已晋升的 active ActivationLink 如果持续弱相关、被负反馈或违反 concept.boundary，应由 study / review 停用或丢弃；需要重新学习时另建 candidate，不能让该 candidate 继续参与检索。
```

每个 `KnowledgePoint` 第一版最多匹配 3 个 `Concept`。

未匹配知识点应记录 `PrecompileEvent`：

```text
event_type = knowledge_point_unmatched
knowledge_point_id
unit_id
reason
```

未匹配不等于应该创建新概念。

候选概念更适合由 `retrieval -> trace -> study` 在真实使用后生成。

### ActivationLink（Precompile 不再创建）

Precompile 只写入 `concept_match_candidates`。`ActivationLink` 由 `Study` 在真实使用证据满足条件后创建（通常为 `status=candidate`）。

`ActivationLink` 连接：

```text
perspective_id
domain_id
concept_id
knowledge_point_id
unit_id
```

它表示：

```text
在当前认知视角下，
当问题处于某类场景、目标、思维模式和注意焦点时，
可以通过该 Domain / Concept 激活这个 KnowledgePoint 和所在 KnowledgeUnit。
```

它不表示：

```text
Concept 是 KnowledgePoint 的上位概念；
KnowledgePoint 支持 Concept；
KnowledgePoint 导致 Concept；
KnowledgePoint 依赖 Concept。
```

字段建议：

```text
id
perspective_id
domain_id
concept_id
knowledge_point_id
unit_id
scene_tags
goal_tags
pattern_tags
focus_tags
relevance_summary
boundary
link_type
confidence
match_reason
status
activation_count
used_count
positive_feedback_count
negative_feedback_count
created_at
updated_at
```

第一版实现可以先把 `scene_tags / goal_tags / pattern_tags / focus_tags` 作为可选 JSON 字段或预留字段。

生成规则：

```text
ConceptMatcher 负责判断 Domain / Concept 是否匹配；
ActivationLinkBuilder 负责把匹配结果转成带使用条件的激活路径；
如果 KnowledgePoint 或 Concept 提供了明确场景、目标、模式、焦点线索，应写入对应 tags；
如果无法可靠判断，tags 可以为空，但必须保留 relevance_summary 和 match_reason；
空 tags 表示尚未学到稳定适用条件，不表示该链接适用于所有场景。
```

初始使用条件的生成方法：

```text
1. 从 KnowledgePoint.text 提取它直接描述的问题对象、动作、判断、指标和约束；
2. 从 KnowledgeUnit.title / center / outline_path 判断材料所在场景；
3. 从 candidate Domain / Concept 的 description、aliases、boundary 判断认知入口；
4. 推导 scene_tags：该知识通常在哪类问题场景下被激活；
5. 推导 goal_tags：该知识主要服务解释、判断、排障、设计、查证、总结等哪类目标；
6. 推导 pattern_tags：该知识更适合快速检索、工作模型、查证、经验路径或冲突检测中的哪类模式；
7. 推导 focus_tags：该知识回答时应该关注的风险、成本、性能、边界、证据、步骤等焦点；
8. 生成 relevance_summary；
9. 生成 boundary，说明不应激活该链接的条件。
```

这些条件来自材料和候选概念本身。

precompile 不应使用单次用户问题来证明这些条件稳定。

提示词和输出 schema（与 Domain 联合判定，见上文 `domain_concept_match`）：

```text
prompts/precompile/domain_concept_match.md
schemas/precompile/domain_concept_match.schema.json
ServiceName: precompile.domain_concept_match
```

`ConceptMatcher` 的输出必须包含：

```text
scene_tags
goal_tags
pattern_tags
focus_tags
relevance_summary
boundary
```

`ActivationLinkBuilder` 只负责把这些字段写入 `ActivationLink`。

如果模型输出的 tags 明显违反 Concept boundary，必须丢弃该 match 或降为 candidate。

`relevance_summary` 应说明：

```text
这个 KnowledgePoint 为什么能通过该 Concept 被激活；
它主要支撑哪类问题；
它不适合哪些边界。
```

后续 retrieval 的 `ActivationLinkMatcher` 可以使用这些字段进行 Top-K 链接匹配。

后续 study 可以基于 trace 中的真实 scene / goal / pattern / focus 反馈，补全、收窄或修正这些字段。

`link_type` 第一版：

```text
preset_match
llm_match
manual
candidate
```

`status`：

```text
active
candidate
disabled
discarded
deleted
```

`deleted` 表示该 ActivationLink 因关联 SourceDocument / KnowledgeUnit / KnowledgePoint 被逻辑删除而失效。

幂等规则：

```text
perspective_id + concept_id + knowledge_point_id 唯一；
重复运行时 upsert；
更新 confidence / match_reason / status；
不清零 activation_count / used_count / feedback_count；
不自动删除旧 link。
```

第一版 `retrieval` 默认只把 `active` ActivationLink 作为正式认知结构召回。

`candidate` ActivationLink 只能按 retrieval 文档作为弱激活信号参与验证。

`disabled / discarded / deleted` ActivationLink 不参与召回、评分或 study 晋升。

`candidate` ActivationLink 不是正式认知结构召回路径，但可以作为弱激活信号被 retrieval 验证，并作为后续学习和人工审核输入。

### PrecompileEventStore

负责记录预编译过程中的关键事件。

第一版事件包括：

```text
default_perspective_created
preset_imported
preset_validation_failed
knowledge_point_created
knowledge_point_updated
knowledge_point_unmatched
multi_route_index_updated
activation_link_created
activation_link_updated
```

事件用于排查和后续学习分析，不替代 trace。

`trace` 记录的是一次问题处理中的思考过程。

`precompile_events` 记录的是预编译过程中的结构变化。

## 数据库表

第一版建议表：

```text
perspectives
domains
concepts
knowledge_points
unit_search_index
knowledge_point_search_index
outline_search_index
activation_links
precompile_events
```

### perspectives

```text
id
name
description
status
created_at
updated_at
```

### domains

```text
id
perspective_id
name
description
status
source
preset_version
activation_count
used_count
positive_feedback_count
negative_feedback_count
last_used_at
created_at
updated_at
```

### concepts

```text
id
perspective_id
domain_id
name
description
aliases
boundary
status
source
preset_version
activation_count
used_count
positive_feedback_count
negative_feedback_count
last_used_at
created_at
updated_at
```

`aliases` 第一版可以用 JSON TEXT 保存。

### knowledge_points

```text
id
unit_id
text
normalized_text
point_type
evidence_text
source_spans
confidence
status
created_at
updated_at
```

`status`：

```text
active
disabled
discarded
deleted
```

只有 `status = active` 的 KnowledgePoint 可以进入知识点检索、ConceptMatcher 和 ActivationLinkBuilder。

当关联 KnowledgeUnit 或 SourceDocument 进入 `disabled / discarded / deleted` 时，KnowledgePoint 必须同步进入相同可用性状态。

### source_search_index

```text
id
source_document_id
title
description
top_outline_summary
status
updated_at
```

`source_search_index` 是文档级树搜索的入口索引。

retrieval 在没有显式 `source_filters` 时，应先通过它选出候选 SourceDocument，再进入该文档内的 `outline_search_index`。

### unit_search_index

```text
unit_id
source_document_id
title
center
content
source_spans
status
updated_at
```

### knowledge_point_search_index

```text
knowledge_point_id
unit_id
source_document_id
text
point_type
source_spans
status
updated_at
```

### outline_search_index

```text
id
unit_id
source_document_id
outline_path
outline_level
outline_summary
source_spans
status
updated_at
```

### activation_links

```text
id
perspective_id
domain_id
concept_id
unit_id
knowledge_point_id
link_type
confidence
match_reason
status
activation_count
used_count
positive_feedback_count
negative_feedback_count
created_at
updated_at
```

状态级联规则：

```text
SourceDocument / KnowledgeUnit / KnowledgePoint disabled
  -> ActivationLink.status = disabled

SourceDocument / KnowledgeUnit / KnowledgePoint discarded
  -> ActivationLink.status = discarded

SourceDocument / KnowledgeUnit / KnowledgePoint deleted
  -> ActivationLink.status = deleted
```

状态级联必须幂等，并写入 `precompile_events`、source delete event 或结构化日志。

状态级联不能物理删除 ActivationLink，因为历史 trace / study_events 可能仍引用 link id。

### precompile_events

```text
id
perspective_id
event_type
domain_id
concept_id
unit_id
knowledge_point_id
activation_link_id
reason
details
created_at
```

`details` 第一版可以用 JSON TEXT。

## 和 retrieval 的契约

`precompile` 向 `retrieval` 提供两类入口。

### 认知结构检索入口

```text
perspective_id
active domains
active concepts
active activation_links
knowledge_points
knowledge_units
```

检索路径：

```text
Domain / Concept
  -> ActivationLink
  -> KnowledgePoint
  -> KnowledgeUnit
```

### 多路召回入口

```text
unit_search_index
knowledge_point_search_index
outline_search_index
unit.title
unit.center
unit.outline_paths
```

召回路线：

```text
unit full-text retrieval
knowledge point retrieval
outline tree retrieval（document route match + outline tree match）
outline_path_search（词法回退）
```

`retrieval` 负责执行召回、融合和重排。

`precompile` 只负责准备索引素材。

如果某个 `KnowledgePoint` 没有匹配到 `Concept`，但通过多路召回被找到并被最终采用，`retrieval / trace / study` 应将其视为认知结构缺口信号。

## 和 trace / study 的关系

第一版 `precompile` 不直接从一次检索结果中修改正式领域和概念。

`trace` 记录：

```text
问题如何被理解；
哪些 Domain / Concept 被激活；
哪些 ActivationLink 命中；
哪些多路召回结果命中；
哪些结果最终被采用；
是否暴露认知结构缺口。
```

`study` 后续可以基于这些记录更新：

```text
activation_count；
used_count；
feedback_count；
candidate domain；
candidate concept；
candidate activation link。
```

第一版 `precompile` 只负责写入可追溯的 Domain / Concept 静态匹配候选，不写入 active / candidate ActivationLink。后续首次创建、激活、降级或丢弃 ActivationLink 均由 `study / review` 基于累计 trace 执行。

ActivationLink 的激活、降级或丢弃由 `study / review` 基于累计 trace 执行。

`study / review` 固定候选路径前，必须重新确认它处理的是长期稳定问题，而不是当前单次检索问题。

判断输入应来自多次 trace，而不是单次 precompile 输出：

```text
candidate ActivationLink；
多个不同问题产生的 trace；
候选路径被激活次数；
候选路径对应知识被最终采用次数；
正负反馈；
最近一次边界复核结果；
是否存在更合适的 Domain / Concept。
```

### 候选固定的证据去重

候选路径固定前，必须确认计数来自不同问题。

不能通过反复询问同一个问题、同义改写同一个问题，或同一任务中的多次重试，累积出虚假的稳定性。

因此，以下计数必须按 `question_cluster_id` 或等价的问题聚类口径去重：

```text
activation_count；
used_count；
positive_feedback_count；
negative_feedback_count。
```

`question_cluster_id` 表示同一批语义等价或任务目标相同的问题。

例如：

```text
"项目奖金怎么算？"
"项目奖金的计算办法是什么？"
"帮我再问一次项目奖金公式"
```

应视为同一个问题簇，不能为候选固定贡献多份独立证据。

建议晋升 `active` 的前置条件：

```text
distinct_question_cluster_count >= 3
activation_count >= 5
used_count >= 3
positive_feedback_count >= 2
negative_feedback_count = 0
supported_hit_route_count >= 2
最近一次 ConceptMatcher 复核 confidence >= 0.80
concept.boundary 仍然匹配
EvidenceRef 可回链
```

其中：

```text
activation_count / used_count / feedback_count 必须来自去重后的不同问题簇；
同一 question_cluster_id 内多次激活只能作为强度参考，不能重复计入晋升阈值；
最近一次 ConceptMatcher 复核只验证边界一致性，不能替代多问题使用证据。
```

如果缺少问题聚类能力，`study / review` 不应自动晋升 candidate，只能进入人工审核。

固定结果只能作用于 `ActivationLink.status`，不能改写 `KnowledgeUnit` 身份。

## 执行流程

### PrecompileJob 输入

`precompile` 可以由后台增量任务触发。

unit 构建完成后投递的任务结构：

```text
PrecompileJob {
  id
  trigger
  source_document_id
  unit_build_job_id
  knowledge_unit_ids
  status
  idempotency_key
  request_id
  trace_id
  created_at
}
```

`trigger` 可取：

```text
unit_build_completed
manual_reprocess
startup_rebuild
```

第一版主要支持：

```text
unit_build_completed
```

`PrecompileJob` 只能消费：

```text
status = active；
source_spans 可回溯；
不处于 needs_review / disabled / discarded / deleted；
属于 knowledge_unit_ids 输入范围；
```

如果 `knowledge_unit_ids` 为空，job 标记为 `skipped`，不写入索引和静态匹配候选。

### PrecompileJob 输出

执行完成后返回 `PrecompileJobResult`：

```text
PrecompileJobResult {
  success
  status
  data
  error
  warnings
  request_id
  trace_id
}
```

`status` 可取：

```text
succeeded
partial
failed
skipped
retrying
```

`data` 至少包含：

```text
job_id
source_document_id
processed_unit_ids
knowledge_point_ids
unit_search_index_ids
knowledge_point_search_index_ids
outline_search_index_ids
concept_match_candidate_ids
precompile_event_ids
```

`PrecompileJobResult.success = true` 表示材料侧可发现性已经建立，或明确没有可处理单元。

即使 Concept 匹配失败，只要材料侧索引已写入，仍可返回：

```text
success = true
status = partial
warnings 包含 concept_match_failed 或 knowledge_point_unmatched
```

这保证知识仍可通过多路召回被发现，并由后续 `retrieval -> trace -> study` 暴露认知结构缺口。

一次完整预编译流程：

```text
EnsureDefaultPerspective
  -> ImportPresetDomainsAndConcepts
  -> LoadActiveKnowledgeUnits
  -> LoadKnowledgePointsGeneratedWithUnits
  -> BuildMultiRouteIndexes
  -> PrefilterDomainAndConceptCandidates
  -> BatchMatchDomainsAndConceptsByUnit
  -> UpsertConceptMatchCandidates
  -> RecordPrecompileEvents
```

其中：

```text
BuildMultiRouteIndexes 不依赖静态匹配结果；
Domain / Concept 必须在同一次模型调用中联合匹配；
默认每个 KnowledgeUnit 调用一次，不得逐 KnowledgePoint 分别调用 domain_match 和 concept_match；
Concept 匹配失败不影响材料侧索引写入；
静态候选写入失败不应删除已写入的材料侧索引；
precompile 应支持按 source_document_id 或 unit_id 增量执行。
```

## 错误处理

`precompile` 错误必须遵守 `docs/impl/error-handling.md` 的统一错误模型。

### 阶段

第一版阶段：

```text
ensure_perspective
load_preset
validate_preset
import_preset
load_units
generate_knowledge_points
build_multi_route_index
build_domain_candidates
build_concept_candidates
match_concepts
upsert_activation_links
record_events
```

### 错误码

| 错误码 | 含义 | 可重试 | 处理 |
| --- | --- | --- | --- |
| `default_perspective_disabled` | 默认认知视角被禁用 | 否 | 停止写入正式结构 |
| `preset_file_missing` | 预制 JSON 文件不存在 | 否 | 启动失败 |
| `preset_json_invalid` | 预制 JSON 无法解析 | 否 | 启动失败 |
| `preset_validation_failed` | 预制 Domain / Concept 校验失败 | 否 | 启动失败 |
| `preset_import_failed` | 预制结构写入失败 | 是 | 当前 precompile 失败 |
| `unit_missing_required_field` | KnowledgeUnit 缺少必要字段 | 否 | 跳过该 unit，记录事件 |
| `knowledge_point_generation_failed` | 知识点生成失败 | 是 | 标记该 unit 的知识点生成失败，继续处理其他 unit |
| `knowledge_point_output_invalid` | 模型返回知识点结构不合法 | 是 | 重试后仍失败则跳过该 unit |
| `multi_route_index_write_failed` | 材料侧召回索引写入失败 | 是 | 当前 unit 的 precompile 失败 |
| `domain_candidate_build_failed` | 领域候选预筛失败 | 是 | 记录 unmatched 事件，继续处理 |
| `concept_candidate_build_failed` | 概念候选预筛失败 | 是 | 记录 unmatched 事件，继续处理 |
| `concept_match_failed` | LLM 概念匹配失败 | 是 | 记录 unmatched 事件，继续处理 |
| `concept_match_output_invalid` | 概念匹配输出结构不合法 | 是 | 重试后仍失败则记录 unmatched |
| `activation_link_write_failed` | ActivationLink 写入失败 | 是 | 不删除已写入材料侧索引，记录错误 |
| `precompile_event_write_failed` | 事件记录写入失败 | 是 | 不回滚已完成结构，写工程日志 |

### 状态更新

处理原则：

```text
preset 校验失败属于启动级错误；
单个 unit 失败不应导致整批 precompile 完全失败；
KnowledgePoint 生成失败时，不写入该 unit 的 knowledge_point_search_index；
材料侧索引写入失败时，不继续为该 unit 建立 ActivationLink；
Concept 匹配失败时，不影响已经写入的材料侧索引；
ActivationLink 写入失败时，不删除已经写入的材料侧索引；
事件写入失败不能伪造成功事件。
```

### 是否进入 trace

`precompile` 通常是后台知识准备过程，不直接进入一次问题处理的 trace。

但如果某次问题处理触发了按需 precompile，并影响了当前回答，则相关错误应进入 trace。

例如：

```text
按需生成 KnowledgePoint 失败，导致当前问题无法召回相关知识；
材料侧索引不可用，导致 retrieval 降级；
ActivationLink 缺失，导致当前认知结构无法激活知识。
```

普通启动导入、后台增量预编译和事件记录失败只写工程日志和 `precompile_events`。

## 验收标准

第一版完成后，应满足：

```text
系统启动后一定存在 default Perspective；
预制 Domain / Concept 可从 presets/precompile/*.json 幂等导入；
坏的 preset JSON 会导致启动失败；
基于 test/*.md 生成的 active KnowledgeUnit 可以进入 PrecompileJob；
给定 active KnowledgeUnit，可以生成 1-5 个 active KnowledgePoint；
KnowledgePoint 必须回到 KnowledgeUnit 和 source_spans；
重复运行不会生成重复 KnowledgePoint；
active KnowledgeUnit / KnowledgePoint / outline path 会写入材料侧召回索引；
材料侧召回索引重复运行不会重复插入；
KnowledgePoint 只匹配已有 active Domain / Concept；
未匹配 KnowledgePoint 不会创建正式 Concept；
匹配结果只建立可追溯的 Domain / Concept 静态候选，不建立 ActivationLink；
precompile 完成后 ActivationLink 数量可以为 0；
retrieval 冷启动时可以通过材料侧多路召回找回 KnowledgeUnit；
真实使用中命中并采用的 KnowledgePoint 可以由 study 创建 candidate ActivationLink；
retrieval 可以通过多路召回索引找回 KnowledgeUnit；
未被认知结构覆盖但被多路召回采用的知识，可以进入后续认知缺口分析。
```
