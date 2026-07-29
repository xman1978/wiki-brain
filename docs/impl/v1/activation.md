# ActivationLink 实现路径（V1）

## 职责

ActivationLink 是「问题条件 → KnowledgePoint」的正式激活路径，V1 的核心新增对象。本模块提供：数据模型与存储、状态机与迁移约束、激活条件匹配器（供检索激活层调用）、人工确认 API。

状态迁移只由 Study 执行（见 `study.md`），检索只读；本模块定义迁移的合法性约束，供 Study 调用统一接口执行。

## 数据结构

```sql
CREATE TABLE activation_links (
    link_id           TEXT PRIMARY KEY,
    question_terms    TEXT NOT NULL,
    -- 创建/最近一次刷新时使用的代表性问法（归一化问题关键词，排序后空格拼接，
    -- 生成规则与 traces.question_terms 完全一致）；仅用于展示与回退匹配
    -- （见步骤 2），不再是去重键（去重键是 point_id，见下方 UNIQUE 约束）
    subject_terms     TEXT NOT NULL DEFAULT '',
    -- 兼容投影：最新一组 observed_conditions 的 Terms(subject)；不再参与 Match
    intent_terms      TEXT NOT NULL DEFAULT '[]',
    -- 兼容投影：最新一组的单元素数组；不再作并集白名单
    audience          TEXT NOT NULL DEFAULT '[]',
    constraint_terms  TEXT NOT NULL DEFAULT '[]',
    observed_conditions TEXT NOT NULL DEFAULT '[]',
    -- Match 唯一真相源：JSON 数组，元素为观测四元组
    -- {subject,intent,audience,constraint,question_terms,first_seen_at,last_seen_at,hit_count}
    -- 组内四门全过才算该组命中，组间 OR（见步骤 2）
    scene             TEXT NOT NULL DEFAULT '',
    goal              TEXT NOT NULL DEFAULT '',
    -- V1 不写入，预留 V2 认知化字段（触发条件 / 认知条件，见 docs/impl/v2/readme.md）
    point_id          TEXT NOT NULL REFERENCES knowledge_points(point_id),
    status            TEXT NOT NULL DEFAULT 'candidate',
    -- candidate / verified / weakened / deprecated（conflicted 预留枚举，V1 不产生）
    adopt_count       INTEGER NOT NULL DEFAULT 0,
    -- 累计 activation_success 次数（Study 更新）
    fail_count        INTEGER NOT NULL DEFAULT 0,
    -- 累计 activation_failure 次数（Study 更新）
    last_used_at      DATETIME,
    -- 最近一次被激活层命中的时间（Retrieval 异步更新，不阻塞请求）
    created_from      TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：创建该链接所依据的 learning_event event_id 列表
    -- （与 learning_results.event_ids 同源；不再写入 link_candidate id）
    status_changed_at DATETIME,
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_al_status ON activation_links(status);
CREATE UNIQUE INDEX idx_al_point_id ON activation_links(point_id);
-- 每个 point 至多一条链接（同一 KP 被不同问法反复命中时刷新已有链接的条件，
-- 不创建新链接，见 study.md 步骤 2 tryCreateLink）
```

与 MVP `link_candidates` 表的关系：link_candidates 保留，仍作为 Study 共现扫描的暂存快照；Study 从达标的 link_candidates 创建或刷新 activation_links（新建时 status=candidate），创建后该 candidate 行保留（用于报告展示），不迁移不删除。activation_links 才是参与系统行为的正式对象。

### 附属表：subject_synonyms（2026-07-24 新增）

```sql
CREATE TABLE subject_synonyms (
    synonym_id   TEXT PRIMARY KEY,
    domain_id    TEXT REFERENCES domains(domain_id),
    term         TEXT NOT NULL,       -- 归一化后的原始措辞（短语级，不分词）
    canonical    TEXT NOT NULL,       -- 归一化后收敛到的规范措辞
    source       TEXT NOT NULL DEFAULT 'manual',  -- preset / gap_mined / manual
    status       TEXT NOT NULL DEFAULT 'active',  -- active / candidate / rejected
    created_from TEXT NOT NULL DEFAULT '[]',
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX idx_subject_synonyms_term_active ON subject_synonyms(term) WHERE status = 'active';
CREATE INDEX idx_subject_synonyms_status ON subject_synonyms(status);
```

只归一化 subject 一个维度，intent/audience/constraint 的精确匹配语义不变。`source=preset` 的行来自 `preset/domains.json` 每个 concept 的 `aliases` 字段（启动时随 domains/concepts 一并 UPSERT，`status=active` 无需确认）；`source=gap_mined` 的行来自 Study 对 `subject_synonym_gap` 学习事件的聚合，默认 `status=candidate`，需人工 confirm 才生效（`study.synonym_auto_promote=true` 时直接 active）。设计动机、挖掘触发条件、Match 算法调整详见 `docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md`；`domain_id` 列 V1 仅作展示，Match 不按 domain 过滤。

## 状态机

```text
                 ┌────────────┐  人工确认（或 auto_promote）
  Study 创建 ──▶ │ candidate  │ ─────────────────────────▶ verified
                 └────────────┘                              │
                       │ 人工驳回 / 长期无信号                │ repeated_failure
                       ▼                                     ▼
                  deprecated ◀────── 长期无有效使用 ────── weakened
                                                             │ repeated_success（重新验证）
                                                             ▼
                                                          verified
```

**合法迁移表**（唯一入口 `TransitionLink(linkID, to, reason, eventIDs)`，非法迁移返回错误）：

```text
candidate  → verified     Study 晋升（默认需人工确认，见 study.md）
candidate  → deprecated   人工驳回，或超过 study.candidate_idle_days 无新信号
verified   → weakened     Study 降权（repeated_failure）
weakened   → verified     Study 重新验证（降权后再次 repeated_success）
weakened   → deprecated   Study 淘汰（超过 study.deprecate_idle_days 无有效使用）
deprecated → （终态，不可迁出；同条件新链接由 Study 重新创建）
```

每次迁移必须携带 reason 和支撑事件，由 `TransitionLink` 统一写入 `learning_results`（见 study.md 数据结构）并更新 `status_changed_at`。单次事件不触发迁移——迁移条件的累积判定在 Study 中实现，本模块只校验迁移合法性。

## 实现步骤

### 步骤 1：存储与内部接口

```text
CreateLink(questionTerms, cond LinkCondition, pointID, createdFrom) → link
  status=candidate；LinkCondition = { subject_terms, intent_terms,
  audience, constraint_terms }，由 Study 的 computeLinkCondition 从该 point
  全部确证信号归纳生成（见 study.md 步骤 2）；(question_terms, point_id)
  UNIQUE 已改为 point_id UNIQUE，冲突时返回已存在链接（幂等），若已存在链接
  为 deprecated 则拒绝创建（同 point 被淘汰过，需 Study 依据新累积信号显式
  复活：deprecated 链接保持不动，拒绝原因写入日志，防止候选被反复自动重建）；
UpdateConditions(linkID, cond)：Study 刷新已有链接的条件（同一归纳算子，
  见 study.md 步骤 2），非状态迁移，不写 learning_results；
TransitionLink(linkID, to, reason, eventIDs)：校验合法迁移表后执行；
UpdateStats(linkID, adoptDelta, failDelta)：Study 累积计数；
TouchLastUsed(linkIDs)：Retrieval 命中后异步更新 last_used_at。
```

### 步骤 2：激活条件匹配器

供检索激活层调用，纯程序计算，不调用 LLM。

**输入基准**：匹配输入是 Session 产出的 `ExpandedQuery`（standalone 补全后的 expanded_question + 主题/意图/对象/约束四元组，见 mvp session.md），不是用户原始输入。省略式追问（"漠河呢"）的原始输入不含完整词项，必须用补全后的问题匹配。链接创建侧的 traces.question 记录的同样是 expanded_question（Page 传给 POST /answer 的问题），两侧文本基准一致。

**匹配语义：观测条件组（组内精确、组间 OR）**。激活链接是"精确命中的缓存"：每组是历史上一起出现过的 `(subject,intent,audience,constraint)`；禁止跨问法并集交叉拼接。详见 `docs/superpowers/specs/2026-07-22-activation-observed-conditions-design.md`。

```text
Match(query ExpandedQuery) → []LinkMatch{link, score}

预处理：
  queryTopic = Normalize(query.subject) + " " + Normalize(query.intent)
  Qi / Qa / Qc / Qq = 与组内字段同一归一化规则

候选加载：status ∈ {verified, candidate} 且 KP lifecycle=current（缓存不变）

主匹配（observed_conditions 非空）：
  任一组：Terms(subject) 各词 ⊆ queryTopic（子串）
         且 Qi==intent 且 Qa==audience 且 Qc==constraint → 命中 score=1.0

回退（observed_conditions 为空）：
  仅当从未观测非空 audience/constraint 时，Qq == question_terms 才命中

排序：按 last_used_at，截断 activation_match_top
```

verified 链接数量在 V1 规模下（预计 <10^4）全量内存匹配足够；不建 Bleve 索引。

**Subject 同义词归一化（2026-07-24 新增）**：`queryTopic` 与各组 `cond.Subject` 在参与 `coreContained` 比较前，先经 `SynonymResolver.Canonicalize` 做一次短语级替换（词表来自 `subject_synonyms` 表 `status=active` 的行，preset 别名 + 人工确认过的挖掘候选）。回退分支（`observed_conditions` 为空时的 `question_terms` 逐字节相等）不经过 resolver，保持最保守的兜底语义。intent/audience/constraint 的精确匹配不受影响。详见 `docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md`。

### 步骤 3a：同义词候选确认 API（2026-07-24 新增）

与步骤 3 的 confirm/reject 同构，作用对象是 `subject_synonyms` 而非 `activation_links`：

```text
GET    /subject-synonyms
  查询参数：status、limit（默认 50）、offset
  响应：[{ synonym_id, domain_id, term, canonical, source, status, created_at, updated_at }]

GET    /subject-synonyms/:id
  响应：完整字段 + created_from 关联的问法列表

POST   /subject-synonyms/:id/confirm
  仅对 status=candidate 生效；status → active；写 learning_results
  (action=synonym_candidate 对应行 status=applied)；调用 Matcher.InvalidateCache
  使新映射立即生效。

POST   /subject-synonyms/:id/reject
  仅对 status=candidate 生效；status → rejected；不自动复活。
```

候选来源（Study 聚合 `subject_synonym_gap` 事件产生 `pending_confirm`）见 `study.md`；挖掘触发条件见 `trace.md` 步骤 3；完整设计见 `docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md`。

### 步骤 3：人工确认 API

```text
GET    /activation-links
  查询参数：status、point_id、limit（默认 50）、offset
  响应：[{ link_id, question_terms, point_id, point_summary, unit_center,
           status, adopt_count, fail_count, last_used_at, created_at }]
  （point_summary / unit_center 由 JOIN knowledge_points / knowledge_units 补充）

GET    /activation-links/:id
  响应：完整字段 + created_from + pending_promote_reason（若有待确认晋升）；
        不含 learning_results（改走下方懒加载接口）

GET    /activation-links/:id/questions
  响应：{ matched: [{question, trace_id, created_at, path_type, retrieval_quality}],
          created_from: [同上] }
  matched：traces.activation_link_ids 含本 link 的问答原文；
  created_from：链接 created_from 事件关联的 traces 原文（创建燃料）

GET    /activation-links/:id/learning-results
  响应：[{ result_id, action, status, reason, created_at, ... }]
  状态迁移时间线，供详情对话框折叠区按需加载

POST   /activation-links/:id/confirm
  仅对 status=candidate 生效；执行 TransitionLink(candidate → verified,
  reason="manual_confirm", 关联 pending 的 learning_result)；
  由 Page 的 ActivationLink 管理视图调用（见 page.md）。
  响应：{ link_id, status: "verified" }

POST   /activation-links/:id/reject
  仅对 status=candidate 生效；TransitionLink(candidate → deprecated,
  reason="manual_reject")。
  响应：{ link_id, status: "deprecated" }
```

确认/驳回与 Study 的关系：Study 晋升判定达标后生成 `pending_confirm` 的 learning_result（不改链接状态）；人工 confirm 时完成实际迁移并将该 result 置为 applied（见 study.md 步骤 5）。`study.auto_promote=true` 时 Study 直接迁移，不产生 pending。

## 依赖

```text
基础设施：SQLite（migration）、结构化日志、HTTP 框架
Trace：   复用问题归一化 / 分词 / 停用词代码（提取为 foundation 层共享包，
          避免 Trace 与 Activation 两份实现漂移）；subject_synonym_gap 学习
          事件的产生方（本模块不产生，只消费其聚合结果，经 Study 中转）
Lifecycle：匹配器 JOIN knowledge_points.lifecycle 过滤
Study：   状态迁移与计数的唯一调用方（本模块提供接口，不自主迁移）；
          subject_synonyms 候选生成的唯一调用方（本模块提供 CRUD/confirm/reject，
          Study 只负责聚合 subject_synonym_gap 事件并写候选行）
Foundation preset：LoadPresetData 解析 domains.json concept.aliases 写入
          subject_synonyms（source=preset），本模块只读该表
```

## 完成标准

```text
migration 建表成功，UNIQUE(point_id) 约束生效（每个 point 至多一条链接）；
CreateLink 幂等；对 deprecated 同 point 链接拒绝重建并记录日志；
TransitionLink 拒绝迁移表之外的一切迁移（含 deprecated 迁出）；
每次迁移写入 learning_results 且 status_changed_at 更新；
匹配器：输入为 ExpandedQuery；
        subject 用 overlap（linkCore ⊆ Qs）而非全等，交集越短越容易命中，
        不是越难命中（测试用例：链接 subject_terms 是问题 subject 的真子集
        时命中；链接 subject_terms 含问题里没有的词时不命中）；
        intent/audience/constraint 用集合成员判断，同一 point 两条不同
        确证 trace 贡献的不同 audience 值都应能命中该链接；
        任一维度不命中即不命中，即使其余三维全同且词项高度重合
        （测试用例：链接约束集合 {"产品A"}、问题约束"产品B" → 不命中）；
        subject 任一侧全空时走回退：链接从未观测到非空 audience/constraint
        且 question_terms 逐字节相等才命中；
        weakened / deprecated 链接及指向非 current KP 的链接不参与匹配；
        candidate 参与 Match 记信号，但不进入快路径直答（见 retrieval.md）；
条件刷新：已有链接的 point 出现新确证信号时，computeLinkCondition 重新
        归纳并通过 UpdateConditions 写回（不产生新链接、不写 learning_results，
        见 study.md 步骤 2）；
缓存失效：链接状态变更或 KP lifecycle 变更后，下一次 Match 反映新状态；
confirm / reject API 正确迁移状态并联动 learning_results；
fake 环境下全部状态机路径、匹配路径、条件刷新路径测试稳定运行；
subject_synonyms：preset alias 正确加载为 active 行，重跑 preset 刷新 canonical
  不新增重复行、不覆盖 gap_mined 行；Match 中 subject 比较经 SynonymResolver
  归一化命中同义措辞，intent/audience/constraint 精确匹配不受影响；
  confirm/reject 正确迁移状态并触发 Matcher.InvalidateCache。
```
