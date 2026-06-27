# Trace 实现路径

## 职责

Trace 在 Answer 完成后异步运行，以 Rerank 产出的证据质量为核心信号，对每次问答做质量分级，累积「问题关键词 × KnowledgePoint」共现统计，为 Study 提供可消费的学习材料。

Trace 不调用 LLM，不阻塞回答，不解释推理过程。只记录可以驱动长期记忆演化的事实。

## 核心组件

```text
质量分级器（从 AnswerResult 判断 confident / partial / gap）
关键词提取器（从 question 提取归一化词项，无需 LLM）
共现计数器（更新 question_kp_cooccurrence，每次问答均执行）
Learning Event 写入器（gap / user_correction 立即写入；candidate_link 由 Study 定期扫描生成）
用户反馈处理器（按需触发，补充 user_correction 信号）
HTTP API
```

## 数据结构

### traces 表

```sql
CREATE TABLE traces (
    trace_id          TEXT PRIMARY KEY,
    answer_id         TEXT NOT NULL REFERENCES answers(answer_id),
    question          TEXT NOT NULL,
    question_hash     TEXT NOT NULL,
    -- 归一化问题文本的 SHA256 哈希，用于识别重复问题
    question_terms    TEXT NOT NULL,
    -- 归一化后的问题关键词（空格分隔），供共现统计使用
    retrieval_quality TEXT NOT NULL,
    -- confident / partial / gap（见质量分级规则）
    path              TEXT NOT NULL,
    -- short / deep / none / error，来自 AnswerResult.path，供 Study 计算 short_path_rate
    direct_point_ids  TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：被 Answer 实际引用的 direct evidence 的 point_id 列表
    -- 只有 confident 级别才有值
    has_feedback      INTEGER NOT NULL DEFAULT 0,
    feedback_type     TEXT,
    -- positive / negative / correction（用户反馈，可为空）
    feedback_content  TEXT,
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_traces_answer_id   ON traces(answer_id);
CREATE INDEX idx_traces_quality     ON traces(retrieval_quality);
CREATE INDEX idx_traces_question_hash ON traces(question_hash);
```

### question_kp_cooccurrence 表

```sql
CREATE TABLE question_kp_cooccurrence (
    cooc_id         TEXT PRIMARY KEY,
    question_terms  TEXT NOT NULL,
    -- 与 traces.question_terms 一致
    point_id        TEXT NOT NULL REFERENCES knowledge_points(point_id),
    hit_count       INTEGER NOT NULL DEFAULT 0,
    -- 该 (question_terms, point_id) 对出现的总次数（含 partial）
    confident_count INTEGER NOT NULL DEFAULT 0,
    -- 仅 confident 级别且该 KP 被引用时累加
    last_seen_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(question_terms, point_id)
);

CREATE INDEX idx_cooc_point_id ON question_kp_cooccurrence(point_id);
```

`confident_count` 统计的是**不同问题**以高置信命中同一 KP 的次数。重复提问同一问题不应重复累加——该表反映的是跨问题的知识稳定性，而非某个问题被问的频率。去重机制见步骤 4。

### cooccurrence_question_dedup 表

```sql
CREATE TABLE cooccurrence_question_dedup (
    question_hash TEXT NOT NULL,
    -- 与 traces.question_hash 一致
    point_id      TEXT NOT NULL REFERENCES knowledge_points(point_id),
    first_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (question_hash, point_id)
);
```

记录哪些「(问题, KP)」组合已经贡献过共现计数。同一组合只贡献一次，无论该问题被提问多少次。`first_seen_at` 记录该组合首次命中的时间，供 Study 分析知识积累时序。

### learning_events 表

```sql
CREATE TABLE learning_events (
    event_id    TEXT PRIMARY KEY,
    trace_id    TEXT NOT NULL REFERENCES traces(trace_id),
    event_type  TEXT NOT NULL,
    -- knowledge_gap / user_correction
    payload     TEXT NOT NULL DEFAULT '{}',
    -- JSON，补充事件相关的 question / point_ids 等信息
    processed   INTEGER NOT NULL DEFAULT 0,
    -- 0=待 Study 消费，1=已处理
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_learning_events_type      ON learning_events(event_type);
CREATE INDEX idx_learning_events_processed ON learning_events(processed);
```

MVP 阶段 Trace 只主动生成两类 Learning Event：
- `knowledge_gap`：无证据，立即写入，Study 据此识别知识盲区
- `user_correction`：用户给出负面反馈或纠正文本，立即写入，高优先级信号

`candidate_link` 类型事件由 Study 定期扫描 `question_kp_cooccurrence` 生成，不由 Trace 触发。

## 实现步骤

### 步骤 1：定义质量分级规则

从 AnswerResult 的 EvidenceSet（evidence_snapshot）和 citations 判断本次检索质量：

```text
从 evidence_snapshot 取 direct_evidence 的全部 fact_id，查出对应 point_id；
从 citations 取 Answer 实际引用的 fact_id，查出对应 point_id；
交集 = 被引用的 direct evidence point_ids；

交集非空          → confident（直接证据被引用，检索准确）
交集为空且 supporting 非空 → partial（仅有支持性证据或直接证据未被引用）
direct 和 supporting 均为空  → gap（知识库无相关材料）
```

`confident` 级别的 direct_point_ids 是后续共现统计和 Study 学习的核心输入。

### 步骤 2：问题归一化与哈希

从 question 同时生成 question_hash 和 question_terms，不调用 LLM：

```text
归一化：
  将 question 转为小写，去除首尾空白，合并连续空格；
  去除标点符号（保留字母、数字、中文字符）；
  归一化结果作为哈希输入，保证「同一问题不同标点」映射到同一 hash。

question_hash：
  对归一化后的完整问题文本计算 SHA256（取前 16 字节 hex，共 32 字符）；
  用于识别重复问题，是去重的核心依据。

question_terms：
  在归一化文本基础上进一步分词；
  过滤停用词（"的"、"是"、"吗"、"what"、"is"、"the" 等，维护停用词表）；
  对词项排序后空格拼接；
  长度超过 200 字符时截断（避免过长问题造成 key 膨胀）。
```

question_hash 代表"同一个问题"，question_terms 代表"同类问题"。两者用于不同场景：hash 用于去重，terms 用于共现统计聚合。

### 步骤 3：写入 trace

```text
生成 trace_id（UUID）；
计算 retrieval_quality 和 direct_point_ids（步骤 1）；
计算 question_hash 和 question_terms（步骤 2）；
写入 traces 表（answer_id、question、question_hash、question_terms、retrieval_quality、path、direct_point_ids）。
path 取自 AnswerResult.path（short / deep / none / error）。
```

### 步骤 4：更新共现计数（含去重）

每次 trace 写入后，对每个 point_id 先检查是否已被同一问题贡献过，再决定是否累加：

```text
对每个 point_id（来源规则见下）：

  1. 查询去重表：
     SELECT 1 FROM cooccurrence_question_dedup
     WHERE question_hash = ? AND point_id = ?

  2. 若已存在（该问题对该 KP 已贡献过计数）：
     跳过本次计数更新，不修改 question_kp_cooccurrence；
     记录 debug 日志（含 question_hash、point_id，标注"重复问题，跳过共现更新"）。

  3. 若不存在（首次命中）：
     INSERT INTO cooccurrence_question_dedup (question_hash, point_id)；
     UPSERT question_kp_cooccurrence
       ON CONFLICT(question_terms, point_id) DO UPDATE:
         hit_count       += 1
         confident_count += (retrieval_quality == 'confident' ? 1 : 0)
         last_seen_at     = now()

point_id 来源规则：
  retrieval_quality = 'confident'：
    point_ids 来自 direct_point_ids（已被引用的直接证据 KP）

  retrieval_quality = 'partial'：
    遍历 supporting evidence 中被 Answer 实际引用的 fact_id；
    从 evidence_snapshot 取对应 point_id；
    所有 supporting 证据的 point_id 均非空（KPN 扩展来源的证据 point_id 为触发扩展的邻居 KP，也非空）；
    按正常流程检查去重表并更新 hit_count（不更新 confident_count）；
    【设计意图】partial 的问题视为无效命中，写入去重表后不允许后续补计 confident_count——
    即使同一问题日后产生 confident 结果，该 (question_hash, point_id) 对已在去重表中，
    confident_count 不会被此问题再次贡献。这反映的是"首次命中质量"，不做事后修正。

  retrieval_quality = 'gap'：
    无 point_id，跳过共现更新
```

去重以 `question_hash` 为粒度，而非 `question_terms`：两个词项相同的不同问题（如「什么是锁？」和「锁是什么？」归一化后 hash 不同）仍各自独立贡献计数，因为它们是不同用户的真实需求。只有完全相同（归一化后 hash 一致）的问题才视为重复。

### 步骤 5：生成 Learning Event

```text
retrieval_quality == 'gap'：
  写入 learning_events（event_type=knowledge_gap，payload 含 question）

retrieval_quality == 'confident' 且 Study 配置的 confident_threshold > 0：
  本步骤不检查阈值，Study 定期扫描 question_kp_cooccurrence 生成 candidate_link 事件；
  Trace 不主动生成 candidate_link。
```

### 步骤 6：用户反馈处理

用户通过反馈 API 对回答打分或纠正：

```text
POST /traces/:id/feedback
  请求：{ "type": "positive|negative|correction", "content": "..." }
  处理：
    1. 更新 traces.feedback_type、feedback_content、has_feedback=1、updated_at；
    2. type=negative 或 type=correction：
       写入 learning_events（event_type=user_correction，payload 含 feedback_content）；
    3. type=positive：更新 traces，不生成 learning_event（仅供调试统计）。
```

正面反馈不生成 learning_event：系统依赖 Rerank 质量作为隐式正反馈，不依赖用户点赞驱动学习。

### 步骤 7：暴露 HTTP API

```text
GET    /traces
  查询参数：quality（过滤 confident/partial/gap）、answer_id（可选，按回答查询）、limit（默认 20）、offset（默认 0）
  响应：[{ trace_id, answer_id, question, retrieval_quality, has_feedback, created_at }]

GET    /traces/:id
  响应：{
    trace_id, answer_id, question, question_terms,
    retrieval_quality, path, direct_point_ids,
    has_feedback, feedback_type, feedback_content,
    created_at
  }

POST   /traces/:id/feedback
  见步骤 6

GET    /cooccurrence
  查询参数：point_id（过滤），min_confident_count（默认 0），limit（默认 50）
  响应：[{ question_terms, point_id, hit_count, confident_count, last_seen_at }]
  用途：Study 扫描接口，也供调试监控使用

GET    /learning-events
  查询参数：type、processed（0/1）、limit
  响应：[{ event_id, trace_id, event_type, payload, processed, created_at }]
```

## 依赖

```text
基础设施：SQLite、异步任务队列、结构化日志、HTTP 框架
Answer：依赖 AnswerResult（含 answer_id、evidence_snapshot、citations、has_answer、path）
Study：消费 question_kp_cooccurrence 和 learning_events 表，Trace 不直接调用 Study
```

## 完成标准

```text
Answer 写库后异步投入队列，Trace 消费并完成步骤 1~5，不阻塞 HTTP 响应；
质量分级正确：confident / partial / gap 按规则分级，direct_point_ids 仅在 confident 时有值；
path 正确写入 traces 表（取自 AnswerResult.path）；
question_hash 对相同归一化问题产生相同值，不同问题产生不同值；
同一问题首次提问时正常累加共现计数，再次提问时跳过累加并记录 debug 日志；
重复问题的跳过不影响 traces 表本身写入（每次问答仍各自留有记录）；
共现计数反映不同问题的命中分布，不因重复问答虚假抬高；
knowledge_gap 事件在 gap 级别时立即生成；
用户反馈 API 正常接收，negative/correction 生成 user_correction 事件；
GET /cooccurrence 可按 min_confident_count 过滤，供 Study 扫描；
fake 任务队列下，分级、写入、去重、共现更新、事件生成路径测试均可稳定运行。
```
