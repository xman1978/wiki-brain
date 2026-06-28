# Wiki-Brain 开发上下文

## 项目概述

Wiki-Brain 是一个知识检索系统，核心流程：文件导入 → KU/KP 提取 → 检索 → 回答 → 质量追踪 → 学习报告。

实现文档在 `docs/impl/mvp/`，**所有设计决策以文档为准，不得自行发明**。

## 实现顺序（严格按序，不得跳跃）

1. Foundation → 2. Source → 3. Unit → 4. Retrieval → 5. Answer → 6. Trace → 7. Study → 8. Page

每个模块实现完成、测试通过后，再开始下一个。

## 技术栈

- Go 1.21+，模块路径：`github.com/jxman78/wiki-brain`（或以 go.mod 实际值为准）
- SQLite：`github.com/mattn/go-sqlite3`
- Bleve：`github.com/blevesearch/bleve/v2` + gse 分词：`github.com/go-ego/gse`、`github.com/go-ego/gse/bleve`
- HTTP：标准库 `net/http`
- 日志：`log/slog`（Go 标准库）
- 前端：单文件 `web/index.html`，`go:embed` 嵌入二进制

## 编码规范

- 包结构：`internal/<module>/`，每模块一个包
- 数据库 migration：`internal/foundation/db/migrations/` 下按版本号命名的 `.sql` 文件
- Prompt 文件：`config/prompts/<用途>.md`，格式见 `docs/impl/mvp/readme.md` Prompt 设计原则节
- 所有 LLM 调用通过 `LLMClient` interface，禁止直接调用 OpenAI SDK
- 测试使用 fake LLM client，不发起真实网络请求
- 错误处理：业务错误返回标准类型（见 foundation.md），不用裸 `errors.New`

## 关键约定

- 位置字段统一用 `line_start` / `line_end`（1-based, inclusive），**禁止** char/byte offset
- 异步队列 task 类型：`source_process` / `unit_extract` / `trace_write`（见 foundation.md）
- EvidenceSet.source_ref 序列化为 JSON 对象 `{"source_id","line_start","line_end"}`
- Study 用 `time.Ticker`，**不走**异步队列
- 预制数据文件：`preset/domains.json`，启动时 UPSERT 写入 domains/concepts 表
- JSON Schema 定义在 prompt 文件 `## Schema` 段内，校验的是**程序整合后的结果**，不是模型原始输出；程序负责解析→组装→再校验

## 要求

- 在修改 bug 时，如果不确定问题的根本原因，不要直接修改代码，要和用户确认问题原因；

- 在修改 bug 时，不要破坏原本的设计和实现方案，除非用户确认修改。

## 参考文档

| 模块         | 文档                            |
| ---------- | ----------------------------- |
| 总览         | `docs/impl/mvp/readme.md`     |
| Foundation | `docs/impl/mvp/foundation.md` |
| Source     | `docs/impl/mvp/source.md`     |
| Unit       | `docs/impl/mvp/unit.md`       |
| Retrieval  | `docs/impl/mvp/retrieval.md`  |
| Answer     | `docs/impl/mvp/answer.md`     |
| Trace      | `docs/impl/mvp/trace.md`      |
| Study      | `docs/impl/mvp/study.md`      |
| Page       | `docs/impl/mvp/page.md`       |
