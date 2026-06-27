# Study 实现路径

## 职责

MVP 阶段 Study 的目标是**验证并呈现**：Rerank 质量信号经 Trace 积累后，能否从 `question_kp_cooccurrence` 中识别出有意义的「问题-KP」稳定路径。

Study 定期扫描共现数据，生成结构化学习报告，报告中包含多维度统计和明确结论（可生成的 ActivationLink 候选和 concept Wiki 候选），供人工审核决策。审核通过后，人工通过 API 确认晋升，Study 不自动修改任何知识结构。

## 核心组件

```text
共现扫描器（定期扫描 question_kp_cooccurrence，标记达到阈值的候选）
知识盲区聚合器（消费 knowledge_gap 事件，聚合频繁出现的盲区）
学习报告生成器（计算多维度统计，输出 ActivationLink 和 Wiki 候选结论）
定时调度器（Go time.Ticker，默认每小时一次；完成后自动生成报告）
HTTP API（触发、查询报告、审核确认接口）
```

## 数据结构

### link_candidates 表

```sql
CREATE TABLE link_candidates (
    candidate_id    TEXT PRIMARY KEY,
    question_terms  TEXT NOT NULL,
    point_id        TEXT NOT NULL REFERENCES knowledge_points(point_id),
    confident_count INTEGER NOT NULL DEFAULT 0,
    hit_count       INTEGER NOT NULL DEFAULT 0,
    flagged_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(question_terms, point_id)
);

CREATE INDEX idx_lc_point_id ON link_candidates(point_id);
```

### knowledge_gaps 表

```sql
CREATE TABLE knowledge_gaps (
    gap_id         TEXT PRIMARY KEY,
    question_terms TEXT NOT NULL,
    question       TEXT NOT NULL,
    hit_count      INTEGER NOT NULL DEFAULT 1,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(question_terms)
);
```

### study_reports 表

```sql
CREATE TABLE study_reports (
    report_id    TEXT PRIMARY KEY,
    period_days  INTEGER NOT NULL DEFAULT 30,
    -- 统计覆盖的时间窗口（天数）
    content      TEXT NOT NULL,
    -- 完整报告 JSON（见报告结构定义）
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

只保留最近 N 份报告（默认 10 份），旧报告在写入新报告时清理。

## 报告结构定义

每次生成的报告为 JSON，结构如下：

```json
{
  "report_id":    "...",
  "generated_at": "2025-01-01T10:00:00Z",
  "period_days":  30,

  "summary": {
    "total_traces":          总问答次数,
    "confident_count":       confident 级别次数,
    "partial_count":         partial 级别次数,
    "gap_count":             gap 级别次数,
    "confident_rate":        confident 比率（小数）,
    "total_cooccurrence_pairs": 共现表行数,
    "candidates_flagged":    已达阈值的候选数
  },

  "activation_link_candidates": [
    {
      "question_terms":     "问题关键词",
      "point_id":           "kp_xxx",
      "point_summary":      "KP 摘要文本",
      "unit_topic":         "所属 KU 主题",
      "concept_id":         "concept_xxx",
      "concept_name":       "概念名称",
      "stats": {
        "confident_count":      12,
        "hit_count":            15,
        "signal_purity":        0.80,
        "activation_breadth":   5,
        "short_path_rate":      0.75,
        "has_kpn_neighbors":    true,
        "last_seen_at":         "2025-01-01T..."
      },
      "recommendation":  "strong | candidate",
      "reason":          "信号纯度 0.80，激活广度 5，短路径占比 0.75"
    }
  ],

  "wiki_candidates": [
    {
      "concept_id":          "concept_xxx",
      "concept_name":        "概念名称",
      "domain_id":           "domain_xxx",
      "qualifying_point_ids": ["kp_1", "kp_2", "kp_3", "kp_4"],
      "stats": {
        "qualifying_kp_count":   4,
        "avg_confident_count":   9.5,
        "kpn_connection_count":  5,
        "days_active":           18
      },
      "recommendation":  "ready | needs_more_data",
      "reason":          "4 个 KP 达到 Wiki 阈值，KPN 连接 5 条，活跃天数 18 天"
    }
  ],

  "knowledge_gaps": [
    {
      "question_terms":  "问题关键词",
      "question":        "最近触发 gap 的原始问题",
      "hit_count":       8,
      "recommendation":  "补充材料"
    }
  ]
}
```

### 各字段计算方式

**summary**

```text
total_traces / confident_count / partial_count / gap_count：
  SELECT retrieval_quality, COUNT(*) FROM traces
  WHERE created_at >= now() - period_days GROUP BY retrieval_quality；

total_cooccurrence_pairs：SELECT COUNT(*) FROM question_kp_cooccurrence；
candidates_flagged：SELECT COUNT(*) FROM link_candidates。
```

**activation_link_candidates 各维度**

```text
signal_purity = confident_count / hit_count
  来源：link_candidates 快照

activation_breadth：
  SELECT COUNT(DISTINCT question_terms) FROM question_kp_cooccurrence
  WHERE point_id = ? AND confident_count > 0
  不同问题关键词集合都能以高置信命中同一 KP，说明该 KP 在多类问题下均有效

short_path_rate（应用层计算，不依赖 SQLite json_each）：
  1. SQL 查询时间窗口内的 confident traces（加时间窗口过滤，不做全表扫描）：
       SELECT path, direct_point_ids FROM traces
       WHERE retrieval_quality = 'confident'
         AND created_at >= datetime('now', '-' || report_period_days || ' days')
  2. Go 代码中解析每条 direct_point_ids JSON 数组，过滤出包含目标 point_id 的记录；
  3. 统计过滤结果中 path = 'short' 的比例（total = 0 时结果为 0）；
  短路径意味着该 KP 足以直接回答问题，ActivationLink 价值最大

has_kpn_neighbors：
  SELECT COUNT(*) > 0 FROM knowledge_point_relations
  WHERE source_point_id = ? OR target_point_id = ?
  邻居存在说明该 KP 处于知识网络中而非孤立节点
```

**recommendation 判断逻辑**

```text
strong：signal_purity ≥ 0.7 AND activation_breadth ≥ 3 AND short_path_rate ≥ 0.6
candidate：满足 link_candidates 阈值，但未达到 strong 全部条件
```

**wiki_candidates 各维度**

```text
qualifying_kp_count：
  同 concept 下 confident_count ≥ study.wiki_confident_min 的 KP 数量

avg_confident_count：
  qualifying KP 的 confident_count 均值

kpn_connection_count：
  qualifying KP 之间在 knowledge_point_relations 中存在的连接条数
  反映这批 KP 是否形成连贯的知识网络，而非互相孤立

days_active：
  SELECT COUNT(DISTINCT DATE(last_seen_at)) FROM question_kp_cooccurrence
  WHERE point_id IN (qualifying_point_ids)
  活跃天数分散说明需求持续存在，而非集中爆发的临时现象
```

**recommendation 判断逻辑**

```text
ready：qualifying_kp_count ≥ study.wiki_kp_min AND kpn_connection_count ≥ 1
needs_more_data：qualifying_kp_count 接近但未达 wiki_kp_min，或 kpn_connection_count = 0
```

## 配置项（config.yml: study 节）

```yaml
study:
  schedule_interval:       "1h"   # 扫描周期
  candidate_confident_min: 5      # 标记候选所需最低 confident_count
  candidate_ratio_min:     0.6    # confident_count / hit_count 最低比值
  wiki_kp_min:             4      # 触发 Wiki 候选所需同 concept 下 KP 最低数量
  wiki_confident_min:      8      # Wiki 候选 KP 的最低 confident_count
  gap_hit_threshold:       3      # gap 累积次数告警阈值
  scan_batch_size:         200    # 每次从 cooccurrence 读取的最大行数
  report_period_days:      30     # 报告统计时间窗口
  report_max_keep:         10     # 最多保留的历史报告数量
```

## 实现步骤

### 步骤 1：实现共现扫描器

```sql
SELECT question_terms, point_id, confident_count, hit_count
FROM question_kp_cooccurrence
WHERE confident_count >= ?
  AND CAST(confident_count AS FLOAT) / CAST(hit_count AS FLOAT) >= ?
ORDER BY confident_count DESC
LIMIT ?;
```

对每条结果 UPSERT `link_candidates`（ON CONFLICT 更新 confident_count / hit_count 快照）。

### 步骤 2：实现知识盲区聚合器

扫描 `learning_events` 中 `processed=0 AND event_type='knowledge_gap'` 的事件：

```text
从 payload 取 question；从关联 traces 取 question_terms；

UPSERT knowledge_gaps ON CONFLICT(question_terms):
  hit_count += 1、question = 当前问题、updated_at = now()；

hit_count 首次达到 study.gap_hit_threshold：
  记录 warn 日志（含 question_terms）；

标记 learning_events.processed = 1。
```

### 步骤 3：实现学习报告生成器

每次调度完成步骤 1 和步骤 2 后，自动触发报告生成：

```text
计算 summary（时间窗口内 traces 分布统计）；
计算 activation_link_candidates：
  遍历 link_candidates，按维度公式逐条计算 signal_purity / activation_breadth /
  short_path_rate / has_kpn_neighbors，JOIN knowledge_points / knowledge_units 补充展示信息，
  按 recommendation 分组排序（strong 优先）；
计算 wiki_candidates：
  按 concept_id 分组 qualifying KP，计算 kpn_connection_count / days_active；
计算 knowledge_gaps：
  取 hit_count 最高的前 20 条；
组装为报告 JSON，INSERT INTO study_reports；
清理超出 report_max_keep 的旧报告（DELETE WHERE report_id NOT IN 最新 N 条）。
```

报告生成以 SQL 为主，辅以少量应用层 JSON 解析（如 short_path_rate 需解析 direct_point_ids），不调用 LLM。

### 步骤 4：定时调度

```text
首次执行延迟 1 分钟（避免与 Source / Unit 初始化任务竞争）；

每次调度顺序：
  步骤 1 共现扫描 → 步骤 2 gap 聚合 → 步骤 3 报告生成；

调度完成后 info 日志：本次新增候选数、处理事件数、报告 ID、耗时；
单次扫描异常：记录 error 日志，不中断后续调度。
```

### 步骤 5：暴露 HTTP API

```text
POST   /study/run
  立即触发一次完整扫描（含报告生成），返回执行摘要
  响应：{ "report_id": "...", "candidates_flagged": N, "gap_events_processed": K, "elapsed_ms": T }

GET    /study/reports
  响应：最近 report_max_keep 份报告的元信息列表
  [{ report_id, period_days, candidates_count, wiki_count, gap_count, created_at }]

GET    /study/reports/:id
  响应：完整报告 JSON（含 summary、候选列表和结论）
  用途：人工审核，决策哪些候选值得晋升或触发 Wiki 编译

GET    /study/reports/latest
  响应：最新一份完整报告，等价于 GET /study/reports/:latest_id

GET    /study/candidates
  查询参数：recommendation（strong / candidate）、limit（默认 50）
  响应：link_candidates 列表，附 point_summary / unit_topic / concept_name

GET    /study/gaps
  查询参数：min_hit_count、limit（默认 50）
  响应：knowledge_gaps 列表，按 hit_count 降序
```

## 依赖

```text
基础设施：SQLite、结构化日志、HTTP 框架、Go time.Ticker
Trace：依赖 question_kp_cooccurrence、learning_events、traces 表（只读）
Unit：JOIN knowledge_points / knowledge_units 补充候选展示信息（只读）
       JOIN knowledge_point_relations 计算 has_kpn_neighbors 和 kpn_connection_count（只读）
Foundation：JOIN concepts、domains 补充 Wiki 候选归属信息（只读）
```

## 完成标准

```text
定时调度正常运行，每轮结束后自动生成报告并写入 study_reports；
共现扫描正确 UPSERT link_candidates；
knowledge_gap 事件正确聚合，达阈值输出 warn 日志；
报告中 signal_purity / activation_breadth / short_path_rate 计算结果准确；
wiki_candidates 的 kpn_connection_count 正确统计 qualifying KP 之间的连接数；
recommendation 字段按判断逻辑正确分级（ActivationLink 候选：strong / candidate；Wiki 候选：ready / needs_more_data）；
GET /study/reports/latest 可返回完整报告供人工阅读；
超出 report_max_keep 的旧报告自动清理；
空数据场景下报告生成不崩溃，summary 各计数为 0；
与 Trace 并发运行时无竞争（Trace 写共现表，Study 只读）。
```
