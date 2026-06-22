# 第一版项目结构

本文档规定第一版必须使用的初始目录结构。

如果现有仓库已经有不同但等价的 Go 工程结构，实现时可以映射到现有结构；否则按本文档创建。

不要创建第二版 agent、网页导入、人工审核工作台或完整 lifecycle service。

## 目录结构

```text
cmd/wiki-brain/
  main.go

internal/platform/
  config/
  errors/
  logging/
  storage/
  files/
  llm/
  httpx/
  jobs/
  httpserver/

web/
  embed.go
  dist/
    index.html
    css/
    js/

internal/knowledge/
  source/
  unit/
  precompile/
  lifecycle/

internal/searchindex/

internal/services/
  problemframing/
  retrieval/
  evidencetracing/
  gapdetection/
  conflictdetection/
  verification/
  workingmodel/
  reasoning/
  responseassembly/
  learningsignal/
  trace/
  study/

internal/agents/
  retrieval/
  reasoning/

internal/router/
  cognitive/

internal/api/
  source/
  query/
  trace/
  study/
  health/

internal/db/
  migrations/
  repositories/

configs/
  config.example.yaml

runtime 默认读取：

```text
/Users/jxu/Code/knowledge/schema/config.yml
```

`configs/config.example.yaml` 只作为示例模板，不是第一版默认运行配置来源。

prompts/
  precompile/
  retrieval/
  working_model/
  reasoning/
  router/

schemas/
  precompile/
  retrieval/
  working_model/
  reasoning/
  router/

data/
  sources/
    original/
    html/
    markdown/
  searchindex/

test/
  *.md
```

`test/*.md` 是第一版测试用内容文件。

它们用于 source / unit / precompile / retrieval / 端到端集成测试中的 Markdown 输入 fixture。

约束：

```text
test/*.md 不属于运行时数据目录；
测试可以把 test/*.md 复制或登记为 normalized Markdown；
测试不得直接绕过 source / unit / precompile 读取 test/*.md 生成答案；
运行时代码不得依赖 test/*.md。
```

## 包职责

| 包 | 职责 |
| --- | --- |
| `platform/config` | 加载 `config.yaml` 和环境变量 |
| `platform/errors` | 定义 `SystemError` 和错误转换 |
| `platform/logging` | 结构化日志 |
| `platform/storage` | SQLite 连接、事务、迁移 |
| `platform/files` | 文件路径策略和文件读写 |
| `platform/llm` | LLM client、fake client、prompt 渲染、schema 校验 |
| `platform/httpx` | 统一 API 响应、request_id |
| `platform/jobs` | 轻量后台 job runner |
| `knowledge/source` | SourceDocument、文件导入、转换服务调用、UnitSourceResult |
| `knowledge/unit` | Markdown 解析、KnowledgeUnit、SourceSpan、UnitPrecompileResult |
| `knowledge/precompile` | KnowledgePoint、SQLite 索引表、Bleve 索引同步、ActivationLink、PrecompileJob |
| `searchindex` | Bleve sidecar 词法索引（gse 分词、BM25）；见 `docs/impl/fts.md` |
| `knowledge/lifecycle` | 第一版只提供生命周期 warning 和候选信号，不做状态流转 |
| `services/retrieval` | 多路召回、融合排序、RetrievalResult |
| `services/evidencetracing` | EvidenceRef 回链 |
| `services/gapdetection` | cognitive_gaps / knowledge_gaps |
| `services/conflictdetection` | conflict_cases |
| `services/verification` | VerificationStartResult 和异步外部证据任务 |
| `services/workingmodel` | WorkingModelResult |
| `services/reasoning` | ReasoningResult |
| `services/responseassembly` | answer、claim_refs、unsupported_claims |
| `services/learningsignal` | learning_signals |
| `services/trace` | TraceService 和 trace 表写入 |
| `services/study` | StudyJob、study_candidates |
| `agents/retrieval` | RetrievalAgent |
| `agents/reasoning` | ReasoningAgent |
| `router/cognitive` | CognitiveRouter |
| `api/*` | HTTP handlers，只做请求解析、调用 service/agent、返回统一响应 |
| `db/repositories` | Repository 实现，不放业务编排逻辑 |

## 命名约束

第一版必须使用以下核心类型名：

```text
SystemError
Result
SourceDocument
UnitSourceResult
UnitBuildResult
UnitPrecompileResult
PrecompileJob
PrecompileJobResult
RetrievalRequest
RetrievalResult
RouterRequest
RouterDecision
AgentRequest
AgentResult
WorkingModelRequest
WorkingModelResult
TraceRecord
StudyJob
StudyJobResult
VerificationStartResult
VerificationCompletionResult
ExternalEvidenceCandidate
```

不要另起语义接近的新名字，例如：

```text
DocChunk
Chunk
RAGResult
MemoryNode
GraphEdge
AutoConcept
```

这些名字会把实现带向传统 RAG 或知识图谱方向。

## 依赖方向

允许：

```text
api -> router / agents / knowledge services
agents -> services
services -> knowledge repositories / platform
knowledge modules -> repositories / platform
repositories -> platform/storage
```

禁止：

```text
repositories -> services
services -> agents
knowledge/source -> knowledge/unit 业务实现
knowledge/unit -> knowledge/precompile 业务实现
agents -> db repositories
llm client -> business packages
```

`source` 可以投递 `UnitSourceResult`。

`unit` 可以投递 `PrecompileJob`。

但上游模块不能直接调用下游模块内部实现来绕过 job/result 契约。

## 第一版不创建的目录

第一版不要创建：

```text
internal/agents/direct_memory/
internal/agents/verification/
internal/agents/conflict/
internal/agents/reflection_learning/
internal/review/
internal/web_crawler/
internal/vectorstore/
internal/knowledge/graph/
```

这些能力属于第二版或后续扩展。
