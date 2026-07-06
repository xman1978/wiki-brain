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
    -- gap_flag / wiki_candidate / recompile_flag
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

## 配置项（config.yml: study 节扩展）

```yaml
study:
  # —— MVP 既有配置保留 ——
  schedule_interval:       "1h"
  candidate_confident_min: 5
  candidate_ratio_min:     0.6
  wiki_kp_min:             4
  wiki_confident_min:      8
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
```

## 实现步骤

每次调度顺序：步骤 1 → 2 → 3 → 4 → 5 之后沿用 MVP 的 gap 聚合与报告生成（步骤 6、7）。单步异常记录 error 日志，不中断本轮后续步骤。

### 步骤 1：共现扫描（沿用 MVP）

扫描 `question_kp_cooccurrence` UPSERT `link_candidates`，逻辑不变。

### 步骤 2：创建 candidate ActivationLink

两个来源，合并去重后创建：

```text
来源 A（共现达标）：
  link_candidates 中 confident_count ≥ candidate_confident_min
  且 ratio ≥ candidate_ratio_min，且 activation_links 中不存在同
  (question_terms, point_id) 的行；

来源 B（activation_gap 事件）：
  processed=0 的 activation_gap 事件，对 payload 中每个
  (question_terms, direct_point_id) 组合，查共现表该组合的
  confident_count ≥ 2 时视为达标（gap 事件本身即一次 confident 命中，
  要求至少再复现一次，避免单次问答直接产生候选）；

对每个达标组合：
  取该 question_terms 下最近一条 confident trace 的四元组
  （subject / intent / audience / constraint_text 列），归一化生成链接的
  条件字段：subject_terms / intent_terms / audience / constraint_terms
  （规则见 activation.md 数据结构；四元组为空时对应字段留空，
   该链接将走匹配器的回退路径）；
  CreateLink(question_terms, 四元组条件, point_id, created_from=事件/候选标识)；
  幂等（已存在则跳过）；deprecated 同条件被拒绝时记录日志跳过；
  写 learning_results(action=create_candidate, status=applied,
    reason 含 confident_count / ratio / 触发来源)；
  标记来源 activation_gap 事件 processed=1。
```

### 步骤 3：链接信号累积与状态判定

对 processed=0 的 activation_success / activation_failure / user_correction（含 link_ids）事件，按 link_id 分组聚合，结合窗口内历史事件做判定：

```text
窗口统计（event_window_days 内，含本批）：
  success_n   = activation_success 事件数
  distinct_n  = 这些 success 事件的不同 question_hash 数（经 trace_id JOIN traces）
  failure_n   = activation_failure 事件数
              + user_correction(含该 link_id) 事件数 × correction_weight

判定（按链接当前状态）：
  candidate：
    success_n ≥ promote_success_min 且 distinct_n ≥ promote_distinct_min
      → 晋升判定达标（进入步骤 5 的确认流）
  verified：
    failure_n ≥ weaken_failure_min 且
    failure_n / (success_n + failure_n) ≥ weaken_ratio_min
      → TransitionLink(verified → weakened, reason 含统计数字, event_ids)
  weakened：
    success_n ≥ reverify_success_min 且窗口内 failure_n == 0
      → TransitionLink(weakened → verified, action=reverify)

计数更新：无论是否迁移，对每条链接 UpdateStats(成功增量, 失败增量)；
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
knowledge_gap 聚合沿用 MVP（knowledge_gaps 表）；
  达 gap_hit_threshold 时额外写 learning_results(action=gap_flag,
  object_type=knowledge_gap)，纳入统一审计；

Wiki 候选计算沿用 MVP 报告逻辑；recommendation=ready 的候选写
  learning_results(action=wiki_candidate, status=pending_confirm,
  object_type=wiki_page, object_id=concept_id)——Wiki 编译经人工确认触发，
  见 wiki.md 步骤 2；

已发布 wiki 页面依赖的 KP 出现 lifecycle != current：
  标记页面 needs_recompile（调 Wiki 模块接口）并写
  learning_results(action=recompile_flag)。
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
```

概念演化模块（顺序 10）实现时，在本任务链末尾追加概念候选扫描子任务（新增聚类 + 合并统计 + 候选过期），配置、表结构与执行规则见 `concept-evolution.md` 步骤 2，本模块实现时无需预留。

## 依赖

```text
基础设施：SQLite、time.Ticker、结构化日志、HTTP 框架
Trace：     learning_events（读+标记 processed）、traces、
            question_kp_cooccurrence（只读）
Activation：TransitionLink / CreateLink / UpdateStats（状态变更唯一路径）
Lifecycle： 判定强化跳过时读取 knowledge_points.lifecycle
Wiki：      needs_recompile 标记接口（未实现时 no-op）
```

## 完成标准

```text
共现达标与 activation_gap 复现均能创建 candidate，动作可审计、幂等；
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
事件消费幂等：同一事件不重复计入（processed 标记 + 单 goroutine 调度）；
fake 环境下全部动作路径测试稳定运行。
```
