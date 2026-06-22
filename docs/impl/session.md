# Session 与查询上下文实现方案

本文档描述第一版**在线 query 时，辅助问题解析（ProblemFraming）的查询上下文**构造方案。

范围包括：

```text
对话历史（session）；
近邻 trace 认知摘要（trace_context）；
study 聚合历史反馈（feedback_context）；
四类上下文字段的分工、编排与注入。
```

`session` 模块只拥有对话状态；`trace_context` 与 `feedback_context` 由独立 resolver 构造，在 API 层统一编排后注入 `ProblemFramingService`。

第一版重点解决：

```text
记录多轮对话历史；
最近 3 轮 eligible turn 原文注入 conversation_context，更早历史以摘要注入；
摘要第一次全量生成，后续增量更新；
近邻 trace 轻量摘要注入 trace_context；
study 聚合摘要注入 feedback_context；
不在线扫描全量 trace，不在线临时聚合 study 明细；
用规则过滤寒暄与无意义 turn；
摘要或上下文构造失败不得阻断主查询回答。
```

相关文档：

```text
docs/impl/api.md                   — POST /api/query session_id / turn 字段（已同步）
docs/impl/retrieval.md             — ProblemFraming 如何消费各上下文字段
docs/impl/study.md                 — study 聚合来源与 question_cluster 规则
docs/impl/implementation-order.md  — 在 trace 之后、study 之前增加本模块步骤
docs/impl/page.md                  — 会话列表可继续用 localStorage 壳
configs/config.example.yaml          — session 配置段（已同步）
prompts/session/                   — 对话摘要提示词（已新增）
schemas/session/                   — 对话摘要 schema（已新增）
```

## 第一版定位

### 本文管什么

本文管**查询上下文编排**：在 `/api/query` 进入 router / agent 前，为问题维度解析准备输入。

问题维度解析指 `ProblemFramingService` 输出的 `CognitiveRouteContext`：

```text
scene / goal / pattern / focus；
constraints；
knowledge_relevance_hints；
feedback_context；
question_cluster_hint。
```

### 本文不管什么

```text
trace 全量记录与回放 — 见 docs/impl/trace.md；
study 离线作业细节 — 见 docs/impl/study.md；
router 的模式选择算法 — 见 docs/impl/router.md；
回答生成与证据引用。
```

### 第一版不实现

```text
跨用户权限系统；
多设备实时同步；
会话分支和合并；
复杂记忆检索与向量历史会话检索；
长期人格记忆；
自动会话标题生成；
完整会话管理 REST API；
在线扫全量 trace 或全量 study candidate；
PreFramingService（router 前的独立 framing，见下文）。
```

## 四类上下文字段

第一版向下游**分字段**传递，不合并为单一散文块。

| 字段 | 回答的问题 | 来源 | 时间范围 |
| --- | --- | --- | --- |
| `query` | 用户当前问题 | 请求体 | 本轮 |
| `available_context` | 用户附带了什么材料 | 客户端显式传入 | 本轮 |
| `conversation_context` | 这轮对话里已经说过什么 | `session.Service` | 当前 session |
| `trace_context` | 系统最近怎样处理过 | `TraceContextResolver` | 近邻 trace |
| `feedback_context` | 历史上哪些路径有效/无效 | `FeedbackContextResolver` | 跨 session |

### 核心原则

```text
不在问题解析阶段扫全量 trace；
历史 trace 由 study / 离线聚合整理为可快速读取的反馈摘要；
trace_context 与 feedback_context 不得互相替代；
四类上下文不得覆盖 query 的显式意图；
优先级：query > conversation_context > trace_context > feedback_context；
feedback_context 只能是加权信号，不能强行覆盖当前 pattern / mode。
```

### 与 Router / ProblemFraming 的消费关系

当前链路中 **router 在 ProblemFraming 之前**执行。因此：

| 消费者 | 读取字段 | 不读取 |
| --- | --- | --- |
| Router | `query`；用户 `available_context`；`RouterContext`（来自 session） | 完整 `conversation_context`；`trace_context`；`feedback_context` |
| ProblemFraming | 全部四类 + `query` | 完整 trace / evidence / working model |
| ActivationLink 匹配 | `CognitiveRouteContext.FeedbackContext`（来自 framing 输出，输入含 `feedback_context`） | study candidate 原文 |

第一版 **feedback 不参与 router 选 mode**，只服务 framing 与 ActivationLink 加权。

若后续需要 feedback 影响路由，再新增轻量 `PreFramingService`，不在第一版范围。

## 模块与职责

编排与实现分离：

```text
internal/api/query/context.go          — QueryContextBuilder，API 层编排
internal/services/session/           — 对话历史（拥有 sessions 表）
internal/services/tracecontext/      — 近邻 trace 摘要（只读 trace）
internal/services/feedbackcontext/   — study 聚合反馈（只读 study 摘要表）
```

router、retrieval、working model、reasoning **不直接**查询 session / trace / study 明细表。

运行时接入：

```text
cmd/wiki-brain/main.go
  -> session.Service
  -> tracecontext.Resolver(trace.Reader)
  -> feedbackcontext.Resolver(store)
  -> queryapi.Handler(QueryContextBuilder)
```

### QueryContextBuilder

建议接口：

```go
type Builder struct {
    session  *session.Service
    trace    *tracecontext.Resolver
    feedback *feedbackcontext.Resolver
    cfg      *config.Config
}

type BuildInput struct {
    SessionID      string
    Query          string
    PerspectiveID  string
    AvailableContext string
    TraceIDHint    string // 可选，显式指定「从这次 trace 继续」
}

type BuildResult struct {
    SessionID            string
    PerspectiveID        string
    AvailableContext     string
    ConversationContext  string
    TraceContext         string
    FeedbackContext      []string
    QuestionClusterHint  string
    RouterContext        string
    Meta                 QueryContextMeta
    Warnings             []*errors.SystemError
}
```

`session.Service` **不**拥有 `feedback_context` 或跨 session 的 study 数据。

## 在线编排流程

`Handler.query` 推荐流程：

```text
decode request
validate query
resolve perspective
resolve or create session（加 session 锁）
build query context（conversation + trace + feedback）
build RouterRequest
route
run agent（透传 query context 字段）
record session turn
maybe update session summary
释放 session 锁
write response
```

伪代码：

```go
lock := sessionSvc.Lock(ctx, body.SessionID)
defer lock.Release()

sessionState := sessionSvc.Resolve(ctx, session.ResolveInput{
    SessionID:     body.SessionID,
    PerspectiveID: perspective,
})

queryCtx := contextBuilder.Build(ctx, querycontext.BuildInput{
    SessionID:        sessionState.SessionID,
    Query:            body.Query,
    PerspectiveID:    sessionState.PerspectiveID,
    AvailableContext: body.AvailableContext,
    TraceIDHint:      body.TraceID, // 可选
})

routerReq := cognitive.Request{
    RequestID:        requestID,
    Query:            body.Query,
    PerspectiveID:    queryCtx.PerspectiveID,
    AvailableContext: session.MergeForRouter(body.AvailableContext, queryCtx.RouterContext),
    // ... 其余 router 字段
}
decision := router.Route(routerReq)

agentReq := decision.ToAgentRequest(requestID, traceID, routerReq)
agentReq.AvailableContext = body.AvailableContext
agentReq.ConversationContext = queryCtx.ConversationContext
agentReq.TraceContext = queryCtx.TraceContext
agentReq.FeedbackContext = queryCtx.FeedbackContext

result := runAgent(agentReq)

sessionSvc.RecordTurnAndMaybeSummarize(ctx, session.RecordTurnInput{
    SessionID:          sessionState.SessionID,
    RequestID:          requestID,
    TraceID:            traceID,
    UserQuery:          body.Query,
    AssistantAnswer:    result.Answer,
    Success:            result.Success,
    Status:             result.Status,
    QueryContextMeta:   queryCtx.Meta,
})
```

`ProblemFramingService` 在 `RetrievalService.Retrieve()` 内消费上述字段（见 `docs/impl/retrieval.md`）。

## trace_context

### 定位

`trace_context` 是**近邻认知过程摘要**，回答「系统最近怎样理解和处理过」，不是对话原文，也不是跨会话统计。

来源：

```text
默认：当前 session 最近 trace_context_turn_limit 个 succeeded/partial turn 的 trace_id；
可选：请求显式 trace_id（继续某次思考）优先于 session 默认。
```

不在线扫描全量 trace 表。

### 适合注入的内容（分层）

**Layer 1（默认，约 300–800 字）**

```text
question_type / normalized_query（若有）；
scene / goal / pattern；
mode / agent；
cognitive_gaps / knowledge_gaps（仅标题）；
conflicts（是否影响核心回答）；
mode_switch_decision（若有）；
unsupported_claims（若有）。
```

**Layer 2（多 trace 合并时可选）**

```text
answer_summary（一句）；
used_knowledge 一行摘要（名称，无 evidence 原文）；
用户反馈摘要（若 trace 有 feedback）。
```

**明确不注入**

```text
完整 retrieved_items / evidence 原文；
完整 working_model / reasoning chain；
service_calls 细节；
大量历史 answer 全文。
```

不要把原始 `learning_signals` 塞进 `trace_context`；聚合后的信号应进入 `feedback_context`。

### TraceContextResolver

```go
type Resolver struct {
    trace trace.Reader
    cfg   *config.Config
}

func (r *Resolver) Resolve(ctx context.Context, in ResolveInput) (ResolveResult, *errors.SystemError)
```

`ResolveInput`：

```text
session_id
perspective_id
trace_ids[]        — 来自 session_turns 或显式 hint
current_query
```

渲染格式建议：

```text
# Recent Trace Context
Trace tr_abc (turn 8)
Scene: system_design | Goal: generate_plan | Pattern: reasoning
Gaps: session persistence schema missing; summary prompt missing
Answer: ...

Trace tr_def (turn 7)
...
```

失败时 `trace_context` 为空，追加 `trace_context_unavailable` warning，不阻断主请求。

## feedback_context

### 定位

`feedback_context` 是 **study 聚合后的历史经验反馈**，回答「过去哪些路径有效、哪些无效、哪些边界容易错」。

来源链路：

```text
trace / learning_signals / study_events
  -> StudyAggregationJob（离线）
  -> study_feedback_summaries（在线只读）
  -> FeedbackContextResolver（轻量匹配 top 1–3）
  -> feedback_context []string
```

不要把 `study_candidates` 原样注入；candidate 是学习材料，未达阈值前不可当作可信反馈。

### study_feedback_summaries

专门服务在线问题解析，避免每次 query 临时聚合 study / trace 明细。

建议表结构（迁移 `011_study_feedback_summaries.sql` 或与 session 迁移分拆）：

```text
id
question_cluster_id
question_cluster_key
perspective_id
common_scene
common_goal
common_pattern
effective_activation_link_ids
failed_activation_link_ids
boundary_mismatches
recurring_cognitive_gaps
recurring_knowledge_gaps
preferred_mode
positive_feedback_count
negative_feedback_count
summary_json
summary_text
status                  — active / stale
updated_at
```

与已有 `activation_link_route_stats` 的分工：

| 存储 | 粒度 | 主要消费者 |
| --- | --- | --- |
| `activation_link_route_stats` | link × route 计数 | ActivationLink 数值加权 |
| `study_feedback_summaries` | question_cluster 叙事摘要 | ProblemFraming prompt |

### 在线匹配（第一版可规则化）

```text
输入：query + perspective_id + conversation summary 的 recent_focus（若有）
步骤：
  1. perspective_id 过滤 active summaries
  2. question_cluster_key 与 query token 重叠打分
  3. 可用 recent_focus 加权
  4. 取 top 1–3
  5. 渲染为短块
```

第一版不要求向量检索。

### Phase 1 过渡方案

在 `study_feedback_summaries` 未就绪前，`FeedbackContextResolver` 可临时从 `activation_link_route_stats` 按 `perspective_id` 聚合出几行模板摘要。

该逻辑必须可替换，避免固化在 session 模块内。

### 渲染格式

```text
# Historical Feedback
Cluster: Go implementation planning (question_cluster_id: qc_impl_go)
Effective patterns:
- design questions usually require reasoning mode
- API/session changes should inspect schema and query handler first
- activation_link:al_session_schema often matches implementation-order questions

Known failures:
- activation_link:al_quick_retrieval mismatches when user asks for design tradeoffs
- boundary: polite confirmation must not be treated as substantive context

Recurring gaps:
- session persistence schema not yet implemented
```

内部保留 `summary_json`；注入 LLM 用渲染文本。

渲染文本中应包含 **`activation_link:` / `question_cluster_id:`** 等可机器识别标记，供 `historyMatch()` 加权，避免纯散文无法关联 link。

### question_cluster_hint

由 `FeedbackContextResolver` 显式输出 `question_cluster_hint`，写入 framing 输入或 `CognitiveRouteContext`。

不要依赖从 `trace_context` 字符串解析 cluster（现有 `inferQuestionCluster` 仅为过渡）。

### FeedbackContextResolver

```go
type Resolver struct {
    store feedbackcontext.Store
    cfg   *config.Config
}

func (r *Resolver) Resolve(ctx context.Context, in ResolveInput) (ResolveResult, []*errors.SystemError)
```

`ResolveResult`：

```text
FeedbackContext     []string
QuestionClusterHint string
Meta                FeedbackContextMeta
```

失败时 `feedback_context` 为空，追加 `feedback_context_unavailable` warning，不阻断主请求。

## 对话历史（session）

以下章节描述 `session.Service` 负责的**对话状态管理**。

session 记录连续对话中的用户意图、约束、偏好、关键结论和未解决问题，并构造 `conversation_context` 与 `RouterContext`。

session 不是 trace；trace 记录单次认知链路。

### 核心不变量

```text
最近 recent_turn_limit 个 eligible turn 以完整 turn 注入 conversation_context；
更早 eligible 历史只通过 session summary 注入；
摘要覆盖范围不得与 recent window 重复；
原始对话必须完整保存；
规则过滤只作用于上下文构造和摘要输入；
session 摘要失败不得导致 /api/query 失败；
用户显式 available_context 与 conversation_context 分字段传递；
当前 query 只走 query 字段，不写入 recent turns。
```

一轮 turn = 一次 user query + 一次 assistant answer。`failed` turn 默认不进入 recent window 和摘要。

### 模块文件

```text
internal/services/session/
  types.go
  store.go
  service.go
  normalize.go
  summary.go
  merge.go
```

配套：

```text
prompts/session/summarize_full.md
prompts/session/summarize_incremental.md
schemas/session/summary.schema.json
internal/db/migrations/010_session.sql
```

### 配置

```yaml
session:
  recent_turn_limit: 3
  summary_model_key: default
  summary_sync: true
  trace_context_turn_limit: 2
  feedback_cluster_limit: 3
  context_char_limits:
    summary_text: 1200
    recent_user_query: 1000
    recent_assistant_answer: 1600
    recent_turns_total: 6000
    conversation_context_total: 8000
    trace_context_total: 1500
    feedback_context_total: 2000
    router_context_total: 2000
  concurrency:
    lock_timeout_ms: 5000
```

| 配置项 | 含义 |
| --- | --- |
| `recent_turn_limit` | conversation recent window 大小 |
| `summary_model_key` | 对话摘要模型键 |
| `summary_sync` | 是否在 `/api/query` 内同步更新对话摘要 |
| `trace_context_turn_limit` | 默认从 session 取几个 trace 构造 trace_context |
| `feedback_cluster_limit` | feedback 匹配返回的 cluster 数上限 |
| `context_char_limits.*` | 各字段字符预算 |

### 请求入口

`POST /api/query` 见 `docs/impl/api.md`。

可选扩展（后续）：

```json
{
  "trace_id": "tr_..."
}
```

显式 `trace_id` 作为 `trace_context` 的首选来源，表示「从这次思考继续」。

响应附加：`session_id`、`turn_id`、`turn_index`。

### perspective_id 变更

```text
session 无历史 turn：允许更新 session.perspective_id；
已有历史 turn：沿用 session.perspective_id，warning = session_perspective_mismatch。
```

### 并发

同 `session_id` 请求串行化；锁超时返回 `session_busy`。

### Recent Window 与摘要覆盖

窗口按 `turn_index` 排序。

**eligible turn**：`status in (succeeded, partial)` 且 `substance = substantive`（含约束关键词强制 substantive）。

构造第 N+1 轮上下文时，已完成 eligible turns 为 `T1..Tk`：

```text
recent_turns = 最后 min(k, recent_turn_limit) 个 eligible turns；
summary 覆盖 turn_index < min(recent_turns.turn_index) 的所有 eligible turns。
```

落库后挤出窗口时：

```text
newly_evicted = eligible turns 满足 C < turn_index < recent_min_index；
无 active summary -> summarize_full(newly_evicted)；
有 active summary -> summarize_incremental(previous, newly_evicted)；
支持 catch-up（摘要曾 failed 时一次合并多个 newly_evicted）。
```

### conversation_context 渲染

```text
# Conversation Summary
...

# Recent Conversation Turns
Turn 7
User: ...
Assistant: ...
```

使用 normalized 文本；raw 仅存库。

### RouterContext

```text
summary_text；
最近 1 个 eligible turn 的 user query 首句。
```

合并进 `RouterRequest.AvailableContext`，总长不超过 `router_context_total`。

### 规则过滤

允许：压缩空白/标点、删首尾寒暄、识别纯感谢/确认 turn、限制 assistant 注入长度。

不得：删否定词/程度副词、改写约束、用规则覆盖 raw turn。

「好的，先别写代码」等含约束句不得判为 `non_substantive`。

### 对话摘要 schema

见 `schemas/session/summary.schema.json` 与 `prompts/session/summarize_*.md`。

### session 数据库

**sessions**：`id, perspective_id, title, status, created_at, updated_at, ...`

**session_turns**：`id, session_id, turn_index, trace_id, user_query_raw, assistant_answer_raw, normalized 字段, substance, status, ...`

**session_summaries**：`id, session_id, covered_turn_index, covered_turn_count, summary_json, summary_text, status, ...`

详见前文版本；约束不变。

### session.Service 接口

```go
Resolve(ctx, ResolveInput) (ResolveResult, *errors.SystemError)
Lock(ctx, sessionID) (SessionLock, *errors.SystemError)
BuildConversationContext(ctx, BuildConversationInput) (ConversationBuildResult, *errors.SystemError)
RecordTurnAndMaybeSummarize(ctx, RecordTurnInput) (RecordTurnResult, []*errors.SystemError)
```

`BuildConversationContext` 返回 `ConversationContext`、`RouterContext` 及 session 元数据；**不**返回 `trace_context` / `feedback_context`。

## 分字段传递总表

| 阶段 | 字段 |
| --- | --- |
| Router | `AvailableContext = MergeForRouter(user, RouterContext)` |
| Agent / Retrieval | `AvailableContext = user`；`ConversationContext`；`TraceContext`；`FeedbackContext` |

优先级与裁剪：

```text
query 永不裁剪；
conversation_context 超限时优先保留 recent user query；
trace_context / feedback_context 超限时压缩摘要段；
RouterContext 超长时截断 session 段，不截断 user 段。
```

## 与 ProblemFraming / ActivationLink 的关系

问题维度拆解算法（scene / goal / pattern / focus 等 11 步）见 `docs/impl/retrieval.md` § ProblemFramingService / 拆解方法。

`retrieval.Request` 透传：

```text
query
available_context
conversation_context
trace_context
feedback_context
```

`ProblemFramingService` 输出 `CognitiveRouteContext`，其中 `feedback_context` 字段可吸收输入并供下游使用。

`ActivationLink` 匹配中的 `historyMatch()` 读取 `CognitiveRouteContext.FeedbackContext`；因此 feedback 渲染文本应含可识别 id。

第一版 router **不**读取 `feedback_context`。

## Trace 记录

query 使用的上下文元数据写入 trace，不重复保存完整会话或 trace 明细。

`trace_records.session_context_meta` 建议扩展：

```json
{
  "session_id": "ses_...",
  "turn_id": "st_...",
  "turn_index": 9,
  "summary_used": true,
  "summary_covered_turn_index": 6,
  "recent_turn_count": 3,
  "conversation_context_char_count": 4200,
  "router_context_char_count": 800,
  "trace_context_char_count": 600,
  "feedback_context_char_count": 450,
  "question_cluster_hint": "qc_impl_go",
  "feedback_cluster_ids": ["qc_impl_go"],
  "summary_update_status": "updated",
  "warnings": []
}
```

## 会话管理 API（第一版）

不实现完整 CRUD；列表壳可用 localStorage；真实 turn 以 `session_id` / `trace_id` 为准。

## 错误和降级

| code | 含义 | 行为 |
| --- | --- | --- |
| `session_not_found` | session 不存在 | 失败 |
| `session_unavailable` | session 已归档/删除 | 失败 |
| `session_busy` | 锁超时 | 失败，客户端重试 |
| `session_perspective_mismatch` | perspective 不一致 | warning |
| `session_context_build_failed` | 对话上下文失败 | 降级为空，warning |
| `session_turn_record_failed` | turn 落库失败 | warning |
| `session_summary_llm_failed` | 对话摘要失败 | 保留旧摘要，warning |
| `session_summary_output_invalid` | 对话摘要 schema 失败 | 保留旧摘要，warning |
| `trace_context_unavailable` | trace 摘要失败 | 空 trace_context，warning |
| `feedback_context_unavailable` | 反馈摘要失败 | 空 feedback_context，warning |
| `session_context_truncated` | conversation 裁剪 | warning |
| `session_router_context_truncated` | router 裁剪 | warning |
| `trace_context_truncated` | trace_context 裁剪 | warning |
| `feedback_context_truncated` | feedback 裁剪 | warning |

原则：

```text
session 本身不可用 -> 失败；
trace / feedback / 对话上下文构造失败 -> 降级继续；
已生成的主回答不得因 turn 或摘要落库失败而丢弃；
摘要失败不得覆盖旧 active summary。
```

## 测试要求

**对话历史**

```text
session 创建/复用；recent window；摘要全量/增量/catch-up；
non_substantive 过滤；分字段传递；并发锁；失败 turn 隔离。
```

**trace_context**

```text
从 session 最近 trace 构造 Layer 1 摘要；
显式 trace_id 优先；
读取失败降级；不包含 evidence 原文。
```

**feedback_context**

```text
perspective 过滤；top-k cluster 匹配；渲染含 activation_link id；
question_cluster_hint 显式输出；
空反馈不阻断；不把 candidate 原文注入。
```

**编排**

```text
QueryContextBuilder 组装四类字段；
Router 不收到 feedback / 完整 conversation；
ProblemFraming 收到全部字段。
```

建议测试文件：

```text
internal/services/session/
internal/services/tracecontext/
internal/services/feedbackcontext/
internal/api/query/context_test.go
internal/api/query/handler_session_test.go
```

## 实现顺序

```text
Phase 1 — 对话历史
  1. config.session；010_session.sql
  2. session store / normalize / window / BuildConversationContext
  3. RecordTurn；handler 接入 session_id
  4. 对话摘要 prompts / MaybeSummarize
  5. 透传 conversation_context；RouterContext

Phase 2 — trace_context
  6. TraceContextResolver
  7. QueryContextBuilder 编排
  8. 透传 trace_context；trace session_context_meta

Phase 3 — feedback_context
  9. study_feedback_summaries 迁移与 StudyAggregationJob
  10. FeedbackContextResolver（可先读 route_stats 过渡）
  11. 透传 feedback_context；question_cluster_hint
  12. 补齐测试；同步 implementation-order.md

摘要默认同步（session.summary_sync: true）；耗时过高再改 job。
```
