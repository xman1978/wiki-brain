# ActivationLink 观测条件组（Observed Conditions）设计（2026-07-22）

## 背景与问题

当前 ActivationLink 条件归纳把多条问法压成一套字段：

- `subject_terms`：标签词项**交集**（`LabelTermIntersection`）；失败则 fallback 到**代表标签**整句 subject
- `intent_terms` / `audience` / `constraint_terms`：各槽位**并集白名单**

Match 再按「核心 ⊆ queryTopic + 三字段 ∈ 白名单」判定。

实测故障（招待费报销 KP）：

1. 多标签全交集只剩 1 词 → fallback 写出 `差旅 报销 招待费`
2. 创建依据里的问法「招待费用报销期限是多久？」Session 解析无「差旅」→ Match 失败 → 慢路径
3. **创建过的问法无法再命中同一 link**——违背「精确命中缓存」语义

更深一层：并集白名单允许「A 问法的 subject + B 问法的 intent」交叉命中，与 `docs/design/precompile.md` 冲突：

> 条件应表达为「实际观测到的条件组合」，而不是各维度独立取值的并集。

词频选核心也不采纳：词频 ≠ 语义核心。

## 目标

1. **Match**：只命中「曾经真实一起出现过的条件组合」；创建/观测过的问法按构造可再命中。
2. **归纳**：取消关键词交集/合并与跨问法并集白名单。
3. **学习闭环**：慢路径成功引用已有 link 的 KP 时，把**本轮问题四元组**追加进该 link，使下次可走快路径。
4. **规模**：V1 仍全量内存匹配（verified &lt; 10⁴）；组数设上限；文档预留倒排剪枝，本变更不实现。

## 非目标

- 不改「每 `point_id` 至多一条非 deprecated link」约束（仍存储形态 A）
- 不做 embedding / LLM / 模糊相似度匹配
- 不把 ActivationLink 做成检索引擎；漏配仍回落慢路径
- 不在本变更实现 intent/约束倒排索引（仅写清升级路径）
- 不改变 candidate 只记信号、不直答；verified 多命中仍歧义回落慢路径

## 设计原则

| 原则 | 含义 |
|------|------|
| 观测组合 | 一条条件 = 一次确证问答的 `(subject, intent, audience, constraint)` 归一化四元组 |
| 组内精确、组间 OR | 四字段在同一组内全部满足才算该组命中；任一组命中即 link 命中 |
| 禁止交叉拼接 | 不再维护跨问法 intent/audience/constraint 并集 |
| 禁止关键词合并 | 删除 `LabelTermIntersection` 及「代表标签 fallback 写宽核心」路径 |
| 慢路径可 enrichment | 已有 link 的 KP 被慢路径 confident 采用 → 追加本轮四元组 |

## 数据模型

### 新增字段

```sql
-- migration NNN_activation_observed_conditions.sql
ALTER TABLE activation_links ADD COLUMN observed_conditions TEXT NOT NULL DEFAULT '[]';
-- JSON 数组，元素见下；匹配唯一真相源
```

### ObservedCondition 元素

```json
{
  "subject": "招待费报销",
  "intent": "报销 期限 查询",
  "audience": "",
  "constraint": "",
  "question_terms": "招待费报销",
  "first_seen_at": "2026-07-22T05:17:09Z",
  "last_seen_at": "2026-07-22T12:32:17Z",
  "hit_count": 2
}
```

| 字段 | 归一化规则（与现 Match 预处理一致） | 用途 |
|------|--------------------------------|------|
| `subject` | `Normalize(trace.subject)` | Match：词 ⊆ `queryTopic`（子串） |
| `intent` | `Terms(Normalize(trace.intent))` | Match：与 `Qi` 全等 |
| `audience` | `NormalizeCompact(trace.audience)` | Match：与 `Qa` 全等 |
| `constraint` | `Terms(Normalize(trace.constraint_text))` | Match：与 `Qc` 全等 |
| `question_terms` | 与 `traces.question_terms` 同规则 | 展示 / 空四元组回退键 |
| `first_seen_at` / `last_seen_at` / `hit_count` | 追加时维护 | 上限淘汰、调试 |

**去重键**：`(subject, intent, audience, constraint)` 四元组归一化后逐字段相等 → 同一组，只更新 `last_seen_at` / `hit_count`。

### 旧字段处置

| 字段 | 处置 |
|------|------|
| `subject_terms` / `intent_terms` / `audience` / `constraint_terms` | **不再参与 Match**；创建/刷新时用「代表组」回填以便旧 UI/API 暂不崩：代表组 = `last_seen_at` 最新的一组；`subject_terms` = 该组 `subject` 的 `Terms` 串；另三字段 = **单元素数组** `[该组对应值]`（不是并集） |
| `question_terms` | 仍为展示用代表标签（confident 最高 / 最新）；回退匹配仍可用 |
| `LabelTermIntersection` | **删除**（或仅留测试确认已无调用方） |
| `computeLinkCondition` 交集+并集逻辑 | **替换**为 `buildObservedConditions(pointID)` |

后续 UI 详情应直接展示 `observed_conditions` 列表；旧四字段视为兼容投影，实现文档标注 deprecated-for-match。

### 组数上限

- 配置项（建议挂 `study` 或 `activation`）：`observed_conditions_max`，默认 **50**
- 超过时：按 `last_seen_at` 升序删最旧组，直至 ≤ max（保留最近活跃问法）
- 追加与全量重建均遵守此上限

## Match 算法

```text
Match(query ExpandedQuery) → []LinkMatch

预处理（不变）：
  queryTopic = trim(Normalize(subject) + " " + Normalize(intent))
  Qi / Qa / Qc / Qq = 现规则

候选：status ∈ {verified, candidate} ∧ KP lifecycle=current（缓存不变）

对每个 link：
  conditions = link.observed_conditions
  if len(conditions) == 0:
      // 存量未迁移或异常空组：仅走旧回退
      if 链接从未观测非空 audience/constraint（看 conditions 或旧字段投影）
         且 Qq == link.question_terms → 命中
      continue

  for each cond in conditions:
      // 组内四门全过
      subjectWords = SplitTerms(Terms(cond.subject))  // 或 SplitTerms(已是词串的 subject)
      // 约定：入库的 cond.subject 存 Normalize 后原文；Match 侧
      //   core = SplitTerms(Terms(cond.subject)) 与「对 Normalize 文本做子串」二选一需实现时统一：
      //   推荐与现一致——对 subject 再 Terms 得词表，每个词 strings.Contains(queryTopic, w)
      if !coreContained(core, queryTopic): continue
      if Qi != cond.intent: continue
      if Qa != cond.audience: continue
      if Qc != cond.constraint: continue
      → link 命中，score=1.0，break

排序截断：仍按 last_used_at，≤ activation_match_top
```

说明：

- **不再** `Qi ∈ intent_terms` 并集
- subject 仍用子串包含，抗分词/槽位漂移；守门仍在同组 audience/constraint
- candidate 命中语义不变（记信号、不直答）

## 条件如何写入 / 追加

### A. 创建 candidate（Study `tryCreateLink`）

`buildObservedConditions(pointID)`：

1. 取该 point 全部「确证且 `direct_point_ids` 含该 point」的 traces（与现 `ConfidentTraceFieldValues` 同源约束，**必须引用级**，禁止只按 `question_terms` join）
2. 每条 trace → 一组 ObservedCondition；按四元组去重合并 `hit_count`
3. 若结果为空 → 不创建 link（与现「无确证信号则 skip」一致）
4. `CreateLink(..., conditions, ...)`；同步写兼容投影字段与代表 `question_terms`

**删除**：`LabelTermIntersection`、交集失败 fallback 代表 subject、三字段并集白名单。

### B. Study 刷新（已有非 deprecated link）

同一 `buildObservedConditions` **全量重建**后与库中比较（集合相等：忽略 `hit_count`/`*_seen_at` 或规范化后再比）；不同则 `ReplaceObservedConditions`。

全量重建保证：纠错后错误四元组可在「该 trace 不再 confident / 不再引用该 point」时被清掉；与「只增不减并集」旧行为不同——**以当前确证集合为准**。

### C. 慢路径 enrichment（本设计新增，用户明确要求）

触发点：Trace 写完学习事件之后（或 Answer/Trace 管线末尾），当同时满足：

```text
path_type = full（慢路径，含快路径校验失败回落）
retrieval_quality = confident
EvidenceSet / trace 的 direct 引用 point_id 集合非空
```

对每个被引用的 `point_id`：

```text
若存在 status ∈ {candidate, verified, weakened} 的 ActivationLink（deprecated 跳过）：
  AppendObservedCondition(link_id, 本轮 Session 四元组 + question_terms)
  去重键命中 → 只 bump hit_count / last_seen_at
  超上限 → 淘汰最旧
  InvalidateCache
```

要点：

- **不要求**本轮 Match 曾命中该 link（慢路径正是未命中或未采用快路径）
- weakened 也追加：便于再验证时问法更全；Match 本身 weakened 仍不参与（现规则不变）
- 不写 `learning_results`（与 Study 条件刷新一样，属维护动作）；可选 debug 日志
- 同一请求多个 direct KP → 每个有 link 的 KP 各追加一组（各组内容相同四元组、不同 link）

不触发追加：

- `uncertain` / 非 confident
- 无 direct 引用
- 该 point 尚无 link（仍只走现有 gap → Study 创建路径）

### D. 与 activation_gap / 共现的关系

- **无 link**：仍靠 gap + 共现达标 → Study 创建；创建时用 `buildObservedConditions` 灌入历史确证组
- **有 link**：慢路径 confident 引用 → **立即** Append（不必等下一轮 Study）
- Study 刷新仍全量重建，与 Append 不冲突：重建是权威快照，Append 是低延迟增量

## API / UI

| 接口 | 变更 |
|------|------|
| `GET /activation-links/:id` | 响应增加 `observed_conditions`；旧四字段保留为投影 |
| 列表/详情 UI | 详情展示「观测条件」列表（subject/intent/audience/constraint）；问法列表逻辑可保留 |
| Match / Retrieval | 无新 HTTP；行为变更见上 |

## 存量迁移

迁移 SQL 只加列，默认 `[]`。

**启动后首次 Study**（或一次性脚本，实现阶段二选一，推荐挂 Study 刷新路径）：

对每条非 deprecated link：`buildObservedConditions(point_id)` → 写回；若为空则保留 `[]` 并依赖 `question_terms` 回退匹配，直到有新确证信号。

不在 migration 里用交集算法回填——避免再次写入错误核心。

## 性能

| 规模 | 策略 |
|------|------|
| V1（L≲10⁴，C≲50） | 内存缓存 × 线性扫组；与现 Matcher 同级 |
| 以后 | 倒排：`intent` / 非空 `audience` / 非空 `constraint` → link_id；先剪枝再组内精确比较 |

瓶颈仍在 LLM 慢路径，不在 Match。

## 测试计划

| 用例 | 期望 |
|------|------|
| 多标签无长交集（招待费类） | 创建后 `observed_conditions` 含各组；**无** `差旅 报销 招待费` 合并核心 |
| 创建问法原样再问 | Match 命中对应组 → verified 走快路径（其余快路径门控满足时） |
| 仅共享 intent、subject 未观测组合 | **不**命中（禁止交叉） |
| 慢路径 confident 引用已有 link 的 KP | `observed_conditions` 增加本轮四元组；再问可命中 |
| Study 全量重建 | 不再 confident 的旧组消失 |
| 超过 `observed_conditions_max` | 最旧组被淘汰 |
| 旧交集单测 | 删除或改写为「多组 OR」语义 |
| candidate / 歧义 / weakened | 行为与现 retrieval 约定一致 |

## 代码影响范围（对照现状）

匹配相关不是单一函数，而是整条链：

```text
Match ← 条件存储 ← Study 归纳 ←（新增）Trace 慢路径 Append ← API/UI 展示
```

Retrieval / Answer 只消费 `Match()` 结果，**接口可保持**；语义变更在 activation/study/trace。

### 必须改：匹配语义真相源

| 位置 | 现状 | 动作 |
|------|------|------|
| `internal/activation/matcher.go` → `Match` | 读 `subject_terms` + 三字段并集白名单 | **重写**：扫 `observed_conditions` 组内四门；`coreContained` 可保留给组内 subject |
| `internal/activation/matcher.go` → `containsString`（并集成员） | 白名单 ∈ | **主路径删除**；仅若兼容回退仍读旧列时慎用 |
| `internal/activation/types.go` → `LinkCondition` / `ActivationLink` | 单核心 + 三集合 | **扩展**：`ObservedConditions`；创建入参改为条件组（或新类型） |
| `internal/activation/store.go` → 写入 / `UpdateConditions` / `linkColumns` / scan | 只写旧四字段 | **改**：读写 `observed_conditions`；新增 `AppendObservedCondition`、`ReplaceObservedConditions`；`UpdateConditions` 替换或废弃 |
| `internal/activation/service.go` → `CreateLink` / `Match` | 透传旧 `LinkCondition` | **改**：创建写入条件组；`Match` 仍委托 Matcher |
| `internal/activation/handler.go` → 详情 JSON | 暴露旧四字段 | **改**：增加 `observed_conditions`；旧字段作投影 |

### 必须改：条件归纳（去掉关键词合并）

| 位置 | 现状 | 动作 |
|------|------|------|
| `study/service.go` → `computeLinkCondition` | 交集 + 并集白名单 | **替换**为 `buildObservedConditions` |
| `study/service.go` → `LabelTermIntersection` | 关键词交集 | **删除** |
| `study/service.go` → `normalizeDedupSorted` / `conditionEqual` | 服务并集与逐字段相等 | **改或删**：相等改为条件组集合比较 |
| `study/service.go` → `tryCreateLink` | 调旧归纳 + `UpdateConditions` | **改**：创建/刷新走条件组 |
| `study/store.go` → `ConfidentTraceFieldValues` | 各槽位去重**并集** | **不能直接复用做 Match 条件**；新增 `ConfidentTraceQuadruples(pointID)` 返回整组四元组（仍须引用级过滤，同 2026-07-21 修订） |
| `study/store.go` → `CooccurrenceLabelsForPoint` | 仅服务交集 | 匹配归纳**不再调用**；共现扫描创建 candidate 仍可用共现表（与 Match 无关） |
| `study/store.go` → `LatestConfidentTraceQuadruple` | 交集失败 fallback | **删除调用路径**（不再写宽核心）；函数可删或仅留测试 |

### 必须新增：慢路径 enrichment

| 位置 | 动作 |
|------|------|
| `internal/trace/service.go`（学习事件之后） | **新增** Append：`full` + `confident` + direct KP 已有非 deprecated link |
| Trace → activation 依赖 | 今日 Trace **不写** link 条件；需注入 `AppendObservedCondition`（经 `activation.Service`） |
| `trace/activation_events_test.go` 等 | **补测** enrichment |

说明：`activation_gap` / `activation_success` 评分逻辑**可不动**；Append 是并行副作用，不替代 gap。

### 调用方：匹配逻辑基本不动

| 位置 | 动作 |
|------|------|
| `retrieval/service.go` → `Match` + `TouchLastUsed` | **接口不变**；命中语义变了，调用代码不用改 |
| `answer/service.go` 传递 `ActivationHits` | **不动** |
| `ListMatchableLinksForCurrentKP` | **保留**；行数据需带上 `observed_conditions` |
| `TouchLastUsed` / `TransitionLink` 状态机 | **不动** |
| KPN `groupPointsForCrossMatch` | **无关**（不是 Activation Match） |
| `GET .../questions` 问法列表 | **可不改**；可选后续标注「命中了哪一组」 |
| Bleve / FTS | **不参与** Activation Match |
| `encodeTermSet` / `decodeTermSet` | 若兼容投影仍写旧列，**可暂留** |

### UI / 配置 / 验收

| 位置 | 动作 |
|------|------|
| `web/index.html` 详情「主体/意图」「约束/受众」 | **应改**：展示 `observed_conditions` 列表；只显示投影会误导（像旧并集） |
| `formatTermSet` | 列表 chip 仍可用；详情以组为准 |
| `config/config.yml` | **加** `observed_conditions_max`（默认 50）；`activation_match_top` 保留 |
| `test/v1/v1-acceptance-test-plan.md` | 回退模拟改为清空 `observed_conditions`；F 组守门改为「组内 constraint」 |
| `wiki/testhelpers_test.go` 手工 INSERT | DEFAULT `[]` 可暂不改；建议显式写入 |

### 测试（需改写，不是小补）

| 文件 | 动作 |
|------|------|
| `activation/matcher_test.go` | **大改**：并集 audience、交集 overlap →「多组 OR / 禁止交叉」 |
| `study/activation_actions_test.go` | `want 交集 "句柄 数据库"` → 多组或代表投影 |
| `study/store_test.go` | 保留引用级约束测；新增 `ConfidentTraceQuadruples` 测 |
| `retrieval/fastpath_test.go` | `CreateLink` 需可命中的条件组（或空组 + `question_terms` 回退） |
| `activation/store_test.go` / `handler_test.go` | Create/Update/详情字段 |

### 文档同步（实现时）

- `docs/impl/v1/activation.md`：数据结构、Match 步骤 2、删交集/并集描述
- `docs/impl/v1/study.md`：步骤 2 → `buildObservedConditions`；慢路径 Append 在 Trace
- `docs/impl/v1/trace.md`：慢路径 enrichment 触发条件
- `docs/impl/v1/page.md`：详情展示条件组
- `docs/design/precompile.md`：可加一句「V1 以 observed_conditions 落实观测组合」
- 验收计划：清空旧四字段的回退模拟 → 清空 `observed_conditions`

## 实现顺序（批准后）

1. migration + types（`ObservedCondition`、`Replace`/`Append`、scan 带新列）
2. Store：`ConfidentTraceQuadruples`；activation `Append`/`Replace`
3. Matcher 改为组内 OR；改写 `matcher_test`
4. Study：`buildObservedConditions` 替换归纳；删 `LabelTermIntersection` 及 fallback；改写 `activation_actions_test`
5. Trace：注入慢路径 Append；补测
6. API/UI：详情展示条件组；`config` 加 max
7. 存量：Study 刷新回填；手测招待费问法快路径
8. 同步 `docs/impl/v1/*` 与验收计划

## 开放决定（写进实现时可默认如下）

| 项 | 默认 |
|----|------|
| 存储形态 | A：每 point 一条 link + `observed_conditions` 数组 |
| subject 入库 | `Normalize(subject)` 原文；Match 再 `Terms` 得词做 `Contains` |
| weakened 是否 Append | 是 |
| Append 是否写 learning_results | 否 |
| 兼容字段投影 | 最新一组的单值投影，非并集 |

---

批准本 spec 后，再按「实现顺序」改代码，并同步 `docs/impl/v1/*`。
