# Trace 实现方案

本文档描述知识大脑第一版中 `trace` 模块的实现方案。

`trace` 是思考记录。

它记录一次问题处理过程中，系统如何理解问题、使用当前认知视角激活知识、从材料和证据中补充查找、组织临时认知模型、形成回答，以及暴露了哪些知识缺口和认知缺口。

## 第一版定位

trace 不是工程日志。

工程日志用于调试运行状态。

trace 用于记录认知过程，并为后续 study 和 lifecycle 提供学习样本。

第一版重点定义：

```text
轻量 trace；
完整 trace；
trace 触发条件；
问题理解记录；
领域和概念激活记录；
补充查找记录；
retrieved / used 区分；
认知缺口记录；
知识缺口记录；
冲突记录；
外部候选证据记录；
模式切换和局部升级记录；
回答证据映射记录；
学习信号记录；
错误和降级记录；
trace 查询和导出。
```

## trace 级别

第一版支持：

```text
none
light
full
```

含义：

| 级别 | 含义 |
| --- | --- |
| `none` | 不记录认知过程 |
| `light` | 记录问题、模式、采用的知识和最终结果 |
| `full` | 记录完整问题理解、激活、补充查找、冲突、缺口和推理过程 |

trace 深度由思维模式、风险和学习价值决定。

## TraceRecord

建议结构：

```text
TraceRecord {
  id
  question
  perspective_id
  mode
  agent
  trace_level
  understanding
  service_calls
  activated_domains
  activated_concepts
  retrieved_items
  used_items
  evidence_refs
  cognitive_gaps
  knowledge_gaps
  conflicts
  external_evidence_candidates
  mode_switch_decision
  experience_path_suggestions
  working_model_summary
  claim_refs
  inference_refs
  unsupported_claims
  answer_summary
  feedback
  learning_signals
  learning_suggestions
  created_at
  updated_at
}
```

## 问题理解记录

记录：

```text
用户问题；
问题类型；
用户意图；
认知路由上下文；
当前认知视角；
当前思维模式；
执行该思维模式的 agent；
匹配到的领域；
匹配到的概念；
是否需要外部证据；
是否需要完整临时认知模型。
```

`认知路由上下文` 来自 `ProblemFramingService`。

至少记录：

```text
scene；
goal；
pattern；
focus；
constraints；
knowledge_relevance_hints；
feedback_context_summary；
question_cluster_hint。
```

trace 中保存的是本次问题处理使用过的摘要，不保存全量历史 trace。

历史 trace 只能通过 study 或聚合索引形成后续请求的 `feedback_context`。

## Service 调用记录

trace 应记录 agent 编排了哪些认知服务。

记录粒度由 trace level 决定。

light trace 至少记录：

```text
service_name
status
warnings
```

full trace 应记录：

```text
service_name
input_summary
output_summary
status
error_code
error
warnings
model_called
model_name
prompt_version
input_token_estimate
context_summary
started_at
finished_at
```

这些记录用于解释：

```text
当前思维模式实际执行了哪些认知能力；
哪个 service 产生了关键结果；
哪个 service 失败或降级；
哪些 service 输出进入了后续推理。
```

`error` 必须使用 `docs/impl/error-handling.md` 中的 `SystemError` 字段，或包含同等字段的模块错误结果。

trace 不保存外部服务原始错误的全部细节，但应保存可定位、可复盘的摘要和错误码。

## 激活记录

领域和概念激活需要记录：

```text
matched_domain_ids
matched_concept_ids
activated_unit_ids
activated_knowledge_point_ids
activation_confidence
activation_failed
failure_reason
```

这些记录用于判断当前认知结构是否有效。

## 补充查找记录

补充查找需要记录：

```text
hit_route
hit_channels
unit_id
knowledge_point_id
source_document_id
source_spans
domain_score
source_score
tree_score
score
rerank_score
fusion_signals
evidence_ready
retrieved
used
```

`hit_route` 可取：

```text
domain_concept_activation
unit_text_search
knowledge_point_search
outline_tree_match
outline_path_search
source_span_lookup
external_evidence
```

`outline_tree_match` 表示阶段二候选文档树语义匹配；`outline_path_search` 表示阶段二失败时的词法回退。

`external_evidence` 在第一版只表示外部候选证据。

如果来源来自 DDG 搜索，trace 应记录：

```text
search_provider
search_query
title
url
snippet
retrieved_at
credibility
confidence
warnings
```

外部候选证据默认低可信，不能和内部 `EvidenceRef` 混用。

## 冲突记录

trace 应记录 `ConflictDetectionService` 发现的冲突。

建议结构：

```text
ConflictRecord {
  conflict_id
  conflict_type
  conflicting_claims
  conflicting_evidence_refs
  related_unit_ids
  related_knowledge_point_ids
  related_domain_ids
  related_concept_ids
  scope_conditions
  why_conflict
  resolution
  affects_core_answer
}
```

如果冲突只触发局部升级，应记录：

```text
local_mode_escalation
conflict_case_id
target_agent
resolution_result
```

局部冲突不应被记录成整个问题的模式切换。

## 学习信号

trace 可以记录 `LearningSignalService` 生成的学习信号。

第一版只保存信号，不执行 study。

建议记录：

```text
signal_id
signal_type
related_domain_ids
related_concept_ids
related_unit_ids
related_knowledge_point_ids
reason
confidence
status
```

如果当前问题暴露出可复用流程，也可以记录 `experience_path_suggestions`。

这只是后续 `ExperiencePathService` 或 study 的输入，不会在第一版自动固化为经验路径。

## retrieved 和 used

trace 必须区分：

```text
retrieved
  被召回或进入候选；

used
  被临时认知模型、推理路径或最终回答采用。
```

被召回但未使用的知识不应自动强化。

被使用且反馈良好的知识，可以由后续 study 强化。

trace 不因 source / unit / point / ActivationLink 后续状态变化而删除。

trace 应保存使用时状态快照，并在读取时展示当前状态：

```text
source_status_at_use
unit_status_at_use
knowledge_point_status_at_use
activation_link_status_at_use
current_source_status
current_unit_status
current_knowledge_point_status
current_activation_link_status
```

如果当前状态为 `disabled / discarded / deleted`，历史 trace 可以展示该引用，但不得把它作为当前可用证据或学习强化输入。

candidate ActivationLink 必须单独记录验证过程，并明确该过程不属于 retrieval 召回。

建议记录：

```text
candidate_activation_link_id
matched_domain_id
matched_concept_id
knowledge_point_id
unit_id
supported_hit_routes
evidence_ref_ids
validation_support
score_breakdown
supporting_result_was_used
user_feedback
validation_result = supported | weak | rejected
reason
```

candidate ActivationLink 不记录“被召回”，只记录独立补充检索是否支持其假设。没有材料侧召回、实际采用或证据回链支持时，应判为 rejected 或保持待审，不得强化。

如果 active ActivationLink 命中后在融合排序中持续低相关、没有被 used，或收到负反馈，应记录为降级候选信号。

## 认知缺口

当领域和概念激活失败，但补充查找找到并采用了有效知识时，应记录认知缺口。

建议结构：

```text
CognitiveGapRecord {
  gap_type
  perspective_id
  query
  failed_domain_ids
  failed_concept_ids
  activation_link_ids
  adopted_unit_ids
  adopted_knowledge_point_ids
  supporting_hit_ids
  suggested_domain_names
  suggested_concept_names
  reason
  confidence
}
```

认知缺口不直接修改认知结构。

它是 study 的输入。

## 知识缺口

知识缺口表示系统没有足够材料或证据回答当前问题。

示例：

```text
缺少事实；
来源证据不足；
已有知识过期；
存在冲突但无法判断；
需要外部查证。
```

建议结构：

```text
KnowledgeGapRecord {
  gap_type
  query
  unit_ids
  source_document_ids
  reason
  required_evidence
  severity
}
```

知识缺口不表示认知结构一定有问题。

它可能只是当前材料不足、证据缺失、来源不可用或需要外部查证。

## 模式切换记录

trace 应记录 `ModeSwitchEvaluationService` 的判断。

建议结构：

```text
ModeSwitchDecisionRecord {
  current_mode
  continue_current_mode
  whole_mode_switch
  local_mode_escalations
  fallback_action
  reason
  warnings
}
```

`whole_mode_switch` 表示整个问题切换到其他 agent。

`local_mode_escalation` 表示只把局部冲突、局部缺口或局部不确定性升级处理。

局部升级不应被记录成完整模式切换。

## 回答映射记录

trace 应记录 `ResponseAssemblyService` 生成的回答映射。

回答中的关键内容必须区分：

```text
supported_claim
inferred_claim
unsupported_or_gap
```

建议结构：

```text
ClaimRefRecord {
  claim_id
  claim_type
  text
  evidence_ref_ids
  unit_ids
  knowledge_point_ids
  reasoning_step_ids
  gap_ids
  confidence
}
```

规则：

```text
supported_claim 必须能回到 EvidenceRef；
inferred_claim 必须能回到 used knowledge 和 reasoning step；
unsupported_or_gap 必须能回到 KnowledgeGap 或 warning；
不能把推断记录成原文事实；
外部候选证据不能作为强证据。
```

## 错误和降级记录

影响认知过程的错误应进入 trace。

例如：

```text
检索索引不可用；
证据来源无法读取；
模型输出不完整；
临时认知模型构建失败；
回答降级为证据不足。
```

工程运行错误仍记录到日志。

trace 只记录影响本次思考的错误。

## 数据库表

第一版建议表：

```text
trace_records
trace_service_calls
trace_activations
trace_retrieval_hits
trace_used_items
trace_evidence_refs
trace_gaps
trace_external_evidence_candidates
trace_conflicts
trace_mode_switch_decisions
trace_claim_refs
trace_learning_signals
```

### trace_records

```text
id
question
perspective_id
mode
agent
trace_level
understanding
cognitive_route_context
service_calls
activated_domains
activated_concepts
retrieved_items
used_items
evidence_refs
cognitive_gaps
knowledge_gaps
conflicts
external_evidence_candidates
mode_switch_decision
experience_path_suggestions
working_model_summary
claim_refs
inference_refs
unsupported_claims
answer_summary
feedback
learning_signals
learning_suggestions
created_at
updated_at
```

### trace_service_calls

```text
id
trace_id
service_name
input_summary
output_summary
status
error_code
error
warnings
model_called
model_name
prompt_version
input_token_estimate
context_summary
started_at
finished_at
created_at
```

`error` 使用 `SystemError` 字段或同等模块错误结果。

### trace_activations

```text
id
trace_id
domain_id
concept_id
activation_link_id
activation_link_status
scene
goal
pattern
focus
unit_id
knowledge_point_id
activation_confidence
candidate_link_score
validation_support
supported_hit_routes
candidate_validation_result
activation_failed
failure_reason
created_at
```

### trace_retrieval_hits

```text
id
trace_id
hit_route
unit_id
knowledge_point_id
source_document_id
source_spans
score
rerank_score
score_breakdown
activation_link_ids
activation_link_statuses
candidate_link_score
candidate_penalty
validation_support
supporting_hit_routes
candidate_validation_result
fusion_signals
evidence_ready
retrieved
used
created_at
```

### trace_used_items

```text
id
trace_id
unit_id
knowledge_point_id
source_document_id
source_spans
used_in
used_reason
created_at
```

### trace_evidence_refs

```text
id
trace_id
unit_id
knowledge_point_id
source_document_id
source_spans
normalized_markdown_path
preview_html_path
original_source_path
quote
evidence_ready
evidence_level
warnings
created_at
```

读取 trace evidence 时，如果当前 source / unit / point 状态为 `deleted`：

```text
evidence_level = unavailable；
evidence_ready = false；
warnings 包含 source_deleted 或 unit_deleted；
展示为历史证据已删除，不参与新回答。
```

`trace_evidence_refs` 对齐 `EvidenceTracingService` 的输出。

第一版以 normalized Markdown 和 SourceSpan 作为精确证据锚点。

HTML 只作为预览入口，不要求块级定位。

### trace_external_evidence_candidates

```text
id
trace_id
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
created_at
```

第一版外部候选证据来自 DDG 时，`credibility` 默认 `low`。

外部候选证据不能替代内部 `EvidenceRef`。

### trace_conflicts

```text
id
trace_id
conflict_type
conflicting_claims
conflicting_evidence_refs
related_unit_ids
related_knowledge_point_ids
related_domain_ids
related_concept_ids
scope_conditions
why_conflict
resolution
affects_core_answer
created_at
```

### trace_mode_switch_decisions

```text
id
trace_id
current_mode
continue_current_mode
whole_mode_switch
local_mode_escalations
fallback_action
reason
warnings
created_at
```

### trace_claim_refs

```text
id
trace_id
claim_id
claim_type
text
evidence_ref_ids
unit_ids
knowledge_point_ids
reasoning_step_ids
gap_ids
confidence
created_at
```

### trace_learning_signals

```text
id
trace_id
signal_type
question_cluster_id
question_cluster_key
perspective_id
scene
goal
pattern
focus
related_domain_ids
related_concept_ids
related_unit_ids
related_knowledge_point_ids
related_activation_link_ids
candidate_link_score
candidate_penalty
validation_support
supporting_hit_routes
candidate_validation_result
score_breakdown
reason
confidence
status
created_at
```

第一版只保存学习信号。

不在 trace 阶段执行 study，也不直接修改长期认知结构。

### trace_gaps

```text
id
trace_id
gap_type
perspective_id
description
related_domain_ids
related_concept_ids
related_unit_ids
related_knowledge_point_ids
related_activation_link_ids
supporting_hit_ids
suggested_domain_names
suggested_concept_names
confidence
created_at
```

## 和 study 的输出契约

trace 输出给 study：

```text
被激活的领域和概念；
激活失败的领域和概念；
被召回但未使用的知识；
被实际使用的知识；
补充查找补救成功的知识；
认知缺口；
知识缺口；
冲突记录；
局部升级和整体模式切换记录；
回答 claim 和 evidence / inference / gap 映射；
外部候选证据；
学习信号；
经验路径建议；
反馈。
```

study 根据这些记录决定：

```text
强化已有领域和概念；
降权无效激活路径；
形成候选领域和候选概念；
合并或拆分概念；
沉淀实践路径。
```

第一版不要求 study 执行这些动作。

trace 只需要提供足够稳定的输入，让后续 study 可以消费。

## 验收标准

第一版验收标准：

```text
能够记录当前认知视角；
能够记录匹配到的领域和概念；
能够记录领域和概念激活出的知识；
能够记录补充查找命中的知识；
能够区分 retrieved 和 used；
能够记录认知缺口和知识缺口；
能够记录冲突、外部候选证据、模式切换和局部升级；
能够记录回答中的证据支持、推断和证据缺口；
能够记录 LearningSignalService 生成的学习信号；
能够把用于回答的知识回到来源位置；
能够为 study 提供学习输入；
错误和降级必须能回到 service 调用和 SystemError；
不把工程日志当作 trace。
```
