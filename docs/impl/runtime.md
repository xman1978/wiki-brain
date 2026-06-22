# Runtime 实现方案

本文档描述知识大脑第一版的运行时基础能力。

`runtime` 不是业务模块。

它为 source、unit、precompile、retrieval、services、agents、router、trace 和 study 提供统一的工程基础设施。

## 第一版范围

第一版 runtime 覆盖：

```text
config.yaml 配置；
环境变量解析；
LLM client；
prompt 渲染；
JSON Schema 校验；
模型上下文预算；
SQLite 存储；
文件系统存储；
事务边界；
HTTP API 总体约定；
request_id / trace_id；
同请求并行执行；
后台任务和队列；
health check；
错误响应。
```

错误处理遵循：

```text
docs/impl/error-handling.md
```

## config.yaml

runtime 负责加载 `config.yaml`。

第一版默认配置文件路径：

```text
/Users/jxu/Code/knowledge/schema/config.yml
```

如果启动参数或环境变量显式指定配置路径，应优先使用显式路径；否则使用上述默认路径。

建议支持：

```text
WIKI_CONFIG_PATH
--config
```

配置至少包含：

```text
openai
storage
database
paths
jobs
logging
source.convert
app.server
```

`app.server` 控制 HTTP 服务：

```yaml
app:
  server:
    listen_addr: "127.0.0.1:8080"
    url_prefix: ""              # 如 /wiki；页面与 API 共用此前缀
    max_connections: 256        # TCP 连接上限，0 表示不限制
    max_inflight_requests: 64   # 并发请求上限，满时返回 503
```

页面静态资源由 `web/dist` 嵌入提供，运行时通过 `GET {prefix}/__meta.json` 下发 `url_prefix`。

OpenAI 模型配置必须包含：

```text
openai.models.{model_key}.model
openai.models.{model_key}.max_input_tokens
openai.models.{model_key}.max_output_tokens
```

所有与模型上下文限制有关的预算，都从 `max_input_tokens` 和 `max_output_tokens` 推算。

业务模块不应单独配置重复的上下文长度。

模型调用必须使用该配置文件中的 `openai` 配置。

各业务模块只传 `model_key`，由 runtime 解析：

```text
openai.models.{model_key}
```

不得在 source、unit、precompile、retrieval、working model、reasoning 或 study 中硬编码 OpenAI 模型名、上下文长度或 base_url。

环境变量可用于覆盖敏感配置，例如：

```text
OPENAI_API_KEY
```

## LLM Client

runtime 提供统一 LLM client。

业务模块不能直接散落调用外部模型 API。

LLM 调用必须记录：

```text
model_key
model
prompt_version
input_token_estimate
max_input_tokens
max_output_tokens
request_id
trace_id
service_name
```

模型调用原则：

```text
能用规则、索引、统计完成的不调用模型；
调用前先缩小候选；
只传必要上下文；
输出必须经过 JSON Schema 校验；
失败必须转换为 SystemError；
下游不得重复调用模型判断上游已经判断过的语义问题。
```

## Prompt 和 JSON Schema

Prompt 文件放在：

```text
prompts/
```

JSON Schema 文件放在：

```text
schemas/
```

runtime 负责：

```text
读取 prompt；
渲染变量；
记录 prompt_version；
调用模型；
解析结构化输出；
使用 JSON Schema 校验；
返回结构化结果或 SystemError。
```

如果 JSON Schema 校验失败，该结果不能进入业务状态。

## SQLite 存储

第一版使用 SQLite 作为主数据库。

SQLite 用于保存：

```text
source 元数据；
KnowledgeUnit；
KnowledgePoint；
Domain / Concept / ActivationLink；
检索索引和事件；
trace；
study job 和候选；
错误和运行状态。
```

材料侧词法检索使用 Bleve sidecar + gse 分词，详见 `docs/impl/fts.md`。

SQLite 只保存 `*_search_index` 元数据；倒排索引落盘在 `{storage.root}/searchindex`。

向量检索不作为第一版核心依赖。

## 启动初始化

程序启动时必须执行基础初始化。

顺序：

```text
LoadConfig
  -> ValidateConfig
  -> InitFilesystem
  -> InitSQLite
  -> RunMigrations
  -> EnsureDefaultPerspective
  -> ImportPresetDomainsAndConcepts
  -> InitSearchIndex (Open Bleve; RebuildAll if missing or stale)
  -> StartJobRunner
  -> StartHTTPServer
```

约束：

```text
数据库初始化和 migration 必须幂等；
PresetImporter 必须幂等；
预制 Domain / Concept 校验失败时启动失败；
default Perspective disabled 时启动失败；
启动阶段和 precompile job 均不得生成 ActivationLink；ActivationLink 由 retrieval -> trace -> study 根据真实使用证据生成；
启动失败必须返回 SystemError 或写入结构化启动日志。
```

## 文件系统存储

文件系统用于保存：

```text
原始文件；
normalized Markdown；
preview HTML；
中间转换产物；
导出文件。
```

数据库只保存路径、状态、摘要和结构化元数据。

文件路径必须使用统一路径策略，不能由各模块随意拼接。

## 事务边界

runtime 提供数据库事务能力。

原则：

```text
单个业务对象状态更新应在事务内完成；
跨模块长流程不放在一个大事务里；
外部模型调用和网络调用不放在数据库事务内；
索引写入失败不能破坏已提交的源数据；
study 写入 ActivationLink 失败不能删除已写入的材料侧索引或历史 trace；
trace 写入失败不能伪造 trace_id。
```

业务模块应使用事件或状态表达部分成功。

## request_id / trace_id

每次用户请求必须有 `request_id`。

需要记录认知过程时创建或传入 `trace_id`。

约定：

```text
request_id 用于工程请求追踪；
trace_id 用于认知过程追踪；
同一次请求内的并行任务共享 request_id；
同一次认知处理内的 service 共享 trace_id；
后台 job 继承来源 trace_id，并生成自己的 job_id。
```

`request_id` 和 `trace_id` 必须进入：

```text
SystemError；
结构化日志；
service_call；
后台 job；
HTTP 响应或调试信息。
```

## 内部 Result 结构

所有跨模块 service、agent、job 结果必须使用统一内部 `Result` 语义。

结构：

```text
success
status
data
error
warnings
request_id
trace_id
```

字段规则：

```text
success = true 表示该步骤可被下游消费；
success = false 表示该步骤失败，下游不得伪造成功状态；
status 表示业务状态，例如 succeeded / partial / failed / insufficient_evidence / skipped；
data 承载模块结果；
error 必须是 SystemError 或同等字段；
warnings 承载可降级问题；
request_id 必须沿调用链传递；
trace_id 可为空，但为空时不得伪造已写入 trace。
```

内部 `Result` 和 HTTP 响应不是同一个结构。

转换规则：

```text
Result.success = true 且 status != failed
  -> HTTP ok = true

Result.success = true 且 status = partial
  -> HTTP ok = true，warnings 非空，data 中保留 partial 状态

Result.success = false
  -> HTTP ok = false，data = null，error = Result.error

Result.trace_id 为空
  -> HTTP trace_id 也为空或仅返回已确认写入的 trace_id，不得伪造。
```

## HTTP API 总体约定

第一版 API 返回统一结构：

```text
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

```text
{
  "ok": false,
  "data": null,
  "warnings": [],
  "error": SystemError,
  "request_id": "...",
  "trace_id": "..."
}
```

部分成功时：

```text
ok = true
warnings 非空
data 中明确标记部分失败或降级结果
```

## 同请求并行执行

runtime 应支持同一请求内的轻量并行执行。

至少用于 retrieval 中互不依赖的召回路径。

要求：

```text
并行任务共享 request_id；
如果已有 trace_id，并行任务共享 trace_id；
每个任务独立返回结果、warning 和错误；
单个任务失败不自动取消其他任务；
聚合阶段负责合并成功结果和失败信息；
错误必须转换为 SystemError 或同等模块错误结果；
并行任务不得绕过模型上下文预算和模型调用最小化原则。
```

并行执行只用于互不依赖的 service 或索引查询。

依赖上游候选、证据、缺口、冲突或推理结果的步骤必须等待上游完成。

## 后台任务和队列

第一版需要支持轻量后台 job。

至少用于：

```text
StudyJob
```

后台 job 不阻塞用户请求主链路。

通用字段：

```text
job_id
job_type
payload
status
priority
idempotency_key
attempt_count
max_attempts
next_run_at
last_error
created_at
updated_at
```

`status` 可取：

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
支持入队；
支持按状态 claim job；
支持重试；
支持幂等键；
支持记录 SystemError；
job 失败不影响已经返回给用户的回答。
```

`StudyJob` 的具体输入、幂等和状态流转以 `docs/impl/study.md` 为准。

## Health Check

第一版 health check 至少检查：

```text
SQLite 可连接；
必要目录可读写；
config.yaml 可加载；
OpenAI 配置存在；
prompt / schema 目录存在；
后台 job 表可访问。
```

health check 不应主动调用外部模型。

## 错误响应

runtime 负责把模块错误转换为统一错误响应。

跨模块边界必须使用：

```text
SystemError
```

或包含同等字段的模块错误结果。

错误必须包含：

```text
error_code
message
module
operation
stage
target
severity
retryable
request_id
trace_id
details
```

不允许：

```text
吞掉错误；
只打印字符串；
失败后写入成功状态；
伪造 trace_id；
把外部服务原始错误直接暴露给用户。
```

## 验收标准

第一版应满足：

```text
能够加载 config.yaml 和环境变量；
能够统一创建 LLM client；
能够渲染 prompt 并校验 JSON Schema；
能够根据模型配置计算上下文预算；
能够连接 SQLite；
能够管理文件系统路径；
能够提供事务边界；
能够生成并传播 request_id / trace_id；
能够支持 retrieval 并行召回；
能够支持 StudyJob 后台运行；
能够输出统一 API 响应；
能够把错误转换为 SystemError；
health check 能发现基础设施不可用。
```
