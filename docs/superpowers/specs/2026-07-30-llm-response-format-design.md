# LLM Provider 响应格式可配置设计（2026-07-30）

## 背景与问题

`CompleteJSON` 路径在构造 chat completions 请求体时，将 `response_format.type` **写死为** `json_object`：

```go
reqBody["response_format"] = map[string]string{"type": "json_object"}
```

部分模型服务商 / 推理网关只接受 `json_schema` 或 `text`，收到 `json_object` 会返回 400，例如：

```text
'response_format.type' must be 'json_schema' or 'text'
```

思考开关已按 `platform` 做字段映射，但 `response_format` 未纳入平台差异，也未暴露给用户配置。同一 `platform` 枚举下不同网关对 type 枚举的要求仍可能不同，因此不能仅靠 platform 内置映射解决。

## 目标

1. 在 **模型服务商（Provider）** 配置上增加「响应格式」参数，可选 `json_object` 或 `json_schema`，**默认 `json_object`**（保持现有行为）。
2. 仅在需要返回 JSON 的请求（`CompleteJSON` / `jsonObject==true`）写入 `response_format: {"type": <配置值>}`。
3. Web「模型设置」服务商表单可编辑并持久化该字段；保存后随现有热更新生效。

## 非目标

- 不实现 OpenAI Structured Outputs 完整形态（不附带 `json_schema.name` / `schema` / `strict` 对象）；本设计只切换 `type` 字符串。
- 不提供 `text` 选项；不按 purpose 分别配置。
- 不按 `platform` 自动推断该字段（用户显式选择）。
- 不改变 prompt 内 `{{json_schema}}` 示例注入，以及客户端侧 JSON 抽取 / Schema 校验 / repair 逻辑。

## 已确认决策

| 项 | 选择 |
|----|------|
| 配置粒度 | Provider 级（与「服务商要求」一致） |
| 可选值 | `json_object` \| `json_schema` |
| 默认 | `json_object` |
| 请求体形态 | 仅 `{"type":"<value>"}`，不附带 schema 对象 |
| 作用范围 | 仅 `jsonObject==true` 的请求；普通 Complete / stream 不写 `response_format` |

---

## 数据模型

### Migration

新增 `internal/foundation/db/migrations/038_llm_response_format.sql`（版本号以仓库当时最大 +1 为准；当前预期为 038）：

```sql
ALTER TABLE llm_providers ADD COLUMN response_format TEXT NOT NULL DEFAULT 'json_object';
```

已有行自动得到默认值 `json_object`。

### 合法值

- `json_object`
- `json_schema`

空字符串、缺省字段、未知值：在 `ValidateProvider` 中归一或拒绝——**缺省 / 空 → 填入 `json_object`；其它非法值 → `ErrInvalidInput`**。

### 结构体

- `llmconfig.Provider` 增加 `ResponseFormat string \`json:"response_format"\``
- `llm.ProviderRuntime` 增加同名字段
- `ProviderToRuntime` / store scan / Insert / Update / bootstrap 导入路径同步读写该列

`config.yml` 启动导入：未配置时写入默认 `json_object`（YAML 无需新增键）。

---

## 请求构建

`marshalChatRequest` 签名增加 `responseFormat string`（或从 `ProviderRuntime` 读取），逻辑：

```text
IF jsonObject:
  type := responseFormat
  IF type empty: type = "json_object"
  reqBody["response_format"] = {"type": type}
```

`OpenAIClient.call` 传入 `c.provider.ResponseFormat`。不按 platform 分支。

单元测试：表驱动覆盖 `json_object` / `json_schema` / 空串回落默认；确认 `jsonObject==false` 时请求体无 `response_format`。

---

## HTTP API

现有 Provider CRUD（`GET/POST/PUT /llm/providers`）的 JSON 增加字段 `response_format`。

- 列表 / 详情返回该字段
- 创建 / 更新接受并校验
- 无新路由；`GET /llm/platforms` 不变（枚举固定两值，可由前端写死或后续可选扩展）

---

## Web UI（`web/index.html`）

在 `renderLlmProviderForm` 中，于「平台」字段附近增加：

- 标签：**响应格式**
- 控件：`<select>`，选项 `json_object`、`json_schema`
- 新建默认选中 `json_object`
- `collectProviderFromForm` / 保存 body 带上 `response_format`

列表摘要行可不展示该字段（可选；非必须）。

---

## 测试

| 层 | 断言 |
|----|------|
| `request_builder` | JSON mode 下 type 等于配置值；非 JSON mode 无该键 |
| `llmconfig` ValidateProvider | 合法值通过；非法值失败；空 → 默认 |
| store（若有） | Insert/Get 往返保留 `response_format` |
| 回归 | 现有 thinking 映射单测不受影响 |

---

## 文件清单

| 操作 | 路径 |
|------|------|
| 新增 | `internal/foundation/db/migrations/038_llm_response_format.sql` |
| 修改 | `internal/foundation/llm/models.go`（`ProviderRuntime`） |
| 修改 | `internal/foundation/llm/request_builder.go` + `*_test.go` |
| 修改 | `internal/foundation/llm/client.go`（传入配置值） |
| 修改 | `internal/llmconfig/store.go`（列读写、校验、`ProviderToRuntime`） |
| 修改 | `internal/llmconfig/service.go`（bootstrap 默认值，若有） |
| 修改 | `web/index.html`（表单与 collect） |

---

## 风险与说明

- 部分严格遵循 OpenAI Structured Outputs 的端点可能要求 `type=json_schema` 时附带完整 schema 对象；本设计不覆盖该场景。若后续仍 400，需另开「完整 json_schema 载荷」需求。
- 选错 type 仍会得到服务商 400；配置正确性由用户根据服务商文档选择。
