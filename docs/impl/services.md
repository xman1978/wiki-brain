# Cognitive Services 实现方案

本文档描述知识大脑第一版中认知服务层的实现方案。

第一版采用以下原则：

```text
认知能力封装为 service；
思维模式封装为 agent；
agent 负责编排 service；
prompt / JSON Schema 只用于 service 内部需要模型参与的局部步骤。
```

## 为什么不用 Skill 承载认知能力

认知能力通常涉及：

```text
数据库访问；
索引查询；
状态更新；
证据回链；
错误处理；
trace 输出；
必要时的模型调用。
```

这些能力需要稳定输入输出、可测试、可复现、可降级。

它们不适合直接做成由模型在上下文中自由选择和调用的 skill。

如果后续需要给模型或外部 Agent 暴露能力，应通过受控工具接口、HTTP API 或 MCP 暴露 service，而不是暴露内部数据库和索引操作。

## 第一版服务和预留接口

第一版至少实现核心 service，并为后续能力预留接口：

```text
ProblemFramingService
CognitiveActivationService
SupplementalRetrievalService
RetrievalFusionService
EvidenceRerankService
EvidenceAnswerService
GapDetectionService
EvidenceTracingService
ConflictDetectionService
VerificationService(optional)
WorkingModelService
ReasoningService
ModeSwitchEvaluationService
ResponseAssemblyService
LearningSignalService(optional)
ExperiencePathService(reserved)
TraceService
```

### EvidenceAnswerService

负责证据直答短路径中的答案组织。它接收原问题和经过目录结构召回、FTS 等多路召回、融合及轻量 rerank 后的完整事实陈述，不要求上游先把证据拆成实体关系或构建 `WorkingModel`。

模型输出只包含答案内容及其使用的稳定 `fact_ids`。代码负责校验 fact ID 白名单、证据状态和答案条目的事实绑定，并确定性回填 `EvidenceRef`。如果证据不能直接回答、存在影响结论的冲突，或需要多跳和跨材料推导，该 service 返回升级信号，而不是自行模拟复杂推理。

定义、枚举、属性、材料定位、简单流程和证据中明确给出的判断规则应优先使用该 service。学习信号可以在回答完成后异步生成，不得阻塞在线回答。

### ProblemFramingService

负责把用户输入的问题或待处理事项拆成可复用的认知路由上下文。

它是 retrieval / reasoning 在线链路的第一步。

查询上下文字段由 `docs/impl/session.md` 的 `QueryContextBuilder` 在线构造；本节描述 ProblemFraming 的输入契约与输出。

它不检索知识，不修改长期认知结构，也不创建 Domain / Concept / ActivationLink。

输入：

```text
query
available_context
conversation_context
trace_context
feedback_context
perspective_id
mode_hint
user_intent_hint
risk_hint
```

字段构造见 `docs/impl/session.md`；拆解步骤见 `docs/impl/retrieval.md` § ProblemFramingService / 拆解方法。

`conversation_context` 用于处理省略、指代、延续问题和用户修正。

`trace_context` 只能是当前 trace 或最近相关 trace 摘要，不应是全量历史 trace。

`feedback_context` 应来自 study 或离线聚合后的历史反馈摘要。

内部可以调用：

```text
prompts/retrieval/problem_framing.md
schemas/retrieval/problem_framing.schema.json
```

输出：

```text
problem_type
intent
risk_level
require_evidence
require_external_evidence
candidate_modes
selected_mode
need_retrieval
need_working_model
cognitive_route_context
warnings
```

`cognitive_route_context` 至少包含：

```text
scene
goal
pattern
focus
constraints
knowledge_relevance_hints
feedback_context
question_cluster_hint
```

### CognitiveActivationService

负责认知结构检索。

它使用当前 `Perspective` 下的 `Domain / Concept` 理解问题，并结合 `CognitiveRouteContext` 选择最相关的 `ActivationLink`，再激活 `KnowledgePoint / KnowledgeUnit`。

内部可以调用：

```text
prompts/retrieval/cognitive_activation.md
schemas/retrieval/cognitive_activation.schema.json
prompts/retrieval/activation_link_match.md
schemas/retrieval/activation_link_match.schema.json
```

输出：

```text
matched_domains
matched_concepts
domain_relevance
concept_relevance
activation_link_candidates
activation_hits
activation_miss
weak_activation_signals
cognitive_route_context
warnings
```

`CognitiveActivationService` 不应在命中 Concept 后无条件展开概念下全部 ActivationLink。

它必须通过 `ActivationLinkMatcher` 根据 scene / goal / pattern / focus / knowledge_relevance / feedback_context 做 Top-K 过滤。

只有 active ActivationLink 进入 matcher。candidate ActivationLink 不得进入最终候选、融合结果或 used_knowledge；它由 study 使用独立补充召回和证据进行验证。

### SupplementalRetrievalService

负责材料侧多路召回。

它使用：

```text
internal/searchindex.Manager（Bleve + gse 词法召回）
unit_search_index / knowledge_point_search_index / outline_search_index（SQLite 元数据与资格过滤）
outline tree retrieval（两阶段：document route match + outline tree match）
source span lookup
```

词法检索实现见 `docs/impl/fts.md`。

内部可以调用：

```text
prompts/retrieval/document_route_match.md
schemas/retrieval/document_route_match.schema.json
prompts/retrieval/outline_tree_match.md
schemas/retrieval/outline_tree_match.schema.json
```

输出：

```text
supplemental_hits
domain_score
source_score
tree_score
hit_channels
route_warnings
evidence_ready
```

### RetrievalFusionService

负责融合认知结构检索和多路召回结果。

它按 `unit_id` 合并候选，保留所有 `hit_route` 和 `hit_channels`，并计算统一排序。

第一版默认不调用模型。

它消费 `CognitiveActivationService` 和 `SupplementalRetrievalService` 已经产生的语义分数和理由，进行确定性融合。

融合时先在各通道内部归一化（tree / multi-recall / cognitive），再加权合并，并对多通道命中的同一 `unit_id` 增加 `route_agreement_bonus`。

`top_k` 截断在归一化、加权、去重之后执行。

Fusion 本身不调用模型；EvidenceRerankService 在需要事实级语义判断的在线链路中独立执行。候选池大小、rerank 深度、Coverage 和 used selection 必须由 `ProblemFramingService` 产出的 `RetrievalPolicy` 决定，不能对所有问题使用固定 Top-K 和全局 Top-N。

定义型问题优先正文直接定义；枚举和角色问题扩大候选池并按实体保留证据；流程问题按阶段覆盖；条件判断问题优先主规则、条件、红线和结果，再保留例外。完整策略矩阵以 `docs/impl/retrieval.md` 的“问题类型驱动的检索策略”为准。

输出：

```text
ranked_results: FusedRetrievalResult[]
route_summary
fusion_signals
score_breakdown
rerank_warnings
```

`FusedRetrievalResult` 的详细字段以 `docs/impl/retrieval.md` 为准。

### GapDetectionService

负责识别知识缺口和认知结构缺口。

它比较：

```text
认知结构激活结果；
多路召回结果；
融合排序结果；
最终采用结果。
```

输出：

```text
cognitive_gaps
knowledge_gaps
weak_activation_signals
suggested_learning_inputs
```

`GapDetectionService` 不直接修改认知结构。

它只输出缺口记录，进入 trace，再由 study 判断是否形成候选领域、候选概念或候选激活路径。

### EvidenceTracingService

负责把被采用知识回到来源。

输出必须能指向：

```text
KnowledgeUnit
SourceSpan
SourceDocument
NormalizedMarkdown
PreviewHTML
OriginalSource
```

输出：

```text
evidence_refs
evidence_summary
evidence_ready
warnings
```

第一版以 Markdown SourceSpan 作为精确证据锚点。

HTML 只作为预览入口，不要求块级定位。

### ConflictDetectionService

负责识别证据、结论、适用条件和推理路径之间的冲突。

第一版以规则判断为主，模型只作为后续可选能力。

输入：

```text
query
results / ranked_results
used_knowledge
evidence_refs
matched_domains
matched_concepts
cognitive_gaps
knowledge_gaps
```

冲突类型：

```text
fact_conflict
definition_conflict
condition_conflict
time_conflict
source_conflict
reasoning_conflict
scope_difference
unresolved_conflict
```

第一版规则：

```text
同一问题下多个候选结论互斥，标记 fact_conflict；
同一概念在不同证据中定义边界不同，标记 definition_conflict；
结论不同但适用条件不同，优先标记 scope_difference；
新旧材料不一致，标记 time_conflict；
权威来源和非权威来源不一致，标记 source_conflict；
证据本身不冲突但推理结论不一致，标记 reasoning_conflict；
证据不足无法裁决，标记 unresolved_conflict。
```

输出：

```text
conflicts
conflict_cases
strong_conflicts
resolution_suggestions
warnings
```

`strong_conflict` 不应默认把整个问题升级到推理模式。

如果冲突只影响局部结论，应生成 `conflict_case`，只把冲突点交给推理模式处理。

只有当冲突影响核心问题结论时，才建议整体切换到 `ReasoningAgent`。

`conflict_case` 至少包含：

```text
conflict_id
conflict_type
conflicting_claims
conflicting_evidence_refs
related_unit_ids
related_knowledge_point_ids
related_domain_ids
related_concept_ids
scope_conditions
source_metadata
why_conflict
```

冲突处理结果可取：

```text
resolved
scope_split
prefer_one_with_reason
unresolved
need_more_evidence
```

### VerificationService

负责对事实、来源和时效性进行查证。

第一版只保留轻量查证能力，不实现完整 `VerificationAgent`。

它可以使用 DDG 免费搜索引擎 API 进行外部查证，但外部搜索结果只能作为低可信候选证据。

外部查证结果不能替代内部 `EvidenceRef`，也不能自动写入长期知识。

输入：

```text
query
claims_to_verify
evidence_refs
conflicts
require_external_evidence
source_filters
```

输出：

```text
verification_results
external_evidence_candidates
verified_claims
unverified_claims
warnings
```

外部证据候选结构：

```text
ExternalEvidenceCandidate {
  title
  url
  snippet
  source_name
  retrieved_at
  search_provider
  search_query
  credibility
  confidence
  warnings
}
```

第一版规则：

```text
search_provider = ddg；
credibility 默认 low；
confidence 默认不高于 0.4；
只保存标题、链接、摘要、检索词和检索时间；
不把搜索摘要当作事实原文；
不把外部候选证据直接转成 KnowledgeUnit；
只有用户明确导入或后续 source 流程处理后，外部内容才可能进入知识单元。
```

如果 DDG 不可用，`VerificationService` 应返回 `external_verification_unavailable` warning，并把相关 claim 标记为 `unverified`。

### WorkingModelService

负责构建或更新本次问题的临时认知模型。

详细实现契约见：

```text
docs/impl/working-model.md
```

它只在需要工作模型的思维模式下启用。

`WorkingModelService` 的目标不是总结检索结果，而是把本次问题组织成可推理、可检查、可追踪的工作结构。

输入输出结构以 `docs/impl/working-model.md` 中的 `WorkingModelRequest`、`WorkingModelResult` 和 `WorkingModel` 为准。

本文件不重复维护字段级契约，避免和详细实现文档发生漂移。

构建规则：

```text
不能把 retrieval results 直接拼接成 working model；
必须区分证据支持、推断、假设和缺口；
必须保留支持证据和反对证据；
必须保留 retrieved 但未 used 的关键候选原因；
必须记录认知结构激活和补充查找各自贡献；
不能把外部候选证据当成已确认事实；
不能因为模型推理补齐缺失证据。
```

更新规则：

```text
新证据推翻原判断时，更新 conflicts 和 answer_boundaries；
发现关键变量缺失时，补充 core_variables 和 knowledge_gaps；
局部冲突不影响核心回答时，保留在 conflicts 并交给 ModeSwitchEvaluationService 判断；
核心冲突影响回答时，应让 ModeSwitchEvaluationService 生成整体切换或保守退出；
证据不足时，必须在 unsupported_areas 和 answer_boundaries 中表达。
```

模型调用：

```text
可以调用模型辅助整理核心变量、假设、推理候选和回答边界；
调用前必须先由上游 service 缩小候选范围；
不得输入整篇原文；
优先输入 title、center、evidence summary、gap、conflict 和来源引用；
输出必须通过 JSON Schema 校验；
模型失败时返回结构化降级结果，并保留已知证据、缺口和 warning。
```

和下游的边界：

```text
WorkingModelService 不生成最终回答；
WorkingModelService 不执行复杂推理结论；
WorkingModelService 不把 claim 绑定到最终回答文本；
ReasoningService 基于 working model 形成推理结果；
ResponseAssemblyService 负责把证据支持结论、推断结论和缺口组织成最终回答。
```

trace 要求：

```text
full trace 必须记录 working_model_summary；
必须记录核心变量、主要证据、主要缺口、主要冲突和回答边界；
light trace 可以只记录 working_model 是否构建、构建失败原因和主要 warning。
```

验收标准：

```text
复杂问题不会绕过 WorkingModelService 直接回答；
working model 至少包含问题框架、核心变量、已知知识、证据集合、缺口、冲突、推理候选和回答边界；
证据、推断、假设和缺口可区分；
外部候选证据不会被当作已确认事实；
working model 可以被 trace 和后续 study 消费；
服务失败时有结构化降级结果。
```

### ReasoningService

负责基于当前结果形成推理结果。

它可以调用模型完成推理组织，但必须基于已提供的知识、证据、缺口、冲突和模式约束。

`ReasoningService` 不负责最终回答排版，也不负责把 claim 绑定到证据。

这些由 `ResponseAssemblyService` 负责。

### ModeSwitchEvaluationService

负责判断当前处理流程是否继续、局部升级、整体切换或安全退出。

输入：

```text
query
current_mode
problem_framing
results / ranked_results
evidence_refs
cognitive_gaps
knowledge_gaps
conflicts
reasoning_result
require_evidence
```

输出：

```text
continue_current_mode
whole_mode_switch
local_mode_escalations
fallback_action
reason
warnings
```

模式切换分两类：

```text
whole_mode_switch
local_mode_escalation
```

`whole_mode_switch` 表示整个问题切换到其他 agent。

`local_mode_escalation` 表示只把局部冲突、局部缺口或局部不确定性升级处理。

兜底规则：

```text
检索结果足够、证据一致、问题简单，继续检索模式；
检索结果有用但不能直接回答，且需要多跳、跨材料组合或隐含推导，整体切到推理模式；
局部 strong_conflict 不影响核心答案，只生成 local_mode_escalation；
strong_conflict 影响核心答案，整体切到推理模式；
证据不足但能说明缺口，返回证据不足并记录 KnowledgeGap；
认知结构未命中但多路召回命中，继续回答并记录 CognitiveGap；
认知结构和多路召回都未命中，返回不知道，不编造；
require_evidence = true 且无证据，不给确定回答；
推理模式仍无法回答，返回部分结论、缺口和所需证据。
```

### ResponseAssemblyService

负责把检索结果、推理结果、证据、冲突和缺口组织成最终回答。

它不负责检索，不负责推理，也不直接修改认知结构。

输入：

```text
query
mode
reasoning_result
used_knowledge
evidence_refs
cognitive_gaps
knowledge_gaps
conflicts
mode_switch_decision
answer_style
```

输出：

```text
answer
answer_sections
claim_refs
inference_refs
unsupported_claims
uncertainty_notes
warnings
```

回答中的关键内容应区分：

```text
supported_claim
inferred_claim
unsupported_or_gap
```

规则：

```text
supported_claim 必须绑定 EvidenceRef；
inferred_claim 必须绑定 used_knowledge 和 reasoning_step；
unsupported_or_gap 必须绑定 KnowledgeGap 或 warning；
冲突未解决时，不输出单一确定结论；
证据不足时，明确说明回答范围和缺失证据；
不能把模型推断伪装成原文事实。
```

第一版不要求复杂引用格式，但必须在结构化输出中保留 claim 到 evidence / inference / gap 的映射。

### LearningSignalService

负责从一次处理过程生成学习信号。

第一版不实现 study，因此该 service 只保存学习信号，并为后续 study 预留接口。

输入：

```text
trace_id
query
perspective_id
used_knowledge
cognitive_gaps
knowledge_gaps
conflicts
mode_switch_decision
feedback
```

输出：

```text
learning_signals
warnings
```

学习信号类型：

```text
cognitive_gap_signal
knowledge_gap_signal
activation_success_signal
activation_failure_signal
conflict_signal
useful_evidence_signal
reasoning_path_signal
feedback_signal
```

第一版规则：

```text
只生成和保存信号；
不创建正式 Domain / Concept；
不更新 ActivationLink；
不修改 KnowledgeUnit；
不执行 study 的晋升、合并、淘汰或修正流程。
```

学习信号应至少包含：

```text
signal_id
trace_id
signal_type
perspective_id
related_domain_ids
related_concept_ids
related_unit_ids
related_knowledge_point_ids
reason
confidence
status
created_at
```

`status` 第一版可取：

```text
pending
ignored
consumed
```

### ExperiencePathService

负责经验路径能力的接口预留。

经验路径是后续重要能力，但不进入第一版实现范围。

第一版只定义查询、候选匹配和 trace 记录接口，不实际沉淀或执行复杂经验路径。

输入：

```text
query
perspective_id
problem_type
matched_domains
matched_concepts
```

输出：

```text
matched_experience_paths
path_suggestions
warnings
```

经验路径候选结构：

```text
ExperiencePathCandidate {
  path_id
  name
  description
  applicable_domains
  applicable_concepts
  trigger_conditions
  steps_summary
  confidence
  status
}
```

第一版规则：

```text
可以返回空结果；
可以记录“未来可沉淀为经验路径”的建议；
不执行经验路径自动编排；
不把单次成功处理直接固化为经验路径；
后续由 study / review 决定是否创建或更新经验路径。
```

### TraceService

负责记录本次问题处理过程。

TraceService 不等同于工程日志。

它记录认知过程：

```text
问题理解；
服务调用；
认知结构激活；
补充查找；
结果融合；
采用结果；
缺口判断；
冲突检测；
外部查证；
模式评估；
回答组织；
学习信号；
降级和失败；
最终回答。
```

## Service 通用约定

每个 service 应定义：

```text
输入结构；
输出结构；
使用哪些 Knowledge Layer 对象；
是否调用模型；
使用哪个 prompt / schema；
是否写 trace；
错误码和降级策略；
是否可重试；
失败时给 agent 返回什么。
```

如果 service 需要调用模型，必须同时说明：

```text
为什么规则或索引不能完成；
调用前如何预筛候选；
传入模型的最小上下文是什么；
如何计算上下文预算；
输出使用哪个 JSON Schema 校验；
失败时如何降级；
是否把模型判断结果传给下游 service 复用。
```

下游 service 不应重复调用模型判断已经由上游 service 判断过的内容。

Service 不应该直接决定完整问题处理流程。

完整流程由 agent 根据思维模式编排。

## 和 Agent 的契约

Agent 调用 service，而不是让模型自由调用 service。

第一版：

```text
RetrievalAgent
  -> ProblemFramingService
  -> CognitiveActivationService
  -> SupplementalRetrievalService
  -> RetrievalFusionService
  -> GapDetectionService(light)
  -> EvidenceTracingService
  -> ConflictDetectionService(light)
  -> VerificationService(optional)
  -> ModeSwitchEvaluationService
  -> ResponseAssemblyService(light)
  -> LearningSignalService(optional)
  -> TraceService(light)
```

```text
ReasoningAgent
  -> ProblemFramingService
  -> CognitiveActivationService
  -> SupplementalRetrievalService
  -> RetrievalFusionService
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

## 验收标准

第一版应满足：

```text
认知能力以 service 形式实现；
agent 编排 service，而不是编排模型可见 skill；
service 内部需要模型参与时，必须使用明确 prompt 和 JSON Schema；
service 输出稳定结构，便于 trace、测试和错误处理；
检索相关 service 能覆盖认知结构检索、多路召回、结果融合、缺口识别、证据回溯和冲突检测；
外部查证可以通过 DDG 生成低可信候选证据；
回答组织能区分证据支持、推断和证据缺口；
模式评估能区分整体切换和局部升级；
学习信号和经验路径只保留接口，不进入第一版 study 实现。
```
