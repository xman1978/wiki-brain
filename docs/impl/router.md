# Router 实现方案

本文档描述知识大脑第一版中 `CognitiveRouter` 的实现方案。

Router 负责根据用户问题、上下文、风险、证据要求和知识熟悉度选择合适的思维模式 agent。

Router 不回答问题，不直接检索知识，也不生成最终回答。

## 第一版范围

第一版只在两个 agent 之间路由：

```text
RetrievalAgent
ReasoningAgent
```

对应思维模式：

```text
检索模式；
推理模式。
```

后续可扩展模式：

```text
DirectMemoryAgent
ExperiencePathAgent
VerificationAgent
ConflictAgent
ReflectionLearningAgent
```

第一版 router 必须为后续多模式扩展保留统一路由结构，而不是把规则写死成两个 if/else。

## 设计模式到第一版能力的映射

设计文档中的思维模式多于第一版实际 agent 数量。

第一版不删除这些模式，而是把它们映射到两个 agent 和若干局部 service 能力中。

| 设计模式 | 第一版承载方式 | 说明 |
| --- | --- | --- |
| 直接记忆模式 | `RetrievalAgent` 的轻量路径 | 只在低风险、低不确定性、已有稳定证据时返回简短回答；第一版不单独实现 `DirectMemoryAgent` |
| 快速检索模式 | `RetrievalAgent` | 查找来源、知识单元、知识点和证据位置 |
| 经验路径模式 | `ExperiencePathService(reserved)` + `ReasoningAgent` | 第一版只保留候选和 trace 记录，不自动执行或固化经验路径 |
| 工作模型模式 | `ReasoningAgent` | 构建 working model，组织证据、缺口、冲突和推理路径 |
| 查证模式 | `VerificationService(optional)` | 第一版只生成低可信外部候选证据，不单独实现 `VerificationAgent` |
| 冲突检测模式 | `ConflictDetectionService` + `ModeSwitchEvaluationService` | 局部冲突优先生成 `local_mode_escalation`，核心冲突可整体切到 `ReasoningAgent` |
| 反思学习模式 | `LearningSignalService` + `StudyJob` | 第一版只保存学习信号和候选，不同步执行完整反思学习 agent |

因此，第一版 `mode` 只暴露：

```text
retrieval
reasoning
```

其他设计模式作为后续 agent 扩展点或当前 agent 内部的局部能力存在。

## 输入

```text
RouterRequest {
  query
  perspective_id
  available_context
  user_intent_hint
  source_filters
  outline_filters
  require_evidence
  require_external_evidence
  risk_hint
  trace_level_hint
}
```

字段含义：

| 字段 | 含义 |
| --- | --- |
| `query` | 用户问题 |
| `perspective_id` | 当前认知视角，可为空 |
| `available_context` | 当前对话或上游已知上下文摘要 |
| `user_intent_hint` | 用户显式意图，例如查找、解释、比较、设计 |
| `source_filters` | 来源范围过滤 |
| `outline_filters` | 目录路径过滤 |
| `require_evidence` | 是否必须返回内部证据 |
| `require_external_evidence` | 是否需要外部候选证据 |
| `risk_hint` | 外部传入的风险提示 |
| `trace_level_hint` | 外部传入的 trace 深度提示 |

## 输出

```text
RouterDecision {
  mode
  agent
  trace_level
  require_evidence
  require_external_evidence
  reason
  confidence
  fallback_agent
  warnings
}
```

`mode` 第一版可取：

```text
retrieval
reasoning
```

`agent` 第一版可取：

```text
RetrievalAgent
ReasoningAgent
```

## 路由方式

第一版采用规则优先、模型辅助的混合路由。

默认流程：

```text
RouterRequest
  -> rule classifier
  -> confidence check
  -> optional LLM classifier
  -> RouterDecision
  -> AgentRequest
```

规则能高置信判断时，不调用模型。

只有在规则判断不确定、用户意图含糊或风险边界不清时，才允许调用模型辅助分类。

模型只输出结构化路由建议，不直接回答问题。

## 路由特征

Router 至少考虑：

```text
用户意图；
问题类型；
问题复杂度；
是否要求证据；
是否需要外部查证；
是否涉及高风险或时效性；
是否需要比较、判断、归因或设计；
是否需要完整临时认知模型；
是否是明确材料查找。
```

问题类型可粗分：

```text
lookup
definition
source_location
explanation
comparison
design
decision
verification
conflict
reflection
```

Router 只选择初始 Agent，不负责把问题类型直接映射为单个推理 Prompt。`ProblemFramingService` 应在 Router 之后输出：

```text
ReasoningPatternPlan {
  primary_pattern
  supporting_patterns
  confidence
  selection_reason
}
```

第一版模板包括 `direct_answer`、`rule_decision`、`causal_analysis`、`diagnostic`、`tradeoff` 和 `planning`。一个请求允许主模板和辅助模板组合。模板选择置信度不足时可以由模型给出结构化建议，但模型不得直接填充证据槽位或形成结论。

## 路由规则

进入 `RetrievalAgent` 的典型规则：

```text
用户明确要求查找、出处、位置、引用；
问题可以通过少量知识单元或证据回答；
问题风险较低；
不需要复杂比较、判断或方案设计；
不需要完整 working model。
```

进入 `ReasoningAgent` 的典型规则：

```text
用户要求解释原因、比较方案、设计系统、做判断；
问题涉及多个条件、目标、约束或权衡；
需要组织多条知识和证据；
存在明显不确定性或冲突；
需要完整 working model；
检索模式返回 whole_mode_switch。
```

外部证据规则：

```text
用户要求当前、最新、精确来源；
问题涉及时效性；
问题风险较高；
内部证据不足；
已有材料可能过期。
```

满足这些条件时，router 应设置：

```text
require_external_evidence = true
```

第一版外部证据只会通过 `VerificationService` 生成低可信 DDG 候选证据。

## LLM 分类策略

Router 的 LLM 分类是可选兜底。

允许调用模型的情况：

```text
规则分类置信度低；
用户意图不清；
问题同时像查找和推理；
风险和证据要求不明确。
```

模型输入必须保持最小：

```text
query
available_context summary
user_intent_hint
risk_hint
candidate_modes
```

模型不得接收大量文档、检索结果或完整 trace。

模型输出必须结构化：

```text
{
  "mode": "retrieval | reasoning",
  "require_evidence": true,
  "require_external_evidence": false,
  "trace_level": "light | full",
  "primary_pattern": "direct_answer | rule_decision | causal_analysis | diagnostic | tradeoff | planning",
  "supporting_patterns": [],
  "reason": "...",
  "confidence": 0.0
}
```

如果模型输出解析失败，router 使用规则结果或默认 `ReasoningAgent`。

## trace_level 决策

第一版规则：

```text
RetrievalAgent 默认 light；
ReasoningAgent 默认 full；
风险高或需要外部证据时至少 light；
发生整体模式切换时使用 full；
用户明确要求不要记录时可以 none，但系统仍可保留工程日志。
```

## 模式切换

Router 负责初始 agent 选择。

Agent 执行过程中可以返回 `mode_switch_request`。

Router 只执行整体模式切换。

局部冲突、局部缺口和局部不确定性优先由 `ModeSwitchEvaluationService` 生成 `local_mode_escalation`，交给当前 agent 编排处理。

只有当局部问题影响核心答案时，才升级为整体模式切换。

第一版支持：

```text
RetrievalAgent -> ReasoningAgent
```

不建议在同一请求中反复切换。

应限制：

```text
max_mode_switches = 1
```

超过限制后，使用当前 agent 的保守回答或返回证据不足。

## 默认模式和降级

默认策略：

```text
规则高置信查找类问题 -> RetrievalAgent；
规则高置信复杂问题 -> ReasoningAgent；
不确定 -> ReasoningAgent；
router 失败 -> ReasoningAgent；
LLM 分类失败 -> 使用规则结果；
规则和 LLM 都失败 -> ReasoningAgent。
```

选择 `ReasoningAgent` 作为失败兜底，是因为它能承接检索、缺口、冲突和证据不足。

但即使进入 `ReasoningAgent`，也必须遵守模型调用最小化原则。

## AgentRequest 映射

RouterDecision 应映射为：

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

`request_id` 来自 runtime。

`trace_id` 可由调用方传入；如果为空，由 agent 或 `TraceService` 创建。

`require_evidence` 和 `require_external_evidence` 必须显式进入 `AgentRequest`，再由 agent 传给 retrieval、verification 和 response assembly 等 service，不能只保存在 `RouterDecision` 中。

## 错误处理

Router 错误必须遵循 `docs/impl/error-handling.md`。

错误类型：

| 错误码 | 含义 | 处理 | retryable |
| --- | --- | --- | --- |
| `router_rule_failed` | 规则分类失败 | 尝试 LLM 分类或默认 RetrievalAgent | 否 |
| `router_llm_failed` | LLM 分类失败 | 使用规则结果或默认 RetrievalAgent | 是 |
| `router_output_invalid` | 路由输出非法 | 默认 RetrievalAgent | 否 |
| `mode_switch_limit_exceeded` | 模式切换超过限制 | 保守回答或证据不足 | 否 |

跨模块返回时，错误必须转换为 `SystemError` 或包含同等字段的模块错误结果。

Router 失败不能导致请求无响应。

## 验收标准

第一版应满足：

```text
能够在 RetrievalAgent 和 ReasoningAgent 之间选择；
路由规则不写死为不可扩展的两个分支；
能够根据用户意图、复杂度、风险和证据要求选择模式；
能够决定 trace_level；
能够决定 require_evidence 和 require_external_evidence；
能够处理 agent 返回的整体 mode_switch_request；
能够区分整体模式切换和局部升级；
router 失败时默认进入 RetrievalAgent 的证据直答短路径；
LLM 分类是可选兜底，不是主路径；
错误处理符合 SystemError 约定。
```

Router 只决定初始处理模式，不应仅凭“判断”“比较”“原因”等表面问题类型直接选择深推理。初始模式默认是证据直答短路径；完成目录结构召回、FTS 召回和轻量筛选后，再由直接回答充分性决定是否局部或整体升级。高风险请求和用户明确要求推理过程的请求可以直接进入 `ReasoningAgent`。
