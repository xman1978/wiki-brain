# Wiki-Brain MVP 实现概览

## 目标

MVP 阶段验证 Wiki-Brain 的核心假设：

1. 知识能否被有效组织为可检索的 KnowledgeUnit / KnowledgePoint；
2. 检索能否稳定找到正确证据；
3. 回答能否基于证据生成并可追溯；
4. 使用过程中产生的检索信号，能否积累为有效的知识激活路径候选。

MVP 不是完整的认知系统，而是验证上述四点的最小可运行闭环。

---

## 实现顺序

模块之间存在明确依赖关系，按以下顺序逐步实现，每个模块可独立开发和测试：

```text
1. Foundation   基础设施，所有模块的前置条件
2. Source       知识导入，系统的数据入口
3. Unit         知识单元提取，依赖 Source 规范化产出
4. Retrieval    知识检索，依赖 Unit 产出的 KU / KP / KPN
5. Answer       回答生成，依赖 Retrieval 产出的 EvidenceSet
6. Trace        质量记录，依赖 Answer 产出，异步执行
7. Study        学习分析，依赖 Trace 积累，定时运行
8. Page         测试控制台，依赖全部后端接口，用于端到端验证
```

---

## 各模块概要

### Foundation（[foundation.md](./foundation.md)）

系统基础设施，所有模块共用。

- SQLite 数据库（含 schema migration）
- Bleve 全文索引（units / points / outlines 三个索引）
- LLM 统一 client（支持 fake client 用于测试）
- 异步任务队列（buffered channel，graceful shutdown）
- 结构化日志、HTTP 框架
- 配置文件加载（config.yml + 环境变量覆盖）
- 预制数据初始化（`preset/domains.json`，预载领域和概念）

核心表：`domains`、`concepts`

---

### Source（[source.md](./source.md)）

知识文件导入与元数据管理。

- 支持 Markdown / HTML / PDF / Word，统一转换为规范化 Markdown
- 提取文档大纲（`source_outlines`），作为 Unit 提取的分段依据
- 生成文档摘要（LLM 提取；失败时拼接一级标题关键词兜底）
- Domain 匹配（LLM 将 source 归类到预制 domain，`domain_id=null` 的 source 始终参与检索）
- Source 完成后自动触发 Unit 提取任务入队

核心表：`sources`、`source_outlines`

---

### Unit（[unit.md](./unit.md)）

从 Source 中提取 KnowledgeUnit、KnowledgePoint 及 KPN 关系。

- 按大纲叶节点分段（`segment_max_chars=4000`），单次 LLM 调用同时产出 KU[] 和 KP[]（平铺 JSON，uid 关联）
- Concept 批量匹配（≤50 KU/次，按 source domain 缩小候选 concept 列表）
- KPN 关系生成：5 种关系类型（related / hierarchical / depends / supplements / contradicts），方向由程序从类型映射，不让模型输出
- 写入 Bleve 索引（units index / points index）

核心表：`knowledge_units`、`knowledge_points`、`knowledge_point_relations`

---

### Retrieval（[retrieval.md](./retrieval.md)）

两级检索，输出带 fact_id 的 EvidenceSet。

**检索流程：**

```text
Domain 预过滤（LLM 匹配问题相关 domain，缩小 source 范围）
  → Source 语义过滤（LLM 判断哪些 source 与问题相关）
  → 目录结构召回（Outline FTS；评分 < 0.5 时并发触发 LLM fallback）
  → FTS 召回（units + points 双索引）
  → RRF 合并（k=60，Top N=20 截断后进入 Rerank）
  → LLM Rerank（逐条分类：direct / supporting / irrelevant）
  → KPN 扩展（direct KU 的 KPN 邻居补充为 supporting）
  → 充分性判断（short / deep 路径分叉）
  → 构建 EvidenceSet（含 fact_id、source_ref）
```

**关键决策：**
- Outline FTS 阈值 0.5：低于阈值才触发 LLM fallback，避免 FTS 高分跳过语义补偿
- 多个低分 source 并发 LLM fallback，受 `llm.max_concurrency=5` 约束
- Top N=20 防止 Rerank prompt token 超限
- KPN 扩展方向感知：bidirectional 类型双向查，directed 类型只跟 source→target

核心输出：`EvidenceSet`（in-memory，每请求生命周期）

---

### Answer（[answer.md](./answer.md)）

基于 EvidenceSet 生成可追溯的回答。

**四条路径：**

| 路径 | 触发条件 | 处理方式 |
|------|---------|---------|
| `short` | direct 非空 | LLM 直接回答，direct + supporting 均传入 |
| `deep` | direct 为空且 supporting 非空，或请求指定 `deep=true` 且存在任意证据 | LLM 结构化推理，要求识别变量、梳理推理路径、指出证据缺口 |
| `none` | direct + supporting 均为空 | 程序返回固定文本，不调 LLM |
| `error` | LLM 调用失败（重试 1 次后） | 返回降级文本，记录 error 日志 |

- Citation 合法性校验：过滤 LLM 幻构的 fact_id
- 完整 EvidenceSet 快照（`evidence_snapshot`）写入 answers 表，保证 fact_id 事后可追溯
- 写库完成后异步将 AnswerResult 投入队列通知 Trace

核心表：`answers`

---

### Trace（[trace.md](./trace.md)）

以 Rerank 质量为核心信号，记录每次问答的检索质量，积累「问题 × KP」共现统计。

**质量分级：**

```text
confident：direct evidence 非空且被 Answer 引用  → 检索准确，有效学习信号
partial  ：无 direct 或 direct 未被引用           → 检索偏弱
gap      ：direct + supporting 均为空             → 知识库盲区，立即生成 knowledge_gap 事件
```

**问题去重：**
- 对归一化问题文本计算 `question_hash`（SHA256 前 16 字节）
- 同一问题对同一 KP 只贡献一次共现计数（查 `cooccurrence_question_dedup` 表）
- 不同问题词项相同（如「什么是锁？」和「锁是什么？」）仍各自计数

**共现积累：**
- `question_kp_cooccurrence`：统计 (question_terms, point_id) 的 hit_count 和 confident_count
- confident_count 反映「多少不同问题以高置信命中该 KP」，是 Study 的核心输入

核心表：`traces`、`question_kp_cooccurrence`、`cooccurrence_question_dedup`、`learning_events`

---

### Study（[study.md](./study.md)）

定时扫描共现数据，生成学习报告，供人工审核 ActivationLink 和 Wiki 候选。

**MVP 定位：验证而非执行。** Study 不自动修改知识结构，只输出候选和统计结论供人工决策。

**定时扫描（默认每小时）：**
1. 共现扫描：找出满足阈值的 (question_terms, point_id) → 写入 `link_candidates`
2. 盲区聚合：消费 `knowledge_gap` 事件 → 累积 `knowledge_gaps`
3. 学习报告生成：多维度统计 → 写入 `study_reports`

**学习报告包含：**
- **总结**：问答质量分布（confident / partial / gap 比率）
- **ActivationLink 候选**：每条候选附 signal_purity、activation_breadth、short_path_rate、has_kpn_neighbors，以及 strong / candidate 分级结论
- **Wiki 候选**：同 concept 下高置信 KP 聚合，附 kpn_connection_count 和 days_active，以及 ready / needs_more_data 结论
- **知识盲区**：高频 gap 列表

报告以 SQL 为主，辅以少量应用层 JSON 解析，不调用 LLM。

核心表：`link_candidates`、`knowledge_gaps`、`study_reports`

---

### Page（[page.md](./page.md)）

MVP 测试控制台（`web/index.html`），用于端到端验证核心闭环，通过 `go:embed` 嵌入二进制，无需前端构建链。

**布局：**
- 顶部导航：品牌标识 · 历史抽屉切换 · 健康状态圆点 · 文件 · 学习报告 · 解释抽屉切换 · 调试开关 · 设置
- 问答区（全宽）：对话线程 + 底部输入框（支持 deep / require_evidence 开关）
- 历史抽屉（左滑入）：当次会话的问答历史，按 answer_id 重新加载，并异步补查 trace_id
- 解释抽屉（右滑入）：有新回答时自动打开，含概览 / 来源 / 缺口三个标签页
- 文件抽屉（右滑入）：上传 + 来源列表，轮询 source 状态
- 学习报告视图（全页覆盖）：ActivationLink 候选表 · Wiki 候选表 · 知识盲区表

**调试模式**额外展示：共现统计 · 思考记录 · 学习事件 · 原始响应 JSON。

**技术约束：** 纯 HTML/CSS/ES Modules，不引入框架；无真实数据时展示空状态，不伪造数据；所有接口错误展示 status 和响应体原文。

不调用 LLM，无数据库表，不写入任何持久化状态。

---

## LLM 调用分布

| 阶段 | 调用 | 必要性 |
|------|------|--------|
| Source | 摘要生成 | 每个 source 1 次 |
| Source | Domain 匹配 | 每个 source 1 次 |
| Source | 语义 outline 生成 | 按需触发（满足条件 A~F），单遍或两遍策略 |
| Unit | KU+KP 联合提取 | 每个分段 1 次 |
| Unit | Concept 批量匹配 | 每批 ≤50 KU 1 次 |
| Unit | KPN 关系生成 | 每个 source 1 次 |
| Retrieval | Domain 预过滤 | 每次问答 1 次 |
| Retrieval | Source 语义过滤 | 每次问答 1 次 |
| Retrieval | Outline LLM fallback | 按需，并发触发 |
| Retrieval | Rerank | 每次问答 1 次 |
| Answer | 回答生成 | 每次问答 1 次（none 路径除外） |
| Trace | — | 不调用 LLM |
| Study | — | 不调用 LLM |

正常问答至少 4 次 LLM 调用（Domain 预过滤 + Source 过滤 + Rerank + Answer）。随着 ActivationLink 成熟，未来可降至 1-2 次。

---

## Prompt 设计原则

系统使用 qwen3-30b 模型，所有 LLM 调用遵循以下原则。

### Prompt 文件格式

每个 prompt 为独立的 Markdown 文件（`.md`），文件名为调用用途（如 `rerank.md`），版本号在文件内 frontmatter 中维护。文件包含三个固定段：

~~~markdown
---
version: v1
---

## System

你是知识提取助手，负责从文档段落中提取知识单元和知识点。

按以下格式输出，不输出任何其他内容：
{"units": [{"unit_id": "1", "center": "...", "line_start": 1, "line_end": 8}], "points": [{"point_id": "1", "unit_id": "1", "content": "...", "type": "definition"}]}

## User

段落内容：
{{content}}

## Schema

```json
{
  "type": "object",
  "required": ["units", "points"],
  "properties": {
    "units": {"type": "array"},
    "points": {"type": "array"}
  }
}
```
~~~

**`## System` 段**：任务角色 + 输出格式说明（含示例 JSON）。不含任何 `{{变量}}`，内容完全静态，可被提供商缓存。

**`## User` 段**：本次调用的全部变量输入，所有 `{{变量}}` 占位符均在此段。程序在调用前完成变量替换。

**`## Schema` 段**：定义程序整合后的目标数据结构，不注入模型，仅供程序侧使用。

模型输出经程序解析后，程序负责组装为符合此 Schema 的结构（例如：将相对行号转换为绝对行号、将 uid 引用替换为 UUID、推断父子关系等）。Schema 校验的是程序整合后的结果，而非模型的原始输出。若模型输出存在小偏差，程序应尝试修复（如截断超长字段、补全缺失可推导字段），而非直接报错丢弃。

程序解析规则：读取文件后，从 frontmatter 提取 `version`，按 `## System` / `## User` / `## Schema` 标题切分三段内容；`## Schema` 段内的 JSON 代码块提取后用于 `jsonschema` 校验整合结果。

缓存收益：同类型调用的 `## System` 段完全相同，提供商可复用已处理的 system token，只需处理 user 段内容。

### 其他原则

- **示例 JSON 而非 JSON Schema DSL**：`## System` 段注入示例 JSON，要求模型按该格式输出；`## Schema` 段定义程序整合后的目标结构，用 `jsonschema` 校验整合结果，不注入模型。不使用独立的 `<用途>_schema.json` 文件
- **扁平结构，禁止数组嵌套数组**：KU[] 和 KP[] 平铺，通过 `uid` 关联；禁止 `units[]` 内嵌 `points[]`
- **枚举值 ≤ 5 个**：超过 5 个时合并，细分由程序处理
- **只让模型生成无法程序化推导的字段**：id、顺序、父子关系、方向等均由程序生成，不进入 schema
- **system 段保持精简**：只含角色定义、约束和格式，去掉说明性文字和反例；说明越多缓存命中率越低

---

## 数据存储

```text
文件系统
  data/sources/original/<source_id>.<ext>   原始文件（只写一次，不修改）
  data/sources/html/<source_id>.html         HTML 预览（可选，FileView 生成）
  data/sources/markdown/<source_id>.md       规范化 Markdown（Unit 提取的唯一输入）

SQLite（单文件 data/wiki-brain.db）
  domains、concepts                  预制知识领域和概念
  sources、source_outlines           Source 及目录结构
  knowledge_units、knowledge_points  知识单元和知识点
  knowledge_point_relations          KPN 关系
  answers                            问答记录（含 evidence_snapshot）
  traces                             质量记录
  question_kp_cooccurrence           共现统计
  cooccurrence_question_dedup        问题去重
  learning_events                    学习事件
  link_candidates、knowledge_gaps    Study 候选输出
  study_reports                      学习报告

Bleve 索引（data/searchindex/）
  units_index                        KnowledgeUnit 全文索引
  points_index                       KnowledgePoint 全文索引
  outlines_index                     Source 目录节点全文索引
（sources 表通过 SQLite 直接查询，不建 Bleve 索引）
```

---

## 项目目录结构

```text
wiki-brain/
├── cmd/
│   └── server/
│       └── main.go                  # 服务入口，初始化各模块并启动 HTTP server
├── internal/
│   ├── foundation/                  # 基础设施
│   │   ├── db/                      # SQLite 初始化、migration
│   │   ├── index/                   # Bleve 索引（units / points / outlines）
│   │   ├── llm/                     # LLM 统一 client（含 fake client）
│   │   ├── queue/                   # 异步任务队列（buffered channel）
│   │   └── config/                  # 配置加载（config.yml + 环境变量覆盖）
│   ├── source/                      # Source 模块
│   ├── unit/                        # Unit 模块
│   ├── retrieval/                   # Retrieval 模块
│   ├── answer/                      # Answer 模块
│   ├── trace/                       # Trace 模块
│   └── study/                       # Study 模块
├── config/
│   ├── config.yml                   # 主配置文件（见下方配置说明）
│   ├── dict/
│   │   └── wiki_brain.txt           # gse 自定义词典，每行一个领域词（如"知识单元"、"知识点"）
│   └── prompts/                     # Prompt 文件，Markdown 格式，一个调用对应一个文件
│       │                            # 文件名为调用用途，版本号在文件内 frontmatter 中管理
│       ├── source_summary.md
│       ├── source_domain_match.md
│       ├── outline_filter.md
│       ├── outline_semantic_full.md
│       ├── outline_semantic_skeleton.md
│       ├── outline_semantic_chunk.md
│       ├── unit_extract.md
│       ├── unit_extract_retry.md
│       ├── kpn_extract.md
│       ├── unit_concept_match.md
│       ├── question_domain_match.md
│       ├── source_filter.md
│       ├── rerank.md
│       ├── answer_short.md
│       └── answer_deep.md
├── preset/
│   └── domains.json                 # 预制领域和概念数据，启动时 UPSERT 写入
├── data/
│   ├── wiki-brain.db                # SQLite 数据库（单文件）
│   ├── sources/                     # 知识文件存储（按类型平铺）
│   │   ├── original/                # 原始文件，<source_id>.<ext>
│   │   ├── html/                    # HTML 预览（可选），<source_id>.html
│   │   └── markdown/                # 规范化 Markdown，<source_id>.md
│   └── searchindex/                 # Bleve 索引目录
│       ├── units/
│       ├── points/
│       └── outlines/
├── web/
│   └── index.html                   # 测试控制台（单文件，内嵌全部 CSS 和 JS，通过 go:embed 嵌入二进制）
├── go.mod
└── go.sum
```

---

## 配置文件（config.yml）

以下为完整配置样例，涵盖所有模块的可调参数。

```yaml
# LLM 配置
llm:
  provider:          "openai"                      # openai 兼容接口
  base_url:          "${LLM_BASE_URL}"             # 支持环境变量替换
  api_key:           "LLM_API_KEY"                 # 直接填写字符串，或填写环境变量名（如 LLM_API_KEY），程序自动读取
  default_model:     "qwen3-30b"
  extraction_model:  ""                            # 留空则使用 default_model
  reasoning_model:   ""                            # 留空则使用 default_model
  timeout:           "60s"
  max_concurrency:   5                             # 全局最大并发 LLM 调用数
  max_retries:       1                             # 单次调用失败后重试次数

# HTTP 服务器
server:
  port:          8080
  read_timeout:  "30s"
  write_timeout: "60s"

# 数据库
database:
  path: "data/wiki-brain.db"                        # SQLite 文件路径，相对于工作目录

# Bleve 索引
index:
  path: "data/searchindex"                         # 索引根目录，内含 units / points / outlines 子目录

# 异步任务队列
queue:
  buffer_size: 100                                 # buffered channel 容量

# FileView 服务（格式转换）
fileview:
  base_url:          "http://192.168.0.169:9000"   # FileView JAR 服务地址
  poll_interval_ms:  1500                          # 轮询间隔（毫秒）
  max_poll_seconds:  600                           # 最长等待时间

# Source 模块
source:
  upload_dir:           "data/sources"             # 原始文件和规范化 Markdown 存储根目录
  segment_max_chars:    4000                        # 大纲叶节点最大字符数（rune）；语义 outline 生成和 Unit 切块共用此值
  single_pass_threshold: 12000                      # 语义 outline 生成：文档字符数（rune）低于此值时单遍处理，否则两遍
  min_segment_chars:    400                         # Unit 切块：相邻叶节点合计低于此字符数（rune）时合并为一段

# Retrieval 模块
retrieval:
  outline_fts_min_score: 0.5                       # Outline FTS 最低分；低于此值对该 source 触发 LLM fallback
  rerank_top_n:          20                        # RRF 合并后截取的最大候选数，超出部分在 Rerank 前丢弃

# Study 模块
study:
  schedule_interval:       "1h"                    # 扫描周期（Go duration 格式）
  candidate_confident_min: 5                       # 标记 ActivationLink 候选所需最低 confident_count
  candidate_ratio_min:     0.6                     # confident_count / hit_count 最低比值
  wiki_kp_min:             4                       # 触发 Wiki 候选所需同 concept 下 KP 最低数量
  wiki_confident_min:      8                       # Wiki 候选 KP 的最低 confident_count
  gap_hit_threshold:       3                       # 同一 question_terms 下 gap 累积次数告警阈值
  scan_batch_size:         200                     # 每次从 cooccurrence 读取的最大行数
  report_period_days:      30                      # 报告统计时间窗口（天）
  report_max_keep:         10                      # 最多保留的历史报告份数
```

环境变量优先级高于 config.yml。`llm.api_key` 字段特殊：填写的值若能匹配环境变量名（如 `LLM_API_KEY`），程序自动读取该环境变量的值；其他字段通过 `WB_` 前缀加大写字段路径覆盖，例如 `WB_LLM_BASE_URL`。

---

## 技术栈

```text
语言：Go 1.21+
存储：SQLite（github.com/mattn/go-sqlite3）
索引：Bleve（github.com/blevesearch/bleve/v2）+ gse 中文分词（github.com/go-ego/gse、github.com/go-ego/gse/bleve）
HTTP：标准库 net/http 或轻量 router
日志：结构化日志（slog 或 zap）
LLM：统一 client 封装，支持 OpenAI 兼容接口
前端：HTML + JS（ES Modules），无框架、无构建步骤；通过 go:embed 嵌入二进制，Go HTTP 服务在 GET / 路由直接返回，前后端一体部署
```

不依赖向量数据库、图数据库、消息队列或分布式组件，单机即可运行。

---

## MVP 成功标准

```text
知识导入：稳定导入 Markdown / PDF / Word，大纲提取和摘要生成正确；
知识提取：KU / KP / KPN 按预期生成，Bleve 索引可查；
检索：给定问题，Rerank 后能返回含 direct evidence 的 EvidenceSet；
回答：回答引用 fact_id 可追溯到具体 KU 和来源位置；
Trace：每次问答正确分级（confident / partial / gap），共现表按不同问题去重累积；
Study：定时扫描后生成报告，报告中候选的 signal_purity 和 activation_breadth 统计符合预期；
Page：测试控制台可完整走通"文件上传 → 问答 → 证据查看 → 学习报告"闭环，调试模式下可查看 trace 详情和共现统计；
人工核查：Study 报告中的 ActivationLink 候选经人工抽样验证，语义相关性达到可接受水平。
```

如果上述标准成立，则 Wiki-Brain 核心设计具有工程可行性，可进入 V2 阶段实现完整的 ActivationLink 状态机、Wiki 编译和跨 Source KPN。
