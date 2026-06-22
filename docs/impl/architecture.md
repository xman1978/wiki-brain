# 知识大脑总体工程设计

本文档描述如何把 `docs/design` 中的知识大脑设计落到工程结构。

设计目标不是实现传统 RAG，也不是实现通用 Agent 平台，而是实现一个面向知识理解、知识使用和知识沉淀的认知能力层。

第一版工程结构按四层理解：

```text
Knowledge Layer
  底层知识、认知结构、来源证据和使用记录

Cognitive Service Layer
  稳定实现认知能力的系统服务

Agent Layer
  承载思维模式，编排多个 cognitive service

Router Layer
  根据问题选择合适 agent
```

## 1. Knowledge Layer

Knowledge Layer 是底层知识体系。

它不是传统 RAG 的 chunk store，也不是单纯知识图谱。

它负责维护材料侧知识、当前认知结构、使用记录，以及长期记忆和来源证据之间的关系。

核心对象包括：

```text
SourceDocument
PreviewHTML
NormalizedMarkdown
KnowledgeUnit
Perspective
Domain
Concept
KnowledgePoint
ActivationLink
PracticePath
Evidence
Trace
LearningSuggestion
LifecycleState
```

它提供基础数据和索引能力：

```text
导入外部材料；
转换 HTML 和 Markdown；
生成知识单元位置记录；
形成可激活的知识点和候选认知线索；
维护材料侧召回索引；
维护认知结构激活索引；
读取证据来源；
保存 trace；
保存学习建议；
管理生命周期。
```

第一版可以使用关系数据库和文件系统。

文件系统保存原始文件、预览 HTML 和规范化 Markdown。

数据库保存知识单元、知识点、索引、认知结构、trace 和运行状态。

图状关系可以先用关系表表达，不需要一开始引入图数据库。

## 2. Cognitive Service Layer

Cognitive Service 是认知能力的工程落地。

认知能力不应该直接封装成模型可见、由模型自由调用的 skill。

原因是这些能力通常涉及：

```text
数据库访问；
索引查询；
状态更新；
证据回链；
错误处理；
trace 输出；
必要时的模型调用。
```

这些能力需要稳定输入输出、可测试、可复现、可降级，不能完全交给模型在上下文中自由决定。

模型提示词只作为 service 内部的局部步骤。

例如：

```text
CognitiveActivationService
  使用当前 Perspective 下的 Domain / Concept 理解问题，并沿 ActivationLink 激活 KnowledgePoint / KnowledgeUnit。
  内部可以调用 prompts/retrieval/cognitive_activation.md。

SupplementalRetrievalService
  通过 Bleve + gse 词法召回，并结合 unit_search_index、knowledge_point_search_index、outline_search_index 做资格过滤；目录结构检索采用两阶段语义路由（document route match + outline tree match）。
  见 docs/impl/fts.md。
  内部可以调用 prompts/retrieval/document_route_match.md 和 prompts/retrieval/outline_tree_match.md。

RetrievalFusionService
  合并认知结构检索和多路召回结果，统一重排，并保留 hit_route。
  第一版默认不调用模型，只消费上游 service 已经产生的语义分数和理由。

GapDetectionService
  判断认知结构是否缺失、激活路径是否缺失、激活结果是否弱，以及补充查找是否补上关键知识。

EvidenceTracingService
  将被采用知识回到 KnowledgeUnit、SourceSpan、NormalizedMarkdown、PreviewHTML 和原始材料。

ConflictDetectionService
  识别证据、结论、适用条件和推理路径之间的冲突；局部 strong_conflict 只生成局部升级，不默认切换整个问题。

VerificationService
  对事实、来源和时效性进行查证；第一版可使用 DDG 免费搜索接口生成低可信外部证据候选。

ProblemFramingService
  判断问题类型、用户意图、风险、是否需要查证，以及候选思维模式。

ExperiencePathService
  预留经验路径匹配接口；第一版不自动执行或沉淀经验路径。

WorkingModelService
  构建或更新本次问题的临时认知模型。详细契约见 docs/impl/working-model.md。

ReasoningService
  基于当前模型、证据、缺口和冲突形成推理结果。

ModeSwitchEvaluationService
  判断当前流程是否继续、局部升级、整体切换或安全退出。

ResponseAssemblyService
  将证据支持结论、推断结论、缺口和冲突组织成最终回答。

LearningSignalService
  从 trace、缺口、冲突、采用结果和反馈中生成学习信号；第一版只保存信号，不实现 study。

TraceService
  记录本次处理过程。

StudyService
  以后台 job 形式从 trace 中生成学习建议或候选认知结构调整；第一版只保存候选，不执行自动晋升。
```

这些 service 是系统内部能力，不等同于 LLM skill。

如果后续需要对外暴露能力，可以通过 HTTP API、MCP 或明确的工具接口暴露 service 的受控入口，而不是把数据库和索引操作直接暴露给模型。

## 3. Agent Layer

Agent 对应设计文档中的思维模式。

Agent 不拥有知识，也不直接写底层检索逻辑。

Agent 负责编排多个 cognitive service，完成某类问题处理方式。

典型 agent 包括：

```text
DirectMemoryAgent
RetrievalAgent
ExperiencePathAgent
ReasoningAgent
VerificationAgent
ConflictAgent
ReflectionLearningAgent
```

第一版重点实现：

```text
RetrievalAgent
ReasoningAgent
```

示例：

```text
RetrievalAgent =
  ProblemFramingService
  + CognitiveActivationService
  + SupplementalRetrievalService
  + RetrievalFusionService
  + GapDetectionService(light)
  + EvidenceTracingService
  + ConflictDetectionService(light)
  + VerificationService(optional)
  + ModeSwitchEvaluationService
  + ResponseAssemblyService(light)
  + LearningSignalService(optional)
  + TraceService(light)
```

```text
ReasoningAgent =
  ProblemFramingService
  + CognitiveActivationService
  + SupplementalRetrievalService
  + RetrievalFusionService
  + GapDetectionService
  + EvidenceTracingService
  + ConflictDetectionService
  + VerificationService(optional)
  + WorkingModelService
  + ReasoningService
  + ModeSwitchEvaluationService
  + ResponseAssemblyService
  + LearningSignalService(optional)
  + TraceService(full)
```

后续模式：

```text
VerificationAgent =
  ProblemFramingService
  + VerificationService
  + SupplementalRetrievalService
  + EvidenceTracingService
  + ConflictDetectionService
  + ReasoningService
  + TraceService
```

```text
ReflectionLearningAgent（后续模式） =
  TraceService(read)
  + LearningSignalService(read)
  + StudyService
  + LifecycleService
```

第一版 `StudyService` 不在用户请求主链路同步执行。

它由后台 `StudyJob` 调用。

Agent 的输出不应该只有回答。

推荐输出包括：

```text
answer
mode
used_knowledge
evidence
working_model
trace_id
learning_suggestions
mode_switch_request
```

## 4. Router Layer

Router 负责认知路由。

它根据问题选择合适 agent。

Router 不负责回答问题，也不承担复杂推理。

输入包括：

```text
user_question
available_context
domain
risk_hints
user_intent
memory_familiarity_signals
```

输出包括：

```text
selected_agent
reason
trace_level
whether_external_evidence_required
```

第一版可以采用规则和 LLM 分类混合实现。

## 5. 层之间的边界

工程实现中需要保持边界清晰。

```text
Knowledge Layer 不做回答，只提供知识、证据、索引和长期记忆状态。

Cognitive Service 完成稳定认知能力，负责数据访问、模型局部调用、错误处理和结果契约。

Agent 不拥有知识，只编排 service 完成一种思维模式。

Router 不回答问题，只选择 agent 或触发模式切换。

Trace 横跨所有 agent，记录实际发生了什么。

Study 第一版不直接改稳定知识，只生成学习建议或候选调整。

Prompt / JSON Schema 只用于 service 内部需要模型参与的局部步骤。

Error Handling 横跨所有层，负责错误分类、错误传播、状态一致性、结构化日志和调试定位。
```

这些边界可以避免系统退化成一个大 prompt、一个普通 RAG，或者一个通用任务执行平台。

## 6. 模型调用最小化原则

### 构建与检索分工

知识构建与在线检索对模型和确定性逻辑的依赖方向相反，必须分开约定：

```text
知识单元切分、语义目录与目录树构建（unit 阶段）
  -> 优先使用大模型做语义理解、摘要与边界判断；
  -> 规则与启发式仅作兜底（模型不可用、解析失败、输出为空或不可信时）。

在线检索（retrieval 阶段）
  -> 优先使用 Bleve BM25 与 SQL 资格过滤做宽召回与排序；
  -> 大模型仅作兜底或精炼（BM25 未命中、BM25 分数低于置信阈值、或候选需语义路由时）。
```

该分工用于：

```text
构建阶段追求切分质量与目录可检索性，值得为离线任务付出模型成本；
检索阶段追求低延迟、低成本与可复现，词法索引应先行；
同一能力不在两个阶段复用同一优先级，避免构建退化成纯规则切分，也避免检索退化成全链路模型路由。
```

### 调用约束

整个系统必须遵循模型调用和模型上下文最小化原则。

原则：

```text
能用规则、索引、数据库统计完成的，不调用模型；
能在上游 service 已经判断的，不在下游重复调用模型；
模型只处理需要语义理解、摘要、匹配、推理的局部步骤；
每次模型调用前必须经过候选预筛；
只传最小必要上下文；
默认传标题、中心句、摘要、路径、结构化字段，不传全文；
必须根据 openai.models.{model_key}.max_input_tokens 计算上下文预算；
长输入必须分段；
模型输出必须结构化，并通过 JSON Schema 校验；
模型调用失败必须有降级路径。
```

该原则用于：

```text
降低成本；
降低延迟；
降低上下文溢出风险；
减少模型误判和不稳定性；
提高可测试性和可复现性；
让确定性逻辑优先发挥作用。
```

各模块的具体约束：

```text
unit（构建：模型优先，规则兜底）
  语义目录、节点摘要、材料维度分析、候选精炼均 LLM-first；
  模型不可用或失败时回退 BuildSemanticOutlinesForNeed、ruleSegmentOutlineSummary、AnalyzeDimensionsRule 等规则路径；
  语义目录生成必须分段，不整篇输入模型；
  知识单元生成在 outline segment 范围内做材料维度分析，再经边界裁决；不按标题章节或固定 token 直接切块。

precompile
  KnowledgePoint 生成只输入单个 KnowledgeUnit；
  Domain / Concept 匹配先规则预筛，再让模型在候选中选择。

retrieval（检索：BM25 优先，模型兜底）
  unit_text_search、knowledge_point_search 默认纯 BM25，不调用模型；
  阶段一 document_route_match：先 BM25 宽召回；最高分达到 BM25ConfidenceThreshold（默认 0.5）时直接采用 BM25 排序；未命中或分数偏低时用 document_route_match 做语义兜底；
  阶段二 outline_tree_match：先在候选文档内做 outline_path_search（Bleve outlines）；BM25 置信时直接采用；未命中或分数偏低时再调用 outline_tree_match，失败仍回退 BM25 结果；
  CognitiveActivation 只让模型从规则预筛后的 Domain / Concept 候选中选择；
  RetrievalFusion 默认不调用模型，各通道分数先归一化再加权融合。

agent
  Agent 编排 service，不让模型自由决定全部流程。

trace
  记录模型调用是否发生、为什么发生、使用了什么摘要上下文。
```

## 7. 横切基础能力

除了四层主体结构，第一版还需要明确几类横切基础能力。

```text
Error Handling
  统一错误对象、错误码、错误阶段、重试标记、错误传播和 API 错误响应。

Structured Logging
  记录工程运行事件，用于调试和观测，不替代 trace。

Trace
  记录认知过程，用于解释回答和后续学习，不替代工程日志。

Storage
  统一管理数据库和文件系统读写，避免业务模块直接散落处理路径和事务。

Prompt / Schema Registry
  管理 service 内部模型步骤使用的 prompt 和 JSON Schema。
```

错误处理的系统级规则见：

```text
docs/impl/error-handling.md
```

每个模块实现文档都应该说明本模块的关键错误阶段、稳定错误码、状态更新规则、是否进入 trace、是否可重试，以及失败时传递给下一阶段的结果。

## 8. 第一版落地范围

第一版不要实现所有 agent 和 service。

建议先完成最小闭环：

```text
source
unit
precompile
retrieval services
ProblemFramingService
WorkingModelService
ReasoningService
TraceService
RetrievalAgent
ReasoningAgent
CognitiveRouter
```

其中 `retrieval services` 至少包括：

```text
CognitiveActivationService
SupplementalRetrievalService
RetrievalFusionService
GapDetectionService
EvidenceTracingService
```

第一版重点支持两种模式：

```text
检索模式；
推理模式。
```

这两个模式可以验证系统是否真的超过传统 RAG：

```text
检索模式验证材料和证据查找能力；
推理模式验证复杂问题组织和推理能力。
```

第一版进入代码实现前，必须优先阅读以下工程入口文档：

```text
docs/impl/implementation-order.md
docs/impl/project-structure.md
docs/impl/schema.md
docs/impl/api.md
docs/impl/cursor.md
```

这些文档不引入新设计，只把本文档和各模块实现文档压缩成可执行的工程顺序、目录结构、数据库迁移、HTTP API 和 Cursor 分步指令。

## 9. 设计能力到版本范围

`docs/design` 描述的是完整知识大脑能力。

第一版只实现最小闭环。

未进入第一版的设计能力统一放到第二版实现，不在第一版中以隐式规则、临时代码或半自动流程实现。

| 设计能力 | 第一版实现 | 第二版实现 |
| --- | --- | --- |
| 文件类外部知识输入 | `source` 支持用户上传文件，保存原始文件、预览 HTML、规范化 Markdown，并进入 `unit` 构建队列 | 支持网页抓取、搜索引擎材料登记、IM 聊天记录、Agent trace 自动沉淀、人工修正和版本管理 |
| 知识单元构建 | `unit` 从 `normalized.md` 构建可追溯 KnowledgeUnit，记录 Markdown 位置和证据回链 | 引入人工审核工作流、更完整的质量评估、Markdown 修正版本和知识版本联动 |
| 初始知识结构 | `unit` 联合生成 KnowledgeUnit / KnowledgePoint；`precompile` 生成多路召回索引和默认 Perspective 下的 Domain / Concept 静态匹配候选；`study` 从真实使用中创建 ActivationLink | 多 Perspective 管理、候选 Domain / Concept 晋升、复杂概念合并、预制框架热更新 |
| 认知结构学习 | `trace` 和 `study` 只保存学习信号、候选记录和 review 输入，不自动修改长期认知结构 | 基于多次 trace、反馈和人工审核执行强化、降权、合并、拆分、晋升和重组 |
| 思维模式选择 | `CognitiveRouter` 只路由到 `RetrievalAgent` 和 `ReasoningAgent` | 独立实现 `DirectMemoryAgent`、`ExperiencePathAgent`、`VerificationAgent`、`ConflictAgent`、`ReflectionLearningAgent` |
| 直接记忆模式 | 作为 `RetrievalAgent` 的轻量路径处理低风险、低不确定性问题 | 独立 agent，支持稳定长期记忆的快速调用、过期检查和轻量 trace |
| 快速检索模式 | `RetrievalAgent` 支持知识单元、知识点、目录路径、来源证据查找 | 更丰富的检索策略、用户指定范围交互、跨来源检索体验优化 |
| 经验路径模式 | `ExperiencePathService` 只保留预留接口、候选和 trace 记录 | 自动匹配已验证 PracticePath，记录路径有效性，并支持路径沉淀和复用 |
| 工作模型模式 | `ReasoningAgent` 调用 `WorkingModelService` 构建临时认知模型并推理 | 支持模型迭代更新、反事实路径、更复杂约束建模和可视化复盘 |
| 查证模式 | `VerificationService` 只生成低可信外部候选证据，可异步执行，不替代内部 EvidenceRef | 独立 `VerificationAgent`，支持多来源查证、来源可信度评估、网页正文抽取和查证闭环 |
| 冲突检测模式 | `ConflictDetectionService` 识别冲突，`ModeSwitchEvaluationService` 区分局部升级和整体切换 | 独立 `ConflictAgent`，主动寻找反证，组织支持/反对证据并沉淀冲突处理结果 |
| 反思学习模式 | `LearningSignalService` 和后台 `StudyJob` 保存候选，不同步反思学习 | 独立 `ReflectionLearningAgent`，分析失败原因、聚合重复缺口并生成可审核学习方案 |
| 外部证据 | 第一版外部搜索证据是异步辅助能力，只作为候选、warning、trace 或后续 source 导入建议 | 外部来源导入闭环、网页内容抽取、来源去重、可信度排序和长期记忆沉淀 |
| 思考追踪 | `TraceService` 区分 light / full，记录 retrieved / used、激活、补充查找、缺口、冲突、回答映射 | 更完整的 trace 查询、对比、复盘、可视化和基于 trace 的自动学习触发 |
| 生命周期 | 只保留 lifecycle 所需字段和 warning，不实现完整 `LifecycleService`，不自动过期、替代或删除知识 | 完整生命周期状态流转，包括 active、candidate、needs_validation、expired、superseded、historical、discarded |
| 对外能力暴露 | 第一版以内部 service、HTTP API 和明确 agent 编排为主，不把数据库和索引直接暴露给模型 | 按需提供受控 MCP / tool / API 入口，暴露稳定能力而不是底层存储操作 |

## 10. 推荐目录结构

概念上的工程目录可以是：

```text
internal/
  platform/
    errors/
    logging/
    storage/
    config/
    prompts/

  knowledge/
    source/
    unit/
    precompile/
    lifecycle/

  services/
    problem_framing/
    retrieval/
    evidence_tracing/
    gap_detection/
    working_model/
    conflict_detection/
    reasoning/
    trace/
    study/
    lifecycle/

  agents/
    direct_memory/
    quick_retrieval/
    experience_path/
    working_model/
    verification/
    conflict/
    reflection_learning/

  router/
    cognitive_router/
```

这只是工程组织建议，不要求第一版完全拆到这个粒度。

第一版可以先保持逻辑分层，等某个 service 或 agent 的边界稳定后再拆成独立模块。

## 11. 总结

工程实现的核心原则是：

```text
知识体系是长期记忆；
service 是认知能力的工程落地；
agent 是思维模式的工程落地；
router 是模式选择；
prompt 是 service 内部的模型步骤；
模型调用和模型上下文必须最小化；
trace 是学习入口；
error handling 是系统可调试和可恢复的基础。
```

知识大脑不是一个大而全的固定流程。

它应该由长期记忆、认知服务、思维模式 agent 和认知路由共同组成，并通过 trace 和 study 持续演化。
