# ActivationLink 问法列表与迁移史懒加载（2026-07-22）

## 目标

详情对话框审核时能看到「匹配此链接的问题原文」与「创建依据问法」，状态迁移史中文化；两块均默认可折叠，展开时才请求。

## 非目标

- 不在 `activation_links` 新增存问法字段
- 不改 Match / Study 业务逻辑

## 数据来源

### 匹配命中（matched）

`traces.activation_link_ids` JSON 数组含该 `link_id` 的行 → `traces.question`。  
覆盖：candidate 记信号命中、verified 快路径/回落慢路径但带 ActivationHits。

### 创建依据（created_from）

`activation_links.created_from` 中的 `learning_event.event_id` → JOIN `learning_events.trace_id` → `traces.question`。  
覆盖：共现/gap 创建候选时的支撑问答（当时尚无 link_id）。

## API

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/activation-links/:id` | 详情基础字段；**不再**内嵌 `learning_results` |
| GET | `/activation-links/:id/questions` | `{ matched: [...], created_from: [...] }`，项含 question/trace_id/created_at/path_type/retrieval_quality |
| GET | `/activation-links/:id/learning-results` | 迁移史数组（字段名与现 LearningResult JSON 一致） |

## UI（`web/index.html`）

- 详情在「采纳/失败」下增加两块折叠：`问法列表`、`状态迁移史`（默认折叠）
- 展开才请求对应接口；同一次打开对话框内缓存，不重复请求
- 问法列表内分「匹配命中」「创建依据」两小段
- action / status / 常见 reason 映射中文；未知原文兜底

## 中文映射（展示层）

| 原文 | 中文 |
|------|------|
| create_candidate | 创建候选 |
| promote | 晋升 |
| weaken | 降权 |
| reverify | 重新验证 |
| deprecate | 淘汰 |
| applied | 已应用 |
| pending_confirm | 待确认 |
| rejected | 已驳回 |
| manual_confirm | 人工确认 |
| manual_reject | 人工驳回 |

## 文档

同步 `docs/impl/v1/activation.md` 步骤 3、`docs/impl/v1/page.md` 步骤 2。
