# LLM Provider 数据库配置与 Web 管理（2026-07-30）

## 目标

- 将 LLM 连接与模型参数从 `config.yml` 的运行时依赖中移除，**持久化在 SQLite**，通过 Web 控制台管理。
- 支持**多套命名 Provider**（不同 `base_url` / 密钥 / 平台类型），并按 **purpose** 分别绑定（`default` / `reasoning` / `extraction` / `classification`）。
- 保存后 **热更新** 内存中的路由 Client，无需重启进程。
- **升级引导**：数据库尚无 Provider 时，从启动加载的 `config.yml` 中 **一次性导入** 原 `llm` 段（之后以数据库为准）。

## 非目标

- 多租户、鉴权、API Key 加密或脱敏（本机信任，API **明文往返**）。
- `WB_LLM_*` 环境变量覆盖（**不再支持**）。
- 按请求/按用户动态选模型（仅全局 purpose 绑定）。
- 非 OpenAI 兼容形态的全新协议（V1 仍以 HTTP `POST …/chat/completions` 为主，平台差异通过请求体字段映射解决）。

## 已确认的产品决策

| 项 | 选择 |
|----|------|
| 绑定粒度 | 每个 purpose 指向一个 provider_id |
| 运行时数据源 | 仅数据库 |
| 环境变量 | 不使用 `WB_LLM_*` |
| 配置生效 | 保存后立即热更新 |
| API Key 暴露 | GET/PUT 明文（本机信任） |
| 空库启动 | 允许；LLM 调用返回明确未配置错误 |
| 平台思考开关 | Provider 级 `platform` 枚举 + 内置映射 |
| 升级迁移 | 空库时从 `config.yml` 的 `llm` 导入一条默认 Provider + 四类 purpose 绑定 |

---

## 数据模型

### 表 `llm_providers`

| 列 | 类型 | 说明 |
|----|------|------|
| `provider_id` | TEXT PK | UUID |
| `name` | TEXT NOT NULL UNIQUE | 展示名，如「DashScope 生产」 |
| `platform` | TEXT NOT NULL | 见下文枚举 |
| `base_url` | TEXT NOT NULL | 兼容模式根 URL，如 `https://…/v1` |
| `api_key` | TEXT NOT NULL DEFAULT '' | Bearer token |
| `timeout_seconds` | INTEGER NOT NULL DEFAULT 120 | |
| `max_retries` | INTEGER NOT NULL DEFAULT 3 | |
| `models` | TEXT NOT NULL | JSON 对象，键为 purpose 或 `default` |
| `created_at` | DATETIME | |
| `updated_at` | DATETIME | |

### `models` JSON 单槽结构（API / Web / DB 一致）

每个键（`default` / `reasoning` / `extraction` / `classification`）对应：

```json
{
  "model": "qwen3-30b-a3b-instruct-2507",
  "temperature": 0,
  "input_max_tokens": 4096,
  "output_max_tokens": 4096,
  "enable_think": false
}
```

- `input_max_tokens`：程序内分批、预算（与现 `ModelConfig.MaxInputTokens` 一致）。
- `output_max_tokens`：写入请求 `max_tokens`。
- `enable_think`：是否开启思考/推理模式（见平台映射）。

校验：`models` 必须包含 `default`；`model` 非空；token 字段 ≥ 0。

### 表 `llm_purpose_bindings`

| 列 | 类型 | 说明 |
|----|------|------|
| `purpose` | TEXT PK | `default` \| `reasoning` \| `extraction` \| `classification` |
| `provider_id` | TEXT NOT NULL REFERENCES llm_providers(provider_id) ON DELETE RESTRICT |

调用 `LLMClient.Complete*(…, purpose)` 时：

1. 查 `purpose` → `provider_id`（缺失 → `llm.ErrNotConfigured`）。
2. 取该 provider 的 `models`，用 `ModelForPurpose(purpose)`（先 `purpose` 键，再 `default`）。
3. 用该 provider 的 `platform` + `base_url` + `api_key` 发 HTTP 请求。

### Migration

文件：`internal/foundation/db/migrations/036_llm_providers.sql`（版本号以仓库当前最大 +1 为准）。

---

## Provider `platform` 枚举与 `enable_think` 映射

Web 创建/编辑 Provider 时 **必选** `platform`。`enable_think == false` 时，对应平台的思考相关字段 **不写入** 请求体（omit empty），避免网关误判。

| `platform` | 说明 | `enable_think: true` | `enable_think: false` |
|------------|------|----------------------|------------------------|
| `dashscope` | 阿里云百炼兼容模式 | `enable_thinking: true` | 省略 `enable_thinking` |
| `doubao` | 火山方舟 OpenAI 兼容 | `thinking: {"type":"enabled"}` 或文档等价字段（实现以火山兼容模式为准） | 省略或 `type: disabled`（以实现时官方文档为准，false 侧优先 omit） |
| `zhipu` | 智谱 OpenAI 兼容 | `thinking: {"type":"enabled"}`（GLM-4.5+ 推理类） | omit |
| `kimi` | Moonshot 兼容 | `enable_thinking: true`（若端点支持）否则 omit + 仅靠模型名 | omit |
| `deepseek` | DeepSeek API | `thinking: {"type":"enabled"}` 或 `enable_thinking: true`（以实现时 API 文档为准） | omit |
| `vllm` | 自建 vLLM OpenAI 服务 | 常见：`chat_template_kwargs.enable_thinking` 或顶层 `enable_thinking`（部署配置为准，V1 采用与 DashScope 相同的顶层 `enable_thinking` 优先，不支持则文档注明需改 platform） | omit |
| `ollama` | Ollama OpenAI 兼容 | `think: true`（Ollama 扩展字段，放在请求体根或与 messages 同级，以实现时 Ollama OpenAI 兼容层为准） | omit `think` |
| `openai_compatible` | 通用 | 顶层 `enable_thinking: true` | omit |

实现位置：`internal/foundation/llm/request_builder.go`（或按 platform 小函数），由 `OpenAIClient` 在 marshal 前合并平台字段。流式与非流式共用同一构建逻辑。

响应侧：保持现有行为（`reasoning_content` 流式、`stripThinkTags` 非流式）。

---

## 配置加载与 YAML 关系

### `config.Config`

- **删除** 结构体字段 `LLM` 及 YAML 中的常规依赖；`config.yml` 示例移除 `llm:` 块（新装靠 Web 配置）。
- **保留可选解析**：`Load()` 仍可解析临时键 `llm` 到内部 `BootstrapLLM *LLMConfig`（不暴露给业务模块），**仅用于**启动导入。

### 启动导入（升级路径）

在 `db.Open` + migration 之后、`RoutingClient` 首次加载之前：

```text
IF COUNT(llm_providers) = 0 AND BootstrapLLM != nil:
  推断 platform（见下）
  INSERT 一条 provider name = "从 config.yml 导入"
  INSERT 四条 purpose_bindings → 该 provider_id
  models JSON 由 BootstrapLLM.Models 转换（thinking → enable_think，max_* 字段改名）
  记录 info 日志，提示可从 config.yml 删除 llm 段
```

`platform` 推断（导入时，可覆盖为 `openai_compatible`）：

- `base_url` 含 `dashscope` → `dashscope`
- 含 `volces` / `doubao` → `doubao`
- 含 `bigmodel` / `zhipu` → `zhipu`
- 含 `moonshot` → `kimi`
- 含 `deepseek` → `deepseek`
- 含 `11434` 或 host `ollama` → `ollama`
- 否则 → `openai_compatible`

导入失败（如缺 `default` model）不阻塞启动，打 error 日志，等同未配置。

### 环境变量

从 `applyEnvOverrides` 移除全部 `WB_LLM_*`。

---

## 运行时架构

```text
Services → llm.RoutingClient (implements LLMClient)
             ├─ bindings: purpose → provider_id   (RWMutex)
             └─ clients:  provider_id → *OpenAIClient (per-provider LLMConfig + platform)
```

- `OpenAIClient` 增加 `platform` 字段，或持有 `RequestBuilder`。
- `LLMConfig` / `ModelConfig` 迁至 `internal/foundation/llm` 或 `internal/llmconfig` 包，避免 `config` 包循环依赖；`ModelConfig` 字段：`Temperature`, `InputMaxTokens`, `OutputMaxTokens`, `EnableThink`, `Model`。
- `ResolvedAPIKey()`：直接返回 DB 中的 `api_key`（不再读 env 名）。

热更新：`llmconfig.Service` 在 CREATE/UPDATE/DELETE provider 或 PUT bindings 后调用 `RoutingClient.Reload(ctx)`，内部写锁下重建 client map。

未配置：`llm.ErrNotConfigured`，HTTP 上游可映射为 503 或业务错误（与 foundation 错误类型一致）。

---

## HTTP API

前缀与现 API 相同（无额外鉴权）。

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/llm/providers` | 列表，含完整 `api_key` |
| POST | `/llm/providers` | 创建；body 含 `platform`, `models`, … |
| GET | `/llm/providers/{id}` | 详情 |
| PUT | `/llm/providers/{id}` | 全量更新 |
| DELETE | `/llm/providers/{id}` | 若仍被 binding 引用 → 409 |
| GET | `/llm/bindings` | `{ "default": "uuid", … }` 或带 provider 摘要 |
| PUT | `/llm/bindings` | body: 四个 purpose → `provider_id`，全部须存在 |
| POST | `/llm/providers/{id}/test` | 用 `default` 槽位发最小 completion（可选 purpose 查询参数） |

GET `/llm/platforms`（可选）：返回平台枚举与中文标签，供 Web 下拉。

模块：`internal/llmconfig`（`store`, `service`, `handler`）；`cmd/server/main.go` 注册路由。

---

## Web UI（`web/index.html`）

- 侧栏新增 **「模型设置」** 全屏视图（与 Wiki / 学习报告一致）。
- **Provider 列表**：新建/编辑/删除；字段：名称、`platform` 下拉、`base_url`、`api_key`、timeout、retries；四个 purpose 折叠面板，每面板五项模型参数。
- **Purpose 绑定**：四个下拉，选项为已保存 provider 名称；保存调用 `PUT /llm/bindings`。
- 单 Provider 保存 → `PUT/POST` + 提示已生效；**测试连接** → `POST …/test`。
- 未配置时问答区错误文案友好提示「请先在模型设置中配置 Provider」。

文档同步：`docs/impl/mvp/foundation.md` 步骤 1（配置加载）与 `docs/impl/mvp/page.md` 增加设置页说明。

---

## 测试

- `llmconfig` store/service：CRUD、绑定约束、Reload 被调用。
- `request_builder`：各 platform 在 `enable_think` true/false 下 JSON 快照测试（表驱动）。
- `RoutingClient`：无 binding 时 `ErrNotConfigured`；有 binding 时委托 Fake/httptest。
- 启动导入：临时 yaml + 空表 → 一条 provider + 四条 binding。
- 现有单测：构造 `LLMConfig` 处改为 seed DB 或 `FakeClient`；`config_test` 去掉 llm 段断言。

---

## 文件清单（实现参考）

| 操作 | 路径 |
|------|------|
| 新增 | `internal/foundation/db/migrations/036_llm_providers.sql` |
| 新增 | `internal/llmconfig/store.go`, `service.go`, `handler.go`, `*_test.go` |
| 新增 | `internal/foundation/llm/routing.go`, `request_builder.go` |
| 修改 | `internal/foundation/config/config.go`（移除运行时 LLM；可选 bootstrap 解析） |
| 修改 | `internal/foundation/llm/client.go`（platform 请求体、ModelConfig 字段名） |
| 修改 | `cmd/server/main.go` |
| 修改 | `config/config.yml`（移除 `llm:`；仓库内示例密钥删除） |
| 修改 | `web/index.html` |
| 修改 | 依赖 `cfg.LLM` 的测试与 `cmd/seed` 等 |

---

## 风险与说明

- 各云厂商 API 字段可能随版本变化；`platform` 映射以集成测试 + 文档链接维护，新增平台加枚举值即可。
- `config.yml` 中曾提交的密钥：升级导入后建议用户轮换密钥并从 yaml 删除 `llm` 段。
- vLLM / Ollama 部署差异大，若默认映射不符，用户可选用 `openai_compatible` 并依赖网关已默认行为，后续可扩展 per-provider `extra_json`（非 V1）。
