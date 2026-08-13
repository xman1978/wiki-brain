# Wiki 生成方案（编译链路重构）

> 状态：**方案已简化，待据此收缩现有代码**（见文末「与 wiki.md 的冲突清单」与
> 「分期落地建议」）。2026-07-31 之前曾按本文档更早版本（提纲合成 + 逐节生成的重架构）
> 实现过一版代码；确认该架构测试门槛过高、收益不足以覆盖复杂度后，方案改为本版——
> 保留切面聚类（阶段 B，已实现且不变），撤回提纲/逐节的拆分，回到两次整页 LLM 调用。
> 修改代码的具体指令见 `docs/impl/v1/wiki-generation-simplify-task-brief.md`。
>
> 范围：替换 wiki.md 步骤 3（编译输入按切面分组）；**保留不变**：步骤 2 的
> analyze/compile 接口形态（扁平 `claims[]`/`tensions[]`，只新增可选 `aspect_id`
> 字段）、步骤 4 的三入口召回与 sufficient/citation 闸门、步骤 5 的重编译（仍整页
> 重新生成，不做按节增量）、步骤 6 的端点命名、步骤 7 的页面关系派生、步骤 9 的级联
> 与拆解信号、步骤 10 的草稿与回流防护。

## 0. 问题与思路

### 现状的三个结构性缺陷

```text
1. 编译输入是无结构集合
   步骤 3 给 LLM 的 materials 是 concept 下 qualifying KP 的平铺列表，
   唯一排序权重是 confident_count。层级只有 domain -> concept -> KP，
   concept 太粗、KP 太细，中间缺一层「切面」。LLM 拿到无结构断言集合，
   只能罗列——与提示词质量无关。

2. 校验只保证形式，不保证内容
   现有校验 = citation 白名单包含关系 + 四节标题存在性。它保证的是
   「引用的 point_id 在集合内」，不保证「这条结论确实是这些 KP 说的」。
   LLM 引用 3 个合法 point_id 写出一条它们并不支持的结论，全部校验通过。

3. 门槛在判定层就已放行
   概念级 ready 的「连贯」口径是 related_connection_count >= 1。
   一个 12 条 qualifying KP、分成 3 个互不连通的簇、总共 1 条 related 边的
   概念照样判定 ready。门槛问的是「有没有连接」，不是「连不连通」。
```

### 本方案的一句话

**把「一次 LLM 把一堆 KP 写成一页」，改造成「用系统已有信号先算出结构，让 LLM 在这个
结构约束下写作，最后用系统已有信号验收」——结构计算和写作调用是分开的两件事，但写作
调用本身不必按结构逐块拆成多次。**

三条原则：

```text
结构由程序算，不由 LLM 想
  沿用 wiki.md 步骤 7「页面关系全部由程序派生，不调 LLM」的既有立场，
  把它从页面层下沉到页面内部的段落组织层——但下沉到「组织依据」为止，
  不下沉到「调用粒度」。

材料与产物携带结构标签，写作时依标签组织，不是无结构罗列
  阶段 B 算出的切面分组喂给分析与成文两次调用；分析阶段产出的每条论断
  标注它属于哪个切面（可选字段，不做白名单收窄依据）；成文阶段被要求
  按切面分组书写「展开说明」，不得把同一切面的内容打散。

验收用系统自有信号，不用 LLM 自评分
  qualifying KP 当初被慢路径以 confident 答对过的真实问法，
  是一个带真值的评测集：页面答不了这些问题，就是没组织好。
```

### 为什么不做「提纲合成 + 逐节生成」

更早版本的方案把阶段 C/D 拆成「1 次提纲 LLM + N 次逐节 LLM」，收益是把 citation 白
名单从「整页 claims 并集」收窄到「本节 point_ids」、单节失败可单独重试。代价是：
`AnalyzeResult`/`CompileRequest` 需要 breaking 改成 `OutlineResult`/`OutlineSection`
结构、前端要同步改、需要新增 `wiki_page_sections` 持久化表、需要处理并发/重试/降级
状态机、单测覆盖面从「给定一次 LLM 响应，装配对不对」膨胀到「N 次响应的组合状态对
不对」。而白名单收窄的价值已经被阶段 E 的支持度核验（判断论断是否真被材料支持，
而不只是 point_id 合法）大部分覆盖——两者不是同一件事，但对「页面别把 KP 堆砌成
不成立的结论」这个核心问题，后者是更直接、更省的解法。

结论：结构计算（阶段 B 的切面聚类）保留，**写作调用退回两次**（分析 + 成文），
切面只作为两次调用的输入组织依据与产出的可选标注，不再是独立的调用边界。这不是
否定「结构收窄幻觉空间」这个方向，只是认为在当前证据不足以证明「N 次调用的复杂度
是必须的」之前，先用更轻的方式把结构用起来；如果后续观察到具体的失败模式（例如
展开说明确实反复把不同切面的内容混写），再针对性升级到逐节生成也不迟。

### 管线总览

```text
阶段 A  信号归集        程序        0 LLM
阶段 B  切面聚类        程序        0 LLM      ← 已实现，本方案核心新增，保留
阶段 C  论断分析        LLM         1 次       ← 即现状 analyze，材料按切面分组
阶段 D  成文生成        LLM         1 次       ← 即现状 compile，正文按切面分组组织
阶段 E  支持度校验      LLM         1 次批量   ← 已实现，堵内容级漏洞
阶段 F  装配与元数据    程序        0 LLM
阶段 G  发布质量门      LLM         K 次       ← 已实现，复用 answer_wiki
```

LLM 预算：2 次（analyze + compile），与现状完全一致，**不比现状增加成本**。阶段 E/G
各自有独立开关（`claim_verify_enabled`/`selfcheck_enabled`），已实现，默认开启；
关闭退回无门行为。编译是低频、人工触发的离线动作，阶段 E/G 的额外调用不在问答关键
路径上。

## 1. 阶段 A：信号归集（程序，0 LLM）—— 已实现，不变

qualifying KP 的定义**不变**（wiki.md 步骤 3：KP 与所属 KU 均 lifecycle=current，
且该 KP 存在 status=verified 的 ActivationLink，仅此一条**，2026-08-12 修订，
取代 2026-08-11 曾加过的 wiki_material_confirm 关卡——该人工确认关卡已整体
废弃，见 `docs/design/wiki.md`「2026-08-12 改判」，qualifying 恢复为只看
verified**）。在此之上归集五路信号，全部来自现有表：

| 信号 | 来源 | 在本方案中的用途 |
|---|---|---|
| 材料结构 | `knowledge_units.center`、`knowledge_points.point_type`、`knowledge_points.source_id` | 聚类兜底边、切面命名、证据属性 |
| 语义关系 | `knowledge_point_relations`（related / contradicts） | 聚类主边、张力定位 |
| 使用共现 | `question_kp_cooccurrence`（question_terms × point_id，confident_count） | 聚类共现边 |
| 使用条件 | `activation_links.observed_conditions`（status=verified，`point_id` UNIQUE 保证 KP↔link 1:1） | 聚类 intent 边、切面命名 |
| 真实问法 | `traces`（retrieval_quality='confident' 且 direct_point_ids ∋ point_id）的 `question` | 切面问法标注、回放评测集、trigger_questions |

**source_id 的定位（重要）**：source 是**证据属性**，不是主题结构。

```text
不按 source 分节的理由（否则违反 docs/design/wiki.md
「Wiki 编译因此是对已经在真实使用中稳定下来的知识的再组织，而不是对原始材料的再排版」）：
  同一 source 内关于同一概念的 KP 常分属不同切面；
  不同 source 常在讲同一切面；
  按 source 分节 = 该合的被拆开、该拆的被合在一起，
  产出「A 说…、B 说…」的文献综述——换一种堆砌方式而已；
  而「跨来源把同一论断汇聚为一条稳定结论」正是 Wiki 相对 KU 的唯一增量。

source 的正确用法（本方案采纳）：
  a. materials 中每条 KP 标注所属 source，让 LLM 能识别
     「这条论断有 N 个独立来源支持」（论断强度）与
     「这个矛盾发生在哪两个来源之间」（冲突归因）；
  b. 页面「依赖来源」一节按 source 归并列出（既有行为，不变）。
```

**查询口径**（均为纯 SQL，无 LLM，已实现于 `internal/wiki/aspect.go`/`gatherMaterials`）：

```sql
-- 共现问题数：两个 KP 被同一个 confident 问题同时引用的问题数
SELECT a.point_id, b.point_id, COUNT(DISTINCT a.question_terms) AS n
FROM question_kp_cooccurrence a
JOIN question_kp_cooccurrence b
  ON a.question_terms = b.question_terms AND a.point_id < b.point_id
WHERE a.confident_count > 0 AND b.confident_count > 0
  AND a.point_id IN (?) AND b.point_id IN (?)
GROUP BY a.point_id, b.point_id;

-- 真实问法：口径与 study.md ConfidentTraceQuadruples 一致，多取 question 原文
SELECT t.question, t.subject, t.intent, t.created_at
FROM traces t
WHERE t.retrieval_quality = 'confident'
  AND EXISTS (SELECT 1 FROM json_each(t.direct_point_ids) j WHERE j.value = ?)
ORDER BY t.created_at DESC;
```

## 2. 阶段 B：切面聚类（程序，0 LLM）—— 已实现，不变

本节描述与 `internal/foundation/graph`（Louvain 通用实现）、`internal/wiki/aspect.go`
（`BuildAspectEdges`/`ClusterAspects`/`SuggestAspectName`）的现状代码一致，**本次简化
不改动这部分**——它只计算结构，产出的 `[]Aspect{AspectID, SuggestedName, PointIDs}`
同时喂给简化后的阶段 C/D 与 Study 的概念内聚度判定，两个消费方都不变。

### 2.1 建图

节点 = 该 concept 的 qualifying KP。无向加权边：

```text
w(p, q) = w_rel    * [存在 KPN related(p,q) 或 contradicts(p,q)]
        + w_cooc   * min(1, cooc_questions(p,q) / cooc_sat)
        + w_intent * Jaccard(intents(p), intents(q))
        + w_unit   * [unit_id(p) == unit_id(q)]

其中 intents(p) = p 的 verified ActivationLink 的 observed_conditions
  中全部 intent 值集合（归一化后）。
```

**contradicts 计正权、不计负权（反直觉但正确）**：互相矛盾的两条 KP 恰恰在讲同一件事，
它们属于同一切面。把矛盾边当斥力会把同一议题的正反两面拆到不同切面，页面反而看不出
冲突。矛盾在**张力收集**时单独提出（阶段 C 的 tensions），不在建图时处理。

**权重语义排序**：`w_cooc`（使用侧，最强）> `w_rel`（关系侧）≈ `w_intent`（使用侧条件）
> `w_unit`（材料侧兜底）。默认值见配置项。这个排序对应系统立场：**主题边界由真实使用
划定，材料结构只作兜底**。

**熟路指针（2026-08-11，设计层面，非实现变更）**：`w_cooc` 现在是对
`question_kp_cooccurrence` 做实时 pairwise SQL join 算出来的——"两个 KP 被同一个
confident 问题同时引用的问题数"，逐对现算，不落库、不沉淀。`docs/design/
activation-bundle.md` 的熟路稳定核本质上是同一份原始信号（confident traces 里
`direct_point_ids` 的共现模式）经过比例阈值、状态机筛出来的沉淀结果（2026-08-12
修订：显影身份判断已改为"先匹配已有熟路、未匹配上才分组发现新熟路"，不再单纯
靠归一化四元组分组，见 activation-bundle.md 步骤 2，但这处细节不影响本指针的
结论——沉淀结果依然比 pairwise 共现数更强），而且比 pairwise 共现数更强——一个
KP 出现在某条稳定核里，说的是"这一组 KP 作为整体被反复依赖过"，不只是两两之间
偶然同时出现过。Bundle 落地后，
这里预期有两处可以变化：(1) `w_cooc` 的计算可以直接复用某个 concept 范围内已
显影的熟路稳定核作为共现来源，不必每次重新做 pairwise join（性能 + 一致性，
同一份"共现"定义在内聚判定和熟路显影两处不再各自实现一遍）；(2) 稳定核本身
可以作为一种比 pairwise 边权更强的证据——同属一条稳定核的 KP 之间，边权可以
不止是"共现次数"，而是"确实作为整体被验证过"。是否替换、替换后 `aspect_w_cooc`
/`aspect_cooc_sat` 要不要重新校准，尚未定案，本次修订不改动上面的公式，只记录
这个接入点。呼应 `wiki.md` 步骤 3「内聚」判定处同一枚熟路指针——那里描述的是
现象（Study 侧内聚判定的边权来源），这里是这枚指针在本方案里对应的具体落点
（`internal/wiki/aspect.go` 的 `BuildAspectEdges`）。

### 2.2 社区检测（替换连通分量）

用 Louvain 模块度优化，不用连通分量。

```text
为什么不能用连通分量（wiki.md 步骤 8 曾经的做法，2026-08-03 起已改为对
  真实提问的四元组聚类、不再对页面图求连通分量，见 wiki.md「主题候选
  识别」——但下面这个问题在概念内部的切面聚类里同样存在，且更严重）：
  relation_kpn_min 默认 1，一条边即连通。低阈值图上连通分量必然退化成
  一个巨型分量 + 若干孤点——wiki.md 自己写了这个问题，并用
  topic_member_max=8 兜底，但兜底的结果是「超限就不产候选」，
  即规模一上来主题页就再也产不出来。

Louvain 优化的是模块度（社区内边密度显著高于随机期望），
  在同样的低阈值图上产出大小合理、边界有依据的社区，
  不需要靠阈值硬切，且天然带层次。
```

后处理：`|C| > aspect_max_size` 时对该社区子图以 `gamma*aspect_split_gamma_factor`
递归再跑一次（最多一层），仍超限则按 `unit_id` 二次切分；`|C| < aspect_min_size` 时
并入与之边权和最大的社区，仍无边（孤立节点）则归入保留切面 `"misc"`；节点遍历顺序按
`point_id` 字典序，保证同输入同输出（确定性、可测试）。

### 2.3 切面命名（程序给建议名，LLM 可在阶段 C 里改写标题措辞，但不能改变分组）

```text
候选名 = 该社区 KP 所属 KU 的 center 的高频词（gse 分词后取 top-2）
        + 该社区最高频 intent（若存在 verified link）
例："索引结构 · 原理"、"索引 · 故障排查"
```

### 2.4 关键副产物：概念内聚度（补上判定层的洞，已实现）

```text
cohesion = max(|C_i|) / |V|            最大社区占比
aspect_count = 有效社区数（不含 misc）

概念级 ready 判定在既有四项（广度 / related 连接 / contradicts 不反客为主 /
days_active）之上新增第五项：
  cohesion >= wiki.entry_cohesion_min（默认 0.5）

不达标时：
  不产 wiki_candidate；
  进学习报告的「概念边界」节（report.entry_split_signals，非 learning_events 行，
  与 topic_decompose_signal 同一先例——只累积、不驱动任何 V1 学习动作）；
  **不建 entry_candidates(kind=split) 行**——split 候选按
  docs/impl/v1/concept-evolution.md 明确推迟到 V3。
```

## 3. 阶段 C：论断分析（1 次 LLM）—— 即 wiki.md 步骤 2 的 analyze，材料按切面分组

### 3.1 输入

```text
concept 名 / description / 所属 domain 名；
切面清单（阶段 B 产出）：
  [{ aspect_id, suggested_name,
     points: [{ point_id, content, point_type, unit_center, source_label }],
     observed_questions: [该切面 KP 的真实 confident 问法，去重后上限
                          wiki.aspect_questions_max（默认 5）] }]
跨切面 contradicts 对：[{ point_id_a, point_id_b, aspect_a, aspect_b }]；
knowledge_gaps：口径不变（question_terms 与概念名/KP 内容有词项重合）；
输入总量上限 wiki.compile_max_chars；超限按切面的 KP 数降序整体截取切面，
  不按单个 KP 的 confident_count 打散截取——**截掉的是整个切面，不是打散的 KP**，
  保证剩下的材料仍然成结构。
```

### 3.2 输出（`AnalyzeResult`，与现状 wiki.md 步骤 2 同形状，只新增一个可选字段）

```json
{ "claims": [{ "summary": "…", "cited_point_ids": ["…"], "aspect_id": "a1" }],
  "tensions": [{ "description": "…", "related_point_ids": ["…"] }] }
```

`aspect_id` 是**非破坏性新增字段**（`omitempty`）：标注这条论断主要属于哪个切面，
供阶段 D 组织「展开说明」的分组依据。它不参与 citation 白名单校验（校验仍然是
`cited_point_ids ⊆ 全部 qualifying KP`，口径不变），LLM 若漏填或填错切面 id，程序
按 `cited_point_ids` 落在哪个切面的 `PointIDs` 里做兜底纠正（多数落入同一切面时用
该切面；跨切面或都不落入时归为 `"misc"`），不因此拒绝这条 claim。

**`readiness`（2026-07-31 新增，同样非破坏性、`omitempty`）**：顶层响应（不在
`claims[]` 里）额外附一份信息性快照——`qualifying_kp_count` / `related_connection_
count` / `contradicts_connection_count` / `days_active`（+`days_active_min`）/
`cohesion`（+`cohesion_min`）。这是 `docs/impl/v1/wiki.md` 步骤 2「人工指定主题
手动编译」新增的第二条生成口径要用的——人工不等 Study 判定直接选概念编译时，
分析阶段仍把 Study 判 ready 用的同一组信号算出来展示，但不设 recommendation、
不阻断。`cohesion` 复用阶段 B 已经算好的切面聚类结果（`in.aspects`），不是重新
按 Study 的口径（只用 KPN+共现两路信号）算一遍，两者数值可能有出入，均属预期
——阶段 B 的切面聚类本来就比 Study 的内聚度判定多考虑 intent/unit 信号，这里只是
顺手把已经算出来的结构拿来当参考展示，不为了对齐 Study 的数字而重新算一份。

### 3.3 Prompt：`config/prompts/wiki_analyze.md`（恢复此文件名，内容按切面分组改写）

```text
根据以下已按「切面」分好组的知识点、以及每个切面对应的真实被问过的问题，
提炼这个概念的稳定结论与待验证的张力。不要写正文。

要求：
1. 只使用提供的材料，不引入材料之外的信息；
2. 切面分组由系统依据真实使用数据算出，不要重新分组；标注每条结论
   （claim）主要属于哪个 aspect_id——若一条结论确实跨切面成立，可以不填
   aspect_id 或标注多个切面里最主要的一个；
3. 同一事实有多个来源支持时合并成一条结论并列出全部 point_id，
   不要按来源分开罗列（"某某文档说…"这种写法不符合本页面的定位）；
4. 材料之间的矛盾、以及 gap 列表中与本概念相关的部分，写入 tensions，
   不要在这一步强行调和。

概念：{{entry_name}}（{{entry_description}}），所属领域：{{domain_name}}
切面与材料：
{{aspects}}
跨切面矛盾：
{{contradictions}}
相关知识缺口：
{{gaps}}

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

### 3.4 分析后校验（程序，口径与现状一致，只新增 aspect_id 兜底）

```text
claims[].cited_point_ids ⊆ 全部 qualifying KP（既有口径，不变）；
tensions[].related_point_ids ⊆ 全部 qualifying KP（既有口径，不变）；
claims[].aspect_id 为空或不在阶段 B 的切面集合内 -> 程序按 cited_point_ids
  归属的切面兜底纠正（见 3.2）；
claims 全空 -> 视为分析失败，重试一次，仍失败 500（既有行为，不变）。
```

## 4. 阶段 D：成文生成（1 次 LLM）—— 即 wiki.md 步骤 2 的 compile，正文按切面分组组织

### 4.1 输入

```text
concept 名 / description；
阶段 C 确认后的 claims（携带 aspect_id）+ tensions；
切面清单（同阶段 C 给的那份，用于「展开说明」按切面组织三级小标题）；
knowledge_gaps；aliases / trigger_questions 不再是输入或输出（见 6.3，程序化取代）。
```

### 4.2 Prompt：`config/prompts/wiki_compile.md`（恢复此文件名，新增「按切面分组」的
硬性要求）

```text
根据以下已确认的结论、张力，以及这些结论所属的「切面」分组，把它们写成一篇完整的
Wiki 页面正文。

要求（在现状既有措辞基础上新增第 2/3 条）：
1. 正文必须包含五个二级标题，按顺序：## 摘要 / ## 稳定结论 / ## 展开说明 /
   ## 待验证点 / ## 依赖来源；
2. 「展开说明」内部必须按提供的切面分组组织，每个切面一个三级标题（### 切面标题），
   同一切面的结论写在同一个三级标题下，不要把一个切面的内容拆散到多处，也不要把不同
   切面的内容混写在同一小标题下；三级标题的措辞可以改写切面建议名，但不能改变分组
   本身（分组由系统依据真实使用数据算出）；
3. 「摘要」是这个概念的一句话定义加 2-3 句概览，不含 [point_id] 标注，必须能脱离
   全文独立成立；
4. 「稳定结论」逐条对应 claims，每条结论末尾以 [point_id] 标注依据；
5. 「待验证点」对应 tensions，以及材料间未调和的矛盾；
6. 「依赖来源」按来源归并列出涉及的 KU/Source；
7. 只使用提供的材料与结论，不得引用未提供的 point_id。

概念：{{entry_name}}（{{entry_description}}）
按切面分组的结论：
{{claims_by_aspect}}
张力：
{{tensions}}
相关知识缺口：
{{gaps}}

直接输出 Markdown 正文，不要输出 JSON、不要输出额外说明。
```

### 4.3 生成后校验（程序，口径与现状一致）

```text
hasRequiredSections：五节标题齐全（新增「## 摘要」一条断言，其余四节既有断言不变）；
正文中的 [point_id] 标注逐一提取，不在「全部 qualifying KP」白名单内的删除并 warn
  （既有 filterContentTags 行为，白名单范围不变——不像更早的逐节方案那样按切面/节
  收窄，这是本次简化明确接受的取舍，见「为什么不做」一节）；
正文为空或全部标注被剔除 -> 重试一次，仍失败 500（既有行为，不变）。
```

## 5. 阶段 E：支持度校验（1 次批量 LLM）—— 已实现，不变

现有校验保证「引用的 point_id 合法」，不保证「结论确实由这些 KP 支持」。这是当前设计
最大的质量漏洞，本节堵住它，与「阶段 C/D 是否拆分逐节」无关，本次简化不改动这部分
代码（`internal/wiki/service.go:verifyClaims`、`config/prompts/wiki_claim_verify.md`、
`wiki_claim_checks` 表）。

### 5.1 做法

```text
输入：阶段 C 确认后的全部 claims，每条附其 cited_point_ids 对应的 KP content
      与 KU 原文切片；
Prompt：config/prompts/wiki_claim_verify.md
  「判断每条结论是否能由其所附材料支持。只判断支持关系，不改写结论，
    不补充材料之外的知识。」
输出：[{ claim_id, verdict: "supported"|"partial"|"unsupported", reason }]
调用参数：与编译同一 reasoning 模型，temperature 0，一次批量（claims 通常 < 20 条）。
```

### 5.2 处置（不改正文，只落库 + 进门槛）

```text
supported   -> 无动作；
partial     -> 落库，页面详情展示提示，计入质量分（不阻断）；
unsupported -> 落库，**阻断 publish**（阶段 G），人工可选择：
               重编译、人工确认放行（force）、或把该 claim 移入待验证点后重编译。
```

### 5.3 表结构（已实现，migration 039）

```sql
CREATE TABLE wiki_claim_checks (
    check_id        TEXT PRIMARY KEY,
    page_id         TEXT NOT NULL REFERENCES wiki_pages(page_id),
    revision_id     TEXT NOT NULL REFERENCES wiki_revisions(revision_id),
    claim_id        TEXT NOT NULL,
    claim_text      TEXT NOT NULL,
    cited_point_ids TEXT NOT NULL DEFAULT '[]',
    verdict         TEXT NOT NULL,              -- supported / partial / unsupported
    reason          TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_wcc_page ON wiki_claim_checks(page_id, revision_id);
```

## 6. 阶段 F：装配与元数据（程序，0 LLM）

### 6.1 页面结构（四节结构不动，只新增摘要节 + 展开说明内部三级化）

```markdown
## 摘要                      ← 新增：LLM 直接写在正文里，不含 [point_id]
## 稳定结论                  ← claims 逐条，带 [point_id]
## 展开说明                  ← 容器不变，内部按切面分三级小节（LLM 一次性写出，非逐节调用装配）
### <切面 1 标题>
### <切面 2 标题>
## 待验证点                  ← tensions + partial/unsupported claims + 跨切面矛盾
## 依赖来源                  ← KU 主题 + source 归并清单
```

兼容性设计要点：`hasRequiredSections` 的既有四节断言全部继续成立，只需新增一条
`## 摘要` 断言。存量页面不受影响——小节校验只在编译产出时执行，存量页面不重跑校验；
存量页面在下次重编译时自然补上摘要节。

### 6.2 摘要单独列化（服务二阶编译，已实现）

```sql
ALTER TABLE wiki_pages ADD COLUMN summary TEXT NOT NULL DEFAULT '';
-- 与正文「## 摘要」节内容一致（程序从生成正文中提取该节文本写入本列，
-- 不再要求 LLM 额外单独输出一份 lead 字段——LLM 只写一份 Markdown 正文，
-- 摘要节的提取是阶段 F 的字符串操作，不是第二次 LLM 调用）。
-- 单独列化是为了二阶编译可以只吃成员页摘要而不是全文（见第 9 节）。
```

### 6.3 aliases / trigger_questions 程序化（已实现，不变）

```text
aliases：查 subject_synonyms（canonical = 概念名 且 status='active' 的全部 term）。
trigger_questions：阶段 A 的真实观测问法，按 point_id 打散取样、按 question_hash
  去重，取 wiki.trigger_questions_max（默认 10）条原文。
```

这两项都只影响召回宽度、不进 citation 白名单，口径不变（wiki.md 步骤 3 既有约定）。

### 6.4 切面结构化落库（供未来查询，不再落库逐节正文）

```sql
-- migration 040（收缩：去掉 wiki_page_sections 表，只保留下面两条 ALTER）
ALTER TABLE wiki_pages ADD COLUMN aspects TEXT NOT NULL DEFAULT '[]';
-- [{ aspect_id, heading, point_ids, question_types }]
-- 与 member_roles 是同一结构在两层的投影：member_roles 描述「主题页的成员页承担什么」，
-- aspects 描述「概念页的切面承担什么」。question_types 程序化取值：该切面 point_ids
-- 对应的真实 confident 问法，按 wiki.aspect_questions_max 截断（与阶段 C 给 LLM 看的
-- observed_questions 同一来源，不需要额外查询）。
-- V3 拆解、V1 检索排序直接查这个字段，不回去解析 Markdown；但不再有 wiki_page_sections
-- 逐节正文表——本次简化不做「按节增量重编译」（见第 8 节），没有独立的节内容需要落库，
-- aspects 只是元数据，不是可独立寻址的内容单元。
```

**不做的事（相对更早版本的收缩）**：不建 `wiki_page_sections` 表，不落库
`OutlineSection`/`compiledSection` 这类中间结构；`Claim`/`Tension` 仍是编译时的
瞬时中间量，跟现状一样不单独持久化（其文本经支持度核验后落一份到
`wiki_claim_checks`，这条既有行为不变）。

### 6.5 其余字段口径不变

`source_point_ids` / `source_unit_ids` / `source_link_ids` / `observed_conditions` /
`uncovered_points` 的计算口径与 wiki.md 步骤 3 完全一致（`source_point_ids` = 正文
实际引用的 point_id 并集）。

## 7. 阶段 G：发布质量门（K 次 LLM，复用 answer_wiki）—— 已实现，不变

本节与「阶段 C/D 是否拆分逐节」无关，本次简化不改动这部分代码
（`internal/wiki/service.go:Selfcheck`/`publish`、`wiki_quality_checks` 表）。

### 7.1 核心思想

这些 qualifying KP 当初是被慢路径以 `retrieval_quality='confident'` 答对过的，
对应的问法是**带真值的评测集**。页面如果答不了这些问题，说明它没把这批知识组织好。
这是用系统自己的能力验收自己的产物，不是 LLM 自评分。

### 7.2 执行

```text
POST /wiki/pages/:id/selfcheck        （也在 publish 内联调用一次，结果复用）
  1. 取该页 source_point_ids 的真实 confident 问法，按 point_id 打散抽样
     wiki.selfcheck_replay_n（默认 5）条；
  2. 逐条调用既有 answerFromPage（config/prompts/answer_wiki.md，完全复用）；
  3. 计算四项指标，落库 wiki_quality_checks；
  响应：{ page_id, revision_id, metrics{...}, passed, blocking_reasons[] }
```

### 7.3 指标与门槛

```text
replay_sufficient_rate   sufficient=true 的比例 >= wiki.selfcheck_min_sufficient_rate（默认 0.6）
material_usage_rate      |source_point_ids| / |qualifying KP| >= wiki.selfcheck_min_material_usage（默认 0.5）
uncited_sentence_rate    「稳定结论」+「展开说明」中不含 [point_id] 的句子占比
                         <= wiki.selfcheck_max_uncited_rate（默认 0.3）
                         （「摘要」「待验证点」「依赖来源」三节不计入）
unsupported_claim_count  阶段 E 的 unsupported 计数，必须 == 0
```

### 7.4 与 publish 的关系

```text
POST /wiki/pages/:id/publish
  请求新增可选字段 { "force": true }；
  未 force 且 selfcheck 未通过 -> 409，响应带 metrics 与 blocking_reasons，
    页面保持 draft / needs_recompile；
  force=true -> 正常发布，wiki_quality_checks.forced=1（这是 force 覆盖唯一的
    留痕方式；不写 learning_results 事件，也不进学习报告——force 覆盖是编译/
    发布链路内部的一次性人工决定，不是 Study 统计口径要追踪的学习动作，按
    "编译/重编译永远需要人工确认"这条本身已经蕴含"人工可以决定覆盖"，不需要
    再叠一层学习事件）。

selfcheck 结果按 (page_id, revision_id) 缓存：同一 revision 重复 publish 不重跑回放。
wiki.selfcheck_enabled=false 时整个阶段跳过（退回现有无门行为）。
```

## 8. 增量重编译 —— 不做，推迟

更早版本设想「按节匹配 + 只重生成变化节」，前提是有独立持久化的节内容可比对
（`wiki_page_sections`）。本次简化不做节级持久化，这个前提不再成立，因此增量重编译
**不在本方案范围内**：`Recompile` 保持 wiki.md 步骤 5 的既有行为——重新走一遍
阶段 A→B→C→D，整页重新生成，不保证与上一 revision 逐字节相同，写入新 revision，
状态回到 draft，等待人工重新 publish。

如果未来观察到具体痛点（例如页面频繁因为极小的材料变化而整页测辞漂移、人工审阅
成本明显过高），再评估是否值得为此重新引入节级持久化，不预先设计。

## 9. 主题页（二阶编译）的同构改造 —— 仍是 P3，未来事项

> **本节「候选产生」部分已被 2026-08-03 的设计修订推翻，不要实现**：
> `docs/design/wiki.md`「主题：从真实使用中识别，而不是从已发布词条事后
> 聚类」否定了"对已发布概念页的图求连通分量/Louvain 社区检测"这整条路线——
> 无论用连通分量还是 Louvain，前提都是先有一批已发布页面才能求图上的簇，
> 这与新设计的立场（主题候选必须先于、且独立于任何页面是否已编译发布）
> 直接冲突。当前权威口径是 `docs/impl/v1/wiki.md` 步骤 8「主题候选识别」
> ——对真实提问的四元组聚类，不是对页面图的任何形式的图聚类。下面这段
> "连通分量 -> Louvain" 的设想整体作废，保留原文只为存档，不要参照实现；
> 二阶编译输入（成员页面 summary 列而非全文等）与本节其余部分不受影响，
> 仍是有效的未来优化方向。

```text
候选产生（步骤 8，已作废，见上方说明）：连通分量 -> 同一份 Louvain 实现
  节点 = published 概念页；
  边权 = w_pagerel * KPN related 关系对数 + w_pageshare * 共享 KP 数；
  topic_member_min / topic_member_max 保留为**后处理约束**（同 2.2 的后处理），
  但不再是「超限就不产候选」的死路——超限社区递归再分，
  oversized_topic_cluster 报告项降级为「递归后仍超限」时才写。

二阶编译输入（步骤 8「输入收集」）：
  成员页面 **summary 列 + 稳定结论节**，而不是全文；
  成员页面之间的 related / contradicts 关系行；
  成员页面「待验证点」节文本并集；
  成员页面 trigger_questions 并集（程序取样，同 6.3）。

主题页五节结构不变（## 主题概览 / ## 主线结论 / ## 子主题分工 /
  ## 跨主题矛盾与待验证点 / ## 依赖页面）；「主题概览」即摘要，写入 summary 列。
member_roles 与 aspects 同源（6.4），口径不变。
阶段 E（支持度）/ G（质量门）对主题页同样适用，回放问法取成员页 trigger_questions。
检索接入完全不变：主题页仍不直答，命中即展开成员 + 注入 skeleton_point_ids。
```

## 10. 配置项（config.yml: wiki 节）

```yaml
wiki:
  # —— 既有项（口径不变）
  compile_max_chars:              12000
  recompile_new_kp_min:           2
  trigger_questions_max:          10
  qualifying_min_days_active:     7
  relation_kpn_min:               1
  relation_shared_point_min:      2
  topic_member_min:               3
  topic_member_max:               8
  topic_compile_max_chars:        24000

  # —— 阶段 B：切面聚类（已实现）
  aspect_w_rel:                   1.0
  aspect_w_cooc:                  1.5     # 使用侧信号权重最高
  aspect_w_intent:                1.0
  aspect_w_unit:                  0.5     # 材料侧只作兜底
  aspect_cooc_sat:                3       # 共现问题数饱和点
  aspect_gamma:                   1.0     # Louvain 分辨率
  aspect_split_gamma_factor:      1.5     # 超大社区递归再分时的分辨率放大
  aspect_min_size:                2
  aspect_max_size:                8
  aspect_questions_max:           5       # 每切面进分析阶段的真实问法条数，
                                          # 同时是 aspects.question_types 的截断上限
  entry_cohesion_min:           0.5     # 最大社区占比门槛（ready 判定第五项）

  # —— 阶段 E：支持度校验（已实现）
  claim_verify_enabled:           true

  # —— 阶段 G：质量门（已实现）
  selfcheck_enabled:              true
  selfcheck_replay_n:             5
  selfcheck_min_sufficient_rate:  0.6
  selfcheck_min_material_usage:   0.5
  selfcheck_max_uncited_rate:     0.3

  # —— 主题页社区检测（P3，未来）
  page_w_rel:                     1.0
  page_w_share:                   0.5
```

**从现有代码中移除**（属于被撤回的提纲/逐节架构，不再需要）：
`outline_min_sections`、`outline_max_sections`、`section_max_chars`、
`section_concurrency`。

## 11. HTTP API

```text
POST /wiki/compile/analyze      响应体 { claims, tensions }（现状既有形状，
                                claims[] 每项新增可选 aspect_id 字段，非 breaking，
                                前端无需改动即可正常渲染，改的话只是能额外展示分组）
POST /wiki/compile              请求体 { entry_id, page_type, result_id?,
                                claims?, tensions? }（现状既有形状，撤回 outline 字段；
                                缺省 claims/tensions 时服务端内部跑阶段 C，等价于
                                「先 analyze 再原样确认」，既有约定不变）
POST /wiki/pages/:id/publish    请求新增可选 { "force": bool }（已实现）；未通过质量门返回 409
POST /wiki/pages/:id/selfcheck  已实现：单独触发质量回放，不改页面状态
GET  /wiki/pages/:id            响应新增 summary / aspects（已实现）；
                                claim_checks[] / latest_quality_check 不进这个
                                响应体——两张表本身已落库（阶段 E/G），但和
                                wiki.md 步骤 3 里其它"API 之外的观察面"（如
                                learning_events）同一惯例，暂不在页面详情接口
                                里展开，需要查表或调 POST /wiki/pages/:id/
                                selfcheck 单独获取

不再新增：POST /wiki/pages/:id/sections/:sid/recompile（没有 section 概念了）。

其余端点（topic/analyze、topic/compile、recompile、archive、relations、drafts）
路径与语义不变。
```

## 12. 与 wiki.md 的冲突清单（收窄后，多数已随 P0/P1 落地不再是待确认冲突）

| # | wiki.md 原条款 | 本方案 | 现状 |
|---|---|---|---|
| 1 | 步骤 3：正文固定四节 | 五节（新增 `## 摘要`） | 需要：`hasRequiredSections` 加一条断言，存量页面不受影响 |
| 2 | 步骤 3：materials 平铺列表 | 按切面分组喂给 analyze/compile | 需要：analyze/compile 的材料组织函数改用阶段 B 输出（阶段 B 本身已实现） |
| 3 | 步骤 3：aliases / trigger_questions 由 LLM 生成 | 程序化 | 已实现，不再是待确认冲突 |
| 4 | 步骤 3：概念级 ready 四项判定 | 新增第五项 cohesion | 已实现，不再是待确认冲突 |
| 5 | 步骤 3：超长按 confident_count 降序截取 KP | 按切面整体截取 | 需要：`gatherMaterials` 的截取逻辑改用阶段 B 的切面分组 |
| 6 | 步骤 4：publish 无条件生效 | 加质量门，可 force | 已实现，不再是待确认冲突 |
| 7 | 步骤 8：连通分量求主题页候选 | Louvain 社区检测 | **已作废**：2026-08-03 起步骤 8 改为四元组聚类，不再对页面图求任何形式的图聚类，见第 9 节说明 |
| 8 | 步骤 8：二阶编译输入取成员页全文 | 取 summary + 稳定结论节 | P3，未来事项 |

**明确撤回的冲突**（更早版本引入、本次简化撤回，实现时应恢复到与 wiki.md 一致）：

```text
analyze 产出结构化 outline（撤回，恢复扁平 claims[]/tensions[]，只加 aspect_id）；
一次 LLM 整页 vs 逐节 N 次生成（撤回，恢复一次 LLM 整页生成）；
重编译按节增量（撤回，恢复整页重新生成，见第 8 节）；
全局 LLM 调用数从 2 次膨胀到 10~12 次（撤回，恢复 2 次，无超时/成本问题）。
```

**不冲突、明确保持不变的**（实现时不得"顺手"改动）：

```text
qualifying KP 定义（lifecycle=current + verified ActivationLink，不叠加次数门槛；
2026-08-11 曾加过的 wiki_material_confirm 关卡已于 2026-08-12 整体废弃，
见阶段 A 说明）；
citation 白名单校验（范围仍是「全部 qualifying KP」，不按切面/节收窄）；
KPN 只有 related / contradicts，direction 恒 bidirectional；
lifecycle 只有 current / superseded / deprecated；
编译/重编译永远需要人工调用，不做流水线自动编译；
两层架构、页面关系只有 related / contradicts / contains，不引入 broader/narrower；
主题页不直答，命中即展开成员 + 注入 skeleton_point_ids，跳过 Outline 召回；
不存在 draft -> page 的写回接口；
Wiki 草稿回流的三条自指防护；
split 候选仍推迟到 V3，本方案只写 entry_split_signal 报告项。
```

## 13. 分期落地建议

```text
P0（不动接口，收益最快）—— 已实现（2026-07-31）
  阶段 E 支持度校验 + 阶段 G 质量门 + aliases/trigger 程序化 + 概念内聚度门槛。

P1（切面聚类落地）—— 结构计算已实现，写作调用需要从重架构收缩为本方案
  已实现、保留：internal/wiki/aspect.go 全部（Louvain 切面聚类）；
  需要按 docs/impl/v1/wiki-generation-simplify-task-brief.md 收缩：
    移除 outline.go / sectioncompile.go 的独立提纲/逐节调用架构，
    恢复 analyzeClaims / compileContent 两次整页调用，
    但材料组织与正文分组消费阶段 B 的切面结构；
    移除 wiki_page_sections 表；claims[] 新增可选 aspect_id 字段；
    恢复 wiki_analyze.md / wiki_compile.md（按本文档 3.3/4.2 的新内容重建），
    删除 wiki_outline.md / wiki_section_compile.md。

P2  （不做，见第 8 节；只有出现具体痛点才重新评估）

P3  第 9 节主题页同构改造——候选产生部分已作废（见第 9 节说明），
    二阶输入改摘要仍是有效的未来优化方向。
```

## 14. 完成标准

```text
切面聚类（已实现，回归项）：
  同一输入产出确定性划分（节点按 point_id 字典序遍历，可断言）；
  低阈值图（每对 KP 仅 1 条 related 边）上不产生覆盖全部节点的单一社区；
  孤立 KP 归入 misc 且不单独成节；
  cohesion < entry_cohesion_min 时不产 wiki_candidate，
    且写出 report.entry_split_signals，不创建 entry_candidates 行。

分析与成文：
  analyze 返回的 claims[] 与 tensions[] 是扁平数组（非嵌套 sections），
    与撤回前 wiki.md 步骤 2 的形状一致，只多一个可选 aspect_id 字段；
  citation 白名单范围是「全部 qualifying KP」，不因为切面分组而收窄；
  正文包含「摘要 / 稳定结论 / 展开说明 / 待验证点 / 依赖来源」五节；
  「展开说明」下每个三级小标题对应阶段 B 的一个切面，同一切面的结论不应
    分散到多个三级小标题下（用 fake LLM 构造违反此要求的输出、断言校验能
    检出——注：这条只能检测「引用越界」这类程序可判定的违规，不能检测
    「LLM 是否真的老实按分组写」，后者本质上和「LLM 写得好不好」一样，
    不是 citation 校验能覆盖的范畴）；
  LLM 调用次数固定为 2（analyze + compile），不随切面数量变化。

支持度校验（已实现，回归项）：
  构造一条引用合法 point_id 但材料并不支持的 claim（fake LLM），
    verdict 判为 unsupported 且 publish 被 409 阻断；force=true 可发布并留痕。

质量门（已实现，回归项）：
  回放问法取自该页 KP 的真实 confident trace，非 LLM 想象；
  sufficient 率低于门槛时 publish 被阻断，metrics 可读；
  同一 revision 重复 publish 不重跑回放（缓存生效）；
  selfcheck_enabled=false 时行为退回现状。

重编译：
  Recompile 整页重新走一遍阶段 A→D，不要求与前次 body 逐字节相同；
  revision 正常写入，状态回到 draft。

主题页：
  暂不改动（P3），现状 wiki.md 步骤 8 行为保持不变。

回归（不得破坏）：
  citation 白名单、qualifying 口径、KPN 2 类关系、lifecycle 3 状态、
  两层架构、人工确认编译、无 draft->page 写回、回流自指防护
  的既有测试全绿。
```
