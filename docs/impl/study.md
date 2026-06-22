# Study 实现方案

本文档描述知识大脑第一版中 `study` 的简化实现方案。

`study` 负责从 trace 和 learning signals 中接收学习输入。

第一版实现受控的 ActivationLink 学习闭环。

它保存候选学习材料，负责首次创建 candidate ActivationLink，并在满足阈值时更新 ActivationLink 状态。

Domain / Concept 的创建、合并、拆分和边界调整仍只进入候选或审核流程。

`study` 不在用户请求主链路中同步执行。

它以后台 job 形式运行。

## 第一版定位

第一版 `study` 不做：

```text
不自动创建正式 Domain；
不自动创建正式 Concept；
不自动合并、拆分或移动 Concept；
不自动固化 ExperiencePath；
不自动修改 KnowledgeUnit；
不根据单次使用结果修改长期认知结构。
```

第一版只做：

```text
读取 trace；
读取 learning_signals；
保存候选学习输入；
根据去重后的多次验证激活 candidate ActivationLink；
根据弱相关、负反馈或边界失配降级 active ActivationLink；
记录处理状态；
为后续 review / study 提供查询接口。
```

## 模型调用

第一版 `study` 默认不调用模型。

原因：

```text
第一版主要保存候选学习输入，并对 ActivationLink 做规则化状态更新；
不执行候选合并；
不判断概念边界；
不生成正式经验路径；
不创建新的正式 Domain / Concept。
```

这些动作都可以通过规则和结构化 trace 完成。

后续完整 study / review 可以把模型作为辅助，但只能用于局部语义判断。

可选模型辅助场景：

```text
判断多个 candidate_concept 是否表达同一概念；
总结多个 trace 暴露出的共同认知缺口；
辅助生成候选 Concept / Domain 的名称和描述；
辅助判断候选 ActivationLink 的适用边界；
辅助整理 ExperiencePath 的步骤摘要；
辅助生成人工审核说明。
```

即使使用模型，也不能由模型直接创建正式 Domain / Concept，或绕过阈值直接修改 ActivationLink。

模型输出只能写入候选记录、review 说明、建议，或作为 ActivationLink 状态更新的复核输入。

约束：

```text
只能在后台 StudyJob 中调用模型；
不能阻塞用户回答；
必须先用规则筛选候选范围；
不能把大量 trace 原文直接输入模型；
只能传入必要摘要、候选对象和证据索引；
必须使用 JSON Schema 校验输出；
失败时只记录 warning，不影响已保存候选。
```

## 配置项

ActivationLink 学习阈值必须配置化，不应散落硬编码。

建议配置：

```text
study.activation_link.promote.min_distinct_question_clusters = 3
study.activation_link.promote.min_activation_count = 5
study.activation_link.promote.min_used_count = 3
study.activation_link.promote.min_positive_feedback_count = 2
study.activation_link.promote.max_negative_feedback_count = 0
study.activation_link.promote.min_supported_hit_route_count = 2
study.activation_link.promote.min_boundary_review_confidence = 0.80

study.activation_link.demote.max_negative_feedback_count
study.activation_link.demote.max_unused_activation_count
study.activation_link.demote.boundary_mismatch_action = discarded
study.activation_link.demote.evidence_missing_action = disabled
```

配置值可以随版本调整，但每次状态变化必须在 `study_events.details` 中记录当时使用的配置快照。

## 后台 Job

第一版 `study` 以后台 job 运行。

触发来源：

```text
TraceService 写入完整 trace 后；
LearningSignalService 写入 learning_signals 后；
用户反馈写入 trace 后；
人工或后台任务请求重新处理某个 trace。
```

Job 输入：

```text
StudyJob {
  job_id
  trace_id
  trigger
  priority
  idempotency_key
}
```

`trigger` 可取：

```text
trace_created
learning_signal_created
feedback_received
manual_reprocess
```

Job 状态：

```text
pending
running
succeeded
skipped
failed
retrying
dead
```

第一版要求：

```text
study job 不阻塞用户回答；
study job 失败不影响已生成回答；
同一个 trace_id + trigger 应具备幂等性；
重复执行不能重复创建相同 study_candidate；
可重试错误进入 retrying；
超过重试次数进入 dead；
所有状态变化写入 study_events。
```

幂等键建议：

```text
idempotency_key = trace_id + signal_id + candidate_type
```

如果同一个学习信号已经生成过候选记录，后续 job 应跳过或更新已有记录，不创建重复候选。

## 输入

study 的输入来自 trace。

第一版至少消费：

```text
trace_id
perspective_id
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
claim_refs
learning_signals
experience_path_suggestions
feedback
```

其中 `learning_signals` 由 `LearningSignalService` 生成。

如果没有 `learning_signals`，study 可以跳过该 trace。

## 学习信号

第一版学习信号类型：

```text
cognitive_gap_signal
knowledge_gap_signal
activation_success_signal
activation_failure_signal
conflict_signal
useful_evidence_signal
reasoning_path_signal
feedback_signal
experience_path_signal
```

学习信号不是长期记忆变更。

它只是说明：

```text
哪里可能值得学习；
哪些知识被真实使用；
哪些激活路径有效或无效；
哪些认知缺口反复出现；
哪些证据或推理路径值得后续复核。
```

学习信号必须尽量保留本次问题的认知路由维度：

```text
scene；
goal；
pattern；
focus；
knowledge_relevance_hints；
question_cluster_id；
question_cluster_key。
```

这些字段来自 trace 中的 `cognitive_route_context`。

如果缺少这些字段，study 可以保存候选，但不得自动把候选 ActivationLink 晋升为 active。

## 候选记录

study 第一版可以把学习信号保存为候选记录。

建议结构：

```text
StudyCandidate {
  id
  trace_id
  signal_id
  idempotency_key
  candidate_type
  scene
  goal
  pattern
  focus
  perspective_id
  related_domain_ids
  related_concept_ids
  related_unit_ids
  related_knowledge_point_ids
  related_activation_link_ids
  reason
  confidence
  status
  created_at
  updated_at
}
```

`candidate_type` 可取：

```text
candidate_domain
candidate_concept
candidate_activation_link
candidate_experience_path
candidate_review_task
```

`status` 可取：

```text
pending
ignored
reviewed
promoted
rejected
```

第一版只写入 `pending` 或 `ignored`。

`reviewed / promoted / rejected` 为后续人工审核或完整 study 流程预留。

## 候选生成规则

第一版只做保守候选生成。

规则：

```text
认知结构未命中，但多路召回命中且被 used，可以生成 candidate_domain 或 candidate_concept；
匹配到 Concept 但没有 active ActivationLink，可以生成 candidate_activation_link；
ActivationLink 命中但后续相关性弱，可以生成 candidate_review_task；
知识缺口反复出现，可以生成 candidate_review_task；
冲突未解决，可以生成 candidate_review_task；
问题处理过程出现可复用步骤，可以生成 candidate_experience_path；
用户明确负反馈，可以生成 candidate_review_task。
```

第一版不根据单次信号直接晋升。

如果缺少问题聚类和重复验证，候选只能保持 `pending`。

## ActivationLink 激活和降级

第一版允许 `study` 根据真实使用证据创建 candidate ActivationLink，并更新已有 `ActivationLink.status`：

```text
candidate -> active
active -> disabled
candidate -> discarded
```

状态边界必须严格执行：

```text
active：唯一可以参加优先认知检索的状态；
candidate：只接受独立补充检索结果、EvidenceRef、实际采用和反馈的验证，不参加 retrieval；
discarded：候选验证不通过后的终态，不再参加检索或重复验证。
```

不得通过该流程创建新的正式 Domain / Concept。

candidate ActivationLink 激活为 active 的建议阈值：

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

其中 `activation_count / used_count / feedback_count` 必须按 `question_cluster_id` 去重。

candidate ActivationLink 不得只因为一次回答采用、一次用户正反馈或一次模型高置信而激活。

候选验证信号必须来自不依赖该候选关系的补充检索路径。不得把候选关系自身指向知识单元所产生的召回、得分或使用次数作为验证证据，以免形成自证循环。

如果 trace 或 learning_signal 缺少 `question_cluster_id`，study 不得自动执行 `candidate -> active`。

这种情况只能：

```text
写入或更新 study_candidate；
标记 review_required = true；
在 study_events.details 中记录 missing_question_cluster；
等待人工审核或后续问题聚类补齐。
```

active ActivationLink 降级规则：

```text
连续多次被激活但未被 used；
多路召回反复不支持该 link；
用户明确负反馈；
最近一次边界复核发现 concept.boundary 不匹配；
证据回链失效或关联 KnowledgeUnit / KnowledgePoint 不再 active。
```

如果关联 SourceDocument / KnowledgeUnit / KnowledgePoint 当前状态为：

```text
disabled -> 不累计 positive 使用计数，ActivationLink.status = disabled；
discarded -> 不累计任何晋升计数，ActivationLink.status = discarded；
deleted -> 不生成候选、不晋升、不强化，ActivationLink.status = deleted。
```

study 不得基于 `disabled / discarded / deleted` 对象创建新的 candidate_activation_link 或执行 `candidate -> active`。

状态动作：

```text
active 路径需要重新观察但尚未确认错误 -> status = disabled，并创建 review task；
短期不可用或证据失效 -> status = disabled；
确认语义错误或边界冲突 -> status = discarded。
candidate 验证失败、边界不符或独立材料不支持 -> status = discarded；
关联 source / unit / point 已删除 -> status = deleted。
```

所有状态变化必须写入 `study_events`，并记录：

```text
previous_status
next_status
trigger_signal_ids
question_cluster_ids
reason
review_required
```

### ActivationLink 边界复核

`study` 在激活或降级 ActivationLink 前，应执行轻量边界复核。

输入：

```text
perspective
domain
concept.name
concept.description
concept.boundary
knowledge_point
unit.title
unit.center
supporting_trace_summaries
positive / negative feedback summary
```

输出：

```text
boundary_match = matched | weak | mismatched
confidence
reason
review_required
```

规则：

```text
boundary_match = matched 且 confidence >= 0.80，才允许 candidate -> active；
boundary_match = weak 时保持或降级为 candidate；
boundary_match = mismatched 时降级为 discarded 或生成 candidate_review_task；
边界复核结果必须写入 study_events.details；
模型可辅助边界复核，但不能绕过计数阈值和 question_cluster 去重。
```

## 和长期认知结构的边界

study 第一版不能直接修改：

```text
Domain.status
Concept.status
KnowledgeUnit
KnowledgePoint
ExperiencePath
```

允许写入或更新：

```text
study_jobs
study_candidates
study_events
ActivationLink.status
ActivationLink.activation_count
ActivationLink.used_count
ActivationLink.positive_feedback_count
ActivationLink.negative_feedback_count
```

ActivationLink 状态更新必须来自 trace / learning_signals 的累计证据。

累计证据不应只按 `activation_link_id` 全局计算。

它还应按以下维度聚合：

```text
perspective_id；
question_cluster_id；
scene；
goal；
pattern；
focus。
```

同一条 ActivationLink 在“故障排查 + 根因定位”下表现良好，不代表它在“系统设计 + 方案规划”下也应该被强化。

当反馈只在某些维度下稳定成立时，study 应优先更新链接的适用条件、候选说明或 review task，而不是简单全局升降权。

这些维度化统计应写入 `activation_link_route_stats`。

每条学习信号处理时，study 应：

```text
读取 related_activation_link_ids；
读取 signal.scene / goal / pattern / focus / question_cluster_id；
按 activation_link_id + question_cluster_id + scene + goal + pattern + focus upsert route stats；
累计 activation_count / used_count / feedback_count / validation_support_count；
保留 supported_hit_routes、last_candidate_link_score 和 last_boundary_match；
同步更新 ActivationLink 的兼容性全局计数字段。
```

自动晋升 candidate ActivationLink 时，阈值判断必须优先使用 `activation_link_route_stats` 的去重统计。

如果只有全局计数满足阈值，但缺少稳定的 route stats，不得自动晋升，只能生成 `candidate_review_task`。

当某条链接在局部 route 下表现稳定有效，但全局表现混杂时，study 不应直接把整个 link 简单升降级。

它应优先：

```text
补全或收窄 ActivationLink.scene_tags / goal_tags / pattern_tags / focus_tags；
生成 candidate_review_task；
必要时建议拆分为更精确的 candidate ActivationLink。
```

ActivationLink 因关联对象 `deleted` 而进入 `deleted` 状态时，可以由 source 删除级联触发，但必须写入 `study_events` 或结构化状态变更日志。

Domain / Concept 候选晋升仍必须等后续 review / 完整 study 流程实现。

## Review 边界

第一版 `study` 可以激活或降级 ActivationLink，但不执行 Domain / Concept 候选晋升。

候选包括：

```text
candidate_domain
candidate_concept
candidate_activation_link
candidate_experience_path
candidate_review_task
import_external_source
```

除 candidate ActivationLink 的状态更新外，这些候选只能作为人工审核或第二版完整 study 的输入。

第一版不得因为以下单次信号直接修改正式认知结构：

```text
一次检索命中；
一次回答采用；
一次用户正反馈；
一次模型匹配高置信；
一次外部候选证据出现；
```

Domain / Concept 候选晋升至少需要后续 review 流程重新确认：

```text
是否来自多个不同问题簇；
是否被多次实际 used；
是否有正反馈且无明确负反馈；
是否符合当前 Perspective；
是否仍符合 Domain / Concept 边界；
是否有可回溯证据；
是否存在生命周期风险；
```

第一版可以保存 review 所需字段，但不提供自动晋升：

```text
review_status
review_reason
reviewer
reviewed_at
promoted_target_type
promoted_target_id
```

这些字段即使存在，也只作为预留或人工流程记录，不代表第一版会自动写入 Domain / Concept / ActivationLink / ExperiencePath。

## 数据库表

第一版建议表：

```text
study_jobs
study_candidates
study_events
```

### study_jobs

```text
id
trace_id
trigger
priority
idempotency_key
status
attempt_count
max_attempts
next_run_at
last_error_code
last_error
created_at
updated_at
```

`last_error` 使用 `docs/impl/error-handling.md` 中的 `SystemError` 字段，或包含同等字段的模块错误结果。

### study_candidates

```text
id
trace_id
signal_id
idempotency_key
candidate_type
question_cluster_id
question_cluster_key
perspective_id
related_domain_ids
related_concept_ids
related_unit_ids
related_knowledge_point_ids
related_activation_link_ids
candidate_link_score
validation_support
supporting_hit_routes
candidate_validation_result
scene
goal
pattern
focus
reason
confidence
status
created_at
updated_at
```

### study_events

```text
id
trace_id
candidate_id
event_type
status
previous_status
next_status
trigger_signal_ids
question_cluster_ids
review_required
details
error_code
error
warnings
created_at
```

`error` 使用 `docs/impl/error-handling.md` 中的 `SystemError` 字段，或包含同等字段的模块错误结果。

## 错误处理

StudyJob 执行应返回 `StudyJobResult`：

```text
StudyJobResult {
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
skipped
failed
retrying
dead
```

`data` 至少包含：

```text
job_id
trace_id
learning_signal_ids
study_candidate_ids
event_ids
```

`error` 必须使用 `SystemError` 字段，或包含同等字段的模块错误结果。

错误类型：

| 错误码 | 含义 | 处理 | retryable |
| --- | --- | --- | --- |
| `trace_missing` | trace 不存在 | 跳过并记录事件 | 否 |
| `learning_signal_missing` | trace 中没有学习信号 | 跳过，不视为失败 | 否 |
| `study_job_duplicate` | 重复 job 或重复候选 | 跳过或更新已有记录 | 否 |
| `study_job_claim_failed` | 获取 job 执行权失败 | 稍后重试 | 是 |
| `study_candidate_write_failed` | 候选记录写入失败 | 记录错误，可重试 | 是 |
| `study_event_write_failed` | study 事件写入失败 | 记录工程日志，可重试 | 是 |

study 失败不能影响当前回答。

study 是异步或后置流程。

`SystemError` 字段要求：

```text
module = study
operation = study.run_job
stage = claim_job | load_trace | load_learning_signals | build_candidates | upsert_candidates | write_events | update_job_status
target = study_job | trace | learning_signal | study_candidate | study_event
entity_type = study_job
entity_id = job_id
request_id = StudyJobResult.request_id
trace_id = trace_id
```

状态更新规则：

| 错误码 | job 状态 | 事件记录 | 传给下一阶段 |
| --- | --- | --- | --- |
| `trace_missing` | `skipped` | 写 `study_events` | 不生成候选 |
| `learning_signal_missing` | `skipped` | 写 `study_events` | 不生成候选 |
| `study_job_duplicate` | `skipped` | 写或更新已有事件 | 不生成重复候选 |
| `study_job_claim_failed` | `retrying` | 写 `study_events` | 稍后重试 |
| `study_candidate_write_failed` | `retrying` 或 `dead` | 写 `study_events`；事件失败则写工程日志 | 不标记 succeeded |
| `study_event_write_failed` | `retrying` 或 `dead` | 写工程日志 | 不伪造事件 |

如果 `StudyJobResult.success = false`，不得把 job 标记为 `succeeded`。

## Job 执行流程

建议流程：

```text
enqueue StudyJob
  -> claim pending job
  -> load trace
  -> load learning_signals
  -> build study candidates
  -> upsert study_candidates by idempotency_key
  -> write study_events
  -> mark job succeeded / skipped / retrying / dead
```

如果 trace 不存在，job 标记为 `skipped`。

如果 trace 存在但没有 learning_signals，job 标记为 `skipped`。

如果候选写入失败，job 根据错误可重试性进入 `retrying` 或 `dead`。

## 和 trace 的关系

trace 记录一次问题处理过程。

study 读取 trace 中的学习输入。

第一版只把这些输入整理为候选记录。

```text
trace
  -> learning_signals
  -> study_jobs
  -> study_candidates
  -> 后续 review / study
```

trace 中 retrieved 但未 used 的知识，不应自动强化。

只有 used、反馈良好、反复出现或有明确缺口指向的信号，才适合进入候选记录。

## 和 precompile 的关系

precompile 只导入预制 Domain / Concept，并保存 KnowledgePoint 到 Domain / Concept 的静态匹配候选；它不创建 ActivationLink。

study 第一版不反向修改预制 Domain / Concept 或 unit 阶段生成的 KnowledgeUnit / KnowledgePoint。

study 可以在材料侧召回独立命中、知识被实际采用且可回链时首次创建 candidate ActivationLink，并基于累计 trace 更新其状态和计数字段。

study 第一版可以基于候选记录决定：

```text
是否将 candidate ActivationLink 晋升为 active；
是否将 active ActivationLink 降级为 candidate / disabled / discarded；
```

后续完整 study 可以基于候选记录决定：

```text
是否创建候选 Domain；
是否创建候选 Concept；
是否调整已有 Concept 边界；
是否沉淀 ExperiencePath。
```

第一版只保留这些输入和状态。

## 验收标准

第一版应满足：

```text
能够读取 trace_id 对应的学习输入；
能够识别 learning_signals；
能够以后台 job 形式处理 trace；
能够保证同一 learning signal 不重复生成候选；
能够保存 study_candidates；
能够记录 study_events；
能够记录 study_jobs 状态；
不会自动修改正式 Domain / Concept / ActivationLink；
不会自动固化 ExperiencePath；
不会修改 KnowledgeUnit 或 KnowledgePoint；
study 失败不影响当前回答；
错误记录符合 SystemError 约定。
```
