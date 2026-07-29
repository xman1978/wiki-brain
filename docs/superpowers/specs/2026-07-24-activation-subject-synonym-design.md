# ActivationLink Subject 同义词归一化设计（2026-07-24）

## 背景与问题

`docs/superpowers/specs/2026-07-22-activation-observed-conditions-design.md` 已把 Match 收敛为「观测条件组，组内精确、组间 OR」：subject 维度用词项子串包含（`coreContained`）判断，抗分词/槽位漂移，但不抗**同义改写**——"招待费报销" 与 "差旅报销" 如果实际指向同一 KP，用户换一种问法首次提问仍会漏配，回落慢路径。

讨论过三种收紧/放宽方案，均已排除或限定范围：

1. **全维度集合化**（intent/audience/constraint 也做并集）：会把从未真实共现过的组合判为命中，违背"精确命中的缓存"语义，误命中的代价是 verified 快路径直答错误，不可接受。
2. **仅 subject 并集**：范围收窄，但同样制造"新主题 + 旧意图"的虚假组合，且不可信的召回增量恰恰集中在 subject 不同义的场景。
3. **Subject 同义词归一化（本设计）**：不改变 observed_conditions 的"组内精确、组间 OR"不变式，只在比较 subject 前把同义措辞收敛到同一表达，intent/audience/constraint 仍逐字段精确匹配。方向可行，代价可控。

关键发现：`preset/domains.json` 的每个 concept 已经带 `aliases` 字段（如 `stock_market` 的 `["证券市场", "二级市场"]`），但 `internal/foundation/preset.go` 的 `presetConcept` 目前只解析 `id/name/description`，`aliases` 完全没有被使用。这是一批已经存在、已经人工审核过的同义词，不需要额外撰写就能作为起步词表。

同义词维护的成本必须和收益挂钩：enrichment（`AppendObservedCondition`，见 2026-07-22 spec）已经保证"一种新措辞慢路径成功一次后，第二次就能走快路径"，所以同义词表能省下的只是"新措辞首次即命中"这一次，以及"一条归一化规则对所有 KP 生效"（enrichment 是逐 link 学习）。为了让投入正比于收益，本设计要求同义词候选必须从真实发生的漏配（gap）中挖掘，而不是预先凭空编写。

## 目标

1. Match 阶段对 **subject 维度**做同义词归一化；intent/audience/constraint 的精确匹配语义不变。
2. 免费起步：把 `preset/domains.json` 已有的 concept `aliases` 接入归一化词表，零额外编写成本。
3. 反应式增量：新增 `subject_synonym_gap` 学习事件，捕捉"intent/audience/constraint 全部匹配、仅 subject 未过"的近似命中场景，Study 聚合后生成待确认的同义词候选，人工确认后生效——与 ActivationLink candidate→verified 同一哲学（自动识别、人工确认、可审计）。
4. 实现收敛在 `internal/activation` 包内：`internal/foundation/text` 保持纯函数，不引入 DB 依赖；`observed_conditions` 的存储格式不变，同义词只影响 Match 时的比较，不改写已存储的 `Subject` 原文。

## 非目标

- 不做 embedding / 模糊相似度匹配（同义词表是精确词/短语替换，不是相似度打分）。
- 不做 domain 范围隔离：V1 全局同义词表，`domain_id` 列预留但 Match 不按 domain 过滤（见「开放决定」）。
- 不改变 intent/audience/constraint 的精确匹配语义，不放宽组间 OR / 组内 AND 结构。
- 不做到无人工确认就采纳挖掘出的候选（`synonym_auto_promote` 默认 `false`，与 `study.auto_promote` 同构）。
- 不追溯改写已存储的 `ObservedCondition.Subject` 原文；同义词只在比较时生效，换词表后旧数据无需迁移。

## 设计原则

| 原则 | 含义 |
|------|------|
| 只归一化 subject | intent/audience/constraint 仍要求逐字段全等，误命中面不扩大 |
| 短语级替换，非逐字替换 | 同义关系是整词/短语对整词/短语（"证券市场"→"股票市场"），不是拆开单字重组 |
| 比较时生效，存储不变 | `ObservedCondition.Subject` 原样保留创建时的归一化文本；同义词只在 `Match` 内比较前应用，改词表立即对存量链接生效 |
| 免费来源优先 | preset concept aliases 直接可用，`status=active` 无需确认 |
| 反应式挖掘 | 新增候选来自真实 `subject_synonym_gap` 事件聚合，而非预先编写；未观测到漏配就不产生候选 |
| 人工确认闸 | 挖掘出的候选默认 `pending_confirm`，防止把"近义但不同义"（认证≠授权）自动收敛为同义词 |

## 数据模型

### `subject_synonyms` 表（migration `033_subject_synonyms.sql`）

```sql
CREATE TABLE subject_synonyms (
    synonym_id   TEXT PRIMARY KEY,
    domain_id    TEXT REFERENCES domains(domain_id),
    -- 来自 preset 的行为对应 domain；挖掘出的候选允许为空（全局）；
    -- V1 Match 不按 domain 过滤，仅用于展示/未来收紧（见开放决定）
    term         TEXT NOT NULL,
    -- 归一化后的原始措辞（text.Normalize 结果，短语级，不分词）
    canonical    TEXT NOT NULL,
    -- 归一化后收敛到的规范措辞；term 在 Match 中被替换为 canonical
    source       TEXT NOT NULL DEFAULT 'manual',
    -- preset / gap_mined / manual
    status       TEXT NOT NULL DEFAULT 'active',
    -- active（preset 与人工确认后）/ candidate（挖掘待确认）/ rejected
    created_from TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：candidate 来源的 learning_event event_id 列表；preset 为 '[]'
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_subject_synonyms_term_active
    ON subject_synonyms(term) WHERE status = 'active';
-- 同一 term 至多一条生效映射（跨 domain 冲突在 V1 属已知限制，见开放决定）
CREATE INDEX idx_subject_synonyms_status ON subject_synonyms(status);
```

`term == canonical` 的行不写入（无意义映射）；`canonical` 本身允许作为别的 `term` 的映射目标，但不做链式传递解析——写入时（preset 加载 / candidate 确认）直接解析到最终规范词，避免运行时递归。

### preset 数据接入（`internal/foundation/preset.go`）

```go
type presetConcept struct {
    ID          string   `json:"id"`
    Name        string   `json:"name"`
    Aliases     []string `json:"aliases"`   // 新增字段
    Description string   `json:"description"`
}
```

`LoadPresetData` 在原有 UPSERT domains/concepts 之后，对每个 concept 的每个 alias 额外执行：

```text
term      = text.Normalize(alias)
canonical = text.Normalize(concept.Name)
term == canonical → 跳过
否则 INSERT INTO subject_synonyms (synonym_id, domain_id, term, canonical, source, status)
     VALUES (uuid, domain.ID, term, canonical, 'preset', 'active')
     ON CONFLICT ... DO UPDATE SET canonical = excluded.canonical
     -- 与 concepts UPSERT 同一理由：重跑 preset 应刷新映射，
     -- 但不覆盖人工确认产生的 gap_mined 行（WHERE source = 'preset'）
```

启动时与 `domains`/`concepts` 的 UPSERT 在同一事务内执行，失败即整体回滚（复用现有 `LoadPresetData` 事务边界）。

## Match 算法调整

### 新文件 `internal/activation/synonyms.go`

```go
// SynonymResolver holds an in-memory phrase-replacement table for the
// subject dimension only. Loaded from subject_synonyms WHERE status='active'.
type SynonymResolver struct {
    mu    sync.RWMutex
    pairs []synonymPair // sorted by len(term) desc, longest match first
}

type synonymPair struct{ term, canonical string }

func (r *SynonymResolver) Canonicalize(normalizedText string) string {
    // phrase-level substring replacement, longest term first, single pass
    // (no chained replacement — canonical values are pre-resolved at write time)
}
```

- 输入输出都是 `text.Normalize` 之后、`text.Terms` 之前的短语文本（短语级替换在分词前进行，因为别名是整词/短语对整词/短语，逐字替换会破坏"证券市场"→"股票市场"这类多字对多字关系）。
- 加载/刷新方式与 `Matcher.loadCache` 同构：`Store.ListActiveSynonyms()` 读全表（V1 规模下无需分页），`InvalidateCache` 与 Matcher 共用同一失效信号（synonym 表变更后调用同一 `Matcher.InvalidateCache`，见下）。

### `matcher.go` 改动点

```text
queryTopic := resolver.Canonicalize(strings.TrimSpace(text.Normalize(query.Subject) + " " + text.Normalize(query.Intent)))

conditionGroupMatches 内：
  core := text.SplitTerms(text.Terms(resolver.Canonicalize(cond.Subject)))
  （其余 qi/qa/qc 比较逻辑不变，不经过 resolver）
```

`Matcher` 持有一个 `*SynonymResolver` 字段（构造时注入，`NewMatcher(store, resolver)`），`InvalidateCache` 触发时一并重新加载 resolver（两者合并成一次 `loadCache`，避免多一次数据库往返）。

回退分支（`observed_conditions` 为空、`question_terms` 逐字节相等）**不经过** resolver——回退分支本身是最保守的兜底，保持逐字相等语义，同义词只加宽主匹配路径。

## 同义词候选挖掘（Trace + Study）

### 触发点：`internal/trace/service.go` 步骤 3（与 observed_conditions enrichment 同一位置）

现有 enrichment 条件（`path_type=full ∧ retrieval_quality=confident ∧ direct_point_ids 非空`）下，对每个已有非 deprecated link 的 point，**在 Append 之前**先做一次近似检测：

```text
对该 link 的 observed_conditions 逐组检查：
  qi == cond.intent 且 qa == cond.audience 且 qc == cond.constraint
  但 !coreContained(SplitTerms(Terms(resolver.Canonicalize(cond.subject))), canonicalized queryTopic)
  → 命中"仅 subject 未过"的近似组

若存在至少一个这样的近似组（取 hit_count 最高的一组作代表）：
  写入 learning_event(type="subject_synonym_gap", payload={
    "point_id": pid, "link_id": link.LinkID,
    "query_subject": text.Normalize(query.Subject),
    "observed_subject": cond.Subject,   // 已归一化的历史 subject 原文
    "question_terms": t.QuestionTerms,
  })
```

这一步只读不写 `activation_links`，与 observed_conditions enrichment 并行、互不影响；`AppendObservedCondition` 照常执行（近似检测不影响 enrichment 本身）。

不触发条件（与 enrichment 共用判断，额外排除）：

- `query_subject == cond.subject`（完全相等不是同义词候选，是别的原因导致的漏配，理论上不会出现在这条分支——core 包含判断失败但归一化文本相等是矛盾状态，仅作防御）
- 该 point 尚无 link（沿用现有"无 link 走 gap→Study 创建"路径，不产生同义词信号）

### Study 步骤（`docs/impl/v1/study.md` 步骤 2 之后新增一步）

```text
扫描 processed=0 的 subject_synonym_gap 事件，按
  (Normalize(query_subject), Normalize(observed_subject)) 归一化后的**无序对**
  （先按字符串排序取 pair key，避免 A→B 和 B→A 被当成两条）聚合：
    hit_count   = 事件数
    distinct_n  = 不同 question_hash 数（经 trace_id JOIN traces）

达标（study.synonym_gap_min 默认 3 且 distinct_n ≥ study.synonym_gap_distinct_min 默认 2）：
  canonical 取该 pair 中 hit_count 更高、或 hit_count 相同时取字符序靠前的一侧
  （确定性规则，避免同一 pair 反复达标时来回改变 canonical 方向）；
  term = 另一侧；

  若已存在同 term 的 active/candidate/rejected 行 → 跳过（同一 term 不重复产生候选，
    rejected 的 term 需人工在 UI 显式重新提交才会再评估，V1 不做自动复活）；

  否则 INSERT subject_synonyms(term, canonical, source='gap_mined',
    status='candidate', created_from=支撑事件 id 列表)
  写 learning_results(action='synonym_candidate', object_type='subject_synonym',
    object_id=synonym_id, status='pending_confirm', reason 含 hit_count/distinct_n,
    event_ids=支撑事件 id 列表)

study.synonym_auto_promote = true 时：直接 status='active'，
  learning_results status='applied'，confirmed_by='auto'（与 auto_promote 同构）。

事件标记 processed=1（无论是否达标、是否已存在同名候选）。
```

### 确认/驳回 API（与 activation-link 同构）

```text
GET  /subject-synonyms
  查询参数：status、limit（默认 50）、offset
  响应：[{ synonym_id, domain_id, term, canonical, source, status,
           created_at, updated_at }]

GET  /subject-synonyms/:id
  响应：完整字段 + created_from 关联的 traces 原文（问法列表，复用
        activation 的 ListCreatedFromQuestions 风格）

POST /subject-synonyms/:id/confirm
  仅对 status=candidate 生效；status → active；
  写 learning_results(status=applied, confirmed_by=manual)；
  调用 Matcher 的 InvalidateCache（词表变化需要立即生效）。

POST /subject-synonyms/:id/reject
  仅对 status=candidate 生效；status → rejected；
  写 learning_results(status=rejected, confirmed_by=manual)。
```

## 配置项新增（`config.yml` study 节）

```yaml
study:
  synonym_gap_min:          3   # subject_synonym_gap 候选达标所需事件数
  synonym_gap_distinct_min: 2   # 且来自 ≥ N 个不同 question_hash
  synonym_auto_promote:     false  # true 时候选直接 active，不经人工确认
```

## 测试计划

| 用例 | 期望 |
|------|------|
| preset alias 加载 | `subject_synonyms` 含各 domain 的 alias 行，`source=preset`，`status=active` |
| 重跑 preset | 已有 preset 行的 canonical 被刷新，不新增重复行，不影响 gap_mined 行 |
| Match：同义词生效 | 链接观测 subject="股票市场"，问题 subject="证券市场"，其余三维一致 → 命中 |
| Match：intent/audience/constraint 不受同义词影响 | 同义词只解析 subject；约束不同仍不命中 |
| Match：回退分支不经过 resolver | 空 observed_conditions 场景下，同义词不影响 question_terms 逐字匹配 |
| 近似检测触发 | 构造 intent/audience/constraint 全同、subject 不同义（未注册映射）的确证问答 → 产生 `subject_synonym_gap` 事件，不产生误报的 `activation_gap` |
| Study 聚合达标 | 3 次不同问题的 `subject_synonym_gap` 事件（同一 pair）→ 生成 `pending_confirm` 候选 |
| confirm 后 Match 生效 | confirm 后同 pair 问法当场可命中（cache 失效正确触发） |
| reject 后不再产生 | 同 term 被 reject 后，新的 gap 事件不会为同一 term 重新创建候选 |
| 跨 domain 术语冲突（已知限制） | 两个 domain 对同一 term 给出不同 canonical 时，写入侧行为符合"后写覆盖"文档说明，不 panic |
| auto_promote=true | 候选达标直接 active，无 pending_confirm |

## 代码影响范围

| 位置 | 动作 |
|------|------|
| `internal/foundation/db/migrations/033_subject_synonyms.sql` | **新增** 建表 |
| `internal/foundation/preset.go` → `presetConcept` / `LoadPresetData` | **改**：解析 `aliases`，UPSERT `subject_synonyms` |
| `preset/domains.json` | 不改（aliases 字段已存在） |
| `internal/activation/synonyms.go` | **新增** `SynonymResolver`、`Canonicalize` |
| `internal/activation/matcher.go` → `Match` / `conditionGroupMatches` | **改**：subject 比较前经 `resolver.Canonicalize` |
| `internal/activation/store.go` | **新增** `ListActiveSynonyms`、`InsertSynonymCandidate`、`GetSynonym`、`UpdateSynonymStatus`、`ListSynonyms` |
| `internal/activation/service.go` | **新增** `ConfirmSynonym` / `RejectSynonym`（同构 `Confirm`/`Reject`），`Matcher` 构造改为注入 resolver |
| `internal/activation/handler.go` | **新增** `GET/POST /subject-synonyms*` 路由 |
| `internal/trace/service.go` → 步骤 3 enrichment 分支 | **新增**：近似检测，写 `subject_synonym_gap` 事件（不影响现有 `AppendObservedCondition` 调用） |
| `internal/study/service.go` | **新增**：`subject_synonym_gap` 聚合与候选生成步骤 |
| `internal/study/store.go` | **新增**：按 pair 聚合查询、`question_hash` 去重计数 |
| `config/config.yml` | **加** `synonym_gap_min` / `synonym_gap_distinct_min` / `synonym_auto_promote` |
| `web/index.html` | **可选新增**：同义词候选确认列表（与 ActivationLink 管理视图同页或并列） |

### 测试文件

| 文件 | 动作 |
|------|------|
| `internal/foundation/preset_test.go` | 新增 alias 加载断言 |
| `internal/activation/matcher_test.go` | 新增同义词命中/不命中用例 |
| `internal/activation/synonyms_test.go` | **新增**：`Canonicalize` 短语替换单测 |
| `internal/activation/store_test.go` / `handler_test.go` | 新增 synonym CRUD / confirm / reject |
| `internal/trace/activation_events_test.go` | 新增 `subject_synonym_gap` 事件产生断言 |
| `internal/study/service_test.go` | 新增聚合与候选生成断言 |

### 文档同步

- `docs/impl/v1/activation.md`：数据结构增补 `subject_synonyms`、步骤 2 增补同义词归一化说明、依赖/完成标准同步
- `docs/impl/v1/study.md`：步骤 2 之后增补同义词候选聚合步骤、配置项、API
- `docs/impl/v1/trace.md`：步骤 3 增补近似检测触发条件

## 实现顺序（批准后）

1. migration + `preset.go` 解析 aliases + 加载测试
2. `internal/activation/synonyms.go`：`SynonymResolver`，`Store.ListActiveSynonyms`
3. `matcher.go` 接入 resolver；改写 `matcher_test.go` 同义词用例
4. `trace/service.go`：近似检测 + `subject_synonym_gap` 事件；补测
5. `study/service.go` + `study/store.go`：聚合与候选生成；`config.yml` 加配置
6. `activation` Store/Service/Handler：synonym CRUD + confirm/reject API
7. `web/index.html`（可选）：候选确认 UI
8. 同步 `docs/impl/v1/activation.md` / `study.md` / `trace.md`

## 开放决定（写进实现时可默认如下）

| 项 | 默认 |
|----|------|
| domain 范围隔离 | 不做；`domain_id` 列只做展示与未来收紧的预留，Match 阶段全局生效 |
| 跨 domain 同 term 不同 canonical | 后写覆盖（`ON CONFLICT DO UPDATE`／候选生成前查重使用最新一行），不报错、不合并；实测出现冲突再评估是否需要 domain 隔离 |
| candidate 方向选择 | hit_count 更高的一侧作为 canonical，相同则字符序靠前者 |
| rejected 复活 | 不自动复活；需人工在 UI 显式重新提交 |
| preset 别名与 gap_mined 候选共享同一张表 | 是（`source` 字段区分，`status` 语义一致），不建两套存储 |
| Match 时机 | 与 `ListMatchableLinksForCurrentKP` 缓存一起失效，不做独立 TTL |

---

批准本 spec 后，再按「实现顺序」改代码，并同步 `docs/impl/v1/*`。
