# 错误处理实现规范

本文档描述知识大脑第一版的系统级错误处理规则。

错误处理是知识大脑的基础能力，不属于某一个业务模块。它横跨：

```text
Knowledge Layer
Cognitive Service Layer
Agent Layer
Router Layer
Trace
Study
外部服务集成
HTTP API
后台任务
```

系统不能只在失败时返回一段自然语言错误。每个关键错误都应该能被定位、解释、检索、重试或进入 trace，帮助后续调试、使用和学习。

## 1. 目标

错误处理要回答：

```text
哪里失败了；
为什么失败；
影响了哪个对象；
影响了哪个处理阶段；
是否影响最终回答或长期记忆；
是否可以重试；
用户能看到什么；
开发者如何定位；
trace / study 是否需要记录。
```

错误处理不是为了让所有失败都自动恢复。

第一版重点是：

```text
错误可见；
错误可定位；
错误可分类；
错误可传递；
错误不破坏状态；
错误不被静默吞掉。
```

## 2. 基本原则

### 2.1 不吞错误

任何模块捕获错误后，必须做出以下至少一种处理：

```text
转换为系统错误对象并向上返回；
写入对应业务对象状态；
写结构化日志；
记录到 trace；
明确标记为可忽略。
```

不允许：

```text
catch 后只打印字符串；
catch 后返回空结果；
失败后继续写入成功状态；
丢弃外部服务错误详情；
把所有错误都包装成 unknown。
```

### 2.2 错误码稳定

错误码面向程序、日志检索和测试。

要求：

```text
错误码稳定；
错误码可枚举；
错误码不包含动态内容；
错误码不直接使用外部服务原始错误文本；
错误码命名能表达模块和原因。
```

错误文本面向人阅读，可以包含动态详情。

### 2.3 错误要带上下文

错误上下文至少应包含：

```text
module
operation
stage
target
entity_id
trace_id
request_id
retryable
```

如果调用外部服务，还应包含：

```text
external_service
external_operation
external_status
external_request_id
external_task_id
external_message
```

### 2.4 状态和错误一致

业务状态必须和错误一致。

例如：

```text
source 转换失败，不能保持 converting；
unit 构建失败，不能标记为 built；
retrieval 没有找到证据，不能伪造 evidence；
LLM 输出解析失败，不能当作有效结构化结果使用。
```

如果某个子步骤失败但主对象仍然可用，应该表达为部分成功或待重试，而不是简单失败。

## 3. 统一错误对象

第一版建议定义统一错误对象 `SystemError`。

字段：

```text
error_id
error_code
message
module
operation
stage
target
severity
retryable
entity_type
entity_id
request_id
trace_id
cause_code
external_service
external_operation
external_status
external_request_id
external_task_id
external_message
details
occurred_at
```

字段含义：

| 字段 | 含义 |
| --- | --- |
| `error_id` | 单次错误唯一 id |
| `error_code` | 稳定错误码 |
| `message` | 人可读错误说明 |
| `module` | 发生错误的模块 |
| `operation` | 当前操作 |
| `stage` | 操作内部阶段 |
| `target` | 失败对象或子目标 |
| `severity` | 严重程度 |
| `retryable` | 是否适合重试 |
| `entity_type` | 关联业务对象类型 |
| `entity_id` | 关联业务对象 id |
| `request_id` | HTTP 或任务请求 id |
| `trace_id` | 认知 trace id，可为空 |
| `cause_code` | 下层错误码，可为空 |
| `external_service` | 外部服务名，可为空 |
| `external_operation` | 外部操作名，可为空 |
| `external_status` | 外部状态或 HTTP 状态，可为空 |
| `external_request_id` | 外部请求 id，可为空 |
| `external_task_id` | 外部任务 id，可为空 |
| `external_message` | 外部错误文本，可为空 |
| `details` | JSON 对象，保存结构化补充信息 |
| `occurred_at` | 发生时间 |

`details` 可以保存模块特有字段，但不要把核心字段都塞进 `details`。

## 4. 模块和阶段

### 4.1 module

建议模块取值：

```text
source
unit
precompile
retrieval
memory
service
agent
router
trace
study
lifecycle
llm
storage
database
http
config
```

### 4.2 severity

建议严重程度：

```text
info
warning
error
critical
```

含义：

| 值 | 含义 |
| --- | --- |
| `info` | 非失败事件，仅用于说明 |
| `warning` | 部分失败或可降级 |
| `error` | 当前操作失败 |
| `critical` | 影响系统继续运行或可能破坏数据 |

### 4.3 stage

`stage` 由模块定义。

示例：

```text
source.save_original
source.submit_convert
source.poll_convert
source.download_artifact
unit.parse_markdown
unit.extract_units
unit.persist_units
retrieval.search
retrieval.fetch_page
retrieval.extract_content
llm.render_prompt
llm.call_model
llm.parse_output
trace.persist
```

第一版不要求所有模块一次性定义完整阶段，但新模块实现时必须定义自己的关键阶段。

## 5. 错误码命名规则

错误码使用 snake_case。

推荐格式：

```text
{module}_{reason}
```

或：

```text
{module}_{operation}_{reason}
```

示例：

```text
source_convert_service_unavailable
source_markdown_convert_failed
unit_markdown_parse_failed
unit_empty_content
retrieval_search_failed
retrieval_page_fetch_timeout
llm_call_timeout
llm_output_parse_failed
database_write_failed
trace_persist_failed
```

不建议：

```text
failed
unknown
bad_request
exception
convert error
HTTP 500
```

允许有兜底错误码，但必须带模块：

```text
source_unknown_error
unit_unknown_error
llm_unknown_error
```

## 6. 错误传播

### 6.1 模块内部

模块内部可以使用语言原生错误。

跨模块边界时，必须转换为 `SystemError` 或包含同等字段的模块错误结果。

例如：

```text
SourceImportService
  -> 返回 SourceImportResult
  -> result.error 使用 SystemError 字段

UnitBuildService
  -> 返回 UnitBuildResult
  -> result.error 使用 SystemError 字段
```

### 6.2 层之间

Knowledge Layer 错误：

```text
必须保留业务对象 id；
必须更新对象状态；
必须写结构化日志；
需要时返回给上层 service。
```

Service 错误：

```text
必须说明是输入不足、知识缺失、工具失败、LLM 失败还是解析失败；
不能把所有失败都变成“无法回答”。
```

Agent 错误：

```text
必须判断是否可降级；
必须决定是否切换模式；
必须决定是否记录完整 trace；
不能让单个 service 的失败无解释地中断整次问题处理。
```

Router 错误：

```text
如果路由失败，应使用默认安全模式；
应记录 router 错误；
不能直接丢弃用户问题。
```

## 7. 结果对象

第一版推荐每个关键流程返回结果对象，而不是只返回数据或错误。

通用结构：

```text
success
status
data
error
warnings
trace_id
```

示例：

```json
{
  "success": false,
  "status": "failed",
  "data": null,
  "error": {
    "error_code": "unit_markdown_parse_failed",
    "message": "failed to parse markdown heading structure",
    "module": "unit",
    "stage": "unit.parse_markdown",
    "entity_type": "source",
    "entity_id": "src_01hxyz",
    "retryable": false
  },
  "warnings": [],
  "trace_id": "tr_01habc"
}
```

对于部分成功：

```json
{
  "success": true,
  "status": "partial",
  "data": {},
  "error": null,
  "warnings": [
    {
      "error_code": "retrieval_page_fetch_timeout",
      "message": "one evidence page timed out",
      "module": "retrieval",
      "stage": "retrieval.fetch_page",
      "retryable": true
    }
  ]
}
```

## 8. 持久化规则

### 8.1 业务对象保存最近错误

有长期状态的业务对象应保存最近一次错误。

例如：

```text
SourceDocument
KnowledgeUnitBuild
Trace
LearningSuggestion
LifecycleCheck
```

建议字段：

```text
last_error_code
last_error_message
last_error_stage
last_error_at
retryable
```

如果模块已有更具体命名，也可以使用：

```text
error_code
error_message
error_stage
failed_at
```

### 8.2 错误事件历史

第一版不强制所有错误入库成历史表。

但以下错误必须至少进入结构化日志：

```text
外部服务调用失败；
数据库写入失败；
文件写入失败；
LLM 调用失败；
LLM 输出解析失败；
对象状态转换失败；
trace 持久化失败；
unit 构建失败。
```

后续如果需要错误历史查询，可以增加：

```text
error_events
```

字段可以直接复用 `SystemError`。

## 9. 日志规则

日志用于工程运行观测，不替代 trace。

每条错误日志必须包含：

```text
error_id
error_code
message
module
operation
stage
severity
retryable
entity_type
entity_id
request_id
trace_id
occurred_at
```

如果有外部服务：

```text
external_service
external_operation
external_status
external_request_id
external_task_id
external_message
```

日志要求：

```text
使用结构化日志；
不要只写自然语言；
不要只记录 stack trace；
不要在日志中泄露 API key、token、cookie、用户隐私原文；
同一次请求或任务使用同一个 request_id；
同一次认知处理使用同一个 trace_id。
```

## 10. Trace 规则

trace 不是日志。

但影响认知过程的错误必须进入 trace。

需要进入 trace 的错误：

```text
问题理解失败；
路由失败或降级；
长期记忆激活失败；
检索失败；
外部证据获取失败；
证据冲突无法解决；
LLM 输出无法解析；
推理过程被中断；
回答被用户纠正；
source 未能进入知识切块；
unit 构建失败导致知识缺口。
```

不一定进入 trace 的错误：

```text
单次日志写入失败；
非关键后台清理失败；
不会影响当前认知结果的临时网络重试。
```

进入 trace 时，不需要记录完整 stack trace，但应记录：

```text
error_code
module
stage
影响了什么认知步骤
系统如何降级或停止
是否生成学习建议
```

## 11. API 响应规则

HTTP API 错误响应使用统一结构。

```json
{
  "code": "source_markdown_convert_failed",
  "message": "Markdown conversion failed",
  "requestId": "req_01hxyz",
  "traceId": "tr_01habc",
  "retryable": false,
  "details": {
    "sourceId": "src_01hxyz",
    "stage": "source.poll_convert"
  }
}
```

规则：

```text
HTTP status 表示协议层结果；
code 表示业务错误码；
message 可以给用户或开发者阅读；
details 不返回敏感信息；
requestId 必须返回，方便查日志；
traceId 有则返回，没有可以为空。
```

示例：

```text
400 参数错误；
404 对象不存在；
409 状态冲突；
422 输入可解析但无法处理；
500 系统内部错误；
502 外部服务失败；
504 外部服务超时。
```

## 12. 重试和降级

错误必须标记 `retryable`。

适合重试：

```text
外部服务临时不可用；
网络超时；
队列投递失败；
数据库临时锁；
LLM 限流。
```

不适合重试：

```text
文件格式不支持；
Markdown 内容为空；
LLM 输出违反 schema 且重试后仍失败；
用户输入参数非法；
业务状态不允许当前操作。
```

降级示例：

```text
retrieval 搜索失败，可以只使用已有长期记忆，但回答要说明证据不足；
router 失败，可以进入默认 ReasoningAgent；
某个 evidence 页面抓取失败，可以保留其他 evidence；
trace 写入失败，不能伪造 trace_id，应返回 trace 记录失败警告。
```

## 13. 模块最低要求

每个模块实现文档至少要说明：

```text
本模块有哪些关键阶段；
本模块有哪些稳定错误码；
哪些错误会更新业务对象状态；
哪些错误会进入 trace；
哪些错误可重试；
失败时传给下一阶段什么结果。
```

第一版重点模块：

```text
source
unit
precompile
retrieval
llm
trace
agent
router
```

## 14. 结构化模型输出的局部容错

LLM 返回数组型决策时，单条无效记录与整次调用失败必须区分处理。

处理顺序为：JSON 解析、schema 校验、ID 白名单校验、枚举校验、引用完整性校验。可以定位到具体 item 的错误只丢弃该 item，并记录包含索引、引用 ID 和原因的 warning；其余有效 item 继续进入下游，结果状态为 `partial`。

只有无法解析顶层 JSON、顶层结构完全无效、全部 item 无效或调用失败时，才执行整次降级。重试请求只包含失败项及简短校验错误，不应要求模型重新生成已经通过校验的项目。

模型不得通过重试创建输入中不存在的 ID。任何未知 ID、跨事实引用的 entity ID 或非法枚举都必须由代码拒绝，不能依靠后续 Prompt 修复。

## 15. 测试要求

错误处理必须有测试。

至少覆盖：

```text
外部服务不可用；
外部服务返回失败；
外部服务超时；
文件写入失败；
数据库写入失败；
LLM 返回非 JSON；
LLM 返回不符合 schema；
状态冲突；
部分成功；
可重试错误；
不可重试错误。
```

测试不仅检查是否失败，还要检查：

```text
error_code 是否正确；
error_stage 是否正确；
retryable 是否正确；
业务状态是否正确；
日志或结果对象是否包含 request_id / trace_id；
失败是否没有产生错误的下游数据。
```

## 16. 和 source 的关系

`docs/impl/source.md` 中的 `SourceError` 是 `SystemError` 在 source 模块中的具体应用。

source 可以保留自己的字段名，例如：

```text
error_code
error_message
error_stage
error_target
retryable
external_status
external_message
```

但语义必须符合本文档。

后续 `unit`、`precompile`、`service`、`retrieval`、`agent` 等实现文档，也应按本文档定义自己的错误阶段、错误码和结果传递规则。
