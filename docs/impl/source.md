# Source 实现方案

本文档描述知识大脑第一版中 `source` 模块的实现方案。

`source` 是外部知识输入的工程入口。它负责接收外部材料，调用文件转换服务生成 HTML 和 Markdown，保存转换后的副本，并保留来源上下文，为后续知识单元构建提供稳定输入。

第一版目标是验证系统基本能力，不实现复杂的人工作业流、网页抓取、搜索引擎接入和长期知识质量管理。

同时，第一版不实现文件版本和知识版本。

也就是说，同一个 `SourceDocument` 只对应一份当前原始文件、一份当前 HTML 副本和一份当前 Markdown 副本。后续知识单元、认知结构或长期记忆如果发生变化，也不在第一版中维护版本历史。

## 1. 设计目标

`source` 第一版要解决的问题是：

```text
外部文件
  -> 保存原始材料
  -> 转换为 HTML 和 Markdown
  -> 保存转换产物副本
  -> 保留来源上下文
  -> 区分长期候选材料和临时证据材料
  -> 为 unit 构建或 working model 提供输入
```

它不是知识理解模块。

它不负责：

```text
切分知识单元；
抽取概念；
形成稳定认知结构；
判断知识是否长期有效；
执行知识检索和证据查找；
回答用户问题。
```

这些能力分别由 `unit`、`precompile`、`retrieval service`、`agent` 和 `lifecycle` 等模块承担。

## 2. 第一版范围

第一版只实现文件类材料导入。

支持流程：

```text
用户上传文件
  -> 创建 SourceDocument
  -> 保存原始文件
  -> 调用文件转换服务生成 HTML
  -> 调用文件转换服务生成 Markdown
  -> 轮询转换任务
  -> 拉取并保存 HTML / Markdown 副本
  -> 更新 source 状态
  -> 如为长期候选材料，进入 unit 构建队列
```

暂不实现：

```text
搜索引擎查询；
网页抓取；
网页正文抽取；
IM 聊天记录导入；
Agent trace 自动沉淀；
人工修正 Markdown；
Markdown 版本对比；
文件版本管理；
知识版本管理；
知识质量审核工作台；
材料自动晋升为长期记忆。
```

搜索引擎和网站获取属于 `retrieval` 阶段的外部证据检索能力。检索阶段获取到的网页或搜索结果，后续仍应登记为 `SourceDocument`，但其获取动作不放在 `source` 第一版中实现。

## 3. 和文件转换服务的关系

知识大脑依赖现有文件转换服务完成文档转换。

转换服务接口：

```text
POST /api/convert/html
POST /api/convert/markdown
GET  /api/task/{taskId}
```

转换服务职责：

```text
接收文件；
执行 HTML 转换；
执行 Markdown 转换；
提供任务状态；
提供转换结果下载地址。
```

知识大脑职责：

```text
保存原始文件；
创建 source 记录；
调用转换服务；
保存转换任务 id；
轮询转换状态；
下载 HTML / Markdown；
保存 HTML / Markdown 副本；
维护 source 状态；
决定是否进入 unit 构建。
```

转换服务不作为知识大脑的长期存储。

原因：

```text
转换服务任务元数据是内存态；
转换服务重启后任务列表不持久化；
知识大脑需要长期追溯来源证据；
知识大脑需要独立管理材料生命周期。
```

因此，转换完成后，知识大脑必须拉取 HTML 和 Markdown，并保存到自己的文件系统中。

## 4. 文件存储

第一版使用文件系统保存原始文件和转换产物。

建议目录：

```text
data/
  sources/
    original/
      {source_id}{ext}
    html/
      {source_id}.html
    markdown/
      {source_id}.md
    meta/
      {source_id}.json
```

说明：

```text
original 保存原始文件副本；
html 保存预览 HTML；
markdown 保存规范化 Markdown；
meta 可选，用于调试和导出，不替代数据库记录。
```

数据库是 source 元数据的主存储。文件系统保存大文件和可人工查看的文本产物。

## 5. 核心对象

### 5.1 SourceDocument

`SourceDocument` 是外部材料进入知识大脑后的主记录。

字段建议：

```text
id
title
original_filename
mime_type
file_extension
source_kind
intake_purpose
trust_level
origin_uri
produced_at
imported_at
status
source_page_count
original_path
preview_html_path
normalized_markdown_path
html_convert_task_id
markdown_convert_task_id
error_message
created_at
updated_at
```

字段含义：

| 字段 | 含义 |
| --- | --- |
| `id` | source 全局唯一 id |
| `title` | 材料标题，默认可由文件名生成 |
| `original_filename` | 用户上传时的原始文件名 |
| `mime_type` | 上传文件 MIME 类型 |
| `file_extension` | 文件扩展名 |
| `source_kind` | 来源类型 |
| `intake_purpose` | 导入用途 |
| `trust_level` | 来源可信等级 |
| `origin_uri` | 原始来源地址，可为空 |
| `produced_at` | 材料产生时间，可为空 |
| `imported_at` | 导入系统时间 |
| `status` | source 当前状态 |
| `source_page_count` | 原文页数或等价数量，可为空 |
| `original_path` | 原始文件保存路径 |
| `preview_html_path` | HTML 副本路径 |
| `normalized_markdown_path` | Markdown 副本路径 |
| `html_convert_task_id` | HTML 转换任务 id |
| `markdown_convert_task_id` | Markdown 转换任务 id |
| `error_message` | 失败原因 |

### 5.2 source_kind

第一版主要使用：

```text
file
```

预留值：

```text
webpage
search_result
chat
agent_trace
note
```

预留值不代表第一版要实现对应导入能力。

### 5.3 intake_purpose

`intake_purpose` 用于区分材料进入系统后的用途。

取值：

```text
long_term_candidate
temporary_evidence
```

含义：

| 值 | 含义 |
| --- | --- |
| `long_term_candidate` | 长期记忆候选材料，转换完成后可进入 unit 构建 |
| `temporary_evidence` | 当前问题或临时任务的证据材料，默认不进入长期记忆 |

同一种来源类型可以有不同用途。

例如：

```text
PDF + long_term_candidate
  表示用户主动导入的一份长期学习材料。

PDF + temporary_evidence
  表示本次问题临时使用的一份证据材料。
```

### 5.4 trust_level

`trust_level` 表示来源可信方式，不表示内容一定正确。

第一版建议取值：

```text
personal
official
internal
web
search_snippet
unknown
```

第一版只保存该字段，不基于它做复杂推理。

后续 `retrieval`、`reasoning`、`lifecycle` 可以使用该字段辅助判断证据权重和复核需求。

## 6. 状态机

`source` 状态不直接等同于转换服务 task 状态。

建议状态：

```text
created
original_saved
converting
converted
ready_for_unit
preview_ready_but_not_learnable
failed
archived
disabled
discarded
deleted
```

状态含义：

| 状态 | 含义 |
| --- | --- |
| `created` | 已创建 source 记录 |
| `original_saved` | 原始文件已保存 |
| `converting` | HTML / Markdown 转换任务已提交或执行中 |
| `converted` | HTML 和 Markdown 已归档，但不一定进入 unit 构建 |
| `ready_for_unit` | 长期候选材料已具备 unit 构建条件 |
| `preview_ready_but_not_learnable` | HTML 可用，但 Markdown 不可用 |
| `failed` | 导入或转换失败 |
| `archived` | 材料已归档，不再进入当前流程 |
| `disabled` | 用户或系统临时禁用 source，可恢复，不进入新 unit 构建和检索 |
| `discarded` | 已判断不应继续作为知识来源，保留历史追溯，不参与新处理 |
| `deleted` | 用户删除 source 后的逻辑删除状态，不参与任何新处理、新检索、新学习 |

状态转换：

```text
created
  -> original_saved
  -> converting
  -> converted
  -> ready_for_unit
```

如果 `intake_purpose = temporary_evidence`：

```text
created
  -> original_saved
  -> converting
  -> converted
```

临时证据材料到 `converted` 即可被 trace 或 working model 使用，默认不进入 `ready_for_unit`。

可用性状态转换：

```text
converted / ready_for_unit
  -> disabled
  -> converted / ready_for_unit

converted / ready_for_unit / disabled
  -> discarded

任何非 deleted 状态
  -> deleted
```

约束：

```text
disabled 可恢复；
discarded 需要人工 review 才能恢复；
deleted 默认不可恢复；
deleted 不物理删除历史 trace；
deleted 不应再投递 unit / precompile / study。
```

如果 HTML 成功但 Markdown 失败：

```text
converting
  -> preview_ready_but_not_learnable
```

如果原始文件保存、任务提交、任务轮询或产物下载失败：

```text
created / original_saved / converting
  -> failed
```

## 7. 导入流程

### 7.1 创建 source

接收上传请求后，系统先创建 `SourceDocument`。

默认值：

```text
source_kind = file
intake_purpose = long_term_candidate
trust_level = unknown
status = created
```

第一版不创建文件版本记录。重复导入同一个文件时，系统将其视为新的 `SourceDocument`，而不是旧 source 的新版本。

如果调用方明确传入 `intake_purpose = temporary_evidence`，则按临时证据处理。

### 7.2 保存原始文件

原始文件保存到：

```text
data/sources/original/{source_id}{ext}
```

保存成功后更新：

```text
original_path
status = original_saved
```

### 7.3 提交转换任务

系统对同一个原始文件提交两个转换任务：

```text
POST /api/convert/html
POST /api/convert/markdown
```

Markdown 转换参数：

```text
markdownToc = true
```

提交成功后保存：

```text
html_convert_task_id
markdown_convert_task_id
status = converting
```

### 7.4 轮询任务状态

通过以下接口轮询：

```text
GET /api/task/{taskId}
```

HTML 任务完成后返回：

```json
{
  "taskId": "c1c4d1ab",
  "status": "finished",
  "sourcePageCount": 12,
  "htmlUrl": "/html/c1c4d1ab.html"
}
```

Markdown 任务完成后返回：

```json
{
  "taskId": "c1c4d1ab",
  "status": "finished",
  "sourcePageCount": 12,
  "markdownToc": true,
  "markdownUrl": "/html/c1c4d1ab.md"
}
```

轮询策略第一版保持简单：

```text
固定间隔轮询；
设置最大等待时间；
失败时记录错误；
不引入独立消息队列。
```

建议配置：

```yaml
source:
  convert:
    poll_interval_seconds: 2
    timeout_seconds: 300
```

### 7.5 下载并保存转换产物

转换完成后，系统从转换服务返回的 URL 下载产物，并保存到：

```text
data/sources/html/{source_id}.html
data/sources/markdown/{source_id}.md
```

保存成功后更新：

```text
preview_html_path
normalized_markdown_path
source_page_count
status = converted
```

`source_page_count` 可以来自 HTML 或 Markdown 任务响应。若两者都有且不一致，第一版以 Markdown 任务返回值为准，并记录结构化日志。

### 7.6 投递后续流程

如果：

```text
intake_purpose = long_term_candidate
status = converted
normalized_markdown_path 不为空
```

则更新：

```text
status = ready_for_unit
```

并投递到 unit 构建队列。

如果：

```text
intake_purpose = temporary_evidence
status = converted
```

则不进入 unit 构建队列。

它可以被当前 trace、working model 或 retrieval 结果引用。

## 8. HTTP API

第一版提供最小 HTTP API。

### 8.1 上传文件

```text
POST /api/sources/files
```

请求：

```text
multipart/form-data
```

参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `file` | file | 是 | 上传文件 |
| `title` | string | 否 | 材料标题 |
| `intakePurpose` | string | 否 | 默认 `long_term_candidate` |
| `trustLevel` | string | 否 | 默认 `unknown` |
| `originUri` | string | 否 | 原始来源地址 |
| `producedAt` | string | 否 | 材料产生时间 |

响应：

```json
{
  "sourceId": "src_01hxyz",
  "status": "converting"
}
```

上传接口可以同步完成原始文件保存和转换任务提交，但不等待转换完成。

### 8.2 查询 source

```text
GET /api/sources/{sourceId}
```

响应：

```json
{
  "sourceId": "src_01hxyz",
  "title": "example.pdf",
  "sourceKind": "file",
  "intakePurpose": "long_term_candidate",
  "trustLevel": "unknown",
  "status": "ready_for_unit",
  "sourcePageCount": 12,
  "previewHtmlUrl": "/api/sources/src_01hxyz/preview",
  "markdownUrl": "/api/sources/src_01hxyz/markdown",
  "createdAt": "2026-06-13T10:00:00Z",
  "updatedAt": "2026-06-13T10:01:20Z"
}
```

### 8.3 读取预览 HTML

```text
GET /api/sources/{sourceId}/preview
```

返回：

```text
text/html
```

如果 HTML 不存在，返回 `404`。

### 8.4 读取 Markdown

```text
GET /api/sources/{sourceId}/markdown
```

返回：

```text
text/markdown
```

第一版固定返回 `normalized_markdown_path`。

由于第一版不实现 Markdown 版本和人工修正，接口不提供版本选择参数。

### 8.5 删除、禁用和废弃 Source

第一版删除采用逻辑删除。

建议接口：

```text
DELETE /api/sources/{sourceId}
POST /api/sources/{sourceId}/disable
POST /api/sources/{sourceId}/discard
POST /api/sources/{sourceId}/restore
```

`DELETE` 行为：

```text
SourceDocument.status = deleted；
关联 KnowledgeUnit.status = deleted；
关联 KnowledgePoint.status = deleted；
关联 ActivationLink.status = deleted；
关联 unit_search_index / knowledge_point_search_index / outline_search_index.status = deleted；
同步 DeleteBySource 清理 Bleve 索引（见 docs/impl/fts.md）；
写 source delete event 或结构化日志；
返回 affected_unit_count / affected_point_count / affected_activation_link_count / affected_index_count；
重复删除必须幂等。
```

`disable` 行为：

```text
SourceDocument.status = disabled；
关联 KnowledgeUnit / KnowledgePoint / index status = disabled；
关联 ActivationLink.status = disabled；
可通过 restore 恢复。
```

`discard` 行为：

```text
SourceDocument.status = discarded；
关联 KnowledgeUnit / KnowledgePoint / index status = discarded；
关联 ActivationLink.status = discarded；
恢复必须经过 review。
```

文件物理删除不是默认行为。

第一版默认只逻辑删除数据库状态，保留 original / html / markdown 文件以支持历史 trace 复盘。

如果后续需要物理清理，应使用独立配置和后台 purge job：

```text
source.delete.purge_files = false
```

### 8.6 列表查询

```text
GET /api/sources
```

支持基础过滤：

```text
status
intakePurpose
sourceKind
```

第一版不对 `GET /api/sources` 提供材料全文检索（材料侧词法检索在 retrieval 阶段经 Bleve 完成，见 `docs/impl/fts.md`）。

## 9. 数据库表

第一版可以使用单表保存 source 元数据。

```sql
CREATE TABLE source_documents (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  original_filename TEXT NOT NULL,
  mime_type TEXT,
  file_extension TEXT,
  source_kind TEXT NOT NULL,
  intake_purpose TEXT NOT NULL,
  trust_level TEXT NOT NULL,
  origin_uri TEXT,
  produced_at TEXT,
  imported_at TEXT NOT NULL,
  status TEXT NOT NULL,
  source_page_count INTEGER,
  original_path TEXT,
  preview_html_path TEXT,
  normalized_markdown_path TEXT,
  html_convert_task_id TEXT,
  markdown_convert_task_id TEXT,
  error_code TEXT,
  error_message TEXT,
  error_stage TEXT,
  error_target TEXT,
  retryable INTEGER NOT NULL DEFAULT 0,
  external_status TEXT,
  external_message TEXT,
  failed_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

建议索引：

```sql
CREATE INDEX idx_source_documents_status
  ON source_documents(status);

CREATE INDEX idx_source_documents_intake_purpose
  ON source_documents(intake_purpose);

CREATE INDEX idx_source_documents_source_kind
  ON source_documents(source_kind);

CREATE INDEX idx_source_documents_imported_at
  ON source_documents(imported_at);
```

第一版可以先不拆分转换任务表。

如果后续需要更强的任务追踪，再引入：

```text
source_conversion_tasks
```

用于保存每个转换任务的目标格式、状态、重试次数和错误详情。

## 10. 配置

`config.yaml` 增加：

```yaml
source:
  storage:
    original_dir: data/sources/original
    html_dir: data/sources/html
    markdown_dir: data/sources/markdown
    meta_dir: data/sources/meta

  convert:
    base_url: http://192.168.0.169:9000
    markdown_toc: true
    poll_interval_seconds: 2
    timeout_seconds: 300
    max_retries: 2

  delete:
    purge_files: false
```

说明：

```text
base_url 指向文件转换服务，第一版默认使用 `http://192.168.0.169:9000`；
markdown_toc 第一版固定为 true；
poll_interval_seconds 控制轮询间隔；
timeout_seconds 控制单个 source 转换等待上限；
max_retries 控制转换任务提交和产物下载重试，不控制转换服务内部重试。
delete.purge_files 第一版默认 false，删除 source 时只做逻辑删除，不物理删除 original / html / markdown。
```

## 11. 错误处理

source 模块的错误处理遵循系统级错误规范：

```text
docs/impl/error-handling.md
```

本节只描述该规范在 source 模块中的具体落地。

第一版错误处理必须支持后续调试和使用过程中的问题定位。

错误处理目标不是只告诉用户“失败了”，而是能回答：

```text
失败发生在哪个阶段；
失败的是哪个 source；
失败的是 HTML 还是 Markdown；
关联的转换任务 id 是什么；
外部服务返回了什么；
是否已经产生了部分可用产物；
是否可以重试；
失败是否影响后续 unit 构建。
```

常见错误：

```text
原始文件保存失败；
文件类型不支持；
转换服务不可用；
HTML 转换失败；
Markdown 转换失败；
转换超时；
产物下载失败；
产物保存失败。
```

### 11.1 SourceError

建议定义统一的 `SourceError` 结构，用于数据库、日志、API 响应和传递给下一阶段的结果消息。

字段：

```text
error_code
error_message
error_stage
error_target
retryable
external_task_id
external_status
external_message
source_status
occurred_at
```

字段含义：

| 字段 | 含义 |
| --- | --- |
| `error_code` | 稳定错误码，供程序判断和搜索日志 |
| `error_message` | 面向开发和使用者的错误说明 |
| `error_stage` | 错误发生阶段 |
| `error_target` | 错误对象，例如 `original`、`html`、`markdown` |
| `retryable` | 是否适合重试 |
| `external_task_id` | 文件转换服务任务 id，可为空 |
| `external_status` | 外部任务状态或 HTTP 状态，可为空 |
| `external_message` | 外部服务返回的错误信息，可为空 |
| `source_status` | 错误发生后 source 状态 |
| `occurred_at` | 错误发生时间 |

### 11.2 error_stage

`error_stage` 用于定位失败发生在哪个环节。

建议取值：

```text
create_source
save_original
submit_html_convert
submit_markdown_convert
poll_html_convert
poll_markdown_convert
download_html
download_markdown
save_html
save_markdown
mark_ready_for_unit
enqueue_unit
```

### 11.3 error_code

第一版建议定义稳定错误码。

```text
source_create_failed
original_save_failed
unsupported_file_type
convert_service_unavailable
html_convert_submit_failed
markdown_convert_submit_failed
html_convert_failed
markdown_convert_failed
html_convert_timeout
markdown_convert_timeout
html_download_failed
markdown_download_failed
html_save_failed
markdown_save_failed
unit_enqueue_failed
unknown_source_error
```

错误码应该稳定，不要把外部服务的原始错误文本直接当作错误码。

外部错误文本保存到：

```text
external_message
```

### 11.4 数据库存储

第一版可以先把最近一次错误保存到 `source_documents` 表中。

建议字段：

```text
error_code
error_message
error_stage
error_target
retryable
external_status
external_message
failed_at
```

如果后续需要完整错误历史，再增加：

```text
source_errors
```

第一版不需要完整错误历史表，但结构化日志中必须保留每次错误事件。

处理原则：

```text
失败必须更新 source 状态；
失败必须保存 error_code；
失败必须保存 error_message；
失败必须保存 error_stage；
失败必须写结构化日志；
不要静默丢弃 source；
不要在数据库中保存半真实状态。
```

特殊情况：

```text
HTML 成功，Markdown 失败：
  status = preview_ready_but_not_learnable
  preview_html_path 有值
  normalized_markdown_path 为空
  不进入 unit 构建

Markdown 成功，HTML 失败：
  status = failed
  第一版不允许无预览材料进入长期候选流程
```

原因是 HTML 是未来证据回看和人工核对的重要入口。第一版要求长期候选材料同时具备预览和 Markdown。

如果 unit 队列投递失败：

```text
status = ready_for_unit
error_code = unit_enqueue_failed
error_stage = enqueue_unit
retryable = true
```

此时 source 本身已经转换成功，不能回退为转换失败。系统应允许后续重新投递 unit 构建。

## 12. 日志与可观测性

source 模块应记录结构化日志。

关键事件：

```text
source_created
source_original_saved
source_convert_submitted
source_convert_poll
source_convert_finished
source_artifact_downloaded
source_ready_for_unit
source_failed
```

日志字段：

```text
source_id
source_kind
intake_purpose
status
source_status
html_convert_task_id
markdown_convert_task_id
event
stage
target
error_code
error_message
external_status
external_message
retryable
duration_ms
created_at
occurred_at
```

第一版不需要复杂监控系统，但日志要足够定位转换失败和状态卡住的问题。

日志要求：

```text
每个 source 导入流程必须有 source_id；
每次调用转换服务必须记录 task_id 或 HTTP 状态；
每次失败必须记录 error_code 和 error_stage；
不要只记录自然语言错误；
不要只在 API 响应里返回错误而不写日志；
不要吞掉外部服务错误信息。
```

## 13. 和 unit 的边界

`source` 输出给 `unit` 的是稳定的 Markdown 输入和来源上下文。

第一版中，`source` 到 `unit` 的传递采用结果消息，而不是只传一个裸 `source_id`。

结果消息用于表达：

```text
source 是否已经具备切块条件；
切块应读取哪些输入；
如果不能切块，原因是什么；
这次 source 导入结果是否需要 trace 或 UI 展示。
```

### 13.1 UnitSourceResult

建议定义 `UnitSourceResult` 作为 source 到 unit 的传递契约。

字段：

```text
source_id
status
ready_for_unit
skip_reason
error
error_code
error_message
error_stage
error_target
retryable
external_status
external_message
normalized_markdown_path
preview_html_path
source_page_count
title
source_kind
intake_purpose
trust_level
origin_uri
produced_at
imported_at
```

字段含义：

| 字段 | 含义 |
| --- | --- |
| `source_id` | source 唯一 id |
| `status` | source 当前状态 |
| `ready_for_unit` | 是否允许进入知识切块 |
| `skip_reason` | 未进入切块的原因 |
| `error` | 完整 `SystemError`，可为空；跨模块失败结果必须优先使用该字段 |
| `error_code` | 错误码，可为空 |
| `error_message` | 错误详情，可为空 |
| `error_stage` | 错误发生阶段，可为空 |
| `error_target` | 错误对象，可为空 |
| `retryable` | 是否适合重试 |
| `external_status` | 外部任务状态或 HTTP 状态，可为空 |
| `external_message` | 外部服务返回的错误信息，可为空 |
| `normalized_markdown_path` | Markdown 输入路径 |
| `preview_html_path` | HTML 证据预览路径 |
| `source_page_count` | 原文页数或等价数量 |
| `title` | 材料标题 |
| `source_kind` | 来源类型 |
| `intake_purpose` | 导入用途 |
| `trust_level` | 来源可信等级 |
| `origin_uri` | 原始来源地址 |
| `produced_at` | 材料产生时间 |
| `imported_at` | 导入系统时间 |

### 13.2 成功进入切块

当 source 满足：

```text
status = ready_for_unit
intake_purpose = long_term_candidate
normalized_markdown_path 不为空
preview_html_path 不为空
```

传递结果：

```json
{
  "source_id": "src_01hxyz",
  "status": "ready_for_unit",
  "ready_for_unit": true,
  "skip_reason": null,
  "error_code": null,
  "error_message": null,
  "error_stage": null,
  "error_target": null,
  "retryable": false,
  "external_status": null,
  "external_message": null,
  "normalized_markdown_path": "data/sources/markdown/src_01hxyz.md",
  "preview_html_path": "data/sources/html/src_01hxyz.html",
  "source_page_count": 12,
  "title": "example.pdf",
  "source_kind": "file",
  "intake_purpose": "long_term_candidate",
  "trust_level": "unknown",
  "origin_uri": null,
  "produced_at": null,
  "imported_at": "2026-06-13T10:00:00Z"
}
```

只有 `ready_for_unit = true` 的结果才进入 unit 构建队列。

### 13.3 不进入切块但不是失败

临时证据材料转换完成后，不进入 unit 构建。

传递结果可以记录到 trace 或返回给调用方，但不投递到 unit 队列：

```json
{
  "source_id": "src_01habc",
  "status": "converted",
  "ready_for_unit": false,
  "skip_reason": "temporary_evidence",
  "error_code": null,
  "error_message": null,
  "error_stage": null,
  "error_target": null,
  "retryable": false,
  "external_status": null,
  "external_message": null,
  "normalized_markdown_path": "data/sources/markdown/src_01habc.md",
  "preview_html_path": "data/sources/html/src_01habc.html"
}
```

### 13.4 失败结果

如果 source 导入失败，必须形成失败结果，但不投递到 unit 构建队列。

例如 Markdown 转换失败：

```json
{
  "source_id": "src_01herr",
  "status": "preview_ready_but_not_learnable",
  "ready_for_unit": false,
  "skip_reason": "markdown_unavailable",
  "error_code": "markdown_convert_failed",
  "error_message": "Markdown conversion failed: unsupported table layout",
  "error_stage": "poll_markdown_convert",
  "error_target": "markdown",
  "retryable": false,
  "external_status": "failed",
  "external_message": "unsupported table layout",
  "normalized_markdown_path": null,
  "preview_html_path": "data/sources/html/src_01herr.html"
}
```

例如转换服务不可用：

```json
{
  "source_id": "src_01hdown",
  "status": "failed",
  "ready_for_unit": false,
  "skip_reason": "source_failed",
  "error_code": "convert_service_unavailable",
  "error_message": "failed to connect to convert service at http://127.0.0.1:8080",
  "error_stage": "submit_html_convert",
  "error_target": "html",
  "retryable": true,
  "external_status": null,
  "external_message": "connection refused",
  "normalized_markdown_path": null,
  "preview_html_path": null
}
```

失败结果的用途：

```text
写入 source_documents.error_message；
写结构化日志；
返回给上传调用方或前端；
进入 trace 时说明材料为什么没有参与知识切块。
```

失败结果不进入 unit 构建队列，避免 unit 需要处理不可切块输入。

### 13.5 unit 读取信息

`unit` 实际执行切块时，需要读取：

```text
source_id
normalized_markdown_path
preview_html_path
source_page_count
title
source_kind
intake_purpose
trust_level
origin_uri
produced_at
imported_at
```

`unit` 负责：

```text
解析 Markdown 结构；
识别知识单元边界；
生成知识单元；
记录知识单元在 Markdown 中的位置；
建立知识单元和 source 的证据关系。
```

`source` 不直接生成知识单元。

`error_code / error_message / error_stage / error_target` 是为了列表展示、状态查询和兼容业务表保存的错误摘要。

当 `ready_for_unit = false` 且原因是失败时，`UnitSourceResult.error` 必须携带完整 `SystemError` 或同等字段，至少包含：

```text
error_code
message
module = source
operation
stage
target
severity
retryable
entity_type = source
entity_id = source_id
request_id
trace_id，可为空
external_service，可为空
external_operation，可为空
external_status，可为空
external_task_id，可为空
external_message，可为空
details
```

下游 `unit` 应以 `error` 判断失败语义，以轻量字段做展示和状态摘要。

## 14. 和 retrieval 的边界

搜索引擎和网站获取不放在 `source` 第一版实现。

边界如下：

```text
retrieval 负责：
  判断当前问题是否需要外部证据；
  生成搜索 query；
  调用搜索引擎；
  抓取网页；
  抽取正文；
  选择哪些材料用于本次问题。

source 负责：
  一旦材料进入系统，就登记、归档、追溯；
  保存 HTML / Markdown 或等价文本；
  标记 intake_purpose；
  提供给 trace / working model / unit 使用。
```

当 retrieval 后续接入网页或搜索结果时，应创建：

```text
source_kind = webpage / search_result
intake_purpose = temporary_evidence
```

这些材料默认不进入长期记忆。

如果用户或系统后续确认某个临时证据值得长期保存，可以通过后续能力将其提升为：

```text
intake_purpose = long_term_candidate
```

然后再进入 unit 构建。

第一版不实现自动晋升。

## 15. 人工修正策略

第一版不实现人工修正流程。

原因：

```text
人工修正会提升知识质量；
但会引入文件版本、Markdown 版本、知识版本、差异比较、审核状态、重新构建、回滚和责任归属；
这些复杂性会干扰第一版验证核心链路。
```

第一版不预留可执行的人工修正状态机，也不维护 Markdown 多版本。

第一版只保存：

```text
normalized_markdown_path
```

后续如果需要人工修正，可以另行引入：

```text
SourceRevision
MarkdownRevision
KnowledgeRevision
```

并重新定义：

```text
当前生效版本；
历史版本；
版本差异；
相关 unit 重新构建；
由版本变化触发的知识失效或复核。
```

这些能力不属于第一版。

## 16. 文件和知识版本策略

第一版不实现文件版本和知识版本。

具体约束：

```text
同一个 source 不维护多份原始文件；
同一个 source 不维护多份 HTML；
同一个 source 不维护多份 Markdown；
不记录文件修订历史；
不记录 Markdown 修订历史；
不记录知识单元修订历史；
不记录认知结构预编译修订历史；
不提供版本对比、回滚和按版本查看。
```

如果用户再次上传同名文件或相似文件，系统创建新的 `SourceDocument`。

第一版可以通过来源字段表达材料之间的弱关联，例如：

```text
title
original_filename
origin_uri
produced_at
imported_at
```

但不把这种关联解释为版本关系。

后续如果需要版本能力，应独立设计：

```text
SourceRevision
ArtifactRevision
KnowledgeUnitRevision
PrecompileRevision
```

并明确版本变化如何影响 unit 重建、证据引用、trace 解释和生命周期判断。

## 17. 验收标准

第一版 source 完成后，应能验证以下场景。

测试输入可以使用仓库 `test/*.md`。

这些文件表示已经可作为 normalized Markdown 的测试内容。

测试中可以通过 fake ConvertClient 将 `test/*.md` 作为 Markdown 转换结果返回，并生成对应 preview HTML 占位文件。

约束：

```text
test/*.md 只用于测试；
source 仍必须创建 SourceDocument、保存路径和转换状态；
后续 unit 必须从 normalized_markdown_path 读取内容；
不得让 retrieval 或 agent 直接读取 test/*.md 绕过 source / unit / precompile。
```

### 17.1 长期候选文件导入

输入：

```text
上传一个 PDF / Word / PPT / Excel 文件
intakePurpose = long_term_candidate
```

期望：

```text
原始文件保存成功；
HTML 转换完成并保存副本；
Markdown 转换完成并保存副本；
source 状态变为 ready_for_unit；
source 可通过 API 查询；
HTML 可通过 preview API 查看；
Markdown 可通过 markdown API 读取；
unit 构建队列收到 ready_for_unit = true 的 UnitSourceResult。
```

### 17.2 临时证据文件导入

输入：

```text
上传一个文件
intakePurpose = temporary_evidence
```

期望：

```text
原始文件保存成功；
HTML / Markdown 保存成功；
source 状态变为 converted；
不进入 unit 构建队列；
可被 trace 或 working model 引用。
```

### 17.3 Markdown 转换失败

输入：

```text
上传一个 HTML 可转换但 Markdown 转换失败的文件
```

期望：

```text
HTML 副本保存成功；
source 状态变为 preview_ready_but_not_learnable；
不进入 unit 构建队列；
error_message 保存 Markdown 失败原因。
```

### 17.4 转换服务不可用

输入：

```text
文件转换服务未启动时上传文件
```

期望：

```text
原始文件保存成功；
source 状态变为 failed；
error_message 说明转换服务不可用；
系统不丢失 source 记录。
```

## 18. 第一版实现顺序

建议按以下顺序实现：

```text
1. source 数据表和枚举；
2. 文件存储目录和路径生成；
3. SourceRepository；
4. ConvertClient；
5. SourceImportService；
6. 转换任务轮询；
7. HTML / Markdown 下载归档；
8. HTTP API；
9. unit 队列投递接口；
10. 基础测试和失败场景测试。
```

实现时应保持 `ConvertClient` 和 `SourceImportService` 分离。

```text
ConvertClient
  只负责和文件转换服务通信。

SourceImportService
  负责编排 source 状态、文件保存、转换调用和后续投递。
```

这样后续替换转换服务或增加网页导入时，不需要改动 source 主流程。
