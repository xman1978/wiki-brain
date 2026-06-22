# 第一版数据库 Schema

本文档集中列出第一版 SQLite schema。

实现时以本文档为迁移入口；各模块文档解释字段语义和业务规则。

字段可以按 SQLite 类型落地，但命名必须保持一致。

## 迁移顺序

```text
001_runtime.sql
002_source.sql
003_unit.sql
004_precompile.sql
005_retrieval.sql
006_trace.sql
007_study.sql
008_external_evidence.sql
```

## 通用字段

有长期状态的业务表应尽量包含：

```text
id
status
created_at
updated_at
last_error_code
last_error
last_error_stage
last_error_at
warnings
```

`last_error` 使用 `SystemError` JSON。

`warnings` 使用 JSON array。

## 001_runtime

### job_runs

用于轻量后台 job runner。

```text
id
job_type
job_key
payload
status
attempt_count
max_attempts
next_run_at
locked_at
locked_by
last_error
created_at
updated_at
```

`job_type` 第一版可取：

```text
unit_build
precompile
study
external_verification
```

## 002_source

### source_documents

来自 `docs/impl/source.md`。

```text
id
title
source_kind
intake_purpose
trust_level
origin_uri
original_file_name
original_file_path
preview_html_path
normalized_markdown_path
source_page_count
produced_at
imported_at
status
html_task_id
markdown_task_id
html_status
markdown_status
error_code
error_message
error_stage
error_target
retryable
external_status
external_message
last_error
created_at
updated_at
```

`source_kind` 第一版只实现：

```text
file
```

预留：

```text
webpage
search_result
chat
agent_trace
note
```

`intake_purpose`：

```text
long_term_candidate
temporary_evidence
```

`status`：

```text
created
original_saved
converting
html_ready
markdown_ready
converted
ready_for_unit
preview_ready_but_not_learnable
failed
archived
disabled
discarded
deleted
```

可用性状态：

```text
disabled  = 暂时不可用，可恢复，不参与新 unit / retrieval / study 强化；
discarded = 已判断不应继续使用，保留历史追溯，不参与新检索和学习；
deleted   = 用户删除来源导致逻辑删除，不参与任何新处理、新检索、新学习。
```

索引：

```text
idx_source_documents_status(status)
idx_source_documents_intake_purpose(intake_purpose)
idx_source_documents_source_kind(source_kind)
idx_source_documents_imported_at(imported_at)
```

## 003_unit

### unit_build_jobs

```text
id
source_document_id
status
attempt_count
max_attempts
started_at
finished_at
last_error
warnings
created_at
updated_at
```

`status`：

```text
pending
parsing
structure_analyzing
semantic_outlining
candidate_building
boundary_deciding
unit_writing
completed
completed_with_review
failed
retrying
```

### document_blocks

```text
id
source_document_id
unit_build_job_id
block_type
heading_path
content
start_line
end_line
start_offset
end_offset
metadata
created_at
```

### semantic_outlines

```text
id
source_document_id
unit_build_job_id
parent_id
title
center
summary
path
source_span
confidence
warnings
created_at
```

### unit_candidates

```text
id
source_document_id
unit_build_job_id
title
center
unit_type
content
original_excerpt
internal_points
outline_paths
outline_refs
source_spans
status
confidence
warnings
created_at
updated_at
```

`status`：

```text
accepted
needs_review
discarded
```

### knowledge_units

```text
id
source_document_id
unit_build_job_id
title
center
unit_type
content
original_excerpt
internal_points
primary_heading_path
covered_heading_paths
outline_paths
outline_refs
status
confidence
warnings
created_at
updated_at
```

`status`：

```text
draft
active
needs_review
disabled
discarded
deleted
```

`status`：

```text
active
needs_review
discarded
```

### unit_source_spans

```text
id
unit_id
source_document_id
block_id
start_line
end_line
start_offset
end_offset
role
created_at
```

`role`：

```text
primary
supporting
context
overlap
```

## 004_precompile

### perspectives

```text
id
name
description
status
created_at
updated_at
```

第一版必须存在：

```text
id = default
status = active
```

### domains

```text
id
perspective_id
name
description
source
preset_version
status
created_at
updated_at
```

### concepts

```text
id
perspective_id
domain_id
name
aliases
description
boundary
source
preset_version
status
created_at
updated_at
```

### knowledge_points

```text
id
unit_id
source_document_id
text
point_type
source_span_ids
status
confidence
warnings
created_at
updated_at
```

### source_search_index

SQLite 元数据表；词法召回由 Bleve `sources` 索引负责（见 `docs/impl/fts.md`）。

它是文档级目录树检索的入口。

```text
id
source_document_id
title
description
top_outline_summary
status
created_at
updated_at
```

字段语义：

| 字段 | 含义 |
| --- | --- |
| `source_document_id` | 候选文档边界，后续 outline tree search 必须限定在该文档内 |
| `title` | SourceDocument 标题或文件名 |
| `description` | 文档级说明，优先使用 source/unit 阶段形成的说明 |
| `top_outline_summary` | 该文档顶层目录节点说明聚合，用于判断文档是否可能相关 |

active 行要求：

```text
source_document_id 指向可检索 source_documents；
title 或 description 至少一个非空；
top_outline_summary 可以为空，但为空时文档选择置信度应降低。
```

### unit_search_index

SQLite 元数据表；词法召回由 Bleve `units` 索引负责（见 `docs/impl/fts.md`）。

```text
id
unit_id
source_document_id
search_text
title
center
outline_paths
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

### knowledge_point_search_index

SQLite 元数据表；词法召回由 Bleve `points` 索引负责（见 `docs/impl/fts.md`）。

```text
id
knowledge_point_id
unit_id
source_document_id
search_text
point_type
status
created_at
updated_at
```

### outline_search_index

SQLite 元数据表；词法召回由 Bleve `outlines` 索引负责（见 `docs/impl/fts.md`）。

```text
id
unit_id
source_document_id
outline_type
outline_path
outline_summary
unit_title
unit_center
status
created_at
updated_at
```

字段语义：

| 字段 | 含义 |
| --- | --- |
| `outline_type` | source、recovered、semantic 或 manual |
| `outline_path` | 目录节点路径，用于结构过滤和展示 |
| `outline_summary` | 目录节点覆盖范围说明，用于目录树词法预筛、outline_path_search 回退，以及 outline_tree_match 阶段二语义匹配 |
| `unit_title` | 该目录节点展开到的知识单元标题 |
| `unit_center` | 该目录节点展开到的知识单元中心句 |

`outline_summary` 不能只理解为展示摘要。

它是检索字段，来源应为：

```text
semantic outline summary；
source / recovered heading section summary；
manual outline summary；
必要时由 precompile 根据 unit.title / unit.center / original_excerpt 生成退化摘要。
```

active 行要求：

```text
outline_path 非空；
outline_summary 非空；
unit_id 指向 active knowledge_units；
source_document_id 指向可检索 source_documents。
```

### activation_links

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
status
confidence
match_reason
activation_count
used_count
positive_feedback_count
negative_feedback_count
created_at
updated_at
```

`link_type`：

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

`scene_tags / goal_tags / pattern_tags / focus_tags` 使用 JSON 数组。

空数组表示尚未学到稳定适用条件，不表示该链接适用于所有场景。

`relevance_summary` 说明该 KnowledgePoint 为什么能通过该 Concept 被激活。

`boundary` 说明该链接不适合被激活的边界。

`activation_count / used_count / positive_feedback_count / negative_feedback_count` 是兼容性全局计数。

按场景、目标、模式和注意焦点的精细统计应写入 `activation_link_route_stats`。

### activation_link_route_stats

```text
activation_link_id
perspective_id
question_cluster_id
scene
goal
pattern
focus
activation_count
used_count
positive_feedback_count
negative_feedback_count
validation_support_count
supported_hit_routes
last_candidate_link_score
last_boundary_match
last_used_at
created_at
updated_at
```

主键建议：

```text
activation_link_id + question_cluster_id + scene + goal + pattern + focus
```

如果缺少 `question_cluster_id`，不得用该记录自动触发 candidate -> active。

### precompile_events

```text
id
job_id
source_document_id
unit_id
knowledge_point_id
event_type
reason
details
created_at
```

### precompile_jobs

```text
id
trigger
source_document_id
unit_build_job_id
knowledge_unit_ids
status
idempotency_key
request_id
trace_id
attempt_count
max_attempts
last_error
created_at
updated_at
```

## 005_retrieval

### retrieval_events

```text
id
trace_id
request_id
perspective_id
query
mode
created_at
```

### retrieval_hits

```text
id
retrieval_event_id
unit_id
knowledge_point_id
source_document_id
hit_route
domain_id
concept_id
activation_link_id
matched_path
route_score
rerank_score
rank
fusion_signals
source_spans
evidence_ready
used
used_reason
created_at
```

### cognitive_gaps

```text
id
trace_id
retrieval_event_id
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
created_at
```

### knowledge_gaps

```text
id
trace_id
retrieval_event_id
gap_type
query
unit_ids
source_document_ids
reason
required_evidence
severity
created_at
```

### evidence_refs

```text
id
retrieval_event_id
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

### conflict_cases

```text
id
retrieval_event_id
conflict_type
conflicting_claims
conflicting_evidence_ref_ids
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

### mode_switch_decisions

```text
id
retrieval_event_id
current_mode
continue_current_mode
whole_mode_switch
local_mode_escalations
fallback_action
reason
warnings
created_at
```

### learning_signals

```text
id
trace_id
retrieval_event_id
signal_type
question_cluster_id
question_cluster_key
perspective_id
scene
goal
pattern
focus
knowledge_relevance_hints
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

## 006_trace

Trace 表以 `docs/impl/trace.md` 为准：

```text
trace_records
trace_service_calls
trace_activations
trace_retrieval_hits
trace_used_items
trace_evidence_refs
trace_external_evidence_candidates
trace_conflicts
trace_mode_switch_decisions
trace_claim_refs
trace_learning_signals
trace_gaps
```

实现迁移时字段必须覆盖 `docs/impl/trace.md` 的表定义。

## 007_study

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
reason
confidence
status
review_status
review_reason
reviewer
reviewed_at
promoted_target_type
promoted_target_id
created_at
updated_at
```

`candidate_type`：

```text
candidate_domain
candidate_concept
candidate_activation_link
candidate_experience_path
candidate_review_task
import_external_source
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
created_at
```

## 008_external_evidence

### verification_tasks

```text
id
retrieval_event_id
trace_id
query
claims_to_verify
gaps
conflicts
status
last_error
created_at
updated_at
```

### external_evidence_candidates

```text
id
retrieval_event_id
verification_task_id
claim_id
title
url
snippet
source_name
source_type
relation
quoted_text
paraphrase
retrieved_at
fetched_at
search_provider
search_query
credibility
confidence
warnings
created_at
```

外部候选证据不能替代 `evidence_refs`，也不能自动写入 `knowledge_units`。

## Prompt JSON Schemas

第一版 retrieval / precompile 相关模型输出 schema：

```text
schemas/retrieval/problem_framing.schema.json
schemas/retrieval/cognitive_activation.schema.json
schemas/retrieval/activation_link_match.schema.json
schemas/retrieval/document_route_match.schema.json
schemas/retrieval/outline_tree_match.schema.json
schemas/precompile/domain_concept_match.schema.json
schemas/precompile/source_document_summary.schema.json
schemas/unit/knowledge_compile.schema.json
```
