# MCP 能力 工程实现

设计依据：`docs/design/mcp.md`。本文档只覆盖 MCP 层新增的部分；`import_file`/`retrieve` 内部实际的文件处理、抽取、检索逻辑完全复用现有 `internal/source`、`internal/retrieval` 包，不改动其一行代码。

> 本模块不在 CLAUDE.md 的 V1 强制实现顺序（lifecycle → activation → trace → study → retrieval → evidence → kpn → wiki → page）之内，是独立的对接能力，可在 V1 主线之外单独排期实现。

## 1. 包结构

```text
internal/mcp/
    server.go          // MCP server 注册与启动
    tools.go            // import_file / retrieve 两个工具的 handler
    citation.go         // SourceRef -> 人类可读引用 的解析逻辑（本模块唯一的新业务代码）
    wiki_citations.go   // Wiki 直答路径的证据 -> 引用 映射（见步骤 3 末尾）
```

`server.go` 持有 `*source.Service`、`*retrieval.Service`、`*source.Store`、`*unit.Store` 四个依赖，均为进程内直接调用，不经过 HTTP 自环。

## 1a. 运行方式

MCP server 不是独立 stdio 子进程，而是以 Streamable HTTP transport（`github.com/modelcontextprotocol/go-sdk/mcp` 的 `NewStreamableHTTPHandler`）挂载在与现有 REST API 同一个长驻 HTTP server 上（`cmd/server/main.go` 里 `apiMux.Handle("/mcp", mcpServer.Handler())`）：

```text
理由：
  同进程、同一份数据库连接池、同一套已构造好的 service 依赖图，MCP 只是多加
  一个 mux 路由；换成独立 stdio 子进程则需要在 cmd/server/main.go 之外重新
  组装一遍完整的 service wiring（source.Service/Store、retrieval.Service、
  unit.Store 等），属于纯粹的重复劳动，且会引入第二套数据库连接管理。
```

`mcp.NewServer(sourceSvc, sourceStore, unitStore, retrievalSvc, cfg)` 在 `main.go` 里与其余 HTTP handler 一起，在服务启动时一次性构造；两个工具（`import_file`/`retrieve`）在 `Handler()` 内通过 `gosdk.AddTool` 注册。客户端（AI Agent 平台）以 MCP Streamable HTTP 协议连接 `http://<host>:<port>/mcp`。

## 2. Tool: `import_file`

### 输入 Schema

```json
{
  "type": "object",
  "properties": {
    "file_path":      { "type": "string", "description": "本地绝对路径，与 content_base64 二选一" },
    "content_base64":  { "type": "string", "description": "文件内容 base64 编码，与 file_path 二选一" },
    "filename":        { "type": "string", "description": "content_base64 模式下必填，用于推断格式与标题" },
    "origin":          { "type": "string", "default": "agent_generated" },
    "origin_page_id":  { "type": "string" }
  }
}
```

### 校验规则

```text
file_path 和 content_base64 必须恰好提供一个：
  都未提供 -> 400 "file_path or content_base64 is required"
  都提供   -> 400 "file_path and content_base64 are mutually exclusive"
content_base64 模式下 filename 为空 -> 400 "filename is required when content_base64 is used"
```

### 处理流程

```text
1. file_path 模式：os.ReadFile(file_path) 读取字节，文件名取 filepath.Base(file_path)；
2. content_base64 模式：base64.StdEncoding.DecodeString 解码字节，文件名取 filename 字段；
3. 两种模式统一后，构造与现有 POST /sources（internal/source/handler.go）multipart 处理
   等价的调用（直接调 source.Service 对应的创建方法，不经 HTTP），origin 默认写
   "agent_generated"（sources.origin 是普通 TEXT 列、无 CHECK 约束，见 migration
   035_wiki_two_tier.sql，新增取值不需要迁移）；
4. 同步等待，超时时间 mcp.import_wait_timeout_seconds（默认 20s），每
   mcp.import_poll_interval_ms（默认 500ms）查一次 sources.status；
5. 等到 status ∈ {completed, failed} 或超时，返回当前状态。
```

### 输出

```json
{
  "source_id":  "string",
  "status":     "pending | processing | completed | failed",
  "title":      "string",
  "format":     "string"
}
```
字段与现有 `POST /sources` 响应一致，直接复用 `internal/source/handler.go:47-87` 的返回结构。

## 3. Tool: `retrieve`

### 输入 Schema

```json
{
  "type": "object",
  "properties": {
    "question":   { "type": "string" },
    "force_full": { "type": "boolean", "default": false }
  },
  "required": ["question"]
}
```
直接透传给 `retrieval.Service.Retrieve`（对应现有 `POST /retrieval`，`internal/retrieval/handler.go:20-52`）。

### 输出 Schema

```json
{
  "question": "string",
  "direct_evidence":     [ { "content": "string", "citation": Citation, "role": "direct" } ],
  "supporting_evidence": [ { "content": "string", "citation": Citation, "role": "supporting" } ],
  "conflicts":           [ { "content": "string", "citation": Citation, "note": "string" } ],
  "gap_reason": "string"
}
```

```text
Citation = {
  "source_title": "string",   // Evidence.SourceTitle，已有字段直接取
  "section":      "string",   // 命中 source_outlines 节点的 Title
  "link":         "string"    // file://{MarkdownPath}#{slug}
}
```

字段裁剪：来自 `EvidenceSet`（`internal/retrieval/types.go:27-90`）的 `DirectEvidence`/`Supporting`/`Conflicts` 三个切片映射到上面三个数组，其余内部字段（`CandidateID`、`MergedRank`、`RecallPaths`、`WikiAnswerContent`、`CompletenessClass` 等校准/排序用字段）不透出。`GapReason` 原样透传，为空表示证据充分。

### 引用解析（citation.go）

```text
输入：Evidence.SourceRef（json.RawMessage，解析为 {source_id, line_start, line_end}）
步骤：
  1. sourceStore.Get(source_id) 取 MarkdownPath；
  2. sourceStore 查该 source 的 outline 树（复用 GET /sources/:id/outlines 背后的
     查询逻辑），从 line_start 落在其 [LineStart, LineEnd] 区间内的节点中取
     [LineEnd-LineStart] 区间宽度最窄的一个，取其 Title——用"区间最窄"代替直接
     比较 outline.Level，因为覆盖同一行的候选节点里，子节点的行区间天然是父节点
     的子集，宽度必然更窄，效果等价于"层级最深"且不需要额外处理 Level 在树中
     不连续、或同一 line_start 被多棵子树覆盖等边界情况；
  3. slug 化标题：小写化、非字母数字替换为 "-"、连续 "-" 折叠、首尾去除 "-"
     （与 GitHub 标题锚点规则一致，保证本地 Markdown 查看器/编辑器能识别）；
  4. 拼接 link = "file://" + MarkdownPath + "#" + slug；
  5. 找不到任何覆盖 line_start 的 outline 节点（极端情况，如无标题的纯正文）时，
     section 留空字符串，link 退化为 "file://" + MarkdownPath（不带锚点）。
```

同一个 `(source_id, line_start)` 在一次 retrieve 调用内可能被多条证据复用，实现时按 `source_id` 缓存一次 outline 树查询，避免每条证据都重新查库。

### 3a. Wiki 直答路径的证据映射（wiki_citations.go）

`retrieve` 的证据来源是 `EvidenceSet`，但当 `EvidenceSet.PathType == retrieval.PathTypeWiki` 时，`DirectEvidence`/`Supporting` 两个切片是空的——Wiki 直答只携带 `CitedPointIDs`（引用到的 KP id 列表）和一段已经合成好的 `WikiAnswerContent`（`internal/retrieval/types.go` EvidenceSet 注释），不是"每条证据自带 SourceRef"的形状。`retrieve` 若按普通路径处理会把 Wiki 命中丢成空证据数组，与「不做答案合成，只返回原始证据」的设计目标（第 1 节）相悖，也不应该把 `WikiAnswerContent` 这个已合成的成品答案透传出去。

因此 `PathTypeWiki` 单独走一条映射（`wikiCitationsFromPoints`），把 `CitedPointIDs` 换回原始证据形式：

```text
1. unitStore.GetPointsByIDs(point_ids) 取每条 KP 的 Content 与其所属 UnitID；
2. unitStore.GetUnitsByIDs(unit_ids) 反查所属 KU 的 SourceID/LineStart；
3. 对每条 KP，用 (SourceID, LineStart) 复用同一个 citationResolver.resolve，
   拼出与普通路径完全相同形状的 Citation；
4. 全部映射为 role="direct" 的 EvidenceItem，写入 RetrieveOutput.DirectEvidence，
   SupportingEvidence/Conflicts 留空（Wiki 直答本身不区分证据强弱、不产出冲突）。
```

这样 Wiki 命中和 ActivationLink/慢路径命中对 Agent 呈现的输出形状完全一致（都是 `{content, citation, role}`），Agent 侧不需要感知知识大脑内部走的是哪一层。

## 4. 配置项（config.yml 新增 mcp 节）

```yaml
mcp:
  import_wait_timeout_seconds: 20
  import_poll_interval_ms:     500
```

## 5. 完成标准

```text
import_file（回归项）：
  file_path 指向已存在的本地文件 -> 正确创建 source，字段与 POST /sources 一致；
  content_base64 + filename -> 正确创建 source，文件名/格式识别与直接上传等价；
  file_path 与 content_base64 同时提供或都不提供 -> 400，错误信息明确；
  content_base64 模式缺 filename -> 400；
  等待超时（构造一个长耗时抽取的 fake 场景）-> 返回 status=processing/pending，
    不阻塞、不报错。

retrieve（回归项）：
  正常问题 -> direct_evidence/supporting_evidence 每条都带 citation，citation.link
    可用本地文件管理器/编辑器打开且能定位到对应章节；
  命中 outline 节点缺失标题的边界情况 -> section 为空、link 退化为不带锚点，不报错；
  证据不足 -> gap_reason 非空，evidence 数组可以为空；
  命中 Wiki 直答路径（PathType=wiki）-> direct_evidence 非空、每条都带 citation，
    不出现 WikiAnswerContent 原文，supporting_evidence/conflicts 为空；
  内部字段（CandidateID/MergedRank/RecallPaths/WikiAnswerContent）确认不出现在
    对外响应 JSON 中。
```
