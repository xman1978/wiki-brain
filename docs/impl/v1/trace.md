# Trace 实现路径（V1 扩展）

## 职责

在 MVP Trace（质量分级、共现统计、gap/user_correction 事件）之上，增加激活类 Learning Event 的自动产生：每次问答记录 ActivationLink 命中与采用情况，产出 `activation_success` / `activation_failure` / `activation_gap` 事件。

**2026-08-13 起的机制基准**：随 `docs/design/activation-convergence.md` 的连续置信度设计（见 `activation.md`「状态机」），`activation_success`/`activation_failure` 不再只是"攒给 Study 事后计数判定跳变"的燃料——本模块在产生这两类事件的同一步，直接调用 `activation.RecordOutcome` 更新对应观测条件的 `success_count`/`failure_count`（见步骤 3）；Study 的周期扫描不再读取这两类事件做计数或状态判定，它的新职责（收敛趋势报告、收敛剪枝）改为直接读 `activation_links` 当前状态，见 `study.md`。本次同时新增 `activation_audit_success` / `activation_audit_failure` 两类事件，记录 Retrieval 抽样触发的独立核实试验结果（快慢路径比对），产生时同样直接调用新增的 `activation.RecordAuditOutcome`（见步骤 3b）。事件的评级逻辑本身（哪个 point 该记 direct、哪个记 supporting、哪个记 failure）完全不变——变的只是这次评级结果现在还会去更新一次置信度计数，而不是驱动链接状态跳变（旧状态机已不存在，见 `activation.md`）。

V1 Trace 仍不调用 LLM、不阻塞回答，沿用 `trace_write` 异步队列；`activation_audit_*` 事件的触发时机见步骤 3b——独立核实试验本身是 Retrieval 侧的后台慢路径比对，不阻塞已经返回给用户的快路径答案，比对完成时才产生这条事件，不在原始问答的 `trace_write` 任务窗口内同步产生。

## 数据结构

### traces 表扩展（migration 版本号顺延）

```sql
ALTER TABLE traces ADD COLUMN path_type TEXT NOT NULL DEFAULT 'full';
-- fast / full / wiki：本次回答走的检索路径（见 retrieval.md V1）
ALTER TABLE traces ADD COLUMN activation_link_ids TEXT NOT NULL DEFAULT '[]';
-- JSON 数组：激活层命中的 link_id 列表（fast 路径时非空）
ALTER TABLE traces ADD COLUMN subject    TEXT NOT NULL DEFAULT '';
ALTER TABLE traces ADD COLUMN intent     TEXT NOT NULL DEFAULT '';
ALTER TABLE traces ADD COLUMN audience   TEXT NOT NULL DEFAULT '';
ALTER TABLE traces ADD COLUMN constraint_text TEXT NOT NULL DEFAULT '';
-- Session 解析四元组，从 EvidenceSet 取原值列化
-- （evidence_snapshot 中已有，列化便于 Study 创建链接时直接查询；
--  constraint 是 SQL 保留字，列名用 constraint_text）

-- 两层架构曾在此扩展 traces.skeleton_page_id（主题页召回骨架注入），随单层化
-- 改造整体删除——见 wiki-single-tier-open-questions.md「已拍板
-- （2026-08-18）」，migration 058 已 DROP COLUMN。
```

traces.question 记录的是 expanded_question（standalone 补全后的问题，Page 传给 POST /answer 的值），question_terms 由其归一化生成——与激活匹配器的输入基准一致（见 activation.md 步骤 2）。

### 上游契约扩展

EvidenceSet 增加字段（Retrieval 产出，经 AnswerResult 原样传递给 Trace）：

```text
EvidenceSet 新增：
  path_type          fast / full / wiki
  activation_hits[]  [{ link_id, point_id, match_score, matched_by, tier,
                       audit_sampled, subject, intent, audience, constraint }]
                     激活层命中的链接及其目标 KP（full 路径为空数组）；
                     2026-08-13 新增 tier / audit_sampled / 四元组字段
                     （随 activation.md「状态机」的 Match() 返回值扩展一并
                     新增，见 activation.md 步骤 2、retrieval.md 步骤 1）：
                     tier ∈ {exploring, self_graded, trusted}（本次命中时
                     该条件所在的服务档位）；audit_sampled 表示 Retrieval
                     是否已为这次命中安排一次独立核实（供 Trace 步骤 3b
                     判断是否需要在核实完成后回写 activation_audit_*）；
                     subject/intent/audience/constraint 是这次命中所归属
                     的观测条件四元组原文（字面问题捷径命中时可能不同于
                     查询原始输入的四元组，见 activation.md「owning
                     condition 的可判定性」）——Trace 步骤 3 调用
                     activation.RecordOutcome 时定位具体条件要用这四个
                     字段，不能用查询自己的四元组代替
  gap_reason         no_candidates / judge_filtered / ""（产出规则见
                     retrieval.md 步骤 6，Trace 只读取，用于 knowledge_gap
                     payload，见下方「learning_events 事件类型扩展」）
  filtered_evidence[] 被 rerank judge 判无关的候选快照（结构同 Evidence，
                     role="irrelevant"）；Trace 不读取，随 EvidenceSet 原样
                     经 AnswerResult 落入 answers.snapshot，供 study.md
                     经 last_trace_id 回查

EvidenceItem 新增（证据挖掘产出，见 evidence.md）：
  mined              bool，该证据是否为挖掘出的片段（false=整段回退）
```

### learning_events 事件类型扩展

`learning_events` 表结构不变，`event_type` 枚举扩展：

```text
既有：knowledge_gap / user_correction（knowledge_gap payload 结构 V1 扩展，见下）
新增：activation_success / activation_failure / activation_gap
新增（2026-08-13，随连续置信度设计一并新增）：activation_audit_success /
  activation_audit_failure——Retrieval 抽样触发的独立核实试验结果（快慢
  路径比对），见步骤 3b；产生时直接调用 activation.RecordAuditOutcome，
  不经 Study 中转
新增（2026-07-24）：subject_synonym_gap（见步骤 3 近似检测；
  聚合消费方是 Study，见 study.md 步骤 2a）
topic_decompose_signal（两层架构曾新增，主题页展开成员后回落慢路径时产生）
  已随单层化改造整体删除——Wiki 不再有"主题页聚合概念页成员"这一层，事件
  产生的前提条件不再存在，见 wiki-single-tier-open-questions.md「已拍板
  （2026-08-18）」。历史行未迁移，只是停止产生新行（learning_events 复用
  通用 payload JSON 字段，无需 migration）。
```

各类型 payload 结构：

```json
// knowledge_gap —— 检索质量为 gap 时产生（MVP 既有事件，V1 payload 扩展）
{ "question": "...", "reason": "no_candidates | judge_filtered | answer_error | unspecified" }

// activation_success —— 每个满足条件的 link 一条事件
// role="direct"：point_id ∈ direct_point_ids；role="supporting"：point_id
// 未进 direct_point_ids，但对应的 supporting evidence 实际被引用（未被
// citation 白名单剔除）。role 的判定逻辑不变（见步骤 3）；2026-08-13 起
// role 不再产生计数上的权重差异——不像旧机制里 role=direct 才计入"晋升"
// 判定、role=supporting 只防"降权"（那套区分是离散阈值判定的产物，见
// study.md 旧「链接信号累积与状态判定」，已随状态机一起移除）。两种角色
// 现在都以 success=true 同等调用 activation.RecordOutcome。这不是把
// "背景引用和主证据同等看待"这件事简单地忽略了——旧区分要防的"仅凭零星
// 背景引用就获得完整信任"，现在由服务分档结构本身挡住：单靠自证证据
// （不管 role 是 direct 还是 supporting）最多把条件推到 self_graded 档，
// 要进最高档 trusted 必须额外经受与 role 完全无关的独立核实抽样考验
// （见 activation.md「服务分档」）。subject/intent/audience/constraint
// 是这次命中所归属的观测条件的四元组原文（不是查询原始输入，是条件本身
// 存储的值——字面问题捷径命中时两者可能不同，见 activation.md「owning
// condition 的可判定性」），Trace 用它定位 RecordOutcome 要更新的具体
// 条件，一并落盘供人工审计核对。
{ "link_id": "...", "point_id": "...",
  "subject": "...", "intent": "...", "audience": "...", "constraint": "...",
  "question_terms": "...", "match_score": 0.83,
  "cited_fact_ids": ["..."], "role": "direct | supporting" }

// activation_failure —— 每个命中但完全未被引用（既非 direct 也非
// supporting）的 link 一条事件；四元组字段含义同 activation_success
{ "link_id": "...", "point_id": "...",
  "subject": "...", "intent": "...", "audience": "...", "constraint": "...",
  "question_terms": "...", "match_score": 0.71,
  "reason": "not_cited | answer_gap | answer_error" }

// activation_audit_success —— 独立核实试验：快慢路径结论一致
// （2026-08-13 新增，见步骤 3b）。audited_trace_id 指向被抽样核实的
// 原始快路径 trace（本事件自身也挂在该 trace_id 下，此字段是让 payload
// 自解释，不强制要求额外 JOIN）；slow_path_direct_point_ids 是独立慢
// 路径跑出来的 direct_point_ids，供人工核对比对依据。
{ "link_id": "...", "point_id": "...",
  "subject": "...", "intent": "...", "audience": "...", "constraint": "...",
  "match_score": 0.83, "audited_trace_id": "...",
  "slow_path_direct_point_ids": ["..."], "agree": true }

// activation_audit_failure —— 独立核实试验：快慢路径结论不一致，
// 以慢路径结论为准（2026-08-13 新增，见步骤 3b）
{ "link_id": "...", "point_id": "...",
  "subject": "...", "intent": "...", "audience": "...", "constraint": "...",
  "match_score": 0.83, "audited_trace_id": "...",
  "slow_path_direct_point_ids": ["..."], "agree": false,
  "reason": "point_not_in_slow_path | slow_path_answer_differs" }

// activation_gap —— 每次问答最多一条事件
{ "question_terms": "...", "direct_point_ids": ["..."] }

// subject_synonym_gap —— 每次问答每个近似命中的 point 一条事件（2026-07-24 新增）
{ "point_id": "...", "link_id": "...", "query_subject": "...",
  "observed_subject": "...", "question_terms": "..." }
```

概念演化模块（顺序 10）启用后，`activation_gap` payload 增加 `gap_level` / `null_entry_ratio` 两个字段；判定规则与存量事件兼容策略见 `concept-evolution.md`「activation_gap payload 扩展」。本模块实现时无需预留。

## 实现步骤

### 步骤 1：质量分级与共现统计（沿用 MVP）

confident / partial / gap 分级规则、question_hash / question_terms 归一化、共现计数与去重逻辑全部不变。

`path_type=wiki` 的补充规则：Wiki 直答的 evidence_snapshot 结构为 `{ wiki_page_id, cited_point_ids }`（见 wiki.md 步骤 4），分级按 cited_point_ids 判定——非空 → confident 且 `direct_point_ids = cited_point_ids`；为空（citations 被白名单校验清空）→ partial。共现统计照常按 direct_point_ids 归集。

片段级证据的影响：证据挖掘后 citations 引用的是片段级 fact_id，但每个片段仍绑定 point_id（继承所属 KU 证据的 point_id，见 evidence.md），因此 `direct_point_ids` 的计算方式不变，精度自然提升——只有片段真正被引用的 KP 才计入。

**约束一致性判定（2026-07-21 新增）**：confident 分级前对每个被引用的直接证据 KP 追加一道确定性守门（不调用 LLM）。背景：问题约束指向的实体（如"神通数据库"）与证据来源（如《达梦数据库优化》）不同的问答仍可能被 rerank 判 direct 并被回答引用，若照常判 confident，学习信号会把跨实体的错误命中固化成 ActivationLink 条件（实测案例：神通问题的 constraint 混入达梦 KP 链接的白名单）。规则：

```text
适用条件：quality==confident 且 path_type != wiki 且 EvidenceSet.Constraint 非空；
问题侧：constraint 按 ，,、;； 拆分为独立约束项，每项 TermSet 分词；
证据侧：该 KP 所属 KU 的 unit_rerank_semantics
    （source_theme + content_theme + object + scope）合并 TermSet；
    语义行缺失 → 该 KP 跳过判定（无依据不误杀）；
冲突判定（单项 vs 单 KP）：约束项与证据词集"有共享词且有多出词"→ 冲突
    （同维度不同实体，如 神通数据库 vs 达梦语义：共享"数据库"、多出"神通"）；
    无共享词 → 正交约束（如 生产环境 / Windows环境），不冲突；
    完全被包含 → 一致，不冲突；
任一约束项与该 KP 冲突 → 该 KP 从 direct_point_ids 剔除；
剔除后 direct_point_ids 为空 → 降级 partial（不产生 confident 共现、
    不产生 activation_gap；命中该 KP 的 activation hit 自然落入
    activation_failure/not_cited，对污染链接形成降权信号）。
```

该判定只影响学习信号（分级与共现），不改变回答本身；"库里没有对应实体的资料"由此正确表现为无 confident 信号，而不是固化错误记忆。

`knowledge_gap` 事件 payload 的 `reason` 判定（quality==gap 时，写入事件前）：

```text
AnswerResult.Path == "error"        → "answer_error"
    （检索阶段本可能有证据，但 LLM 生成失败，不是真正的知识缺口）
EvidenceSet.GapReason != ""         → 原样取值（"no_candidates" 或 "judge_filtered"）
其余（理论边界情况，如 EvidenceSet == nil） → "unspecified"
```

### 步骤 2：写入 trace（扩展）

在 MVP 写入字段基础上，从 EvidenceSet 取新增列的值：

```text
path_type           = evidenceSet.path_type
activation_link_ids = activation_hits[].link_id 序列化
subject / intent / audience / constraint_text
                    = evidenceSet 对应字段原值（不归一化，保留展示形态；
                      归一化在消费侧进行——Study 创建链接时统一处理）
```

### 步骤 3：产生激活类事件

trace 写入后、共现更新前执行。判定全部基于本次 AnswerResult，纯程序计算；**评级逻辑本身（下面这段"谁记 direct、谁记 supporting、谁记 failure"）2026-08-13 未改动**——变的是评级出来之后多做一步：

```text
对 activation_hits 中的每条 (link_id, point_id, subject, intent,
audience, constraint, tier, audit_sampled)：

  该 point_id ∈ direct_point_ids（步骤 1 计算的"被引用的直接证据 KP"）
    → 写入 activation_success 事件，role="direct"（cited_fact_ids 取
      citations 中绑定该 point_id 的 fact_id）；
      同步调用 activation.RecordOutcome(link_id, subject, intent,
      audience, constraint, success=true, questionTerms=本轮
      question_terms, eventID=刚写入事件的 event_id)；

  否则，该 point_id 对应 EvidenceSet.Supporting 中实际被引用（未被
  citation 白名单剔除）的知识点
    → 写入 activation_success 事件，role="supporting"（cited_fact_ids
      同上，取 Supporting 侧 citations 中绑定该 point_id 的 fact_id）；
      同样同步调用 activation.RecordOutcome(..., success=true, ...)——
      role 只影响写进事件 payload 的标签，不影响传给 RecordOutcome 的
      success 取值，两种角色一视同仁记一次成功（理由见上方 payload
      注释「role 不再产生计数上的权重差异」）；

  否则（命中但完全未被引用，direct、supporting 均不含该 point_id）
    → 写入 activation_failure 事件，reason 按序判定：
      AnswerResult.path == "error"        → answer_error
      retrieval_quality == "gap"          → answer_gap
      其余（命中但回答未引用该 KP）        → not_cited
      同步调用 activation.RecordOutcome(link_id, subject, intent,
      audience, constraint, success=false, questionTerms=本轮
      question_terms, eventID=刚写入事件的 event_id)；

  RecordOutcome 调用失败（见 activation.md 步骤 1：定位不到归属条件时
  记 warn、不报错）不影响本次 trace_write 任务的其余步骤——学习信号
  更新失败不应该拖累共现统计、gap 判定等其余既有逻辑正常完成；

activation_gap 判定（activation_hits 为空时）：
  path_type == "full" 且 retrieval_quality == "confident"
    → 写入一条 activation_gap 事件（payload 含 question_terms 与
      direct_point_ids）——没有激活路径、但完整链路找到了被采用的知识，
      这是候选链接最直接的来源信号（Study 仍按既有的周期扫描消费这一
      事件类型创建新链接，见 study.md 步骤 2——这条不受本次置信度改写
      影响，activation_gap 回答的是"该不该开始追踪一个全新的组合"，不是
      "更新一个已在追踪的组合"，两者是不同的问题）；
  其余情况不产生 activation_gap（partial / gap 已由既有机制覆盖）。

observed_conditions enrichment（与 gap 并行，不写 learning_results）：
  path_type == "full" 且 retrieval_quality == "confident"
  且 direct_point_ids 非空：
    对每个 point 若已有非 deprecated ActivationLink →
    AppendObservedCondition（本轮 Session 四元组）；
    使创建/慢路径采用过的问法下次可 Match，无需等 Study。
    **与上面 RecordOutcome 的关系（2026-08-13 明确，避免误解为重复
    机制）**：两者都可能让某条条件的 success_count 增加，但触发条件和
    目的不同——RecordOutcome 只在这条条件本轮被 Match() 实际用于快路径
    时触发（"用过的路径给不给反馈"）；AppendObservedCondition/enrichment
    在慢路径 confident 命中时触发，不要求这条问题本轮经过 Match（"发现
    一个可能还没被追踪、或需要被追加进已有链接的四元组变体"）。慢路径
    永远不调用 Match()，所以这里不存在"重复记一次"的问题：同一次问答
    要么走快路径（触发 RecordOutcome，不触发 enrichment），要么走慢路径
    （触发 enrichment，不触发 RecordOutcome，因为 activation_hits 本来
    就是空的）。enrichment 命中已有条件时沿用既有的 MergeObservedConditions
    行为（该条件的 success_count 递增——字段改名前是 hit_count，逻辑
    未变）；命中的是一个全新四元组时，新条件以 success_count=1、
    failure_count=0 起步（初始 mean=(1+1)/(1+0+2)=0.667，比全零起步的
    0.5 略乐观——这一步观测本身就是一次 confident 慢路径确认，作为
    起步先验合理）。

subject 同义词近似检测（2026-07-24 新增，与上面 enrichment 同一触发条件，
  在 Append 之前执行；完整设计见
  docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md）：
  对该 point 已有的非 deprecated ActivationLink，逐组检查 observed_conditions：
    qi == cond.intent 且 qa == cond.audience 且 qc == cond.constraint，
    但同义词归一化后 subject 仍不满足 coreContained（本检测自身内联
    做这次同义词归一化比较，2026-08-12 改判后 `Matcher.SubjectOnlyMiss`
    不再借用 Match 的 `BuildQueryConditionTerms`——Match 本身已经不做
    子串/coreContained 判断、也不做同义词归一化，四字段一律精确相等，
    这里的 coreContained 判断纯粹是 Trace 侧诊断口径，不代表 Match 的
    实际行为）→ 视为一次"仅 subject 未过"的近似命中；
  存在至少一个这样的近似组时（取 hit_count 最高的一组作代表），写入
    learning_event(type="subject_synonym_gap", payload={
      "point_id", "link_id", "query_subject"（本轮归一化 subject）,
      "observed_subject"（该组归一化 subject 原文）, "question_terms" })；
  只读 activation_links，不写；与 AppendObservedCondition 互不影响，
    Append 照常执行。
  query_subject 与 observed_subject 归一化后逐字节相等时不产生该事件
  （矛盾状态的防御性排除，理论上不会触发）。

path_type == "wiki" 的问答不产生激活类事件（Wiki 直答不经过激活层，
见 wiki.md）；knowledge_gap / user_correction 事件规则不变。
```

**Bundle 命中的对称处理（2026-08-20 新增，`trace.generateBundleActivationEvents`）**：上面整段是 ActivationLink（`EvidenceSet.ActivationHits`）的判定逻辑；`EvidenceSet.BundleHits`（新增字段，镜像 `ActivationHits`，`docs/impl/v1/retrieval.md` 步骤 2）走一套并列但独立的判定，紧接在上面的 hits 循环之后执行，同样在 trace 写入后、共现更新前：

```text
对 bundle_hits 中的每条 (bundle_id, subject, intent, audience,
constraint, member_point_ids, match_score)：

  对 member_point_ids 中的每个 point_id，按上面同一份"是否被 direct/
  supporting 引用"判定（复用同一个 citedFactIDsByEvidence 结果，不重新
  计算）：
    被引用 → 调用 activation.RecordMemberOutcome(bundle_id, point_id,
      success=true)；
    未被引用 → 调用 activation.RecordMemberOutcome(bundle_id, point_id,
      success=false)；

  这条 bundle 本身的触发轴判定：member_point_ids 中只要有至少一个被引用
  （不是引用比例阈值，见 docs/impl/v1/activation-bundle.md 步骤 5
  2026-08-20 编注的理由）→ bundle_success=true；一个都没有 → false；

  写入事件：**不新增 bundle_success/bundle_failure 事件类型**，复用
  activation_success/activation_failure（event_type 相同，payload 换成
  bundle_id + member_point_ids，取代 link_id）；bundle_success=true 时
  写 activation_success，否则写 activation_failure（reason 判定与上面
  Link 的三段式 answer_error/answer_gap/not_cited 相同）；
  同步调用 activation.RecordBundleOutcome(bundle_id, subject, intent,
  audience, constraint, bundle_success)；

  RecordBundleOutcome/RecordMemberOutcome 调用失败同 RecordOutcome 的
  处理方式：记 warn，不中断 trace_write 其余步骤。

bundle_hits 为空时不产生任何事件、不触发 activation_gap——activation_gap
是 Link 侧"完整链路找到了被采用的知识但没有激活路径"这一信号，Bundle
是否命中不影响这个判定，两者不共享 gap 语义。
```

独立核实试验复用本模块既有的事件产生管线（learning_events 写入 + processed 标记），不新起一套队列或存储——这正是 `docs/design/activation-convergence.md` 第 4 节"打破自证循环"要求的机制，本质上只是给 Trace 新增一种"谁触发、什么时候触发、写什么 payload"的事件类型，架构上和 `activation_success`/`activation_failure` 完全对等。

```text
触发方：Retrieval（不是本模块）。见 retrieval.md 步骤 2——一次快路径
  命中若被 Match() 判定 audit_sampled=true（见 activation.md「服务
  分档」），Retrieval 在已经把快路径答案返回给用户之后，另起一次不
  阻塞任何用户请求的慢路径检索，跑出一份独立的 direct_point_ids；

本模块只负责：Retrieval 拿到这份独立慢路径结果后，调用 Trace 提供的
  写入函数（与 trace_write 现有的事件写入是同一段代码路径，只是触发
  时机不再是原始请求的 trace_write 任务窗口内，而是这次后台比对完成
  的时刻——两者共享"写 learning_events + 标记 processed"这段实现，
  不是两套并行代码）：

  比对规则（对每个被抽样核实的 (link_id, point_id)）：
    point_id ∈ 独立慢路径的 direct_point_ids
      → 写入 activation_audit_success 事件，agree=true；
        同步调用 activation.RecordAuditOutcome(link_id, subject,
        intent, audience, constraint, agree=true, eventID=刚写入
        事件的 event_id)；
    point_id ∉ 独立慢路径的 direct_point_ids
      → 写入 activation_audit_failure 事件，agree=false，
        reason="point_not_in_slow_path"；
        同步调用 activation.RecordAuditOutcome(..., agree=false, ...)；
    （V1 范围裁剪：比对粒度只到"这个 point 是否也出现在独立慢路径的
    direct_point_ids 里"，不逐字比较两份回答文本本身是否说的是同一件
    事——那需要额外一次 LLM 判断，且"证据来源是否一致"已经是"结论是否
    可信"的一个足够强的代理指标，见 activation-convergence.md 第 4 节
    "两边独立算出的结果做对比"；`slow_path_answer_differs` 这个 reason
    枚举值为将来升级到文本级比对预留，V1 不产生）；

  subject/intent/audience/constraint 取自原始快路径命中时 Match()
  返回的归属条件四元组（与被审计的 activation_success/failure 事件
  一致，不是独立慢路径重新解析出来的——独立慢路径本身不经过 Match()，
  没有"归属条件"这个概念，四元组身份以快路径那次的归属为准）；

RecordAuditOutcome 调用失败的处理同 RecordOutcome（记 warn、不报错，
  不阻断比对流程的其余部分）；

这套事件不影响原始快路径答案——用户已经拿到答案，独立核实只影响这条
  观测条件未来的 mean/tier，不会让已经发出的答案被追加、撤回或提示。
```

> **熟路指针（2026-08-11 新增，2026-08-12 path_type 定案）**：`docs/impl/v1/activation-bundle.md`
> 步骤 5 给出的契约是命中熟路（ActivationBundle）后产生独立的 `bundle_success` /
> `bundle_failure` 事件——判定逻辑与上面的 `activation_success`/`activation_failure`
> 同构（比对的是"稳定核成员本次是否被实际引用"，而不是单个 point 是否被引用），
> 但对象、payload、消费方（Study 对 bundle_id 的窗口统计）都是独立的一套，不复用
> 也不覆盖上面这套事件；两者会在同一次问答里并存产生（一次命中熟路的问答，其
> 稳定核成员各自仍可能有对应的 ActivationLink，两套事件互不排斥）。
>
> **命中熟路的问答不新增 path_type 取值，复用 `path_type=fast`，靠证据里是否带
> `bundle_hits[]` 区分来源（2026-08-12 定案，2026-08-12 字段形状随 EvidenceSet
> 一并定案）**：`path_type` 回答的是"这次命中经过了几层过滤"（Wiki 直答 / 激活层
> / 完整链路），熟路命中在这个维度上和单链接命中没有区别——都跳过召回+rerank、
> 都经 `fast_path_verify` 把关——真正不同的只是"证据来自一条链接还是一组"，这件
> 事交给独立字段表达更准确，不该膨胀 path_type 本身的取值域。想单独看熟路的表现
> （例如 fast_path_rate 拆分），按 `bundle_hits[]` 是否非空过滤即可，不需要为此
> 新增枚举值。`bundle_hits[]` 的字段形状（`{ bundle_id, member_point_ids[],
> match_score, matched_by }`，独立于 `activation_hits[]`，不合并）已在 retrieval.md
> 步骤 1「EvidenceSet 契约扩展」定案，本文档的事件生成逻辑本次不改动——`bundle_success`/
> `bundle_failure` 事件的实际产生仍是「阶段 2」范围，见 activation-bundle.md 步骤 5
> 「Trace 信号回写契约」。

同一次问答可产生多条 activation_success / activation_failure（多链接命中），事件均关联同一 trace_id。

`repeated_success` / `repeated_failure` 不是事件类型：它们是 Study 扫描时对同一 link_id 事件的累积判定（见 study.md 步骤 3），Trace 不做窗口统计。

### 步骤 4：用户反馈处理（扩展）

`POST /traces/:id/feedback` 逻辑沿用 MVP，一处扩展：

```text
type=negative 或 correction，且该 trace 的 activation_link_ids 非空：
  user_correction 事件的 payload 增加 "link_ids": [...]；
  2026-08-13 起，本步骤在写入 user_correction 事件的同一次处理内，对
  每个 link_id 直接调用 study.correction_weight 次
  activation.RecordOutcome(link_id, subject, intent, audience,
  constraint, success=false, questionTerms="", eventID=刚写入事件的
  event_id)——四元组取该 trace 自己存储的 subject/intent/audience/
  constraint_text（步骤 2 写入的那份，即本次问答的查询四元组）；不再
  经 Study 中转"提高累积失败权重"（旧机制见 study.md 已移除的「链接
  信号累积与状态判定」）。取查询四元组而不是精确重查当时 Match() 命中
  的归属条件，是一个已知的近似：多数情况下二者相同，只有经字面问题
  捷径命中的极少数场景可能不同（见 activation.md「owning condition 的
  可判定性」）；user_correction 是低频、人工触发的路径，RecordOutcome
  定位不到条件时的既定行为（记 warn、不写入，见 activation.md 步骤 1）
  已经能优雅处理这种不匹配，不必为这个次要路径单独新增一条精确归属的
  存储字段。
```

### 步骤 5：HTTP API 扩展

```text
GET /traces、GET /traces/:id
  响应增加 path_type、activation_link_ids 字段；
  列表接口增加查询参数 path_type（fast / full / wiki 过滤）。

GET /learning-events
  type 参数支持新增的激活事件类型（activation_success / activation_failure /
  activation_gap 三种，加上 2026-08-13 新增的 activation_audit_success /
  activation_audit_failure 两种，共五种；接口本身不变）。
```

## 依赖

```text
基础设施：SQLite（migration）、异步任务队列（trace_write，沿用）
Retrieval：EvidenceSet 新增 path_type / activation_hits / gap_reason /
           filtered_evidence 字段（见 retrieval.md）
Answer：   AnswerResult 原样传递扩展后的 EvidenceSet，Answer 自身无逻辑改动
Study：    消费新增事件类型收窄为 activation_gap（创建候选链接，见 study.md
           步骤 2）与 subject_synonym_gap（诊断聚合，见 study.md 步骤 2a）；
           不再消费 activation_success / activation_failure /
           activation_audit_success / activation_audit_failure 做计数或
           状态判定（2026-08-13 起这三对事件在产生的同一步已经直接更新
           完了置信度，Study 的新职责——收敛趋势报告、收敛剪枝——改为直接
           读 activation_links 当前状态，见 study.md）
Activation：**2026-08-13 起直接写**——步骤 3/3b/4 调用
            activation.RecordOutcome / RecordAuditOutcome 更新观测条件的
            success_count / failure_count / audited_*（不再是"只读做近似
            检测比较、不改写该表"）；近似检测（subject_synonym_gap，见
            上方「subject 同义词近似检测」）仍是只读比较，与本条新增的
            写入调用是两件独立的事——前者读 observed_conditions 判断
            "这条问法是不是只差 subject 没对上"，后者写 success_count/
            failure_count，互不影响；仍只记录 link_id 到
            traces.activation_link_ids（这部分不变）
Retrieval：审计事件的触发方（步骤 3b）——Retrieval 完成独立核实比对后
            调用本模块的写入函数；本模块自身不判断"要不要审计"、不跑
            慢路径检索，只负责把 Retrieval 已经算出的比对结果落成事件
            并调用 RecordAuditOutcome，见 retrieval.md 步骤 2
```

## 完成标准

```text
migration 后存量 traces 行 path_type=full、activation_link_ids=[]，行为兼容；
fast 路径问答：命中且被引用的链接产生 activation_success，
              命中未引用的产生 activation_failure（reason 正确）；
              两者均同步调用 activation.RecordOutcome，成功/失败方向
              正确、四元组定位正确（测试用例：断言调用后目标条件的
              success_count/failure_count 按预期变化）；
              role=supporting 与 role=direct 均以 success=true 调用
              RecordOutcome（不再有权重差异，见步骤 3 payload 注释）；
full 路径 confident 问答产生一条 activation_gap，其余组合不产生；
  activation_gap 不触发 RecordOutcome（该事件仍走 Study 周期扫描创建
  候选链接的既有路径，不受本次改写影响）；
enrichment（AppendObservedCondition）与 RecordOutcome 互斥触发（同一次
  问答要么走快路径触发前者、要么走慢路径触发后者，不会同一次问答两者
  都触发或都不触发对同一 point 的计数更新，除非该 point 完全没有对应
  ActivationLink）；
独立核实（步骤 3b）：Retrieval 判定 audit_sampled=true 的命中，比对完成
  后正确产生 activation_audit_success（agree=true）或
  activation_audit_failure（agree=false，reason=point_not_in_slow_path）；
  同步调用 activation.RecordAuditOutcome，且该调用总是同时递增
  success_count/failure_count 与对应的 audited_success_count/
  audited_failure_count（测试用例断言两对计数同方向变化）；
  audit_sampled=false 的命中不产生这两类事件；
wiki 路径不产生激活类事件（含 audit 类，Wiki 直答不经过激活层，
  audit_sampled 字段对 wiki 路径恒不适用）；
一次问答多链接命中时事件逐条产生且 trace_id 一致；
user_correction 对 fast 路径问答携带 link_ids，且按 study.correction_weight
  次数调用 RecordOutcome(success=false)（测试用例：correction_weight=2
  时，一次 correction 反馈使目标条件 failure_count 恰好 +2）；
共现统计行为与 MVP 完全一致（片段级 citations 不改变 point_id 归集逻辑）；
knowledge_gap payload.reason 按 Path==error → answer_error、
  GapReason 非空 → 原样取值、其余 → unspecified 的顺序正确判定；
subject_synonym_gap：intent/audience/constraint 全同、subject 因同义词未注册
  而未过 coreContained 时产生该事件，且不影响 AppendObservedCondition 照常执行；
  query_subject 与 observed_subject 归一化后相等时不产生该事件；
RecordOutcome/RecordAuditOutcome 调用失败（fake 环境注入定位不到条件的
  场景）不中断 trace_write 任务的其余步骤（共现统计、gap 判定等照常完成）；
fake 队列与 fake activation 依赖下，快路径成功/失败、独立核实一致/不
  一致、user_correction 加权、enrichment 与 RecordOutcome 互斥触发等
  全部事件产生路径测试稳定运行。
```
