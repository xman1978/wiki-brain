# Trace 实现路径（V1 扩展）

## 职责

在 MVP Trace（质量分级、共现统计、gap/user_correction 事件）之上，增加激活类 Learning Event 的自动产生：每次问答记录 ActivationLink 命中与采用情况，产出 `activation_success` / `activation_failure` / `activation_gap` 事件，作为 Study 学习动作的燃料。

V1 Trace 仍不调用 LLM、不阻塞回答，沿用 `trace_write` 异步队列。

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
```

traces.question 记录的是 expanded_question（standalone 补全后的问题，Page 传给 POST /answer 的值），question_terms 由其归一化生成——与激活匹配器的输入基准一致（见 activation.md 步骤 2）。

### 上游契约扩展

EvidenceSet 增加字段（Retrieval 产出，经 AnswerResult 原样传递给 Trace）：

```text
EvidenceSet 新增：
  path_type          fast / full / wiki
  activation_hits[]  [{ link_id, point_id, match_score }]
                     激活层命中的链接及其目标 KP（full 路径为空数组）
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
```

各类型 payload 结构：

```json
// knowledge_gap —— 检索质量为 gap 时产生（MVP 既有事件，V1 payload 扩展）
{ "question": "...", "reason": "no_candidates | judge_filtered | answer_error | unspecified" }

// activation_success —— 每个满足条件的 link 一条事件
{ "link_id": "...", "point_id": "...", "question_terms": "...",
  "match_score": 0.83, "cited_fact_ids": ["..."] }

// activation_failure —— 每个命中但未生效的 link 一条事件
{ "link_id": "...", "point_id": "...", "question_terms": "...",
  "match_score": 0.71, "reason": "not_cited | answer_gap | answer_error" }

// activation_gap —— 每次问答最多一条事件
{ "question_terms": "...", "direct_point_ids": ["..."] }
```

概念演化模块（顺序 10）启用后，`activation_gap` payload 增加 `gap_level` / `null_concept_ratio` 两个字段；判定规则与存量事件兼容策略见 `concept-evolution.md`「activation_gap payload 扩展」。本模块实现时无需预留。

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

trace 写入后、共现更新前执行。判定全部基于本次 AnswerResult，纯程序计算：

```text
对 activation_hits 中的每条 (link_id, point_id)：

  该 point_id ∈ direct_point_ids（步骤 1 计算的"被引用的直接证据 KP"）
    → 写入 activation_success 事件（cited_fact_ids 取 citations 中
      绑定该 point_id 的 fact_id）；

  否则 → 写入 activation_failure 事件，reason 按序判定：
      AnswerResult.path == "error"        → answer_error
      retrieval_quality == "gap"          → answer_gap
      其余（命中但回答未引用该 KP）        → not_cited

activation_gap 判定（activation_hits 为空时）：
  path_type == "full" 且 retrieval_quality == "confident"
    → 写入一条 activation_gap 事件（payload 含 question_terms 与
      direct_point_ids）——没有激活路径、但完整链路找到了被采用的知识，
      这是 candidate 链接最直接的来源信号；
  其余情况不产生 activation_gap（partial / gap 已由既有机制覆盖）。

path_type == "wiki" 的问答不产生激活类事件（Wiki 直答不经过激活层，
见 wiki.md）；knowledge_gap / user_correction 事件规则不变。
```

同一次问答可产生多条 activation_success / activation_failure（多链接命中），事件均关联同一 trace_id。

`repeated_success` / `repeated_failure` 不是事件类型：它们是 Study 扫描时对同一 link_id 事件的累积判定（见 study.md 步骤 3），Trace 不做窗口统计。

### 步骤 4：用户反馈处理（扩展）

`POST /traces/:id/feedback` 逻辑沿用 MVP，一处扩展：

```text
type=negative 或 correction，且该 trace 的 activation_link_ids 非空：
  user_correction 事件的 payload 增加 "link_ids": [...]，
  供 Study 将纠正信号定向关联到具体链接（提高其累积失败权重，
  见 study.md 步骤 3），而不是仅作用于全局。
```

### 步骤 5：HTTP API 扩展

```text
GET /traces、GET /traces/:id
  响应增加 path_type、activation_link_ids 字段；
  列表接口增加查询参数 path_type（fast / full / wiki 过滤）。

GET /learning-events
  type 参数支持新增的三种激活事件类型（接口本身不变）。
```

## 依赖

```text
基础设施：SQLite（migration）、异步任务队列（trace_write，沿用）
Retrieval：EvidenceSet 新增 path_type / activation_hits / gap_reason /
           filtered_evidence 字段（见 retrieval.md）
Answer：   AnswerResult 原样传递扩展后的 EvidenceSet，Answer 自身无逻辑改动
Study：    消费新增事件类型（只读，经 learning_events.processed 标记）
Activation：不直接依赖——Trace 只记录 link_id，不读写 activation_links 表
```

## 完成标准

```text
migration 后存量 traces 行 path_type=full、activation_link_ids=[]，行为兼容；
fast 路径问答：命中且被引用的链接产生 activation_success，
              命中未引用的产生 activation_failure（reason 正确）；
full 路径 confident 问答产生一条 activation_gap，其余组合不产生；
wiki 路径不产生激活类事件；
一次问答多链接命中时事件逐条产生且 trace_id 一致；
user_correction 对 fast 路径问答携带 link_ids；
共现统计行为与 MVP 完全一致（片段级 citations 不改变 point_id 归集逻辑）；
knowledge_gap payload.reason 按 Path==error → answer_error、
  GapReason 非空 → 原样取值、其余 → unspecified 的顺序正确判定；
fake 队列下全部事件产生路径测试稳定运行。
```
