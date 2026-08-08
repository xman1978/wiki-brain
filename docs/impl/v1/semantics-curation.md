# Rerank 语义 / KP 人工修正（Semantics Curation）实现路径（V1 扩展）

## 职责

给用户提供对知识单元 rerank 语义（`unit_rerank_semantics`）、中心句（`knowledge_units.center`）与知识点（`knowledge_points`，即 KP）的查看与手动修正能力，用确定性的人工修正兜住 LLM 流水线的概率性遗漏，并保证人工修正不被后续自动流程静默覆盖。

## 设计依据（2026-07-17 问答准确率测试诊断 → 后续统一决策）

已实锤的故障链（A5「培训积分能累计到明年吗」、G17「P 序列绩效奖金权重怎么分」两例，debug 日志逐环节确认）：

```text
KU 切分 / unit_semantics_extract.md 语义抽取（LLM，有损）
  → key_facts / center 漏掉问题所需的关键事实
  → rerank_judge.md 只依据这份摘要判断（明文纪律：不补充输入之外的事实）
  → 正确候选被判 irrelevant，在 rerank 阶段出局
  → 结果为空（path=none）或被错误候选顶替
```

已排除的方案及原因：

- **优化抽取 prompt**：只能降低平均遗漏率，摘要有损的本质不变，且可能引入新问题；
- **judge 输入增加 center 字段**（已实现并保留）：center 与 key_facts 来自两次独立抽取、互补盲区，但实测（A5 重放三轮）judge 的排除纪律要求摘要含有**答案内容本身**，仅含"答案所在的规则类别"仍判 irrelevant——center 提高了信息密度，但救不了这类 case；
- **supporting 挖空时 lastResort 整段回退**（已实现并保留）：救的是"judge 选对了候选、挖掘阶段挖空"，救不了"judge 一开始就滤掉正确候选"。

初版方案是给 `unit_rerank_semantics.key_facts` 单独做一套人工修正入口（migration 022/024）。后续复盘发现 key_facts 与 KP（`knowledge_points`，definition/rule/method/case/question）本质上是同一件事——都是从 KU 正文中由 LLM 概率性抽取出的"可检索事实点"，只是走了两条独立管线、各自都会遗漏。于是决定**废弃 key_facts，统一用 KP** 作为 rerank judge 的事实来源与人工补漏的唯一载体：

```text
KU 切分 / unit_point_extract.md 知识点抽取（LLM，有损，产出 KP）
  → rerank_judge.md 读该 KU 的 center + KP 列表判断
  → 某条应有的事实缺失时，直接给该 KU 人工新增一条 KP（而不是编辑一段摘要文本）
  → 新 KP 立即参与检索判断；同时触发该 Source 内的增量 KPN 关系分析，
    保持它在知识图谱里不孤立
```

结论：KU 切分、KP 抽取、语义抽取、LLM rerank 全部由模型操作，无论怎么改进提示词，模型的不确定性都有一定概率丢失证据。对症手段是让用户手动修正——修正是确定性的、持久的、不增加 LLM 调用，且符合本项目"LLM 批量生产、人保留最终修正权"的一贯取向（同 Wiki 人工编译、ActivationLink 人工晋升）。人工修正落在两个独立对象上：**rerank 语义**（source_theme/content_theme/intent/object/scope，KU 级的主题/意图分类）与 **KP**（该 KU 下的具体事实点列表）。

## 数据结构

`unit_rerank_semantics` 表（migration 022+024）不再有 `key_facts_json`（migration 026 DROP COLUMN），只剩五个语义分类字段 + 人工修正标记：

```sql
-- migration 022+024（历史）
source_theme / content_theme / intent / object / scope / prompt_version
manually_edited INTEGER NOT NULL DEFAULT 0
edited_at DATETIME
```

`knowledge_points` 表新增两列（migration 026），与 `unit_rerank_semantics` 同一套人工修正标记：

```sql
ALTER TABLE knowledge_points ADD COLUMN manually_edited INTEGER NOT NULL DEFAULT 0;
ALTER TABLE knowledge_points ADD COLUMN edited_at DATETIME;
```

- `manually_edited = 1` 表示该行内容经人工修正，自动流程不得覆盖（见"防覆盖"节）；
- `edited_at` 记录最近一次人工修正时间，展示与审计用；
- `knowledge_units` 表不动：KU 本体（center、行号、正文等）是提取产物，**只读**——人工修正权分别落在 rerank 语义（五个分类字段）与 KP（事实点列表）上。center 有缺陷时通过给该 KU 补一条 KP 来补偿（judge 对 center 与 KP 同等采信）。

## API

### GET /units/:id/semantics

返回该 KU 的完整视图：KU 本体字段（只读展示用）+ rerank 语义（可编辑部分，五个分类字段，不含事实点）：

```json
{
  "unit": {
    "unit_id": "...",
    "center": "培训积分的统计、清零与公布规则",
    "line_start": 111, "line_end": 119,
    "lifecycle": "current",
    "content": "# 第三章 积分结果公布及应用\n\n第六条积分统计及公布\n..."
  },
  "semantics": {
    "source_theme": "...", "content_theme": "...", "intent": "...",
    "object": "...", "scope": "...",
    "prompt_version": "v13", "manually_edited": false, "edited_at": null
  }
}
```

- `unit.content` 是按 `markdown_path` + 行号切片的 KU 正文原文——编辑语义/新增 KP 时必须能对照原文，否则用户无从判断摘要漏了什么；
- KU 不存在返回 404；存在但无语义行（missing）时 `semantics` 为 null——这类 KU 一旦被召回会导致 rerank 硬失败（retrieval 的完整性检查），是最需要人工补写的对象；
- 该 KU 下的 KP 列表通过 `GET /units/:id/points` 单独获取（既有接口，见下）。

### PUT /units/:id/semantics

```json
{
  "semantics": {
    "source_theme": "...", "content_theme": "...", "intent": "...",
    "object": "...", "scope": "..."
  }
}
```

- 只接受 `semantics`——KU 本体字段（center、行号、正文）一律只读，请求中不出现；
- `semantics` 整体提交（不做字段级 PATCH），五个字段均必填——校验规则与 `unit_semantics_extract.md` 的 Schema 一致，保证人工数据与 LLM 数据形状相同，rerank 读取端无需感知来源；
- 写入后置 `manually_edited = 1`、`edited_at = now`；`prompt_version` 保持原值不变（它记录的是"最近一次 LLM 抽取用的 prompt 版本"这一诊断事实，人工修正不伪造它）；语义行原本缺失（missing）时允许 PUT 直接创建，`prompt_version` 写入当前 `rerank.ExtractPromptVersion`；
- KU 的 lifecycle 非 current 时拒绝（409）——superseded/deprecated 的 KU 不参与检索，修正无意义。

### POST /units/:id/points（新增，取代 key_facts 人工编辑）

```json
// 请求
{ "content": "培训积分不跨年累计，次年自动清零", "point_type": "rule" }

// 响应
{
  "point_id": "...", "unit_id": "...", "content": "...", "point_type": "rule",
  "manually_edited": true, "relations_created": 2
}
```

- `content` 非空、`point_type` ∈ `definition/rule/method/case/question`（与 `unit_point_extract.md` 的枚举一致），否则 400；
- KU 的 lifecycle 非 current 时拒绝（409）；
- 写入后 `manually_edited = 1`、`edited_at = now`，`lifecycle` 沿用 schema 默认值 `current`；
- 同步（HTTP handler 内，非异步队列）触发该 KP 的**增量 KPN 分析**：与该 Source 内其余 current KP 一起重跑 `kpn_extract.md`（复用 `internal/unit/service.go` 既有的 `kpnBatch`，不新增 prompt）——Source 总 KP 数 ≤60 时整批重跑，超过时只对新 KP 所属的顶层 outline 分组重跑，避免每次人工加一条事实就把整个 Source 的 KPN 全部重新分析一遍；已存在的关系靠 `idx_kp_relations_uniq` 的 `INSERT OR IGNORE` 天然去重。`relations_created` 是本次新增的关系数，供前端提示。

### PUT /points/:id（新增，编辑已有 KP）

```json
{ "content": "...", "point_type": "rule" }
```

- 校验规则同 POST；KP 所属 KU 非 current 时 409；
- 写入后 `manually_edited = 1`、`edited_at = now`；
- **不触发**增量 KPN——已有关系不因内容微调失效，避免每次小修改都重新付一次 LLM 调用（关系语义仍然基于原先建立时的判断，若改动大到需要重新评估关系，走"新增 KP + 后续人工清理旧 KP 关系"的路径，本期不做自动失效检测）。

### POST /points/:id/deprecate（撤销手动 KP）

仅允许撤销 `manually_edited=1` 的 KP：将 `lifecycle` 置为 `deprecated`（非硬删），并更新 Bleve lifecycle 字段。已 deprecated 幂等成功。LLM 提取产物仍不可经此接口作废——需要作废时走既有重抽取工具并显式确认覆盖，或等 Reupload 让整个 KU 连同其 KP 一起 superseded。

## 防覆盖

所有会写 `unit_rerank_semantics` / `knowledge_points` 的自动路径，遇到 `manually_edited = 1` 的行必须跳过并记录 `slog.Info`（跳过不是失败）：

```text
1. unit 提取链路（internal/unit/rerank_semantics.go 的批量抽取落库，
   internal/unit/store.go 的 PublishGeneration/InsertStandaloneUnit）：
   正常只服务新建 KU/KP，理论上撞不到人工行；insert 语句仍要带
   WHERE manually_edited = 0 兜底，防御未来的重跑入口；
2. 存量回填工具（prompt_version 升级后的批量重抽取）：
   必须跳过人工行；提供 --force-manual 显式参数才允许覆盖，
   覆盖时把 manually_edited 清零并 slog.Warn 逐条记录。
```

**Reupload 边界（明确不保护）**：Shadow Source 替换后原 KU/KP 整体 superseded、新 KU/KP 是全新行，人工修正随原 KU/KP 一起退场。这是合理语义（原文已变，旧修正未必成立），但 Page 的重新上传入口需提示："该文档下 N 条人工修正的语义/知识点将随重传失效"（N 由 `GET /sources/:id` 响应新增的 `manually_edited_count` 提供，统计 `unit_rerank_semantics` 与 `knowledge_points` 两处人工行之和）。

## Retrieval 侧

rerank judge 候选证据的事实来源从 `unit_rerank_semantics.key_facts` 切换为该 KU 的 `knowledge_points`（`GetPointContentsByUnitIDs`，只取 `lifecycle = 'current'` 的 KP），JSON payload 字段名从 `key_facts` 改为 `points`（每条含 `content` 与 `type`）。读语义/KP 时不区分人工/自动（`manually_edited` 不进 judge 输入），人工修正天然生效。

不需要任何存量数据回填：KP 本就是每个 KU 提取时就有的核心数据（不像 key_facts 是后加的 V1 字段），切换读取源后旧 Source 的数据直接可用。

诊断日志（2026-07-17 已加入，正式保留，均为 debug 级）：

```text
retrieval: rerank judge payload    每批次喂给 judge 的完整候选 JSON
retrieval: rerank judge analysis   judge 对每个候选的 role 与判断理由原文
retrieval: rerank judge role       候选 unit_id ↔ 最终 role 对照
```

这三条日志是"从失败回答反查到该修正哪个 KU"的诊断基础：复现问题 → 从 payload/role 找到被误判 irrelevant 的 KU → 对照 analysis 看 judge 缺了什么信息 → 给该 KU 新增一条缺失的 KP。

## Page

来源详情的 KU 列表每行增加「编辑」按钮，点击打开 KU 页面（GET /units/:id/semantics + GET /units/:id/points），分两个区域：

```text
上半区：KU 本体（全部只读）
  center ｜ 行号（line_start-line_end）｜ lifecycle
  KU 内容（unit.content 原文，等宽/滚动区域展示）——
    编辑语义/新增 KP 时对照原文，判断摘要漏了什么

下半区：分两块
  rerank 语义（可编辑表单）：
    content_theme / intent / object / scope / source_theme   单行文本框
    manually_edited  为 true 时显示系统 `.badge`「人工」（KU 列表行同）
    missing 状态     显示"该 KU 无语义记录，被召回将导致检索报错"警示，
                     表单为空白待填
    [保存] → PUT /units/:id/semantics（只提交 semantics）

  KP 列表（可增、可撤销手动项）：
    每条显示 content / point_type；手动项另标 `.badge`「人工」
    [新增知识点] → 弹出 content + point_type 表单 → POST /units/:id/points
      成功后提示"已新增，关联出 N 条新关系"
    手动项 [撤销] → POST /points/:id/deprecate（lifecycle=deprecated）
    每条 [编辑] → PUT /points/:id（可选）
```

修正的典型入口路径写入界面文案：问答结果不对/为空 → 解释抽屉看证据 → 缺了本该在的文档 → 打开该文档 KU 列表 → 检查对应 KU 的知识点 → 新增缺失的知识点。

## 与 Study 的联动（本期不实现，仅立契约）

Study 报告已聚合 path=none 的问题与负反馈 trace；后续版本可将其组织为"修正候选队列"（问题 → 当时的召回候选与 judge 判定 → 一键跳转 KU 页面新增 KP）。本期不做，但 trace 侧不得丢弃 rerank 判定相关字段，为该队列保留数据基础。

## 实现顺序

```text
1. migration 026（DROP key_facts_json；ADD knowledge_points.manually_edited/edited_at）
   + store 层读写（含防覆盖条件）
2. 抽取管线去掉 key_facts（unit_semantics_extract.md v13、rerank.Semantics、
   internal/unit/rerank_semantics.go）
3. Retrieval 候选证据改读 KP（GetPointContentsByUnitIDs、rerank_judge.md v4）
4. GET/PUT /units/:id/semantics 去掉 key_facts；
   POST /units/:id/points、PUT /points/:id + 校验 + 测试
5. 增量 KPN（AddManualPoint 触发，复用 kpnBatch）
6. GET /sources/:id 增加 manually_edited_count；reupload 提示
7. Page 编辑抽屉（rerank 语义表单 + KP 列表）
8. 验收：修正 A5 的 f56a8a5c（新增 KP"培训积分不跨年累计，次年自动清零"）
   与 G17 的 130df3c6（新增 P/M 序列权重两条 KP），重放两题，
   预期均转为 direct 命中且回答含正确事实
```

## 完成标准

```text
人工修正后立即生效：给某 KU 新增一条 KP 后重放原问题，此前被判 irrelevant 的
  KU 进入 direct/supporting，回答引用正确证据（A5、G17 两个实测案例）；
防覆盖：批量回填对 manually_edited=1 的行（语义/KP 两处）跳过且有日志；
  --force-manual 才可覆盖并清标记；
missing 补写：无语义行的 KU 可通过 PUT 创建，创建后该 KU 被召回不再
  触发完整性报错；
lifecycle 保护：对非 current 的 KU/KP 的写操作均返回 409；
校验：语义五字段缺失 400；KP content 为空 / point_type 不在五种枚举内 400；
增量 KPN：新增 KP 后与同 Source 现有 KP 之间的 related/contradicts 关系
  被写入，重复触发不产生重复关系；
Page：KU 页面（本体只读 + 语义可编辑 + KP 新增/手动撤销）、保存、系统
  `.badge`「人工」标记、missing 警示、reupload 提示可用；
go test ./... 全绿。
```
