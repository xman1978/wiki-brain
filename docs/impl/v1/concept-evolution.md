# 概念演化实现路径（V1）

## 职责

消费 `activation_gap`（concept_gap 层级）与采用共现统计，形成概念**新增**与**合并**候选；候选经人工确认后在事务内执行。设计依据：`docs/design/concept-evolution.md`。

在 V1 实现顺序中位于第 10 位（page 之后）：依赖 trace（事件扩展）、study（周期任务）、wiki（needs_recompile 接口）均已完成。

## 与设计文档的 V1 适配

设计文档描述的是目标架构，以下三点按 V1 实际结构适配，**不是**对设计的推翻：

```text
1. 合并信号的在线产生点（Concept 匹配器 ambiguous_match）推迟到 V2：
   V1 在线链路没有 Concept 匹配器（只有 Domain 预过滤；Concept 归属
   由导入期 unit_concept_match 单选写入，无匹配分数）。
   V1 合并信号只用离线共同采用统计（设计产生点 3）。

2. 合并/新增不涉及 ActivationLink 迁移：
   V1 链接挂 Session 四元组条件 → point_id，不经 Concept。
   概念迁移只改 knowledge_units.concept_id 和 Wiki 标记。

3. 拆分候选推迟到 V2：
   依赖同一概念下事件的 scene / goal 分布聚类，V1 事件量不足以
   支撑可靠的分布判定，且 V1 概念承载的在线行为有限，收益低。
```

## 数据结构

### concepts 表扩展（migration）

```sql
ALTER TABLE concepts ADD COLUMN merged_into TEXT REFERENCES concepts(concept_id);
-- 非空 = 已被合并，不再是当前认知入口；保留行用于追溯
ALTER TABLE concepts ADD COLUMN origin TEXT NOT NULL DEFAULT 'preset';
-- preset / evolved（人工确认新增的概念）
```

`merged_into` 非空的概念在以下位置一律排除：`unit_concept_match` 的候选概念列表、Wiki 候选计算（study 步骤 6）、Page 的概念列举。历史数据不迁移：既有 KU 指向被合并概念的 `concept_id` 在合并事务中统一改写（见步骤 3）。

**preset UPSERT 规则调整**（foundation 启动加载）：按 concept_id UPSERT 时只更新 name / description，**不得**清除 `merged_into`、不得改写 `origin`。被合并的概念不因 preset 中仍存在而恢复为当前入口。

### concept_candidates 表

```sql
CREATE TABLE concept_candidates (
    candidate_id   TEXT PRIMARY KEY,
    kind           TEXT NOT NULL,
    -- add / merge（split 预留枚举值，V2 启用）
    domain_id      TEXT REFERENCES domains(domain_id),
    -- kind=add：建议归属领域（可为 NULL，确认时人工指定）
    suggested_name TEXT,
    -- kind=add：程序从簇内 KU center 高频词提取的建议名，确认时可改
    merge_from     TEXT NOT NULL DEFAULT '[]',
    -- kind=merge：JSON 数组，涉及的两个 concept_id
    point_ids      TEXT NOT NULL DEFAULT '[]',
    -- 关联 KnowledgePoint 集合（JSON 数组）
    evidence       TEXT NOT NULL DEFAULT '{}',
    -- 统计依据 JSON：事件数、不同问题数、KP 重叠度、共同采用次数等
    event_ids      TEXT NOT NULL DEFAULT '[]',
    -- 支撑候选的 learning_event event_id 列表（JSON 数组）
    status         TEXT NOT NULL DEFAULT 'pending_confirm',
    -- pending_confirm / applied / rejected / expired
    last_signal_at DATETIME NOT NULL,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_cc_status ON concept_candidates(status);
CREATE INDEX idx_cc_kind ON concept_candidates(kind);
```

### activation_gap payload 扩展（trace 模块）

`learning_events` 表结构不变，`activation_gap` 的 payload 增加两个字段：

```json
{ "question_terms": "...", "direct_point_ids": ["..."],
  "gap_level": "link_gap | concept_gap", "null_concept_ratio": 0.8 }
```

**判定时机与规则**（trace_write 组装 payload 时，一次数据库 join，不增加 LLM 调用）：

```text
direct_point_ids -> KP -> 所属 KU -> concept_id；
无概念归属（concept_id 为 NULL，或其概念 merged_into 非空）的 KP 占比
  ≥ concept_null_ratio_min -> gap_level = concept_gap；
否则 -> gap_level = link_gap。
```

存量事件无 `gap_level` 字段，Study 消费时按 `link_gap` 处理（行为与现状一致，向后兼容）。

## 配置项（config.yml: study 节扩展）

```yaml
study:
  # —— 概念演化（V1 新增）——
  concept_null_ratio_min:      0.7   # concept_gap 判定：无概念归属 KP 占比下限
  concept_add_event_min:       5     # 新增候选：簇内 concept_gap 事件数下限
  concept_add_distinct_min:    3     # 且来自 ≥ N 个不同 question_hash
  concept_add_overlap_min:     0.5   # 簇内事件 KP 集合两两 Jaccard 重叠下限
  concept_merge_cooccur_min:   5     # 合并候选：概念对共同采用次数下限
  concept_merge_overlap_min:   0.6   # 且共同采用中 KP 重叠比例下限
  concept_candidate_idle_days: 60    # 候选无新信号自动过期
  concept_event_window_days:   90    # 聚合统计的时间窗口（长于链接窗口）
```

单一条件不触发候选：新增需事件数、问题数、重叠度全部达标；合并需共现次数与重叠比例同时达标（只摇摆不重叠可能是问题模糊，只重叠不共现可能是正常邻近概念——V1 无摇摆信号，共现统计承担其角色）。

## 实现步骤

### 步骤 1：trace 写入扩展

修改 trace 模块 `activation_gap` 事件的 payload 组装，按上述规则写入 `gap_level` 和 `null_concept_ratio`。此改动随本模块 migration 一起上线，早于 Study 扫描启用。

### 步骤 2：Study 概念候选扫描

挂在既有 Study Ticker 任务链的末尾（步骤 6 之后），共两个子任务：

**新增聚类**：

```text
取窗口内 gap_level = concept_gap 且未 processed 的事件；
贪心聚簇：按 KP 集合 Jaccard ≥ concept_add_overlap_min 并簇；
簇达标（事件数 / 不同问题数 / 重叠度）->
  创建或更新 concept_candidates(kind=add)：
    point_ids   = 簇内 KP 并集；
    domain_id   = 簇内 KU 所属 source 的 domain_id 多数决（可为 NULL）；
    suggested_name = 簇内 KU center 分词后的高频词组合
                     （程序统计，不调 LLM，确认时人工可改）；
  写 learning_results(action=concept_add_candidate,
    object_type=concept_candidate, status=pending_confirm)；
同簇已有 pending_confirm 候选（point_ids 重叠 ≥ 阈值）时更新该候选的
  evidence / event_ids / last_signal_at，不重复建行。
```

**合并统计**：

```text
取窗口内 traces 的 cited point_ids -> KU -> concept_id，
  对每次问答提取涉及的概念对（去重）；
概念对共现计数 ≥ concept_merge_cooccur_min 时，
  计算共同采用中的 KP 重叠比例；
两条件同时达标 -> concept_candidates(kind=merge, merge_from=[A,B])
  + learning_results(action=concept_merge_candidate)；
排除 merged_into 非空的概念参与统计。
```

**过期**：`last_signal_at` 距今超过 `concept_candidate_idle_days` 的 pending_confirm 候选置 expired，对应 learning_results 同步更新。

### 步骤 3：确认与执行 API

```text
GET  /concepts/candidates?status=pending_confirm   列出候选（含完整 evidence）
POST /concepts/candidates/:id/confirm              确认执行
POST /concepts/candidates/:id/reject               拒绝
```

confirm 的执行在**单个事务**内完成：

```text
kind=add：
  body 可覆盖 suggested_name / domain_id（domain_id 为 NULL 时必须指定）；
  INSERT concepts(origin='evolved')；
  簇内 KP 所属 KU 中 concept_id 为 NULL 的行，UPDATE 为新概念；
  不自动创建 ActivationLink——新概念的激活路径走正常的
  candidate 链接形成与验证流程；

kind=merge：
  body 指定 target（并入 merge_from 中的哪一个）；
  被并概念下所有 KU 的 concept_id UPDATE 为 target；
  被并概念 merged_into = target；
  涉及两概念的已发布 Wiki 页面调 Wiki 模块接口标记 needs_recompile
  （不自动重编译，与 wiki.md 一致）；

两种 kind 共同：
  candidate 置 applied；
  对应 learning_results 置 applied（confirmed_by=manual），
  reason 补记执行摘要（迁移 KU 数、标记页面数）。
```

reject：candidate 与 learning_results 置 rejected，不做任何结构改动。概念演化**没有** auto 模式——与链接晋升的 `auto_promote` 不同，结构性改动一律人工确认（设计文档第 4 节）。

### 步骤 4：当前入口排除与 preset 规则

```text
unit_concept_match（unit 模块步骤 5）：候选概念列表排除 merged_into 非空；
Wiki 候选计算（study 步骤 6）：同上排除；
foundation preset 加载：UPSERT 不清除 merged_into / origin。
```

### 步骤 5：报告与 Page 扩展

Study 报告 JSON 新增 `concept_candidates` 节（pending 候选摘要 + 窗口内 concept_gap 统计）。Page 审计视图增加概念候选列表：展示依据（事件数、问题列表、KP 集合、重叠度），提供 confirm（含改名 / 选 target 表单）和 reject 入口，风格沿用链接晋升确认流。

## 依赖

```text
Foundation：migration（concepts 两列 + concept_candidates 表）、preset 规则调整
Trace：     activation_gap payload 扩展（步骤 1，本模块内改动）
Study：     Ticker 任务链扩展（步骤 2），learning_results 复用
Unit：      unit_concept_match 概念列表排除规则（步骤 4）
Wiki：      needs_recompile 标记接口（已有，wiki.md）
Page：      候选审计与确认视图（步骤 5）
```

## 完成标准

```text
gap_level 判定正确：无概念归属占比达阈值的 activation_gap 带
  concept_gap，其余 link_gap；存量无字段事件按 link_gap 兼容消费；
新增聚类：fake LLM 下构造同主题 concept_gap 事件，事件数 / 不同问题数 /
  重叠度三条件逐项验证——任一不达标不生成候选，全部达标生成且
  suggested_name / domain_id / evidence 正确；
合并统计：两概念 KP 反复共同采用生成 merge 候选；仅共现或仅重叠
  单条件不生成；merged_into 概念不参与统计；
confirm 执行：add 后新概念可被 unit_concept_match 选中且 origin=evolved；
  merge 后被并概念 KU 全部迁移、merged_into 写入、Wiki 页面
  needs_recompile、被并概念不再出现在任何当前入口位置；
preset 重放不复活 merged_into 概念、不改写 evolved 概念；
reject 与 idle 过期路径正确；同簇候选更新不重复建行；
全部路径在 fake LLM 与 fake 队列下经 go test 验证。
```
