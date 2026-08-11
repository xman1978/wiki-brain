# Study 实现路径（V1）

## 职责

Study 从 MVP 的「只产报告」升级为「执行学习动作 + 可审计」：消费激活类 Learning Event 与共现统计，执行 ActivationLink 的创建、晋升、降权、淘汰，每个动作落库为 Learning Result 并附 Learning Reason。报告继续生成，内容扩展为本周期执行的动作及原因。

运行方式沿用 MVP：`time.Ticker` 定时扫描，不走异步队列。Study 是 activation_links 状态迁移的唯一执行方（经 activation.md 的 `TransitionLink` 接口）。

## 数据结构

### learning_results 表

```sql
CREATE TABLE learning_results (
    result_id       TEXT PRIMARY KEY,
    action          TEXT NOT NULL,
    -- create_candidate / promote / weaken / reverify / deprecate /
    -- gap_flag / wiki_candidate / recompile_flag / topic_page_candidate
    object_type     TEXT NOT NULL,
    -- activation_link / knowledge_gap / wiki_page
    object_id       TEXT NOT NULL,
    reason          TEXT NOT NULL,
    -- Learning Reason：人类可读的依据说明（含关键统计数字）
    event_ids       TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：支撑本动作的 learning_event event_id 列表
    status          TEXT NOT NULL DEFAULT 'applied',
    -- applied / pending_confirm / rejected
    confirmed_by    TEXT,
    -- pending_confirm 被处理时记录 'manual' 或 'auto'
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_lr_object ON learning_results(object_type, object_id);
CREATE INDEX idx_lr_status ON learning_results(status);
```

### knowledge_gaps 表扩展（gap_reason 溯源）

现状（MVP）：报告只能看到「哪个问题命中了几次缺口」，看不出缺口的根因——是检索链路完全没召回到候选（真的没材料），还是召回到了候选但被 rerank judge 判成不相关（内容存在但语义提取/判断有问题），还是证据齐全但 LLM 生成失败（系统性错误，不是知识缺口）。这三种情况需要的人工动作完全不同，混在一个 `hit_count` 里无法区分，每次都要去翻 trace 明细排查。

前置依赖（非本模块改动，需 retrieval.md / trace.md 配合）：`knowledge_gap` 学习事件 payload 新增 `reason` 字段，取值三选一：

```text
no_candidates   — outline+FTS 召回合并后候选为 0，链路上根本没有可判的候选
                  （来源：retrieval.EvidenceSet 新增 GapReason 字段，
                   在 recallFromSources 合并候选为空时置为该值）
judge_filtered  — 召回到候选，但 rerank judge 全部判 irrelevant（含 KPN 扩展后
                  direct/supporting 仍均为 0）；内容大概率存在，是语义提取摘要
                  或 judge 判断的问题，指向 semantics-curation.md 的人工修正流程
                  （同一来源字段，recallFromSources 该分支置为该值）
answer_error    — 检索阶段有 direct 或 supporting 证据，但 AnswerResult.Path
                  == "error"（LLM 生成失败），不是真正的知识缺口
                  （来源：trace 层直接读 t.Path，无需 retrieval 改动）
```

`knowledge_gaps` 表新增两列（migration `025_knowledge_gap_reason.sql`）：

```sql
ALTER TABLE knowledge_gaps ADD COLUMN reason_counts TEXT NOT NULL DEFAULT '{}';
-- JSON 对象，累计各 reason 命中次数，如 {"no_candidates":5,"judge_filtered":2}
ALTER TABLE knowledge_gaps ADD COLUMN last_reason TEXT NOT NULL DEFAULT '';
-- 最近一次命中的 reason，用于报告里按主导原因分级 recommendation
ALTER TABLE knowledge_gaps ADD COLUMN last_trace_id TEXT NOT NULL DEFAULT '';
-- 最近一次命中的 trace_id，用于回查该次检索的完整证据（含被过滤的候选）
```

不在 `knowledge_gaps` 里重复存储 KU 原文/文档标题：`last_trace_id` 只是指针，KU 内容、`source_ref`（含 source_id）已经完整存在 `answers.snapshot` 里（`GET /traces/:id` 拿到 `answer_id` 后 `GET /answers/:id` 即可取回）；这条链路复用现有接口，不新建存储也不新建接口。要让"被过滤的 KU"同样可查，前置依赖 retrieval.md 给 `EvidenceSet` 新增 `FilteredEvidence []Evidence`（rerank 阶段把 role=="irrelevant" 的候选一并带上，结构复用现有 `Evidence`），这样它会随现有 `evidence_snapshot` 一起自然落进 `answers.snapshot`，study 侧无需额外处理。

## 配置项（config.yml: study 节扩展）

```yaml
study:
  # —— MVP 既有配置保留 ——
  schedule_interval:       "1h"
  candidate_confident_min: 5
  candidate_ratio_min:     0.6
  wiki_kp_min:             4
  # wiki_confident_min 已移除（2026-07-29 修订，见 docs/design/wiki.md
  # "ActivationLink 回答'这条管不管用'，Wiki 编译回答'这个主题够不够格立传'"）——
  # 可靠性只由 ActivationLink 的 verified 状态判断，不再叠加一个次数门槛；
  # 素材排序（wiki.md 步骤 3，按 confident_count 降序截取）用的是 KP 自身的
  # confident_count 数值，不需要任何最小值配置，所以这个字段没有剩余用途。
  gap_hit_threshold:       3
  scan_batch_size:         200
  report_period_days:      5
  report_max_keep:         10
  # —— V1 新增 ——
  auto_promote:            false  # true 时晋升不经人工确认
  promote_success_min:     3      # 晋升所需窗口内 activation_success 事件数
  promote_distinct_min:    2      # 且来自 ≥ N 个不同 question_hash（防单一问题刷信号）
  weaken_failure_min:      3      # 降权所需窗口内 activation_failure 事件数
  weaken_ratio_min:        0.5    # 且窗口内 failure / (success+failure) ≥ 该比值
  reverify_success_min:    2      # weakened 重新验证所需窗口内 success 数
  event_window_days:       30     # 上述累积判定的时间窗口
  candidate_idle_days:     30     # candidate 无新信号自动淘汰
  deprecate_idle_days:     60     # weakened 无有效使用自动淘汰
  correction_weight:       2      # user_correction 关联链接时按 N 次 failure 计
  # —— subject 同义词挖掘（2026-07-24 新增）——
  synonym_gap_min:          3     # subject_synonym_gap 候选达标所需事件数
  # —— 问题复杂度观测量（步骤 7，只观测不驱动）——
  complexity_min_questions: 3     # 条件组内问答数下限，低于此不出报告项
  synonym_gap_distinct_min: 2     # 且来自 ≥ N 个不同 question_hash
  synonym_auto_promote:     false # true 时候选直接 active，不经人工确认
```

## 实现步骤

每次调度顺序：步骤 1 → 2 → 3 → 4 → 5 之后沿用 MVP 的 gap 聚合与报告生成（步骤 6、7）。单步异常记录 error 日志，不中断本轮后续步骤。

### 步骤 1：共现扫描（2026-07-18 修订：按 point 聚合）

扫描 `question_kp_cooccurrence` UPSERT `link_candidates`。达标判定不再按
`(question_terms, point_id)` 行级计数，而是**按 point_id 聚合**：

```text
按 point_id GROUP BY，SUM(confident_count) ≥ candidate_confident_min
且 SUM(confident_count)/SUM(hit_count) ≥ candidate_ratio_min 视为达标；
每个达标 point 只写一行 link_candidates：question_terms 取该 point 的
代表标签（confident_count 最高，并列取 last_seen_at 最新），
confident_count / hit_count 存聚合值。
```

修订原因（V1 验收测试诊断）：question_terms 的取值是 Session 解析出的
subject 标签，同一话题的不同问法会产生不同标签（"数据库句柄限制"/"数据库
句柄管理"……），行级计数把同一 KP 的学习信号打散在多行、每行都到不了阈值，
candidate 永远无法形成；KP 才是跨问法稳定的锚点。

### 步骤 2：创建或刷新 candidate ActivationLink

两个来源，合并后逐个处理（每个达标 point_id 要么创建、要么刷新，不会两者都发生）：

```text
来源 A（共现达标）：
  link_candidates 中的行（步骤 1 已按 point 聚合达标）；

来源 B（activation_gap 事件）：
  processed=0 的 activation_gap 事件，对 payload 中每个 direct_point_id，
  查共现表该 point 的 SUM(confident_count) ≥ 2 时视为达标（gap 事件本身即
  一次 confident 命中，要求至少再复现一次，避免单次问答直接产生候选；
  复现看 point 聚合值，与步骤 1 同因——不要求恰好同标签复现）；

分流（两来源共用，tryCreateLink 内部判断）：
  activation_links 中已存在该 point_id 的链接（任意非 deprecated 状态）
  → 走"刷新条件"；deprecated → 跳过（终态，不复活）；
  否则 → 走"创建"。两者共用同一条件归纳逻辑（computeLinkCondition，见下），
  只是创建走 CreateLink+写 learning_results，刷新走 UpdateConditions+不写
  learning_results（条件收敛是持续性维护动作，不是一次性学习事件）。

computeLinkCondition → buildObservedConditions(pointID) → []ObservedCondition：
  取 ConfidentTraceQuadruples(pointID)（确证且 direct_point_ids 含该 point）；
  每条 trace → 一组归一化四元组；按四元组去重合并 hit_count；
  上限 study.observed_conditions_max（默认 50），超限按 last_seen_at 淘汰最旧；
  **不再** LabelTermIntersection / 并集白名单 / 代表标签 fallback。
  空结果 → 跳过创建。

创建：CreateLink(..., ObservedConditions=conds, ...)；
刷新：ReplaceObservedConditions（全量重建）；集合相等则跳过。

慢路径 enrichment（Trace，非 Study）：
  path=full ∧ confident ∧ 引用已有非 deprecated link 的 point
  → AppendObservedCondition（本轮四元组）；不必等下一轮 Study。


创建路径：
  CreateLink(question_terms, cond, point_id, created_from=支撑事件 id 列表)；
  幂等（UNIQUE(point_id) 冲突时返回已存在链接，见 activation.md 数据结构）；
  写 learning_results(action=create_candidate, status=applied,
    reason 含 confident_count / ratio / 触发来源，
    event_ids = 支撑本动作的 learning_event event_id 列表)：
    来源 A：该 point 关联的 activation_gap 事件 id（不得写入
      link_candidates.candidate_id——candidate_id 不是 learning_event）；
    来源 B：触发本次创建的 activation_gap 事件 id；

刷新路径：
  cond 与链接当前存储条件逐字段比较（conditionEqual：subject_terms 字符串
  相等 + 三个集合字段各自的有序切片相等），不同才 UpdateConditions 写回，
  相同则跳过（避免每轮 Study 都无意义地 touch updated_at）；

标记来源 activation_gap 事件 processed=1（无论创建/刷新/跳过都标记，
  该事件已被消费）。
```

### 步骤 2a：subject 同义词候选聚合（2026-07-24 新增）

供 ActivationLink Match 的 subject 维度同义词归一化提供人工确认候选；完整背景与动机见 `docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md`。挖掘信号来自 Trace 步骤 3（见 `trace.md`）产生的 `subject_synonym_gap` 事件：intent/audience/constraint 全部匹配某已有 ActivationLink 的某组观测条件、仅 subject 未通过 `coreContained` 时触发。

```text
扫描 processed=0 的 subject_synonym_gap 事件，按
  (Normalize(query_subject), Normalize(observed_subject)) 排序后取字符序
  组成的无序 pair key 聚合（避免 A→B 与 B→A 被当成两条）：
    hit_count  = 事件数
    distinct_n = 不同 question_hash 数（经 trace_id JOIN traces）

达标（hit_count ≥ synonym_gap_min 且 distinct_n ≥ synonym_gap_distinct_min）：
  canonical = pair 中 hit_count 更高的一侧；相同则取字符序靠前者（确定性规则，
    避免同一 pair 反复达标时来回改变归一化方向）；
  term = 另一侧；

  已存在同 term 的 active/candidate/rejected 行 → 跳过（不重复产生候选；
    rejected 的 term 不自动复活，需人工在 UI 显式重新提交）；

  否则 INSERT subject_synonyms(term, canonical, source='gap_mined',
    status='candidate', created_from=支撑事件 id 列表)，
  写 learning_results(action='synonym_candidate', object_type='subject_synonym',
    object_id=synonym_id, status='pending_confirm', reason 含 hit_count/distinct_n,
    event_ids=支撑事件 id 列表)；

synonym_auto_promote=true 时：直接 status='active'，
  learning_results status='applied'，confirmed_by='auto'（与 auto_promote 同构）；

事件标记 processed=1（无论是否达标、是否已存在同名候选，该事件已被消费）。
```

confirm/reject 由 Activation 模块的 `POST /subject-synonyms/:id/confirm|reject` 执行（见 `activation.md` 步骤 3a），本步骤只负责产生候选，不做状态迁移。

### 步骤 3：链接信号累积与状态判定

对 processed=0 的 activation_success / activation_failure / user_correction（含 link_ids）事件，按 link_id 分组聚合，结合窗口内历史事件做判定：

```text
窗口统计（event_window_days 内，含本批）：
  success_direct_n     = activation_success 事件数（payload.role="direct"）
  success_supporting_n = activation_success 事件数（payload.role="supporting"）
  success_n            = success_direct_n（晋升判定只认 direct，见下）
  distinct_n  = role="direct" 的 success 事件的不同 question_hash 数
                （经 trace_id JOIN traces；role=supporting 不计入，理由同下）
  failure_n   = activation_failure 事件数
              + user_correction(含该 link_id) 事件数 × correction_weight
              （supporting 角色的 activation_success 不产生 failure，
              不稀释 failure_n，也不需要单独扣减）

判定（按链接当前状态）：
  candidate：
    success_n ≥ promote_success_min 且 distinct_n ≥ promote_distinct_min
      → 晋升判定达标（进入步骤 5 的确认流）
      （只用 success_direct_n / distinct_n：一条链接只被反复当 supporting
      引用、从未真正是某次答案的直接依据，还不足以证明它本身可作为独立
      激活入口被信赖，见 docs/design/precompile.md「反复使用」条）
  verified：
    failure_n ≥ weaken_failure_min 且
    failure_n / (success_n + failure_n) ≥ weaken_ratio_min
      → TransitionLink(verified → weakened, reason 含统计数字, event_ids)
      （分母沿用 success_n=success_direct_n；supporting 命中不参与稀释
      failure 占比，避免"这段时间恰好没被当作 direct 引用"被误判成失效）
  weakened：
    (success_direct_n + success_supporting_n) ≥ reverify_success_min
      且窗口内 failure_n == 0
      → TransitionLink(weakened → verified, action=reverify)
      （reverify 只需证明"这条路径仍然有效"，direct/supporting 都算数，
      与"首次晋升需要 direct 主导"的门槛不同）

计数更新：无论是否迁移，对每条链接 UpdateStats(成功增量, 失败增量)；
  成功增量 = success_direct_n + success_supporting_n（adopt_count 是展示用
  累计值，不区分角色；角色权重只影响上面的晋升/降权/reverify 判定逻辑）；
目标 KP lifecycle != current 的链接：跳过一切强化（不晋升、不 reverify、
  不累加 adopt_count），但失败与降权判定照常——过期知识只降不升；
处理完的事件标记 processed=1。
```

### 步骤 4：闲置淘汰

```text
candidate 且 created_at 距今 > candidate_idle_days 且窗口内无任何事件：
  → TransitionLink(candidate → deprecated, reason="idle_candidate")
weakened 且 status_changed_at 距今 > deprecate_idle_days 且窗口内无 success：
  → TransitionLink(weakened → deprecated, reason="idle_weakened")
```

### 步骤 5：晋升确认流

```text
auto_promote = false（默认）：
  晋升达标 → 写 learning_results(action=promote, status=pending_confirm,
  reason 含 success_n / distinct_n)；链接保持 candidate；
  同一链接已有 pending_confirm 的 promote 时不重复生成；
  人工在 Page 调 POST /activation-links/:id/confirm →
    activation 模块执行迁移，并将该 pending result 置 applied
    （confirmed_by=manual）；reject 同理置 rejected。

auto_promote = true：
  直接 TransitionLink(candidate → verified)，
  learning_results status=applied，confirmed_by=auto。
```

### 步骤 6：gap 聚合与 Wiki / 重编译信号

```text
knowledge_gap 聚合逻辑沿用 MVP（UPSERT knowledge_gaps ON CONFLICT(question_terms)，
  hit_count += 1），额外从 payload 取 reason 做两件事：
    reason_counts：对该字段做 JSON 内自增（不存在则置 1），一次 UPSERT 只增量
      更新命中的那个 key，不重写整个对象；
    last_reason = 当前 reason；
    last_trace_id = 当前事件的 trace_id（learning_events 行自带，无需额外查询）；
  达 gap_hit_threshold 时额外写 learning_results(action=gap_flag,
  object_type=knowledge_gap, reason 含 last_reason)，纳入统一审计；

Wiki 候选计算：qualifying KP 定义与概念级 ready 判定口径见 wiki.md 步骤 3
  "输入收集"/"概念级 ready 判定"（2026-07-29 修订：qualifying KP 不再要求
  confident_count 达标，只要求 lifecycle=current 且 ActivationLink
  status=verified；概念级新增连贯性的 related/contradicts 区分，不再
  只看连接总数）；recommendation=ready 的候选写
  learning_results(action=wiki_candidate, status=pending_confirm,
  object_type=wiki_page, object_id=entry_id)——Wiki 编译经人工确认触发，
  见 wiki.md 步骤 2；

已发布 wiki 页面依赖的 KP 出现 lifecycle != current：
  标记页面 needs_recompile（调 Wiki 模块接口）并写
  learning_results(action=recompile_flag)。

主题页候选（两层架构，口径见 wiki.md 步骤 8「主题候选识别」；2026-08-03
  修订：不再从已发布概念页的图连通性事后推导，改为对真实提问的四元组
  聚类，设计依据 `docs/design/wiki.md`「主题：从真实使用中识别，而不是从
  已发布词条事后聚类」）：
  窗口内 traces 按归一化四元组 (subject, intent, audience, constraint_text)
  分组（口径同本节「问题复杂度观测量」的分组，含 subject 同义词归一化，
  但不要求 confident——主题候选要回答的是"有没有人反复问"，与答没答上
  无关）；分组同时满足 distinct_question_count ≥
  wiki.topic_cluster_min_questions、days_active ≥
  wiki.topic_cluster_min_days_active 时是一个主题候选；在候选范围内对
  知识点做语义检索、筛出 qualifying 的部分、按 entry_id 分组（未发布
  但满足概念级 ready 判定的分组随批写 wiki_candidate）、核验关联与整体
  可靠度两项二阶准入，满足后建 draft 壳页并写
  learning_results(action=topic_page_candidate, object_type=wiki_page,
  object_id=壳页 page_id, status=pending_confirm)——object_id 用页面 id
  而不是四元组分组指纹，标识天然唯一、人工确认对象即一个具体页面；
  二阶编译同样经人工确认触发，见 wiki.md 步骤 8。

页面关系重算：跨 Source KPN 新增后重算涉及的页面对关系
  （纯程序派生，不调 LLM，不产生 learning_results，见 wiki.md 步骤 7b；
  这批 related / contradicts 关系仍然计算，只是不再用于事后求连通分量
  产生主题候选，而是用于步骤 8 的「二阶编译准入·关联」核验）。

报告提示项（只进报告，不产生 learning_results）：
  topic_signal_underfilled：四元组分组满足稳定簇判定，但候选范围内没有
    qualifying KP，或未通过二阶准入（关联 / 整体可靠度）——附四元组摘要、
    distinct_question_count、days_active、未达标原因，作为内容采集
    优先级信号；
  wiki_draft_reflow：origin='wiki_draft' 的 Source 及其产出 KP 数、
    被跳过的自体祖先边数（见 wiki.md 步骤 10「回流的自体循环」），
    用于发现系统在自己身上打转；
  topic_decompose：topic_decompose_signal 的聚合——按主题页分组的信号数、
    resolved_member_page_ids 平均成员数、resolved_outside_count > 0 的
    占比（后者持续偏高说明该主题页成员边界漏了知识，提示重编译）。
```

### 步骤 7：报告扩展

报告 JSON 在 MVP 结构上新增一节：

```json
"learning_actions": {
  "created_candidates": N, "promoted": N, "pending_promotions": N,
  "weakened": N, "reverified": N, "deprecated": N,
  "actions": [
    { "result_id": "...", "action": "promote", "object_id": "link_xxx",
      "reason": "窗口内 4 次 success，3 个不同问题", "status": "pending_confirm" }
  ]
}
```

`summary` 增加 `fast_path_rate`（窗口内 traces.path_type=fast 占比），用于验证「学习改变检索行为」的 V1 目标。

#### 问题复杂度观测量（新增一节，只观测不驱动）

设计依据：`docs/design/cognitive-routing.md`「复杂度是相对已有知识而言的」。认知路由要按问题形态查表决定路径，而这张映射表的修正需要数据基础——「这类问题历史上实际需要几块知识、有没有被现成结论满足过」。V1 已经在积累这些信号，本节把它们聚合成可读的观测量，**为 V3 的路由映射表准备依据**，V1 自身不据此改变任何行为。

```text
分组口径：窗口内（event_window_days）的 traces 按四元组条件组分组——
  (subject, intent, audience, constraint_text)，归一化口径与
  activation.Matcher 的条件匹配一致（见 activation.md 步骤 2），
  保证「同一类问题」的定义在检索侧和学习侧是同一个；
  组内 question_count < study.complexity_min_questions（默认 3）的
  不出报告项——样本太少的组只是噪声。

每组指标（全部来自既有字段，不新增采集点）：
  question_count           组内问答数；
  path_distribution        {wiki, fast, full} 各自次数（traces.path_type）；
  avg_direct_point_count   direct_point_ids 数量均值——
                           「这类问题需要几块知识」最直接的度量；
  wiki_satisfied_ratio     path_type=wiki 占比，即被现成概念页结论
                           满足过的比例；
  skeleton_used_count      traces.skeleton_page_id 非空次数
                           （主题页提供过骨架，见 wiki.md 步骤 8）；
  cross_member_ratio       该组的 topic_decompose_signal 中
                           resolved_member_page_ids ≥ 2 的占比——
                           「必须跨成员拼起来才答得了」的比例；
  outside_ratio            resolved_outside_count > 0 的占比——
                           答案落在主题页成员边界之外的比例。

派生标签 complexity_hint（只作展示，不参与任何判定）：
  simple     现成结论覆盖得住（wiki_satisfied_ratio 高、
             avg_direct_point_count 低）；
  composite  需要多块知识拼合（cross_member_ratio 高 或
             avg_direct_point_count 高）；
  uncovered  知识本身不够（outside_ratio 高、组内 gap 事件多）。

  **阈值不预先拍定**：先在真实 traces 上测出基线分布，再回填到本文档，
  没有基线的阈值是假精确（与 docs/impl/v2/readme.md「两步制定标」同一立场）。
  在阈值确定之前，报告只输出原始指标，complexity_hint 留空。

边界（硬）：
  只进报告，不产生 learning_results、不改 ActivationLink 状态、
  不触发 Wiki 编译或重编译、不改变任何检索行为——V1 没有路由层
  （见 docs/impl/v1/readme.md「V1 不做什么」），这些数字的消费方是
  人工阅读与 V3 的路由映射表；
  不新增 LLM 调用，全部是对既有 traces / learning_events 的 SQL 聚合。
```

报告 JSON 新增：

```json
"question_complexity": {
  "groups": [
    { "subject": "...", "intent": "...", "audience": "...", "constraint": "...",
      "question_count": 7,
      "path_distribution": { "wiki": 1, "fast": 2, "full": 4 },
      "avg_direct_point_count": 3.4,
      "wiki_satisfied_ratio": 0.14,
      "skeleton_used_count": 3,
      "cross_member_ratio": 0.6,
      "outside_ratio": 0.2,
      "complexity_hint": null }
  ]
}
```

`knowledge_gaps` 每条记录新增 `reason_counts` / `last_reason` / `last_trace_id`，`recommendation` 由固定的「补充材料」改为按 `last_reason` 细化：

```text
last_reason == "no_candidates"  → recommendation = "补充材料"
                                   （链路没召回到候选，大概率是真的缺内容）
last_reason == "judge_filtered" → recommendation = "语义提取待核对"
                                   （候选存在但被判无关，指向
                                    docs/impl/v1/semantics-curation.md 的人工修正流程，
                                    不需要导入新材料）
last_reason == "answer_error"   → recommendation = "生成异常，需查日志"
                                   （非知识缺口，是系统性错误）
```

### 步骤 8：HTTP API 扩展

```text
GET  /study/results
  查询参数：action、object_type、object_id、status、limit（默认 50）
  响应：learning_results 列表；object_type=activation_link 时 JOIN 补充
        question_terms / point_summary

GET  /study/results/:id
  响应：完整字段 + event_ids 展开后的事件摘要列表（审计视图数据源）

POST /study/run、GET /study/reports* 等 MVP 接口保留，
/study/run 响应扩展执行摘要（各动作计数）。

GET /study/gaps 响应每条记录新增 reason_counts / last_reason / last_trace_id 字段；
  查询参数新增 reason（按 last_reason 精确过滤，如只看 judge_filtered 的缺口）；
  last_trace_id 不内联展开证据详情——客户端按需自行
  GET /traces/:last_trace_id → answer_id → GET /answers/:id 取
  evidence_snapshot（direct/supporting/filtered，含 KU 内容与 source_ref 所属文档），
  避免 /study/gaps 响应体因内联大段 KU 原文而膨胀。
```

概念演化模块（顺序 10）实现时，在本任务链末尾追加概念候选扫描子任务（新增聚类 + 合并统计 + 候选过期），配置、表结构与执行规则见 `concept-evolution.md` 步骤 2，本模块实现时无需预留。

## 依赖

```text
基础设施：SQLite、time.Ticker、结构化日志、HTTP 框架
Trace：     learning_events（读+标记 processed）、traces、
            question_kp_cooccurrence（只读）；knowledge_gap 事件 payload 需
            trace.md 补充 reason 字段（依赖 retrieval.md 的 EvidenceSet.GapReason，
            见「knowledge_gaps 表扩展」节——本节仅描述 study 消费侧，两份
            上游文档待确认后补齐）
Activation：TransitionLink / CreateLink / UpdateStats（状态变更唯一路径）
Lifecycle： 判定强化跳过时读取 knowledge_points.lifecycle
Wiki：      needs_recompile 标记接口（未实现时 no-op）
Trace：     subject_synonym_gap 事件的产生方（本模块只读+标记 processed）
Activation：subject_synonyms 表的 CRUD 与 confirm/reject 由 Activation 模块提供，
            本模块只调用 InsertSynonymCandidate 写候选、不做状态迁移
```

## 完成标准

```text
共现达标与 activation_gap 复现均能创建 candidate，动作可审计、幂等；
同一 point 用不同问法反复达标只刷新已有链接条件，不产生第二条链接、
  不重复写 learning_results；条件不变时刷新是空操作（不 touch updated_at）；
构造 4 次不同问题的 success 事件后，candidate 产生 pending_confirm 晋升；
  人工 confirm 后链接 verified、result applied；reject 后 rejected；
auto_promote=true 时同场景直接晋升；
verified 链接构造 3 次 failure（比值达标）后降权，weakened 再构造
  2 次 success 且无 failure 后 reverify 回 verified；
user_correction 按 correction_weight 计入失败累积；
目标 KP superseded 后该链接不再被强化，降权判定仍生效；
闲置 candidate / weakened 按天数阈值自动淘汰；
每个状态迁移在 learning_results 中可回溯到事件列表与 reason；
报告含 learning_actions 与 fast_path_rate；
knowledge_gap 事件按 payload.reason 正确累加 reason_counts、更新 last_reason /
  last_trace_id，同一 gap 多次命中不同 reason 时三类计数互不覆盖；
GET /study/gaps 可按 reason 过滤，recommendation 按 last_reason 正确分级；
  last_trace_id 能正确串联到 GET /traces/:id → GET /answers/:id 拿到完整
  evidence_snapshot（judge_filtered 场景下 filtered 证据非空）；
事件消费幂等：同一事件不重复计入（processed 标记 + 单 goroutine 调度）；
subject_synonym_gap 按 pair 聚合达标后生成 pending_confirm 候选，
  同一 pair 不重复生成、reject 后的 term 不自动复活；
  auto_promote=true 时同场景直接 active；
fake 环境下全部动作路径测试稳定运行。
```
