# Source 实现路径

## 职责

Source 负责将外部材料导入系统：调用 FileView 服务转换格式，规范化 Markdown，提取并补全目录结构（含语义目录），为后续 Unit 提取提供标准化输入。

语义目录结构（参考 PageIndex 的 tree index 思路）在本层完成。Unit 模块只消费 `source_outlines`，不感知节点来源（structural / semantic）。

## 核心组件

```text
FileView HTTP 客户端（格式转换，异步轮询）
Markdown 直读器（.md / .markdown 格式跳过 FileView）
Markdown 规范化器（标题归一化、格式统一）
结构 outline 提取器（从 Markdown heading 构建 outline tree）
语义 outline 生成器（LLM，两遍策略处理任意长度文档）
Source 状态管理（pending → processing → completed / failed）
Source 存储（SQLite + 文件系统双写）
Bleve outlines 索引写入
HTTP API
```

## 数据结构

### sources 表

```sql
CREATE TABLE sources (
    source_id     TEXT PRIMARY KEY,           -- UUID
    title         TEXT NOT NULL,
    format        TEXT NOT NULL,              -- markdown / html / pdf / word / image / other
    file_name     TEXT NOT NULL,              -- 原始文件名
    original_path TEXT NOT NULL,              -- data/sources/original/<source_id>.<ext>
    html_path     TEXT,                       -- data/sources/html/<source_id>.html（可为空）
    markdown_path TEXT NOT NULL,              -- data/sources/markdown/<source_id>.md
    status        TEXT NOT NULL DEFAULT 'pending',  -- pending / processing / completed / failed
    error_msg     TEXT,                       -- 失败原因（status=failed 时填写）
    outline_type  TEXT,                       -- structural / semantic / mixed（outline 来源）
    summary       TEXT,                       -- 文档自然语句摘要，供检索层 LLM Source 过滤使用
    domain_id     TEXT REFERENCES domains(domain_id),  -- 匹配到的知识领域（可为空）
    word_count    INTEGER,                    -- 规范化后正文字符数（rune）
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### source_outlines 表

```sql
CREATE TABLE source_outlines (
    outline_id  TEXT PRIMARY KEY,             -- UUID
    source_id   TEXT NOT NULL REFERENCES sources(source_id),
    parent_id   TEXT REFERENCES source_outlines(outline_id),
    level       INTEGER NOT NULL,             -- 标题层级，1=H1，2=H2，...
    title       TEXT NOT NULL,
    summary     TEXT,                         -- LLM 生成的关键词（语义节点填写，供 FTS）
    line_start  INTEGER NOT NULL,             -- 节点内容在规范化 Markdown 中的起始行号（1-based, inclusive，见 foundation.md 行号约定）
    line_end    INTEGER NOT NULL,             -- 节点内容的结束行号（1-based, inclusive）
    node_type   TEXT NOT NULL DEFAULT 'structural',  -- structural / semantic
    position    INTEGER NOT NULL,             -- 同级排序序号
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_source_outlines_source_id ON source_outlines(source_id);
CREATE INDEX idx_source_outlines_parent_id ON source_outlines(parent_id);
```

### 文件系统路径约定

```text
data/sources/
  original/<source_id>.<ext>      ← 原始文件，只写一次，不修改
  html/<source_id>.html           ← 可供人工查看的 HTML 预览（可选）
  markdown/<source_id>.md         ← 规范化 Markdown，Unit 提取的唯一输入
```

## 实现步骤

### 步骤 1：集成 FileView HTTP 客户端

FileView 是独立运行的 JAR 服务，wiki-brain 通过 HTTP 调用它完成格式转换。服务地址在 config.yml 中配置：

```yaml
fileview:
  base_url: "http://192.168.0.169:9000"   # FileView 服务地址（默认地址，可通过 config.yml 覆盖）
  poll_interval_ms: 1500              # 轮询间隔（毫秒）
  max_poll_seconds: 600               # 最长等待时间（与 FileView convert.timeout.seconds 对齐）
```

**FileView 调用流程**（参见 fileview/doc/api.md）：

```text
1. POST {base_url}/api/convert/markdown
   multipart/form-data，字段 file=<文件内容>，markdownToc=false
   → 返回 { "code": 0, "data": { "taskId": "...", "status": "processing" } }

2. 轮询 GET {base_url}/api/task/{taskId}
   → status=finished 时取 markdownUrl；status=failed 时取 error 字段终止

3. GET {markdownUrl} 下载 Markdown 内容，写入 data/sources/markdown/<source_id>.md

4. POST {base_url}/api/convert/html（可选，生成预览）
   → 同样流程，产物写入 data/sources/html/<source_id>.html
```

**Go 接口定义**：

```go
// FileViewClient 封装所有对 FileView HTTP 服务的调用，屏蔽轮询细节。
type FileViewClient interface {
    // ConvertToMarkdown 上传文件，同步等待转换完成，返回 Markdown 内容。
    // 内部执行：POST /api/convert/markdown → 轮询 /api/task/{taskId} → GET markdownUrl
    ConvertToMarkdown(ctx context.Context, srcPath string) (markdown []byte, err error)

    // ConvertToHTML 上传文件，同步等待转换完成，返回 HTML 内容。
    // 内部执行：POST /api/convert/html → 轮询 /api/task/{taskId} → GET htmlUrl
    ConvertToHTML(ctx context.Context, srcPath string) (html []byte, err error)
}
```

**错误处理**：
- FileView 返回 503（队列满）或超时：Source 标记为 `failed`，error_msg 记录原因，支持 retry
- FileView 返回 400（不支持的格式）：同上，不重试
- 轮询超过 `max_poll_seconds`：主动中止，Source 标记为 `failed`

### 步骤 2：实现格式转换

```text
1. 接收上传文件，分配 source_id，title 取原始文件名（file_name，不含路径）；
2. 写入 data/sources/original/<source_id>.<ext>（原始文件，不修改）；
3. 根据扩展名确定 format；
4. 按 format 路由：
   - .md / .markdown → 跳过 FileView，直接复制到 data/sources/markdown/（最小清理）；
   - 其他格式（html / pdf / docx / pptx / xls / xlsx / image 等）→ 调用 FileViewClient.ConvertToMarkdown()；
     - 若需要 HTML 预览（可选），同时调用 ConvertToHTML()；
5. Markdown 内容写入 data/sources/markdown/<source_id>.md；
6. HTML 内容（非空时）写入 data/sources/html/<source_id>.html；
7. Source 状态推进到 processing；
8. 转换失败：状态设为 failed，记录 error_msg，停止后续步骤。
```

FileView 支持的格式白名单（来自 api.md §4.5）：

```text
doc, docx, xls, xlsx, ppt, pptx, wps, et, dps, pdf, ofd, rtf, txt,
md, markdown, jpg, jpeg, png, bmp, tif, tiff
```

不在白名单内的格式直接 failed，不调用 FileView。

### 步骤 3：实现 Markdown 规范化

规范化目标是让所有来源的 Markdown 在格式上对齐，消除 KU 提取时因格式差异产生的噪声。

```text
标题层级归一化：
  - 不强制插入或升级 H1；保留文档原有 heading 结构；
  - 层级不连续时（如 H1 直接跳 H3）插入虚拟层级节点，不修改原始标题文本；
  - 超过 H4 的标题降级为 H4；

保留原始内容结构：
  - 代码块保留语言标注；
  - 表格保留 Markdown 表格格式；
  - 列表保留嵌套层级；

清理噪声：
  - 连续空行压缩为单空行；
  - 去除 \r、零宽字符等非打印字符；
  - 去除 HTML 注释。
```

规范化结果覆盖写回 `data/sources/markdown/<source_id>.md`，不保留中间版本。

### 步骤 4：实现结构 outline 提取

从规范化 Markdown 的 heading 构建 source_outlines 树：

```text
遍历 Markdown，识别 #、##、###、#### 标题行；
维护一个层级栈，按层级关系确定 parent_id；
每个节点记录：
  - title：标题文本（去除 # 前缀和空格）
  - level：1~4
  - line_start：该 heading 所在行的行号（1-based）
  - line_end：下一个同级或更高级 heading 的前一行行号（或文档末行）；即本节点内容末尾行（inclusive）
  - node_type：structural
  - position：同级排序序号（从 0 开始）
将所有节点写入 source_outlines 表。
```

### 步骤 5：判断是否触发语义 outline 生成

完成结构 outline 提取后，按以下条件判断是否需要 LLM 语义补全：

```text
满足以下任一条件，触发语义 outline 生成（步骤 6）：

A. 无结构
   - 结构 outline 节点数 = 0（文档完全没有 heading）

B. 结构过于稀疏
   - outline 节点数 < 3，且文档字符数（rune）> 2000

C. 覆盖率不足
   - 结构节点行范围之和不足文档总行数的 50%：
     sum(line_end - line_start + 1 FOR each structural node) / total_lines < 0.5
   （大量正文在 heading 层次之外，常见于 PDF 转换后的无标题段落）

D. 结构扁平
   - 所有 heading 层级相同（如全是 H2，无 H1 / H3），且节点数 < 5
   （仅有同级条目，缺乏层次导航能力）

E. 叶节点过长（即使有结构，也需要补全子层）
   - 任意叶节点的行范围对应内容字符数（rune）超过 segment_max_chars
   - segment_max_chars 与 unit.md 中切块阈值为同一配置项；语义目录生成的目标是保证
     所有叶节点内容 ≤ segment_max_chars，使 unit.md 无需对正文做二次切块

F. 来源为 PDF 或图片（高概率丢失结构）
   - format 为 pdf / ofd / image（jpg / png / tif 等）时，无论结构 outline 如何，
     均执行叶节点长度检查（条件 E）；若 E 未触发，跳过语义生成。
```

不满足任何条件：`source.outline_type` 设为 `structural`，跳过步骤 6。

满足条件时：记录触发原因（用于 debug 和日志），进入步骤 6。

### 步骤 6：实现语义 outline 生成（LLM 两遍策略）

**上下文限制说明**：LLM 输入 `max_tokens` 默认 4096（extraction 模型），单次调用能处理约 12000~16000 字符（rune）的文档内容。超出此限制时采用两遍策略。阈值由 `source.single_pass_threshold`（默认 12000，单位：rune 字符数）配置。

#### 6.1 触发路径分支

```text
文档字符数（rune）≤ source.single_pass_threshold（默认 12000）
  → 单遍处理（6.2）

文档字符数（rune）> source.single_pass_threshold
  → 两遍处理（6.3 + 6.4）

条件 E（叶节点过长）
  → 仅对超长叶节点运行分段细化（6.5），不替换已有结构节点
```

#### 6.2 单遍处理

直接将完整规范化 Markdown 发送给 LLM，一次调用生成完整语义 outline：

**Prompt 文件**：`config/prompts/outline_semantic_full.md`

```text
将以下文档划分为有语义的章节，输出层级目录。

要求：
1. 每个章节代表一个独立主题，层级最多 3 级
2. 每个章节提供 3~6 个关键词（空格分隔），概括该章节的核心主题
3. line_start 和 line_end 是章节在原文中的行号（行号从 1 开始，line_end 行本身包含在章节内），相邻章节不重叠，合计覆盖全文
4. 每个叶节点至少 5 行
5. 同级章节 level 值相同，子章节 level = 父章节 level + 1

文档内容（共 {{total_lines}} 行）：
{{document_content}}

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

#### 6.3 两遍处理 — 第一遍：骨架提取

**目的**：从长文档中提取结构骨架（体积通常 < 2000 tokens），一次 LLM 调用生成 L1-L2 层级结构。

骨架构建规则：
```text
遍历规范化 Markdown，逐行处理：
  - 所有 heading 行（完整保留，标注行号）
  - 每个段落的首行（不超过 100 字符，标注行号）
  - 代码块替换为 [代码块，N 行]（标注起始行号）
  - 表格替换为 [表格，N 行]（标注起始行号）
骨架每行标注其在原文中的行号（1-based）。
```

**Prompt 文件**：`config/prompts/outline_semantic_skeleton.md`

```text
以下是文档骨架（每行标注原文行号）。基于骨架生成顶层章节结构（level 1~2）。

line_start 和 line_end 使用骨架中标注的原文行号（1-based, inclusive），相邻章节不重叠，合计覆盖全文。summary 为 3~6 个关键词（空格分隔）。

骨架（格式：[行号] 内容）：
{{skeleton}}

文档总行数：{{total_lines}}

按以下 JSON Schema 输出：
{{json_schema}}
```

第一遍结果：得到 L1-L2 层级的顶层 outline 树，各节点有 line_start / line_end。

#### 6.4 两遍处理 — 第二遍：节点细化

对第一遍中行范围内容字符数（rune）超过 `segment_max_chars` 的节点，逐一细化：

```text
取该节点的原文内容（按 line_start / line_end 切片行）；
若内容字符数（rune）> source.single_pass_threshold，再次切块（每块行数使内容 ≤ single_pass_threshold，相邻块 overlap 10 行）；
对每块调用 LLM，生成子章节（level = 父节点 level + 1，最多到 level 3）；
合并各块输出，去除重叠边界处的重复节点；
将子节点挂载到父节点下。
```

**Prompt 文件**：`config/prompts/outline_semantic_chunk.md`

```text
以下是文档片段（原文第 {{chunk_line_start}} 行到第 {{chunk_line_end}} 行），将其细分为 level {{target_level}} 的子章节（父章节："{{parent_title}}"）。

line_start 和 line_end 是整篇文档中的行号（1-based, inclusive），不是片段内行号。每个子章节至少 5 行。summary 为 3~6 个关键词（空格分隔）。若内容是连贯整体，返回空列表。

内容：
{{chunk_content}}

按以下 JSON Schema 输出：
{{json_schema}}
```

#### 6.5 叶节点超长细化（条件 E）

对已有结构 outline 中的超长叶节点，单独运行细化（使用 `outline_semantic_chunk.md`），生成 semantic 子节点挂在结构叶节点下。此情况下 `source.outline_type` 设为 `mixed`。

#### 6.6 写入结果

```text
语义生成的节点写入 source_outlines 表，node_type = semantic；
混合情况（条件 E）：结构节点保留，新增子节点；
source.outline_type 更新为：
  - structural：无语义补全
  - semantic：全部由 LLM 生成
  - mixed：结构 + 语义混合
生成失败时不阻塞，保留已有结构 outline，记录 warn 日志。
```

**LLM 输出格式**（三个 prompt 共用）：

注入 prompt 的 `{{json_schema}}` 是示例 JSON，不是 JSON Schema DSL。示例只展示字段名和示例值，不含校验约束：

```json
{
  "sections": [
    {"title": "章节标题", "summary": "关键词1 关键词2 关键词3", "line_start": 1, "line_end": 42, "level": 1}
  ]
}
```

程序将模型输出解析并整合后，用对应 prompt 文件（`outline_semantic_full.md` / `outline_semantic_skeleton.md` / `outline_semantic_chunk.md`）内 `## Schema` 段的 JSON Schema 校验整合结果，检查：`line_start < line_end`、`level` 在 1~3 范围内、相邻 section 不重叠、所有行号在文档范围内。整合过程包括：推断父子关系、补全 position 字段。Source 语义 outline 的 LLM 输出统一使用整篇规范化 Markdown 的绝对行号，不做相对行号转换。

**父子关系推断**：模型输出 `level` 字段，不输出 `children` 或 `parent_id`。写入 `source_outlines` 前，程序按以下规则从平铺列表推断父子关系：

```text
按 line_start 升序排列 sections；
维护一个层级栈：当前节点 level 若大于栈顶节点 level，则栈顶为父节点；
否则弹出栈顶直到找到更小 level 的节点作为父节点；
由此确定每个节点的 parent_id；
position 字段从同级节点的数组顺序计算（从 0 开始）；
node_type 由调用路径决定（单遍/两遍/骨架 → semantic，叶节点细化 → semantic，结构提取 → structural），不从模型输出读取。
```

`line_start`/`line_end` 直接写入数据库对应字段。

### 步骤 7：生成 Source 摘要

outline 完成后，生成一段自然语句描述整份文档的主题和内容范围，写入 `sources.summary`，供检索层 LLM Source 过滤使用。

**输入构建**

```text
文档 title；
所有 level=1 的 outline 节点 title（按 line_start 顺序）；
文档正文前 300 字符（规范化 Markdown 首段，去除 heading 行）。
```

**Prompt 文件**：`config/prompts/source_summary.md`

```
根据以下文档信息，用一句话概括这篇文档的主题和内容范围（50~150字）。

文档标题：{{title}}

顶层目录：
{{top_outline_titles}}

文档首段：
{{first_paragraph}}

直接输出摘要，不输出其他内容。
```

摘要为纯文本，不使用 JSON Schema，模型直接输出一句话。

**失败处理**：LLM 调用失败时，将所有 `level=1` 的 outline 节点的 `summary`（关键词）按 `line_start` 顺序拼接（空格分隔）作为 `sources.summary`，不阻塞 Source 完成。

### 步骤 8：Domain 匹配

Source 摘要生成（步骤 7）完成后，根据文档标题和摘要，调用 LLM 在预制 Domain 列表中选择最匹配的 Domain，更新 `sources.domain_id`。

**输入**：

```text
source.title
source.summary（步骤 7 已生成）
domains 表中所有 domain（domain_id + name + description）
```

**Prompt 文件**：`config/prompts/source_domain_match.md`

```
以下是一份文档的信息：
标题：{{title}}
内容概述：{{summary}}

以下是可用的知识领域列表：
{{domain_list}}

请选择最匹配的知识领域 domain_id。若没有匹配的领域，返回空字符串。

按以下格式输出，不输出任何其他内容：
{"domain_id": "xxx"}
```

`{{domain_list}}` 每行格式：`[domain_id] name：description`

**结果处理**：

```text
domain_id 非空且存在于 domains 表：写入 sources.domain_id；
domain_id 为空或不在 domains 表中：sources.domain_id 保持 null；
  → domain_id 为 null 的 source 在检索时不受 domain 过滤约束，始终参与召回；
LLM 调用失败：记录 warn 日志，sources.domain_id 保持 null，不阻塞 Source 完成。
```

### 步骤 9：写入 Bleve 索引

Source 完成后同步写入 outlines index：

```text
outlines index 写入字段：
  outline_id、source_id、title、summary（可为空）、level、node_type
  （供 Retrieval 的目录结构召回按标题和关键词做 FTS）
```

索引写入失败记录错误日志，不将 Source 标记为 failed（索引可通过重建命令补全）。

### 步骤 10：状态管理

```text
pending     → 已接收，等待处理
processing  → 正在转换和解析（FileView 调用 / 规范化 / outline 提取期间）
completed   → 规范化 Markdown 和 outline 均已写入，可供 Unit 消费
failed      → 任一步骤失败，记录 error_msg，可通过 retry 接口重新触发
```

状态变更写入 SQLite（`updated_at` 同步更新），异常不静默丢弃。

Source 处理链在异步任务队列中顺序执行（通过 foundation 的 buffered channel 调度），失败即终止当前 Source，不影响其他 Source。

**Source 完成后自动触发 Unit 提取**：

```text
Source 状态变更为 completed 时，立即将 Unit 提取任务投入同一异步队列：
  任务参数：source_id
  消费者：Unit 模块的提取处理器（等同于 POST /sources/:id/units 触发的逻辑）

投入失败（队列已满）：记录 error 日志（含 source_id），不将 Source 标记为 failed；
  可通过 POST /sources/:id/units 手动补触发。
Source 状态为 failed 时不触发 Unit 提取。
```

### 步骤 11：暴露 HTTP API

```text
POST   /sources
  请求：multipart/form-data，字段 file（文件）或 url（远程 URL，可选实现）
  title 由原始文件名自动设置，不接受客户端传入
  响应：{ source_id, status, title, format }

GET    /sources
  查询参数：status（可选过滤）、limit（默认 20）、offset（默认 0）
  响应：[ { source_id, title, format, status, outline_type, created_at } ]

GET    /sources/:id
  响应：{ source_id, title, format, status, outline_type, summary, domain_id, word_count, error_msg, created_at }

POST   /sources/:id/retry
  仅对 status=failed 的 Source 生效，重置为 pending 并重新触发处理链
  retry 幂等规则（按顺序检查）：
    1. 若 data/sources/markdown/<source_id>.md 已存在且非空：
       跳过 FileView 调用和规范化步骤，从步骤 4（结构 outline 提取）重新开始；
    2. 若 markdown 文件不存在或为空：从步骤 1（FileView 调用）重新开始；
    3. source_outlines 表中已有该 source 的节点：清空后重新提取（避免部分写入的脏数据）
  响应：{ source_id, status }

GET    /sources/:id/outlines
  响应：outline 树（按层级嵌套，JSON）
  [
    {
      "outline_id": "...",
      "title": "...",
      "level": 1,
      "node_type": "structural",
      "summary": null,
      "line_start": 1,
      "line_end": 80,
      "children": [
        {
          "outline_id": "...",
          "title": "...",
          "level": 2,
          "node_type": "semantic",
          "summary": "部署 配置 环境变量",
          "line_start": 5,
          "line_end": 38,
          "children": []
        }
      ]
    }
  ]

GET    /sources/:id/markdown
  响应：规范化 Markdown 原文（text/markdown）
  用途：测试页面查看 Source 的标准化输入，Unit 提取也以该文件为唯一输入。

GET    /sources/:id/preview
  响应：HTML 预览（text/html）
  规则：html_path 存在时返回 FileView 生成的 HTML；否则将规范化 Markdown 做安全转义后返回简单 HTML 预览。
```

## 依赖

```text
基础设施：SQLite（WAL）、文件系统、Bleve 索引、LLM client（步骤 6、7）、
          异步任务队列（处理链调度）、结构化日志、HTTP 框架
外部服务：FileView HTTP 服务（步骤 1~2），地址由 config.yml 的 fileview.base_url 配置
无其他模块依赖，是最早可独立构建和测试的业务模块。
```

## 接口契约（供 Unit 消费）

Unit 模块通过以下查询消费 Source 输出：

```sql
-- 取某 Source 的所有 outline 节点（按行号顺序）
SELECT outline_id, parent_id, title, summary, line_start, line_end, level, node_type
FROM source_outlines
WHERE source_id = ?
ORDER BY line_start ASC;
```

Unit 按 `line_start / line_end` 切分 `markdown_path` 对应的文件内容（split by `\n` 后取对应行）作为 KU 提取的输入，不区分 structural / semantic 节点。

## 完成标准

```text
FileView 客户端能完成上传 → 轮询 → 下载全流程，含超时和失败处理；
能稳定导入 Markdown（直读）和 FileView 支持的格式；
规范化 Markdown 写入文件系统，结构 outline 写入 source_outlines；
语义 outline 触发条件（A~F）按规则正确判断；
单遍和两遍处理均能产出有效的 source_outlines，LLM 输出通过 JSON Schema 校验；
Prompt 文件存放在 config/prompts/，版本号在文件内 frontmatter 中管理；
Source 状态可通过 API 查询，failed 状态有 error_msg；
outlines API 返回正确的层级嵌套结构；
Source 摘要生成成功写入 sources.summary，失败时不阻塞完成状态；
fake LLM client 下，语义 outline 路径测试可稳定运行；
Unit 模块可直接按 source_outlines 切块，无需额外处理。
```
