# 第一版 API

本文档定义第一版 HTTP API 和内部 job 入口。

所有 HTTP API 必须使用 `docs/impl/runtime.md` 的统一响应：

```json
{
  "ok": true,
  "data": {},
  "warnings": [],
  "error": null,
  "request_id": "...",
  "trace_id": "..."
}
```

失败时：

```json
{
  "ok": false,
  "data": null,
  "warnings": [],
  "error": {},
  "request_id": "...",
  "trace_id": "..."
}
```

`error` 必须是 `SystemError` 或同等字段。

## HTTP API 总表

| 方法 | 路径 | 第一版 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/health` | 必须 | 健康检查 |
| `POST` | `/api/sources/files` | 必须 | 上传文件 source |
| `GET` | `/api/sources/{sourceId}` | 必须 | 查询 source |
| `GET` | `/api/sources/{sourceId}/preview` | 必须 | 读取预览 HTML |
| `GET` | `/api/sources/{sourceId}/markdown` | 必须 | 读取 normalized Markdown |
| `GET` | `/api/sources` | 必须 | source 列表 |
| `DELETE` | `/api/sources/{sourceId}` | 必须 | 逻辑删除 source，并级联失效关联知识 |
| `POST` | `/api/sources/{sourceId}/disable` | 必须 | 临时禁用 source |
| `POST` | `/api/sources/{sourceId}/discard` | 必须 | 废弃 source |
| `POST` | `/api/sources/{sourceId}/restore` | 必须 | 恢复 disabled source |
| `POST` | `/api/query` | 必须 | 用户问题入口，调用 router + agent |
| `GET` | `/api/traces/{traceId}` | 必须 | 查询 trace |
| `GET` | `/api/study/candidates` | 必须 | 查询 study candidates |
| `GET` | `/api/perspectives` | 必须 | 查询可用认知视角（设置页、问题请求） |
| `POST` | `/api/jobs/precompile/retry` | 可选 | 手动重试 precompile job |
| `POST` | `/api/jobs/study/retry` | 可选 | 手动重试 study job |

第一版不要实现：

```text
网页抓取导入 API；
IM 导入 API；
正式 Domain / Concept 创建 API；
候选自动晋升 API；
完整 lifecycle 状态流转 API；
会话列表 / 置顶 / 重命名 / 删除 API；
知识域 / 概念 / 激活链接独立查询 API；
知识单元独立查询 API。
```

## 与页面契约的说明

`docs/impl/page.md` 中部分能力不要求独立 HTTP API，页面须降级实现：

```text
会话壳（列表、置顶、重命名、删除）：前端 localStorage；
多轮对话：仅通过 POST /api/query 的 session_id 创建或续用；
知识域全量浏览：从 AgentResult、trace、study candidates、sources 组装；
知识检查器中的激活链接详情：从 trace.activations、used_knowledge 读取，无独立链接查询 API。
```

第一版 **提供** 多轮会话，但 **不提供** 会话 CRUD。`session_id` 为空时由 `POST /api/query` 创建新 session 并在响应中返回。

## DELETE /api/sources/{sourceId}

第一版删除 source 采用逻辑删除。

行为：

```text
source.status = deleted；
关联 KnowledgeUnit.status = deleted；
关联 KnowledgePoint.status = deleted；
关联 ActivationLink.status = deleted；
关联 unit_search_index / knowledge_point_search_index / outline_search_index.status = deleted；
同步清理 Bleve sidecar 索引（见 docs/impl/fts.md）。
不物理删除 original / html / markdown 文件；
重复调用幂等。
```

返回：

```text
source_id
status = deleted
affected_unit_count
affected_point_count
affected_activation_link_count
affected_index_count
warnings
```

约束：

```text
删除不删除 trace；
删除后 retrieval 不得召回关联知识；
历史 EvidenceRef 只能显示为 unavailable；
study 不得基于 deleted 对象生成候选或强化 ActivationLink。
```

## GET /api/health

检查：

```text
config loaded；
SQLite reachable；
required data directories writable；
prompt/schema files readable；
```

不得主动调用外部模型。

## POST /api/sources/files

参考：

```text
docs/impl/source.md
```

请求：

```text
multipart/form-data
file
title，可为空
intakePurpose，可为空，默认 long_term_candidate
trustLevel，可为空，默认 unknown
originUri，可为空
producedAt，可为空
```

响应 `data`：

```text
sourceId
status
```

规则：

```text
接口不等待 unit/precompile 完成；
长期候选转换成功后由后台投递 unit；
temporary_evidence 不进入 unit；
```

## GET /api/sources/{sourceId}

响应 `data`：

```text
sourceId
title
sourceKind
intakePurpose
trustLevel
status
sourcePageCount
previewHtmlUrl
markdownUrl
pipeline
latestUnitBuildJob，可为空
latestPrecompileJob，可为空
error
warnings
createdAt
updatedAt
```

`pipeline` 供文件抽屉上传进度轮询（见 `docs/impl/page.md` §7.1）。不得伪造进度；各阶段由 `source.status` 与最新 job 记录推导。

`pipeline.stages[]`：

```text
key: upload | intake | markdown | unit_build | precompile
label: 页面展示用中文标签
status: pending | running | completed | failed | skipped
error: SystemError，可为空
updatedAt，可为空
```

阶段推导规则：

| `key` | `completed` | `running` | `pending` | `failed` | `skipped` |
| --- | --- | --- | --- | --- | --- |
| `upload` | 记录已创建 | — | — | — | — |
| `intake` | `status` ≥ `created` 且非导入失败 | — | 刚创建尚未落库完成 | `status = failed` 且失败在 intake 前 | — |
| `markdown` | `converted` / `ready_for_unit` 及之后可用状态 | `converting` | `created` / `original_saved` | 转换阶段 `failed` | — |
| `unit_build` | 最新 job `completed` / `completed_with_review` | job 处于 `pending`…`unit_writing` 或 `retrying` | `ready_for_unit` 但尚无 job | job `failed` | `intakePurpose = temporary_evidence` |
| `precompile` | 最新 job `completed` | job `pending`（执行中同样返回 `pending` 或 `running`，实现二选一但须稳定） | unit 未完成且无 job | job `failed` | `temporary_evidence` 或 unit 被跳过 |

`latestUnitBuildJob`（`intakePurpose = long_term_candidate` 且存在 job 时返回）：

```text
jobId
status
attemptCount
error
updatedAt
```

`unit_build_jobs.status` 取值见 `docs/impl/schema.md`。

`latestPrecompileJob`（unit 构建完成后、存在 job 时返回）：

```text
jobId
status
attemptCount
error
updatedAt
```

`precompile_jobs.status`：

```text
pending
completed
failed
```

`temporary_evidence` 来源不投递 unit / precompile 时，`unit_build` 与 `precompile` 阶段必须为 `skipped`，页面不得显示为等待中。

## GET /api/sources/{sourceId}/preview

返回 `text/html`。

如果 HTML 不存在，返回统一错误响应。

## GET /api/sources/{sourceId}/markdown

返回 `text/markdown`。

第一版不支持 Markdown 版本选择。

## GET /api/sources

支持过滤：

```text
status
intakePurpose
sourceKind
```

响应 `data`：

```json
{
  "sources": []
}
```

`sources[]` 元素与 `GET /api/sources/{sourceId}` 的 `data` 同形（`SourceView`）。

列表项默认包含 `pipeline` 摘要；若实现为减轻负载，可只返回 `pipeline.stages[].key` 与 `status`，详情仍以单条查询为准。

第一版不对 `GET /api/sources` 提供材料全文检索（材料侧词法检索在 retrieval 阶段经 Bleve 完成，见 `docs/impl/fts.md`）。

## GET /api/perspectives

供设置页「默认认知视角」与问题请求 `perspective_id` 下拉使用。

响应 `data`：

```json
{
  "perspectives": [
    {
      "id": "default",
      "name": "默认",
      "description": "",
      "status": "active"
    }
  ]
}
```

规则：

```text
只返回 status = active 的视角；
第一版库中必须存在 id = default；
不提供创建 / 修改 / 删除；
perspective_id 未知时 POST /api/query 返回 perspective_missing 类 warning 并降级为 default。
```

## POST /api/query

用户问题入口。

参考：

```text
docs/impl/session.md
```

请求：

```json
{
  "session_id": null,
  "query": "...",
  "perspective_id": "default",
  "available_context": null,
  "user_intent_hint": null,
  "source_filters": null,
  "outline_filters": null,
  "require_evidence": false,
  "require_external_evidence": false,
  "risk_hint": null,
  "trace_level_hint": null
}
```

字段说明：

| 字段 | 含义 |
| --- | --- |
| `session_id` | 可选。为空时创建新 session；非空时继续多轮对话 |
| `query` | 当前用户问题 |
| `perspective_id` | 认知视角；与已有 session 不一致时沿用 session 绑定值并返回 warning |
| `available_context` | 用户或上游显式提供的附加上下文，不是服务端构造的会话历史 |

处理：

```text
session.Resolve / BuildContext
  -> RouterRequest.AvailableContext（user + RouterContext）
  -> CognitiveRouter
  -> RouterDecision
  -> AgentRequest{
       AvailableContext: user,
       ConversationContext: session,
       TraceContext: session,
     }
  -> RetrievalAgent 或 ReasoningAgent
  -> AgentResult
  -> session.RecordTurnAndMaybeSummarize
  -> TraceService
```

响应 `data` 使用 `AgentResult`，并附加 session 字段：

```text
success
status
error
request_id
answer
mode
used_knowledge
evidence
external_evidence_candidates
working_model，可为空
conflicts
cognitive_gaps
knowledge_gaps
claim_refs
inference_refs
unsupported_claims
learning_signals
mode_switch_request
retrieval_event_id
trace_id
session_id
turn_id
turn_index
warnings
```

`used_knowledge[]`（`FusedResult`）页面与知识检查器依赖字段：

```text
unit_id
knowledge_point_ids
source_document_id
source_spans
title
center
snippet
hit_routes
matched_domain_ids
matched_concept_ids
activation_link_ids
matched_paths
score_breakdown
supporting_hit_routes
rerank_score
rank
fusion_signals
evidence_ready
retrieved
used
used_reason
warnings
```

`evidence[]`（`EvidenceRef`）：

```text
id
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
```

`source_spans` 用于来源查看器高亮；第一版无精确偏移时可为空，页面降级为展示 `quote`（见 `docs/impl/page.md` §8）。

激活链接详情不在 `AgentResult` 顶层展开。页面通过 `used_knowledge[].activation_link_ids` 与 `GET /api/traces/{traceId}` 的 `activations[]` 关联；字段不足时展示链接编号与缺失提示，不要求独立 `GET /api/activation-links/{id}`。

完整 Agent 字段语义见 `docs/impl/thinking-agents.md` 与 `docs/impl/retrieval.md`。

规则：

```text
session_id 不存在时返回 session_not_found；
session 已 archived/deleted 时返回 session_unavailable；
同 session 并发冲突且锁超时时返回 session_busy；
require_evidence=true 且无 EvidenceRef 时，不得返回确定回答；
external_evidence_candidates 不能替代内部 evidence；
AgentResult.status=partial 时 HTTP ok=true，但 warnings 非空；
AgentResult.status=failed 时 HTTP ok=false；
session 摘要或 turn 落库失败时，若主回答已生成，HTTP 仍可 ok=true，但 warnings 非空。
```

会话上下文构造、摘要策略和错误码见 `docs/impl/session.md`。

## GET /api/traces/{traceId}

响应 `data`：

```text
trace_record
service_calls
activations
retrieval_hits
used_items
evidence_refs
gaps
conflicts
claim_refs
learning_signals
```

`trace_record` 可包含 `session_context_meta`（见 `docs/impl/session.md`），记录本次请求使用的 session 摘要与 recent turn 元数据，不包含完整历史会话。

`activations[]` 基础字段（`trace_activations`）：

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

读取 trace 时应 **enrich** 以下可选字段（JOIN `perspectives` / `domains` / `concepts` / `activation_links`），供知识检查器与激活链接面板使用；查不到时省略，页面不得伪造：

```text
perspective_id
perspective_name
domain_name
concept_name
link_type
confidence
activation_count
used_count
positive_feedback_count
negative_feedback_count
relevance_summary
boundary
current_activation_link_status
source_status_at_use
current_source_status
unit_status_at_use
current_unit_status
knowledge_point_status_at_use
current_knowledge_point_status
```

`retrieval_hits[]` 须区分 `retrieved` 与 `used`；`used_items[]` 与 `evidence_refs[]` 字段见 `docs/impl/trace.md`。

用于调试、复盘、study 输入检查与 `docs/impl/page.md` 思考记录 / 知识检查器回放。

## GET /api/study/candidates

过滤：

```text
status
candidate_type
perspective_id
trace_id
```

响应 `data`：

```json
{
  "candidates": []
}
```

`candidates[]` 元素字段见 `docs/impl/study.md`。

第一版只查询候选，不提供自动晋升。

## 内部 Job 入口

以下 job 由后台 runner 执行，不要求都有 HTTP API。

### unit_build

输入：

```text
UnitSourceResult
```

输出：

```text
UnitBuildResult
UnitPrecompileResult
```

### precompile

输入：

```text
PrecompileJob
```

输出：

```text
PrecompileJobResult
```

### study

输入：

```text
StudyJob
```

输出：

```text
StudyJobResult
```

### external_verification

输入：

```text
VerificationTask
```

输出：

```text
VerificationCompletionResult
```

## API 测试要求

至少覆盖：

```text
health 成功；
文件上传返回 sourceId；
source 查询能看到 status 与 pipeline.stages；
上传后轮询 source 详情时 unit_build / precompile 阶段随 job 推进；
temporary_evidence 来源 pipeline 中 unit_build / precompile 为 skipped；
GET /api/perspectives 至少返回 default；
preview / markdown 路径不存在时返回 SystemError；
query 简单检索能返回 trace_id；
query 无 session_id 时创建 session 并返回 session_id / turn_id；
query 带 session_id 时复用会话并追加 turn；
require_evidence=true 且无证据时返回 insufficient_evidence；
trace 查询能看到 retrieved / used；
trace activations enrich 后含 domain_name / concept_name（有链接时）；
study candidates 查询为空时返回空数组而不是错误；
```
