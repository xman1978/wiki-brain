# Wiki-Brain API 文档

本文档整理三类核心 API：**文件导入**（Source）、**知识检索**（Retrieval）、**知识问答**（Answer）。

## 通用约定

- Base URL：`http://<host>:<port><prefix>`，默认 `http://localhost:8080`（`prefix` 由 `config.yml` 的 `server.path_prefix` 决定，默认为空）。
- 请求/响应均为 `application/json`（文件上传接口除外，使用 `multipart/form-data`）。
- 错误响应统一格式：

```json
{
  "error": "错误描述"
}
```

- 时间字段格式：`2006-01-02T15:04:05Z`（UTC）。

---

## 一、文件导入（Source）

### 1.1 上传文件（新建 Source）

`POST /sources`

以 `multipart/form-data` 上传原始文件，触发异步的 `source_process → unit_extract` 处理链路。接口本身只做导入登记，转换/抽取在后台队列中完成，可通过 [1.5 查询详情](#15-查询-source-详情) 或 [1.8 处理进度（SSE）](#18-处理进度sse) 跟踪状态。

**请求参数（form fields）**

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `file` | file | 是 | 原始文件，支持 pdf/pptx/docx/xlsx/xls/doc/txt/md 等格式 |
| `origin` | string | 否 | 来源标记，默认 `upload`；Wiki 草稿回流等内部场景会传 `wiki_draft` |
| `origin_page_id` | string | 否 | 当 `origin=wiki_draft` 时，标记来源页面 ID，用于防自指 |

**响应 `201 Created`**

```json
{
  "source_id": "src_abc123",
  "status": "processing",
  "title": "员工手册.pdf",
  "format": "pdf"
}
```

**错误**

- `400` 缺少 `file` 字段 / 不支持的文件格式
- `409` 文件名已存在（需先改名或删除同名文件）

**curl 示例**

```bash
curl -X POST "http://localhost:8080/sources" \
  -F "file=@/path/to/员工手册.pdf"
```

---

### 1.2 文件列表

`GET /sources`

**查询参数**

| 参数 | 类型 | 说明 |
| --- | --- | --- |
| `status` | string | 按状态过滤（如 `processing`/`completed`/`failed`/`deleted`） |
| `domain_id` | string | 按知识域过滤 |
| `q` | string | 按标题模糊搜索 |
| `limit` | int | 分页大小，默认 10 |
| `offset` | int | 分页偏移，默认 0 |

**响应 `200 OK`**

```json
{
  "total": 42,
  "items": [
    {
      "source_id": "src_abc123",
      "title": "员工手册.pdf",
      "format": "pdf",
      "status": "completed",
      "units_status": "completed",
      "units_stage": "done",
      "outline_type": "heading",
      "domain_id": "dom_hr",
      "domain_name": "人力资源",
      "created_at": "2026-08-01T10:00:00Z",
      "processing_started_at": "2026-08-01T10:00:01Z",
      "completed_at": "2026-08-01T10:00:05Z",
      "units_completed_at": "2026-08-01T10:01:00Z",
      "units_built_at": "2026-08-01T10:01:00Z"
    }
  ],
  "domains": [
    { "domain_id": "dom_hr", "name": "人力资源" }
  ]
}
```

**curl 示例**

```bash
curl "http://localhost:8080/sources?status=completed&limit=20&offset=0"
```

---

### 1.3 查询 Source 详情

`GET /sources/{id}`

**响应 `200 OK`**

```json
{
  "source_id": "src_abc123",
  "title": "员工手册.pdf",
  "format": "pdf",
  "status": "completed",
  "units_status": "completed",
  "units_stage": "done",
  "version": 1,
  "created_at": "2026-08-01T10:00:00Z",
  "manually_edited_count": 0,
  "outline_type": "heading",
  "summary": "本文档介绍公司考勤与请假制度",
  "domain_id": "dom_hr",
  "word_count": 12345,
  "processing_started_at": "2026-08-01T10:00:01Z",
  "completed_at": "2026-08-01T10:00:05Z",
  "units_completed_at": "2026-08-01T10:01:00Z",
  "units_built_at": "2026-08-01T10:01:00Z",
  "register_duration_ms": 120,
  "convert_duration_ms": 3400,
  "units_duration_ms": 8200,
  "semantics_duration_ms": 2100
}
```

`error_msg` 字段仅在处理失败时出现。

**curl 示例**

```bash
curl "http://localhost:8080/sources/src_abc123"
```

---

### 1.4 删除 Source

`DELETE /sources/{id}`

失败状态（`status=failed` 或 `units_status=failed`）的文件会被硬删除；其余状态执行软删除（关联 KU/KP 标记为 `deprecated`，文件与记录保留）。

**响应**

- 硬删除：`204 No Content`
- 软删除：`200 OK`

```json
{
  "source_id": "src_abc123",
  "deprecated_units": 12
}
```

**curl 示例**

```bash
curl -X DELETE "http://localhost:8080/sources/src_abc123"
```

---

### 1.5 恢复已软删除的 Source

`POST /sources/{id}/restore`

**响应 `200 OK`**

```json
{
  "source_id": "src_abc123",
  "restored_units": 12
}
```

**curl 示例**

```bash
curl -X POST "http://localhost:8080/sources/src_abc123/restore"
```

---

### 1.6 重试失败的处理

`POST /sources/{id}/retry`

仅对 `status=failed` 的 Source 生效。

**响应 `200 OK`**

```json
{
  "source_id": "src_abc123",
  "status": "processing",
  "units_status": "pending"
}
```

**curl 示例**

```bash
curl -X POST "http://localhost:8080/sources/src_abc123/retry"
```

---

### 1.7 重新上传（Shadow Source 机制）

`POST /sources/{id}/reupload`

新文件先在隐藏的 Shadow Source 中走完整处理链路，全部成功后才原子替换原 Source 内容；期间不影响原文件的检索可用性。

**请求参数（form fields）**

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `file` | file | 是 | 新版本文件 |

**响应 `202 Accepted`**

```json
{
  "source_id": "src_abc123",
  "shadow_source_id": "src_shadow_xyz",
  "status": "processing"
}
```

**curl 示例**

```bash
curl -X POST "http://localhost:8080/sources/src_abc123/reupload" \
  -F "file=@/path/to/员工手册_v2.pdf"
```

重新上传失败后，可用 `POST /sources/{id}/reupload/retry` 续跑已有的失败 Shadow：

```bash
curl -X POST "http://localhost:8080/sources/src_abc123/reupload/retry"
```

---

### 1.8 处理进度（SSE）

`GET /sources/{id}/progress`

Server-Sent Events 流，实时推送导入/抽取处理进度。

**事件格式**

```
event: progress
data: {"phase":"units_extract","status":"running","detail":"正在抽取第3/10段"}

event: done
data: {"source_id":"src_abc123"}
```

**curl 示例**

```bash
curl -N "http://localhost:8080/sources/src_abc123/progress"
```

---

### 1.9 其他常用只读/管理接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `PATCH /sources/{id}/domain` | 人工修正知识域，body `{"domain_id":"dom_hr"}` |
| `PATCH /sources/{id}/summary` | 人工修正摘要，body `{"summary":"..."}` |
| `GET /sources/{id}/outlines` | 获取目录树 |
| `GET /sources/{id}/markdown` | 获取转换后的 Markdown（支持 `?unit_id=` / `?version=`） |
| `GET /sources/{id}/preview` | 获取 HTML 预览 |
| `GET /sources/{id}/original` | 下载/预览原始文件 |
| `GET /sources/{id}/versions` | 列出历史版本（reupload 归档） |

---

## 二、知识检索（Retrieval）

### 2.1 检索问题证据

`POST /retrieval`

对给定问题执行分层检索（Wiki 直答 ∥ ActivationLink 快路径 ∥ 慢路径 RRF+Rerank），返回结构化的证据集合（`EvidenceSet`），不生成最终回答文本——生成文本请使用第三部分的 Answer 接口。

**请求 Body**

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `question` | string | 是 | 用户问题 |
| `force_full` | bool | 否 | 强制跳过快路径，走完整慢路径（用于调试/对比评测） |

**响应 `200 OK`（`EvidenceSet`）**

关键字段说明：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `question` | string | 原始问题 |
| `subject`/`intent`/`audience`/`constraint` | string | 问题四元组解析结果 |
| `path` | string | 命中路径描述 |
| `path_type` | string | `wiki` / `fast` / `slow` 等 |
| `activation_hits` | array | ActivationLink 命中详情（`link_id`/`point_id`/`match_score`/`tier` 等） |
| `bundle_hits` | array | ActivationBundle（熟路）命中详情 |
| `direct_evidence` | array | 直接支撑答案的证据（`Evidence[]`，见下） |
| `supporting` | array | 辅助证据 |
| `conflicts` | array | 冲突证据 |
| `gap_reason` | string | 命中知识缺口时的原因（`no_candidates`/`judge_filtered`） |
| `wiki_page_id` | string | 命中 Wiki 页面时的页面 ID（`path_type=wiki` 时有效） |
| `cited_point_ids` | array | Wiki 直答引用的知识点 ID |

`Evidence` 对象结构：

```json
{
  "fact_id": "fact_001",
  "unit_id": "ku_001",
  "point_id": "kp_001",
  "content": "全职员工每年享有5天带薪年假",
  "source_ref": { "source_id": "src_abc123", "line_start": 10, "line_end": 12 },
  "role": "direct",
  "origin": "extracted",
  "source_title": "员工手册.pdf"
}
```

**示例响应**

```json
{
  "question": "全职员工年假有几天？",
  "subject": "年假",
  "intent": "查询天数",
  "path": "fast_path",
  "path_type": "fast",
  "activation_hits": [
    { "link_id": "link_001", "point_id": "kp_001", "match_score": 0.95, "tier": "trusted" }
  ],
  "direct_evidence": [
    {
      "fact_id": "fact_001",
      "unit_id": "ku_001",
      "point_id": "kp_001",
      "content": "全职员工每年享有5天带薪年假",
      "source_ref": { "source_id": "src_abc123", "line_start": 10, "line_end": 12 },
      "role": "direct",
      "origin": "extracted",
      "source_title": "员工手册.pdf"
    }
  ],
  "supporting": []
}
```

**curl 示例**

```bash
curl -X POST "http://localhost:8080/retrieval" \
  -H "Content-Type: application/json" \
  -d '{"question": "全职员工年假有几天？"}'
```

强制走慢路径：

```bash
curl -X POST "http://localhost:8080/retrieval" \
  -H "Content-Type: application/json" \
  -d '{"question": "全职员工年假有几天？", "force_full": true}'
```

---

### 2.2 主题标签管理（辅助检索准确性）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET /sources/{id}/subject-tags` | 查看 Source 的人工主题标签 |
| `POST /sources/{id}/subject-tags` | 新增标签，body `{"subject":"考勤制度"}` |
| `DELETE /sources/{id}/subject-tags/{affinity_id}` | 删除标签 |

**curl 示例**

```bash
curl -X POST "http://localhost:8080/sources/src_abc123/subject-tags" \
  -H "Content-Type: application/json" \
  -d '{"subject": "考勤制度"}'
```

---

## 三、知识问答（Answer）

### 3.1 一次性问答

`POST /answer`

内部先调用 Retrieval 获取证据，再基于证据生成完整回答（非流式，一次性返回）。

**请求 Body**

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `question` | string | 是 | 用户问题 |
| `deep` | bool | 否 | 是否强制走深度（慢路径）回答 |
| `session_id` | string | 否 | 会话 ID，传入后会把本次回答关联到该会话的最新一轮对话 |

**响应 `200 OK`（`AnswerResult`）**

```json
{
  "answer_id": "ans_001",
  "question": "全职员工年假有几天？",
  "content": "根据员工手册，全职员工每年享有5天带薪年假[1]。",
  "citations": ["fact_001"],
  "has_answer": true,
  "path": "fast_path",
  "path_type": "fast",
  "evidence_snapshot": { "...": "同 POST /retrieval 的 EvidenceSet 结构" },
  "created_at": "2026-09-02T09:00:00Z"
}
```

**curl 示例**

```bash
curl -X POST "http://localhost:8080/answer" \
  -H "Content-Type: application/json" \
  -d '{"question": "全职员工年假有几天？"}'
```

带会话关联：

```bash
curl -X POST "http://localhost:8080/answer" \
  -H "Content-Type: application/json" \
  -d '{"question": "那病假呢？", "session_id": "sess_001"}'
```

---

### 3.2 流式问答（SSE）

`POST /answer/stream`

以 Server-Sent Events 流式返回生成过程，适合前端实时展示思考/生成内容。

**请求 Body**

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `question` | string | 是 | 用户问题 |
| `deep` | bool | 否 | 强制深度回答 |
| `session_id` | string | 否 | 会话 ID |
| `subject`/`intent`/`audience`/`constraint` | string | 否 | 已解析的问题四元组（会话层传入，跳过重复解析） |
| `domain_ids` | string[] | 否 | 已解析的知识域范围 |
| `domain_resolved` | bool | 否 | 是否已解析知识域（为 true 时跳过内部的域匹配） |
| `follow_up` | bool | 否 | 是否为会话中的非首个问题 |

**响应事件类型**

| event | 说明 |
| --- | --- |
| `phase` | 当前处理阶段提示 |
| `thinking` | 思考过程片段（可选） |
| `content` | 回答正文片段（增量） |
| `result` | 完整 `AnswerResult`（同 3.1 响应结构） |
| `error` | 出错信息 |
| `done` | 流结束，data 为 `[DONE]` |

**curl 示例**

```bash
curl -N -X POST "http://localhost:8080/answer/stream" \
  -H "Content-Type: application/json" \
  -d '{"question": "全职员工年假有几天？"}'
```

**输出示例**

```
event: phase
data: 正在检索相关知识...

event: content
data: 根据员工手册，

event: content
data: 全职员工每年享有5天带薪年假[1]。

event: result
data: {"answer_id":"ans_001","question":"全职员工年假有几天？","content":"...","citations":["fact_001"],"has_answer":true,"path":"fast_path","path_type":"fast","evidence_snapshot":{...}}

event: done
data: [DONE]
```

---

### 3.3 查询历史回答

`GET /answers/{id}`

**响应 `200 OK`**：同 `AnswerResult` 结构（见 3.1）。

**curl 示例**

```bash
curl "http://localhost:8080/answers/ans_001"
```

**错误**

- `404` 回答不存在
