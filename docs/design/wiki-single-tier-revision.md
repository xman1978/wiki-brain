# Wiki 单层化改判（2026-08-18）

## 定案

取代 `docs/design/wiki.md`「Wiki 页面类型」「主题：从真实使用中识别」「二阶编译的准入」「页面关系只有三种，层级由 contains 承载」等章节描述的两层架构（概念页一阶编译 + 主题页二阶编译，Study 自动识别主题候选驱动）。

新口径：

```text
Wiki 只有一种页面（沿用"主题页"这个名字），编译单层完成，触发方式改为
人工指定 Concept/Fact 词条集合（一个或多个），不再有 Study 驱动的自动
候选识别、qualifying 自动标记、needs_recompile 自动信号；编译材料改为
围绕词条集合做 Core/Context/Conflict 结构化展开，而不是整块塞给 LLM；
检索消费改为问题先做 Concept/Fact 识别再查已发布页面，不再用四元组
精确匹配。
```

## 为什么改（决策链）

1. Wiki 的核心价值主张是覆盖"冷启动、低频但重要"的知识——这类知识恰恰**没有**重复问答信号可积累。
2. 但概念页 qualifying 判据（verified ActivationLink）与主题候选判据（四元组重复问答聚类）本质上都依赖问答置信度积累，对冷启动材料结构性失效——一条从未被问过的 KP，`observed_conditions` 恒为空，Beta 后验均值恒 0.5，永远不会 verified，永远进不了 Wiki。
3. 也就是说旧架构下 Wiki 从未真正覆盖过它自己声称要解决的场景；继续投入打磨这套自动化（Study 四元组聚类、qualifying 自动化）没有意义。
4. 真正能覆盖冷启动材料的准入信号，是**材料结构本身**（这批知识点之间关联紧不紧、有没有明确的概念/事实归属），而不是问答置信度——这正是 [fact-entry-parent-concept-task-brief.md](../impl/v1/fact-entry-parent-concept-task-brief.md) 里已经开始沉淀的 Concept/Fact 结构（`entries.kind`、`parent_entry_id`）能提供的东西。
5. 触发方式因此也从"系统识别候选、人工确认"改为"人工直接指定范围"——冷启动材料本来就不会自己被系统看见（没有问答信号可依赖），系统识别候选这一步在这个场景下无法工作，索性去掉，改成人工凭对知识领域的理解直接圈定范围。
6. 两层架构（概念页→主题页二阶编译）原本是为了在**自动**流水线里控制编译颗粒度；触发方式改成人工指定后，颗粒度由人工圈定的 entry 集合大小自然决定，不再需要程序分两阶段拼层级。

## 架构

### 触发

```text
POST /wiki/compile
body: { entry_ids: [entry_id, ...] }   -- 一个或多个 Concept/Fact 词条

不再有：
  Study 的主题候选四元组聚类（原 wiki.md「主题：从真实使用中识别」）；
  概念页 qualifying 自动标记 needs_recompile（原 study.md 步骤 6）；
  「知识领域页 + 新增词条」以外的任何自动识别入口。
```

### 材料选取：Core / Context / Conflict 子图

对 `entry_ids` 中每个词条：

```text
Core(entry)    = entry 直接归属的 KP（lifecycle=current）；
                 entry.kind=fact 且 parent_entry_id 非空时，父 Concept 的
                 Core 一并纳入，标注为「背景」而非「本页核心」；
Context(entry) = Core 中 KP 的一跳 related（KPN scope 不限 cross/内部）；
Conflict(entry)= Core 中 KP 的一跳 contradicts；

Subgraph = ∪ Core(entry_ids) + ∪ Context(entry_ids) + ∪ Conflict(entry_ids)
```

`related`/`contradicts` 只展开一跳，不递归——沿用之前讨论过的"图谱子图 ≠ 给 LLM 的上下文"的收敛规则，避免大 entry（几十个 KP）一展开就吃满预算。

编译 prompt 的输入从现状的扁平材料列表，改为三个分组（Core / Context / Conflict）显式传入，写作阶段据此区分"本页应该讲什么／可以引用什么背景／必须交代什么例外"，而不是让 LLM 自己从一堆材料里猜层次。

### 产物

```text
wiki_pages 表结构不变（沿用「主题页」这个既有 page_type 名称，不再有
「概念页」这个 page_type）；
citation 白名单 = Subgraph 覆盖的全部 point_id（不再是"成员概念页
source_point_ids 并集"这种依赖 contains 关系的算法）；
wiki_page_relations 只保留 related/contradicts（已发布页面之间，程序从
entries 层面的 KPN 关系派生，不调 LLM）；contains 关系整体废弃——不再
有"主题页聚合概念页"这回事，一次编译请求直接产出一份成品页面；
wiki_drafts 写作草稿机制不变；Evidence citation 回链机制不变。
```

### 检索消费

```text
问题 -> 轻量 Concept/Fact 识别（判断这个问题主要涉及哪个/哪些
  entry_id，程序层面可复用/参照现有 unit_entry_match 的判断方式）
     -> 命中的 entry_id 若有已发布页面覆盖 -> 进入 Wiki 直答；
不再有 matchFourTupleEntry 四元组精确匹配这个入口。
```

Concept/Fact 识别本身要不要经过 LLM、经过时是否计入快路径 LLM 调用预算，是本文档之外需要单独定案的一点（见「待确认」）。

## 废弃清单

```text
Study:   四元组主题聚类整条链路（distinct_question_count/days_active
           判据、topic_cluster_min_questions/days_active 配置项、
           buildWikiCandidates 及其依赖的 topic 聚类部分）；
         概念页 qualifying -> needs_recompile 自动标记（study.md 步骤 6）。
Wiki:    matchFourTupleEntry 四元组直答匹配；
         二阶编译（页面聚合页面）相关逻辑与 member_roles 结构化落库；
         「概念页/主题页」两种 page_type 的区分，contains 关系。
```

## 保留/复用

```text
wiki_pages 存储结构、citation 白名单校验、四/五节正文结构（是否仍要求
  固定节数待后续「领域模板」版本再议，本次改判不动写作 prompt 措辞）；
wiki_drafts 派生草稿写作链路；
Evidence 回链（Wiki -> Concept/Fact entry -> KP -> KU -> source_ref）；
wiki_page_relations 的 related/contradicts（程序派生，不调 LLM）；
Study 报告中 Wiki 相关统计的展示价值——是否保留取决于展示内容是否还有
  数据源（四元组聚类没了，qualifying 统计还在，可以保留为只读参考）。
```

## 已拍板（2026-08-18，取代上一版「待确认」）

```text
1. Concept/Fact 识别匹配经过 LLM；检索出的候选 KP 之后仍要走既有 Wiki
   检索链路同款的分类、证据充分性验证等阶段（与旧四元组匹配命中后的
   处理一致，这次只换了"怎么找到候选页面"这一步，命中后的验证流程不
   变）。retrieval.md「LLM 调用预算对照」需要同步新增这一次调用。
2. 现有已生成的「概念页」「主题页」数据（含 wiki_page_relations 的
   contains 行）直接删除，不做任何迁移——本次改判视为 Wiki 产物推倒
   重来，存量数据不具备沿用价值。
3. Core 展开时 entry.kind=fact 的父 Concept Core 不设大小上限——父
   Concept 下的 KP 数量本身受限于归属判断的产出规模（不是无界图遍历
   的问题），"图谱子图有界展开"原则只需要管住 related/contradicts 只
   展开一跳这一条，不需要额外管 Core 本身的大小。
```
