# ActivationBundle（熟路）实现路径（V1，设计方向，尚未排入强制实现顺序）

> 状态说明：本文档是 `docs/design/activation-bundle.md` 的工程落地方案，**尚未排入 CLAUDE.md「实现顺序」的强制序列**，也尚未开始编码。写在这里是为了让方案有一个可以被审阅、被指出问题的具体形态，而不是代表可以直接按此开工。真正开始实现前，需要用户对下面标注为「待确认」的点逐一拍板；未标注「待确认」的部分是本文档给出的具体设计，同样接受审阅调整，不是不可讨论的定论。

## 职责

ActivationLink（见 `activation.md`）记住「一个知识点管不管用」，锚点是 `point_id`。ActivationBundle 记住「一组知识点合在一起，对同一类问题管不管用」，锚点是一次真实问答归一化后的四元组分组——这是 `docs/design/activation-bundle.md` 定义的缺口：需要综合多个知识点才能回答的问题，现有结构完全没有位置存放"这个组合本身反复被验证有效"这件事。

**ActivationBundle 不替代 ActivationLink**，两者的产生、存储、匹配相互独立；单知识点问题继续走 ActivationLink 的最短路径，不途经本模块。

本文档按数据依赖关系分两个阶段描述：

```text
阶段 1（本文档给出完整规格，可独立实现，不影响任何现有行为）：
  显影扫描 + 存储 + 状态机——ActivationBundle 从 traces 历史数据里被
  看出来、被记录、被巩固或淘汰，全程只读 traces / activation_links，
  不产生任何检索行为上的变化；

阶段 2（本文档只给出预期契约，具体接入点留给 retrieval.md / trace.md
  各自的修订，届时需要重新对齐检索总流程与优先级顺序）：
  Retrieval 消费 ActivationBundle 走快路径、Trace 回写 bundle 级别的
  成功/失败信号——这一阶段会改变现有检索行为（新增一层命中优先级、
  放宽跨 unit 歧义判定），影响面比阶段 1 大得多，需要独立评估。
```

这个拆分本身是本文档提出的实现路径选择（先建立"记忆"，再决定"怎么用"），**待确认**：是否认可这个分阶段顺序，还是希望两阶段一次性设计完再开工。

## 数据结构

```sql
CREATE TABLE activation_bundles (
    bundle_id          TEXT PRIMARY KEY,
    cluster_fingerprint TEXT NOT NULL,
    -- 归一化四元组分组的指纹（Normalize(subject)+intent+audience+constraint
    -- 拼接后取值），只是显影时留的书签，**不是身份、不是去重键**
    -- （2026-08-11 修订，取代此前"UNIQUE(cluster_fingerprint) 就是去重键"
    -- 的口径——指纹本身就是四元组解析出来的东西，跟 ActivationLink 早年
    -- UNIQUE(question_terms, point_id) 因问法抖动无法收敛是同一个坑：
    -- 同一类问题两次解析出不同指纹，会被错当成两条熟路。真正的身份判断
    -- 见步骤 2「显影扫描」——新证据先尝试匹配已有熟路的 observed_conditions
    -- （复用 activation.md 步骤 2 的 Match，2026-08-12 改判后 Match 恢复
    -- 为纯程序精确匹配，不再含模型辅助匹配），匹配上就合并，不看这个
    -- 字段；只有全部匹配不上时才会退到按这个指纹分组去发现全新的熟路）；
    representative_terms TEXT NOT NULL DEFAULT '',
    -- 展示用：该分组当前的代表性问法拼接，逻辑同 activation_links.question_terms，
    -- 不参与匹配、不参与去重；
    observed_conditions TEXT NOT NULL DEFAULT '[]',
    -- 结构与 activation_links.observed_conditions 完全相同：该分组归一化后
    -- 观测到的四元组集合，Match 时复用 activation.md 步骤 2 的组内精确/
    -- 组间 OR 语义；
    member_point_ids   TEXT NOT NULL DEFAULT '[]',
    -- 2026-08-13 修订（见「成员置信度：Bundle 独有的第二根轴」）：不再是
    -- 离散的核心/路肩两个数组各自存一份静态标签，改为单个数组，每个
    -- 成员自带一对独立于触发轴的计数：JSON 数组
    -- [{"point_id","success_count","failure_count","last_seen_at"}]；
    -- 是否算"核心"不再是建 Bundle 那一刻写死的标签，是这对计数当前的
    -- mean/tier 落在哪个区间（同 activation.md 的连续置信度公式，见下），
    -- 随每次 Bundle 被用来回答问题、这个成员这次到底有没有被引用而持续
    -- 更新；
    fringe_point_ids   TEXT NOT NULL DEFAULT '[]',
    -- 2026-08-13 起废弃为独立字段：路肩不再是一个成员因为初始比例没达标
    -- 就被分进的另一个数组，是 member_point_ids 里同一批成员当前 mean
    -- 落在低档区间的那部分——一次 SELECT 就能从 member_point_ids 里筛出
    -- 来，不需要维护两份平行的存储、不需要"路肩转正"这个搬家动作（步骤 5
    -- 原有的"路肩转正"事件描述随之简化，见该步骤 2026-08-13 编注）。本列
    -- 保留只是为了不在 migration 里立刻删列——迁移时机与「Migration 与
    -- 回填」一节一并给出，读取路径应统一改读 member_point_ids 按 tier
    -- 过滤，不应再读本列；
    status              TEXT NOT NULL DEFAULT 'candidate',
    -- 2026-08-13 修订：不再是本表自己的一套 candidate/verified/weakened/
    -- deprecated 四态状态机——见「状态机」一节，本列现在与
    -- activation_links.status 同构：从触发轴（本 Bundle 自己的
    -- observed_conditions，复用 activation.md 的置信度公式与三档服务
    -- 分档）派生、落库的缓存摘要，取值收窄为三态（candidate/verified/
    -- deprecated，weakened 同 activation_links 一并退休）；
    adopt_count         INTEGER NOT NULL DEFAULT 0,
    fail_count          INTEGER NOT NULL DEFAULT 0,
    last_used_at        DATETIME,
    created_from        TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：显影时依据的 trace_id 列表；
    status_changed_at   DATETIME,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_ab_cluster ON activation_bundles(cluster_fingerprint);
-- 非唯一索引，仅用于显影时的调试/观测查询（"这次算出来的指纹历史上
-- 出现过吗"），不承担去重职责，见上方字段注释；
CREATE INDEX idx_ab_status ON activation_bundles(status);
```

migration 编号待实现时按当前最大版本号 +1（同本仓库其余待实现文档的一贯写法，不在文档里预先固定）。

**不引入独立的"成员关联表"**：`member_point_ids` / `fringe_point_ids` 用 JSON 数组存（同 `activation_links.observed_conditions` 的既有模式），核心大小有上限（见下），不会膨胀到需要关系表才能查询的规模；换成关系表能获得的唯一好处（按 `point_id`反查所属 bundle）用量很低——一个 KP 同时属于多条 bundle 的场景本身就该被核心大小上限挡住大部分，需要时全表扫一次 JSON 也足够，V1 不为这个低频查询新增表。

## 状态机

```text
                 ┌────────────┐   窗口内 bundle_success 达标（默认自动，
  Study 显影 ──▶ │ candidate  │   见下）
                 └────────────┘ ─────────────────────────▶ verified
                       │ 长期无新信号                          │
                       ▼                                     ▼
                  deprecated ◀────── 长期无有效使用 ────── weakened
                                                             │ 重新证实
                                                             ▼
                                                          verified
```

合法迁移表与 `activation.md` 的 `TransitionLink` 完全同构（复用同一组迁移语义：`candidate→verified`、`candidate→deprecated`、`verified→weakened`、`weakened→verified`、`weakened→deprecated`，`deprecated` 终态不可迁出），差异只在下面这一点：

**默认晋升策略（2026-08-11 更新，与 ActivationLink 现在一致，理由不同；2026-08-12 更新交叉引用，结论不变）**：`study.auto_promote`（ActivationLink 用）已改为默认 `true`（见 `activation.md`「verified 的含义」——理由是 Retrieval 快路径在生成答案前有 `fast_verify` 兜底，误晋升的链接答不出来会回落慢路径，不直接产出错误答案）。ActivationBundle 建议新增独立开关 `study.bundle_auto_promote`，**同样默认 `true`**，但论证角度略有不同、结论恰好一致：熟路是可随时被后续使用证伪、自动衰减、自动淘汰的缓存层，不是被人工正式接纳的知识对象；真正需要人工把关的沉淀关卡是 Wiki 编译的人工确认（`POST /wiki/compile`），这里不需要重复设卡（2026-08-12 修订：此前设想的 `wiki_material_confirm` 独立人工确认关卡已整体废弃，见 `docs/design/wiki.md`「2026-08-12 改判」——ActivationLink 一侧现在也没有单独的"进入 Wiki 材料池"人工把关，均在 Wiki 编译时由整体判断——广度/连贯/稳定——一并回答）。**待确认**：是否认可这个默认值，以及——如果未来 `wiki.md` 的熟路指针被确认要用（Bundle 信号接入 Wiki 可靠度判定）——Bundle 信号进入 Wiki 材料的资格判据应参照 ActivationLink 现在的口径（自动晋升后的 `verified` 直接被 Wiki 编译采信、在编译时统一把关），而不是重新给 Bundle 单独设一道候选阶段的人工确认。

> **2026-08-13 编注（随 `docs/design/activation-convergence.md` 的连续置信度设计一并同步，取代本节以上全部离散状态机描述作为当前实现基准，但不删除——留作对照历史演进）**：以上的状态图、"合法迁移表与 `activation.md` 的 `TransitionLink` 完全同构"、`bundle_auto_promote` 控制"晋升要不要经人工确认"，全部建立在离散四态跳变之上，这套机制已随 `activation.md`「状态机」的改写一并替换为连续置信度机制，Bundle 的触发轴（本 Bundle 自己的 `observed_conditions`）**原样复用** `activation.md` 的公式与三档服务分档，不重新设计——`docs/design/activation-convergence.md` 第 6 节明确写"触发轴，原样复用第 3-5 节的机制"。`status` 列因此与 `activation_links.status` 同构：`verified ⟺ 存在至少一条 tier ∈ {self_graded, trusted} 的观测条件`，`candidate ⟺` 不满足 verified 且触发轴对应的 KP 组合仍然有效，`deprecated ⟺` 与置信度无关的外部事实（见步骤 3「lifecycle 驱动的降权」，该处逻辑本身不变，只是落点从"离散跳变"改为"派生 status 直接写入"）。`bundle_auto_promote`/`TransitionBundle` 这两个由本节引入的概念随之作废——原因与 `activation.md`「与旧状态机的映射」下方 2026-08-13 编注完全相同：verified 不再是一次单独的、需要开关决定"自动还是人工确认"的跳变时刻，是持续判断的实时派生结果。下方「实现步骤」步骤 1/3 涉及 `TransitionBundle`/`bundle_auto_promote` 的部分维持原文不改写（同一贯做法，供读者对照 diff），实际以本编注为准；步骤 1 新增的 `RecordOutcome`/`RecordAuditOutcome` 对称方法见步骤 1 下方 2026-08-13 追加段落。

## 成员置信度：Bundle 独有的第二根轴（2026-08-13 新增）

`docs/design/activation-convergence.md` 第 6 节把 Bundle 的不确定性拆成两根独立的轴：**触发轴**（这句话该不该触发这条熟路）原样复用 `activation.md` 的机制，上面「状态机」的 2026-08-13 编注已经交代；**成员轴**（触发之后，这一组里到底是哪几个成员在真正撑着这次回答）是 Bundle 独有的，本节给出具体 schema。

**核心认识倒转**：此前的核心/路肩（member/fringe）是建 Bundle 或每轮显影扫描时，按"出现比例是否达到 `study.bundle_core_ratio_min`"算出来、写死成两个数组的静态标签——一个成员这一轮被算作核心，下一轮显影之前它的身份不变，即使这段时间它其实一次都没被真正引用过。这和 `activation_links` 改判前"一次晋升，之后只能整体升降"的问题是同一种结构：把一个连续变化的东西，定期折叠成一个离散标签。

**替换方案**：每一个成员知识点，在这个 Bundle 里维护一条自己的置信度分布，不再是"这一轮算下来它是核心还是路肩"这个静态判断，而是"这个成员至今为止，在这条 Bundle 被使用的历次记录里，有多大比例真的被引用了"这个持续更新的连续分数。

### Schema（`member_point_ids` 字段内 JSON 元素形状）

```json
{
  "point_id": "kp_xxx",
  "success_count": 12,
  "failure_count": 3,
  "last_seen_at": "2026-08-13T10:00:00Z"
}
```

字段含义与更新规则，逐一对照 `activation.md`「数据结构」`observed_conditions` 元素的 `success_count`/`failure_count` 设计，成员轴与条件轴用的是完全同一套 Beta 后验：

```text
success_count / failure_count：这个成员在 Bundle 被命中并走完流程的历次
  记录里，"这次答案是否真的引用了这个成员"的累计计数——引用=success++，
  未引用=failure++；证据来源见步骤 5「Trace 信号回写契约」2026-08-13
  编注（下方给出具体触发点，目前仍是「契约，非实现」，见「显式收窄」）；

mean(member) = (success_count+1) / (success_count+failure_count+2)
  —— 逐字复用 activation.md 的公式，Laplace 平滑、新成员从 0.5 起步；

分档：沿用 activation.md「服务分档」同一套三档判定（exploring/
  self_graded/trusted）与同一组配置项（serving_confidence_min 等，见
  retrieval.md），不为 Bundle 成员另开一组阈值——触发轴与成员轴面对的
  是同一类"这条证据信不信得过"的问题，用同一套语言判断，不重新发明；

组装时的用法（替代原「核心/路肩」的用法）：mean(member) 越过
  serving_confidence_min 的成员（即 tier ∈ {self_graded, trusted}）
  组装答案时视为必需内容（原"核心"位置）；未越过的（tier=exploring）
  可选择性带上或不带（原"路肩"位置）；不再有"路肩转正"这个搬家动作
  ——同一个 point_id 从"未过线"变成"过线"，只是它自己的 mean 涨过了
  serving_confidence_min，`member_point_ids` 数组本身不需要任何写操作
  之外的搬家，见「数据结构」`fringe_point_ids` 字段 2026-08-13 编注。
```

### 与显影扫描（步骤 2）出现比例的关系

步骤 2「核心计算」目前用的"出现比例 = 该 point_id 出现的样本 trace 数 / 样本 trace 总数"，回答的是"建 Bundle 或刷新核心这一刻，这个成员看起来重不重要"，是**初始化/离线批量重算**用的统计口径，不是**查询时**用的置信度——两者不冲突，分工不同：

```text
显影扫描（Study，周期批处理）：仍然按出现比例计算，用于 UpdateMembers
  写入初始/重算后的 success_count/failure_count 起点（新成员或历次
  批量重算，用"历史窗口内出现比例"折算出一对起始计数，具体折算公式—
  例如按窗口内 trace 总数与出现次数直接映射为 success_count=出现次数、
  failure_count=总数-出现次数——留给实现时确认，本文档只定成员置信度
  这根轴该存什么、该怎么用，不代为决定"用出现比例初始化置信度"这一步
  的具体折算系数）；

Bundle 被实际使用（Retrieval/Trace，查询时，阶段 2）：每次真实命中后，
  按"这次答案是否引用了这个成员"精确地对该成员的 success_count/
  failure_count 做增量更新（同 activation.md 的 RecordOutcome 语义），
  不是重新算一次比例；

两者共存的理由与 activation_links 的 enrichment/RecordOutcome 二元关系
  相同（见 trace.md 步骤 3 enrichment 与 RecordOutcome 的关系说明）：
  批量重算负责"从统计里发现/巩固初始判断"，精确增量负责"每次实际使用
  后的持续校准"，不是重复记两次同一件事。
```

### 显式收窄（与本文档已有的阶段划分保持一致，避免误读为已实现）

**Bundle 自己的 `bundle_success`/`bundle_failure` 信号写入，目前在代码里完全没有实现**（已单独确认，超出本次文档修订范围）——本节只规定"一旦这个信号存在，成员置信度应该怎么建模"，不实现、也不要求现在就实现这套信号写入。步骤 5「Trace 信号回写契约」本身仍然是**契约，不是实现指令**（该步骤原有措辞不变）；本节新增的 `success_count`/`failure_count` schema 与 mean/tier 公式，是这套信号一旦被写入之后，成员置信度该如何更新的规格，不因为信号还不存在就不该先把消费侧的形状定下来——同「阶段 1 可独立实现、阶段 2 只给契约」这个既有拆分的精神一致，本节属于阶段 2 契约的一部分，不是新增的阶段 3。

## 实现步骤

### 步骤 1：存储与内部接口

```text
CreateBundle(clusterFingerprint, representativeTerms, cond ObservedCondition,
             members, fringe, createdFrom) → bundle
  status=candidate；仅在步骤 2「显影扫描」的防御性 Match 复核确认无
  任何已有非 deprecated 熟路可匹配后才调用（2026-08-11 修订：不再靠
  UNIQUE(cluster_fingerprint) 冲突判断幂等，身份判断已经前移到调用方
  的 Match 复核这一步，本函数只管创建，不做去重判断）；调用方在防御性
  Match 复核（只查非 deprecated）之外，还需单独用同一套 Match 语义
  额外查一遍 deprecated 范围，命中则拒绝创建并记录日志（同 point_id
  的教训：被淘汰过的组合不自动复活，需人工/新累积信号显式处理，此处
  沿用同一处理方式）；

UpdateMembers(bundleID, members, fringe)：显影扫描每轮重新计算后写回，
  非状态迁移，不写 learning_results（同 UpdateConditions）；

UpdateConditions(bundleID, cond)：同 activation_links 的条件追加/刷新；

TransitionBundle(bundleID, to, reason, eventIDs)：合法迁移表同上；

UpdateStats(bundleID, adoptDelta, failDelta)；
TouchLastUsed(bundleIDs)。
```

### 步骤 2：显影扫描（Study 侧，周期调度新增一步；2026-08-12 定案排入 `study.md` 步骤 5b，位于步骤 5「晋升确认流」之后、步骤 6「gap 聚合与 Wiki/重编译信号」之前，理由见该文档步骤 5b 说明）

**身份判断方式（2026-08-11 修订，取代此前"先按四元组分组、分组指纹即身份"的口径）**：这类问题该不该算作同一条已有熟路，不再靠两次算出来的归一化四元组分组指纹是否相等来判断——四元组解析本身会漂移（同一个问题措辞不同可能解析出不同的四元组，这在 `subject_jitter_v1_disposition` 等既有记录里已确认），拿它当身份判断标准，会重蹈 `activation_links` 早年 `UNIQUE(question_terms, point_id)` 因问法抖动无法收敛的覆辙。改为逐条 trace 先尝试匹配已有熟路，匹配不上的才走分组去发现全新熟路：

```text
对窗口内（study.event_window_days，复用既有配置，不新增窗口）每条
retrieval_quality='confident' 且 direct_point_ids 非空 的 trace，逐条处理：

1. 匹配已有熟路：用该 trace 的四元组跑 activation.md 步骤 2 的 Match
   （复用同一套流程——硬性过滤 audience/constraint、精确匹配；
   2026-08-12 改判撤销"候选组非空且未命中时升级第二轮模型辅助匹配"
   的环节，Match 现在只有这一层，纯程序、不调 LLM），候选范围是
   全部非 deprecated 的熟路的 observed_conditions；

   命中某条熟路 → AppendObservedCondition（该四元组作为新变体追加）；
     该 trace 的 direct_point_ids 计入这条熟路的核心重算样本（步骤 2.3
     的比例计算，样本来源从"仅本轮分组"扩展为"这条熟路历次吸收到的
     全部样本"，样本随熟路存续持续累积，不因扫描周期重置）；
     处理下一条 trace；

   未命中 → 放入本轮待聚类池，处理下一条 trace；

2. 待聚类池处理完全部 trace 后，按归一化四元组分组（分组函数与
   `wiki.md` 步骤 8 第 1 步、`study.md`「问题复杂度观测量」三处共用
   同一个实现——这条不变，分组函数本身仍然有用，只是不再兼职当身份
   判断）；

   范围说明（与 wiki 步骤 8 第 1 步的差异不变）：主题候选聚类不要求
   confident、不要求 direct_point_ids 非空；熟路显影两者都要求；

3. 核心计算（对每个达标分组，或每条被吸收的已有熟路的累积样本）：
     出现比例 = 该 point_id 出现的样本 trace 数 / 样本 trace 总数
   （V1 范围说明：只统计 direct_point_ids，不统计 supporting 角色的
   实际引用——traces 表目前只持久化 direct_point_ids，supporting 命中
   只存在于单次问答的 EvidenceSet/answers.snapshot 里，没有可批量查询的
   列；引入 supporting 需要额外解析每条 trace 的快照，成本明显更高，
   V1 先只用已有的、和 ConfidentTraceQuadruples 同源的查询方式。
   这是一个 V1 范围裁剪，不是否定 supporting 未来值得计入）；

   出现比例 ≥ study.bundle_core_ratio_min（建议默认 0.5）→ 进 member；
   出现比例 > 0 但未达标 → 进 fringe；
   同时过滤 lifecycle=current 的 KP 与所属 KU（同 activation_links 的
   lifecycle 过滤口径）；

   > **2026-08-13 编注（本段"出现比例 ≥ bundle_core_ratio_min → 进
   > member/fringe"这条判定已被上方「成员置信度：Bundle 独有的第二根轴」
   > 一节取代，不删除本段是为了保留对照）**：核心/路肩不再是这里算出来
   > 就写死的静态标签，是每个成员自己的 `success_count`/`failure_count`
   > 持续算出的 `mean(member)`/tier（见该节「组装时的用法」）。本步骤这
   > 里的"出现比例"计算不再直接决定 member/fringe 归属，只负责给**新
   > 出现**的成员提供 `success_count`/`failure_count` 的初始种子值（该节
   > 「与显影扫描出现比例的关系」已给出示例折算：success_count=出现
   > 次数、failure_count=总数-出现次数）——种子值写入之后，member/fringe
   > 由 tier 持续判定，`bundle_core_ratio_min` 这个键本身随之失去独立
   > 判定用途，只在还没有实现新逻辑之前的过渡期作为回退值参考。

4. 待聚类池的分组达标判定（四项同时满足才新建；已被吸收进已有熟路的
   样本走步骤 3 重算核心 + UpdateMembers 刷新，不重复走这一步的新建
   判定）：
     distinct_question_count ≥ study.bundle_cluster_min_questions（建议默认 3，
       与 wiki.topic_cluster_min_questions 同量级，独立配置）；
     days_active ≥ study.bundle_cluster_min_days_active（建议默认 7，
       计算口径与 wiki.qualifying_min_days_active 相同，输入换成本分组
       traces.created_at）；
     member 非空；
     member 大小 ≤ study.bundle_core_size_max（建议默认 8）——超过说明
       这不是一类问题、是几类问题被分组函数混在一起了，不建 bundle，
       改记录信号（见下「核心过宽」）；

   **2026-08-13 编注**：这四项创建门槛不套用 Beta 均值/宽度公式（对照
   `activation.md` 的链接创建门槛，见 `study.md` 步骤 1 2026-08-13 修订）
   ——`docs/design/activation-convergence.md` 第 11 节明确了理由：
   `distinct_question_count`/`days_active` 问的是"这个组合是不是真的
   反复、持续出现过"，是多样性/时间跨度问题，跟"这个组合可不可靠"是
   两件不同的事，不该被并入置信度框架；`member 非空`/`member 大小上限`
   是结构性检查，同样不是可靠性判断。四项原样保留。

5. 待聚类池中达标的分组 → CreateBundle 前做一次防御性 Match 复核
   （防止并发扫描或本轮内分组之间产生的竞态，正常单线程 Ticker 调度下
   极少触发）：仍未匹配到任何已有熟路 → CreateBundle，写
   learning_results(action=bundle_create, status=applied 或
   pending_confirm，取决于 bundle_auto_promote，event_ids=trace_id 列表)；
   复核后发现已被匹配 → 按步骤 3 走吸收流程，不重复建。

已被吸收的已有熟路：UpdateMembers + UpdateConditions（刷新，不写
  learning_results——条件/成员收敛是持续性维护动作，同 activation_links
  的 UpdateConditions 语义）。

新旧两套机制的分工：分组函数仍然是"从零发现一条全新熟路"的唯一手段，
  这一步骤本身依赖的解析仍可能抖动、仍可能把同一类问题的初次显影拆成
  两条（`wiki.md` 主题候选聚类面对的是同一个未解决的根因，`subject_jitter_
  v1_disposition` 记录的既定态度是"V1 不修，靠积累机制吸收"，这里同样
  接受这个残留风险，不假装已解决）；但一旦某条熟路显影出来，后续证据
  全部走步骤 1 的 Match 吸收，不会再因为解析抖动分裂——风险范围从
  "每次抖动都可能分裂"收窄到"只有第一次显影那一刻的抖动才可能分裂"。

核心过宽（达标但 member 超过 bundle_core_size_max）：不建 bundle，
  写入学习报告新增节 bundle_scope_too_wide（分组四元组摘要、
  候选核心大小、代表问法）——同 wiki.md 步骤 3「内聚不达标」时写
  entry_split_signals 的既有处理方式：只报告、不建实体、不驱动任何
  自动动作，供人工判断这类问题是否需要先被拆解（复杂问题拆解本身
  不在 V1，见 CLAUDE.md「复杂问题拆解不在 V1」）。

达标但 member 为空（有人反复问、但从未拼出稳定组合）：不新建机制，
  这类信号已经由现有 activation_gap / knowledge_gaps 覆盖
  （path_type=full 且 confident 时已经在写 activation_gap，见 trace.md
  步骤 3），本步骤不重复处理。
```

### 步骤 3：巩固与状态迁移（Study 侧；窗口统计判定依赖阶段 2 的回写信号，lifecycle 驱动的降权不依赖，阶段 1 即生效，见下）

```text
窗口统计（同 activation_links 步骤 3 的结构，对象换成 bundle_id）：
  success_n  = 窗口内 bundle_success 事件数（阶段 2 产生，见下）
  distinct_n = 不同 question_hash 数
  failure_n  = 窗口内 bundle_failure 事件数

candidate：success_n ≥ study.bundle_promote_success_min 且
  distinct_n ≥ study.bundle_promote_distinct_min →
  bundle_auto_promote=true 时直接 TransitionBundle(candidate → verified)；
  false 时走 pending_confirm 流程（同 activation_links 步骤 5）；

verified：failure_n ≥ study.bundle_weaken_failure_min 且
  failure_n/(success_n+failure_n) ≥ study.bundle_weaken_ratio_min →
  TransitionBundle(verified → weakened)；

weakened：success_n ≥ study.bundle_reverify_success_min 且 failure_n==0 →
  TransitionBundle(weakened → verified)；

闲置淘汰：同 activation_links 步骤 4，阈值独立配置
  （study.bundle_candidate_idle_days / bundle_deprecate_idle_days）。

在阶段 2（Retrieval 消费 + Trace 回写）落地之前，bundle_success /
bundle_failure 事件不会产生，窗口统计判定部分对已显影的 bundle 空转——
bundle 会停留在 candidate，仅靠步骤 2 的 UpdateMembers 持续刷新核心成员，
这是预期行为，不是 bug：阶段 1 独立可上线的前提就是「先能被看见和记录，
用不用它是下一步的决定」。

lifecycle 驱动的降权（2026-08-12 定案，取代 lifecycle.md 步骤 4 那条
「是否需要对熟路生成降权信号，尚未定案」的开放问题；**这条不依赖阶段 2
的事件，阶段 1 就应当生效**——它的输入是 lifecycle 变更通知，不是
bundle_success/bundle_failure）：

某个 point_id 的 lifecycle 变为非 current（SetUnitLifecycle 触发）时：
  该 point_id 在某条非 deprecated 熟路的 member_point_ids（核心）里
    → 立即 TransitionBundle(verified → weakened)（若当前状态是
      verified；candidate 状态则不迁移，只是下一轮 UpdateMembers
      时该 point 自然被 lifecycle 过滤掉，不再计入核心/路肩候选）；
      单次事件即触发，不经过窗口统计——理由同 activation_links 步骤 4
      「Study 感知」对 verified 链接的降权处理：核心成员少了一个，
      这条熟路当下能不能答对已经不确定，应当立刻降级观察，而不是
      继续按窗口内旧信号维持 verified；
  该 point_id 只在某条熟路的 fringe_point_ids（路肩）里
    → 不触发任何状态迁移；下一轮 UpdateMembers 因为 lifecycle 过滤，
      这个 point 自然从候选样本里消失，路肩列表下一轮就会不含它；
  weakened 之后的恢复路径与窗口统计判定共用同一条（reverify），
    不单独为 lifecycle 触发的降权设一条特殊恢复规则。

**全灭兜底（2026-08-17 补充）**：以上规则只检查核心成员，对"一条熟路里
全部成员从未攒够核心门槛、清一色是路肩"这种情况没有覆盖——路肩过期不触发
迁移，于是这条熟路的全部知识点都已经 lifecycle 非 current 时，仍然会一直
挂着 verified，界面上无法区分它和真正健康的熟路。补一条独立判据，与上面的
"核心过期"判据并列（不是替换）：一条 verified 熟路的**全部成员**（不分
核心/路肩）都已 lifecycle 非 current 时，同样立即置 deprecated。触发时机、
恢复路径（走 reverify）与上面核心过期的处理完全一致，只是判定条件从"任一
核心过期"换成"全部成员过期"。`internal/study/bundle_scan.go`
`weakenBundlesWithExpiredCoreMembers` 已实现这条兜底。

熟路成员变化本身**不**触发任何 Wiki 侧动作（不产生页面 needs_recompile
通知）：页面重编译只认 KP 自身的 lifecycle 变化（见 lifecycle.md 步骤 4a
「lifecycle 传导」，扫描 published 页面 source_point_ids 命中即触发，
与该 KP 是否也是某条熟路的成员完全无关，已经覆盖了这个场景）；纯粹的
使用漂移（成员因为最近没被引用够而掉出核心，KP 自身 lifecycle 没变）
更不该触发 Wiki 通知——那和这个 KP 是否还可靠没有关系，通知了就是
误触发。不要为熟路单独加一条通知 Wiki 的路径，会和 lifecycle.md 步骤
4a 已有的机制重复触发同一件事。
```

### 步骤 4：匹配器契约（预期，供 retrieval.md 未来接入）

```text
Match(query ExpandedQuery) → []BundleMatch{bundle, memberPointIDs, score}

复用 activation.md 步骤 2 的 Match 语义：观测条件组、组内精确匹配、
组间 OR、**硬性过滤 + 单级程序精确匹配（2026-08-12 改判，取代
2026-08-11「两级、含模型辅助匹配」的口径——四个字段同一层级精确
相等，subject 不再单独享受同义词归一化/模糊匹配）**，逐字复用同一套
函数，只是候选集合从 activation_links 换成 activation_bundles；
Match 全程不调用模型，不存在需要为 bundle 单独设计的模型调用成本
控制或触发范围收紧；

候选加载：status ∈ {verified, candidate}，member_point_ids 内的 KP 与
所属 KU 均要求 lifecycle=current（同 point 级过滤口径）；

命中后：取 member_point_ids → 反查各自 unit_id → 构建 direct 候选，
**不触发** retrieval.md 现有的"跨 unit 视为歧义、回落慢路径"判定
（`plan-parser-vocab-and-unit-ambiguity.md` 那条规则针对的是零散多条
ActivationLink 偶然同时命中、临时拼接的场景；bundle 的成员组合是历史上
确实一起被反复验证过的整体，不是临时拼凑，语义上不构成"歧义"）；

命中优先级（预期，待 retrieval.md 实际修订时确认）：
  Wiki 直答 → 熟路 Match → 单链接 Match → 慢路径
  ——熟路排在单链接之前，因为它是更贵的组合投入，能用则优先用，
  用不上时单链接照样兜底简单问题；

命中后仍需经过现有的证据挖掘、fast_verify 快路径校验——熟路只决定
"这一次该优先看哪些材料"，不跳过任何一道既有的正确性把关。
```

本步骤是**契约**，不是实现指令——retrieval.md 的检索总流程、优先级顺序、`EvidenceSet.path_type` 取值范围等实际改动，留给 retrieval.md 自己的修订去做，本文档不代为修改该文档。

**2026-08-12 部分实现，修正上面的命中优先级预期**：实际接入点不是"Wiki 直答 → 熟路 Match → 单链接 Match → 慢路径"这个独立优先级层，而是**只在 ActivationLink 的 Match 结果出现跨 unit 歧义时才 consult Bundle**（`retrieval/fastpath_helpers.go` `resolveBundleForAmbiguousHits`，见 `retrieval.md` 步骤 2 对应记录）——Bundle 不是一个独立于单链接之前、优先尝试的命中层，而是单链接歧义时的兜底/仲裁：多个 verified Bundle 同时命中时用 KPN `contradicts` 判冲突（冲突就还是回落慢路径，不合并），不冲突则合并核心成员继续走快路径；一个 verified Bundle 都没覆盖时，从这次观测实时新建/加强一条 candidate Bundle（不是等 Study 离线聚类），仍回落慢路径。这个范围比本步骤原先设想的"Bundle 优先于单链接"要窄，只解决了"多链接歧义时怎么办"这一个入口，不是完整的阶段 2——`bundle_hits[]` 独立字段、Trace 的 `bundle_success`/`bundle_failure`、Bundle 自己的 `adopt_count`/`known_question_terms`/`auto_promote`（本文档步骤 5/6 契约）仍未实现，命中优先级的完整口径待这些补齐后再回来核对是否需要采纳"Bundle 优先于单链接"这个原始设想。

### 步骤 5：Trace 信号回写契约（预期，供 trace.md 未来接入）

```text
一次问答命中了某条 bundle 并走完流程后：
  member_point_ids 中，本次答案实际引用的比例 ≥
    study.bundle_core_ratio_min（复用同一阈值，双重含义：既用于判定
    "谁该进核"，也用于判定"这次命中，核心是否被验证有效"）
    → 写 bundle_success 事件（payload: bundle_id, cited_ratio）；
  比例低于阈值，或 fast_verify 判定不充分导致回落慢路径
    → 写 bundle_failure 事件；

答案引用了 fringe_point_ids 里的某个成员且 confident
    → 该成员在 fringe 里的命中计数 +1，达到 study.bundle_core_ratio_min
    对应的次数后，下一轮显影扫描（步骤 2）会自然把它算进 member
    （不需要单独的"路肩转正"事件，步骤 2 每轮都会重新计算比例）；

同一次问答不产生 activation_success/failure（那是 ActivationLink 的
事件），两套事件并存、各自驱动各自对象的状态机，互不覆盖。
```

> **2026-08-13 编注**：以上段落沿用了离散核心/路肩标签的旧措辞（"进核"、
> "路肩转正"），维持原文不改写（同「状态机」一节的处理方式，供 diff 对照）。
> 按「成员置信度：Bundle 独有的第二根轴」一节的规格，这套契约的实际形状是：
> 一次命中走完流程后，对 `member_point_ids` 里的**每一个**成员（不是只看
> 聚合比例）分别判定"这次答案是否引用了它"，逐个调用与 `activation.
> RecordOutcome` 对称的方法（更新该成员的 `success_count`/`failure_count`，
> 命名建议 `bundle.RecordMemberOutcome`，供 trace.md 阶段 2 实现时参考）；
> `bundle_success`/`bundle_failure`（触发轴事件，判定 Bundle 这次命中本身
> 该不该被信任）与成员级的逐个 outcome 更新是两件独立的事，同一次命中会
> 同时产生一条触发轴事件与最多 `|member_point_ids|` 条成员级更新，互不
> 替代——同 `activation_success`（条件是否可信）与 KP 本身的关系不是一回事
> 这个既有分工一致。不再有"路肩转正"这个单独的事件类型——一个成员的 mean
> 涨过服务门槛，是它自己的 `success_count`/`failure_count` 持续更新的自然
> 结果，不需要步骤 2 显影扫描帮它"转正"，见前一节「组装时的用法」。
同样是契约，不是对 `trace.md` 的代为修改。

### 步骤 6：只读观测 API

```text
GET  /activation-bundles
  查询参数：status、limit（默认 50）、offset
  响应：[{ bundle_id, representative_terms, member_point_ids,
           fringe_point_ids, status, adopt_count, fail_count,
           last_used_at, created_at }]

GET  /activation-bundles/:id
  响应：完整字段 + created_from + member/fringe 各自的 point_summary
        （JOIN knowledge_points 补充展示）

GET  /activation-bundles/:id/questions
  响应：{ matched: [{question, trace_id, created_at}] }
  —— 命中过这条 bundle 的问答原文，用于人工判断这条熟路是否合理
```

**不提供 confirm/reject**：bundle 默认自动晋升（见「状态机」，现在与 ActivationLink 默认值一致），没有需要人工确认的 pending 状态；`bundle_auto_promote=false` 时才会产生 `pending_confirm` 的 `learning_results`，此时复用 `activation_links` 现有的 `POST /learning-results/:id/confirm` 通用确认入口（如果该入口存在），不为 bundle 单独开一套确认 API。**待确认**：是否已有这样的通用入口，还是需要新增。

### 步骤 7：与 Wiki 的接入点

`wiki.md` 已经在步骤 3（qualifying 判定、连贯、内聚）、步骤 8（四元组聚类、候选范围检索、整体可靠度）留了指向本文档的指针，均标注为"设计层面，未定案"。本模块提供的是这些指针需要的原始能力（存储、显影、匹配契约），具体怎么消费——qualifying 是否放宽、连贯/内聚要不要把熟路稳定核并入连接来源、放宽到什么口径、可靠度公式怎么叠加——由 `wiki.md` 自己的后续修订决定，不在本文档展开，避免同一件事在两份文档里各说一半、互相不同步。

其中「连贯」与「内聚」两处的接入方式和 qualifying/候选范围检索不同，值得单独提一句：qualifying 等三处是"熟路补上一条独立的证据来源"，连贯/内聚是"把熟路稳定核当成一种边/连接，并入本来就已经在用的图结构"——内聚判定现在的 Louvain 边权已经在混合 KPN 关系与临时统计的共享 confident 问题共现，这本身就是熟路想要形式化的同一种信号的雏形，是这两处天然的先例，接入时预期是替换掉那部分临时统计，而不是新增一条平行判据。

## 依赖

```text
基础设施：SQLite（migration）、结构化日志
LLM：     LLMClient interface（2026-08-11 新增依赖，与 activation.md
          共用同一个 Match 实现与同一份 Prompt，见 activation.md 步骤 2；
          本模块不单独维护一份 Prompt 或调用逻辑）
Trace：   复用问题归一化/分词/停用词共享包（同 activation.md 依赖）；
          阶段 2 需要 trace.md 新增 bundle_success/bundle_failure
          事件产生逻辑（本文档只给契约）
Lifecycle：匹配器 JOIN knowledge_points.lifecycle 过滤，同 activation.md
Study：   显影扫描、状态迁移判定的唯一调用方；四元组分组函数与
          wiki.md 步骤 8、study.md 问题复杂度观测量三处共用一份实现，
          不要各自重复实现
Activation：Match 语义、observed_conditions 结构、状态机迁移表均复用
          activation.md 已有实现，建议存储与匹配器代码直接放在
          internal/activation 包内新增文件，而不是新建顶层模块——
          **待确认**：是否认可放在同一个包，还是希望作为独立模块
Retrieval / Trace：阶段 2 的实际接入是这两个模块各自的后续修订，
          本文档不代为修改
```

## 完成标准（分阶段）

```text
阶段 1（可独立验收，不依赖 retrieval/trace 修订）：
  migration 建表成功；cluster_fingerprint 上只有非唯一索引，不承担
    去重职责（2026-08-11 修订）；
  身份判断：同一类问题两次显影扫描、四元组解析结果逐字相同时不产生
    两条重复熟路——第二次的 trace 应通过 Match（2026-08-12 改判后为
    纯程序精确匹配，不再涉及模型辅助判断）被吸收进第一次创建的那条，
    UpdateMembers 命中、CreateBundle 不被调用（测试用例范围相应收窄：
    不再模拟"语义等价但字面不同"的变体被模型判为同一条熟路这种场景，
    因为 Match 不再具备这种判断能力，抖动导致的漏吸收改由挖掘/诊断
    链路观测，不在 Match 层面兜底）；
  CreateBundle 只在 Match 复核（含 deprecated 范围的复核）确认无同源
    熟路时才被调用，对已存在 deprecated 的同源熟路拒绝创建并记录日志；
  显影扫描：四元组分组函数与 wiki.md/study.md 复用同一实现（不是各自
    重复一份），且只用于"发现全新熟路"、不再兼职身份判断；核心/路肩
    比例计算正确（样本随熟路存续累积，不因扫描周期重置）；四项达标
    判定（问法数/天数/核非空/核不超限）全部生效；核心过宽时写
    bundle_scope_too_wide 报告节、不建 bundle；
  （原「模型辅助匹配」验收项已删除，2026-08-12 改判后 Match 不再
    调用模型，不存在需要验收的模型调用触发/失败处理路径）；
  UpdateMembers 每轮刷新，member/fringe 集合变化时正确更新，未变化
    时不空转 touch updated_at；
  TransitionBundle 拒绝迁移表之外的一切迁移；
  bundle_auto_promote 两种取值下的晋升路径均可通过测试验证
    （因为阶段 1 没有真实 bundle_success 事件来源，测试需要直接构造
    learning_events 或走内部接口模拟窗口统计）；
  只读 API 正确返回数据；
  fake 环境下全部路径测试稳定运行。

阶段 2（依赖 retrieval.md / trace.md 各自完成对应修订后才能验收）：
  Match 契约按 activation.md 同构语义实现（触发轴，2026-08-13 起复用
    连续置信度公式与三档分档，不再有 weakened 中间态）；
  跨 unit 命中不再触发歧义回落（仅限 bundle 命中路径）；
  触发轴事件（`bundle_success`/`bundle_failure`）与成员轴逐成员的
    `RecordMemberOutcome`（暂定命名，见步骤 5 2026-08-13 编注）均正确
    产生/调用，Bundle 的派生 `status` 按 activation_links 同构规则
    （verified ⟺ 存在 ≥1 条 tier∈{self_graded,trusted} 的观测条件）
    实时反映；不再有 candidate → verified → weakened → verified/
    deprecated 这条离散跳变路径需要验收（weakened 已退休）；
  member_point_ids 内每个成员的 mean(member) 越过 serving_confidence_min
    后即视为必需内容，不需要等待下一轮显影扫描"转正"（测试用例：单次
    RecordMemberOutcome 调用后立即查询该成员的 tier 变化，不依赖
    Study 周期任务）；
  成员置信度 schema（success_count/failure_count/mean/tier）本身独立于
    上面两条是否已实现——这是本文档 2026-08-13 新增的规格，一旦
    bundle_success/bundle_failure 信号写入落地，消费方按本规格实现即可，
    不需要另行设计。
```
