# Study 实现路径（V1）

## 职责

Study 从 MVP 的「只产报告」升级为「执行学习动作 + 可审计」：消费共现统计与 `activation_gap` 事件，执行 ActivationLink 的创建，每个动作落库为 Learning Result 并附 Learning Reason。报告继续生成，内容扩展为本周期执行的动作及原因。

**2026-08-13 起的机制基准**：随 `docs/design/activation-convergence.md` 的连续置信度设计，Study 不再是 activation_links 状态迁移的执行方——见 `activation.md`「状态机」，晋升/降权/重新验证这三类离散跳变已经不存在，`success_count`/`failure_count` 的更新改由 Trace 在产生 `activation_success`/`activation_failure`/`activation_audit_success`/`activation_audit_failure` 事件的同一步直接调用 `activation.RecordOutcome`/`RecordAuditOutcome`（见 `trace.md` 步骤 3/3b）。Study 因此从「执行链接状态迁移」的角色里退出，换成两项新职责：(a) 把 `docs/design/activation-convergence.md` 第 5 节要求的收敛趋势——置信度分布收窄了没有、试探名额消耗的比例是不是在降——变成报告里可读的时间序列（见步骤 7）；(b) 对长期停在低分且已经稳定不动的观测条件执行剪枝，把它们从 `observed_conditions` 里清掉（见新「步骤 3：收敛剪枝」，取代原「链接信号累积与状态判定」）。这两项都是只读 `activation_links` 当前状态做 SQL 聚合、或对单条条件执行一次剪枝写入，不需要重放 `learning_events`。

Study 仍然是新增候选链接（`CreateLink`）与新增观测条件（`UpdateConditions`）的调用方——这部分（步骤 1、2、2a）与置信度改写无关，原样保留：Study 依然回答"该不该开始追踪一个全新的问题条件组合"，只是不再回答"这个已经在追踪的组合，现在信不信得过"（那个问题现在由每条条件自己的连续分数持续回答，见 `activation.md`）。

运行方式沿用 MVP：`time.Ticker` 定时扫描，不走异步队列。

## 数据结构

### learning_results 表

```sql
CREATE TABLE learning_results (
    result_id       TEXT PRIMARY KEY,
    action          TEXT NOT NULL,
    -- create_candidate / prune_condition / gap_flag /
    -- recompile_flag / synonym_candidate
    -- （wiki_candidate / topic_page_candidate 随 Wiki 单层化改造删除
    -- Study 自动候选识别一并移除，见 wiki.md「职责」与本文档步骤 6）
    -- （2026-08-13 修订：promote / weaken / reverify / deprecate 四个动作
    -- 随离散状态机一起移除——没有跳变，就没有跳变类动作；deprecate 的
    -- 触发方也不再是 Study，KP lifecycle 变化时由 lifecycle 模块直接写
    -- activation_links.status，不经过 learning_results，见 activation.md
    -- 「状态机」「与 Study 的分工变化」。prune_condition 是本次新增的
    -- 剪枝动作，覆盖原 promote/weaken/reverify 三个动作腾出的语义空间，
    -- 也是 activation.md `POST /activation-links/:id/reject` 使用的同一个
    -- 动作名，见该文档步骤 3；
    -- 2026-08-12 修订：wiki_material_confirm 动作与 knowledge_point
    -- object_type 已随该人工确认关卡的整体废弃一并移除，见 wiki.md 步骤 2）
    object_type     TEXT NOT NULL,
    -- activation_link / knowledge_gap / wiki_page / subject_synonym
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
  # candidate_confident_min / candidate_ratio_min 已删除（2026-08-13，
  # 随 docs/design/activation-convergence.md 第 11 节一并替换——创建前的
  # 裸计数+裸比例门槛换成与「收敛剪枝」同一套 Beta 均值/宽度公式，理由
  # 见下方步骤 1；候选判定不再是这两个键，看 create_confidence_min/
  # create_width_max）
  create_confidence_min:   0.55  # mean_pre 门槛：明显低于 retrieval.md
                                   # 的 serving_confidence_min（0.7-0.75）
                                   # ——创建回答"这是不是个真实模式"，不是
                                   # "信不信得过"，信不信得过交给创建后
                                   # 持续更新的置信度去回答（见
                                   # activation-convergence.md 第 11 节）
  create_width_max:        0.03   # width_pre 门槛：与 prune_width_max
                                   # 同一个公式（mean*(1-mean)/(n+3)），
                                   # 数值上比剪枝的 0.02 略松——创建只要
                                   # "不是单次巧合"，不要求剪枝那种
                                   # "确定收敛"的严格程度
  wiki_kp_min:             4
  # wiki_confident_min 已移除（2026-07-29 修订，见 docs/design/wiki.md
  # "ActivationLink 回答'这条管不管用'，Wiki 编译回答'这个主题够不够格立传'"）——
  # 素材排序（wiki.md 步骤 3，按 confident_count 降序截取）用的是 KP 自身的
  # confident_count 数值，不需要任何最小值配置，所以这个字段没有剩余用途。
  # 2026-08-11 补记（已被下方 2026-08-12 修订取代）：当时的判断是
  # auto_promote 改默认 true 后 verified 不再隐含"有人工看过"，需要单独
  # 补一道 Wiki 材料确认的人工关卡。2026-08-12 改判：该关卡整体废弃，见
  # `docs/design/wiki.md`「2026-08-12 改判」——脱离具体 Wiki 主题语境，
  # 人工看着一条孤立的 KP 判断"值不值得沉淀"并不比程序多掌握信息，真正
  # 能做这个判断的时机是 Wiki 编译时（那时主题范围已定，编译时的整体
  # 判断——广度/连贯/稳定——自然回答了这个问题）。qualifying 因此恢复为
  # 只看 verified ActivationLink，`qualifying_confirm_success_min`/
  # `qualifying_confirm_distinct_min` 两个配置项随之废弃（见下方 wiki
  # 材料确认段落的说明）。
  gap_hit_threshold:       3
  scan_batch_size:         200
  report_period_days:      5
  report_max_keep:         10
  # —— V1 新增 ——
  # auto_promote / promote_success_min / promote_distinct_min /
  # weaken_failure_min / weaken_ratio_min / reverify_success_min 六个键
  # 已删除（2026-08-13，随离散状态机一起废弃，完整映射说明见
  # activation.md「状态机」下方「配置项」一节：晋升/降权/重新验证不再是
  # 离散跳变，替代逻辑是每条观测条件持续用 mean(cond) 与三档服务分档
  # 判定，对应新配置项搬到了 retrieval.md 的 retrieval: 节——
  # serving_confidence_min / audit_sample_min / explore_rate_low /
  # explore_rate_self_graded / explore_rate_trusted，理由是这些参数
  # 配置的是 Match() 的实时服务行为，和本节其余"周期扫描判定阈值"性质
  # 不同，不适合继续放在 study: 节）
  event_window_days:       30     # 报告回看窗口（仍用于收敛趋势报告的
                                   # 时间跨度与下方剪枝的 idle 判定；语义
                                   # 从"累积判定窗口"收窄为"报告/剪枝的
                                   # 回看跨度"）
  # candidate_idle_days / deprecate_idle_days 已删除（2026-08-13，随离散
  # 状态机一起废弃——它们淘汰的是整条链接，新机制里淘汰粒度下沉到单条
  # 观测条件，见下方 prune_* 四项）
  # —— 收敛剪枝（2026-08-13 新增，见步骤 3）——
  prune_mean_max:           0.3   # 剪枝候选的置信度上限：mean(cond) 必须
                                   # 低于此值才可能被判定"收敛低分"
  prune_width_max:          0.02  # 剪枝候选的置信度分布宽度上限：
                                   # mean*(1-mean)/(success+failure+3)
                                   # 必须不超过此值——宽度仍然大，说明这条
                                   # 条件证据自相矛盾（好坏参半），不是真的
                                   # "收敛低分"，不剪，改进报告的
                                   # self_contradictory 节交给人看（见步骤 7）
  prune_sample_min:         8     # 剪枝候选的最小样本量：
                                   # success_count+failure_count 达到此值，
                                   # 才有把握说这条条件"确实收敛"，不是样本
                                   # 太少导致的暂时性低分
  prune_idle_days:          30    # 收敛低分剪枝：该条件最近一次被观测
                                   # （last_seen_at）距今超过此天数才剪，
                                   # 给"最近还在被试探"的条件留出反弹空间
  prune_stale_days:         90    # 长期闲置剪枝（样本不足以判定"收敛"，
                                   # 但长期没人问）：last_seen_at 距今超过
                                   # 此天数、且样本量不足 prune_sample_min，
                                   # 也清理掉——比 prune_idle_days 更宽松，
                                   # 因为样本太少时没把握说它"确实没用"，
                                   # 纯粹是"太久没人问了"的清理，不是判定
  correction_weight:       2      # user_correction 关联链接时按 N 次 failure 计
                                   # （2026-08-13 起由 Trace 直接调用
                                   # RecordOutcome 消费，见 trace.md 步骤 4；
                                   # 本键仍在 study: 节声明——配置的是"人工
                                   # 纠正该有多重"这条政策，不是"谁在代码里
                                   # 读它"）
  # Wiki 材料确认（2026-08-11 新增的独立人工确认关卡）已于 2026-08-12
  # 整体废弃，qualifying_confirm_success_min/qualifying_confirm_distinct_min
  # 两个配置项随之删除，见上方 wiki_kp_min 处的补记与 wiki.md 步骤 2。
  # —— subject 同义词挖掘（2026-07-24 新增）——
  synonym_gap_min:          3     # subject_synonym_gap 候选达标所需事件数
  # —— 问题复杂度观测量（步骤 7，只观测不驱动）——
  complexity_min_questions: 3     # 条件组内问答数下限，低于此不出报告项
  synonym_gap_distinct_min: 2     # 且来自 ≥ N 个不同 question_hash
  synonym_auto_promote:     true  # 2026-08-12 新默认（原 false）：候选直接
                                   # active，不经人工确认；理由同
                                   # study.auto_promote——错误同义词对最坏
                                   # 只是 Match 第一轮多/少命中一次，
                                   # fast_path_verify 兜底，不直接影响答案
                                   # 正确性，人工可事后 reject 补救
```

## 实现步骤

每次调度顺序：步骤 1 → 2 → 2a → 3 → 4 → 5 → **5b**（2026-08-12 定案，新增，见下）→ 之后沿用 MVP 的 gap 聚合与报告生成（步骤 6、7）。单步异常记录 error 日志，不中断本轮后续步骤——5b 遵循同一条既有纪律，不额外特殊处理。

### 步骤 5b：熟路显影扫描（ActivationBundle，2026-08-12 定案排入调度顺序）

完整规格见 `docs/impl/v1/activation-bundle.md` 步骤 2「显影扫描」、步骤 3「巩固与状态迁移」，本节不重复展开，只记录它在调度顺序里的位置与理由：

**归一化四元组累积（2026-08-20 重设计要点，完整机制见 activation-bundle.md
步骤 2）**：显影扫描不再按 `distinct_question_count`/`days_active` 判断
"问法够不够多样"，改为跟本节步骤 1（ActivationLink 创建）同一套 Beta 均值/
宽度公式（复用 `create_confidence_min`/`create_width_max`，不新增配置），
计算对象是"归一化四元组本身积累了多少置信证据"——`scanActivationBundles`
（`internal/study/bundle_scan.go`）先把每条新的 confident 多点 trace 归一化
后累加进新表 `bundle_trigger_cooccurrence`（跟 `question_kp_cooccurrence`
同构，键从 point_id 换成归一化四元组），再对累积结果做门槛判定；越过门槛
的每个归一化四元组，成员名单是历史上匹配到这个四元组的全部 trace 的
`direct_point_ids` 并集，不是创建时写死的固定集合。

```text
位置：步骤 5（晋升确认流）之后、步骤 6（gap 聚合与 Wiki/重编译信号）之前。

不依赖步骤 1-5 的处理结果：输入是 traces（confident 且 direct_point_ids
  非空），不是 activation_links 的状态，理论上放在本轮任意位置都能跑；

排在步骤 5 之后的理由：显影扫描的 Match 第一轮复用 subject_synonyms
  表做免费预过滤，排在步骤 2a（同义词候选聚合，synonym_auto_promote
  默认 true 时本轮新候选会直接 active）之后，能用上本轮刚刷新的同义词表；

挨着步骤 6/7 的理由：显影扫描的分组函数与步骤 7「问题复杂度观测量」共用
  同一个归一化+分组实现，位置相邻便于以后接入复用（步骤 6 原有的"主题页
  候选"四元组聚类已随 Wiki 单层化改造删除，不再是共用方之一；是否复用见
  wiki.md 熟路指针，仍未定案，这里只保证物理位置不妨碍以后接入）；

不占用步骤 6/7 现有编号：两个编号已被 wiki.md/page.md/activation.md/
  CLAUDE.md 多处交叉引用（尤其 wiki_material_confirm 相关说明），插入
  新步骤改变编号会牵连大量修改，用 5b 这种不挪位的编号方式（同 2a 先例）。
```

### 步骤 1：共现扫描（2026-07-18 修订：按 point 聚合；2026-08-13 修订：达标判定改用 Beta 均值/宽度）

扫描 `question_kp_cooccurrence` UPSERT `link_candidates`。达标判定不再按
`(question_terms, point_id)` 行级计数，而是**按 point_id 聚合**：

```text
按 point_id GROUP BY，取 confident_count 总和为 s、hit_count 总和为 h：
  mean_pre  = (s + 1) / (h + 2)
  width_pre = mean_pre * (1 - mean_pre) / (h + 3)
  mean_pre ≥ create_confidence_min 且 width_pre ≤ create_width_max 视为达标；
每个达标 point 只写一行 link_candidates：question_terms 取该 point 的
代表标签（confident_count 最高，并列取 last_seen_at 最新），
confident_count / hit_count 存聚合值（供 activation.md CreateLink 之后
把 s/h 原样作为该链接首条观测条件的 success_count/failure_count 起点，
不清零重来——延续 activation.md「数据结构」已定的 hit_count 承接规则）。
```

**2026-08-13 修订说明**：`candidate_confident_min ≥ 5 且 ratio ≥ 0.6` 这套
裸计数+裸比例判定，换成与「步骤 3：收敛剪枝」同一套 Beta 均值/宽度公式，
理由见 `docs/design/activation-convergence.md` 第 11 节——创建、服务、
剪枝问的是同一类"给定目前的成功/失败次数，这个估计有多可信多确定"，
不该用三套互不相干的规则各自回答。换成 Beta 公式后有两点直接好处：
小样本（比如 1 次命中 1 次确信）不会被裸比例误判成 100% 可靠，
`mean_pre` 的 Laplace 平滑会自然打折；且门槛本身可以设得比旧值更松
（`create_confidence_min` 明显低于服务门槛），因为创建后的系统现在有
服务分档与收敛剪枝两层自愈能力，不再需要靠"宁可少建"防止建错的对象
堵死系统。`create_confidence_min`/`create_width_max` 的具体数值是按
旧门槛的边界点（`confident_count=5, ratio=0.6`）反推的起点，不是精确
校准结果——待 Study 报告的收敛趋势时间序列（步骤 7）积累真实数据后，
应该用实际观察到的宽度/命中分布重新校准，不是一次定死。

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
  查共现表该 point 的聚合 s（confident_count 总和）、h（hit_count 总和），
  用步骤 1 同一套 mean_pre/width_pre 公式判定达标（2026-08-13 修订，
  取代此前"SUM(confident_count) ≥ 2"这条独立的裸计数门槛——gap 事件
  本身已经是一次 confident 命中，不需要再单独发明一套比步骤 1 更松的
  计数规则，两个来源共用同一套判定公式，只是自然因为"至少两次观测"
  这个前提，通常比步骤 1 更快越过 width_pre 门槛，不需要额外区分对待）；

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

**归一化接入构建阶段（2026-08-20 新增，config-gated，复用
`retrieval.question_tuple_norm_enabled`，不新增配置项）**：此前
`activation.TupleNormalizer`（`retrieval.md` 步骤 2 的 Tier1 精确匹配 →
Tier2 本地 Jaccard → Tier3 LLM 判断）只在 Retrieval `tryFastPath` 查询侧生
效，`buildObservedConditions` 按四元组**字面字符串精确相等**分组——同一个
KP 被措辞不同但语义等价的问法命中时，每种问法各开一条独立的
`ObservedCondition`，每条都从 `success_count=0` 起步，观测样本被打散，置
信度长期收敛不上去（命中率/复用率低的根因，不是生成门槛本身；生成门槛
`create_confidence_min`/`create_width_max` 保持不变，不做调整）。开启后，
`buildObservedConditions` 先用 `store.DomainIDsForPoint(pointID)`（经
`knowledge_points.source_id → sources.domain_id` 反查）取该 point 所属
domain，再对 `ConfidentTraceQuadruples` 返回的每条四元组调用
`activationSvc.NormalizeTuple`（同一套 Tier1-3，同一张 `question_tuple_
norms` 表，与查询侧共享 canonical 空间）替换后再分组合并——语义等价的问法
折叠进同一条 `ObservedCondition`，观测量才能真正攒起来。domain 查不到
（source 未配置 domain_id）或归一化调用出错 → 跳过归一化，回退为原始四元
组，不影响构建本身成功；查询侧 `Matcher.Match` 的精确匹配算法不受影响
——因为查询侧四元组同样先过这套归一化，两边落在同一个 canonical 空间里，
精确匹配自然对齐。允许时序竞态：构建某个问法当时若还没被查询侧写入过
`question_tuple_norms`，本轮就归一化不到、条件没能合并，不做额外协调，
下一轮自然收敛。默认关闭，关闭时 `buildObservedConditions` 与改动前逐字
节一致。

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

### 步骤 2a：subject 同义词候选聚合（2026-07-24 新增，2026-08-12 默认策略修订）

**2026-08-12 改判后角色收窄**：Match 本身不再消费 `subject_synonyms`（见 activation.md 步骤 2，Match 已恢复为四字段同层级精确匹配，不对 subject 做归一化）。这条挖掘链路继续保留，但产出不再服务任何查询时判定路径，只服务 Trace 的诊断可见性——`Matcher.SubjectOnlyMiss`（原文见 `docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md`）用它内联比较"这条问法是不是只差 subject 没对上"。挖掘信号来自 Trace 步骤 3（见 `trace.md`）产生的 `subject_synonym_gap` 事件：intent/audience/constraint 全部匹配某已有 ActivationLink 的某组观测条件、仅 subject 未通过 `coreContained` 时触发。

**这条挖掘链路继续保留、默认策略仍是自动（2026-08-12 定案，不冻结）**：此前的表述是"模型辅助匹配解决当场判断，挖掘链路把判断过的等价关系沉淀成免费规则"——随着模型辅助匹配整体撤销，这个"当场判断"的另一半已经不存在，挖掘链路现在单纯是"记录 subject 措辞变体供观察"的诊断信号收集，不再是任何调用成本优化手段的一部分。价值判断不变：一条同义词对判断错了，最坏后果只是诊断噪声，不影响 Match 的实际命中结果，不需要事前逐对人工审阅，因此 `synonym_auto_promote` 默认仍是 `true`。

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
    created_from=支撑事件 id 列表)：

  synonym_auto_promote=true（2026-08-12 新默认）：status='active' 直接生效，
    learning_results(action='synonym_candidate', status='applied',
    confirmed_by='auto', reason 含 hit_count/distinct_n, event_ids=支撑
    事件 id 列表)——理由同 study.auto_promote 的默认自动化：一条同义词
    对即使判断错了，最坏后果是 Match 第一轮多命中/少命中一次，答案仍要
    经过 fast_path_verify 才会真正出口，不会把错误直接送到用户面前；
    错误的同义词对可以人工在 Page 上事后 reject（见 activation.md
    步骤 3a），不必事前逐条把关；

  synonym_auto_promote=false（灰度回退）：status='candidate'，
    learning_results status='pending_confirm'，等人工在 Page 上
    confirm/reject（见 activation.md 步骤 3a）；

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
auto_promote = true（2026-08-11 修订，新默认）：
  晋升达标 → 直接 TransitionLink(candidate → verified)，
  learning_results status=applied，confirmed_by=auto。
  修订理由：verified 目前唯一直接解锁的高风险动作是 Retrieval 快路径，
  而快路径在真正生成答案前必经 fast_path_verify（见 retrieval.md 步骤 2a）
  ——误晋升的链接如果证据其实答不上这次问题，会在这一步被拦下、回落
  慢路径，不会把错误答案直接送到用户面前。原先"默认需要人工确认"防的
  是这个风险，既然它已经在查询时被结构性地兜住，就不必再在链接形成时
  重复设卡。verified 曾经隐含的另一层含义——"人工看过、值得信赖到可以
  进入 Wiki 材料池"——不再成立，这层判断被移到了下面步骤 6 的 Wiki
  材料确认这道独立关卡，那道关卡**没有** auto 开关，见 wiki.md 步骤 3。

auto_promote = false（保留，灰度回退用）：
  晋升达标 → 写 learning_results(action=promote, status=pending_confirm,
  reason 含 success_n / distinct_n)；链接保持 candidate；
  同一链接已有 pending_confirm 的 promote 时不重复生成；
  人工在 Page 调 POST /activation-links/:id/confirm →
    activation 模块执行迁移，并将该 pending result 置 applied
    （confirmed_by=manual）；reject 同理置 rejected。
```

### 步骤 6：gap 聚合与 Wiki 重编译信号

> **2026-08-18 单层化改造重写**：本步骤原描述"Wiki 候选计算"（qualifying
> 概念/事实写 `wiki_candidate` learning_result）与"主题页候选"（真实提问
> 四元组聚类，产出 `topic_page_candidate`）两条 Study 自动识别链路，均已
> 随 `docs/design/wiki-single-tier-revision.md` 定案的单层化改造整体删除
> （`buildWikiCandidates` 及其调用点、`topic_cluster_min_questions`/
> `topic_cluster_min_days_active` 配置项均已删除，见
> `docs/impl/v1/wiki-single-tier-open-questions.md`「落地记录」）。Wiki
> 编译触发方式改为人工直接指定 entry_id 集合（见 `wiki.md`），Study **不再
> 产生任何 Wiki 触发相关的 learning_result**，本步骤现在只保留与 Wiki 触发
> 无关的 gap 聚合。

```text
knowledge_gap 聚合逻辑沿用 MVP（UPSERT knowledge_gaps ON CONFLICT(question_terms)，
  hit_count += 1），额外从 payload 取 reason 做两件事：
    reason_counts：对该字段做 JSON 内自增（不存在则置 1），一次 UPSERT 只增量
      更新命中的那个 key，不重写整个对象；
    last_reason = 当前 reason；
    last_trace_id = 当前事件的 trace_id（learning_events 行自带，无需额外查询）；
  达 gap_hit_threshold 时额外写 learning_results(action=gap_flag,
  object_type=knowledge_gap, reason 含 last_reason)，纳入统一审计；

Wiki 重编译标记：设计上仍应有"已发布页面依赖的 KP 出现新增 qualifying KP /
  lifecycle != current 时标记 needs_recompile"这一动作（见 wiki.md「重编译
  标记」a/b 两条），但截至本次文档重写，Study 侧本步骤未见对应的周期扫描
  实现——unit/activation → wiki 的跨模块自动通知接线已在本次改造中被拔除
  （原为支撑已删除的 Wiki 自动候选识别而存在的三个扫描函数一并删除，见
  `wiki-single-tier-open-questions.md`），Study 侧也没有看到替代实现重新
  接上。这是需要用户确认的现状落差，已记入 `wiki.md`「已知遗留」与
  `wiki-single-tier-open-questions.md`，本文档不代为决定是否需要在本步骤
  补一段新的重编译标记扫描逻辑。

报告提示项（只进报告，不产生 learning_results）：
  wiki_draft_reflow：origin='wiki_draft' 的 Source 及其产出 KP 数、
    被跳过的自体祖先边数（见 wiki.md「写作草稿」回流的自体循环防护），
    用于发现系统在自己身上打转。
  （原 topic_signal_underfilled、topic_decompose 两个报告项——分别服务于
    已删除的主题页候选识别与已删除的骨架注入慢路径——随对应机制一起删除，
    见 wiki-single-tier-open-questions.md「步骤 5 落地记录」。）
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
                           满足过的比例。
  （skeleton_used_count / cross_member_ratio / outside_ratio 三项——依赖
   主题页骨架注入与 topic_decompose_signal——已随单层化改造整体删除，见
   wiki-single-tier-open-questions.md「已拍板（2026-08-18）」。）

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
  响应：learning_results 列表；question_terms 字段按 object_type 分别 JOIN 补充
        为该对象的可读名称——activation_link 取 activation_links.question_terms
        （同时 JOIN knowledge_points 补 point_summary）、knowledge_gap 取
        knowledge_gaps.question、wiki_page 取 wiki_pages.title、entry_candidate
        取 entry_candidates.suggested_name、activation_bundle 取
        activation_bundles.representative_terms；管理页「学习动作」列表的「对象」列
        展示这个名称而不是 object_id 原始 uuid（2026-09-02 补充，此前只有
        activation_link 有名称，其余类型的列表页显示的是截断 uuid；
        entry_add_candidate 动作的 object_type 是 entry_candidate 而非其动作名
        本身，容易在核对时漏看）

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
