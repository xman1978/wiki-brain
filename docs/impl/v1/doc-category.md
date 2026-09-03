# 文档分类（doc_category）实现方案

设计依据：`docs/design/doc-category.md`。**不排入 CLAUDE.md「实现顺序」的 V1 强制序列**，独立实现，不依赖也不影响其余 V1 模块的推进节奏。

## 1. 数据结构

迁移 075（`internal/foundation/db/migrations/075_doc_categories.sql`）：

```sql
CREATE TABLE IF NOT EXISTS doc_categories (
    category_id TEXT PRIMARY KEY,
    domain_id   TEXT NOT NULL REFERENCES domains(domain_id),
    name        TEXT NOT NULL,
    description TEXT,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_doc_categories_domain_id ON doc_categories(domain_id);

ALTER TABLE sources ADD COLUMN doc_category_id TEXT REFERENCES doc_categories(category_id);
```

## 2. 值域来源：`preset/domains.json`

`presetDomain`（`internal/foundation/preset.go`）新增 `DocCategories []presetDocCategory json:"doc_categories"`，与现有 `Concepts []presetEntry json:"entries"` 平级：

```go
type presetDocCategory struct {
    ID          string `json:"id"`
    Name        string `json:"name"`
    Description string `json:"description"`
}
```

`LoadPresetData` 在既有 entries UPSERT 循环之后，同 domain 下追加一段 doc_categories UPSERT：`INSERT ... ON CONFLICT(category_id) DO UPDATE SET name=excluded.name, description=excluded.description, domain_id=excluded.domain_id, updated_at=CURRENT_TIMESTAMP`——只增/刷新，不删除，同 entries 的既有语义一致（人工在 domains.json 里删掉一条不会级联删库里的行，需要单独走管理界面删除）。

11 个现有预定义领域按各自体裁补齐 `doc_categories`（内容见 `preset/domains.json` 本身，此处不重复罗列）。

## 3. 领域管理：`internal/domain` 包扩展

不新建独立包——doc_categories 是领域的简单子资源，没有 entries 那样的候选/演化流程，直接扩展 `internal/domain`：

- `types.go` 新增 `DocCategory{CategoryID, DomainID, Name, Description, SourceCount, CreatedAt}`。
- `store.go` 新增：
  - `ListDocCategories(domainID string) ([]DocCategory, error)` —— 含每个类目下的 `source_count`（相关子查询，同 `Domain.SourceCount` 的写法）。
  - `CreateDocCategory(domainID, name, description string) (string, error)`
  - `UpdateDocCategory(categoryID, name, description string) error`
  - `DeleteDocCategory(categoryID string) error` —— 删除前若仍有 `sources.doc_category_id` 引用，先置空引用（不级联删文档，只解除分类关系）。
- `service.go` 加对应的薄封装（trim 校验同 `Create` 现有写法）。
- `handler.go` 新增路由：
  - `GET /domains/{id}/doc-categories`
  - `POST /domains/{id}/doc-categories`
  - `PATCH /doc-categories/{id}`
  - `DELETE /doc-categories/{id}`

`domain.NewHandler(domainSvc).RegisterRoutes(apiMux)` 已在 `cmd/server/main.go` 接线，新增路由不需要改 main.go。

## 4. Source 分类：镜像 `matchDomain`

### 4.1 `internal/source/store.go`

- `Source` 结构体新增 `DocCategoryID sql.NullString`。
- `Create`/`GetByID`/`List` 三个全列 SELECT/INSERT 方法同步加上 `doc_category_id` 列与对应 `Scan` 目标（`GetSourcesByIDs`/`GetShadowByTarget` 目前的调用方用不到这个字段，未同步改动，需要时再加）。
- 新增 `UpdateDocCategoryID(sourceID string, categoryID *string) error`，写法镜像 `UpdateDomainID`。
- 新增本包内直查（同 `ListDomains`/`DomainExists` 现有模式，不引入对 `internal/domain` 包的依赖）：
  - `ListDocCategories(domainID string) ([]DocCategory, error)`（本包内的精简 `DocCategory{CategoryID, Name, Description}` 类型，不是 `internal/domain` 的那个）
  - `DocCategoryExists(categoryID string) (bool, error)`
- `SwapShadowIntoTarget`：换血时把影子行的 `doc_category_id` 与 `domain_id`/`outline_type`/`summary`/`word_count` 一起读出、写回目标行（第 604 行、645 行两处 SQL 同步加列）。

### 4.2 `internal/source/service.go`

- 新增 `matchDocCategory(ctx context.Context, sourceID string)`，紧跟在主流程 `s.matchDomain(ctx, sourceID)`（约第 409 行）之后同步调用，复用同一个「Step 8: 领域匹配」进度事件，不新增进度步骤：
  1. 重新 `GetByID` 拿到刚写入的 `domain_id`；`domain_id` 为空则直接返回（没有领域就没有值域可言）。
  2. `ListDocCategories(domainID)`；空列表直接返回，不发起 LLM 调用。
  3. 调 `source_doc_category_match.md`，输入 `title`/`summary`/`category_list`（`[id] name：description` 逐行拼接，同 `matchDomain` 的 `domainList` 拼接方式）。
  4. 解析 `{"category_id": "..."}`；非空且 `DocCategoryExists` 校验通过才 `UpdateDocCategoryID`；LLM 调用失败或解析失败只 `slog.Warn`，不影响主流程（同 `matchDomain` 的失败处理）。
- Shadow 重跑路径（约第 1603 行第二处 `s.matchDomain(ctx, sourceID)` 调用点）同样紧跟一次 `s.matchDocCategory(ctx, sourceID)`。
- `SetDomain`：`domainID != oldDomainID` 分支内，额外清空 `doc_category_id`（`UpdateDocCategoryID(sourceID, nil)`），并在新 `domainID` 非空时异步触发一次 `go s.matchDocCategory(context.Background(), sourceID)`——同该方法已有的 `go s.conceptMatcher.MatchEntries(...)` 并列执行，不互相依赖。
- 新增 `SetDocCategory(sourceID, categoryID string) error`，镜像 `SetDomain`：校验 `categoryID` 存在（且属于该 source 当前的 `domain_id`，防止跨领域误挂）后直接 `UpdateDocCategoryID`，空字符串表示清空。

### 4.3 `internal/source/handler.go`

- `GET /sources` 列表 item 新增 `doc_category_id`/`doc_category_name`（同 `domain_id`/`domain_name` 的可选指针字段写法，`domainMap` 旁再建一个 `categoryMap`；`categoryMap` 只需覆盖当前结果集实际出现过的 `domain_id` 对应的类目，用 `ListDocCategories` 按需拉取即可，源列表页量级小，不必担心 N+1）。
- `GET /sources/:id` 详情 response 新增 `doc_category_id`（`src.DocCategoryID.Valid` 时才写入，同其余可空字段的既有写法）。
- 新增 `PATCH /sources/{id}/doc-category`，镜像 `setSourceDomain`/`SetDomain` 的错误处理分支（source not found → 404；unknown/mismatched category → 400）。

### 4.4 新增 prompt：`config/prompts/source_doc_category_match.md`

结构镜像 `source_domain_match.md`：

```markdown
---
version: v1
---

## System

你是文档分类助手。根据文档标题和摘要，从提供的文档类别列表中选择最匹配的一个类别。若没有合适的类别，返回 null。

输出 json 格式数据，不输出任何其他内容：
{"category_id": "类别ID或null"}

## User

文档标题：{{title}}
文档摘要：{{summary}}

可用文档类别：
{{category_list}}

## Schema

​```json
{
  "type": "object",
  "required": ["category_id"],
  "properties": {
    "category_id": { "type": ["string", "null"] }
  }
}
​```
```

## 5. 前端：`web/index.html`

- **知识领域页**（domain 详情区域）：新增"文档分类"管理面板，列出该领域已有类目（名称+描述+文档数），支持新增/编辑/删除，UI 结构参考现有领域描述编辑/entries 列表的既有组件写法。
- **文件列表**：域下拉（`sourceDomainSelectHtml`）旁新增文档分类下拉，同款可点击下拉框，值域取自该来源当前 `domain_id` 对应的类目列表（未分类或该领域没有维护值域时不渲染，同 `domain_id` 为空的既有处理），调用 `PATCH /sources/:id/doc-category`——域本身在现有实现里也只在列表卡片展示，详情弹窗未展示 `domain_id`，故本次同样不在详情弹窗新增。

## 6. 明确不做（本次范围之外）

- 不改动 `internal/retrieval` 任何匹配/过滤/排序逻辑——`doc_category` 目前只是可查询、可管理的结构化字段，不参与检索决策。是否、如何接入检索是独立于本次的后续决策（`docs/design/doc-category.md` 3.4）。
- 不做 doc_category 的自动挖掘/候选流程，没有 Study 侧的扫描逻辑。
- `sources.doc_category_id` 单值，不支持多分类。
