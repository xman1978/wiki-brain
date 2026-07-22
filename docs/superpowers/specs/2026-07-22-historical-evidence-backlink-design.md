# 历史证据回链：归档 Markdown + superseded KU

日期：2026-07-22

## 背景

reupload 换血后，历史回答的 `evidence_snapshot` 仍正确保存老 `unit_id`（`lifecycle=superseded`）与当时的 `content`。但回链体验串台：

1. 「查看来源」打开 Source 详情时，KU 列表只拉 `lifecycle=current`，看不到被引用的老 KU。
2. KU 正文不落库，一律用当前 `sources.markdown_path` + `line_start/line_end` 现切；换血后当前文件已是新版，按老行号切出的是新正文（或错位正文）。
3. 换血后的 `ReindexSource` 会把含 superseded 在内的全部 KU 用**新** markdown 再切一遍写进 Bleve，污染旧单元索引正文（检索虽按 `lifecycle=current` 过滤，但追溯/调试受影响）。

库内已核实（例：日常费用报销期限管理规定 v2）：换血前回答的 snapshot `unit_id` 仍为 superseded 行，并非被改写成新 KU id。

## 目标

历史证据回链时：

- 读**归档** markdown（对应该 KU 被 superseded 时的版本）；
- Source 详情 / 回链上下文能**展示被引用的 superseded KU**；
- 换血后不再用新文件重切旧 KU 的 Bleve 正文。

## 非目标

- 不改变 reupload Shadow Source 换血主流程（挂靠、标记 superseded、归档、删影子行）。
- 不物理删除 superseded/deprecated 行。
- 不要求回滚 Source 版本或恢复旧 KU 为 current。
- 不在本变更中重做 deleted→deprecated 后的复杂多版本追溯（见下方「deprecated 简化」）。
- 不改 Answer 写入 `evidence_snapshot` 的既有字段形状（已含 `unit_id` / `content` / `source_ref`）。

## 设计

### 1. 解析 KU 对应的 markdown 路径（后端唯一入口）

新增内部解析函数（建议挂在 Source 或 Unit 服务，供 Handler / 切片复用）：

```text
ResolveMarkdownPathForUnit(unit) → relative markdown path

if unit.lifecycle == "current":
    return sources.markdown_path

if unit.lifecycle == "superseded":
    在 source_versions 中取
      archived_at >= unit.lifecycle_changed_at
    的最早一条，返回其 markdown_path
    若无匹配版本：回退 sources.markdown_path（并 slog.Warn）

if unit.lifecycle == "deprecated":
    // 删除时会刷新 lifecycle_changed_at，无法可靠对齐 reupload 归档时刻
    // V1 简化：回退 sources.markdown_path（与现网「删除不删文件」一致）
    return sources.markdown_path
```

说明：

- reupload 换血顺序为先 `SetUnitLifecycle`（写 `lifecycle_changed_at`）再归档并写 `source_versions`，故 superseded 单元的 `lifecycle_changed_at` ≤ 对应版本的 `archived_at`，用「最早且 `archived_at >= lifecycle_changed_at`」可对齐单次/多次 reupload。
- deprecated 的精细版本对齐留待后续（若需要可另加 `source_version` 列）；本 spec 不引入新 migration，除非实现时发现无 `lifecycle_changed_at` 无法工作（存量 superseded 在 migration 010 后均有该列）。

### 2. API

#### `GET /units/:id`

在现有响应上增加：

| 字段 | 说明 |
|------|------|
| `content` | 按 §1 解析路径后，用 `line_start`/`line_end` 切出的正文 |
| `lifecycle` / `lifecycle_changed_at` | 已有，保持 |

失败时：单元不存在 → 404；markdown 不可读 → `content` 可为空字符串并打 Warn，不因切片失败而 500（与现网尽力展示一致，具体错误码与现 Handler 风格对齐）。

#### `GET /sources/:id/units`

- 已有 `lifecycle` 查询参数；扩展合法值：`current`（默认，保持现行为）/ `superseded` / `deprecated` / `all`。
- 历史证据回链打开详情时使用 `all`（或至少确保 cited `unit_id` 可见）。

#### `GET /sources/:id/markdown`

- 增加可选查询参数：`unit_id`（优先）或 `version`。
- 若带 `unit_id`：按该 KU 的 §1 规则返回对应 markdown 全文（superseded → 归档文件）。
- 若带 `version`：返回该 `source_versions` 行的 markdown。
- 均不传：保持现行为（当前 `sources.markdown_path`）。

回链两条稳定路径都要实现：

- **展开上下文**：`GET /units/:id` 的 `content`
- **查看来源 / 预览**：`GET /sources/:id/markdown?unit_id=`（避免打开当前文件却按老行号解读）

### 3. 前端历史证据回链

| 动作 | 行为 |
|------|------|
| 证据卡正文 | 继续只用 snapshot 的 `content`（已正确，不改） |
| 展开上下文 | 使用 `GET /units/:id` 返回的 `content`；**禁止**再对当前 `/sources/:id/markdown` 按行现切 |
| 查看来源 | 打开详情时带上 cited `unit_id`；KU 列表用 `lifecycle=all`（或保证该 superseded KU 在列表中并显示徽标）；预览优先展示该 KU 对应版本正文（归档） |

不要求改回答气泡内已渲染的 snapshot 摘要。

### 4. 换血后索引

`ReindexSource`（换血后调用）**只重切 `lifecycle=current` 的 KU/KP**：

- 影子挂靠过来的新单元需要把 Bleve 里的 `source_id` 从影子 id 改为目标 id；
- superseded 单元在换血前的 `SetUnitLifecycle` 中已用**旧** markdown 建过索引，换血后不得再用新文件覆盖其 Bleve 正文。

若实现时发现 superseded 文档在 Bleve 中仍带影子 `source_id`：换血前标记的是目标下旧单元（本就挂在 target id 上），不存在影子 id 问题；仅新挂靠单元需要 `ReindexSource`。保持「只 current」即可。

## 测试

- Unit：superseded KU 的 `ResolveMarkdownPath` / `GET /units/:id` 的 `content` 来自归档文件，与换血前原文一致；current KU 仍读当前 path。
- 换血：`ReindexSource` 后，superseded 单元的 Bleve 正文不被新文件同号行污染（可用索引读回或通过「仅 current 被更新」的断言）。
- 前端/集成（能测则测）：历史证据展开上下文不请求当前 markdown 现切；Source 详情在回链场景能见到 superseded KU。

## 风险与边界

- 多次 reupload：依赖 `lifecycle_changed_at` 与 `source_versions.archived_at` 对齐；若时钟异常导致无匹配，回退当前 path 并 Warn。
- `deprecated`：本变更不保证对齐到某次 reupload 归档；删除场景文件按文档保留在原 path。
- 无 `source_versions` 行的存量 superseded（极老数据或归档写入失败）：回退当前 path。
