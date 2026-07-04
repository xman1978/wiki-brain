# ActivationLink 实现路径（V1）

## 职责

ActivationLink 是「问题条件 → KnowledgePoint」的正式激活路径，V1 的核心新增对象。本模块提供：数据模型与存储、状态机与迁移约束、激活条件匹配器（供检索激活层调用）、人工确认 API。

状态迁移只由 Study 执行（见 `study.md`），检索只读；本模块定义迁移的合法性约束，供 Study 调用统一接口执行。

## 数据结构

```sql
CREATE TABLE activation_links (
    link_id           TEXT PRIMARY KEY,
    question_terms    TEXT NOT NULL,
    -- 归一化问题关键词（排序后空格拼接），生成规则与 traces.question_terms
    -- 完全一致（复用共享归一化包）；用于展示与回退匹配（见步骤 2）
    subject_terms     TEXT NOT NULL DEFAULT '',
    -- 激活条件主字段：subject（核心主题）归一化分词后排序拼接
    intent_terms      TEXT NOT NULL DEFAULT '',
    -- 激活条件次字段：intent（意图）同规则处理
    audience          TEXT NOT NULL DEFAULT '',
    -- 对象守门字段：audience 归一化原文（小写、去标点、去空白），不分词；
    -- 空 = 该知识不限定角色
    constraint_terms  TEXT NOT NULL DEFAULT '',
    -- 约束守门字段：constraint 归一化分词后排序拼接；空 = 不限定场景
    -- 以上四字段来自触发链接创建的 confident trace 的四元组（见 study.md 步骤 2）；
    -- 四元组由 Session 解析产出（session.md），经 EvidenceSet 列化进 traces（trace.md）
    scene             TEXT NOT NULL DEFAULT '',
    goal              TEXT NOT NULL DEFAULT '',
    -- V1 不写入，预留 V2 认知路由上下文字段
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
    -- JSON 数组：创建该链接所依据的 learning_event / link_candidate 标识
    status_changed_at DATETIME,
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(question_terms, point_id)
);

CREATE INDEX idx_al_status   ON activation_links(status);
CREATE INDEX idx_al_point_id ON activation_links(point_id);
```

与 MVP `link_candidates` 表的关系：link_candidates 保留，仍作为 Study 共现扫描的暂存快照；Study 从达标的 link_candidates 创建 activation_links（status=candidate），创建后该 candidate 行保留（用于报告展示），不迁移不删除。activation_links 才是参与系统行为的正式对象。

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
  audience, constraint_terms }，由 Study 从触发 trace 的四元组归一化生成；
  UNIQUE 冲突时返回已存在链接（幂等），若已存在链接为 deprecated 则拒绝创建
  （同条件被淘汰过，需 Study 依据新累积信号显式复活：deprecated 链接保持不动，
   拒绝原因写入日志，防止候选被反复自动重建）；
TransitionLink(linkID, to, reason, eventIDs)：校验合法迁移表后执行；
UpdateStats(linkID, adoptDelta, failDelta)：Study 累积计数；
TouchLastUsed(linkIDs)：Retrieval 命中后异步更新 last_used_at。
```

### 步骤 2：激活条件匹配器

供检索激活层调用，纯程序计算，不调用 LLM。

**输入基准**：匹配输入是 Session 产出的 `ExpandedQuery`（standalone 补全后的 expanded_question + 主题/意图/对象/约束四元组，见 mvp session.md），不是用户原始输入。省略式追问（"漠河呢"）的原始输入不含完整词项，必须用补全后的问题匹配。链接创建侧的 traces.question 记录的同样是 expanded_question（Page 传给 POST /answer 的问题），两侧文本基准一致。

```text
Match(query ExpandedQuery) → []LinkMatch{link, score}

预处理：
  对 query.subject / intent / constraint 分别做归一化+分词+停用词过滤
  （复用共享归一化包），得到词项集合 Qs / Qi / Qc；
  对 query.audience 只做归一化（小写、去标点、去空白），得到 Qa；
  对 expanded_question 同规则处理得到 Qq（供回退匹配）。

候选加载：
  全部 status=verified 且目标 KP lifecycle=current 的链接
  （JOIN knowledge_points 过滤；结果缓存内存，activation_links 或
   lifecycle 变更时失效重载——两处变更入口分别调用 InvalidateCache）。

第一阶段：硬性守门（逐链接，任一不过即排除，不参与计分）
  audience 守门：
    链接 audience 为空                → 通过（知识不限定角色）
    链接非空 且 Qa 为空               → 排除（知识限定了角色，问题未落到
                                        该角色，宁走慢路径不错答）
    双方非空                          → 归一化后相等才通过
  constraint 守门：
    链接 constraint_terms 为空        → 通过
    链接非空 且 Qc 为空               → 排除
    双方非空                          → 链接 constraint_terms ⊆ Qc 才通过
  守门方向不对称：链接限定的场景问题必须覆盖；问题多出的限定不拦截
  （多出的限定由回答质量兜底，错配会以 activation_failure 回流学习）。
  守门规则与 Rerank Prompt 对 audience / constraint 的判定语义对齐——
  快路径跳过 Rerank，这两个维度的把关必须在匹配层完成。

第二阶段：计分（守门通过的链接）
  s_subject = |Qs ∩ Ls| / |Ls|        （Ls = 链接 subject_terms 词项集合）
  s_intent  = |Qi ∩ Li| / |Li|        （Li 为空时 s_intent = 1.0）
  score     = 0.7 × s_subject + 0.3 × s_intent
  score ≥ retrieval.activation_match_min（默认 0.7）进入结果。

回退匹配（四元组缺失时）：
  链接 subject_terms 为空（存量迁移或 Session 降级期创建的链接），或
  Qs 为空（本轮 Session 解析降级，subject 未提取出）：
    改用 question_terms 包含度 score = |Qq ∩ Lq| / |Lq|，
    阈值 retrieval.activation_match_min_fallback（默认 0.85）——
    没有守门维度，用更高词项覆盖率补偿；
    且仅允许匹配 audience 与 constraint_terms 均为空的链接
    （问题侧限定未知时，不冒险命中带限定条件的链接）。

排序输出：按 score 降序，取前 retrieval.activation_match_top（默认 5）条。
```

包含度均以链接侧词项为分母：链接条件来自历史问题归一化，通常短于新问题；Jaccard 会因问题较长而系统性压低分数。

verified 链接数量在 V1 规模下（预计 <10^4）全量内存匹配足够；不建 Bleve 索引，避免分词器差异引入不一致。

### 步骤 3：人工确认 API

```text
GET    /activation-links
  查询参数：status、point_id、limit（默认 50）、offset
  响应：[{ link_id, question_terms, point_id, point_summary, unit_center,
           status, adopt_count, fail_count, last_used_at, created_at }]
  （point_summary / unit_center 由 JOIN knowledge_points / knowledge_units 补充）

GET    /activation-links/:id
  响应：完整字段 + created_from + 关联的 learning_results 列表
        （从 learning_results 按 object_id 查询，展示状态迁移史）

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
          避免 Trace 与 Activation 两份实现漂移）
Lifecycle：匹配器 JOIN knowledge_points.lifecycle 过滤
Study：   状态迁移与计数的唯一调用方（本模块提供接口，不自主迁移）
```

## 完成标准

```text
migration 建表成功，UNIQUE(question_terms, point_id) 约束生效；
CreateLink 幂等；对 deprecated 同条件链接拒绝重建并记录日志；
TransitionLink 拒绝迁移表之外的一切迁移（含 deprecated 迁出）；
每次迁移写入 learning_results 且 status_changed_at 更新；
匹配器：输入为 ExpandedQuery，同一四元组重现时可命中由其产生的链接（score=1.0）；
        audience 不等或 constraint 不覆盖的链接被守门排除，即使词项高度重合
        （测试用例：链接约束"产品A"、问题约束"产品B"，主题意图全同 → 不命中）；
        链接限定 audience/constraint 而问题侧为空 → 排除；
        四元组缺失时走回退匹配：更高阈值，且只匹配无限定条件的链接；
        weakened / deprecated / candidate 链接及指向非 current KP 的链接不参与匹配；
缓存失效：链接状态变更或 KP lifecycle 变更后，下一次 Match 反映新状态；
confirm / reject API 正确迁移状态并联动 learning_results；
fake 环境下全部状态机路径与匹配路径测试稳定运行。
```
