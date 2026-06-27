# 基础设施实现路径

基础设施是所有模块的前置条件，必须最先完成。其他模块的开发和测试均依赖本层提供的能力。

## 职责

提供所有模块共用的运行基础：数据库、文件存储、配置、LLM 调用、搜索索引、异步任务、日志和 HTTP 框架。

## 核心组件

```text
SQLite 初始化（含 WAL、外键、迁移机制）
文件系统目录结构
YAML 配置加载（含环境变量覆盖）
LLM 统一 client
Bleve 索引管理（三个独立索引）
异步任务队列（Go channel + goroutine consumer）
结构化日志
HTTP API 框架
测试基础设施（fake LLM client、临时 DB、临时目录）
```

## 实现步骤

### 步骤 1：配置加载

实现 YAML 配置加载，支持以下查找顺序：

```text
1. --config 启动参数
2. WIKI_CONFIG_PATH 环境变量
3. ./config.yml（当前工作目录）
4. ~/.wiki/config.yml
```

`llm.api_key` 支持两种写法：直接填写字符串，或填写环境变量名 `LLM_API_KEY`（程序启动时自动读取对应环境变量的值）。业务模块通过统一配置对象读取参数，不硬编码任何配置项。

### 步骤 2：SQLite 初始化

启动时自动完成：

```text
启用 WAL 模式；
启用 foreign_keys；
执行迁移脚本（按版本号顺序，幂等执行）。
```

迁移脚本放在独立目录，每次新增表或变更结构通过新版本脚本完成，不直接修改旧脚本。

迁移脚本按版本号命名，放在 `internal/foundation/db/migrations/` 目录下。各表建表 SQL 如下：

**预制数据表（`domains`、`concepts`）**

```sql
CREATE TABLE domains (
    domain_id    TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE concepts (
    concept_id   TEXT PRIMARY KEY,
    domain_id    TEXT NOT NULL REFERENCES domains(domain_id),
    name         TEXT NOT NULL,
    description  TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_concepts_domain_id ON concepts(domain_id);
```

**会话表（`sessions`、`session_turns`）**

```sql
CREATE TABLE sessions (
    session_id      TEXT PRIMARY KEY,
    title           TEXT NOT NULL DEFAULT '',
    -- 取第一轮用户输入前 30 字，由程序截取，不调 LLM
    state_snapshot  TEXT NOT NULL DEFAULT '{}',
    -- 持久化 SessionState 字段的 JSON 快照：
    -- intent、confirmed_objects、recent_objects、clarification_log、
    -- current_object、continuable_action、step_summary
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE session_turns (
    turn_id      TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    turn_index   INTEGER NOT NULL,
    -- 轮次序号，从 1 开始，同一 session 内单调递增
    user_input   TEXT NOT NULL,
    action       TEXT NOT NULL,
    -- retrieve / clarify / interrupted
    answer_id    TEXT,
    -- action=retrieve 时关联 answers.answer_id，Answer 完成后由 /session/working 补填
    clarify_msg  TEXT,
    -- action=clarify 时的澄清问题文本
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_session_turns_session_id ON session_turns(session_id);
```

### 内容存储原则

SQLite、文件系统、Bleve 各司其职，不重复存储：

```text
SQLite       存元数据、路径、状态、短派生内容（标题、摘要、KP 正文等 ≤ 200 字符的字段）；
             不存原始文件内容，不存长文本正文。

文件系统     存原始文件和规范化 Markdown（按 source_id 组织路径）；
             SQLite 只保留路径字段，不缓存文件内容。

Bleve        存可检索的文本内容（KU 行号切片正文、KP 正文、outline 标题摘要等）；
             与 SQLite 的元数据通过 id 关联，不在 SQLite 中重复存全文。
```

### 行号定位全局约定

系统中所有位置字段（`source_outlines`、`knowledge_units` 等表）均使用**行号**定位，而非字节偏移。

**字段命名**：`line_start` / `line_end`，均为 **1-based、inclusive**（第一行为 1，line_end 行本身包含在范围内）。

**选用行号的原因**：
- Markdown 是行结构文档，所有有意义的边界（标题、段落、代码块）均落在行边界上，不存在需要精确到"行内某字节"的切分场景
- LLM 的行号估算比字节计数可靠得多——模型能感知"这是第几个标题"，无法精确计算字节数
- 提取内容只需 `strings.Split(content, "\n")[line_start-1:line_end]`，调试直观，截断无乱码风险

**内容提取公式**（Go）：

```go
lines := strings.Split(content, "\n")
nodeContent := strings.Join(lines[lineStart-1:lineEnd], "\n")
// lineStart、lineEnd 均为数据库中的绝对行号（1-based, inclusive）
```

**所有涉及行号的实现位置必须遵守此约定：**

```text
Source 层（outline 提取）：
  - structural outline：遍历 Markdown 行，heading 所在行即为 line_start；
    下一个同级或更高级 heading 的前一行（或文档末行）为 line_end
  - semantic outline：LLM 输出整篇规范化 Markdown 中的绝对行号（1-based, inclusive）；
    单遍、骨架和分块细化 prompt 均显式要求输出原文绝对行号
  - 文档总行数：len(strings.Split(content, "\n"))

Unit 层（KU 提取）：
  - 分段按 outline 叶节点的 line_start / line_end 切片
  - LLM 输出相对行号（1-based，相对于传入分段首行），绝对化同上
  - unit.line_start = segment.line_start - 1 + llm_output.line_start
  - unit.line_end   = segment.line_start - 1 + llm_output.line_end

LLM Prompt：
  - Source outline Prompt 统一说明"输出整篇文档中的起始行号和结束行号（行号从 1 开始）"
  - Unit 提取 Prompt 统一说明"输出在当前分段中的相对起始行号和结束行号（行号从 1 开始）"
  - 模型按结构感知行号，精度远高于字节估算
```

**验证方式**：写入 `source_outlines` 后，用 `lines[line_start-1:line_end]` join 结果应等于原始节点内容，否则为行号计算错误。

**文本长度计数**：`segment_max_chars`、`min_segment_chars`、`single_pass_threshold`、`word_count` 等阈值均以 **rune 字符数**计（Go 实现用 `utf8.RuneCountInString`），不用字节数。

KnowledgeUnit 正文不单独写文件：通过 `sources.markdown_path` 与 `knowledge_units.line_start` / `line_end` 从规范化 Markdown 动态切片读取。Bleve units index 写入时同样按行号切片正文供 FTS；SQLite 只存行号、`center` 等元数据，不存 KU 全文。

### 步骤 3：文件系统目录结构

启动时检查并创建以下目录（不存在则创建，已存在则跳过）：

```text
config/
  config.yml      ← 主配置文件（默认查找路径之一）
  prompts/        ← Prompt 模板文件（与配置一起管理）
preset/
  domains.json    ← 预制知识领域和概念定义（单文件，路径为 preset/domains.json）
data/
  sources/
    original/     ← 原始文件
    html/         ← 预览 HTML
    markdown/     ← 规范化 Markdown
  searchindex/    ← Bleve 索引目录
  traces/         ← Trace 导出
  exports/        ← 其他导出
logs/
```

### 步骤 4：LLM 统一 client

统一 client 封装所有 LLM 调用，对业务模块屏蔽 OpenAI SDK 细节。client 必须支持：

```text
按用途选择模型（default / reasoning / extraction / embedding）；
加载并渲染 Prompt 文件（变量替换）；
记录 prompt 版本号和模型名；
执行结构化输出（JSON Schema 校验）；
超时控制；
失败重试（指数退避，最大次数可配置）；
返回标准错误类型（区分超时、校验失败、模型错误）。
```

测试时通过 fake LLM client 替换真实调用，fake client 根据配置返回预设响应，不发起网络请求。

**Go interface 定义**：

```go
// LLMClient 是所有模块共用的 LLM 调用接口。
// 业务模块只依赖此接口，不直接调用 OpenAI SDK。
type LLMClient interface {
    // Complete 加载 Prompt 文件，替换变量，调用 LLM，返回原始文本输出。
    // promptFile：相对于 config/prompts/ 的文件名（如 "source_summary.md"）
    // vars：模板变量，键为不含 {{ }} 的变量名（如 "title"），值为替换内容
    // model：用途标识（"default" / "extraction" / "reasoning"），
    //        client 内部映射到 config.yml 中配置的模型名；
    //        extraction 和 reasoning 若未单独配置，默认使用 default_model 的值
    Complete(ctx context.Context, promptFile string, vars map[string]string, model string) (string, error)

    // CompleteJSON 同 Complete，但额外用 prompt 文件 ## Schema 段内的 JSON Schema
    // 对程序整合后的结果做校验（不从独立 schema 文件读取）。
    // 返回校验通过的原始 JSON 字节；校验失败返回 ErrSchemaValidation。
    CompleteJSON(ctx context.Context, promptFile string, vars map[string]string, model string) ([]byte, error)
}

// 标准错误类型，业务模块按类型决策（重试 / 降级 / 失败）
var (
    ErrTimeout          = errors.New("llm: timeout")
    ErrSchemaValidation = errors.New("llm: schema validation failed")
    ErrModelError       = errors.New("llm: model error")
)
```

注：`{{json_schema}}` 变量（Prompt `## System` 段注入的示例 JSON）通过 `vars["json_schema"]` 传入，
由调用方将示例 JSON 序列化为字符串后传递，client 不感知其内容。程序整合后的结果校验使用同一 prompt 文件 `## Schema` 段。

### 步骤 5：Bleve 索引初始化

启动时初始化三个独立 Bleve 索引：

```text
units index       ← KnowledgeUnit 全文检索，对应 knowledge_units 表
points index      ← KnowledgePoint 全文检索，对应 knowledge_points 表
outlines index    ← Source 目录节点全文检索，对应 source_outlines 表
```

sources 表通过 SQLite 直接查询（title + summary），不建 Bleve 索引。

索引目录存放在 `data/searchindex/` 下。提供统一的索引管理接口：写入、删除、批量重建。

#### 5.1 中文分词方案

系统内容以中文为主，Bleve 默认 `standard` analyzer 按空格分词，无法切分中文词组（如"知识单元"会被视为单个 token），导致 FTS 召回率极低。

**方案：自定义 `wiki_brain` analyzer，使用 gse 词典分词。**

依赖：
```text
github.com/go-ego/gse        ← 中文词典分词库
github.com/go-ego/gse/bleve  ← gse 的 Bleve tokenizer 注册包
```

gse 支持在初始化时加载自定义词典，词典文件放在 `config/dict/wiki_brain.txt`，每行一个词（如"知识单元"、"知识点"、"激活链接"等领域词），与默认词典合并使用。

```go
import (
    "github.com/blevesearch/bleve/v2"
    _ "github.com/go-ego/gse/bleve"  // 注册 gse tokenizer
)

// analyzer 定义（在 IndexMapping 中注册）
analyzerDef := map[string]interface{}{
    "type":      "custom",
    "tokenizer": "gse",              // gse 词级别分词：
                                     // "知识单元" → ["知识单元"]（词典命中）
                                     // "深度学习" → ["深度", "学习"] 或 ["深度学习"]
    "token_filters": []string{
        "to_lower",                  // ASCII 小写
    },
}
```

**gse 选型理由：**
- 词典分词，精度优于 bigram（无跨词噪音 token）
- 支持加载自定义词典文件，可直接将领域词（"知识单元"、"知识点网络"等）录入，避免被切断
- `github.com/go-ego/gse/bleve` 提供标准 Bleve tokenizer 注册接口，与 Bleve 集成无侵入
- bigram 会产生如"识单"这类无意义字对，在精确匹配场景下引入假阳性

**三个索引均使用同一 analyzer（`wiki_brain`），在 IndexMapping 中统一配置：**

```go
// 以 units index 为例，points index / outlines index 同理
indexMapping := bleve.NewIndexMapping()
indexMapping.AddCustomAnalyzer("wiki_brain", analyzerDef)
indexMapping.DefaultAnalyzer = "wiki_brain"
```

**查询时同样使用 `wiki_brain` analyzer**，保证索引和查询的分词方式一致：

```go
query := bleve.NewMatchQuery(searchText)
query.Analyzer = "wiki_brain"
```

**自定义词典格式**（`config/dict/wiki_brain.txt`，每行一词）：
```text
知识单元
知识点
激活链接
知识点网络
```

### 步骤 6：异步任务队列

实现内存任务队列，用于 Source 处理、Unit 提取、Trace 写入的异步调度（Study 使用独立的 `time.Ticker`，不经过此队列）：

```text
Go buffered channel 作为队列；
独立 goroutine 顺序消费任务；
服务关闭时等待当前任务完成后再退出（graceful shutdown）；
任务失败记录错误日志，不重试，不阻塞后续任务；
不引入外部消息队列。
```

**队列任务类型**（消费者 goroutine 按 `task_type` 字段路由到对应处理函数）：

```go
type Task struct {
    Type    string      // 任务类型，见下表
    Payload interface{} // 具体载荷，按 Type 断言
}

// TaskTypeSourceProcess：Source 完整处理链（格式转换 → 规范化 → outline → 摘要 → domain 匹配）
// Payload: SourceTask{ SourceID string }
const TaskTypeSourceProcess = "source_process"

// TaskTypeUnitExtract：Unit 提取链（切块 → LLM 提取 → KPN → Concept 匹配）
// Payload: UnitTask{ SourceID string }
const TaskTypeUnitExtract = "unit_extract"

// TaskTypeTrace：Trace 异步写入（质量分级 → 共现更新 → Learning Event）
// Payload: TraceTask{ Result AnswerResult }
// AnswerResult 由 Answer 模块定义，Trace 消费者接收后执行全部 Trace 步骤
const TaskTypeTrace = "trace_write"
```

消费者结构示例（各模块注册自己的处理函数，consumer 按类型分发）：

```go
func (q *Queue) consume(task Task) {
    switch task.Type {
    case TaskTypeSourceProcess:
        q.sourceHandler(task.Payload.(SourceTask))
    case TaskTypeUnitExtract:
        q.unitHandler(task.Payload.(UnitTask))
    case TaskTypeTrace:
        q.traceHandler(task.Payload.(TraceTask))
    default:
        log.Error("unknown task type", "type", task.Type)
    }
}
```

### 步骤 7：结构化日志

使用 Go 标准库或成熟日志库，输出结构化日志（JSON 格式）。支持：

```text
日志级别（debug / info / warn / error）；
同时输出到 console 和文件；
日志文件按大小和天数轮转；
每条日志携带 request_id 或 trace_id，便于关联同一次请求的日志。
```

### 步骤 8：HTTP API 框架

选定 Go HTTP 框架，完成以下初始化：

```text
路由注册（各模块注册自己的路由）；
统一错误响应格式；
请求日志中间件（记录方法、路径、耗时、状态码）；
request_id 注入中间件；
前端静态文件服务（见下）。
```

**前端静态文件服务（前后端一体）：**

```go
//go:embed web/index.html
var webFS embed.FS

// 注册根路由，GET / 返回嵌入的 index.html
// 前端直接调用同域 /api/* 接口，无跨域问题，无需独立前端服务器
mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    http.ServeFileFS(w, r, webFS, "web/index.html")
}))
```

`web/index.html` 是单一 HTML 文件，内嵌全部 CSS 和 JS，通过 `go:embed` 编译进二进制。用户访问 `http://localhost:8080` 即可使用控制台，无需任何前端构建或独立部署步骤。

HTTP API 遵循 OpenAPI 描述，接口定义先于实现完成。

### 步骤 9：预制数据初始化

服务启动时，在 SQLite 初始化（步骤 2）完成后，自动从 `preset/domains.json` 读取预制的领域和概念数据，写入 `domains` 和 `concepts` 表。

**`domains.json` 格式**：

```json
{
  "domains": [
    {
      "id": "software-engineering",
      "name": "软件工程",
      "description": "软件设计、开发、部署和运维",
      "concepts": [
        {"id": "deployment", "name": "部署", "description": "应用程序发布和运行环境配置"},
        {"id": "architecture", "name": "架构设计", "description": "系统结构和模块划分"}
      ]
    }
  ]
}
```

**写入逻辑**：

```text
遍历 domains 数组，按 domain_id UPSERT（INSERT OR IGNORE）到 domains 表；
遍历每个 domain 下的 concepts，按 concept_id UPSERT 到 concepts 表；
UPSERT 策略：已存在则跳过（不覆盖运行时修改），不存在则插入；
文件不存在或解析失败：记录 warn 日志，不阻断启动。
```

### 步骤 10：测试基础设施

为后续所有模块的测试提供：

```text
创建临时 SQLite 数据库（测试结束后清理）；
创建临时文件目录；
fake LLM client（可配置预设响应和错误注入）；
配置加载测试工具（从字符串或 map 加载）；
JSON Schema 校验测试工具。
```
