# 编码任务指令：Wiki 单层化改造

## 背景与依据

设计定案：`docs/design/wiki-single-tier-revision.md`（取代 `docs/design/wiki.md` 两层架构相关章节）。核心变化：

```text
触发：Study 自动主题候选识别 + 概念页 qualifying 自动标记 -> 全部去掉，
      改为人工指定一个或多个 Concept/Fact 词条（entry_id 集合）触发编译；
产物：概念页 + 主题页两阶段 -> 单层，一次编译请求直接产出一份页面
      （沿用"主题页"这个既有 page_type 名字/概念，不再有"概念页"）；
材料：qualifying KP 全量塞给 LLM -> Core/Context/Conflict 结构化子图
      （entry 直属 KP + 一跳 related + 一跳 contradicts，Fact 词条额外
      带出父 Concept 的 Core 作为背景，不设大小上限）；
检索：matchFourTupleEntry 四元组精确匹配 -> LLM 做 Concept/Fact 识别，
      命中 entry 后查其已发布页面；命中后的分类/证据充分性验证等
      下游流程不变；
存量数据：已发布的概念页/主题页/wiki_page_relations 全部删除，不迁移。
```

本任务改动横跨 Wiki / Study / Retrieval 三个模块。**严格按下面的步骤顺序执行**，每步后跑一次相关包的 `go test`，不要等全部改完再一次性测试——改动量大，出错后难以定位是哪一步引入的。

## 开工前：先把要删的东西看一遍现状（不要跳过）

以下是上一轮代码调研给出的精确定位，**执行删除前先用 Read 确认这些位置在你开工时仍然有效**（此文档写完到你实际执行之间可能已有其他改动）：

```text
Wiki 编译触发：
  internal/wiki/handler.go:22   POST /wiki/compile/analyze
  internal/wiki/handler.go:23   POST /wiki/compile          -> h.compile
  internal/wiki/handler.go:26   POST /wiki/pages/{id}/recompile -> h.recompile
  internal/wiki/handler.go:46   POST /wiki/pages/{id}/topic/compile -> h.topicCompile
  internal/wiki/handler.go:392  func (h *Handler) compile          （一阶）
  internal/wiki/handler.go:460  func (h *Handler) recompile        （一阶重编译）
  internal/wiki/handler.go:101  func (h *Handler) topicCompile     （二阶）
  internal/wiki/service.go:163  func (s *Service) Compile
  internal/wiki/service.go:268  func (s *Service) Recompile
  internal/wiki/topic.go:1150   func (s *Service) CompileTopic
  internal/wiki/topic.go:1197   func (s *Service) RecompileTopic

四元组直答匹配：
  internal/wiki/service.go:1960-2002  func (s *Service) matchFourTupleEntry
  internal/wiki/service.go:1791       func (s *Service) gatherDirectAnswerCandidates（调用点）
  internal/wiki/service.go:1753       func (s *Service) TryDirectAnswer
  internal/retrieval/service.go:160   s.wikiSvc.TryDirectAnswer(...) 调用点

Study 主题聚类：
  internal/study/service.go:414  func (s *Service) buildWikiCandidates
  internal/study/service.go:658  调用点（Ticker 学习周期内）
  internal/study/service.go:769,773  topicClusterMinQuestions / topicClusterMinDaysActive 使用处
  config/config.yml:154-155      topic_cluster_min_questions / topic_cluster_min_days_active

概念页 qualifying -> needs_recompile 自动标记：
  internal/wiki/service.go:1546  func (s *Service) MarkNeedsRecompile（核心 mutator，保留）
  internal/wiki/service.go:1615  func (s *Service) ScanForNewQualifyingKP（调用 MarkNeedsRecompile，删除整个扫描函数及其调用点）
  internal/wiki/service.go:1648-1680  lifecycle 变化触发的标记扫描（同上，删除扫描逻辑本身，MarkNeedsRecompile 保留供人工重编译场景使用）
  internal/wiki/service.go:1688-1717  ActivationLink verified 触发的标记扫描（同上）
  internal/wiki/service.go:1568-1587  成员页面变化级联标记主题页（随二阶编译一起删除，不再有"成员"概念）

wiki_page_relations.contains：
  写入：internal/wiki/topic.go:916  UpsertPageRelation(..., RelationContains, ...)
  读取：internal/wiki/relations_store.go:228,252,296
        internal/wiki/catalog.go:248
        internal/wiki/handler.go:74-76
  常量：internal/wiki/types.go:249  RelationContains = "contains"

一阶编译材料选取（qualifying KP）：
  internal/wiki/store.go:411    func (s *Store) ListQualifyingPoints
  internal/wiki/service.go:1024 func (s *Service) gatherMaterials（消费点，改造而非删除）
  internal/wiki/service.go:472  selectAspectsWithinBudget
  internal/wiki/service.go:504  renderAspectsText

related/contradicts 派生（保留，但要去掉 page_type 判断）：
  internal/wiki/relations.go:19  func (s *Service) RecomputeRelationsForPage
  internal/wiki/relations.go:23  if ... page.PageType == PageTypeTopic { return nil }
    -- 这行现在的语义是"跳过主题页，只对概念页做关系派生"；单层化后
       所有已发布页面都是同一种类型，这个 early-return 整体删除，
       让全部已发布页面都参与 related/contradicts 派生。

citation 白名单（成员页 source_point_ids 并集，改造为 Subgraph 覆盖集）：
  internal/wiki/topic.go:1032,1051  白名单构建
  internal/wiki/topic.go:1136-1137,1328  filterClaims/filterTensions/filterContentTags 消费点
  internal/wiki/drafts.go:89  buildEvidenceIndex（关联的 union 辅助函数，检查是否可复用）

KPN 一跳查询（复用，不要重写）：
  internal/unit/store.go:1222  func (s *Store) GetRelationsByPointID(pointID, scope string)
    -- 单个 point_id 一次查询，返回 related 与 contradicts 不分类型（调用方按
       relation_type 过滤）。子图展开需要对多个 Core KP 批量调用，或新增一个
       批量版本（IN 查询）——先检查是否已有类似批量函数，没有再新增，不要
       在子图展开逻辑内部写 N+1 循环查询。
```

## 实现顺序

### 步骤 0：存量数据清理

先 `grep -rn "REFERENCES wiki_pages" internal/foundation/db/migrations/` 找出所有外键指向 `wiki_pages.page_id` 的表（预计包括但不限于 `wiki_page_relations`、`wiki_revisions`、`wiki_drafts`、`wiki_observed_conditions`、`wiki_synthesis_satisfaction`、`wiki_sections`、`wiki_quality`、`wiki_wizard_tasks`、`wiki_source_links`、`wiki_trigger_fields`），新增一个 migration，按依赖顺序 `DELETE FROM` 这些表的全部行，最后 `DELETE FROM wiki_pages`。不删表结构（后面新逻辑还要用），只清数据。这是本次改判"存量不迁移、直接删除"的落地点，必须在新编译逻辑上线前执行，避免新旧两种 page_type 混杂。

### 步骤 1：Study 侧——删除四元组主题聚类

删除 `buildWikiCandidates`（`internal/study/service.go:414`）及其在 Ticker 周期内的调用点（`:658`）、`topicClusterMinQuestions`/`topicClusterMinDaysActive` 字段与配置项（`config/config.yml:154-155`）、`WikiCandidate` 类型（`internal/study/types.go:277`，确认调用方清空后再删类型定义）。

同时删除概念页 qualifying → needs_recompile 的三处自动扫描（`internal/wiki/service.go:1615` `ScanForNewQualifyingKP`、`:1648-1680` lifecycle 触发扫描、`:1688-1717` ActivationLink verified 触发扫描）及其在 Study/Ticker 侧的调用点——先在 `internal/study/` 里 grep 这三个函数名找到调用方。`MarkNeedsRecompile` 本身（`:1546`）保留，人工重编译时仍需要它。

Study 报告 JSON 里原本输出 wiki 候选/qualifying 相关的节，同步删除或改为空（按实际报告 schema 检查，不在本清单内，需要 grep `study.go` 报告组装函数确认字段名）。

`go test ./internal/study/... ./internal/wiki/...` 验证。

### 步骤 2：Wiki 侧——删除二阶编译与 contains

删除 `internal/wiki/topic.go` 里 `CompileTopic`（:1150）、`RecompileTopic`（:1197）、topic 相关的成员页面展开/白名单构建（:1032,1051,1136-1137,1328）、`handler.go` 的 `topicCompile`（:101）及其路由（:46）。删除 `contains` 相关读写（写入 `topic.go:916`；读取 `relations_store.go:228,252,296`、`catalog.go:248`、`handler.go:74-76`）与 `RelationContains` 常量的所有引用（常量定义本身按你判断保留或删，取决于是否还有别处引用）。

`internal/wiki/service.go:1568-1587`（成员页面变化级联标记主题页 needs_recompile）随二阶编译一起删除。

`internal/wiki/relations.go:23` 的 `page.PageType == PageTypeTopic { return nil }` 整段 early-return 删除——单层化后所有已发布页面都要参与 related/contradicts 派生，不再有"跳过主题页"这回事。

`go build ./...` 此时会报大量因为删除函数导致的编译错误，是预期的，继续下一步补上新逻辑再统一修。

### 步骤 3：Wiki 侧——单层编译入口与 Core/Context/Conflict 子图

新的编译请求体（替代原 `CompileRequest`，具体字段名以现有 `internal/wiki/service.go:163` 附近的 `CompileRequest` 结构为基础改造，不要另起一个新类型名增加认知负担）：

```go
type CompileRequest struct {
    EntryIDs []string  // 一个或多个 Concept/Fact 词条 entry_id，人工指定
    // 原有的其余字段（domain 归属、compiled_from 等）按需保留
}
```

新增子图展开函数（建议放在 `internal/wiki/` 新文件 `subgraph.go`，不要塞进已经很大的 `service.go`）：

```text
func (s *Service) buildKnowledgeSubgraph(entryIDs []string) (core, context, conflict []KnowledgePoint, err error)

规则：
  1. Core：对每个 entry_id 查其直属 KP（lifecycle=current）——复用
     internal/wiki/store.go:411 ListQualifyingPoints 的查询逻辑，但注意
     该函数目前可能带 requireVerified 参数，单层化后编译材料的准入不
     再依赖 verified（人工指定的 entry 本身就是准入信号），检查调用时
     传参是否需要调整，不要沿用旧的 verified 门槛;
  2. Fact 词条（entries.kind='fact'）额外读取 entries.parent_entry_id
     （fact-entry-parent-concept-task-brief.md 新增字段），非空时把父
     Concept 的直属 KP 也并入 Core，标记来源为"背景"（编译 prompt 需要
     区分"本词条核心" vs "父概念背景"，建议 KnowledgePoint 或包装结构体
     加一个 SubgraphRole 字段：core / core_parent_background）;
  3. Context：Core 中每个 KP 的一跳 related —— 用 internal/unit/store.go:1222
     GetRelationsByPointID(pointID, "related") 批量查询（Core 可能几十个
     KP，评估是否需要新增批量版本而非循环调用，循环调用前先确认 KP 数量
     级别可接受，不要过早优化）;
  4. Conflict：同上，relation_type="contradicts";
  5. 只展开一跳，不递归；Core 不设大小上限（父 Concept 归属规模本身
     有限，见设计文档「已拍板」第 3 条）。
```

**Core/Context/Conflict 与现有切面聚类的关系（已确认，2026-08-18）**：Core/Context/Conflict 是新的顶层分组，**取代**现有切面聚类（`wiki-generation.md` P1 aspect clustering），不是在其内部再嵌套一层。`internal/wiki/service.go:472` `selectAspectsWithinBudget`、`:504` `renderAspectsText` 及其依赖的切面聚类调用链整体删除，`gatherMaterials`（`:1024`）改造为直接消费 `buildKnowledgeSubgraph` 产出的三组材料，不再先做切面聚类再分组。编译 prompt（`config/prompts/` 下现有 wiki 编译相关文件）模板变量从"切面列表"改为三个固定变量（core / context / conflict 材料文本），写作阶段两次 LLM 调用（analyze + compile）的整体结构不变，只是 analyze 阶段的输入结构变了。

Citation 白名单：改为 `Core ∪ Context ∪ Conflict` �covered 的全部 `point_id`（替代原来的"成员概念页 source_point_ids 并集"，逻辑位置同样在 `topic.go` 相关白名单构建处，随单层化一起搬到新的单层编译流程里，不再区分一阶/二阶）。

`page_type` 处理：所有新编译页面统一写 `page_type='topic'`（沿用既有 `PageTypeTopic` 常量），`PageTypeConcept` 常量若已无引用则删除。

### 步骤 4：Retrieval 侧——Concept/Fact 识别替代四元组匹配

新增函数（建议位置 `internal/wiki/service.go`，替代被删除的 `matchFourTupleEntry`）：

```text
func (s *Service) matchEntriesByConceptRecognition(ctx context.Context, question string) ([]string /* entry_ids */, error)

调用一次 LLM（新增 prompt 文件，如 config/prompts/wiki_entry_recognize.md），输入问题原文，
输出该问题主要涉及哪个/哪些已存在的 Concept/Fact entry_id（prompt 需要把
domain 下 entries 列表作为参照，类似现有 unit_entry_match.md 的做法，
可以直接参考其 prompt 结构，不要另起炉灶设计新格式）。
命中 entry_id 后查这些 entry 是否有已发布页面覆盖（page 的 citation
白名单 / 或者一个 entry -> page 的关联索引，检查现有 wiki_pages 是否有
entry_id 关联列可以直接查，没有则需要新增）。
```

`gatherDirectAnswerCandidates`（`service.go:1791`）改为调用这个新函数而不是 `matchFourTupleEntry`。`TryDirectAnswer`（`service.go:1753`）签名里原本传入的 `subject, intent, audience, constraint` 四元组参数是否还需要保留——如果新匹配机制完全不用四元组，这些参数应该整体去掉，改传 `question` 原文；调用方 `internal/retrieval/service.go:160` 同步改造。

命中候选页面后的下游处理（分类、证据充分性验证）**不变**——这是用户明确要求保留的部分，不要在这一步顺手改动 `internal/retrieval/service.go:657` `VerifyEvidenceSufficient` 或其调用逻辑。

`retrieval.md`「LLM 调用预算对照」表新增这一次 Concept/Fact 识别调用，更新预算说明。

### 步骤 5：清理与收尾

```text
grep 全仓库确认没有残留对已删除函数/常量的引用（matchFourTupleEntry、
  buildWikiCandidates、CompileTopic、RecompileTopic、RelationContains、
  selectAspectsWithinBudget、renderAspectsText 及其依赖的切面聚类调用链
  等，如常量确认无引用则一并删除定义）；
internal/wiki/service.go:1568-1587、1615-1717 涉及的旧扫描逻辑对应的
  learning_results/study 报告字段一并检查是否残留；
member_roles/uncovered_points 列（035_wiki_two_tier.sql 引入）：
  单层化后不再写入，是否需要新 migration 删列——按项目惯例（这两年
  的迁移记录里没有先例做过 DROP COLUMN），倾向于保留列不再写，不新增
  删列 migration，除非用户要求；
web/index.html 前端页面：概念页/主题页两种视图、二阶编译触发按钮、
  member_roles 展示等需要同步改造，属于 page.md 范畴，本任务文档不
  展开，改完后端后单独确认前端改动范围。
```

## 完成标准

```text
步骤 0：新 migration 应用后，wiki_pages 及其全部依赖表行数为 0，表结构不变；
步骤 1：go test ./internal/study/... 全绿；buildWikiCandidates 及
  WikiCandidate 类型、topic_cluster_min_* 配置项、needs_recompile 三处
  自动扫描函数均已删除且无残留引用；MarkNeedsRecompile 仍存在且可被
  人工重编译路径调用；
步骤 2：go build 在完成步骤 3/4 之前会失败属预期，不要在这一步要求
  全绿；contains 关系读写、CompileTopic/RecompileTopic、
  relations.go:23 的 page_type 跳过逻辑均已删除；
步骤 3：POST /wiki/compile body 改为 entry_ids 数组，人工指定一个或
  多个 Concept/Fact 词条即可触发编译；buildKnowledgeSubgraph 正确产出
  Core/Context/Conflict 三组（fake LLM + 构造 KPN related/contradicts
  数据验证）；Fact 词条正确带出父 Concept 的 Core；citation 白名单
  覆盖 Subgraph 全部 point_id，超出白名单的引用被过滤；
步骤 4：新 Concept/Fact 识别函数在 fake LLM 下正确匹配已有 entries 并
  找到对应已发布页面；命中后仍进入既有分类/证据充分性验证流程（复用
  现有测试断言这条链路没变）；四元组相关参数已从 TryDirectAnswer 签名
  与调用方清除；
全部：go build ./... 与 go test ./... 全绿；docs/impl/v1/wiki.md、
  docs/impl/v1/study.md、docs/impl/v1/retrieval.md 三份实现文档已同步
  改写对应章节，不再描述已废弃的两层架构/四元组匹配/自动主题聚类。
```
