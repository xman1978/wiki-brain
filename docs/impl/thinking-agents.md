# Thinking Agents 实现方案

本文档描述知识大脑第一版中思维模式 agent 的实现方案。

Agent 是思维模式的工程落地。

Agent 不直接拥有知识，也不直接实现底层检索、索引查询或数据库访问。

Agent 负责编排 cognitive service，完成某类问题处理方式。

## 第一版范围

第一版先实现两个 agent：

```text
RetrievalAgent
ReasoningAgent
```

设计文档中的七类思维模式在第一版中的落点如下：

| 设计模式 | 第一版落点 |
| --- | --- |
| 直接记忆 | `RetrievalAgent` 的低成本回答路径 |
| 快速检索 | `RetrievalAgent` |
| 经验路径 | `ExperiencePathService(reserved)`，不自动执行 |
| 工作模型 | `ReasoningAgent` |
| 查证 | `VerificationService(optional)` |
| 冲突检测 | `ConflictDetectionService` 和局部升级 |
| 反思学习 | `LearningSignalService`、`TraceService`、后台 `StudyJob` |

这些映射是第一版收窄，不代表设计模式被取消。

推理模板不是新的 Agent 类型。Agent 决定执行深度和 service 编排，模板决定需要哪些证据、如何检查完整性以及允许输出什么类型的结论。

## 第一版定位

第一版重点定义：

```text
agent 输入输出；
service 编排顺序；
错误降级；
trace 深度；
mode_switch_request；
answer / evidence / working_model 的输出契约。
```

## Agent 通用输入

```text
AgentRequest {
  request_id
  trace_id，可为空
  query
  perspective_id
  mode
  source_filters
  outline_filters
  require_evidence
  require_external_evidence
  available_context
  trace_level
}
```

## Agent 通用输出

```text
AgentResult {
  success
  status
  error
  request_id
  trace_id
  answer
  mode
  used_knowledge
  evidence
  external_evidence_candidates
  working_model
  conflicts
  cognitive_gaps
  knowledge_gaps
  claim_refs
  inference_refs
  unsupported_claims
  learning_signals
  warnings
  mode_switch_request
}
```

`status` 可取：

```text
succeeded
partial
failed
mode_switch_requested
insufficient_evidence
```

`error` 必须使用 `docs/impl/error-handling.md` 中的 `SystemError` 字段，或包含同等字段的模块错误结果。

如果 agent 只能降级完成，应返回：

```text
success = true
status = partial
warnings 非空
error = null
```

如果 agent 无法形成可用结构化结果，应返回：

```text
success = false
status = failed
error = SystemError
```

## RetrievalAgent

检索模式适合明确查找、材料定位、证据回溯和低风险事实问题。

它的目标不是完整推理，而是围绕用户问题找到最相关、可追溯、可解释的知识和证据。

编排：

```text
ProblemFramingService
  -> CognitiveActivationService
  -> SupplementalRetrievalService
  -> RetrievalFusionService
  -> EvidenceRerankService(light)
  -> DirectAnswerSufficiency
       -> sufficient: EvidenceAnswerService -> EvidenceValidation
       -> insufficient: ModeSwitchEvaluationService
  -> GapDetectionService(light)
  -> EvidenceTracingService
  -> ConflictDetectionService(light)
  -> VerificationService(optional)
  -> ModeSwitchEvaluationService
  -> ResponseAssemblyService(light)
  -> LearningSignalService(optional)
  -> TraceService(light)
```

特点：

```text
同时使用认知结构检索和多路召回；
重点返回知识单元、知识点、来源证据和简短回答；
只做轻量缺口识别；
只做轻量冲突检测；
需要外部查证时，可以生成低可信外部证据候选；
不构建完整 WorkingModel。
默认不做实体关系化和结构化证据聚合；模型直接基于筛选后的完整证据陈述组织答案。
```

适用场景：

```text
用户明确询问某个概念、规则、定义或材料位置；
用户要求找依据、找出处、列相关内容；
问题可以通过少量已知知识和证据回答；
风险较低，不需要完整分析链路；
系统需要检查当前认知结构是否能激活相关知识。
```

输出重点：

```text
answer
ranked_results
used_knowledge
evidence_refs
conflicts(light)
cognitive_gaps(light)
knowledge_gaps(light)
external_evidence_candidates(optional)
learning_signals(optional)
trace_id
warnings
```

`RetrievalAgent` 可以返回 `mode_switch_request`。

触发切换的典型情况：

```text
检索结果存在影响核心答案的明显冲突；
检索结果不足以直接回答；
答案需要多跳关系、跨材料组合、隐含条件推导或复杂权衡；
用户明确要求展示推理过程；
缺口信号较强，需要进入推理或后续学习。
```

路由不得仅因问题包含“判断”“比较”“原因”等表达而切换。即使是判断或枚举问题，只要证据已经直接陈述答案，`RetrievalAgent` 就应完成证据组织和回答。

如果冲突只影响局部结论，`RetrievalAgent` 不应默认把整个问题切到推理模式。

它应生成 `local_mode_escalation`，只把冲突点交给 `ReasoningAgent` 或后续冲突处理流程。

只有当冲突影响核心问题结论时，才返回整体 `mode_switch_request`。

如果问题需要时效性或外部来源验证，`RetrievalAgent` 可以调用 `VerificationService`。

第一版外部查证只通过 DDG 免费搜索接口获取候选外部证据，可信度默认低，不作为确定结论依据。

## ReasoningAgent

推理模式适合证据不能直接回答、确实需要复杂设计、多跳分析、冲突消解、跨材料比较、归因推导或组织多类证据的问题。

它的目标不是返回更多材料，而是把激活出的长期记忆、补充查找的证据、当前问题变量和缺口组织成本次问题的临时认知模型，再在这个模型上形成推理。

推理模式复用检索模式的证据准备链路，但不能在 rerank 后直接推理。候选证据必须先被聚合为事实、规则、变量、条件和证据绑定，再进行覆盖与冲突检查。`WorkingModelService` 只接收这组经过检查的结构化输入，不能把原始多路召回结果直接视为可推理事实。

编排：

```text
ProblemFramingService
  -> ReasoningPatternService
  -> EvidenceSlotPlanningService
  -> CognitiveActivationService
  -> SupplementalRetrievalService
  -> RetrievalFusionService
  -> SlotCoverageRerankService
  -> GapDetectionService
  -> EvidenceTracingService
  -> ConflictDetectionService
  -> VerificationService(optional)
  -> WorkingModelService
  -> PatternCompletenessService
  -> ReasoningService
  -> ModeSwitchEvaluationService
  -> ResponseAssemblyService
  -> LearningSignalService(optional)
  -> TraceService(full)
```

特点：

```text
两条检索路径都运行；
融合结果不直接等同于最终回答；
先检查证据和结论冲突；
必要时进行低可信外部查证；
先组织临时认知模型；
完整记录认知结构缺口和知识缺口；
把推理结果和证据来源组织成最终回答；
可以生成学习信号，但不执行 study；
为后续 study 提供输入。
```

`PatternCompletenessService` 必须在推理前执行门控：

```text
必需槽位完整且证据有效：正常推理；
非核心槽位缺失：输出条件性结论和缺口；
核心槽位缺失：不输出确定结论；
证据冲突：进入冲突处理，之后返回原模板；
需要当前外部事实：触发 VerificationService 或标记待验证。
```

允许的结论类型为 `definitive`、`conditional`、`framework_only`、`insufficient_evidence` 和 `conflicted`。

适用场景：

```text
用户要求解释为什么、如何设计、如何选择；
问题涉及多个条件、约束、目标或权衡；
需要从多条知识和证据中形成判断；
检索结果之间存在冲突或不确定性；
回答失败后需要复盘问题理解、证据使用或推理路径。
```

输出重点：

```text
answer
working_model
reasoning_path
used_knowledge
evidence_refs
conflicts
cognitive_gaps
knowledge_gaps
external_evidence_candidates(optional)
learning_signals(optional)
learning_suggestions
trace_id
warnings
```

`ReasoningAgent` 不直接修改长期认知结构。

它可以把缺口、有效推理路径、被采用知识和用户反馈写入 trace，由后续 study 判断是否沉淀为候选 Domain、候选 Concept、候选 ActivationLink 或新的经验路径。

`ReasoningAgent` 中的 `ResponseAssemblyService` 必须区分：

```text
有直接证据支持的结论；
由证据和推理路径推出的结论；
当前证据不足或仍有冲突的内容。
```

不能把推断伪装成原文事实。

`LearningSignalService` 在第一版只保存学习信号。

这些信号可以被后续 study 使用，但 `ReasoningAgent` 不直接创建正式 Domain、Concept、ActivationLink 或 ExperiencePath。

## 两种思维模式的关系

检索模式和推理模式不是上下级关系。

它们面对的问题不同：

```text
检索模式回答“相关知识和证据在哪里”；
推理模式回答“基于这些知识和证据，应该如何理解、判断和回答”。
```

推理模式通常会调用检索相关 service，但这不表示所有检索都必须进入推理。

检索模式也可以产生轻量回答，但它不承担完整的临时认知模型组织和复杂推理。

两种模式都必须遵循：

```text
优先使用当前 Perspective；
同时保留认知结构检索和多路召回；
区分 retrieved 和 used；
记录证据和缺口；
记录冲突和局部升级；
不因为单次命中直接修改正式认知结构。
```

## 预留能力

第一版保留但不完整实现：

```text
VerificationService
LearningSignalService
ExperiencePathService
```

`VerificationService` 可用于外部查证。

第一版只允许通过 DDG 免费搜索接口得到低可信外部证据候选。

这些候选可以辅助判断“需要查证”或“证据不足”，但不能替代内部证据。

`LearningSignalService` 只保存学习信号。

它不执行 study，不修改长期认知结构。

`ExperiencePathService` 只保留接口。

经验路径不会在第一版自动生成、自动执行或自动固化。

## 编排规则

Agent 编排 service，而不是编排模型可见 skill。

模型只在 service 内部需要语义判断、匹配、摘要或推理时被调用。

Agent 必须保留每个 service 的结构化输出，供 trace 使用。

Agent 不应编排“让模型生成完整业务对象”的调用。所有模型步骤遵循统一边界：

```text
模型：对稳定 ID 做选择、分类、打分和最小推理规划；
service：校验 ID、局部容错、回填数据并组装完整对象；
agent：编排 service、传播 partial / warning 和模式切换结果。
```

Rerank 应在 Fact 级分批执行；Working Model 的确定层由代码构建，模型只补充推理规划层。Agent 必须区分“整次模型调用失败”和“单条决策无效”，后者不得导致整条链路失败。

Agent 编排不要求所有 service 严格串行。

在依赖关系允许时，Agent 应并行执行检索相关 service。

可并行的典型步骤：

```text
CognitiveActivationService；
unit full-text retrieval；
knowledge point retrieval；
outline tree retrieval（document route match + outline tree match）；
VerificationService(optional)。
```

必须等待上游结果的步骤：

```text
RetrievalFusionService；
EvidenceTracingService；
GapDetectionService；
ConflictDetectionService；
WorkingModelService；
ReasoningService；
ModeSwitchEvaluationService；
ResponseAssemblyService；
TraceService。
```

并行执行必须保证：

```text
共享 request_id / trace_id；
每个 service 独立记录 service_call；
单一路径失败不取消其他路径；
融合阶段保留成功结果并记录失败路线；
不重复调用模型判断同一语义问题。
```

如果某个 service 失败，Agent 应根据错误类型决定：

```text
继续执行其他 service；
降级为轻量回答；
请求切换思维模式；
停止并返回证据不足；
记录错误并写入 trace。
```

## Trace 深度

`RetrievalAgent` 默认使用 light trace：

```text
记录问题理解；
记录 hit_routes；
记录最终采用的知识；
记录主要 warning。
```

`ReasoningAgent` 默认使用 full trace：

```text
记录问题理解；
记录认知结构激活路径；
记录多路召回命中；
记录结果融合和排序；
记录缺口判断；
记录冲突检测和处理结果；
记录 working model；
记录最终推理和回答。
```

## 和 Router 的边界

Router 选择 agent。

Agent 执行思维模式。

Router 不直接调用底层 service，也不生成回答。

Agent 可以返回 `mode_switch_request`，由 Router 决定是否切换到其他 agent。

## 验收标准

第一版应满足：

```text
RetrievalAgent 可以编排检索相关 service 形成轻量回答和证据列表；
ReasoningAgent 可以编排检索、缺口识别、冲突检测、working model 和推理形成复杂回答；
Agent 不直接访问数据库和索引；
Agent 不把认知能力暴露为模型自由调用的 skill；
每次 agent 执行都能形成 trace；
service 失败时有明确降级或模式切换行为。
```
