# Wiki 单层化改造：未决问题

记录执行 `docs/impl/v1/wiki-single-tier-task-brief.md` 步骤 2/3 时遇到的、文档未覆盖、需要用户确认的设计判断。以下三条已于 2026-08-18 拍板，作为步骤 3.5（本文档下方新增）的实现依据；未决期间的临时实现已按下方决议改造，不再是占位。

## 已拍板（2026-08-18）

```text
1. entry_id 多值承载：新建关联表 wiki_page_entries(page_id, entry_id)，
   不用 JSON 列。wiki_pages.entry_id 单值列保留兼容（catalog 现有
   JOIN 不用大改），但新写路径以 wiki_page_entries 为准，Recompile/
   步骤4的 entry_id 反查改读这张表。
2. matchFourTupleEntry / 四元组匹配：保持原样不动，留到后续修改 Wiki
   检索方法（步骤 4）时再一并处理，本次不碰。
3. ScanForNewQualifyingKP / NotifyPointsLifecycleChanged /
   NotifyLinkVerified：删除，含 internal/unit/、internal/activation/
   两个包里对应的 WikiNotifier 接口方法与调用点——自动 needs_recompile
   标记要彻底清除，保证和"没有任何自动识别入口"的设计一致。
```

## 1. `wiki_pages.entry_id` 是单值列，无法完整承载多 entry_id 编译

`CompileRequest.EntryIDs` 允许一次编译传入多个 Concept/Fact `entry_id`，但 `wiki_pages` 表结构未变（任务文档步骤 3 明确"Page 表结构不变"），`Page.EntryID` 仍是单个 `sql.NullString`。

当前实现（`internal/wiki/service.go` `Compile`）把 `req.EntryIDs[0]`（第一个 entry_id）存为 `Page.EntryID`，作为该页面的"主 entry"，用于：

- `GetActivePageByEntryID` 判重（`Compile` 对 `req.EntryIDs` 里的**每一个** entry_id 都查一次，只要任意一个已有活跃页面就拒绝，不只是查第一个）；
- catalog 按 domain 分组的 `JOIN entries c ON c.entry_id = p.entry_id`；
- `Recompile` 用来重建 entry_id 集合。

**由此产生的实际限制**：一个由多个 entry_id 编译出的页面，`Recompile` 时只能拿到主 entry 一个 id，重新构建的 Core/Context/Conflict 子图会比原来编译时窄（丢失了其余 entry_id 的 Core）。这不是本次任务范围内能修的 bug——需要先决定 schema 方案（例如给 `wiki_pages` 加一个 `entry_ids` JSON 列，或者建一张 `wiki_page_entries` 关联表），再排期落地迁移。

步骤 4（Concept/Fact 识别命中已发布页面）大概率也需要"entry_id → page_id"的完整反查索引，同样卡在这个空缺上，建议一并决定。

## 2. `matchFourTupleEntry` / 四元组匹配保留为编译期占位

按任务指示，`internal/wiki/service.go` 的 `matchFourTupleEntry` 与 `gatherDirectAnswerCandidates` 对它的调用**原样保留未动**——这是为了让步骤 2/3 能独立编译通过而做的临时妥协，不是步骤 3 本该做的事。`TryDirectAnswer`/`gatherDirectAnswerCandidates` 的 `subject/intent/audience/constraint` 四元组参数也原样保留（连带 `internal/retrieval/service.go` 调用点未动）。用 Concept/Fact 识别替换它、以及把这些参数从签名里去掉，是步骤 4 的范围，留给后续任务。

`gatherDirectAnswerCandidates` 里原本"命中主题页则展开 contains 成员"的逻辑已经删除（单层化后没有"主题页聚合概念页"这回事），`SkeletonInfo`/`SkeletonMember` 类型和 `TryDirectAnswer` 的返回值形状保留未动（`retrieval` 包还在消费），但 `gatherDirectAnswerCandidates` 现在恒返回 `nil` skeleton——这也是本次改动带来的行为变化，不是步骤 4 才产生的新问题，但下游 `retrieval`/`trace` 对 `SkeletonInfo` 恒为 nil 之后的行为是否需要同步清理，未在本次任务范围内确认。

## 3. `internal/wiki/service.go` 中仍保留但已成孤儿的自动扫描函数

`ScanForNewQualifyingKP`、`NotifyPointsLifecycleChanged`、`NotifyLinkVerified` 三个函数（原 `docs/impl/v1/wiki.md` 步骤 5b/lifecycle 触发的自动 `needs_recompile` 标记）**未在本次任务中删除**——任务文档步骤 1 把它们列为"同时删除"的范围，但用户确认本次会话开工前只完成了 Study 侧（`buildWikiCandidates` 等）的删除，wiki 包这三个函数尚未处理，且本次任务明确只做步骤 2/3，故原样保留。

其中 `ScanForNewQualifyingKP` 目前已无调用方（Study 侧调用点已删）是纯孤儿代码；`NotifyPointsLifecycleChanged`/`NotifyLinkVerified` 仍被 `internal/unit/service.go`/`internal/activation/service.go` 通过 `unit.WikiNotifier`/`activation.WikiNotifier` 接口调用，会继续在 lifecycle 变化/link verified 时自动标记页面 `needs_recompile`——这与设计文档"不再有 Study 驱动的自动候选识别...不再有'知识领域页+新增词条'以外的任何自动识别入口"的精神不完全一致，但删除它们涉及改动 `internal/unit/`、`internal/activation/` 两个包的接口注册，超出本次任务"只做步骤2/3"的范围，留给后续任务确认是否要删、怎么删。

## 落地记录（2026-08-18，步骤 3.5）

上面「已拍板」三条决议已全部实现：

```text
1. wiki_page_entries 关联表：migration 057 新增（page_id, entry_id 复合主键 +
   entry_id 索引）。Store.InsertPageWithEntries（InsertPage 的新超集，
   InsertPage 委托给它、entryIDs 传 nil）在同一事务里写 wiki_pages 主表和
   wiki_page_entries 全量集合。Recompile 改为先调用新增的
   Store.EntryIDsByPageID 读完整 entry_id 集合，读不到（迁移前的旧页面）
   再退回 page.EntryID 单值兜底。GetActivePageByEntryID 判重逻辑确认**不
   等价**——原实现只查 wiki_pages.entry_id 单列，一个 entry_id 若只作为
   某页面的非主 entry（只在 wiki_page_entries 里，不是该页面的
   wiki_pages.entry_id）就查不到，已改为优先 JOIN wiki_page_entries 查，
   查不到再退回单列查询兼容旧数据。新增 Store.PageIDsByEntryID 反查方法，
   本次未接入任何调用方，留给步骤 4。

2. matchFourTupleEntry：确认未动，维持原样。

3. 三个自动扫描函数已删除：
   - internal/wiki/service.go 的 ScanForNewQualifyingKP（连同其专属类型
     RecompileFlag）、NotifyPointsLifecycleChanged、NotifyLinkVerified
     三个函数体全部删除；GetActivePageByEntryID（不在删除范围内）保留。
   - internal/unit/service.go：WikiNotifier 接口类型（原来只有
     NotifyPointsLifecycleChanged 一个方法）整体删除；Service.wikiNotifier
     字段、SetWikiNotifier 方法、SetUnitLifecycle 与
     coverage_fix.go FixCoverageGap 里对 s.wikiNotifier 的调用点全部删除。
     unit.ActivationNotifier 接口（不同接口，方法同名但语义不同——用于
     KP lifecycle 通知 Activation 自己的 Matcher 缓存/状态重算）未受影响，
     保留。
   - internal/activation/service.go：WikiNotifier 接口类型（原来只有
     NotifyLinkVerified 一个方法）整体删除；Service.wikiNotifier 字段、
     SetWikiNotifier 方法、notifyIfNewlyVerified 辅助函数删除；
     AppendObservedCondition/ReplaceObservedConditions/RecordOutcome/
     RecordAuditOutcome 里对 notifyIfNewlyVerified 的调用点，以及
     deriveAndPersistStatus 里 newStatus==StatusVerified 时的
     NotifyLinkVerified 调用，全部删除（连带清理了几处只为传递 oldStatus
     而存在、现在不再需要的局部变量）。activation.Service 自己实现的
     unit.ActivationNotifier.NotifyPointsLifecycleChanged（不同接口）未受
     影响，保留。
   - cmd/server/main.go：unitSvc.SetWikiNotifier(wikiSvc)、
     activationSvc.SetWikiNotifier(wikiSvc) 两行接线调用删除。
   - 测试：internal/unit/lifecycle_test.go 删除 fakeWikiNotifier 类型与
     TestSetUnitLifecycle_CascadesAndReindexes 里对它的用法（该测试其余
     断言保留，只是不再验证 wiki 通知）；internal/activation/
     wiki_notifier_test.go 整个文件删除（该文件仅测试已删除行为）；
     internal/wiki/service_test.go 删除
     TestNotifyPointsLifecycleChanged_MarksNeedsRecompile/
     _UnaffectedPageUntouched、TestScanForNewQualifyingKP_
     FlagsAboveThreshold/_BelowThresholdNotFlagged 四个测试。

达成目标确认：lifecycle 变化（unit.SetUnitLifecycle/FixCoverageGap）与
ActivationLink verified（activation deriveAndPersistStatus/
RecordOutcome/RecordAuditOutcome/AppendObservedCondition/
ReplaceObservedConditions 全部写路径）都不再触碰 Wiki 包的任何方法——grep
确认 internal/unit、internal/activation 两个包里已无对 wiki 包的直接或
接口间接引用残留。MarkNeedsRecompile 本身保留在 internal/wiki/service.go，
供 handler 层人工重编译路径调用。

go build ./...、go vet ./...、go test ./...（含本次改动的三个包与全仓库）
全绿，无需要额外记录的设计判断分歧——本次三条决议按文档字面直接落地，
未发现任务文档未覆盖的边界情况。

## 步骤 4 落地记录（2026-08-18）

`matchFourTupleEntry` 已按任务文档步骤 4 删除，替换为
`matchEntriesByConceptRecognition`（`internal/wiki/service.go`）：一次 LLM
调用（新增 `config/prompts/wiki_entry_recognize.md`，结构直接照抄
`unit_entry_match.md` 的 system/user/schema 三段式，entry_list 渲染复用
`unit.renderEntryList` 的 `[entry_id] name：description｜边界：boundary`
格式，新增 `renderEntryCandidateList` 做同样的事而不是导出/复用 unit 包私
有函数），输入问题原文与候选 entry 列表，输出该问题主要涉及的
entry_id 数组。

**domain 范围怎么定**：`internal/wiki/store.go` 新增
`Store.ListEntriesForRecognition(domainIDs []string)`，直接查 `entries`
表（不经过 `internal/unit` 包——`entries` 是共享表，wiki 包已经在
`GetEntryInfo`/`ListPublishedEntryPages` 里直接查询它，不是 unit 包独占
资源）。domainIDs 来自 `retrieval.QueryContext.DomainIDs`（检索链路既有的
Session 合并解析+Domain 预过滤产出），透传链路：
`retrieval.Service.tryWikiAnswer` → `wiki.Service.TryDirectAnswer` →
`gatherDirectAnswerCandidates` → `matchEntriesByConceptRecognition`。
`DomainIDs` 为空（Session 未解析出 domain，或调用方走的是不经过 Session
的纯 `POST /answer`）时不做 domain 过滤，候选是全部 domain 的 entries——
与 `unit.Store.GetEntriesByDomainID("")` 的空 domainID 兜底口径一致，不
是新发明的行为。**未对是否要求 `DomainResolved=true` 才透传 DomainIDs 做
额外判断**——直接用 `qc.DomainIDs` 当前的值（无论是否已 resolved），因为
即使 `DomainResolved=false` 时 `DomainIDs` 也应为空切片，效果与显式判断
`DomainResolved` 相同，没有必要多加一层。

**TryDirectAnswer/gatherDirectAnswerCandidates 签名**：
`subject, intent, audience, constraint` 四个参数已整体移除，替换为
`domainIDs []string`；`internal/retrieval/service.go:160`
`s.wikiSvc.TryDirectAnswer(...)` 调用点同步改为传 `qc.DomainIDs`。
`gatherDirectAnswerCandidates` 因为新增的 LLM 调用，签名从纯同步函数
改为多接收一个 `ctx context.Context` 参数。

**命中 entry_id 后查已发布页面**：接入了步骤 3.5 遗留未接的
`Store.PageIDsByEntryID`（`wiki_page_entries` 反查，覆盖一页多 entry_id
的情况，不止查 `wiki_pages.entry_id` 单列）——对识别命中的每个
entry_id，查其全部关联 page_id，逐个 `GetPage` 校验 `status=published`
才作为候选，未发布/草稿/归档的页面静默丢弃（不是"识别出了词条但没有
可答内容"这种情况的错误，是正常的"这个词条还没编译成页面"）。

**Match 精确度口径的一致性**：`internal/wiki/service.go` CLAUDE.md 记录的
2026-08-12 改判（"Match 恢复为单级、纯程序、免费匹配，不给任何字段模型
辅助"）针对的是 `activation.Matcher`/`BundleMatcher` 的四元组 Match，与
本次新增的 Concept/Fact 识别是不同的匹配问题（"这条观测条件是否等价于
另一条已知条件"vs"这个问题主要在问哪个已存在的词条"），任务文档步骤 4
明确要求这里调 LLM，两者不冲突、不视为对 2026-08-12 改判的违反。

**SkeletonInfo/SkeletonMember 现状确认（任务第 6 点）**：`gatherDirectAnswerCandidates`
返回值第二项恒为 `nil`（单层化后不再有"主题页展开成员"这回事，
`docs/impl/v1/wiki-single-tier-open-questions.md`「已拍板」第 2 条落地
记录已提前说明）。检查了下游消费方：

- `internal/retrieval/service.go` `tryWikiAnswer`：`skeleton != nil` 判断
  恒为 false 分支，`skeletonPageID`/`skeletonMembers` 恒为空值，随后透传进
  `EvidenceSet.SkeletonPageID`/`SkeletonMembers`、`retrieveSlowPathWithSkeleton`
  （`SkeletonInjectionEnabled` 门控的候选注入逻辑）——门控逻辑本身没坏，
  只是永远拿不到非空输入，等价于该功能被静默关闭。
- `internal/trace/service.go` 679 行附近：`t.SkeletonPageID == ""` 时直接
  跳过 `topic_decompose_signal` 相关的回填逻辑（`resolved_member_page_ids`
  等）——同样永远短路，不是新出现的 bug，是这次改动的直接后果。

**未在本次任务中处理，判断不清楚该删还是该留，记录在此**：这些类型/
字段/门控逻辑（`SkeletonInfo`/`SkeletonMember`/
`retrieval.SkeletonMemberInfo`/`EvidenceSet.SkeletonPageID`/
`SkeletonMembers`/`RetrievalConfig.SkeletonInjectionEnabled`/
`trace.Service` 的 `topic_decompose_signal` 回填代码路径）目前处于"代码
存在、编译通过、测试仍绿、但生产路径永远拿不到非空输入"的状态。是否
整体删除涉及：(a) `traces` 表/`learning_events` 里是否已经积累了历史
非空 `skeleton_page_id` 数据，删除消费代码是否需要一并处理存量数据或
只是不再写新数据；(b) 这套"主题页展开骨架注入慢路径"的机制未来是否
可能以别的形态（例如 Core/Context/Conflict 子图本身作为骨架）复活，
删除后再要就要重新设计而不是简单复用；(c) `docs/impl/v1/trace.md`/
`docs/impl/v1/study.md` 里 `topic_decompose_signal`/`skeleton_used_count`
相关章节需要同步改写，超出本次任务允许改动的文档范围（只批准改
`retrieval.md`「LLM 调用预算对照」一处）。这些判断需要用户先确认，
本次任务未做删除，仅记录现状。

**已拍板（2026-08-18）**：整体删除。理由：系统目前还没有正式用户在用，
不存在需要兼容的历史 `skeleton_page_id` 存量数据，(a) 的顾虑不成立；
(b) 的"未来复活"顾虑不构成保留理由——真要做时按新设计重新实现，留着
死代码不会降低重新设计的成本；(c) 涉及的 `trace.md`/`study.md` 章节随
步骤 5 一并处理。删除范围：`SkeletonInfo`/`SkeletonMember`/
`retrieval.SkeletonMemberInfo`/`EvidenceSet.SkeletonPageID`/
`SkeletonMembers`/`RetrievalConfig.SkeletonInjectionEnabled`/
`retrieveSlowPathWithSkeleton` 的骨架注入分支/`trace.Service` 的
`topic_decompose_signal` 回填代码路径，含相关配置项、测试、以及
`traces`/`learning_events` 表里为这套机制专设的列（如果有独立列而非
复用通用 JSON payload 字段，需要新 migration 删列；如果是复用的通用
payload 字段就不用动表结构）。

## 步骤 5 落地记录（2026-08-18）

上面「已拍板」的删除范围已全部执行完毕，`go build ./...`/`go vet ./...`/
`go test ./...` 全绿。

**删除清单**：

- `internal/wiki/service.go`：`SkeletonInfo`/`SkeletonMember` 类型删除；
  `TryDirectAnswer`/`gatherDirectAnswerCandidates` 签名去掉骨架返回值
  （`(*DirectAnswerResult, bool, error)`/`([]string, error)`）。
- `internal/retrieval/types.go`：`EvidenceSet.SkeletonPageID`/
  `SkeletonMembers` 字段、`SkeletonMemberInfo` 类型删除。
- `internal/retrieval/service.go`：`retrieveSlowPathWithSkeleton`/
  `uniqueSkeletonPointIDs`/`buildSkeletonCandidates`/
  `mergeSkeletonCandidates` 四个函数整体删除；`retrieveSlowPathInternal`/
  `filterAndRecall`/`recallFromSources` 的 `skeleton []candidate` 参数
  一并从签名和调用链上摘除（`retrieveSlowPath` 恢复为直调
  `retrieveSlowPathInternal(ctx, qc, progress)`，去掉原来包一层
  `retrieveSlowPathWithSkeleton` 的写法）；`tryWikiAnswer`/
  `RetrieveWithProgress` 相应简化，不再透传骨架三元组。
- `internal/foundation/config/config.go` + `config/config.yml`：
  `RetrievalConfig.SkeletonInjectionEnabled` 字段与
  `skeleton_injection_enabled` 配置行删除。
- `internal/retrieval/skeleton_test.go`：整个文件删除（专测已删除行为）。
- `internal/trace/types.go`：`Trace.SkeletonPageID` 字段删除。
- `internal/trace/service.go`：`generateTopicDecomposeSignal` 函数整体
  删除（连同 `Service.processTrace` 里的调用点）；`SaveTrace` 前对
  `skeletonPageID` 的赋值/透传删除；`"sort"` import 随之变为未使用一并
  删除（`nonNilStrings` 仍被其他事件类型使用，未删）。
- `internal/trace/store.go`：`SaveTrace`/`SaveAuditPlaceholder`/
  `GetTrace` 三处 SQL 的 `skeleton_page_id` 列全部摘除（含参数列表位移）。
- `internal/study/types.go`：`Report.TopicDecompose` 字段、
  `TopicDecomposeEntry` 类型、`QuestionComplexityGroup.SkeletonUsedCount`/
  `CrossMemberRatio`/`OutsideRatio` 三个字段删除。
- `internal/study/service.go`：`buildTopicDecomposeSection` 函数整体
  删除（含 `generateReport` 里的调用点与报告组装行）；
  `buildQuestionComplexitySection` 里读取 `TopicDecomposeSignals`/
  计算 `globalCrossMemberRatio`/`globalOutsideRatio`/`skeletonUsed` 的
  代码全部删除。
- `internal/study/store.go`：`TopicDecomposeSignals` 方法、
  `TopicDecomposeSignalRow` 类型删除；`ComplexityTraces`
  查询与 `ComplexityTraceRow` 去掉 `skeleton_page_id`/`SkeletonPageID`。

**新增 migration**：`internal/foundation/db/migrations/058_drop_skeleton_injection.sql`
——`ALTER TABLE traces DROP COLUMN skeleton_page_id`（这是本次任务中唯一
一处专属列，其余全是复用 `learning_events.payload` 通用 JSON 字段，未动
表结构）。`TestMigrateIdempotent` 已验证迁移链（含新迁移）在空库上顺序
应用成功。

**`topic_decompose_signal` 独立用途核查结论**：读了 `internal/trace/
service.go`（生产方，仅 `generateTopicDecomposeSignal` 一处）与
`internal/study/service.go`/`store.go`（唯一消费方，`buildTopicDecomposeSection`
+ `buildQuestionComplexitySection` 里的 `cross_member_ratio`/
`outside_ratio` 全局聚合）——事件的生产条件（`t.SkeletonPageID != ""`）
本身就绑死骨架机制，消费方也只用于这一件事的报告展示，没有发现独立用途。
按任务指示"只服务于这套机制就整体删"处理，事件类型本身、其 Go 生产/消费
代码、`study.md`/`trace.md` 里描述它的文字全部删除/改写。

**文档同步**：`docs/impl/v1/trace.md`（数据结构段的
`traces.skeleton_page_id` 迁移说明、learning_events 事件类型枚举段的
`topic_decompose_signal` 描述）、`docs/impl/v1/study.md`（步骤 6 报告
提示项、步骤 7 问题复杂度观测量的字段列表与 JSON 示例）、
`docs/impl/v1/retrieval.md`（检索总流程图里第 0 层"命中主题页展开
成员"与"骨架注入"两段）已同步删除/改写为指向本节的说明。

**本次任务范围之外、发现但未处理的既有遗留问题（供后续参考，不是本次
改动引入的）**：`docs/impl/v1/wiki.md`（步骤 8/9 附近）与
`docs/impl/v1/wiki-generation.md`（第 242/581/691 行附近）里仍有大段
描述"主题页命中即展开成员 + 注入 skeleton_point_ids"的两层架构叙述，
以及 `docs/impl/v1/readme.md` 一处 `topic_decompose_signal` 提及——这些
是此前单层化改造（步骤 0-4）未同步完成的文档债，在本次任务开始前就已
经与代码不一致（代码早已是单层化，`gatherDirectAnswerCandidates` 恒不
产出骨架）。本次任务只被授权改 `retrieval.md`「检索总流程」与
`trace.md`/`study.md` 对应章节，`wiki.md`/`wiki-generation.md`/`readme.md`
里更大范围的两层架构描述改写超出本次授权范围，未动，留待后续专门
整理 Wiki 文档时一并处理。

## 文档整理任务落地记录（2026-08-18，第二轮：文档同步）

上一节末尾提到的"留待后续专门整理 Wiki 文档时一并处理"已执行。改写范围：
`docs/impl/v1/wiki.md`（全文重写）、`docs/impl/v1/wiki-generation.md`（收缩
为只保留阶段 E/G 两道质量门，切面聚类/两层架构相关内容删除）、
`docs/impl/v1/study.md`（步骤 6 删除 Wiki 候选/主题页候选两条自动识别链路
描述）、`docs/impl/v1/retrieval.md`（检索总流程第 0 层三入口描述更正为
Concept/Fact 识别 + 词法 + 概念名包含）、`docs/impl/v1/readme.md`（Wiki 模块
简介改写）、`docs/design/wiki.md`（正文改写为单层架构定案内容，不再用
"改判指针"写法）。删除 `docs/impl/v1/two-tier-task-brief.md`（两层架构编码
任务指令，两层架构已整体删除，任务指令随之失去存在意义）与
`docs/impl/v1/wiki-generation-simplify-task-brief.md`（"收缩切面聚类回两次
整页调用"的任务指令，切面聚类本身已被 Core/Context/Conflict 整体取代，
任务指令同样失去存在意义）。`docs/impl/v1/wiki-single-tier-task-brief.md`、
`docs/impl/v1/wiki-single-tier-open-questions.md`（本文档）、
`docs/design/wiki-single-tier-revision.md` 三份改造决策/执行记录按要求保留
未删。

### 新增发现：needs_recompile 自动标记现状核实（需要用户确认）

重写 `wiki.md`「重编译标记」一节时核实发现：设计文档定义的三条自动
`needs_recompile` 标记来源（a. lifecycle 传导、b. Study 周期扫描新增
qualifying KP、d. ActivationLink 越过服务门槛）里，a/d 依赖的
`unit`/`activation` → `wiki` 跨模块通知接线（`WikiNotifier` 接口、
`NotifyPointsLifecycleChanged`/`NotifyLinkVerified`）已在本次单层化改造
的步骤 1（Study 侧删除四元组主题聚类）中随三个自动扫描函数一起被删除
（见本文档「落地记录（2026-08-18，步骤 3.5）」第 3 条）；b（Study 周期
扫描新增 qualifying KP 数量）目前也没有看到替代实现。也就是说，**当前
代码里 a/b/d 三条自动标记来源实际上都没有生产者在跑**——`MarkNeedsRecompile`
这个 mutator 本身还在（供人工重编译路径使用），但触发它的自动入口都已
不存在。这是否是本次改造的预期结果之一（例如"标记也一并改成人工触发"），
还是遗漏了应该重新接线的部分，需要用户确认；`wiki.md`「已知遗留」与
`study.md` 步骤 6 已如实记录这一现状，未代为决定或补写接线代码（本次任务
范围是纯文档，不改代码）。

### 新增发现：代码/配置层面的死代码与孤儿文件（非文档问题，供后续代码清理参考）

- `internal/wiki/aspect.go` 的 `buildAspects`/`BuildAspectEdges`/
  `ClusterAspects`/`SuggestAspectName` 在当前 `internal/wiki/service.go`
  中已确认无调用方（`buildAspects` 本身也没有调用方）——切面聚类被
  Core/Context/Conflict 取代后成为孤儿代码。Study 侧自己独立实现的内聚度
  计算（`internal/study/service.go` 的 `CohesionConfig`/`Cohesion`）是另一
  套实现，不依赖 `internal/wiki/aspect.go`，不受影响。
- `config/config.yml` 的 `wiki:` 节仍保留一批已无代码引用的配置项：
  两层架构专属（`topic_member_min`、`topic_compile_max_chars`、
  `topic_candidate_kp_max`、`topic_reliability_min`、
  `topic_rerank_batch_max_chars`）与切面聚类专属
  （`aspect_w_intent`、`aspect_w_unit`、`aspect_split_gamma_factor`、
  `aspect_min_size`、`aspect_max_size`、`aspect_questions_max`、
  `entry_cohesion_min` 等，部分仍被 Study 自己的内聚度计算使用，需要
  逐项核实哪些真正孤儿、哪些仍被 Study 侧引用后再清理，不能整节删除）。
- `config/prompts/wiki_topic_analyze.md`、`wiki_topic_compile.md`、
  `wiki_topic_candidate_rerank.md` 三个 prompt 文件已确认无 `.go` 代码
  引用，是二阶编译删除后的孤儿文件。

以上三项均为代码/配置文件层面的清理项，不是文档问题，本次任务（纯文档）
未处理，如实记录供后续代码清理参考。

## 收尾任务落地记录（2026-08-18，第三轮：代码改动）

上面两条「新增发现」在本轮全部落地为代码改动。

### needs_recompile 自动标记：重新接回 a（lifecycle 传导）与新增的
### entry_id 归属变化两条，b/d 明确不恢复

用户拍板：不恢复依赖问答置信度积累的 b（Study 周期扫描新增 qualifying KP）
与 d（ActivationLink 越过服务门槛）——这两条与本次改造"编译准入不再依赖
置信度"的方向冲突。只重新接回：

1. **lifecycle 传导**：`internal/unit/service.go` `SetUnitLifecycle` 执行
   后，收集受影响 units 的 entry_id，调用新接口通知 Wiki。
2. **entry_id 归属变化**（本次新增，不在原 a/b/d 三条设计里）：一个 KU 的
   entry_id 归属发生变化（新分类进某个 entry，或从一个 entry 改判到另一
   个），同样通知 Wiki。触发点：`internal/unit/service.go`
   `matchConceptBatch`（`unit_entry_match.md` 直接匹配写入）、`MatchEntries`
   （rematch 前先快照旧 entry_id 集合，因为 `ClearEntryIDBySourceID` 清空
   后这些 entry 也失去成员）、`internal/entry/service.go` 的
   `confirmAdd` "归入已有概念" `ConfirmAssign` 分支、`AddEntryPoints`、
   `RemoveEntryPoint`。

新接口 `unit.WikiEntryNotifier`（定义在 `internal/unit/service.go`，方法
`NotifyEntriesChanged(entryIDs []string, reason string) error`，由
`wiki.Service` 实现，`cmd/server/main.go` 里 `unitSvc.SetWikiEntryNotifier
(wikiSvc)` 接线）——刻意不是旧 `WikiNotifier` 的两方法形状（"point_id
lifecycle 变化"/"link verified"），避免复用旧接口顺带带回 b/d。判断逻辑
只查 `wiki_page_entries` 精确匹配 entry_id（Core 归属），不展开
Context/Conflict 一跳关系，按用户要求控制成本；只标记 `status=published`
的页面，draft/archived 不触发。`internal/entry/service.go` 已经直接持有
`*wiki.Service`（不经接口），新增 `notifyEntryChanged` 辅助方法复用
`wiki.Service.GetActivePageByEntryID` + `MarkNeedsRecompile`，同时补了
`page.Status != wiki.StatusPublished` 判断——这一点与既有的
`flagMergedEntryPages`（合并路径）不同：合并路径对非 archived 页面（含
draft）都标记，本次改动的三处新增调用点则显式只标记已发布页面，两者刻意
不统一，`notifyEntryChanged` 注释里记了这个差异，供以后决定是否需要
统一（本次任务未要求改动既有合并路径行为）。

`FixCoverageGap`（coverage_fix.go）本身不改变任何已有 KP 的 lifecycle，
只插入一条全新 current 内容，因此没有单独接一个 lifecycle 通知调用；它对
Wiki 的实际影响是通过其内部调用的 `matchEntries` → `matchConceptBatch`
自然触发的（scenario 2），不是重复接线。

`docs/impl/v1/wiki.md`「重编译标记」一节与「已知遗留」第 1 条已同步改写，
不再是"当前没有生产者"的描述。

### 代码/配置死代码清理

- 删除 `internal/wiki/aspect.go`（及其测试 `aspect_test.go`）——
  `buildAspects`/`BuildAspectEdges`/`ClusterAspects`/`SuggestAspectName`
  确认无调用方。但 `edgeKeyPair`（该文件定义的小工具函数）被
  `internal/wiki/store.go` 的 `CooccurrencePairs` 实际调用，删除整个文件
  会破坏编译，因此把这一个函数原样搬到 `store.go`。**由此发现一个新问题
  留待用户决定**：`CooccurrencePairs`/`PointIntents`/`RelationsAmong`
  （均在 `internal/wiki/store.go`）在 `aspect.go` 删除后，自身也变成了
  没有任何调用方的孤儿 Store 方法（此前唯一的调用方就是被删除的
  `BuildAspectEdges`）。本次任务只点名了 `aspect.go` 里的四个函数，这三个
  store 方法未在任务范围内，故保留未删，`edgeKeyPair` 迁移处的注释里已
  记了这一点，供后续决定是否一并清理。
- 另发现 `internal/study/service.go` 的 `CohesionConfig`（`WRel`/`WCooc`/
  `CoocSat`/`Gamma` 四个字段）虽然被 `cmd/server/main.go` 用
  `cfg.Wiki.AspectWRel` 等配置项赋值构造、且作为 `NewService` 参数存进
  `Service.cohesion` 字段，但该字段本身在 `internal/study` 包内**没有任何
  读取点**——内聚度计算逻辑看起来已经不在了，只剩下这个从未被消费的配置
  管道。因为 `AspectWRel`/`AspectWCooc`/`AspectCoocSat`/`AspectGamma`
  四个配置项技术上仍"被代码引用"（main.go 读取并传参），按用户"只删真正
  无引用的项"的要求予以保留，未删除；但这是否意味着 Study 侧的内聚度门槛
  这整块功能已经名存实亡、该不该借这次机会一起清理，需要用户确认，本次
  任务未处理。
- `config.yml`/`config.go` 已删除：两层架构专属
  （`topic_member_min`/`topic_compile_max_chars`/`topic_candidate_kp_max`/
  `topic_reliability_min`/`topic_rerank_batch_max_chars`）与切面聚类
  P1 专属（`aspect_w_intent`/`aspect_w_unit`/`aspect_split_gamma_factor`/
  `aspect_min_size`/`aspect_max_size`/`aspect_questions_max`）共 11 项，
  逐项 grep 确认无引用。`entry_cohesion_min`/`aspect_w_rel`/`aspect_w_cooc`/
  `aspect_cooc_sat`/`aspect_gamma` 予以保留——仍被 main.go 读入
  `study.CohesionConfig`（即上一条提到的、实际上已不被消费的管道）。
- 已删除 `config/prompts/wiki_topic_analyze.md`、`wiki_topic_compile.md`、
  `wiki_topic_candidate_rerank.md` 三个孤儿 prompt 文件，删除前再次 grep
  确认无 `.go` 引用。

`go build ./...`、`go vet ./...`、`go test ./... -count=1` 全绿（含本轮
新增的 lifecycle/entry_id 归属变化通知的单元测试，覆盖：已发布页面触发、
草稿/归档页面不触发、未覆盖已发布页面的 entry_id 变化不触发/不报错）。

## 第四轮清理落地记录（2026-08-18）

上一节末尾两条「新增发现」（`internal/wiki/store.go` 的孤儿 Store 方法、
`study.CohesionConfig` 死管道）本轮全部核实处理完毕。

### `CooccurrencePairs`/`PointIntents`/`RelationsAmong` 孤儿 Store 方法：删除

grep 确认三者除自身实现、注释外无任何调用方（唯一历史调用方
`aspect.go` 的 `BuildAspectEdges` 已在上一轮删除）。已删除三个方法，以及
仅服务于 `CooccurrencePairs` 的 `edgeKeyPair` 辅助函数（一并删除，删除后
无其他调用方）。`PointRelation` 类型本身仍被 `RelationsFromPoints`
使用，保留未动。`internal/wiki` 包的 `activation`/`encoding/json` 两个
import 在删除后仍被其他函数使用，未变成未使用 import。

### `study.CohesionConfig` 死配置管道：核实为完全死代码，整条删除

逐点核实结论：`internal/study` 包内对 `s.cohesion`（`CohesionConfig`
类型的字段）**没有任何读取点**——`Stats.Cohesion`
在代码里根本不存在（此前 `service.go` 顶部注释声称"Stats.Cohesion 仍会
计算并展示"是失实描述，未在本次核实前被发现）。内聚度计算逻辑（无论
是 Louvain 社区检测还是别的实现）在 `internal/study` 包中完全不存在，
`CohesionConfig` 从 `NewService` 参数存进 `Service.cohesion` 字段后就是
死胡同。

结论：这不是"换了种方式读配置"或"功能被等价替代"，是纯粹的死管道，
按要求整体删除：

- `internal/study/service.go`：`CohesionConfig` 类型、`Service.cohesion`
  字段、`NewService` 的 `cohesion CohesionConfig` 参数全部删除
  （`NewService` 签名从 8 个参数减为 7 个）。
- `cmd/server/main.go`：`study.NewService(...)` 调用点去掉
  `study.CohesionConfig{Min: ..., WRel: ..., WCooc: ..., CoocSat: ...,
  Gamma: ...}` 这个实参。
- `internal/foundation/config/config.go`：`WikiConfig` 的
  `EntryCohesionMin`/`AspectWRel`/`AspectWCooc`/`AspectCoocSat`/
  `AspectGamma` 五个字段删除（原先"仍被 main.go 读入
  study.CohesionConfig"这条保留理由已不成立，因为 CohesionConfig 本身
  被删）。
- `config/config.yml`：对应的 `entry_cohesion_min`/`aspect_w_rel`/
  `aspect_w_cooc`/`aspect_cooc_sat`/`aspect_gamma` 五行配置删除。
- 测试文件：`internal/study/*_test.go` 里全部 `NewService(..., cfg,
  activationSvc, nil/wikiSvc, 0, 0, CohesionConfig{}, 0)` 调用点批量改为
  去掉 `CohesionConfig{}` 实参（`scheduler_test.go`/`service_test.go`/
  `synonym_gap_test.go`/`activation_actions_test.go`/
  `bundle_scan_test.go`/`handler_test.go`/`integration_test.go`/
  `entry_scan_test.go` 共 8 个文件）。`internal/wiki/testhelpers_test.go`
  里 `config.WikiConfig{..., AspectGamma: 1.0}` 的 `AspectGamma` 字段赋值
  同步删除（该测试构造的是 `wiki.WikiConfig`，字段删除后编译不通过，
  一并清理）。

`go build ./...`、`go vet ./...`、`go test ./... -count=1`（全仓库）
本轮结束时验证全绿。
