# 证据筛选阶段 top-N / 目录检索系数自收敛：实现方案

设计依据：`docs/design/topn-coefficient-convergence.md`。本文档只覆盖工程落地，具体阈值数值不预设，留待接入真实数据后再定（见设计文档第 6 节）。

## 0. 范围

只涉及慢/全量路径：`internal/retrieval/service.go` 的 `recallFromSources` → `rerankAndBuildEvidenceSet` → `internal/answer/service.go` 的 `checkSlowPathSufficiency`。不改动 Fast path、Wiki 直答路径。分阶段实现，每阶段独立可测，不要求一次做完：

1. 阶段 A：候选池扩展重试（数据采集的前提，先落地）
2. 阶段 B：Trace/Store 记录校准样本
3. 阶段 C：Study 侧计算（conformal 分位数 + ACI + 安全闸门 + c 网格搜索）
4. 阶段 D：N/c 从静态配置变为运行时可调状态，接入实际检索路径

## 1. 阶段 A：候选池扩展重试

### 1.1 现状

`checkSlowPathSufficiency`（`internal/answer/service.go:113`）调用 `VerifyEvidenceSufficient`（`internal/retrieval/service.go:623`）：
- 一次不充分 → `widenEvidenceToFullUnits` 换全文重试一次 → 仍不充分才拒答。

`rerankAndBuildEvidenceSet`（`internal/retrieval/service.go:927`）目前只接收已经按 `mergedRank < N` 截断过的候选（`merged`/`expanded` 由 `recallFromSources` 传入，截断点在 Step 6 RRF merge 之后、Step 6b `expandCandidatesToPoints` 之前或之内，需要实现时定位准确的截断行）。

### 1.2 新增逻辑

在 `checkSlowPathSufficiency` 的"内容扩展仍不充分"分支之后，新增一步：

```
若（RRF merge 阶段实际候选总数 > 当前 N）：
    从未截断的候选全集里取 [N, 2N) 这一段，
    与原 top-N 候选合并，重跑 Step 7 rerank 分类（重新走 rerankWithProgress），
    重跑 kpn 扩展（Step 8）、Step 9 sufficiency 判断，
    重新调用 VerifyEvidenceSufficient
否则：
    候选总数不足 N，不满足扩池前提，维持现状（走既有的空结果兜底）
```

这要求 `recallFromSources`/`rerankAndBuildEvidenceSet` 把"RRF merge 后、截断前的候选全集"一路带到 Answer 层能触达的地方（或者把这层重试整体收回 Retrieval 内部、由 Retrieval 自己决定要不要在 Answer 判定不充分后被回调重试——具体挂在 Retrieval 还是 Answer 哪一侧，实现时先确认现有 `EvidenceSet`/`QueryContext` 的传递路径,不要凭空新增跨包依赖)。

### 1.3 结果打标

扩池重试之后，无论成功与否，都要在 `EvidenceSet`（或紧随其后的 Trace 写入路径）上记录这次 trace 落入设计文档第 3 节五类里的哪一类。建议新增字段（挂在 `EvidenceSet` 或单独通过 Trace 传递，不与现有 `GapReason` 复用，避免语义混淆）：

```go
// 新增，命名待定，语义如下
EvidenceCompletenessClass string // "tight" / "content_rescued" / "pool_rescued" / "pool_exhausted_before_2n" / "gap_at_2n"
CompletenessRankProxy     int    // worst cited mergedRank（tight/pool_rescued 时有意义）
CandidatePoolSizeAtRRF    int    // Step 6 RRF merge 后、任何截断前的候选总数
```

`content_rescued` 类是 `widenEvidenceToFullUnits` 生效那次，跟本次新增的池扩展无关，只是顺带需要打这个标，用于 `content_rescue_rate` 诊断指标。

## 2. 阶段 B：Trace / Store 记录

### 2.1 新表：`topn_calibration_samples`

沿用项目 migration 惯例（`internal/foundation/db/migrations/`），新增一张表：

```sql
CREATE TABLE topn_calibration_samples (
    sample_id           TEXT PRIMARY KEY,
    trace_id            TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    domain_id           TEXT,              -- 供后续按 domain 分组分析，暂不强制分域校准
    n_at_query_time      INTEGER NOT NULL,  -- 这次查询实际生效的 N
    coefficient_at_query_time REAL NOT NULL,
    completeness_class   TEXT NOT NULL,     -- 见 1.3
    rank_proxy_lower     INTEGER,           -- tight/pool_rescued: 保守代理值；pool_rescued 取区间上界见下
    rank_proxy_is_interval INTEGER NOT NULL DEFAULT 0, -- 1 表示 rank_proxy_lower 是区间上界（第3类），非精确观测
    candidate_pool_size  INTEGER NOT NULL
);
CREATE INDEX idx_topn_calib_created ON topn_calibration_samples(created_at);
```

- `completeness_class = tight`：`rank_proxy_lower` = worst cited `mergedRank`，`rank_proxy_is_interval = 0`。
- `completeness_class = pool_rescued`：`rank_proxy_lower` = min(2N, 实际救回时观察到的 worst cited rank)（设计文档第 3 节"区间上界"约定），`rank_proxy_is_interval = 1`。
- `completeness_class = content_rescued` / `pool_exhausted_before_2n` / `gap_at_2n`：写入但 `rank_proxy_lower` 留空，Study 计算分位数时按 completeness_class 过滤，不使用这些行的 rank 字段。

写入时机：`internal/trace/service.go` 现有的 trace_write 流程里增加一步，从 `EvidenceSet` 读出 1.3 节的新字段落库，与其余 Trace 写入同一事务提交，不额外走异步队列（同 `study` 用 ticker、不进异步队列的既有约定保持一致的"轻量旁路写入"风格；具体是否要走 `trace_write` 队列还是单独同步写，实现时对照 `internal/trace/service.go` 现有写入路径决定，不新增队列类型）。

## 3. 阶段 C：Study 侧计算

### 3.1 新增 Study 步骤（排在现有步骤序列的哪个位置，需要对照 `internal/study/service.go` 现有编号；本机制与既有 ActivationLink/Wiki 收敛互不依赖，可并行排布，不占用其编号）

```
1. 拉取窗口内的 topn_calibration_samples（tight + pool_rescued 两类，按 completeness_class 过滤）
2. 计算候选 N：
   a. 排序 rank_proxy_lower
   b. 位次 = ceil((n+1) * (1 - alpha_t)) ，n = 样本数
   c. 若位次 > n：样本不足，本轮不产出候选 N，跳到 4
   d. 否则取该位次对应值，clamp 到 [5, 当前值 + 单周期最大增幅]（收缩方向额外要求连续达标周期数达标，见 3.3）
3. 更新 alpha_t（ACI）：
   err_t 只用 pool_rescued（真实 miss，rank_proxy_lower > 当前 N）和 gap_at_2n 类别的最近记录计算，
   tight/content_rescued/pool_exhausted 不贡献 err_t 观测（不计为 0，是跳过不计入分母）
   alpha_{t+1} = clamp(alpha_t + gamma * (alpha_target - err_t), alpha_min, alpha_max)
4. c 的网格搜索：
   取窗口内 pool_rescued 类样本（这些候选在扩池重试时已经被重新打过标，具备 [0, 2N) 范围内的
   真实相关性标签），对候选打分分量重放候选网格内的每个 c，重算各样本在该 c 下的排名，
   取满足 3.2 步骤同一分位数公式的最小 N，选 N 最小的 c；
   c 只在当前值 ± 网格步长范围内搜索，不做大跳跃（设计文档第 5 节的自我强化偏差缓解）
5. 安全闸门（见 3.3），产出本周期实际下发的 N/c
6. 上报诊断指标（见 3.4）
```

### 3.2 依赖的原始打分数据

c 的重放需要候选打分的分量而非合成分——检查 `rrfMerge`/`outlineRecall`（`internal/retrieval/service.go`）现有是否已分别保留各召回路径的原始分数；若目前只落了合成后的 `score`，需要在 `candidate` 结构或 Step 6 前的中间结果里补充每个候选的分量打分，随 `topn_calibration_samples` 的 `pool_rescued` 类样本一并持久化（或单独一张关联表，避免让 `topn_calibration_samples` 承载过多列）。这是本阶段的前置工作，需要先确认现有代码是否已经保留分量分数（未确认，实现前应先查代码，不要假设）。

### 3.3 安全闸门（独立于 ACI，强制执行）

```
N 收缩：仅当连续 K 个周期都满足步骤 2 计算的候选 N < 当前 N 时才生效，且单周期最多下降 1 个步长
N 增大：候选 N > 当前 N，或最近一个较短窗口内 err_t 明显高于 alpha_target（具体倍数待观察数据定），
        立即生效，不设滞回，不等 ACI 自己收敛
持久化"上一个已验证安全的 (N, c)"，任一次安全监控触发异常，直接回退到该值，
        而不是等 alpha_t 自身走回去
```

### 3.4 Study 报告新增字段

```
topn_current, coefficient_current
topn_rank_proxy_p50 / p95（tight + pool_rescued 合并后的分布，用于人工观察趋势）
content_rescue_rate
pool_exhausted_rate（第五类，需与知识缺口率分开展示，不合并统计）
gap_at_2n_rate
alpha_t 当前值、最近 N 次调整历史（含每次调整的触发原因：常规收敛 / 安全闸门回退）
```

## 4. 阶段 D：配置项与运行时状态

现有 `retrieval.rerank_top_n`（或等价配置名，需核对 `config/` 实际字段名）从静态配置改为持久化的运行时状态（类似 ActivationLink 置信度不是配置而是数据库状态的做法），新增：

```
retrieval.rerank_top_n_min = 5           # 硬下限，配置项，不可被算法突破
retrieval.outline_score_coefficient_min = 1  # 硬下限
study.topn_target_alpha                  # 待实测确定初始值
study.topn_aci_gamma                     # 待实测确定初始值
study.topn_shrink_confirm_cycles         # 收缩所需连续达标周期数，待实测确定
study.topn_grow_failure_rate_trigger     # 触发即时回退的短窗口失败率阈值，待实测确定
study.topn_coefficient_grid_step         # c 网格步长，待实测确定
```

`rerank_top_n`/`outline_score_coefficient` 的当前生效值本身持久化在一张状态表（或复用 `topn_calibration_samples` 之外的一张小状态表），检索路径查询时读取该状态，不再读静态 config 常量；下限仍由 config 校验。

## 5. 实施顺序建议

严格按 A → B → C → D 推进，每阶段独立验证：

1. 阶段 A 落地后先跑一段时间，只观察 `pool_rescued`/`pool_exhausted_before_2n`/`content_rescued` 的自然发生率，不接入任何自动调整——这批观察数据本身就能回答"当前 N=10 到底有没有明显偏大或偏小"这个最初的问题。
2. 阶段 B/C 的分位数计算和 ACI 状态更新先只读不写（只在 Study 报告里展示"如果按这套算法算，N 会是多少"，不实际下发），跑够数据、人工确认趋势合理后再进入阶段 D。
3. 阶段 D 上线时，`study.topn_shrink_confirm_cycles`、`study.topn_grow_failure_rate_trigger` 等安全相关配置项优先设置得保守（宁可收缩慢、回退快），后续再根据实测数据收紧或放松。
