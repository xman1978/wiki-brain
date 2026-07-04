# Wiki-Brain 开发上下文

## 项目概述

Wiki-Brain 是一个知识检索系统，核心流程：文件导入 → KU/KP 提取 → 检索 → 回答 → 质量追踪 → 学习报告。

**MVP 已完成并通过测试**（internal/foundation、source、unit、retrieval、answer、trace、study、session 均已实现）。当前处于 **V1（能力提升版）** 实现阶段，目标：让检索信号能转化为长期记忆（ActivationLink、Wiki 初版），并反过来改变检索行为（激活路径优先、LLM 调用减少）。

实现文档：MVP 在 `docs/impl/mvp/`，V1 在 `docs/impl/v1/`，V2（暂不实现）在 `docs/impl/v2/`。**所有设计决策以文档为准，不得自行发明**；文档本身如有歧义或与下方"V1 关键设计决策"不一致，先向用户确认，不要edge-case 猜测实现。

## 实现顺序（严格按序，不得跳跃）

**MVP**（已完成）：1. Foundation → 2. Source → 3. Unit → 4. Retrieval → 5. Answer → 6. Trace → 7. Study → 8. Page

**V1**（进行中）：1. lifecycle → 2. activation → 3. trace（扩展） → 4. study（扩展） → 5. retrieval（扩展） → 6. evidence → 7. kpn（跨 Source） → 8. wiki → 9. page（扩展）

每个模块实现完成、`go test ./...` 全绿后，再开始下一个，不要并行推进多个模块。

## V1 关键设计决策（近期定稿，实现时务必对照 docs/impl/v1/ 精确核对，不要凭旧印象或直觉简化/扩展）

- **KPN 关系类型只有 2 种**：`related` / `contradicts`，`direction` 恒为 `bidirectional`（MVP 的 `internal/unit/service.go` 已是此实现）。V1 跨 Source 匹配（`kpn.md`）保持同样的 2 种，不要按早期设计文档的 5 种类型实现。
- **KU/KP lifecycle 只有 3 种状态**：`current` / `superseded` / `deprecated`，没有 candidate/needs_verification/conflicted/historical/retracted（详见 `docs/design/lifecycle.md` 第 2 节的场景推导）。`activation_links` 自己有独立状态机（candidate/verified/weakened/deprecated，见 `activation.md`），与 KU/KP 的 lifecycle 是两套不同状态，不要混用。
- **Source 重新上传用 Shadow Source 机制**：新文件先在隐藏的影子 Source（`sources.shadow_of` 指向目标 source_id）里走完全正常的 `source_process → unit_extract`（不改动该链路一行代码），全程不影响、不暴露原 Source；只有影子处理全部成功（含 KPN、Concept 匹配）后，才在一个事务里把影子内容的 `source_id` 改写为目标 source_id、原内容标记 `superseded`、影子行删除。创建影子时要跳过对目标 source_id 自身的文件名去重检查。影子失败直接丢弃，不需要回滚代码；提供 `POST /sources/:id/reupload/retry` 复用已有的 `POST /sources/:shadow_id/retry` 续跑逻辑。Retrieval 的 Domain 预过滤、Source 语义过滤要排除 `shadow_of IS NOT NULL` 的行。详见 `docs/impl/v1/lifecycle.md`。
- **Study 报告已有 `kpn_citation_rate`**：retrieval 层 `Evidence.origin` 字段、trace 层 `kpn_cited_count`/`cited_count`、study 层聚合已实现，V1 扩展 traces 表和报告结构时不要破坏这条链路。
- **Wiki 编译不是全自动的**：候选识别（Study）和 `needs_recompile` 标记是自动的，但编译/重编译永远需要人工调用 `POST /wiki/compile` 或 `/wiki/pages/:id/recompile` 确认后才执行，不要做成流水线自动编译。

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

- 回复时使用和提问时一样的语言；

- 在修改 bug 时，如果不确定问题的根本原因，不要直接修改代码，要和用户确认问题原因；

- 在修改 bug 时，不要破坏原本的设计和实现方案，除非用户确认要修改。

## 参考文档

### MVP（已完成）

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

### V1（进行中，按此顺序实现）

| 顺序 | 模块       | 文档                          |
| -- | -------- | --------------------------- |
| 1  | Lifecycle  | `docs/impl/v1/lifecycle.md` |
| 2  | Activation | `docs/impl/v1/activation.md` |
| 3  | Trace      | `docs/impl/v1/trace.md`     |
| 4  | Study      | `docs/impl/v1/study.md`     |
| 5  | Retrieval  | `docs/impl/v1/retrieval.md` |
| 6  | Evidence   | `docs/impl/v1/evidence.md`  |
| 7  | KPN        | `docs/impl/v1/kpn.md`       |
| 8  | Wiki       | `docs/impl/v1/wiki.md`      |
| 9  | Page       | `docs/impl/v1/page.md`      |

总览与范围边界：`docs/impl/v1/readme.md`；设计依据：`docs/design/lifecycle.md`（记忆生命周期）、`docs/design/precompile.md`（ActivationLink）、`docs/design/study.md`（学习机制）、`docs/design/retrieval.md`（分层检索）、`docs/design/wiki-compilation.md`（Wiki 编译）。
