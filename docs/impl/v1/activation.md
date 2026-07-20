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
    -- 激活条件定位核心：该 point 全部确证共现标签的词项交集（跨问法稳定核心，
    -- 见 study.md 步骤 2 LabelTermIntersection）；标签不足 2 个或交集不足 2 词
    -- 时退回单条 trace 的 subject。匹配用 overlap（核心词 ⊆ 问题词），非全等
    -- （见步骤 2）
    intent_terms      TEXT NOT NULL DEFAULT '[]',
    -- 累积白名单：JSON 数组，该 point 全部确证 trace 观测到的 intent 归一化值
    -- 去重集合（只增不减，含空串——某条确证 trace 未解析出 intent 也是一次
    -- 合法观测，不是需要剔除的噪声）。匹配用集合成员判断
    audience          TEXT NOT NULL DEFAULT '[]',
    -- 对象守门字段，同 intent_terms：JSON 数组累积白名单，值为 audience
    -- 归一化原文（小写、去标点、去空白，不分词）
    constraint_terms  TEXT NOT NULL DEFAULT '[]',
    -- 约束守门字段，同 intent_terms：JSON 数组累积白名单，值为 constraint
    -- 归一化分词后排序拼接
    -- 以上四字段由 Study 的 computeLinkCondition 从该 point 全部确证 trace
    -- 归纳生成（见 study.md 步骤 2），创建时算一次、此后每轮有新确证信号
    -- 都会刷新，不是创建时定死
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
    -- JSON 数组：创建该链接所依据的 learning_event / link_candidate 标识
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

**匹配语义：subject 子串包含 + 三字段集合成员判断**。激活链接是"精确命中的缓存"，不是小型检索引擎：误命中的代价（错误答案直接出口）远大于漏配（回落慢路径，行为与 MVP 一致），因此依然不做相似度打分——但 subject_terms 存的是跨问法交集（见数据结构节），逐字节全等会让交集算得越好、反而越难命中，必须改成包含判断。

2026-07-19 用真实问题实测（非假设）暴露两个具体缺口，把原先的"token 集合子集判断"进一步改成了"对 subject+intent 合并文本做子串包含"：

```text
缺口 1（词槽不稳定）：核心词入库时来自触发链接创建的历史 trace 的 subject 槽
  （study.md 步骤 2 的 computeLinkCondition 只读 subject 槽标签求交集），
  但同一概念在不同问法里 Session 不保证解析进同一个槽——"扣分"这次落进
  subject，下次可能落进 intent。只对 query.subject 做子集判断会漏掉这类
  case；改成对 subject+intent 合并文本做包含判断，不需要连带放宽
  computeLinkCondition 对核心词来源的把关（那一侧仍然只认 subject 槽，
  更保守、更稳）。

缺口 2（分词边界不稳定）：gse 分词器对同一个复合名词不保证每次切出相同的
  token 边界——建核心词时"数据库连接"被切成一个 token，新问题里同样的
  字符序列被切成"数据库"+"连接"两个 token。token 集合子集判断要求边界
  逐字对齐，对分词器漂移零容忍；改成子串包含（strings.Contains，不再
  分词比较查询侧文本）后，只要核心词的字符序列原样出现在查询文本里就算
  命中，不再关心查询侧自己怎么切词。

代价：子串包含天然比 token 集合更宽松（极短核心词理论上可能命中无关长词的
  子串），用 audience/constraint 仍是精确匹配硬门控来兜住——话题词凑巧撞上
  不足以单独激活错误链接，还需要 audience/constraint 也一致。
```

```text
Match(query ExpandedQuery) → []LinkMatch{link, score}

预处理：
  queryTopic = Normalize(query.subject) + " " + Normalize(query.intent)
  （trim 后）——subject+intent 合并成一段文本，供 subject 核心词的子串
  包含判断用，不再单独分词、不再拆集合；
  对 query.intent / constraint 归一化+分词+停用词过滤+排序拼接
  （与链接侧 intent_terms/constraint_terms 单个集合元素的生成规则一致），
  得 Qi / Qc；
  对 query.audience 归一化（小写、去标点、去空白）得 Qa；
  对 expanded_question 同规则得 Qq（供回退匹配）。

候选加载：
  全部 status=verified 且目标 KP lifecycle=current 的链接
  （JOIN knowledge_points 过滤；结果缓存内存，activation_links 或
   lifecycle 变更时失效重载——两处变更入口分别调用 InvalidateCache）。

主匹配（queryTopic 与链接侧 subject_terms 均非全空时）：
  四个维度全部命中才算命中，score 恒为 1.0：
    linkCore 中每个词都是 queryTopic 的子串
                                      （subject：coreContained，
                                        linkCore = SplitTerms(subject_terms)）
    Qi ∈ intent_terms                （集合成员判断）
    Qa ∈ audience                    （集合成员判断）
    Qc ∈ constraint_terms            （集合成员判断）
  空值语义：intent_terms/audience/constraint_terms 是该 point 全部确证
  trace 观测到的原始值去重集合，"" 和其它值一样是合法成员——集合非空时，
  判断就是普通成员判断（≥1 条 trace 时天然兼容原始标量相等语义）；
  集合为空（该字段从未被计算过——迁移前的存量链接、或手工构造未触碰该
  字段的场景）时按约定等同于 {""}：查询侧该字段也为空才命中，查询侧非空
  则不命中——泛化的是原始标量"''==''才算相等"这条规则，不是"空集合等于
  通配符"。

回退匹配（链接侧 subject_terms 全空，或 queryTopic 全空——即 query.subject
与 query.intent 均未解析出内容——时进入，存量链接未迁移或本轮 Session
解析降级都属于这种情况）：
  仅当链接从未观测到非空 audience 与非空 constraint 时才进入此分支——
  主信号（subject）缺失时，audience/constraint 的相等性无法可信验证，
  带限定条件的链接一律不通过回退匹配；判断的是"集合里有没有非空成员"，
  不是"集合是否为空"——{} 和 {""} 都算未限定，{"", "hr"} 算已限定；
  Qq == question_terms（归一化词项串逐字节相等）才命中，score=1.0。
  同一问题原样（或仅词序/停用词差异）重复提问时，不依赖 Session
  四元组也能命中。

排序输出：命中数超过 retrieval.activation_match_top（默认 5）时
  按 last_used_at 降序截断（相等匹配无分数区分度）。
```

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
        weakened / deprecated / candidate 链接及指向非 current KP 的
        链接不参与匹配；
条件刷新：已有链接的 point 出现新确证信号时，computeLinkCondition 重新
        归纳并通过 UpdateConditions 写回（不产生新链接、不写 learning_results，
        见 study.md 步骤 2）；
缓存失效：链接状态变更或 KP lifecycle 变更后，下一次 Match 反映新状态；
confirm / reject API 正确迁移状态并联动 learning_results；
fake 环境下全部状态机路径、匹配路径、条件刷新路径测试稳定运行。
```
