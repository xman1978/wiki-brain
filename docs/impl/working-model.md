# 临时认知模型实现方案

本文档描述知识大脑第一版中 `WorkingModelService` 的实现方案。

临时认知模型是本次问题的工作模型。

它不是长期记忆，不是知识图谱，也不是最终回答。

它负责把问题理解、激活的长期记忆、补充查找的证据、缺口、冲突和候选推理路径组织成一个可推理的中间结构，供 `ReasoningService` 和 `ResponseAssemblyService` 使用。

## 第一版定位

第一版只实现请求级临时认知模型。

范围包括：

```text
接收 ReasoningAgent 上游 service 输出；
筛选并归并 used_knowledge；
组织问题变量、约束和回答目标；
把 EvidenceRef 绑定到可使用的事实或知识；
记录认知缺口、知识缺口和冲突；
生成候选推理路径；
计算模型完整度和风险；
输出结构化 WorkingModel；
写入 full trace 的 working_model_summary。
```

第一版不做：

```text
不修改长期认知结构；
不生成正式 Domain / Concept / ActivationLink；
不把外部候选证据提升为内部证据；
不替代 ReasoningService 形成最终推理；
不替代 ResponseAssemblyService 组织最终回答；
不把完整原文塞进模型上下文。
```

## 所属链路

`WorkingModelService` 只在需要完整工作模型的模式中执行。

第一版主要由 `ReasoningAgent` 调用：

```text
ProblemFramingService
  -> CognitiveActivationService
  -> SupplementalRetrievalService
  -> RetrievalFusionService
  -> EvidenceRerankService
  -> EvidenceAggregationService
  -> EvidenceCoverageService
  -> GapDetectionService
  -> EvidenceTracingService
  -> ConflictDetectionService
  -> VerificationService(optional)
  -> WorkingModelService
  -> ReasoningService
  -> ModeSwitchEvaluationService
  -> ResponseAssemblyService
  -> LearningSignalService(optional)
  -> TraceService(full)
```

`RetrievalAgent` 默认不构建完整临时认知模型。

如果检索模式发现证据不能直接回答，并且确实需要多跳关系、跨材料组合、隐含条件推导、复杂权衡或冲突消解，应通过 `mode_switch_request` 切换到 `ReasoningAgent`，再由 `WorkingModelService` 构建模型。问题被分类为判断、比较、归因或方案设计，不足以单独触发切换；证据已明确陈述规则或结论时仍走证据直答短路径。

## 输入

```text
WorkingModelRequest {
  request_id
  trace_id
  query
  perspective_id
  mode
  problem_framing
  cognitive_route_context
  matched_domains
  matched_concepts
  results
  used_knowledge
  evidence_refs
  evidence_summary
  external_evidence_candidates
  cognitive_gaps
  knowledge_gaps
  conflicts
  conflict_cases
  available_context
  answer_intent
  context_budget
}
```

字段说明：

| 字段 | 含义 |
| --- | --- |
| `problem_framing` | 问题类型、用户意图、风险、候选模式和回答形态 |
| `cognitive_route_context` | `ProblemFramingService` 输出的场景、目标、模式、注意焦点、知识相关性和历史反馈摘要 |
| `matched_domains` | 当前认知视角下命中的领域 |
| `matched_concepts` | 当前认知视角下命中的概念 |
| `results` | retrieval 输出给 working model 的结构化知识输入，来自 rerank、证据聚合和覆盖检查后的结果；原始融合候选仅用于 trace，不直接作为事实 |
| `used_knowledge` | 已被上游判断为可进入回答或推理的知识 |
| `evidence_refs` | 可回到来源的内部证据引用 |
| `external_evidence_candidates` | 外部候选证据，第一版默认低可信 |
| `cognitive_gaps` | 当前认知结构暴露出的缺口 |
| `knowledge_gaps` | 当前材料和证据不足以回答的缺口 |
| `conflicts` | 冲突检测结果 |
| `conflict_cases` | 可局部升级或纳入模型处理的冲突实例 |
| `available_context` | 用户提供的上下文或当前会话上下文摘要 |
| `context_budget` | runtime 根据模型配置推算的上下文预算 |

## 输出

```text
WorkingModelResult {
  working_model
  model_summary
  reasoning_inputs
  evidence_requirements
  unresolved_gaps
  unresolved_conflicts
  confidence
  completeness
  risk_flags
  warnings
}
```

`working_model` 是完整结构化模型。

`model_summary` 是写入 trace、传给下游模型步骤或展示调试信息的摘要。

`reasoning_inputs` 是给 `ReasoningService` 的收敛输入，不应包含无关候选或完整原文。

`evidence_requirements`、`unresolved_gaps` 和 `unresolved_conflicts` 是从 `working_model` 中派生出的便捷字段：

```text
evidence_requirements
  来自 affects_answer = true 的 gaps.required_evidence。

unresolved_gaps
  来自 gaps 中 affects_answer = true 或 fallback_handling 仍需补证据的记录。

unresolved_conflicts
  来自 conflicts 中 resolution_status = unresolved 或 deferred 的记录。
```

## WorkingModel 数据结构

建议结构：

```text
WorkingModel {
  id
  request_id
  trace_id
  query
  perspective_id
  mode
  cognitive_route_context
  problem
  answer_goal
  variables
  assumptions
  knowledge_items
  facts
  evidence_map
  external_evidence_candidates
  gaps
  conflicts
  candidate_reasoning_paths
  answer_boundaries
  confidence
  completeness
  risk_flags
  created_at
}
```

约束不作为独立顶层结构维护。

第一版用以下结构表达约束：

```text
WorkingVariable(status=known / assumed)；
WorkingKnowledgeItem(role=constraint)；
WorkingFact(scope_conditions)；
AnswerBoundary(boundary_type=scope / risk / assumption)。
```

### problem

```text
WorkingProblem {
  original_query
  normalized_question
  problem_type
  intent
  risk_level
  required_answer_shape
  need_verification
  need_conflict_handling
}
```

`normalized_question` 只用于本次处理，不替代用户原始问题。

### variables

变量表示本次问题中会影响判断的关键因素。

```text
WorkingVariable {
  variable_id
  name
  description
  value
  source
  required
  status
}
```

`status` 可取：

```text
known
unknown
assumed
conflicting
irrelevant
```

变量可以来自：

```text
用户问题；
available_context；
ProblemFramingService；
cognitive_route_context；
matched_domains / matched_concepts；
used_knowledge；
conflicts；
knowledge_gaps。
```

### knowledge_items

`knowledge_items` 表示进入工作模型的长期记忆或材料侧知识。

```text
WorkingKnowledgeItem {
  item_id
  unit_id
  knowledge_point_id
  source_document_id
  source_result_ids
  matched_domain_ids
  matched_concept_ids
  hit_routes
  role
  summary
  relevance
  confidence
  evidence_ref_ids
  warnings
}
```

`role` 可取：

```text
core
supporting
background
counterpoint
constraint
example
discarded
```

规则：

```text
只有 core / supporting / counterpoint / constraint / example 默认进入 reasoning_inputs；
background 只在上下文预算允许时进入摘要；
discarded 必须说明原因，不进入 reasoning_inputs；
同一个 unit_id 多路命中时只保留一个 WorkingKnowledgeItem，并合并 hit_routes；
没有 EvidenceRef 的知识不能作为 supported_claim 的依据。
```

### facts

`facts` 表示本次模型中可被使用的事实或事实性前提。

```text
WorkingFact {
  fact_id
  statement
  source_type
  evidence_ref_ids
  knowledge_item_ids
  confidence
  status
  scope_conditions
  warnings
}
```

`source_type` 可取：

```text
internal_evidence
user_context
external_candidate
inferred
assumption
```

`status` 可取：

```text
supported
candidate
assumed
conflicting
unsupported
```

规则：

```text
internal_evidence + EvidenceRef 才能生成 supported fact；
user_context 可以生成 assumed 或 supported_by_user fact；
external_candidate 默认只能生成 candidate fact；
inferred fact 必须由 ReasoningService 产生，WorkingModelService 第一版不主动生成强推断事实；
unsupported fact 只能作为缺口或 warning，不能进入确定结论。
```

### evidence_map

`evidence_map` 负责把模型中的知识、事实和后续结论回到来源。

```text
EvidenceMapEntry {
  evidence_ref_id
  source_document_id
  unit_id
  source_span
  supports
  contradicts
  quote_summary
  reliability
}
```

`supports` 和 `contradicts` 指向：

```text
fact_id
knowledge_item_id
conflict_id
candidate_reasoning_path_id
```

第一版不要求复杂引用格式，但必须保留可追溯映射。

`evidence_map` 必须由 `EvidenceMapBuilder` 使用上游 `evidence_refs` 和 `used_knowledge / results` 规则构建。

它不由 `prompts/working_model/build_working_model.md` 直接生成。

模型辅助步骤只能引用已有的 `evidence_ref_id`，不能创建新的证据映射。

### gaps

缺口来自 `GapDetectionService`，由 `WorkingModelService` 组织到模型中。

```text
WorkingGap {
  gap_id
  gap_type
  description
  affects_answer
  required_evidence
  fallback_handling
  related_variable_ids
  related_knowledge_item_ids
}
```

`gap_type` 可取：

```text
cognitive_gap
knowledge_gap
evidence_gap
variable_gap
reasoning_gap
```

规则：

```text
cognitive_gap 不等于当前无法回答；
knowledge_gap 如果影响核心结论，应降低 completeness；
evidence_gap 如果 require_evidence = true，应阻止确定回答；
variable_gap 可以通过 assumptions 暂时处理，但必须进入 answer_boundaries；
reasoning_gap 通常由 ReasoningService 或失败 trace 追加。
```

### conflicts

冲突来自 `ConflictDetectionService`。

```text
WorkingConflict {
  conflict_id
  conflict_type
  conflicting_fact_ids
  conflicting_evidence_ref_ids
  scope_conditions
  affects_core_answer
  resolution_status
  handling_strategy
}
```

`resolution_status` 可取：

```text
resolved
unresolved
scoped
deferred
```

`handling_strategy` 可取：

```text
include_as_uncertainty
split_by_scope
request_verification
local_mode_escalation
whole_mode_switch
stop_with_gap
```

局部冲突不应默认导致整个问题切换模式。

如果冲突影响核心结论且无法在当前模型中处理，应交给 `ModeSwitchEvaluationService` 生成整体切换或停止策略。

### candidate_reasoning_paths

候选推理路径是给 `ReasoningService` 的起点。

```text
CandidateReasoningPath {
  path_id
  path_type
  question
  premise_fact_ids
  knowledge_item_ids
  gap_ids
  conflict_ids
  expected_output
  risk
}
```

`path_type` 可取：

```text
deduction
induction
comparison
tradeoff
causal_explanation
design_synthesis
counterfactual
conflict_resolution
```

第一版可以用规则生成候选路径：

```text
problem_type = compare -> comparison；
problem_type = design / plan -> design_synthesis；
problem_type = why / cause -> causal_explanation；
存在 unresolved conflict -> conflict_resolution；
存在多个方案和约束 -> tradeoff；
用户问如果/假设 -> counterfactual。
```

`WorkingModelService` 不输出最终推理结论。

它只告诉 `ReasoningService` 可以沿哪些路径推理、哪些证据可用、哪些缺口必须保留。

### answer_boundaries

回答边界说明最终回答必须遵守的限制。

```text
AnswerBoundary {
  boundary_id
  boundary_type
  description
  related_gap_ids
  related_conflict_ids
  related_variable_ids
}
```

`boundary_type` 可取：

```text
scope
uncertainty
missing_evidence
time_sensitivity
risk
assumption
source_limit
```

这些边界必须传给 `ResponseAssemblyService`，避免最终回答把不确定内容写成确定结论。

## 构建流程

临时认知模型的构建过程可以理解为：

```text
先梳理相关知识；
再对知识进行分类、定位和证据绑定；
然后识别缺口、冲突、关键变量、约束和假设；
接着形成结构化的临时认知模型；
最后让 ReasoningService 使用这个结构化模型组织提示词并进行推理。
```

`WorkingModelService` 的责任到“形成结构化临时认知模型”为止。

真正的模型推理发生在 `ReasoningService`。

也就是说：

```text
WorkingModelService
  把材料摆好，形成可追踪、可检查、可推理的工作台。

ReasoningService
  基于这个工作台组织推理提示词，让模型在受约束的结构内推理。
```

## 具体实现

第一版应实现为确定性 pipeline，模型只作为局部辅助。

不要让模型一次性生成完整 working model。

原因是完整 working model 涉及证据绑定、缺口记录、冲突保留、上下文预算、trace 和降级策略，这些都需要可测试、可复现的工程逻辑。

建议模块结构：

```text
WorkingModelService
  WorkingModelBuilder
  KnowledgeItemBuilder
  EvidenceMapBuilder
  FactBuilder
  VariableBuilder
  GapProjector
  ConflictProjector
  ReasoningPathBuilder
  ModelQualityScorer
  ReasoningInputCompactor
  WorkingModelTraceWriter
```

主流程：

```text
build(request):
  validate_input
  frame_problem
  build_knowledge_items
  build_evidence_map
  build_facts
  build_gaps
  build_conflicts
  build_variables
  build_assumptions
  build_reasoning_paths
  build_answer_boundaries
  score_model
  compact_for_reasoning
  write_trace_summary
  return WorkingModelResult
```

### 1. validate_input

先检查上游结果是否足以构建模型。

最低可用输入是：

```text
query；
problem_framing；
至少一个 used_knowledge、evidence_ref、available_context、knowledge_gap 或 conflict。
```

如果没有任何可用知识、证据、上下文、缺口或冲突，应返回最小 working model：

```text
problem = 根据 query 和 problem_framing 构建；
knowledge_items = 空；
facts = 空；
gaps = working_model_input_empty 对应的 knowledge_gap；
confidence = insufficient；
completeness = insufficient；
risk_flags 包含 evidence_insufficient。
```

这种情况下不能进入完整确定推理，只能让下游回答“当前证据不足”或请求补充信息。

### 2. frame_problem

把 `ProblemFramingService` 的结果转成 `WorkingProblem` 和 `answer_goal`。

它要明确：

```text
这次问题要回答什么；
用户期待什么回答形态；
是否需要比较、解释、设计、决策或查证；
是否需要保守回答；
是否需要处理冲突；
是否有时效性或高风险。
```

第一版不要重新判断全部问题语义。

它应优先消费 `problem_framing` 已经给出的：

```text
problem_type
intent
risk_level
need_retrieval
need_working_model
need_verification
required_answer_shape
cognitive_route_context.scene
cognitive_route_context.goal
cognitive_route_context.pattern
cognitive_route_context.focus
cognitive_route_context.constraints
```

如果 `problem_framing` 缺字段，可以用规则从 query 中补最小值。

例如：

```text
出现“比较 / 哪个更好 / vs” -> problem_type = compare；
出现“为什么 / 原因” -> problem_type = why；
出现“如何设计 / 方案 / 架构” -> problem_type = design；
出现“是否 / 能不能 / 应不应该” -> problem_type = decision；
出现“如果 / 假设” -> problem_type = counterfactual。
```

补出的字段应标记为低置信来源，不覆盖上游明确判断。

### 3. build_knowledge_items

这一步把 `results` 和 `used_knowledge` 归并成 `WorkingKnowledgeItem`。

目标不是保留所有检索结果，而是把可用知识放到本次问题的位置上。

处理规则：

```text
按 unit_id 聚合；
同一个 unit_id 只生成一个 WorkingKnowledgeItem；
保留来源 result_id 到 source_result_ids；
合并 hit_routes；
合并 matched_domain_ids 和 matched_concept_ids；
保留最高 relevance 和最高 confidence；
保留 evidence_ref_ids；
保留上游 used / retrieved 状态；
记录被丢弃或降级的原因。
```

`used_knowledge` 优先级高于普通 `results`。

`core` 不能仅由融合总分或 `strong_cognitive_hit` 决定。Working Model 必须服从上游 `RetrievalPolicy`、事实角色和 Coverage：

```text
定义问题：core 来自绑定目标主语的 direct definition；
枚举 / 角色问题：core 是按实体去重后的 direct fact 集合，不是单个最高分 Unit；
流程问题：core 覆盖必要阶段；
条件判断问题：core 优先选择 main_rule / condition / constraint / threshold / outcome，例外和背景不能替代主规则；
比较问题：core 必须同时覆盖比较双方和共同维度；
因果 / 关系 / 多跳问题：core 必须构成连通证据链。
```

若高分候选与问题所需事实结构不匹配，它只能作为 supporting、constraint 或 background。若 required slots 尚未覆盖，Working Model 不得把 `completeness` 标为 high，也不得选择与问题类型不匹配的 reasoning pattern。

对于条件判断问题，Working Model 应把主规则和必要条件构造成核心事实链，不能把例外升级路径设为唯一 `core_knowledge_item`。例如“是否继续投入”必须优先包含审批、投入范围、成本红线、风险约束和停止/继续结果；延期未通过后的会议机制只能作为 exception。

对于关系问题，若任一实体或 `relation_or_path` 缺失，Working Model 必须设置 `insufficient_evidence` 边界，不得以共同关键词、共同目录或单侧事实构造关系推理路径。

如果某条 ranked result 没有进入 `used_knowledge`，但它是高分反例、约束或冲突来源，可以保留为：

```text
counterpoint
constraint
background
```

如果它只是低分重复内容，应标记为 `discarded` 或不进入 working model。

角色判断规则：

| 条件 | role |
| --- | --- |
| strong_cognitive_hit 且分数高 | `core` |
| 已被 used_knowledge 采用且有 EvidenceRef | `core` 或 `supporting` |
| 来自补充查找、能支持核心问题 | `supporting` |
| 影响适用条件、边界、前提 | `constraint` |
| 与主要结论方向相反 | `counterpoint` |
| 案例、示例、实践片段 | `example` |
| 相关但不影响回答 | `background` |
| 无证据、低相关、重复或超预算 | `discarded` |

没有 EvidenceRef 的知识可以保留，但不能作为 `supported_claim` 的依据。

它只能进入：

```text
background；
candidate fact；
assumption；
unsupported_area；
gap 的相关线索。
```

### 4. build_evidence_map

这一步把 `EvidenceRef` 绑定到知识、事实和冲突。

处理规则：

```text
遍历 evidence_refs；
找到对应 unit_id / source_document_id / source_span；
关联到使用该证据的 WorkingKnowledgeItem；
如果 evidence_ref 被 ConflictDetectionService 引用，也关联到 WorkingConflict；
生成 quote_summary 或 evidence_summary；
记录 reliability；
记录 supports / contradicts。
```

`EvidenceMapEntry` 不保存完整原文。

它只保存足够回溯的锚点和摘要：

```text
source_document_id；
unit_id；
source_span；
quote_summary；
reliability；
supports；
contradicts。
```

如果某个核心 knowledge item 找不到 EvidenceRef：

```text
保留 knowledge item；
降低 confidence；
生成 warning = working_model_evidence_missing；
对应 fact 不得标记为 supported。
```

### 5. build_facts

这一步把本次可用内容整理成可推理事实。

第一版不要让模型自由编事实。

`WorkingFact` 只能来自以下来源：

```text
内部 EvidenceRef；
用户问题或 available_context；
外部候选证据；
显式假设。
```

内部证据生成规则：

```text
for each evidence_ref:
  找到对应 knowledge_item；
  优先使用 evidence_summary；
  如果没有 evidence_summary，使用 KnowledgeUnit.center / summary；
  生成 WorkingFact(source_type=internal_evidence, status=supported)；
  绑定 evidence_ref_ids 和 knowledge_item_ids。
```

用户上下文生成规则：

```text
for each available_context item:
  如果是用户明确给定条件，生成 WorkingFact(source_type=user_context, status=assumed)；
  如果用户明确声明事实，可标记 supported_by_user，但不能等同内部证据；
  重要上下文必须转成 variable 或 constraint。
```

外部候选证据生成规则：

```text
for each external_evidence_candidate:
  生成 WorkingFact(source_type=external_candidate, status=candidate)；
  默认 confidence 不高于外部证据候选自身 confidence；
  不允许作为 supported fact；
  不允许直接支撑确定结论。
```

假设生成规则：

```text
只有为继续推理必须补齐的前提才能生成 assumption；
每个 assumption 必须绑定 answer_boundary；
每个 assumption 必须能被下游回答明确表达；
不能用 assumption 隐藏 knowledge_gap。
```

### 6. build_gaps

这一步把上游缺口投影到工作模型中。

处理规则：

```text
cognitive_gaps -> WorkingGap(gap_type=cognitive_gap)；
knowledge_gaps -> WorkingGap(gap_type=knowledge_gap 或 evidence_gap)；
unknown required variable -> WorkingGap(gap_type=variable_gap)；
无法形成推理路径 -> WorkingGap(gap_type=reasoning_gap)。
```

关键判断是 `affects_answer`。

如果缺口只说明当前认知结构没有很好激活，但补充查找找到了足够证据，则：

```text
gap_type = cognitive_gap；
affects_answer = false 或 partial；
进入 trace 和 study 输入；
不阻止当前回答。
```

如果缺口影响核心结论，则：

```text
affects_answer = true；
降低 completeness；
生成 AnswerBoundary(missing_evidence)；
必要时让 ModeSwitchEvaluationService 保守退出。
```

### 7. build_conflicts

这一步把 `ConflictDetectionService` 的结果投影为 `WorkingConflict`。

处理规则：

```text
保留 conflict_type；
绑定 conflicting_evidence_ref_ids；
绑定 related knowledge_item_ids；
如果能按适用条件拆开，resolution_status = scoped；
如果不能解决，resolution_status = unresolved；
如果只影响局部结论，handling_strategy = local_mode_escalation 或 include_as_uncertainty；
如果影响核心答案，handling_strategy = whole_mode_switch 或 stop_with_gap。
```

`WorkingModelService` 不负责强行裁决复杂冲突。

它只负责把冲突放到模型里，并明确冲突影响什么。

后续由：

```text
ReasoningService 尝试在结构内解释或拆分冲突；
ModeSwitchEvaluationService 判断是否需要切换模式或保守退出；
ResponseAssemblyService 在回答中表达未解决冲突。
```

### 8. build_variables

变量表示本次问题中会影响判断的关键因素。

第一版优先使用问题类型驱动的规则，不强依赖模型。

规则：

| problem_type | 需要识别的变量 |
| --- | --- |
| `compare` | 比较对象、比较维度、评价标准、适用场景 |
| `design` / `plan` | 目标、约束、资源、风险、成功标准 |
| `why` / `cause` | 现象、候选原因、支持证据、反证 |
| `decision` | 选项、目标、偏好、约束、风险 |
| `how_to` | 当前状态、目标状态、步骤约束、前置条件 |
| `counterfactual` | 假设条件、基线状态、变化因素、影响范围 |

变量来源：

```text
query；
available_context；
problem_framing；
matched_domains / matched_concepts；
core knowledge_items；
supported facts；
conflicts；
knowledge_gaps。
```

如果变量值明确，标记：

```text
status = known
```

如果变量缺失但对回答必要，标记：

```text
status = unknown
required = true
```

并生成：

```text
WorkingGap(gap_type=variable_gap)
AnswerBoundary(boundary_type=missing_evidence 或 assumption)
```

如果变量只能靠假设补齐，标记：

```text
status = assumed
```

并把假设写入 `assumptions` 和 `answer_boundaries`。

### 9. build_assumptions

假设只用于让推理在证据不足但仍可部分回答时继续。

假设不能替代证据。

生成假设的典型场景：

```text
用户问题隐含了一个默认场景；
比较或设计问题缺少轻微上下文；
某个变量对结论有影响，但可以明确条件化表达；
用户要求方案而不是事实确认。
```

不允许生成假设的场景：

```text
高风险事实；
用户要求查证；
核心证据缺失；
存在未解决核心冲突；
假设会改变问题结论。
```

每个假设必须满足：

```text
有 assumption_id；
说明假设内容；
说明为什么需要它；
绑定相关 variable / gap；
生成对应 AnswerBoundary；
传给 ResponseAssemblyService。
```

### 10. build_reasoning_paths

这一步生成给 `ReasoningService` 使用的候选推理路径。

推理路径不是最终推理。

它只是告诉后续模型：

```text
应该沿哪种推理框架处理；
哪些事实是前提；
哪些知识可以使用；
哪些缺口必须保留；
哪些冲突必须处理；
输出应该是什么形态。
```

规则：

| 条件 | path_type |
| --- | --- |
| `problem_type = compare` 且存在多个对象 | `comparison` |
| `problem_type = design` 或 `plan` | `design_synthesis` |
| `problem_type = why` 或 `cause` | `causal_explanation` |
| 存在多个方案和约束 | `tradeoff` |
| 存在 unresolved conflict | `conflict_resolution` |
| query 包含假设条件 | `counterfactual` |
| 有明确规则和前提 | `deduction` |
| 需要从多个案例归纳 | `induction` |

每条路径必须绑定：

```text
premise_fact_ids；
knowledge_item_ids；
gap_ids；
conflict_ids；
expected_output；
risk。
```

如果无法生成任何推理路径，应生成：

```text
WorkingGap(gap_type=reasoning_gap)
completeness = insufficient
risk_flags 包含 evidence_insufficient
```

### 11. build_answer_boundaries

回答边界用于约束最终回答。

它来自：

```text
unknown required variables；
assumptions；
answer-affecting gaps；
unresolved conflicts；
external_candidate_only；
time_sensitive；
high_user_risk；
context_truncated。
```

生成规则：

```text
缺少核心证据 -> missing_evidence；
存在假设 -> assumption；
存在未解决冲突 -> uncertainty；
问题有时效性 -> time_sensitivity；
来源只覆盖部分场景 -> scope；
用户风险高 -> risk；
上下文被裁剪 -> source_limit 或 uncertainty。
```

`ResponseAssemblyService` 必须消费这些边界。

最终回答不能绕过 answer_boundaries 输出确定结论。

### 12. score_model

`confidence` 和 `completeness` 可以先用规则评分。

建议内部使用 0 到 1 的分数，再映射为等级。

`confidence_score` 初始为 1.0。

扣分规则：

| 条件 | 扣分 |
| --- | --- |
| 核心 fact 无 EvidenceRef | 0.25 |
| 存在 unresolved core conflict | 0.25 |
| 外部候选证据用于关键判断 | 0.20 |
| 证据与问题作用域不完全匹配 | 0.15 |
| 来源可靠性低 | 0.10 |
| 上下文被裁剪且影响核心内容 | 0.10 |

`completeness_score` 初始为 1.0。

扣分规则：

| 条件 | 扣分 |
| --- | --- |
| required variable unknown | 0.20 |
| answer-affecting knowledge_gap | 0.25 |
| cognitive activation 完全失败 | 0.10 |
| 证据只覆盖问题一部分 | 0.20 |
| 比较问题只有单侧证据 | 0.25 |
| 无法生成候选推理路径 | 0.30 |

等级映射：

```text
score >= 0.80 -> high
score >= 0.55 -> medium
score >= 0.30 -> low
score < 0.30 -> insufficient
```

如果 `completeness = insufficient`，`ReasoningService` 不应输出完整确定结论。

### 13. compact_for_reasoning

这一步生成 `reasoning_inputs`。

`reasoning_inputs` 是给 `ReasoningService` 和推理提示词的收敛输入。

它不是完整 working model，也不是原始检索结果。

应包含：

```text
problem；
answer_goal；
known required variables；
unknown required variables；
assumptions；
core / supporting / counterpoint / constraint knowledge summaries；
supported / candidate / assumed / conflicting facts；
unresolved gaps；
unresolved conflicts；
candidate_reasoning_paths；
answer_boundaries；
confidence；
completeness；
risk_flags。
```

不应包含：

```text
results 全量；
background knowledge 全量；
discarded items；
完整 source span 正文；
外部网页正文；
数据库内部对象；
与本次问题无关的候选。
```

裁剪优先级：

```text
core knowledge；
constraint knowledge；
counterpoint；
supporting evidence；
active conflicts；
answer-affecting gaps；
examples；
background。
```

被裁剪内容必须进入：

```text
model_summary.omitted_items；
warnings；
trace service_call output_summary。
```

### 14. write_trace_summary

`WorkingModelService` 必须为 full trace 输出 `working_model_summary`。

摘要应回答：

```text
本次模型围绕什么问题建立；
核心变量是什么；
用了哪些核心知识；
哪些证据支撑事实；
哪些缺口影响回答；
哪些冲突未解决；
建议了哪些推理路径；
可信度和完整度是多少；
有没有上下文裁剪或模型降级。
```

## 模型调用边界

第一版 `WorkingModelService` 默认优先使用规则和上游结构化结果。

完整 `WorkingModel` 必须由代码组装，不得要求模型一次性生成或回显。实现上分为两层：

```text
确定层（代码构建）
  problem / knowledge_items / facts / evidence_map
  gaps / conflicts / coverage

推理规划层（模型可选生成）
  variables / assumptions
  candidate_reasoning_paths / answer_boundaries
```

模型输出推理规划层时应采用最小 ID 引用契约：变量引用 `source_fact_ids`，推理步骤引用 `input_fact_ids`，回答边界引用 `related_gap_ids` 或 `conflict_ids`。模型只能引用请求中提供的 ID，不能复制或改写事实，不能创建 EvidenceRef，也不能决定长期知识关系。

代码完成 ID 白名单、引用完整性和枚举校验后，把有效规划合并进确定层。单个变量、步骤或边界引用非法时，只丢弃对应项并记录 warning；存在部分有效规划时返回 `partial`；全部无效或模型调用失败时，保留确定层并使用规则生成的最小推理路径降级。

可以调用模型的场景只包括：

```text
从复杂 query 和 available_context 中抽取变量；
把多个 used_knowledge 摘要归并成问题相关事实；
为复杂问题生成候选推理路径；
压缩 working_model_summary。
```

模型调用必须使用：

```text
prompts/working_model/build_working_model.md
schemas/working_model/build_working_model.schema.json
```

如果第一版尚未实现该 prompt 和 schema，则 `WorkingModelService` 必须走规则降级路径。

模型输入只能包含：

```text
query；
problem_framing 摘要；
matched domain / concept 名称和 id；
used_knowledge 摘要；
evidence_summary；
gap 摘要；
conflict 摘要；
available_context 摘要。
```

模型输入不能包含：

```text
完整原文；
未筛选的 results 全量内容；
数据库内部对象；
没有证据锚点的长文本；
外部网页正文全文。
```

模型输出必须经过 JSON Schema 校验。

校验失败时：

```text
不使用模型输出；
记录 warning = working_model_llm_output_invalid；
回退到规则构建；
trace 记录模型调用失败或降级原因。
```

## 上下文预算

`WorkingModelService` 必须遵守 runtime 的模型上下文预算。

候选内容进入模型或 `reasoning_inputs` 前应按优先级裁剪：

```text
core knowledge；
constraint knowledge；
counterpoint；
supporting evidence；
active conflicts；
answer-affecting gaps；
examples；
background。
```

裁剪规则：

```text
优先保留有 EvidenceRef 的内容；
优先保留影响核心问题的变量、缺口和冲突；
相同 unit_id 只保留一次；
同一来源的重复证据合并为摘要；
被裁剪内容必须记录在 warnings 或 model_summary.omitted_items 中。
```

## confidence 和 completeness

`confidence` 表示当前模型支持推理的可信程度。

`completeness` 表示当前模型覆盖问题所需变量、证据和冲突的完整程度。

第一版可以使用规则计算。

降低 `confidence` 的情况：

```text
核心事实缺少 EvidenceRef；
外部候选证据被用于关键判断；
存在 unresolved conflict；
来源可靠性低；
证据与问题作用域不完全匹配。
```

降低 `completeness` 的情况：

```text
核心变量 unknown；
knowledge_gap affects_answer = true；
cognitive activation 完全失败；
证据只覆盖问题的一部分；
用户要求比较但只有单侧证据。
```

建议取值：

```text
high
medium
low
insufficient
```

当 `completeness = insufficient` 时，`ReasoningService` 不应输出完整确定结论。

## risk_flags

`risk_flags` 用于提示下游回答需要保守。

可取：

```text
evidence_insufficient
external_candidate_only
unresolved_conflict
high_user_risk
time_sensitive
scope_unclear
assumption_required
context_truncated
```

`ResponseAssemblyService` 必须把这些风险转换为回答边界、缺口说明或不确定性说明。

## 和 ReasoningService 的边界

`WorkingModelService` 负责组织可推理结构。

`ReasoningService` 负责基于该结构形成推理结果。

边界：

```text
WorkingModelService 可以生成候选推理路径；
WorkingModelService 不生成最终结论；
WorkingModelService 可以标记证据支持或反对；
WorkingModelService 不裁决复杂冲突；
WorkingModelService 可以提出缺口和边界；
WorkingModelService 不把缺口改写成答案。
```

`ReasoningService` 输入应优先使用：

```text
working_model.problem
working_model.variables
working_model.knowledge_items(core/supporting/counterpoint/constraint)
working_model.facts(supported/candidate/assumed/conflicting)
working_model.gaps
working_model.conflicts
working_model.candidate_reasoning_paths
working_model.answer_boundaries
```

## 结构化知识如何进入推理提示词

推理提示词不应直接由原始检索结果拼接而成。

它应由 `ReasoningPromptBuilder` 根据 `WorkingModel` 和 `reasoning_inputs` 组织。

也就是说：

```text
WorkingModel 决定模型能看什么；
ReasoningPatternPlan 决定需要哪些证据槽位和推理框架；
candidate_reasoning_paths 决定模型沿哪些路线推理；
gaps / conflicts / answer_boundaries 决定模型必须保留哪些不确定性；
EvidenceRef 决定哪些内容可以作为证据支持。
```

建议实现：

```text
ReasoningPromptBuilder
  输入 WorkingModelResult.reasoning_inputs
  使用上游已选择的 ReasoningPatternPlan，不在回答阶段重新选择模板
  将 facts / knowledge / evidence 按 evidence_slots 绑定到模板
  将 gaps / conflicts / slot_status / completeness 填入模板
  加入推理规则和禁止项
  要求模型输出结构化 ReasoningResult
```

第一版默认使用：

```text
prompts/reasoning/reason_from_working_model.md
schemas/reasoning/reason_from_working_model.schema.json
```

不同问题可以使用主模板和辅助模板组成的推理计划。

例如：

| pattern_id | 推理提示词组织方式 |
| --- | --- |
| `direct_answer` | 按查询对象、直接事实、来源和回答边界组织 |
| `rule_decision` | 按规则、当前状态、阈值、例外、审批结果和决策权限组织 |
| `causal_analysis` | 按现象、候选原因、机制、支持证据、反证、影响和不确定性组织 |
| `diagnostic` | 按症状、基线、候选故障、区分性检查、检查结果和修复验证组织 |
| `tradeoff` | 按比较对象、评价标准、约束、收益、成本、风险和适用条件组织 |
| `planning` | 按目标、现状、约束、依赖、里程碑、风险和验收条件组织 |

模板定义至少包含：

```yaml
pattern_id: rule_decision
applicable_question_types: [decision, judgment]
evidence_slots: []
reasoning_steps: []
decision_policy: {}
output_contract: {}
```

每个 `evidence_slot` 还应定义 `required`、`critical`、`accepted_fact_types`、实体与时间绑定要求、权威性和时效性要求，以及缺失时的行为。槽位状态可取 `covered`、`conflicting`、`not_retrieved`、`not_available` 和 `requires_external_verification`。

提示词必须显式区分：

```text
有 EvidenceRef 支持的事实；
来自用户上下文的假设；
外部候选证据；
模型可以推断的部分；
当前不能确定的部分；
未解决冲突；
回答边界。
```

提示词必须禁止：

```text
把 candidate fact 写成 supported fact；
把 assumption 写成事实；
用推理补齐缺失证据；
忽略 unresolved conflict；
输出没有绑定 evidence / inference / gap 的关键结论；
绕过 answer_boundaries 给确定回答。
```

`ReasoningResult` 至少应包含：

```text
reasoning_steps
supported_conclusions
inferred_conclusions
uncertainty_notes
conflict_handling
gap_handling
claim_refs
inference_refs
unsupported_claims
warnings
```

其中：

```text
supported_conclusions 必须引用 fact_id 或 evidence_ref_id；
inferred_conclusions 必须引用 reasoning_step 和 premise_fact_ids；
uncertainty_notes 必须引用 gap_id、conflict_id 或 answer_boundary_id；
unsupported_claims 必须进入 ResponseAssemblyService 的边界说明，不能伪装成结论。
```

## 和 Trace 的关系

full trace 必须记录：

```text
working_model_summary；
使用了哪些 knowledge_items；
哪些 EvidenceRef 支撑 facts；
哪些 gaps 影响回答；
哪些 conflicts 未解决；
生成了哪些 candidate_reasoning_paths；
confidence / completeness；
risk_flags；
是否发生模型调用；
是否发生上下文裁剪；
降级原因。
```

light trace 不要求保存完整 working model。

`TraceRecord.working_model_summary` 不应保存完整原文或长证据正文。

建议摘要：

```text
WorkingModelSummary {
  model_id
  problem_type
  answer_goal
  core_variable_names
  core_knowledge_item_ids
  core_fact_ids
  evidence_ref_ids
  gap_ids
  conflict_ids
  candidate_reasoning_path_ids
  confidence
  completeness
  risk_flags
  omitted_items
}
```

## 错误和降级

错误处理必须遵循：

```text
docs/impl/error-handling.md
```

`WorkingModelService` 捕获的关键错误必须转换为 `SystemError`，或写入 `warnings` 并记录到 trace。

禁止：

```text
捕获错误后返回空 working_model；
LLM 输出校验失败后继续使用该输出；
证据缺失时伪造 EvidenceRef；
trace 写入失败时伪造 trace_id；
把所有错误统一包装成 unknown。
```

`SystemError` 字段要求：

```text
module = service
operation = working_model.build
stage = 下表定义的阶段
target = working_model
entity_type = trace 或 request
entity_id = trace_id 或 request_id
request_id = WorkingModelRequest.request_id
trace_id = WorkingModelRequest.trace_id
```

常见错误：

| 错误码 | stage | severity | retryable | 进入 trace | 降级 |
| --- | --- | --- | --- | --- | --- |
| `working_model_input_empty` | `working_model.validate_input` | `warning` | `false` | 是 | 返回最小模型，`confidence/completeness = insufficient` |
| `working_model_evidence_missing` | `working_model.build_evidence_map` | `warning` | `false` | 是 | 保留相关知识为 candidate / unsupported，降低 confidence |
| `working_model_fact_build_failed` | `working_model.build_facts` | `warning` | `false` | 是 | 跳过失败 fact，保留 evidence_map 和 warning |
| `working_model_variable_missing` | `working_model.build_variables` | `warning` | `false` | 是 | 生成 variable_gap 和 answer_boundary |
| `working_model_conflict_unresolved` | `working_model.build_conflicts` | `warning` | `false` | 是 | 生成 unresolved_conflict 和 mode switch 信号 |
| `working_model_context_budget_exceeded` | `working_model.compact_for_reasoning` | `warning` | `false` | 是 | 裁剪 background / example，记录 omitted_items |
| `working_model_llm_call_failed` | `working_model.llm_call` | `warning` | 视 cause_code 而定 | 是 | 回退规则构建 |
| `working_model_llm_output_invalid` | `working_model.validate_llm_output` | `warning` | `false` | 是 | 丢弃模型输出并回退规则构建 |
| `working_model_trace_write_failed` | `working_model.write_trace_summary` | `warning` | `true` | 否 | 返回 working_model，但追加 trace warning，不伪造 trace_id |

阶段定义：

```text
working_model.validate_input
working_model.frame_problem
working_model.build_knowledge_items
working_model.build_evidence_map
working_model.build_facts
working_model.build_gaps
working_model.build_conflicts
working_model.build_variables
working_model.build_assumptions
working_model.build_reasoning_paths
working_model.build_answer_boundaries
working_model.score_model
working_model.compact_for_reasoning
working_model.llm_call
working_model.validate_llm_output
working_model.write_trace_summary
```

下游结果要求：

```text
如果只有 warning 级错误且可降级，仍返回 WorkingModelResult；
如果 input_empty，返回最小 working_model 和 insufficient 状态；
如果 evidence_missing，不能生成 supported fact；
如果 llm_call_failed 或 llm_output_invalid，不能使用模型输出；
如果 context_budget_exceeded，必须返回 omitted_items；
如果 trace_write_failed，不能声称 full trace 已保存。
```

进入 trace 时至少记录：

```text
error_code
module
stage
影响了哪个 working model 构建步骤
系统如何降级
是否影响 confidence / completeness
是否影响 ReasoningService 输入
```

## 验收标准

第一版应满足：

```text
ReasoningAgent 能调用 WorkingModelService 构建结构化 working_model；
working_model 能区分知识、事实、证据、缺口、冲突、变量和候选推理路径；
没有 EvidenceRef 的内容不会被标记为 supported fact；
外部候选证据不会被当作内部确定证据；
模型调用失败时可以规则降级；
上下文超预算时可以裁剪并记录 omitted_items；
full trace 能记录 working_model_summary；
ReasoningService 可以只消费 reasoning_inputs 完成推理；
ResponseAssemblyService 能根据 answer_boundaries 输出带边界的回答；
WorkingModelService 不修改长期认知结构。
```
