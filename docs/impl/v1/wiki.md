# Wiki 编译实现路径（V1，单层架构）

> **2026-08-18 全文重写说明**：本文档原描述"概念页/事实页一阶编译 + 主题页
> 二阶编译"的两层架构，已被 `docs/design/wiki-single-tier-revision.md` 定案
> 的单层架构取代并在代码中实际落地（`docs/impl/v1/wiki-single-tier-task-brief.md`
> 步骤 0-5，执行记录见 `docs/impl/v1/wiki-single-tier-open-questions.md`）。
> 本文档现在描述的是单层架构的实际现状，不再是计划或过渡态。两层架构的历史
> 决策链、为什么改判，见 `docs/design/wiki-single-tier-revision.md`；本次改造
> 之前的两层架构实现细节不再需要，本文档不再保留其描述。

## 职责

Wiki 是长期知识沉淀层：人工从 Concept/Fact 词条（`entries` 表，`kind ∈
{concept, fact}`）中挑选一个或多个 entry_id，触发一次编译，产出一份带证据
回链的页面；页面草稿可人工自由改写（不写回页面本身）；发布后进入独立
Bleve 索引，作为检索的 Wiki 直答层；下游 lifecycle 变化、Study 周期扫描、
ActivationLink 越过服务门槛，都只会把已发布页面标记 `needs_recompile`——
实际重编译永远需要人工调用 `POST /wiki/pages/:id/recompile` 确认。

**Wiki 只有一种页面**（`page_type` 恒为 `topic`，沿用这个既有常量名，不再有
`page_type=concept/fact` 这两种取值），一次编译请求直接产出一份成品页面，
不存在"页面聚合页面"这回事。触发方式是人工指定 entry_id 集合，**不存在任何
自动候选识别入口**——没有 Study 主题聚类、没有 qualifying KP 自动标记
`needs_recompile`、没有"知识领域页 + 新增词条"以外的其他自动触发。

编译材料不再是把某个词条下的全部 KP 整体塞给 LLM，而是围绕 entry_id 集合做
结构化的 **Core / Context / Conflict 子图**展开（见「编译材料：Core/Context/
Conflict 子图」）；编译内部仍是两次 LLM 调用（analyze 产出论断结构供人工
确认，compile 据此生成正文），这条两阶段结构本身不受本次改造影响，只是
喂给它的材料从"切面聚类分组"换成了"Core/Context/Conflict 分组"——编译链路
内部的信号归集、支持度校验、发布质量门等机制详见
`docs/impl/v1/wiki-generation.md`（同样已按本次改造重写）。

检索侧不再有四元组精确匹配（`matchFourTupleEntry` 已删除），改为一次 LLM
判断问题主要涉及哪个/哪些已发布的 Concept/Fact 词条（`matchEntriesByConcept
Recognition`），命中后走与此前完全相同的下游流程（sufficient 判断、
citation 白名单校验）。

Claim 双产物与防固化要素补齐属 V2（见 `docs/impl/v2/readme.md`）；复杂问题的
拆解与子结论聚合属深想路径 / Working Model，是 V3 能力。「主题页展开成员 +
骨架注入慢路径」这套机制随单层化整体删除（不是推迟，是没有对应物了——见
`docs/impl/v1/wiki-single-tier-open-questions.md`「步骤 5 落地记录」），
`docs/impl/v1/trace.md`/`docs/impl/v1/study.md`/`docs/impl/v1/retrieval.md`
里原描述这套机制的章节已同步删除。

**熟路（ActivationBundle）指针**：设计依据 `docs/design/activation-bundle.md`，
仍是尚未排入实现顺序的设计方向。本次单层化改造没有改变熟路与 Wiki 的关系——
Wiki 材料准入仍只吃单条 KP 的 lifecycle 状态与 KPN 信号，不吃熟路信号；
差异在于旧的"广度/连贯/稳定/内聚"五项 ready 判据本身已经随 Study 自动候选
识别被删除（不再有需要这五项判据的产物），下方「材料准入」一节只保留
Core 展开时的 lifecycle 过滤这一道判据，不再有词条级/主题级的整体 ready 判定。

## 数据结构

```sql
CREATE TABLE wiki_pages (
    page_id          TEXT PRIMARY KEY,
    page_type        TEXT NOT NULL,
    -- 恒为 'topic'（沿用既有常量 PageTypeTopic）。concept/fact 两种取值
    -- 随单层化废弃：page_type 不再由 entries.kind 决定，一次编译无论
    -- 输入几个 entry_id、kind 是否混合，产物都是同一种页面。
    entry_id         TEXT REFERENCES entries(entry_id),
    -- 编译请求 entry_ids 的第一个，作为该页面的"主 entry"，供 catalog
    -- 按 domain 分组的既有 JOIN 使用。完整 entry_id 集合见 wiki_page_entries。
    title            TEXT NOT NULL,
    content          TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'draft',
    -- draft / published / needs_recompile / archived
    source_point_ids TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：citation 白名单 = Core ∪ Context ∪ Conflict 子图覆盖的
    -- 全部 point_id 中，正文实际引用到的部分
    source_unit_ids  TEXT NOT NULL DEFAULT '[]',
    source_link_ids  TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：source_point_ids 上已存在的 verified ActivationLink id
    aliases          TEXT NOT NULL DEFAULT '[]',
    trigger_questions TEXT NOT NULL DEFAULT '[]',
    observed_conditions TEXT NOT NULL DEFAULT '[]',
    -- source_point_ids 上各 verified ActivationLink 的 observed_conditions
    -- 并集，只读消费，供检索侧四元组匹配之外的用途参考（四元组直答入口本身
    -- 已随本次改造删除，见「检索接入」）
    compiled_from    TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：触发编译的 result_id，或人工直接指定时的哨兵值
    -- ["manual_trigger"]（ManualTriggerSentinel）
    summary          TEXT NOT NULL DEFAULT '',
    aspects          TEXT NOT NULL DEFAULT '[]',
    -- 切面聚类结构化落库字段，随「材料按切面分组」被 Core/Context/Conflict
    -- 取代后恒写 '[]'，字段保留未删（是否删列见文末「已知遗留」）
    member_roles     TEXT NOT NULL DEFAULT '[]',
    uncovered_points TEXT NOT NULL DEFAULT '[]',
    -- 该次编译 entry_id 集合下 lifecycle=current 但当次未被纳入 Core 的
    -- KP 清单 [{ point_id, summary }]，只作字段展示、不进正文/citation 白名单
    prompt_version   TEXT NOT NULL,
    model_name       TEXT NOT NULL,
    synthesis_success_count            INTEGER NOT NULL DEFAULT 0,
    synthesis_failure_count            INTEGER NOT NULL DEFAULT 0,
    synthesis_audited_success_count    INTEGER NOT NULL DEFAULT 0,
    synthesis_audited_failure_count    INTEGER NOT NULL DEFAULT 0,
    -- 综合满意度轴四列，见「综合满意度轴」一节，不受本次改造影响
    compiled_at      DATETIME,
    published_at     DATETIME,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE wiki_page_entries (
    page_id  TEXT NOT NULL REFERENCES wiki_pages(page_id),
    entry_id TEXT NOT NULL REFERENCES entries(entry_id),
    PRIMARY KEY (page_id, entry_id)
);
CREATE INDEX idx_wiki_page_entries_entry ON wiki_page_entries(entry_id);
-- migration 057。wiki_pages.entry_id 是单值列，无法完整承载一次编译传入的
-- 多个 entry_id；本表承载完整集合，Recompile 用它重建 Core/Context/Conflict
-- 子图（不只是主 entry 一个），matchEntriesByConceptRecognition 命中 entry_id
-- 后也用本表反查其全部关联页面。写路径：Store.InsertPageWithEntries（
-- InsertPage 的超集，二者共用同一实现，entryIDs 传 nil 即原 InsertPage 行为）
-- 在同一事务内写 wiki_pages 主表与本表全量集合。GetActivePageByEntryID 判重
-- 优先 JOIN 本表查询，查不到再退回 wiki_pages.entry_id 单列兼容旧数据。

CREATE TABLE wiki_revisions (
    revision_id  TEXT PRIMARY KEY,
    page_id      TEXT NOT NULL REFERENCES wiki_pages(page_id),
    content      TEXT NOT NULL,
    reason       TEXT NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE wiki_page_relations (
    relation_id   TEXT PRIMARY KEY,
    from_page_id  TEXT NOT NULL REFERENCES wiki_pages(page_id),
    to_page_id    TEXT NOT NULL REFERENCES wiki_pages(page_id),
    relation_type TEXT NOT NULL,
    -- 只有 related / contradicts 两种，无向，from/to 按 page_id 字典序归一化
    -- 只存一行，由程序从 KPN 关系 + 共享 point_id 派生，不调 LLM。
    -- contains 已随二阶编译一起删除——不再有"页面聚合页面"这回事，本表
    -- 现在只承载已发布页面两两之间的 related/contradicts。
    derived_from  TEXT NOT NULL DEFAULT 'kpn',
    evidence      TEXT NOT NULL DEFAULT '{}',
    -- JSON：{"shared_point_ids":[...],"kpn_relation_count":N}
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_wpr_uniq ON wiki_page_relations(from_page_id, to_page_id, relation_type);

CREATE TABLE wiki_drafts (
    draft_id           TEXT PRIMARY KEY,
    page_id            TEXT NOT NULL REFERENCES wiki_pages(page_id),
    source_revision_id TEXT NOT NULL REFERENCES wiki_revisions(revision_id),
    source_page_ids    TEXT NOT NULL DEFAULT '[]',
    -- 单层化后恒为 [page_id]——组装模式（原"主题页 + 成员概念页合并"）随
    -- contains 一起废弃，草稿只剩 page 模式一种
    evidence_index     TEXT NOT NULL DEFAULT '[]',
    title              TEXT NOT NULL,
    content            TEXT NOT NULL,
    note               TEXT NOT NULL DEFAULT '',
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

`sources` 表的 `origin`/`origin_page_id` 两列（草稿回流防自指，见「写作草稿」）
不受本次改造影响。

Bleve `wiki` 索引写入字段不变：`page_id`、`title`、`content`、`aliases`、
`trigger_questions`、`entry_id`、`page_type`（恒 `topic`）、`status`；只索引
`status=published` 的页面，发布时写入，`archived`/`needs_recompile` 时删除。

## 编译材料：Core / Context / Conflict 子图

`buildKnowledgeSubgraph(entryIDs)`（`internal/wiki/subgraph.go`）取代原来的
"整块塞给 LLM 的 qualifying KP 列表"与"切面聚类分组"，对人工指定的 entry_id
集合展开三组材料：

```text
Core：每个 entry_id 直属的 KP（lifecycle=current，不再要求 verified——
  人工指定 entry_id 这个动作本身就是准入信号，见「材料准入」）。
  entry.kind=fact 且 parent_entry_id 非空时，父 Concept 的直属 KP 一并
  纳入 Core，标注来源为「背景」（SubgraphRole=core_parent_background，
  区别于本词条自己的 SubgraphRole=core）——一个 KP 若同时是某 entry 自己的
  Core、又是另一 entry 的父背景，取「本页核心」，不降级为背景。
  Core 不设大小上限：父 Concept 下的 KP 数量本身受归属判断产出规模约束，
  不是无界图遍历问题。

Context：Core 中每个 KP 的一跳 related（KPN scope 不限 cross/内部），
  只展开一跳，不递归——Context/Conflict 中的点不再被二次展开。

Conflict：Core 中每个 KP 的一跳 contradicts，同样只展开一跳。

Subgraph = Core ∪ Context ∪ Conflict（去重，一个 point_id 只属于其中一组，
  优先级 Core > Context/Conflict）。
```

一跳查询批量执行（`Store.RelationsFromPoints(pointIDs, relationType)` 一次
IN 查询覆盖全部 Core point_id，不对每个 KP 单独查询），返回后再补一次
`Store.PointsByIDs` 取端点内容。

`gatherSubgraphInputs` 在此基础上渲染 analyze/compile prompt 的三个材料变量
（core / context / conflict 文本段），citation 白名单 = Subgraph 覆盖的
**全部** point_id（不再是"成员概念页 source_point_ids 并集"这种依赖
`contains` 的算法，也不做预算截断——设计文档「已拍板」第 3 条明确 Core 无
大小上限，一跳展开本身就是唯一的边界控制）。

## 材料准入

单层化后不再有"词条级 ready 判定"（原广度/连贯/稳定/内聚五项，服务于 Study
自动识别候选，该识别机制已随本次改造整体删除）。当前唯一的准入判据落在
Core 展开这一步本身：

```text
Core 展开时读取的是「entry 直属 KP 且 lifecycle=current」——不要求该 KP
存在 verified ActivationLink。这是本次改判的核心变化之一：旧口径下
"Study 推荐路径要求 verified、人工手动路径不要求"的两档判据，随 Study 推荐
路径本身消失而收敛为一档，且这一档不再要求 verified——人工指定 entry_id
本身就是准入信号（冷启动、从未被问过的材料，observed_conditions 恒为空、
永远不会 verified，这正是本次改判要解决的问题，见
docs/design/wiki-single-tier-revision.md「为什么改」）。

uncovered_points（当前实现下，因 Core 已取该 entry 全部 lifecycle=current
直属 KP，正常路径下应恒为空数组；字段保留供后续判据收紧或异常路径使用，
不进正文、不进 citation 白名单、不参与任何门槛判定）。
```

Wiki 材料是否可信、编译产出是否可信，交给页面自己的「触发轴」（`observed_
conditions` 复用 ActivationLink 同一套连续置信度机制，见 `activation.md`）
与「综合满意度轴」（见下）持续验证，不再是编译前的一次性资格审查。

## 触发与 API

```text
POST /wiki/compile/analyze
  请求：{ "entry_ids": ["...", ...], "result_id"?: "..." }
  处理：不改变任何状态，收集 Core/Context/Conflict 子图，调用分析 Prompt
    （config/prompts/wiki_analyze.md），产出 claims/tensions 结构，做
    cited_point_ids ⊆ Subgraph 白名单校验（越界剔除并记录 warn）；
    LLM 调用失败或 claims 为空 → 500，不返回分析产物。
  响应：{ entry_ids, result_id, claims: [{ summary, cited_point_ids }],
          tensions: [{ description, related_point_ids }] }

POST /wiki/compile
  请求：{ "entry_ids": ["...", ...], "result_id"?: "...",
          "claims"?: [...], "tensions"?: [...] }
  处理：
    1. result_id 非空时尝试解析对应 pending wiki_candidate learning_result
       为 applied——该字段与解析逻辑保留在 API/代码里，但当前系统里没有任何
       生产方会写 wiki_candidate（Study 主题聚类已删除，见下方「已知遗留」），
       实际调用路径应恒为 result_id 为空，走 compiled_from=["manual_trigger"]
       这条分支；
    2. entry_ids 中任一 entry 已存在非 archived 页面（GetActivePageByEntryID，
       优先查 wiki_page_entries 再退回单列兼容）→ 409（ErrPageAlreadyExists）；
    3. 请求体带 claims → 直接作为生成输入；未带 → 服务端内部按 analyze
       同样逻辑跑一遍；
    4. 基于 claims 生成正文，同步执行（HTTP 超时放宽到 120s）；
    5. 成功 → page_type=topic、status=draft，写首条 wiki_revisions，
       写 wiki_page_entries 全量集合，触发一次 claim 支持度核验（见
       wiki-generation.md 阶段 E）。
  响应：{ page_id, status: "draft", title }

POST /wiki/pages/:id/recompile
  请求：{ "reason": "..." }
  处理：从 wiki_page_entries 读回该页面完整 entry_id 集合（读不到则退回
    page.entry_id 单值兜底，兼容 migration 057 之前的存量页面），重新走一遍
    analyze→compile（无预览步骤，人工点击"重编译"本身即确认），status 重置
    为 draft，从 wiki index 删除（避免旧结论在重编译期间继续被直答）。

POST /wiki/pages/:id/publish
  请求可选 { "force": bool }
  仅对 draft/needs_recompile 生效；质量门（wiki-generation.md 阶段 G）未过
  时不带 force 返回 409；成功 → status=published，写入 wiki index。

POST /wiki/pages/:id/selfcheck
  单独触发一次质量回放（wiki-generation.md 阶段 G），结果按 (page_id,
  revision_id) 缓存。

POST /wiki/pages/:id/archive
GET  /wiki/pages
GET  /wiki/catalog
GET  /wiki/pages/:id
GET  /wiki/pages/:id/revisions/:rev
GET  /wiki/pages/:id/relations
POST /wiki/pages/:id/drafts
GET  /wiki/drafts
GET  /wiki/drafts/:id
PATCH /wiki/drafts/:id
DELETE /wiki/drafts/:id
```

不再有 `POST /wiki/pages/{id}/topic/compile`（二阶编译入口）——`internal/
wiki/topic.go` 的 `CompileTopic`/`RecompileTopic` 已随二阶编译整体删除，
`internal/wiki/topic.go` 文件本身是否保留/删除以实际代码为准。

## 关系派生（related / contradicts）

`RecomputeRelationsForPage(pageID)`（`internal/wiki/relations.go`）在页面
发布后，对每个已发布页面重新计算它与其余全部已发布页面之间的
`related`/`contradicts`：

```text
relatedCount, contradictsCount, shared := 两页各自 source_point_ids（先按
  lifecycle=current 过滤）之间的 KPN 关系计数与共享 point_id；
isRelated = relatedCount ≥ wiki.relation_kpn_min || len(shared) ≥
  wiki.relation_shared_point_min；
isContradicts = contradictsCount ≥ 1。
两者不互斥，可以同时成立——同一对页面可以同时有 related 和 contradicts
两行，这是页面「待验证点/跨页矛盾」素材的来源之一。
```

单层化后**所有已发布页面都参与派生**——原来"跳过主题页，只对概念页做关系
派生"的 `page.PageType == PageTypeTopic { return nil }` early-return 已删除
（不再有 page_type 区分的必要，全部页面同一种类型）。`RecomputeRelationsFor
Points(pointIDs)` 保留：新增跨 Source KPN 关系时，只对 source_point_ids 命中
受影响 point_id 的已发布页面增量重算，不做全量两两重扫。页面归档时
`clearRelationsForPage` 删除其全部 related/contradicts 行。

`contains` 关系类型（原"主题页 → 成员概念页"）与 `RelationContains` 常量已
随二阶编译整体删除，`wiki_page_relations` 现在只写 related/contradicts 两种
行。

## 重编译标记（needs_recompile）

标记仍然应为自动、执行仍然全部人工（`CLAUDE.md`「Wiki 编译不是全自动的」
不变）。设计文档原定义的自动标记来源有三条（a/b/d，「c. 成员页面变化传导」
随二阶编译一起删除，不再有"成员"这个概念）：

```text
a. lifecycle 传导：SetUnitLifecycle 执行后，受影响 units 所属 entry_id
   若被某已发布页面引用为 Core 成员 → needs_recompile；
b. Study 周期扫描：published 页面依赖的 entry 出现新增 qualifying KP
   （数量比编译时增加 ≥ wiki.recompile_new_kp_min，默认 2）→
   needs_recompile + learning_results(action=recompile_flag)；
d. ActivationLink 越过服务门槛（activation.md「状态机」派生 status 由非
   verified 变为 verified）→ 若其 point_id 被某已发布页面的 source_point_ids
   引用 → needs_recompile（reason=link_verified）。
```

**现状（2026-08-18 单层化收尾重新接线，取代此前"三条来源均无生产者"的
记录）**：a/b/d 三条设计连同旧 `WikiNotifier` 接口在单层化改造中被整体
删除后，本次收尾只重新接回 a，并额外新增一条设计文档原本没有的来源——
**entry_id 归属变化**：一个 KU 被新分类进某个 entry、或从一个 entry 改判到
另一个（`unit_entry_match.md` 直接匹配、`MatchEntries` rematch、人工
`AddEntryPoints`/`RemoveEntryPoint`/`confirmAdd` 归入已有概念）——都会让
该 entry 的 Core KP 集合变化，同样标记 needs_recompile。**b（Study 周期
扫描新增 qualifying KP）与 d（ActivationLink verified）明确不恢复**——
两者都依赖问答置信度积累，与本次改造"编译准入不再依赖置信度"的方向冲突，
用户已拍板不做（见 `wiki-single-tier-open-questions.md`「收尾任务落地
记录」）。

接线方式：`unit.WikiEntryNotifier` 接口（`internal/unit/service.go`，
`NotifyEntriesChanged(entryIDs []string, reason string) error`，由
`wiki.Service` 实现，`cmd/server/main.go` `unitSvc.SetWikiEntryNotifier
(wikiSvc)` 接线）——刻意不是旧 `WikiNotifier` 的"point_id lifecycle 变化"/
"link verified" 两方法形状，避免复用旧接口顺带带回 d。判断只查
`wiki_page_entries` 精确匹配 entry_id（Core 归属这一层），不展开
Context/Conflict 一跳关系，控制每次 lifecycle/entry_id 事件的查询成本；
只标记 `status=published` 的页面，draft/archived 不触发。`internal/entry/
service.go` 已直接持有 `*wiki.Service`（不经接口），新增 `notifyEntryChanged`
辅助方法复用既有的 `GetActivePageByEntryID`/`MarkNeedsRecompile`。触发点
一览：

```text
SetUnitLifecycle（unit/service.go）        → a. lifecycle 传导
matchConceptBatch（unit/service.go）        → entry_id 归属变化（新分类进入）
MatchEntries（unit/service.go）             → entry_id 归属变化（rematch 前后
                                              旧/新两侧都通知）
confirmAdd ConfirmAssign 分支（entry/service.go）→ entry_id 归属变化（人工归入已有概念）
AddEntryPoints / RemoveEntryPoint（entry/service.go）→ entry_id 归属变化（人工手动增删）
```

`FixCoverageGap`（coverage_fix.go）不单独接线——它只插入全新 current 内容，
不改变任何已有 KP 的 lifecycle；对 Wiki 的影响通过其内部调用的
`matchEntries` 链路自然触发，不是遗漏。

## 综合满意度轴（synthesis satisfaction）

不受本次单层化改造影响，机制原样保留：Wiki 直答成功服务后按
`wiki.synthesis_audit_rate` 抽样触发一次独立慢路径核实（复用
`retrieval.md`「独立核实」编排），核实结果写 `wiki_synthesis_audit_success`/
`_failure` 事件并更新 `wiki_pages` 的四个 `synthesis_*` 计数列：

```text
mean(page) = (synthesis_success_count+1) /
             (synthesis_success_count+synthesis_failure_count+2)
```

`mean(page)` 只进页面详情展示与 Study 报告，不驱动任何自动动作——不会自动
触发 `needs_recompile`，不会自动下线页面，不会跳过 selfcheck。

## 检索接入

检索总流程与"直答候选采集"细节见 `retrieval.md`；这里只记 Wiki 侧的三个
候选入口（`gatherDirectAnswerCandidates`，均不看 `page_type`——只有一种）：

```text
1. Concept/Fact 识别入口（新增，取代原四元组精确匹配）：
   matchEntriesByConceptRecognition(question, domainIDs) 调用一次 LLM
   （config/prompts/wiki_entry_recognize.md），输入问题原文与候选 entry
   列表（domainIDs 限定范围，来自 retrieval.QueryContext.DomainIDs；为空
   时不做 domain 过滤，候选是全部 domain 的 entries；候选列表为空时直接
   跳过、不调 LLM），输出该问题主要涉及的 entry_id 数组；命中 entry_id 后
   经 wiki_page_entries 反查其全部关联 page_id，逐个校验 status=published
   才作为候选（未发布/草稿/归档静默丢弃）。

2. 词法入口：对问题分词后查询 wiki index（title/content/aliases/
   trigger_questions 均参与打分），取分数 ≥ retrieval.wiki_min_score 的
   页面按分数降序。

3. 概念入口：问题（去除空白后）字面包含已发布页面的概念名称（词法包含，
   不调 LLM、不分词匹配），命中即入候选。
```

三入口合并去重，优先级：Concept/Fact 识别命中 > 词法命中（按分数降序）>
仅概念名包含命中，截取前 `retrieval.wiki_max_candidates` 个。**不再有
"命中主题页则展开 contains 成员"这一步**——单层化后没有"主题页聚合概念页"
这回事，每个候选本身就是可直接作答的页面。`gatherDirectAnswerCandidates`
的骨架返回值（原用于慢路径骨架注入）已随本次改造整体删除，`TryDirectAnswer`
签名不再传出 skeleton 相关信息，见 `retrieval.md`。

`TryDirectAnswer(ctx, question, domainIDs, minScore, maxCandidates)` 签名
已去掉原来的 `subject, intent, audience, constraint` 四元组参数，改传
`question` 原文 + `domainIDs`——四元组入口本身连同这些参数一起从签名与
`internal/retrieval/service.go` 调用点清除。

候选页面命中后的下游处理（`answerFromPage`：`config/prompts/answer_wiki.md`
判断 sufficient、citation 按该页 `source_point_ids` 白名单过滤）**不变**，
这是本次改造明确保留的部分。

## 写作草稿（不受本次改造影响）

`wiki_drafts` 记录来源 `page_id` + `source_revision_id`，人工在草稿上自由
改写（不做 citation/结构校验）；`wiki_pages.content` 仍然只由编译产生，
不存在任何 draft → page 的写回接口。`source_page_ids` 单层化后恒为
`[page_id]`（原"组装模式"随 `contains` 一起废弃，草稿只剩 page 模式一种，
`evidence_index` 生成逻辑不变）。草稿内容要长期沉淀就走 `POST /sources`
正常导入链路回流，回流打 `sources.origin='wiki_draft'` + `origin_page_id`
防自指（KPN 匹配剔除来源页面已引用过的 KP，这些边不计入关系派生/综合
满意度统计）——这条防自指设计不受本次改造影响。

## 已知遗留（未在本次文档重写中处理，需要用户确认）

```text
1. [2026-08-18 已解决，见「重编译标记」一节] needs_recompile 自动标记：
   a（lifecycle 传导）与新增的 entry_id 归属变化两条已重新接线；b/d 明确
   拍板不恢复（依赖问答置信度积累，与本次改造方向冲突）。

2. [2026-08-18 已解决] config/config.yml 的 wiki 节两层架构/切面聚类 P1
   专属配置项（topic_member_min 等 5 项、aspect_w_intent 等 6 项）已删除，
   逐项 grep 确认无引用。entry_cohesion_min/aspect_w_rel/aspect_w_cooc/
   aspect_cooc_sat/aspect_gamma 予以保留——仍被 main.go 读入
   study.CohesionConfig，但该 struct 的字段在 internal/study 包内已确认
   没有任何读取点，是否借这次机会一并清理这条名存实亡的配置管道，需要
   用户确认，见 wiki-single-tier-open-questions.md「收尾任务落地记录」。

3. [2026-08-18 已解决] config/prompts/wiki_topic_analyze.md、
   wiki_topic_compile.md、wiki_topic_candidate_rerank.md 三个孤儿 prompt
   文件已删除。

4. wiki_pages.aspects / member_roles 两列随「切面聚类」「二阶编译成员分工」
   被取代后恒写默认空值，未新增 migration 删列——沿用项目惯例（不做
   DROP COLUMN，除非用户要求）。

5. [2026-08-18 新增] internal/wiki/aspect.go 删除后，internal/wiki/store.go
   的 CooccurrencePairs/PointIntents/RelationsAmong 三个 Store 方法失去了
   唯一调用方（原 BuildAspectEdges），自身也变成孤儿代码，但未在本次任务
   点名范围内，保留未删，需要用户确认是否一并清理。
```

上述 1-5 已同步记入 `docs/impl/v1/wiki-single-tier-open-questions.md`
「收尾任务落地记录（2026-08-18，第三轮：代码改动）」小节，供后续任务参考。
