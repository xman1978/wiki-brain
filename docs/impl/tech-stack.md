# 技术栈

本文档只说明知识大脑第一版实现所使用的基础技术。

它不描述各业务组件如何实现。source、unit、precompile、service、agent、router、trace、study、lifecycle 等模块的具体实现，应放在各自的实现文档中。

第一版技术栈可以概括为：

```text
Go
SQLite
文件系统
HTML
Markdown
YAML
JSON Schema
Prompt 文件
OpenAI API
结构化日志
HTTP API
OpenAPI
MCP
```

## 1. Go

第一版使用 Go 作为主开发语言。

原因：

```text
适合构建长期运行的本地服务；
部署简单；
并发和文件处理能力稳定；
适合实现清晰的接口和模块边界；
对 SQLite、YAML、日志、HTTP API 支持成熟。
```

建议使用当前稳定 Go 版本。

如果本地环境暂时不支持最新版本，项目应在 `go.mod` 中明确实际版本。

## 2. SQLite

第一版使用 SQLite 作为主数据库。

SQLite 用于保存结构化数据，例如元数据、索引、关系、运行记录和配置状态。

使用原则：

```text
启用 WAL；
启用 foreign_keys；
写操作使用事务；
时间字段统一格式；
JSON 数据可以先用 TEXT 保存；
材料侧词法检索使用 Bleve sidecar + gse 分词（见 docs/impl/fts.md），不使用 SQLite FTS5；
不要在第一版引入独立数据库服务。
```

第一版不使用独立向量数据库。

如果需要向量检索，可以先作为可选能力保留接口，不作为核心存储假设。

## 2.1 词法检索（Bleve + gse）

材料侧全文检索使用嵌入式 Bleve 索引 + gse 分词，详见 `docs/impl/fts.md`。

```text
Bleve 索引目录：data/searchindex/
不使用 SQLite FTS5；
不使用 CGO / gojieba；
倒排索引与 SQLite 元数据表双写，SQLite 为 source of truth。
```

## 3. 文件系统

文件系统用于保存大文本、原始文件和可人工查看的内容。

第一版文件存储包括：

```text
原始文件；
预览 HTML；
规范化 Markdown；
prompt 文件；
trace 导出文件；
其他导出结果。
```

建议目录：

```text
data/
  sources/
    original/
    html/
    markdown/
  searchindex/
  prompts/
  traces/
  exports/
```

原始文件保存到 `data/sources/original/`。

预览 HTML 保存到 `data/sources/html/`。

规范化 Markdown 保存到 `data/sources/markdown/`。

知识单元不单独保存为文件，知识单元位置应记录在数据库中。

## 4. HTML

HTML 用于预览和人工查看。

系统从原始材料转换出的 HTML，应尽量保留适合阅读和核对的内容结构。

HTML 不作为知识构建的主要输入。

## 5. Markdown

Markdown 是知识构建的主要文本中间格式。

所有进入知识处理链路的材料，都应转换为规范化 Markdown。

知识单元、认知结构预编译和后续知识构建应基于 Markdown，而不是直接基于任意原始文件格式。

## 6. YAML

系统配置使用 YAML。

默认配置文件为：

```text
/Users/jxu/Code/knowledge/schema/config.yml
```

本地开发可以通过 `WIKI_CONFIG_PATH` 或启动参数 `--config` 覆盖配置文件路径。

配置文件负责保存系统运行配置，不保存业务知识。

建议配置结构：

```yaml
app:
  name: wiki
  env: development

database:
  driver: sqlite
  path: data/wiki.db

storage:
  root: data
  sources_dir: data/sources
  prompts_dir: data/prompts
  traces_dir: data/traces

source:
  convert:
    base_url: http://192.168.0.169:9000
    markdown_toc: true
    poll_interval_seconds: 2
    timeout_seconds: 300
    max_retries: 2

openai:
  api_key: OPENAI_API_KEY
  base_url: https://api.openai.com/v1
  timeout_seconds: 60
  max_retries: 3
  models:
    default:
      name: gpt-4.1
      temperature: 0.2
      max_input_tokens: 1000000
      max_output_tokens: 4096
    reasoning:
      name: o4-mini
      temperature: 0.1
      max_input_tokens: 200000
      max_output_tokens: 8192
    extraction:
      name: gpt-4.1-mini
      temperature: 0
      max_input_tokens: 1000000
      max_output_tokens: 4096
    embedding:
      name: text-embedding-3-small
      max_input_tokens: 8191
      dimensions: 1536

logging:
  level: info
  outputs:
    - console
    - file
  file:
    path: logs/wiki.log
    max_size_mb: 100
    max_backups: 10
    max_age_days: 30
    compress: true
```

第一版必须使用 `/Users/jxu/Code/knowledge/schema/config.yml` 中的 `openai` 配置作为模型配置来源。

业务模块不得硬编码模型名、上下文长度或 OpenAI base_url。

`openai.api_key` 支持两种写法。

如果是双引号字符串，表示直接使用该字符串作为密钥：

```yaml
openai:
  api_key: "sk-..."
```

如果不是双引号字符串，表示该值是系统环境变量名：

```yaml
openai:
  api_key: OPENAI_API_KEY
```

这种情况下，系统从 `OPENAI_API_KEY` 环境变量读取真实密钥。

生产环境建议使用环境变量方式。

## 7. JSON Schema

JSON Schema 用于描述结构化输入输出。

适用范围包括：

```text
LLM 结构化输出；
service 输入输出；
agent 输出；
HTTP API 请求和响应；
配置校验；
测试数据校验。
```

## 8. Prompt 文件

提示词模板使用文件保存。

建议使用：

```text
prompt.md
```

提示词不应硬编码在业务代码中。

Prompt 文件应至少包含：

```text
用途；
输入变量；
输出格式；
约束；
示例；
版本号。
```

Prompt 一旦用于生成有效知识数据，不应无记录地覆盖。

如果 prompt 发生变化，应能识别由哪个 prompt 版本生成了对应结果。

## 9. OpenAI API

第一版通过 OpenAI API 调用大模型。

LLM 调用必须通过统一 client。

业务模块不应直接调用 OpenAI SDK。

统一 client 至少负责：

```text
读取模型配置；
读取和渲染 prompt；
执行模型调用；
处理超时；
处理重试；
记录模型名称；
记录 prompt 版本；
执行结构化输出校验；
返回标准错误。
```

模型按用途配置。

建议至少区分：

```text
default
  通用回答、轻量分析。

reasoning
  复杂推理、工作模型、冲突检测。

extraction
  source 解析、知识单元构建、知识点提取和候选认知线索生成。

embedding
  可选向量检索能力。
```

## 10. 结构化日志

日志写入 `logs/` 目录。

日志配置通过 `config.yaml` 管理。

需要支持：

```text
日志级别；
输出端；
日志文件路径；
单文件大小；
保留文件数量；
保留天数；
是否压缩；
结构化日志；
请求或 trace 关联 ID。
```

推荐日志级别：

```text
debug
info
warn
error
```

日志用于工程运行观测，不替代 trace。

## 11. HTTP API

第一版可以提供本地 HTTP API。

HTTP API 用于本地 UI、CLI、测试工具或外部系统访问知识大脑能力。

对外接口建议使用 OpenAPI 描述。

## 12. OpenAPI

OpenAPI 用于描述 HTTP API。

它用于：

```text
接口文档；
请求响应结构；
测试生成；
客户端生成；
对外集成。
```

## 13. MCP

未来如果要给 Agent 平台使用，应优先考虑暴露 MCP server。

MCP 是对外集成方式，不应反向约束内部架构。

知识大脑可以通过 MCP 向 Agent 平台暴露知识能力和思维能力。

## 14. 测试基础设施

第一版测试应支持：

```text
SQLite 测试库；
临时文件目录；
fake LLM client；
配置加载测试；
JSON Schema 校验测试；
HTTP API 测试；
日志输出测试。
```

LLM 调用必须支持 fake client，以便测试稳定运行。

## 15. 总结

本系统第一版技术栈是：

```text
Go + SQLite + 文件系统 + HTML + Markdown + YAML + JSON Schema + Prompt 文件 + OpenAI API + 结构化日志 + HTTP API。
```

OpenAPI 和 MCP 用于对外集成。

具体模块实现不在本文档展开。
