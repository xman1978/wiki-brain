# 编码任务指令：Wiki 两层架构 + 召回骨架 + 复杂度观测量

> 这是一份交给编码会话的任务说明，不是设计文档。设计与实现口径**一律以下列文档为准**，
> 本文件只负责给出范围、顺序、约束与验收门。文档与本文件冲突时以文档为准；
> 文档本身有歧义时**停下来问用户**，不要按直觉补全。

## 任务

按已定稿的文档实现四组能力：Wiki 两层架构（概念页 / 主题页）、页面关系派生、
主题页作为召回骨架接入检索、问题复杂度观测量。

**必读文档**（先全部读完再动手，不要边读边写）：

```text
docs/design/wiki.md               「页面关系只有三种，层级由 contains 承载」「主题页
                                   是召回骨架，不是直答单元」「写作产出回流必须防自指」
                                   「主题：从真实使用中识别，而不是从已发布词条事后
                                   聚类」（2026-08-03 起主题候选识别的权威依据，取代
                                   下面 wiki.md 步骤 4 曾经的"连通分量"口径）
docs/design/retrieval.md          「分层检索路径」「表达层的两种参与方式」「检索路径全图」
docs/design/cognitive-routing.md  「复杂度是相对已有知识而言的」「三种"复杂"分别由谁承接」
docs/impl/v1/wiki.md              数据结构 + 步骤 3、4、5、7、8、9、10（主体）
docs/impl/v1/retrieval.md         检索总流程第 0 层、骨架注入、LLM 调用预算
docs/impl/v1/trace.md             topic_decompose_signal、traces.skeleton_page_id
docs/impl/v1/study.md             主题页候选、报告提示项、步骤 7「问题复杂度观测量」
docs/impl/v1/kpn.md               步骤 2「自体祖先排除」
docs/impl/v1/page.md              步骤 3 两层架构扩展（关系区、覆盖度区、草稿）
CLAUDE.md                         「V1 关键设计决策」全部条目
```

## 第 0 步：先处理工作区状态（不要跳过）

当前工作区有约 1900 行未提交改动（`internal/wiki`、`internal/study`、`internal/activation`、
`config/`、`test/v1/` 等），是上一轮 Wiki 模块实现留下的，不属于本任务。

```text
1. git status / git diff 查看，向用户确认这批改动的处置（提交 / 保留 / 搁置）；
2. 确认 go test ./... 基线全绿，再开始本任务；
3. 基线不绿时先报告失败项并停下，不要在红的基线上叠加新代码。
```

## 实现顺序（严格按序，每步 `go test ./...` 全绿后再进入下一步）

```text
1. Migration 035 与 schema
     wiki_page_relations、wiki_drafts 建表；
     wiki_pages 增 member_roles / uncovered_points；
     sources 增 origin / origin_page_id；traces 增 skeleton_page_id；
     存量 page_type='topic' 且 entry_id 非空的行改写为 'concept'；
     口径见 wiki.md「数据结构」两层架构扩展段。

2. 概念页 uncovered_points（wiki.md 步骤 3 生成后校验段）
     编译 / 重编译时整体重算；不进正文、不进白名单、不参与门槛。

3. 页面关系派生（wiki.md 步骤 7）
     publish 时全量重写该页关系行；Study 侧按新增 KPN 增量重算；
     无向关系按 page_id 字典序归一化；纯 SQL + 内存计算，禁止 LLM。

4. 主题页候选（wiki.md 步骤 8「主题候选识别」+ study.md 步骤 6；
   2026-08-03 起改为四元组聚类，不再是 related 连通分量）
     traces 按归一化四元组 (subject, intent, audience, constraint_text)
       分组 → 稳定簇判定（distinct_question_count / days_active）→
       候选范围内知识点语义检索 → qualifying 筛选 → 按 entry_id 分组
       （未发布但满足概念级 ready 判定的分组随批写 wiki_candidate）→
       二阶准入（关联 + 整体可靠度）→ draft 壳页 + contains（已发布
       成员）+ learning_results(action=topic_page_candidate,
       object_id=壳页 page_id)；
     不满足稳定簇判定或候选范围内无 qualifying KP 时写
       topic_signal_underfilled 报告项，不产壳页；
     人工手动指定（POST /wiki/topics）改为给 topic_name/topic_description，
       不再要求给已发布 member_page_ids。

5. 二阶编译（wiki.md 步骤 8）
     新增 config/prompts/wiki_topic_analyze.md / wiki_topic_compile.md；
     前置检查成员全部 published（否则 409 + 列出待处理）；
     五节结构校验；member_roles 落库与越界剔除；
     source_link_ids / observed_conditions / uncovered_points 取成员并集。

6. 检索接入（wiki.md 步骤 8「检索接入」+ retrieval.md 第 0 层与骨架注入）
     主题页命中 → 展开 contains 成员进候选（按 member_roles 排序）；
     skeleton_point_ids 注入慢路径 Rerank、跳过 Outline/FTS/RRF；
     traces.skeleton_page_id 记录。

7. 拆解信号与级联重编译（wiki.md 步骤 9 + trace.md）
     topic_decompose_signal 检索时写、慢路径完成后在 trace_write 内回填
     resolved_point_ids / resolved_member_page_ids / resolved_outside_count；
     成员页面 needs_recompile / archived → 父主题页 needs_recompile。

8. 写作草稿（wiki.md 步骤 10）
     mode=page / assembled；evidence_index 只读生成；stale 标记；
     PATCH 不做任何校验；**不实现任何 draft → page 的写回接口**。

9. 回流防护（wiki.md 步骤 10 + kpn.md 步骤 2）
     POST /sources 支持 origin / origin_page_id（默认 upload，既有行为不变）；
     KPN 匹配剔除来源页面已引用过的 KP；被剔除的边不计入 qualifying /
     连通分量统计；计数进 wiki_draft_reflow 报告项。

10. Study 报告扩展（study.md 步骤 6 报告提示项 + 步骤 7 复杂度观测量）
      oversized_topic_cluster / wiki_draft_reflow / topic_decompose；
      question_complexity 按四元组条件组聚合，归一化口径复用
      activation.Matcher；complexity_hint 阈值未定标前留 null。

11. Page 视图（page.md 步骤 3 两层架构扩展）
      关系区、member_roles、覆盖度区、主题页候选区、草稿编辑器 + 证据清单。

12. 验收测试
      go test ./... 全绿；补 test/v1/ 的端到端用例，覆盖 wiki.md
      「完成标准」两层架构扩展段的每一条。
```

## 开工前必须向用户确认的两处（文档留白，不要自行决定）

```text
A. 骨架注入时 domain(1) + source(1) 两次预过滤是否也跳过？
   文档写了「跳过 Outline/FTS 与 RRF」，但也写了「注入不足时由既有召回补齐」，
   补齐就需要这两步。待确认的建议方案：skeleton_point_ids ≥ rerank_top_n
   时纯注入并跳过预过滤（慢路径 3 次调用）；不足时保留预过滤走混合召回（5 次）。

B. 骨架注入要不要灰度开关（类似 retrieval.fast_path）？
   注入把召回质量押在主题页成员边界上，边界偏窄会漏且 sufficient 兜不住。
   建议加开关、默认关闭，观测 resolved_outside_count 后再打开。

C. 主题候选识别的四元组聚类是 2026-08-03 才从"连通分量"改过来的新机制，
   `wiki.topic_cluster_min_questions` / `topic_cluster_min_days_active` /
   `topic_candidate_kp_max` / `topic_reliability_min` 这几个阈值是 wiki.md
   本次修订按设计方向新提出的实现方案，不是设计文档钦定的数字，也未经过
   任何真实数据验证。开工前应与用户确认这批阈值是否可用作起点，以及"候选
   范围语义检索"具体复用哪一套现成的全文检索实现（wiki.md 未指定，只写了
   "对知识点全文索引做语义检索"）。
```

## 硬约束（最容易被"顺手"违反的条目，逐条对照）

```text
页面关系只有 related / contradicts / contains 三种。
  禁止引入 broader / narrower——KPN 只有 2 种关系且恒 bidirectional、
  entries 在 domain 下平铺无父子，派生不出层级，层级唯一来源是 contains。

主题页命中后不调 answer_wiki。
  它是召回骨架，不是直答单元。不要"顺手"让它也试一次直答。

主题页只聚合概念页，不聚合主题页。重编译级联深度恒为 1。

主题页 citation 白名单 = 成员页面 source_point_ids 并集，正文仍标注 point_id。
  「主题页 → 概念页 → KP → KU → source_ref」必须完整可走。
  二阶编译只重组，不新增事实，不引用成员之外的 point_id。

uncovered_points 只作字段，不进正文。
  概念页四节 / 主题页五节的结构校验与 citation 白名单校验一条都不许改。

member_roles 必须结构化落库，不能只写在正文里。

wiki_pages.content 只由编译产生。不存在任何 draft → page 的写回接口。

回流三条防护缺一不可（来源标记、祖先关系跳过、统计排除）。
  只排除自体祖先边，回流 KP 与其他知识的关系照常建立。

topic_decompose_signal 只累积、只进报告。
  不改页面状态、不计入 ActivationLink 成功 / 失败统计、不触发重编译。

question_complexity 只进报告。不改任何检索行为——V1 没有路由层。

不实现：复杂问题拆解与子结论聚合（V3）、Claim 双产物（V2）、
  intent 跳层守门（本轮明确未采纳）、方法 / 经验 / 问题 / 决策页（V3）。
```

## 沿用的既有工程约定（CLAUDE.md）

```text
包结构 internal/<module>/；migration 按版本号追加，035 起；
所有 LLM 调用经 LLMClient interface，测试用 fake client，不发真实网络请求；
位置字段统一 line_start / line_end（1-based inclusive），禁止 char/byte offset；
Prompt 文件放 config/prompts/，Schema 写在 prompt 的 ## Schema 段内；
错误处理返回标准业务错误类型，不用裸 errors.New；
Study 走 time.Ticker，不进异步队列；
回复使用与提问相同的语言。
```

## 完成标准

以 `docs/impl/v1/wiki.md`「完成标准」的两层架构扩展段为准，逐条可验证。
其中三条必须有对应测试断言：

```text
命中主题页的问答里 answer_wiki 调用次数 ≤ 展开后实际尝试的概念页数；
主题页展开后直答全失败时，慢路径未执行 Outline 召回，
  且总 LLM 调用数不高于同问题无主题页命中时；
代码中不存在 draft → page 的写回路径。
```
